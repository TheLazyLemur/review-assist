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
	"strings"
	"time"

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

// ---- HTTP ----

func (c *Client) repoPath(rest string) string {
	return "/repositories/" + url.PathEscape(c.repo.Owner) + "/" + url.PathEscape(c.repo.Name) + "/" + rest
}

// getAll reads the pages of a list endpoint until it has max values, or all
// of them when max is 0.
func getAll[T any](ctx context.Context, c *Client, path string, query url.Values, max int) ([]T, error) {
	q := url.Values{"pagelen": {"50"}}
	for k, v := range query {
		q[k] = v
	}
	next := c.baseURL + path + "?" + q.Encode()
	var all []T
	for next != "" && (max == 0 || len(all) < max) {
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
	if max > 0 && len(all) > max {
		all = all[:max]
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
