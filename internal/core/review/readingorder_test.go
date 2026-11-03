package review_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

var anyRepo = pr.Repo{Platform: pr.GitHub, Hostname: "h", Owner: "o", Name: "n"}

// fakeJudge answers from a table by path and records what it was asked.
type fakeJudge struct {
	mu      sync.Mutex
	answers map[string]review.FileJudgement
	fail    string // a path to fail on
	asked   []string
}

func (j *fakeJudge) JudgeFile(_ context.Context, title, _ string, f diff.File) (review.FileJudgement, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.asked = append(j.asked, f.Path())
	if f.Path() == j.fail {
		return review.FileJudgement{}, errors.New("429 Too Many Requests")
	}
	return j.answers[f.Path()], nil
}

// idleAgent is a backend that must not be reached.
type idleAgent struct{ runs int }

func (a *idleAgent) Run(context.Context, review.AgentRun) error {
	a.runs++
	return errors.New("no agent expected")
}

func changed(paths ...string) []diff.File {
	var files []diff.File
	for _, p := range paths {
		files = append(files, diff.File{OldPath: p, NewPath: p, Additions: 1})
	}
	return files
}

func detailsOf(files []diff.File) *pr.Details {
	return &pr.Details{PR: &pr.PR{Summary: pr.Summary{Number: 7, Title: "Cap retries"}, Body: "Retries stop after three tries.", HeadSHA: "h", BaseSHA: "b"}, Files: files}
}

func TestJudgedReadingOrderPutsWhatNeedsReadingFirst(t *testing.T) {
	// given
	// ... a PR whose files Jev judges as: generated, a test of the main change, the main change, an unannounced contract change; plus a binary file
	files := changed("gen/client.go", "retry_test.go", "retry.go", "api/types.go")
	files = append(files, diff.File{OldPath: "logo.png", NewPath: "logo.png", Binary: true})
	judge := &fakeJudge{answers: map[string]review.FileJudgement{
		"gen/client.go": {Kind: review.KindGenerated, Central: 0.1, Contract: 0.1, Described: 0.1},
		"retry_test.go": {Kind: review.KindTest, Central: 0.9, Contract: 0.0, Described: 0.9},
		"retry.go":      {Kind: review.KindBehaviour, Central: 0.8, Contract: 0.1, Described: 0.9},
		"api/types.go":  {Kind: review.KindBehaviour, Central: 0.5, Contract: 0.9, Described: 0.1},
	}}
	agent := &idleAgent{}
	reviewer := review.NewService(agent, noCode{}, judge, 5, 1)

	// when
	// ... a reading order is asked for
	order, err := reviewer.ReadingOrder(context.Background(), anyRepo, detailsOf(files))

	// then
	// ... files to read come first, the central one before the other, then the test to skim, then what to skip
	if err != nil {
		t.Fatal(err)
	}
	want := []review.ReadingStep{
		{Path: "retry.go", Tier: review.TierRead, Reason: "behaviour change · central to the PR"},
		{Path: "api/types.go", Tier: review.TierRead, Reason: "behaviour change · changes what callers rely on · not in the description"},
		{Path: "retry_test.go", Tier: review.TierSkim, Reason: "test · central to the PR"},
		{Path: "gen/client.go", Tier: review.TierSkip, Reason: "generated"},
		{Path: "logo.png", Tier: review.TierSkip, Reason: "binary file"},
	}
	if len(order.Steps) != len(want) {
		t.Fatalf("want %d steps, got %+v", len(want), order.Steps)
	}
	for i := range want {
		if order.Steps[i] != want[i] {
			t.Errorf("step %d: want %+v, got %+v", i+1, want[i], order.Steps[i])
		}
	}

	// ... Jev was not asked about the binary file, and no agent ran
	if len(judge.asked) != 4 || strings.Contains(strings.Join(judge.asked, ","), "logo.png") {
		t.Errorf("judge asked about %v", judge.asked)
	}
	if agent.runs != 0 {
		t.Errorf("an agent ran %d times", agent.runs)
	}
}

func TestJudgedReadingOrderFailsWhenAFileCannotBeJudged(t *testing.T) {
	// given
	// ... a judge that fails on one of two files
	judge := &fakeJudge{answers: map[string]review.FileJudgement{"a.go": {Kind: review.KindRefactor}}, fail: "b.go"}
	reviewer := review.NewService(&idleAgent{}, noCode{}, judge, 5, 1)

	// when
	// ... a reading order is asked for
	_, err := reviewer.ReadingOrder(context.Background(), anyRepo, detailsOf(changed("a.go", "b.go")))

	// then
	// ... it fails, naming the file and the cause, rather than ordering with a missing answer
	if err == nil || !strings.Contains(err.Error(), "b.go") || !strings.Contains(err.Error(), "429") {
		t.Fatalf("want an error naming b.go and the cause, got %v", err)
	}
}

func submitOrder(steps ...string) toolCall {
	var parts []string
	for _, p := range steps {
		parts = append(parts, `{"path":"`+p+`","tier":"read","reason":"r"}`)
	}
	return toolCall{Name: "submit_reading_order", Input: json.RawMessage(`{"steps":[` + strings.Join(parts, ",") + `]}`)}
}

func TestAgentReadingOrderWithoutJevHoldsEveryChangedFileOnce(t *testing.T) {
	// given
	// ... no judge, a PR whose commits cannot be fetched, and an agent that reads the diff then orders both files
	agent := &scriptedAgent{calls: []toolCall{
		{Name: "list_changed_files", Input: json.RawMessage(`{}`)},
		{Name: "submit_reading_order", Input: json.RawMessage(`{"steps":[
			{"path":"b.go","tier":"read","reason":"the new retry cap"},
			{"path":"a.go","tier":"skip","reason":"rename only"}]}`)},
	}}
	reviewer := review.NewService(agent, noCode{}, nil, 5, 1)

	// when
	// ... a reading order is asked for
	order, err := reviewer.ReadingOrder(context.Background(), anyRepo, detailsOf(changed("a.go", "b.go")))

	// then
	// ... the agent's order comes back with its tiers and reasons
	if err != nil {
		t.Fatal(err)
	}
	want := []review.ReadingStep{
		{Path: "b.go", Tier: review.TierRead, Reason: "the new retry cap"},
		{Path: "a.go", Tier: review.TierSkip, Reason: "rename only"},
	}
	if len(order.Steps) != 2 || order.Steps[0] != want[0] || order.Steps[1] != want[1] {
		t.Fatalf("want %+v, got %+v", want, order.Steps)
	}

	// ... only the read tools and the reading order's finish tool were offered
	if got := strings.Join(agent.tools, ","); got != "list_changed_files,get_diff,read_file,list_dir,grep,git_log,pr_description,submit_reading_order" {
		t.Errorf("tools: %s", got)
	}

	// ... the missing commits are noted
	if len(order.Notes) != 1 || !strings.Contains(order.Notes[0], "head and base") {
		t.Errorf("want a note about missing commits, got %v", order.Notes)
	}
}

func TestAgentReadingOrderRefusesAnOrderThatMissesInventsOrRepeatsAFile(t *testing.T) {
	// given
	// ... an agent that submits an order missing a file, then one with an unknown file, then one with a repeat, then a valid one
	agent := &scriptedAgent{calls: []toolCall{
		submitOrder("a.go"),
		submitOrder("a.go", "b.go", "c.go"),
		submitOrder("a.go", "a.go", "b.go"),
		submitOrder("b.go", "a.go"),
	}}
	reviewer := review.NewService(agent, noCode{}, nil, 5, 1)

	// when
	// ... a reading order is asked for
	order, err := reviewer.ReadingOrder(context.Background(), anyRepo, detailsOf(changed("a.go", "b.go")))

	// then
	// ... each bad order was refused with the file it got wrong
	if err != nil {
		t.Fatal(err)
	}
	if len(agent.results) != 4 {
		t.Fatalf("want four submissions, got %q", agent.results)
	}
	for i, want := range []string{"leaves out b.go", "c.go is not a changed file", "lists a.go twice"} {
		if !strings.HasPrefix(agent.results[i], "error: ") || !strings.Contains(agent.results[i], want) {
			t.Errorf("submission %d: want an error containing %q, got %q", i+1, want, agent.results[i])
		}
	}

	// ... the agent could submit again, and the valid order was kept
	if len(order.Steps) != 2 || order.Steps[0].Path != "b.go" || order.Steps[1].Path != "a.go" {
		t.Errorf("want b.go then a.go, got %+v", order.Steps)
	}
}

func TestAgentReadingOrderFailsWithoutAValidOrder(t *testing.T) {
	// given
	// ... an agent that only ever submits an incomplete order
	agent := &scriptedAgent{calls: []toolCall{submitOrder("a.go")}}
	reviewer := review.NewService(agent, noCode{}, nil, 5, 1)

	// when
	// ... a reading order is asked for
	_, err := reviewer.ReadingOrder(context.Background(), anyRepo, detailsOf(changed("a.go", "b.go")))

	// then
	// ... the run fails rather than returning a partial order
	if err == nil || !strings.Contains(err.Error(), "reading order") {
		t.Fatalf("want an error about the missing reading order, got %v", err)
	}
}
