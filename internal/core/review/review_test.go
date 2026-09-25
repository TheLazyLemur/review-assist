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

// scriptedModel replays tool calls and records what the agent sent.
type scriptedModel struct {
	replies []review.Reply
	tools   []string
	inputs  []review.Input
}

func (m *scriptedModel) Start(_, _ string, tools []review.ToolSpec) review.Conversation {
	for _, t := range tools {
		m.tools = append(m.tools, t.Name)
	}
	return m
}

func (m *scriptedModel) Send(_ context.Context, in review.Input) (review.Reply, error) {
	m.inputs = append(m.inputs, in)
	if len(m.replies) == 0 {
		return review.Reply{}, errors.New("script exhausted")
	}
	r := m.replies[0]
	m.replies = m.replies[1:]
	return r, nil
}

// noCode is a repository where no commit could be fetched.
type noCode struct{}

func (noCode) Open(context.Context, pr.Repo, int, string, string) (review.Code, error) {
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

func call(id, name, input string) review.ToolCall {
	return review.ToolCall{ID: id, Name: name, Input: json.RawMessage(input)}
}

func TestQuickReviewExploresWithReadToolsAndReturnsAnchoredSuggestions(t *testing.T) {
	// given
	// ... a PR whose commits cannot be fetched, and a model that reads the diff then submits one finding on an added line
	files, err := diff.Parse("diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1,2 +1,2 @@\n x := 1\n-y := x\n+y := x / 0\n")
	if err != nil {
		t.Fatal(err)
	}
	details := &pr.Details{PR: &pr.PR{Summary: pr.Summary{Number: 7, Title: "tweak"}, HeadSHA: "h", BaseSHA: "b"}, Files: files}
	model := &scriptedModel{replies: []review.Reply{
		{Calls: []review.ToolCall{call("t1", "get_diff", `{"path":"a.go"}`)}},
		{Calls: []review.ToolCall{call("t2", "submit_findings", `{"findings":[{"path":"a.go","line":2,"side":"RIGHT","severity":"blocking","confidence":"high","title":"Division by zero","explanation":"Always panics.","suggested_comment":"This divides by zero."}]}`)}},
	}}
	reviewer := review.NewReviewer(model, noCode{}, 5, 1)

	// when
	// ... a quick review runs
	res, err := reviewer.Review(context.Background(), pr.Repo{Host: "h", Owner: "o", Name: "n"}, details, review.LevelQuick, func(review.Event) {})

	// then
	// ... the model received the annotated diff as the tool result
	if err != nil {
		t.Fatal(err)
	}
	if len(model.inputs) != 2 || len(model.inputs[1].Results) != 1 || !strings.Contains(model.inputs[1].Results[0].Content, `R2     +y := x / 0`) {
		t.Fatalf("second turn should carry the annotated diff, got %+v", model.inputs)
	}

	// ... only read tools and submit_findings were offered
	want := "list_changed_files,get_diff,read_file,list_dir,grep,git_log,pr_description,submit_findings"
	if got := strings.Join(model.tools, ","); got != want {
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
