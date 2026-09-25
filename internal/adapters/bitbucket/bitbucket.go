// Package bitbucket reads pull requests from Bitbucket Cloud over its REST
// API.
package bitbucket

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

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
}

// NewClient takes the API root, https://api.bitbucket.org/2.0 in production,
// and an Atlassian account email with an API token.
func NewClient(baseURL, email, token string, repo pr.Repo) *Client {
	return &Client{http: &http.Client{Timeout: 60 * time.Second}, baseURL: strings.TrimSuffix(baseURL, "/"), email: email, token: token, repo: repo}
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

// listLimit matches the GitHub adapter's list.
const listLimit = 100

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

type commentDTO struct {
	ID      int64 `json:"id"`
	Content struct {
		Raw string `json:"raw"`
	} `json:"content"`
	User      userDTO   `json:"user"`
	CreatedOn time.Time `json:"created_on"`
	Deleted   bool      `json:"deleted"`
	Pending   bool      `json:"pending"`
	Parent    *struct {
		ID int64 `json:"id"`
	} `json:"parent"`
	Inline *struct {
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
	return prs, nil
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
	body, err := c.do(ctx, c.baseURL+c.pullRequestPath(number, "/diff"))
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
	body, err := c.do(ctx, rawURL)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode %s: %w", rawURL, err)
	}
	return nil
}

func (c *Client) do(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.email, c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", rawURL, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := http.StatusText(resp.StatusCode)
		var e errorDTO
		if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		// The reason first: callers show this on one line cut to the terminal
		// width, and the URL can be long.
		return nil, fmt.Errorf("%s (%d GET %s)", msg, resp.StatusCode, rawURL)
	}
	return body, nil
}
