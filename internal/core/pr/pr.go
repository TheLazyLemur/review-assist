// Package pr is the pull request domain: the types a review works with, the
// Host port a code host adapter implements, and the Service the UI calls.
package pr

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
)

type Repo struct {
	Host  string // github.com or a GitHub Enterprise host
	Owner string
	Name  string
}

func (r Repo) FullName() string { return r.Owner + "/" + r.Name }

// Qualified is the HOST/OWNER/REPO form.
func (r Repo) Qualified() string { return r.Host + "/" + r.FullName() }

func (r Repo) URL() string { return "https://" + r.Qualified() }

var (
	prURLRe    = regexp.MustCompile(`^(?:https?://)?([^/\s]+)/([^/\s]+)/([^/\s]+)/pulls?/(\d+)`)
	shortRefRe = regexp.MustCompile(`^(?:([^/\s]+)/)?([^/\s]+)/([^/#\s]+)#(\d+)$`)
)

// ParseRef accepts a PR URL on any host (github.com or GHE), HOST/OWNER/REPO#N
// or OWNER/REPO#N (github.com).
func ParseRef(s string) (Repo, int, error) {
	s = strings.TrimSpace(s)
	if m := prURLRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[4])
		return Repo{Host: m[1], Owner: m[2], Name: strings.TrimSuffix(m[3], ".git")}, n, nil
	}
	if m := shortRefRe.FindStringSubmatch(s); m != nil {
		host := m[1]
		if host == "" {
			host = "github.com"
		}
		n, _ := strconv.Atoi(m[4])
		return Repo{Host: host, Owner: m[2], Name: m[3]}, n, nil
	}
	return Repo{}, 0, fmt.Errorf("invalid PR reference %q (use a PR URL, OWNER/REPO#N or HOST/OWNER/REPO#N)", s)
}

// State filters the PR list.
type State string

const (
	Open   State = "open"
	Closed State = "closed"
	Merged State = "merged"
	All    State = "all"
)

var States = []State{Open, Closed, Merged, All}

type Summary struct {
	Number         int
	Title          string
	Author         string
	HeadRef        string
	BaseRef        string
	IsDraft        bool
	State          string // OPEN, CLOSED or MERGED
	ReviewDecision string // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED or ""
	UpdatedAt      time.Time
	Additions      int
	Deletions      int
	ChangedFiles   int
	Labels         []string
}

type PR struct {
	Summary
	Body      string
	URL       string
	HeadSHA   string
	BaseSHA   string
	Mergeable string
	CreatedAt time.Time
	Reviews   []Review
	Checks    []Check
}

type Review struct {
	Author      string
	Body        string
	State       string
	SubmittedAt time.Time
}

type Check struct {
	Name   string
	Result string // lower case: success, failure, pending, ...
}

// IssueComment is a PR-level (conversation) comment.
type IssueComment struct {
	ID        int64
	Author    string
	Body      string
	CreatedAt time.Time
}

// ReviewComment is an inline comment on a line, or a comment on a whole file.
type ReviewComment struct {
	ID          int64
	FileLevel   bool
	InReplyToID int64
	Path        string
	Line        int
	StartLine   int
	Side        diff.Side
	Body        string
	Author      string
	CreatedAt   time.Time
}

type ReviewEvent string

const (
	Approve        ReviewEvent = "approve"
	RequestChanges ReviewEvent = "request_changes"
	CommentReview  ReviewEvent = "comment"
)

type MergeMethod string

const (
	MergeCommit MergeMethod = "merge"
	Squash      MergeMethod = "squash"
	Rebase      MergeMethod = "rebase"
)

// InlineComment anchors a comment to a line (or StartLine..Line range) of the diff.
type InlineComment struct {
	Body      string
	CommitSHA string
	Path      string
	Line      int
	Side      diff.Side
	StartLine int       // 0 for a single line
	StartSide diff.Side // set with StartLine
}

// Details is everything the PR screen shows.
type Details struct {
	PR             *PR
	Files          []diff.File
	ReviewComments []ReviewComment
	IssueComments  []IssueComment
}
