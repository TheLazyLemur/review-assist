// Package pr is the pull request domain: its types, the CodeHost port, and
// the Service the UI calls.
package pr

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
)

type Platform string

const (
	GitHub    Platform = "github"
	Bitbucket Platform = "bitbucket"
)

type Repo struct {
	Platform Platform
	Hostname string // such as github.com, a GitHub Enterprise server or bitbucket.org
	Owner    string
	Name     string
}

func (r Repo) FullName() string { return r.Owner + "/" + r.Name }

// Qualified is the HOSTNAME/OWNER/REPO form.
func (r Repo) Qualified() string { return r.Hostname + "/" + r.FullName() }

func (r Repo) URL() string { return "https://" + r.Qualified() }

var (
	bitbucketURLRe = regexp.MustCompile(`^(?:https?://)?bitbucket\.org/([^/\s]+)/([^/\s]+)/pull-requests/(\d+)`)
	prURLRe        = regexp.MustCompile(`^(?:https?://)?([^/\s]+)/([^/\s]+)/([^/\s]+)/pulls?/(\d+)`)
	shortRefRe     = regexp.MustCompile(`^(?:([^/\s]+)/)?([^/\s]+)/([^/#\s]+)#(\d+)$`)
)

// ParseRef accepts a PR URL on github.com, a GHE server or bitbucket.org,
// HOSTNAME/OWNER/REPO#N or OWNER/REPO#N (github.com).
func ParseRef(s string) (Repo, int, error) {
	s = strings.TrimSpace(s)
	if m := bitbucketURLRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[3])
		return Repo{Platform: Bitbucket, Hostname: "bitbucket.org", Owner: m[1], Name: m[2]}, n, nil
	}
	if m := prURLRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[4])
		return Repo{Platform: GitHub, Hostname: m[1], Owner: m[2], Name: strings.TrimSuffix(m[3], ".git")}, n, nil
	}
	if m := shortRefRe.FindStringSubmatch(s); m != nil {
		hostname := m[1]
		if hostname == "" {
			hostname = "github.com"
		}
		n, _ := strconv.Atoi(m[4])
		return Repo{Platform: GitHub, Hostname: hostname, Owner: m[2], Name: m[3]}, n, nil
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
	Verdicts  []Verdict
	Checks    []Check
}

type Decision string

const (
	Approve        Decision = "approve"
	RequestChanges Decision = "request_changes"
)

type Verdict struct {
	Author   string
	Decision Decision
	Message  string
	At       time.Time
}

type Check struct {
	Name   string
	Result string // lower case: success, failure, pending, ...
}

type MergeMethod string

const (
	MergeCommit MergeMethod = "merge"
	Squash      MergeMethod = "squash"
	Rebase      MergeMethod = "rebase"
)

// Anchor is where a comment points. Line 0 means the whole file.
type Anchor struct {
	Path      string
	Line      int
	Side      diff.Side
	StartLine int // 0 for a single line
	StartSide diff.Side
}

type Comment struct {
	// ID is 0 when the code host cannot delete the comment on its own, such
	// as the summary of a GitHub review.
	ID        int64
	ReplyTo   int64
	Author    string
	Body      string
	CreatedAt time.Time
	Anchor    *Anchor // nil: about the whole pull request
}

type NewComment struct {
	Body   string
	Anchor *Anchor
	// HeadSHA is the head commit the user saw; an anchor refers to it.
	HeadSHA string
}

// Details is everything the PR screen shows.
type Details struct {
	PR       *PR
	Files    []diff.File
	Comments []Comment
}
