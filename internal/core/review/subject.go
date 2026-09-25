package review

import (
	"context"
	"strings"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

// Subject is everything the agents may look at for one PR: the parsed diff
// and read-only Code at the PR's head and base commits.
type Subject struct {
	HeadSHA  string
	BaseSHA  string
	Title    string
	Body     string
	Files    []diff.File
	HasRules bool

	code   Code
	headOK bool
	baseOK bool
}

func newSubject(ctx context.Context, code Code, d *pr.Details) *Subject {
	w := &Subject{
		HeadSHA: d.PR.HeadSHA, BaseSHA: d.PR.BaseSHA, Title: d.PR.Title, Body: d.PR.Body, Files: d.Files,
		code: code,
	}
	w.headOK = code.HasCommit(ctx, w.HeadSHA)
	w.baseOK = code.HasCommit(ctx, w.BaseSHA)
	if w.headOK {
		for _, f := range RulesFiles {
			if _, err := code.ReadFile(ctx, w.HeadSHA, f); err == nil {
				w.HasRules = true
				break
			}
		}
	}
	return w
}

// Degraded explains missing commits, or is empty when both are available.
func (w *Subject) Degraded() string {
	var missing []string
	if !w.headOK {
		missing = append(missing, "head")
	}
	if !w.baseOK {
		missing = append(missing, "base")
	}
	if len(missing) == 0 {
		return ""
	}
	return "could not fetch the PR " + strings.Join(missing, " and ") + " commit; agents only see the diff"
}

func (w *Subject) file(path string) (diff.File, bool) {
	for _, f := range w.Files {
		if f.Path() == path || f.OldPath == path {
			return f, true
		}
	}
	return diff.File{}, false
}

// Anchored reports whether (path, side, line) is a line in the diff: a code
// host accepts a comment anchored there.
func (w *Subject) Anchored(path string, side diff.Side, line int) bool {
	f, ok := w.file(path)
	if !ok {
		return false
	}
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			s, n := l.Target()
			if s == side && n == line {
				return true
			}
		}
	}
	return false
}
