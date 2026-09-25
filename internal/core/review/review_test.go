package review_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

type toolCall struct {
	Name  string
	Input json.RawMessage
}

// scriptedAgent is a backend that makes a fixed sequence of tool calls.
type scriptedAgent struct {
	calls   []toolCall
	tools   []string
	results []string
}

func (a *scriptedAgent) Run(ctx context.Context, task review.Task) error {
	byName := map[string]review.Tool{}
	for _, t := range task.Tools {
		a.tools = append(a.tools, t.Name)
		byName[t.Name] = t
	}
	for _, c := range a.calls {
		out, err := byName[c.Name].Call(ctx, c.Input)
		if err != nil {
			out = "error: " + err.Error()
		}
		a.results = append(a.results, out)
		if task.Done() {
			return nil
		}
	}
	return nil
}

// noCode is a repository where no commit could be fetched.
type noCode struct{}

func (noCode) Open(context.Context, pr.Repo, *pr.PR) (review.Code, error) {
	return noCode{}, nil
}
func (noCode) Location() string                                         { return "nowhere" }
func (noCode) HasCommit(context.Context, string) bool                   { return false }
func (noCode) ReadFile(context.Context, string, string) ([]byte, error) { return nil, errors.New("no") }
func (noCode) ListDir(context.Context, string, string) ([]string, error) {
	return nil, errors.New("no")
}
func (noCode) Grep(context.Context, string, string, string, bool) ([]string, error) {
	return nil, errors.New("no")
}
func (noCode) Log(context.Context, string, string, int) (string, error) { return "", errors.New("no") }

func TestQuickReviewExploresWithReadToolsAndReturnsAnchoredSuggestions(t *testing.T) {
	// given
	// ... a PR whose commits cannot be fetched, and an agent that reads the diff then submits one finding on an added line
	files, err := diff.Parse("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n x := 1\n-y := x\n+y := x / 0\n")
	if err != nil {
		t.Fatal(err)
	}
	details := &pr.Details{PR: &pr.PR{Summary: pr.Summary{Number: 7, Title: "tweak"}, HeadSHA: "h", BaseSHA: "b"}, Files: files}
	agent := &scriptedAgent{calls: []toolCall{
		{Name: "get_diff", Input: json.RawMessage(`{"path":"a.go"}`)},
		{Name: "submit_findings", Input: json.RawMessage(`{"findings":[{"path":"a.go","line":2,"side":"head","severity":"blocking","confidence":"high","title":"Division by zero","explanation":"Always panics.","suggested_comment":"This divides by zero."}]}`)},
		{Name: "get_diff", Input: json.RawMessage(`{}`)}, // never reached: the task is done
	}}
	reviewer := review.NewService(agent, noCode{}, 5, 1)

	// when
	// ... a quick review runs
	res, err := reviewer.Review(context.Background(), pr.Repo{Platform: pr.GitHub, Hostname: "h", Owner: "o", Name: "n"}, details, review.LevelQuick, func(review.Event) {})

	// then
	// ... the diff tool returned the annotated diff, and the run stopped at the submission
	if err != nil {
		t.Fatal(err)
	}
	if len(agent.results) != 2 || !strings.Contains(agent.results[0], `H2     +y := x / 0`) {
		t.Fatalf("want the annotated diff then the submission, got %q", agent.results)
	}

	// ... only read tools and submit_findings were offered
	want := "list_changed_files,get_diff,read_file,list_dir,grep,git_log,pr_description,submit_findings"
	if got := strings.Join(agent.tools, ","); got != want {
		t.Errorf("tools: want %s, got %s", want, got)
	}

	// ... the missing commits are reported, and the finding comes back anchored to the diff line
	if len(res.Notes) != 1 || !strings.Contains(res.Notes[0], "head and base") {
		t.Errorf("want a note about missing commits, got %v", res.Notes)
	}
	if len(res.Findings) != 1 || !res.Findings[0].Anchored || res.Findings[0].Lens != "L1" {
		t.Fatalf("want one anchored L1 finding, got %+v", res.Findings)
	}
}
