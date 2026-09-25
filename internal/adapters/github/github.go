// Package github implements pr.CodeHost with the GitHub CLI, for github.com and
// GitHub Enterprise hosts.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
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
		// The reason first: callers show this on one line cut to the terminal
		// width, and the arguments can be long.
		sub := args
		if len(sub) > 2 {
			sub = sub[:2]
		}
		return nil, fmt.Errorf("%s (%s %s)", msg, name, strings.Join(sub, " "))
	}
	return stdout.Bytes(), nil
}

// Platforms says which platform a hostname is on. A hostname gh has
// credentials for, working or not, is GitHub on that hostname: gh --json exits
// 0 and lists a failing host with state "error", and dropping it would hide the
// real error behind "no supported code host". gh is asked at most once, and
// only for a hostname other than github.com and bitbucket.org, so a Bitbucket
// clone does not need gh.
func Platforms(ctx context.Context, run Runner) func(hostname string) (pr.Platform, bool, error) {
	var once sync.Once
	var ghHosts map[string]json.RawMessage
	var ghErr error
	return func(hostname string) (pr.Platform, bool, error) {
		switch hostname {
		case "github.com":
			return pr.GitHub, true, nil
		case "bitbucket.org":
			return pr.Bitbucket, true, nil
		}
		once.Do(func() {
			out, err := run.Run(ctx, nil, "gh", "auth", "status", "--json", "hosts")
			if err != nil {
				ghErr = err
				return
			}
			var v struct{ Hosts map[string]json.RawMessage }
			if err := json.Unmarshal(out, &v); err != nil {
				ghErr = fmt.Errorf("gh auth status: %w", err)
				return
			}
			ghHosts = v.Hosts
		})
		if ghErr != nil {
			return "", false, ghErr
		}
		if _, ok := ghHosts[hostname]; ok {
			return pr.GitHub, true, nil
		}
		return "", false, nil
	}
}

// Client is a pr.Host for one repository.
type Client struct {
	run  Runner
	repo pr.Repo
}

var _ pr.CodeHost = (*Client)(nil)

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
		// Other states (commented, dismissed, pending) carry no verdict.
		if dec, ok := decisions[r.State]; ok {
			p.Verdicts = append(p.Verdicts, pr.Verdict{Author: r.Author.Login, Decision: dec, Message: r.Body, At: r.SubmittedAt})
		}
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

var decisions = map[string]pr.Decision{"APPROVED": pr.Approve, "CHANGES_REQUESTED": pr.RequestChanges}

// GitHub spells the sides of a diff LEFT and RIGHT.
var (
	sideToWire   = map[diff.Side]string{diff.Base: "LEFT", diff.Head: "RIGHT"}
	sideFromWire = map[string]diff.Side{"LEFT": diff.Base, "RIGHT": diff.Head, "": diff.Head}
)

type reviewDTO struct {
	User        author    `json:"user"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// Comments merges GitHub's three kinds: comments on code, comments on the
// pull request, and the summaries of "comment" reviews. Outdated comments on
// code (no current line) are skipped.
func (c *Client) Comments(ctx context.Context, number int) ([]pr.Comment, error) {
	var (
		onCode    []reviewCommentDTO
		onPR      []issueCommentDTO
		summaries []reviewDTO
		errs      [3]error
		wg        sync.WaitGroup
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		onCode, errs[0] = paginate[reviewCommentDTO](ctx, c, c.pullsPath(number)+"/comments")
	}()
	go func() {
		defer wg.Done()
		onPR, errs[1] = paginate[issueCommentDTO](ctx, c, fmt.Sprintf("repos/%s/issues/%d/comments", c.repo.FullName(), number))
	}()
	go func() {
		defer wg.Done()
		summaries, errs[2] = paginate[reviewDTO](ctx, c, c.pullsPath(number)+"/reviews")
	}()
	wg.Wait()
	if err := errors.Join(errs[:]...); err != nil {
		return nil, err
	}

	var out []pr.Comment
	for _, d := range onCode {
		fileLevel := d.SubjectType == "file"
		if !fileLevel && d.Line <= 0 {
			continue
		}
		a := &pr.Anchor{Path: d.Path}
		if !fileLevel {
			side, ok := sideFromWire[d.Side]
			if !ok {
				return nil, fmt.Errorf("comment %d: unknown side %q", d.ID, d.Side)
			}
			a.Line, a.Side = d.Line, side
			if d.StartLine > 0 && d.StartLine != d.Line {
				a.StartLine, a.StartSide = d.StartLine, side
			}
		}
		out = append(out, pr.Comment{ID: d.ID, ReplyTo: d.InReplyToID, Author: d.User.Login, Body: d.Body, CreatedAt: d.CreatedAt, Anchor: a})
	}
	for _, d := range onPR {
		out = append(out, pr.Comment{ID: d.ID, Author: d.User.Login, Body: d.Body, CreatedAt: d.CreatedAt})
	}
	for _, r := range summaries {
		if r.State == "COMMENTED" && strings.TrimSpace(r.Body) != "" {
			// ID 0: GitHub cannot delete a review summary on its own.
			out = append(out, pr.Comment{Author: r.User.Login, Body: r.Body, CreatedAt: r.SubmittedAt})
		}
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

var verdictFlags = map[pr.Decision]string{pr.Approve: "--approve", pr.RequestChanges: "--request-changes"}

func (c *Client) SubmitVerdict(ctx context.Context, number int, decision pr.Decision, message string) error {
	flag, ok := verdictFlags[decision]
	if !ok {
		return fmt.Errorf("unknown decision %q", decision)
	}
	_, err := c.gh(ctx, []byte(message), "pr", "review", strconv.Itoa(number), flag, "--body-file", "-")
	return err
}

func (c *Client) PostComment(ctx context.Context, number int, nc pr.NewComment) error {
	a := nc.Anchor
	if a == nil {
		_, err := c.gh(ctx, []byte(nc.Body), "pr", "comment", strconv.Itoa(number), "--body-file", "-")
		return err
	}
	payload := map[string]any{"body": nc.Body, "commit_id": nc.HeadSHA, "path": a.Path}
	if a.Line == 0 {
		payload["subject_type"] = "file"
	} else {
		payload["line"], payload["side"] = a.Line, sideToWire[a.Side]
		if a.StartLine != 0 {
			payload["start_line"], payload["start_side"] = a.StartLine, sideToWire[a.StartSide]
		}
	}
	return c.postJSON(ctx, c.pullsPath(number)+"/comments", payload)
}

// Reply threads under a comment on code; GitHub has no threads for comments
// on the pull request itself.
func (c *Client) Reply(ctx context.Context, number int, parent pr.Comment, body string) error {
	if parent.Anchor == nil {
		return errors.New("GitHub threads replies only under comments on code")
	}
	root := parent.ID
	if parent.ReplyTo != 0 {
		root = parent.ReplyTo
	}
	return c.postJSON(ctx, fmt.Sprintf("%s/comments/%d/replies", c.pullsPath(number), root), map[string]any{"body": body})
}

func (c *Client) DeleteComment(ctx context.Context, _ int, cm pr.Comment) error {
	kind := "issues"
	if cm.Anchor != nil {
		kind = "pulls"
	}
	_, err := c.gh(ctx, nil, "api", "--method", "DELETE", fmt.Sprintf("repos/%s/%s/comments/%d", c.repo.FullName(), kind, cm.ID))
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
		args = append([]string{"api", "--hostname", c.repo.Hostname}, args[1:]...)
	}
	return c.run.Run(ctx, stdin, "gh", args...)
}
