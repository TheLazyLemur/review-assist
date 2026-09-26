// Package bitbucket implements pr.CodeHost for Bitbucket Cloud, over its REST
// API and, for checkout, git.
package bitbucket

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

// Client talks to one repository. repo.Owner is the workspace, repo.Name the
// repository slug.
type Client struct {
	http    *http.Client
	baseURL string
	email   string
	token   string
	repo    pr.Repo
	opts    Options
}

var _ pr.CodeHost = (*Client)(nil)

// Options are what the actions outside the API need. Checkout and
// OpenInBrowser fail naming an option that is unset.
type Options struct {
	// Remote is the git remote review-assist picked; Checkout fetches from it.
	Remote string
	// Dir is the working directory Checkout runs git in.
	Dir string
	// Open opens a URL in the browser.
	Open func(url string) error
	// PollInterval is the wait between polls of a merge task; zero means a
	// second.
	PollInterval time.Duration
	// MergeWait caps how long Merge waits for a merge task; zero means
	// defaultMergeWait.
	MergeWait time.Duration
}

// defaultMergeWait bounds the poll on its own: the TUI acts with a context
// that never ends, and a task stuck at PENDING would keep it busy for good.
const defaultMergeWait = 5 * time.Minute

// NewClient takes the API root, https://api.bitbucket.org/2.0 in production,
// and an Atlassian account email with an API token.
func NewClient(baseURL, email, token string, repo pr.Repo, opts Options) *Client {
	hc := &http.Client{Timeout: 60 * time.Second, CheckRedirect: checkRedirect}
	if opts.PollInterval == 0 {
		opts.PollInterval = time.Second
	}
	if opts.MergeWait == 0 {
		opts.MergeWait = defaultMergeWait
	}
	return &Client{http: hc, baseURL: strings.TrimSuffix(baseURL, "/"), email: email, token: token, repo: repo, opts: opts}
}

// maxRedirects is net/http's own cap, which a CheckRedirect replaces.
const maxRedirects = 10

// checkRedirect refuses to follow a write: net/http re-sends a redirected
// POST as a GET without its body, and the GET's 200 would pass for success.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if orig := via[0]; orig.Method != http.MethodGet {
		return fmt.Errorf("refusing to follow a redirect of %s %s to %s", orig.Method, orig.URL, req.URL)
	}
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	return nil
}

// ---- wire types: Bitbucket JSON field names stay in this file ----

type userDTO struct {
	Nickname string `json:"nickname"`
}

type branchRefDTO struct {
	Branch struct {
		Name string `json:"name"`
	} `json:"branch"`
	Commit struct {
		Hash string `json:"hash"`
	} `json:"commit"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

type summaryDTO struct {
	ID          int          `json:"id"`
	Title       string       `json:"title"`
	Author      userDTO      `json:"author"`
	Source      branchRefDTO `json:"source"`
	Destination branchRefDTO `json:"destination"`
	Draft       bool         `json:"draft"`
	State       string       `json:"state"`
	UpdatedOn   time.Time    `json:"updated_on"`
}

func (d summaryDTO) toDomain() (pr.Summary, error) {
	state, err := domainState(d.State)
	if err != nil {
		return pr.Summary{}, fmt.Errorf("pull request %d: %w", d.ID, err)
	}
	return pr.Summary{
		Number: d.ID, Title: d.Title, Author: d.Author.Nickname,
		HeadRef: d.Source.Branch.Name, BaseRef: d.Destination.Branch.Name,
		IsDraft: d.Draft, State: state, UpdatedAt: d.UpdatedOn,
	}, nil
}

func domainState(s string) (string, error) {
	switch s {
	case "OPEN", "MERGED":
		return s, nil
	case "DECLINED", "SUPERSEDED":
		return "CLOSED", nil
	}
	return "", fmt.Errorf("unknown Bitbucket pull request state %q", s)
}

var bitbucketStates = map[pr.State][]string{
	pr.Open:   {"OPEN"},
	pr.Merged: {"MERGED"},
	pr.Closed: {"DECLINED", "SUPERSEDED"},
	pr.All:    {"OPEN", "MERGED", "DECLINED", "SUPERSEDED"},
}

type pullRequestDTO struct {
	summaryDTO
	Description string    `json:"description"`
	CreatedOn   time.Time `json:"created_on"`
	Links       struct {
		HTML struct {
			Href string `json:"href"`
		} `json:"html"`
	} `json:"links"`
	Participants []participantDTO `json:"participants"`
}

type participantDTO struct {
	User           userDTO   `json:"user"`
	State          *string   `json:"state"`
	ParticipatedOn time.Time `json:"participated_on"`
}

var decisions = map[string]pr.Decision{"approved": pr.Approve, "changes_requested": pr.RequestChanges}

type idDTO struct {
	ID int64 `json:"id"`
}

type commentDTO struct {
	ID      int64 `json:"id"`
	Content struct {
		Raw string `json:"raw"`
	} `json:"content"`
	User      userDTO   `json:"user"`
	CreatedOn time.Time `json:"created_on"`
	Deleted   bool      `json:"deleted"`
	Pending   bool      `json:"pending"`
	Parent    *idDTO    `json:"parent"`
	Inline    *struct {
		Path      string `json:"path"`
		From      *int   `json:"from"`
		To        *int   `json:"to"`
		StartFrom *int   `json:"start_from"`
		StartTo   *int   `json:"start_to"`
		Outdated  bool   `json:"outdated"`
	} `json:"inline"`
}

// lineOnSide reads a Bitbucket from/to pair. When both are set the line is
// context, which the domain anchors on the head side.
func lineOnSide(from, to *int) (int, diff.Side) {
	switch {
	case to != nil:
		return *to, diff.Head
	case from != nil:
		return *from, diff.Base
	}
	return 0, ""
}

func (d commentDTO) toDomain() (pr.Comment, error) {
	c := pr.Comment{ID: d.ID, Author: d.User.Nickname, Body: d.Content.Raw, CreatedAt: d.CreatedOn}
	if d.Parent != nil {
		c.ReplyTo = d.Parent.ID
	}
	if d.Inline != nil {
		if d.Inline.Path == "" {
			return pr.Comment{}, fmt.Errorf("comment %d: inline without a path", d.ID)
		}
		a := &pr.Anchor{Path: d.Inline.Path}
		a.Line, a.Side = lineOnSide(d.Inline.From, d.Inline.To)
		start, startSide := lineOnSide(d.Inline.StartFrom, d.Inline.StartTo)
		if start > 0 && a.Line == 0 {
			return pr.Comment{}, fmt.Errorf("comment %d: range start without an end line", d.ID)
		}
		if start > 0 && (start != a.Line || startSide != a.Side) {
			a.StartLine, a.StartSide = start, startSide
		}
		c.Anchor = a
	}
	return c, nil
}

type newCommentDTO struct {
	Content struct {
		Raw string `json:"raw"`
	} `json:"content"`
	Inline *newInlineDTO `json:"inline,omitempty"`
	Parent *idDTO        `json:"parent,omitempty"`
}

type newInlineDTO struct {
	Path string `json:"path"`
	From *int   `json:"from,omitempty"`
	To   *int   `json:"to,omitempty"`
}

func newInline(a pr.Anchor) (*newInlineDTO, error) {
	if a.StartLine != 0 {
		return nil, fmt.Errorf("a comment on a range of lines is not supported on Bitbucket: %+v", a)
	}
	in := &newInlineDTO{Path: a.Path}
	if a.Line == 0 {
		return in, nil
	}
	line := a.Line
	switch a.Side {
	case diff.Head:
		in.To = &line
	case diff.Base:
		in.From = &line
	default:
		return nil, fmt.Errorf("comment anchor has no valid side: %+v", a)
	}
	return in, nil
}

// A binary file's lines are null, which decodes as zero.
type diffstatDTO struct {
	LinesAdded   int `json:"lines_added"`
	LinesRemoved int `json:"lines_removed"`
}

type mergeDTO struct {
	MergeStrategy string `json:"merge_strategy"`
}

type taskStatusDTO struct {
	TaskStatus string `json:"task_status"`
}

type draftDTO struct {
	Draft bool `json:"draft"`
}

type page[T any] struct {
	Values []T    `json:"values"`
	Next   string `json:"next"`
}

type errorDTO struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

// ---- reads ----

// Viewer is the account's nickname because that is the field a pull
// request's Author carries, which pr.IsOwn compares; display_name is not
// unique.
func (c *Client) Viewer(ctx context.Context) (string, error) {
	var u userDTO
	if err := c.get(ctx, c.baseURL+"/user", &u); err != nil {
		return "", err
	}
	return u.Nickname, nil
}

// listLimit matches the GitHub adapter's list.
const listLimit = 100

func (c *Client) List(ctx context.Context, state pr.State) ([]pr.Summary, error) {
	states, ok := bitbucketStates[state]
	if !ok {
		return nil, fmt.Errorf("unknown pull request state %q", state)
	}
	query := url.Values{"state": states, "sort": {"-updated_on"}}
	dtos, err := getAll[summaryDTO](ctx, c, c.repoPath("pullrequests"), query, listLimit)
	if err != nil {
		return nil, err
	}
	prs := make([]pr.Summary, 0, len(dtos))
	for _, d := range dtos {
		s, err := d.toDomain()
		if err != nil {
			return nil, err
		}
		prs = append(prs, s)
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxDiffstatsInFlight)
	for i := range prs {
		g.Go(func() error { return c.addCounts(gctx, &prs[i]) })
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return prs, nil
}

// maxDiffstatsInFlight bounds List's diffstat requests, one per pull request
// and up to listLimit of them.
const maxDiffstatsInFlight = 8

// addCounts fills the line and file counts from the diffstat, because
// Bitbucket's pull request carries none.
func (c *Client) addCounts(ctx context.Context, s *pr.Summary) error {
	files, err := getAll[diffstatDTO](ctx, c, c.pullRequestPath(s.Number, "/diffstat"), nil, 0)
	if err != nil {
		return fmt.Errorf("%w (diffstat of pull request %d)", err, s.Number)
	}
	for _, f := range files {
		s.Additions += f.LinesAdded
		s.Deletions += f.LinesRemoved
	}
	s.ChangedFiles = len(files)
	return nil
}

func (c *Client) Get(ctx context.Context, number int) (*pr.PR, error) {
	var d pullRequestDTO
	if err := c.get(ctx, c.baseURL+c.pullRequestPath(number, ""), &d); err != nil {
		return nil, err
	}
	summary, err := d.summaryDTO.toDomain()
	if err != nil {
		return nil, err
	}
	p := &pr.PR{
		Summary: summary, Body: d.Description, URL: d.Links.HTML.Href,
		HeadSHA: d.Source.Commit.Hash, BaseSHA: d.Destination.Commit.Hash, CreatedAt: d.CreatedOn,
	}
	if err := c.addCounts(ctx, &p.Summary); err != nil {
		return nil, err
	}
	for _, part := range d.Participants {
		// Null: a reviewer who has not decided yet, or someone who only commented.
		if part.State == nil {
			continue
		}
		dec, ok := decisions[*part.State]
		if !ok {
			return nil, fmt.Errorf("pull request %d: unknown participant state %q", number, *part.State)
		}
		p.Verdicts = append(p.Verdicts, pr.Verdict{Author: part.User.Nickname, Decision: dec, At: part.ParticipatedOn})
	}
	return p, nil
}

// Diff relies on net/http following Bitbucket's redirect with the
// Authorization header, which it keeps on a redirect to the same host or one
// of its subdomains and drops otherwise.
func (c *Client) Diff(ctx context.Context, number int) (string, error) {
	body, err := c.do(ctx, http.MethodGet, c.baseURL+c.pullRequestPath(number, "/diff"), nil)
	return string(body), err
}

// Comments leaves out deleted comments and the viewer's pending drafts.
func (c *Client) Comments(ctx context.Context, number int) ([]pr.Comment, error) {
	dtos, err := getAll[commentDTO](ctx, c, c.pullRequestPath(number, "/comments"), nil, 0)
	if err != nil {
		return nil, err
	}
	var out []pr.Comment
	for _, d := range dtos {
		// An outdated comment's lines belong to an older commit, so against
		// this diff it would sit on the wrong line.
		if d.Deleted || d.Pending || (d.Inline != nil && d.Inline.Outdated) {
			continue
		}
		cm, err := d.toDomain()
		if err != nil {
			return nil, err
		}
		out = append(out, cm)
	}
	return out, nil
}

// ---- writes ----

var verdictPaths = map[pr.Decision]string{pr.Approve: "/approve", pr.RequestChanges: "/request-changes"}

// SubmitVerdict posts a message as a comment on the pull request before the
// verdict, because Bitbucket's verdict endpoints take no message.
func (c *Client) SubmitVerdict(ctx context.Context, number int, decision pr.Decision, message string) error {
	path, ok := verdictPaths[decision]
	if !ok {
		return fmt.Errorf("unknown decision %q", decision)
	}
	if strings.TrimSpace(message) == "" {
		_, err := c.do(ctx, http.MethodPost, c.baseURL+c.pullRequestPath(number, path), nil)
		return err
	}
	if err := c.PostComment(ctx, number, pr.NewComment{Body: message}); err != nil {
		return err
	}
	if _, err := c.do(ctx, http.MethodPost, c.baseURL+c.pullRequestPath(number, path), nil); err != nil {
		return fmt.Errorf("the comment was posted, but the verdict failed: %w", err)
	}
	return nil
}

func (c *Client) PostComment(ctx context.Context, number int, nc pr.NewComment) error {
	var d newCommentDTO
	d.Content.Raw = nc.Body
	if nc.Anchor != nil {
		in, err := newInline(*nc.Anchor)
		if err != nil {
			return err
		}
		d.Inline = in
	}
	return c.postComment(ctx, number, d)
}

// Reply sends parent.ID, not ReplyTo: Bitbucket nests a reply under the
// comment it answers, where GitHub threads every reply under the root.
func (c *Client) Reply(ctx context.Context, number int, parent pr.Comment, body string) error {
	var d newCommentDTO
	d.Content.Raw = body
	d.Parent = &idDTO{ID: parent.ID}
	return c.postComment(ctx, number, d)
}

func (c *Client) DeleteComment(ctx context.Context, number int, cm pr.Comment) error {
	_, err := c.do(ctx, http.MethodDelete, c.baseURL+c.pullRequestPath(number, "/comments/"+strconv.FormatInt(cm.ID, 10)), nil)
	return err
}

func (c *Client) postComment(ctx context.Context, number int, d newCommentDTO) error {
	_, err := c.do(ctx, http.MethodPost, c.baseURL+c.pullRequestPath(number, "/comments"), d)
	return err
}

var mergeStrategies = map[pr.MergeMethod]string{
	pr.MergeCommit: "merge_commit",
	pr.Squash:      "squash",
	pr.Rebase:      "rebase_fast_forward",
}

// Merge waits for a merge Bitbucket queues as a task, so a nil error means
// the pull request is merged.
func (c *Client) Merge(ctx context.Context, number int, m pr.MergeMethod) error {
	strategy, ok := mergeStrategies[m]
	if !ok {
		return fmt.Errorf("unknown merge method %q", m)
	}
	resp, _, err := c.send(ctx, http.MethodPost, c.baseURL+c.pullRequestPath(number, "/merge"), mergeDTO{MergeStrategy: strategy})
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusAccepted {
		return nil
	}
	task, err := resp.Location()
	if err != nil {
		return fmt.Errorf("merge of pull request %d was queued with no task to poll: %w", number, err)
	}
	return c.awaitMerge(ctx, task.String())
}

// awaitMerge polls a merge task. A failed merge answers the poll with an
// error status, which get returns with Bitbucket's reason.
func (c *Client) awaitMerge(ctx context.Context, taskURL string) error {
	// taskURL comes from a response header; the credentials must not follow
	// it to another host.
	if !strings.HasPrefix(taskURL, c.baseURL+"/") {
		return fmt.Errorf("merge task %q is outside %s", taskURL, c.baseURL)
	}
	ctx, cancel := context.WithTimeout(ctx, c.opts.MergeWait)
	defer cancel()
	for {
		var s taskStatusDTO
		if err := c.get(ctx, taskURL, &s); err != nil {
			// The cap can expire mid-request, where net/http reports only the
			// deadline.
			if ctx.Err() != nil {
				return fmt.Errorf("merge still pending: %w (%s)", ctx.Err(), taskURL)
			}
			return err
		}
		switch s.TaskStatus {
		case "SUCCESS":
			return nil
		case "PENDING":
		default:
			return fmt.Errorf("unknown merge task status %q (%s)", s.TaskStatus, taskURL)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("merge still pending: %w (%s)", ctx.Err(), taskURL)
		case <-time.After(c.opts.PollInterval):
		}
	}
}

func (c *Client) Close(ctx context.Context, number int) error {
	_, err := c.do(ctx, http.MethodPost, c.baseURL+c.pullRequestPath(number, "/decline"), nil)
	return err
}

func (c *Client) Reopen(context.Context, int) error {
	return errors.New("reopening a declined pull request is not supported on Bitbucket")
}

func (c *Client) MarkReady(ctx context.Context, number int) error {
	return c.setDraft(ctx, number, false)
}

func (c *Client) ConvertToDraft(ctx context.Context, number int) error {
	return c.setDraft(ctx, number, true)
}

func (c *Client) setDraft(ctx context.Context, number int, draft bool) error {
	_, err := c.do(ctx, http.MethodPut, c.baseURL+c.pullRequestPath(number, ""), draftDTO{Draft: draft})
	return err
}

// ---- local ----

// Checkout never resets a local branch: one that exists is switched to and
// fast-forwarded, so commits only on it make the checkout fail instead of
// being lost.
func (c *Client) Checkout(ctx context.Context, number int) error {
	if c.opts.Remote == "" || c.opts.Dir == "" {
		return fmt.Errorf("checkout needs a remote and a working directory, got %q and %q", c.opts.Remote, c.opts.Dir)
	}
	var d summaryDTO
	if err := c.get(ctx, c.baseURL+c.pullRequestPath(number, ""), &d); err != nil {
		return err
	}
	src, dst := d.Source.Repository.FullName, d.Destination.Repository.FullName
	if src == "" || dst == "" {
		return fmt.Errorf("pull request %d has no source or destination repository", number)
	}
	if src != dst {
		return fmt.Errorf("checking out a pull request from a fork (%s) is not supported on Bitbucket", src)
	}
	branch := d.Source.Branch.Name
	// A leading dash would reach git as an option, and a name git rejects as
	// a ref, such as feat*, would reach fetch as a pattern. The plain form,
	// not --branch, which accepts @{-1}.
	if branch == "" || strings.HasPrefix(branch, "-") || c.git(ctx, "check-ref-format", "refs/heads/"+branch) != nil {
		return fmt.Errorf("pull request %d: source branch %q cannot be checked out", number, branch)
	}
	local, tracking := "refs/heads/"+branch, "refs/remotes/"+c.opts.Remote+"/"+branch
	if err := c.git(ctx, "fetch", c.opts.Remote, "+"+local+":"+tracking); err != nil {
		return err
	}
	// Any failure reads as "no such branch": switch -c then fails loudly if
	// the branch does exist.
	if c.git(ctx, "rev-parse", "--verify", "--quiet", local) != nil {
		return c.git(ctx, "switch", "-c", branch, "--track", tracking)
	}
	// Checked before switching, so a diverged branch leaves HEAD where it was.
	if c.git(ctx, "merge-base", "--is-ancestor", local, tracking) != nil {
		return fmt.Errorf("local branch %s has commits %s/%s lacks; not checking out", branch, c.opts.Remote, branch)
	}
	if err := c.git(ctx, "switch", branch); err != nil {
		return err
	}
	return c.git(ctx, "merge", "--ff-only", tracking)
}

// AcceptsVerdictFromAuthor is true: Bitbucket Cloud records an approval or a
// request for changes from the author (ADR 0003).
func (c *Client) AcceptsVerdictFromAuthor() bool { return true }

func (c *Client) OpenInBrowser(_ context.Context, number int) error {
	if c.opts.Open == nil {
		return errors.New("no way to open a browser was given to the Bitbucket client")
	}
	return c.opts.Open(c.repo.URL() + "/pull-requests/" + strconv.Itoa(number))
}

func (c *Client) git(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", c.opts.Dir}, args...)...)
	// A credential prompt would hang behind the TUI or draw over it.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s (git %s)", msg, args[0])
	}
	return nil
}

// ---- HTTP ----

func (c *Client) pullRequestPath(number int, rest string) string {
	return c.repoPath("pullrequests/" + strconv.Itoa(number) + rest)
}

func (c *Client) repoPath(rest string) string {
	return "/repositories/" + url.PathEscape(c.repo.Owner) + "/" + url.PathEscape(c.repo.Name) + "/" + rest
}

// getAll reads the pages of a list endpoint until it has limit values, or all
// of them when limit is 0.
func getAll[T any](ctx context.Context, c *Client, path string, query url.Values, limit int) ([]T, error) {
	q := url.Values{"pagelen": {"50"}}
	for k, v := range query {
		q[k] = v
	}
	next := c.baseURL + path + "?" + q.Encode()
	var all []T
	for next != "" && (limit == 0 || len(all) < limit) {
		// next comes from the response body; the credentials must not follow
		// it to another host.
		if !strings.HasPrefix(next, c.baseURL+"/") {
			return nil, fmt.Errorf("next page %q is outside %s", next, c.baseURL)
		}
		var p page[T]
		if err := c.get(ctx, next, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Values...)
		next = p.Next
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// get decodes the JSON at an absolute URL into v.
func (c *Client) get(ctx context.Context, rawURL string, v any) error {
	body, err := c.do(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode %s: %w", rawURL, err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, rawURL string, payload any) ([]byte, error) {
	_, body, err := c.send(ctx, method, rawURL, payload)
	return body, err
}

// send is do for a caller that needs the response's status or headers. The
// response body is already read and closed.
func (c *Client) send(ctx context.Context, method, rawURL string, payload any) (*http.Response, []byte, error) {
	var reqBody io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, fmt.Errorf("encode %s %s: %w", method, rawURL, err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reqBody)
	if err != nil {
		return nil, nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.SetBasicAuth(c.email, c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", rawURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := http.StatusText(resp.StatusCode)
		var e errorDTO
		if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		// The reason first: callers show this on one line cut to the terminal
		// width, and the URL can be long.
		return nil, nil, fmt.Errorf("%s (%d %s %s)", msg, resp.StatusCode, method, rawURL)
	}
	return resp, body, nil
}
