// Package github implements pr.Host with the GitHub CLI, for github.com and
// GitHub Enterprise hosts.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

// Runner executes a command and returns its stdout.
type Runner interface {
	Run(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{ Dir string }

func (r ExecRunner) Run(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = r.Dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// Detect resolves the GitHub repository for the runner's directory the same
// way every other gh command there will.
func Detect(ctx context.Context, run Runner) (pr.Repo, error) {
	out, err := run.Run(ctx, nil, "gh", "repo", "view", "--json", "owner,name,url")
	if err != nil {
		return pr.Repo{}, fmt.Errorf("not in a GitHub repository gh can resolve: %w", err)
	}
	var v struct {
		Owner struct{ Login string }
		Name  string
		URL   string
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return pr.Repo{}, fmt.Errorf("gh repo view: %w", err)
	}
	u, err := url.Parse(v.URL)
	if err != nil || u.Host == "" || v.Owner.Login == "" || v.Name == "" {
		return pr.Repo{}, fmt.Errorf("gh repo view returned no host/owner/name: %s", out)
	}
	return pr.Repo{Host: u.Host, Owner: v.Owner.Login, Name: v.Name}, nil
}

// Client is a pr.Host for one repository.
type Client struct {
	run  Runner
	repo pr.Repo
}

var _ pr.Host = (*Client)(nil)

func NewClient(run Runner, repo pr.Repo) *Client { return &Client{run: run, repo: repo} }

// ---- wire types: gh JSON field names stay in this file ----

type author struct {
	Login string `json:"login"`
}

type summaryDTO struct {
	Number         int       `json:"number"`
	Title          string    `json:"title"`
	Author         author    `json:"author"`
	HeadRefName    string    `json:"headRefName"`
	BaseRefName    string    `json:"baseRefName"`
	IsDraft        bool      `json:"isDraft"`
	State          string    `json:"state"`
	ReviewDecision string    `json:"reviewDecision"`
	UpdatedAt      time.Time `json:"updatedAt"`
	Additions      int       `json:"additions"`
	Deletions      int       `json:"deletions"`
	ChangedFiles   int       `json:"changedFiles"`
	Labels         []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

const summaryFields = "number,title,author,headRefName,baseRefName,isDraft,state,reviewDecision,updatedAt,additions,deletions,changedFiles,labels"

func (d summaryDTO) toDomain() pr.Summary {
	s := pr.Summary{
		Number: d.Number, Title: d.Title, Author: d.Author.Login, HeadRef: d.HeadRefName, BaseRef: d.BaseRefName,
		IsDraft: d.IsDraft, State: d.State, ReviewDecision: d.ReviewDecision, UpdatedAt: d.UpdatedAt,
		Additions: d.Additions, Deletions: d.Deletions, ChangedFiles: d.ChangedFiles,
	}
	for _, l := range d.Labels {
		s.Labels = append(s.Labels, l.Name)
	}
	return s
}

type prDTO struct {
	summaryDTO
	Body       string    `json:"body"`
	URL        string    `json:"url"`
	HeadRefOid string    `json:"headRefOid"`
	BaseRefOid string    `json:"baseRefOid"`
	Mergeable  string    `json:"mergeable"`
	CreatedAt  time.Time `json:"createdAt"`
	Reviews    []struct {
		Author      author    `json:"author"`
		Body        string    `json:"body"`
		State       string    `json:"state"`
		SubmittedAt time.Time `json:"submittedAt"`
	} `json:"reviews"`
	Checks []checkDTO `json:"statusCheckRollup"`
}

// checkDTO covers both check runs (name/status/conclusion) and commit
// statuses (context/state).
type checkDTO struct {
	Name       string `json:"name"`
	Context    string `json:"context"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

func (c checkDTO) toDomain() pr.Check {
	name := c.Name
	if name == "" {
		name = c.Context
	}
	result := c.Status
	switch {
	case c.Conclusion != "":
		result = c.Conclusion
	case c.State != "":
		result = c.State
	}
	return pr.Check{Name: name, Result: strings.ToLower(result)}
}

type reviewCommentDTO struct {
	ID          int64     `json:"id"`
	SubjectType string    `json:"subject_type"` // "line" or "file"
	InReplyToID int64     `json:"in_reply_to_id"`
	Path        string    `json:"path"`
	Line        int       `json:"line"`
	StartLine   int       `json:"start_line"`
	Side        string    `json:"side"`
	Body        string    `json:"body"`
	User        author    `json:"user"`
	CreatedAt   time.Time `json:"created_at"`
}

type issueCommentDTO struct {
	ID        int64     `json:"id"`
	User      author    `json:"user"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// ---- reads ----

func (c *Client) List(ctx context.Context, state pr.State) ([]pr.Summary, error) {
	out, err := c.gh(ctx, nil, "pr", "list", "--state", string(state), "--limit", "100", "--json", summaryFields)
	if err != nil {
		return nil, err
	}
	var dtos []summaryDTO
	if err := json.Unmarshal(out, &dtos); err != nil {
		return nil, fmt.Errorf("decode pr list: %w", err)
	}
	prs := make([]pr.Summary, 0, len(dtos))
	for _, d := range dtos {
		prs = append(prs, d.toDomain())
	}
	return prs, nil
}

func (c *Client) Get(ctx context.Context, number int) (*pr.PR, error) {
	fields := summaryFields + ",body,url,headRefOid,baseRefOid,mergeable,createdAt,reviews,statusCheckRollup"
	out, err := c.gh(ctx, nil, "pr", "view", strconv.Itoa(number), "--json", fields)
	if err != nil {
		return nil, err
	}
	var d prDTO
	if err := json.Unmarshal(out, &d); err != nil {
		return nil, fmt.Errorf("decode pr view: %w", err)
	}
	p := &pr.PR{
		Summary: d.summaryDTO.toDomain(), Body: d.Body, URL: d.URL, HeadSHA: d.HeadRefOid, BaseSHA: d.BaseRefOid,
		Mergeable: d.Mergeable, CreatedAt: d.CreatedAt,
	}
	for _, r := range d.Reviews {
		p.Reviews = append(p.Reviews, pr.Review{Author: r.Author.Login, Body: r.Body, State: r.State, SubmittedAt: r.SubmittedAt})
	}
	for _, ch := range d.Checks {
		p.Checks = append(p.Checks, ch.toDomain())
	}
	return p, nil
}

func (c *Client) Diff(ctx context.Context, number int) (string, error) {
	out, err := c.gh(ctx, nil, "pr", "diff", strconv.Itoa(number), "--color", "never")
	return string(out), err
}

// ReviewComments lists inline and file-level comments. Outdated line comments
// (no current line) are skipped.
func (c *Client) ReviewComments(ctx context.Context, number int) ([]pr.ReviewComment, error) {
	dtos, err := paginate[reviewCommentDTO](ctx, c, c.pullsPath(number)+"/comments")
	if err != nil {
		return nil, err
	}
	var out []pr.ReviewComment
	for _, d := range dtos {
		fileLevel := d.SubjectType == "file"
		if !fileLevel && d.Line <= 0 {
			continue
		}
		side := diff.Side(d.Side)
		if side == "" {
			side = diff.Right
		}
		out = append(out, pr.ReviewComment{
			ID: d.ID, FileLevel: fileLevel, InReplyToID: d.InReplyToID, Path: d.Path, Line: d.Line,
			StartLine: d.StartLine, Side: side, Body: d.Body, Author: d.User.Login, CreatedAt: d.CreatedAt,
		})
	}
	return out, nil
}

func (c *Client) IssueComments(ctx context.Context, number int) ([]pr.IssueComment, error) {
	dtos, err := paginate[issueCommentDTO](ctx, c, fmt.Sprintf("repos/%s/issues/%d/comments", c.repo.FullName(), number))
	if err != nil {
		return nil, err
	}
	out := make([]pr.IssueComment, 0, len(dtos))
	for _, d := range dtos {
		out = append(out, pr.IssueComment{ID: d.ID, Author: d.User.Login, Body: d.Body, CreatedAt: d.CreatedAt})
	}
	return out, nil
}

func paginate[T any](ctx context.Context, c *Client, path string) ([]T, error) {
	out, err := c.gh(ctx, nil, "api", "--paginate", "--slurp", path)
	if err != nil {
		return nil, err
	}
	var pages [][]T
	if err := json.Unmarshal(out, &pages); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	var all []T
	for _, p := range pages {
		all = append(all, p...)
	}
	return all, nil
}

// Viewer is the login of the authenticated user.
func (c *Client) Viewer(ctx context.Context) (string, error) {
	out, err := c.gh(ctx, nil, "api", "user", "--jq", ".login")
	return strings.TrimSpace(string(out)), err
}

// ---- writes ----

var reviewFlags = map[pr.ReviewEvent]string{
	pr.Approve:        "--approve",
	pr.RequestChanges: "--request-changes",
	pr.CommentReview:  "--comment",
}

func (c *Client) SubmitReview(ctx context.Context, number int, event pr.ReviewEvent, body string) error {
	flag, ok := reviewFlags[event]
	if !ok {
		return fmt.Errorf("unknown review event %q", event)
	}
	_, err := c.gh(ctx, []byte(body), "pr", "review", strconv.Itoa(number), flag, "--body-file", "-")
	return err
}

func (c *Client) Comment(ctx context.Context, number int, body string) error {
	_, err := c.gh(ctx, []byte(body), "pr", "comment", strconv.Itoa(number), "--body-file", "-")
	return err
}

func (c *Client) AddInlineComment(ctx context.Context, number int, ic pr.InlineComment) error {
	payload := map[string]any{"body": ic.Body, "commit_id": ic.CommitSHA, "path": ic.Path, "line": ic.Line, "side": string(ic.Side)}
	if ic.StartLine != 0 {
		payload["start_line"], payload["start_side"] = ic.StartLine, string(ic.StartSide)
	}
	return c.postJSON(ctx, c.pullsPath(number)+"/comments", payload)
}

func (c *Client) AddFileComment(ctx context.Context, number int, commitSHA, path, body string) error {
	return c.postJSON(ctx, c.pullsPath(number)+"/comments",
		map[string]any{"body": body, "commit_id": commitSHA, "path": path, "subject_type": "file"})
}

func (c *Client) Reply(ctx context.Context, number int, commentID int64, body string) error {
	return c.postJSON(ctx, fmt.Sprintf("%s/comments/%d/replies", c.pullsPath(number), commentID), map[string]any{"body": body})
}

func (c *Client) DeleteReviewComment(ctx context.Context, id int64) error {
	_, err := c.gh(ctx, nil, "api", "--method", "DELETE", fmt.Sprintf("repos/%s/pulls/comments/%d", c.repo.FullName(), id))
	return err
}

func (c *Client) DeleteIssueComment(ctx context.Context, id int64) error {
	_, err := c.gh(ctx, nil, "api", "--method", "DELETE", fmt.Sprintf("repos/%s/issues/comments/%d", c.repo.FullName(), id))
	return err
}

var mergeFlags = map[pr.MergeMethod]string{pr.MergeCommit: "--merge", pr.Squash: "--squash", pr.Rebase: "--rebase"}

func (c *Client) Merge(ctx context.Context, number int, m pr.MergeMethod) error {
	flag, ok := mergeFlags[m]
	if !ok {
		return fmt.Errorf("unknown merge method %q", m)
	}
	_, err := c.gh(ctx, nil, "pr", "merge", strconv.Itoa(number), flag)
	return err
}

func (c *Client) Close(ctx context.Context, number int) error {
	_, err := c.gh(ctx, nil, "pr", "close", strconv.Itoa(number))
	return err
}

func (c *Client) Reopen(ctx context.Context, number int) error {
	_, err := c.gh(ctx, nil, "pr", "reopen", strconv.Itoa(number))
	return err
}

func (c *Client) MarkReady(ctx context.Context, number int) error {
	_, err := c.gh(ctx, nil, "pr", "ready", strconv.Itoa(number))
	return err
}

func (c *Client) ConvertToDraft(ctx context.Context, number int) error {
	_, err := c.gh(ctx, nil, "pr", "ready", strconv.Itoa(number), "--undo")
	return err
}

func (c *Client) Checkout(ctx context.Context, number int) error {
	_, err := c.gh(ctx, nil, "pr", "checkout", strconv.Itoa(number))
	return err
}

func (c *Client) OpenInBrowser(ctx context.Context, number int) error {
	_, err := c.gh(ctx, nil, "pr", "view", strconv.Itoa(number), "--web")
	return err
}

func (c *Client) postJSON(ctx context.Context, path string, payload map[string]any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = c.gh(ctx, b, "api", "--method", "POST", path, "--input", "-")
	return err
}

func (c *Client) pullsPath(number int) string {
	return fmt.Sprintf("repos/%s/pulls/%d", c.repo.FullName(), number)
}

func (c *Client) gh(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	// Pin every call to the repo (and its host, for GHE) so gh never guesses.
	switch args[0] {
	case "pr":
		args = append(args, "--repo", c.repo.Qualified())
	case "api":
		args = append([]string{"api", "--hostname", c.repo.Host}, args[1:]...)
	}
	return c.run.Run(ctx, stdin, "gh", args...)
}
