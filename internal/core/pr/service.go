package pr

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
)

// Host is the port to the code host holding the PRs of one repository.
type Host interface {
	List(ctx context.Context, state State) ([]Summary, error)
	Get(ctx context.Context, number int) (*PR, error)
	Diff(ctx context.Context, number int) (string, error)
	ReviewComments(ctx context.Context, number int) ([]ReviewComment, error)
	IssueComments(ctx context.Context, number int) ([]IssueComment, error)
	Viewer(ctx context.Context) (string, error)

	SubmitReview(ctx context.Context, number int, event ReviewEvent, body string) error
	Comment(ctx context.Context, number int, body string) error
	AddInlineComment(ctx context.Context, number int, c InlineComment) error
	AddFileComment(ctx context.Context, number int, commitSHA, path, body string) error
	Reply(ctx context.Context, number int, commentID int64, body string) error
	DeleteReviewComment(ctx context.Context, id int64) error
	DeleteIssueComment(ctx context.Context, id int64) error

	Merge(ctx context.Context, number int, method MergeMethod) error
	Close(ctx context.Context, number int) error
	Reopen(ctx context.Context, number int) error
	MarkReady(ctx context.Context, number int) error
	ConvertToDraft(ctx context.Context, number int) error
	Checkout(ctx context.Context, number int) error
	OpenInBrowser(ctx context.Context, number int) error
}

// Service is what the UI calls for everything PR related. It checks the rules
// a request must meet before it reaches the host.
type Service struct {
	host Host
	repo Repo
}

func NewService(host Host, repo Repo) *Service {
	if host == nil || repo.Host == "" || repo.Owner == "" || repo.Name == "" {
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
		errs [4]error
	)
	wg.Add(4)
	go func() { defer wg.Done(); d.PR, errs[0] = s.host.Get(ctx, number) }()
	go func() { defer wg.Done(); raw, errs[1] = s.host.Diff(ctx, number) }()
	go func() { defer wg.Done(); d.ReviewComments, errs[2] = s.host.ReviewComments(ctx, number) }()
	go func() { defer wg.Done(); d.IssueComments, errs[3] = s.host.IssueComments(ctx, number) }()
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

func (s *Service) SubmitReview(ctx context.Context, number int, event ReviewEvent, body string) error {
	switch event {
	case Approve:
	case RequestChanges, CommentReview:
		if blank(body) {
			return fmt.Errorf("a %s review needs a body", strings.ReplaceAll(string(event), "_", " "))
		}
	default:
		return fmt.Errorf("unknown review event %q", event)
	}
	return s.host.SubmitReview(ctx, number, event, body)
}

func (s *Service) Comment(ctx context.Context, number int, body string) error {
	if blank(body) {
		return fmt.Errorf("comment body is empty")
	}
	return s.host.Comment(ctx, number, body)
}

func (s *Service) AddInlineComment(ctx context.Context, number int, c InlineComment) error {
	if blank(c.Body) {
		return fmt.Errorf("comment body is empty")
	}
	if c.CommitSHA == "" || c.Path == "" || c.Line <= 0 || !validSide(c.Side) {
		return fmt.Errorf("inline comment is not anchored: %+v", c)
	}
	if c.StartLine != 0 && (c.StartLine <= 0 || !validSide(c.StartSide)) {
		return fmt.Errorf("inline comment range is not anchored: %+v", c)
	}
	return s.host.AddInlineComment(ctx, number, c)
}

func (s *Service) AddFileComment(ctx context.Context, number int, commitSHA, path, body string) error {
	if blank(body) {
		return fmt.Errorf("comment body is empty")
	}
	if commitSHA == "" || path == "" {
		return fmt.Errorf("file comment needs a commit and path")
	}
	return s.host.AddFileComment(ctx, number, commitSHA, path, body)
}

func (s *Service) Reply(ctx context.Context, number int, commentID int64, body string) error {
	if blank(body) {
		return fmt.Errorf("reply body is empty")
	}
	return s.host.Reply(ctx, number, commentID, body)
}

func (s *Service) DeleteReviewComment(ctx context.Context, id int64) error {
	return s.host.DeleteReviewComment(ctx, id)
}

func (s *Service) DeleteIssueComment(ctx context.Context, id int64) error {
	return s.host.DeleteIssueComment(ctx, id)
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

func validSide(s diff.Side) bool { return s == diff.Left || s == diff.Right }
