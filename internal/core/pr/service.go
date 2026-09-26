package pr

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
)

type CodeHost interface {
	List(ctx context.Context, state State) ([]Summary, error)
	Get(ctx context.Context, number int) (*PR, error)
	Diff(ctx context.Context, number int) (string, error)
	Comments(ctx context.Context, number int) ([]Comment, error)
	Viewer(ctx context.Context) (string, error)

	// AcceptsVerdictFromAuthor reports whether the code host records a
	// verdict from the pull request's author (ADR 0003).
	AcceptsVerdictFromAuthor() bool
	SubmitVerdict(ctx context.Context, number int, decision Decision, message string) error
	PostComment(ctx context.Context, number int, c NewComment) error
	Reply(ctx context.Context, number int, parent Comment, body string) error
	DeleteComment(ctx context.Context, number int, c Comment) error

	Merge(ctx context.Context, number int, method MergeMethod) error
	Close(ctx context.Context, number int) error
	Reopen(ctx context.Context, number int) error
	MarkReady(ctx context.Context, number int) error
	ConvertToDraft(ctx context.Context, number int) error
	Checkout(ctx context.Context, number int) error
	OpenInBrowser(ctx context.Context, number int) error
}

// Service checks the rules a request must meet before it reaches the code
// host.
type Service struct {
	host CodeHost
	repo Repo
}

func NewService(host CodeHost, repo Repo) *Service {
	if host == nil || repo.Platform == "" || repo.Hostname == "" || repo.Owner == "" || repo.Name == "" {
		panic(fmt.Sprintf("pr.NewService: incomplete wiring (host=%v repo=%+v)", host != nil, repo))
	}
	return &Service{host: host, repo: repo}
}

func (s *Service) Repo() Repo { return s.repo }

func (s *Service) List(ctx context.Context, state State) ([]Summary, error) {
	return s.host.List(ctx, state)
}

func (s *Service) Viewer(ctx context.Context) (string, error) { return s.host.Viewer(ctx) }

// Load fetches a PR, its diff and its comments in parallel.
func (s *Service) Load(ctx context.Context, number int) (*Details, error) {
	var (
		d    Details
		raw  string
		wg   sync.WaitGroup
		errs [3]error
	)
	wg.Add(3)
	go func() { defer wg.Done(); d.PR, errs[0] = s.host.Get(ctx, number) }()
	go func() { defer wg.Done(); raw, errs[1] = s.host.Diff(ctx, number) }()
	go func() { defer wg.Done(); d.Comments, errs[2] = s.host.Comments(ctx, number) }()
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	if d.PR.HeadSHA == "" || d.PR.BaseSHA == "" {
		return nil, fmt.Errorf("pr %d: host returned no head or base commit", number)
	}
	files, err := diff.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse diff of #%d: %w", number, err)
	}
	d.Files = files
	return &d, nil
}

// IsOwn reports whether the viewer opened the pull request. An unknown
// viewer owns nothing.
func IsOwn(author, viewer string) bool {
	return viewer != "" && strings.EqualFold(author, viewer)
}

// VerdictPostsAsComment reports whether SubmitVerdict posts a comment headed
// with the verdict instead of the verdict. It does on the viewer's own pull
// request when the code host refuses a verdict from the author: the author
// still reviews code an agent wrote.
func (s *Service) VerdictPostsAsComment(p *PR, viewer string) bool {
	return !s.host.AcceptsVerdictFromAuthor() && IsOwn(p.Author, viewer)
}

// SubmitVerdict posts a verdict, or a comment headed with it when
// VerdictPostsAsComment says so.
func (s *Service) SubmitVerdict(ctx context.Context, p *PR, viewer string, decision Decision, message string) (postedAsComment bool, err error) {
	var heading string
	switch decision {
	case Approve:
		heading = "Approved"
	case RequestChanges:
		heading = "Changes requested"
		if blank(message) {
			return false, fmt.Errorf("a request for changes needs a message")
		}
	default:
		return false, fmt.Errorf("unknown decision %q", decision)
	}
	if !s.VerdictPostsAsComment(p, viewer) {
		return false, s.host.SubmitVerdict(ctx, p.Number, decision, message)
	}
	body := "**" + heading + "**"
	if !blank(message) {
		body = "**" + heading + ":**\n\n" + message
	}
	return true, s.host.PostComment(ctx, p.Number, NewComment{Body: body})
}

func (s *Service) PostComment(ctx context.Context, number int, c NewComment) error {
	if blank(c.Body) {
		return fmt.Errorf("comment body is empty")
	}
	if a := c.Anchor; a != nil {
		switch {
		case a.Path == "" || c.HeadSHA == "":
			return fmt.Errorf("an anchored comment needs a path and a head commit: %+v", c)
		case a.Line < 0 || (a.Line > 0 && !validSide(a.Side)):
			return fmt.Errorf("comment anchor has no valid line and side: %+v", *a)
		case a.StartLine != 0 && (a.Line == 0 || a.StartLine < 0 || !validSide(a.StartSide)):
			return fmt.Errorf("comment range is not anchored: %+v", *a)
		}
	}
	return s.host.PostComment(ctx, number, c)
}

func (s *Service) Reply(ctx context.Context, number int, parent Comment, body string) error {
	if blank(body) {
		return fmt.Errorf("reply body is empty")
	}
	return s.host.Reply(ctx, number, parent, body)
}

func (s *Service) DeleteComment(ctx context.Context, number int, c Comment) error {
	if c.ID == 0 {
		return fmt.Errorf("the code host cannot delete this comment on its own")
	}
	return s.host.DeleteComment(ctx, number, c)
}

func (s *Service) Merge(ctx context.Context, number int, method MergeMethod) error {
	switch method {
	case MergeCommit, Squash, Rebase:
		return s.host.Merge(ctx, number, method)
	default:
		return fmt.Errorf("unknown merge method %q", method)
	}
}

func (s *Service) Close(ctx context.Context, number int) error  { return s.host.Close(ctx, number) }
func (s *Service) Reopen(ctx context.Context, number int) error { return s.host.Reopen(ctx, number) }

func (s *Service) MarkReady(ctx context.Context, number int) error {
	return s.host.MarkReady(ctx, number)
}

func (s *Service) ConvertToDraft(ctx context.Context, number int) error {
	return s.host.ConvertToDraft(ctx, number)
}

func (s *Service) Checkout(ctx context.Context, number int) error {
	return s.host.Checkout(ctx, number)
}

func (s *Service) OpenInBrowser(ctx context.Context, number int) error {
	return s.host.OpenInBrowser(ctx, number)
}

func blank(s string) bool { return strings.TrimSpace(s) == "" }

func validSide(s diff.Side) bool { return s == diff.Base || s == diff.Head }
