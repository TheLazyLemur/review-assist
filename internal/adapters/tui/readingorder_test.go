package tui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

// tableJudge judges each file from a table; block makes it wait for cancellation.
type tableJudge struct {
	mu          sync.Mutex
	answers     map[string]review.FileJudgement
	fail        error
	block       bool
	asked       int
	answerFirst string        // with block, this file is answered and the rest wait
	stopped     chan struct{} // closed when a blocked judgement is cancelled
	once        sync.Once
}

func (j *tableJudge) JudgeFile(ctx context.Context, _, _ string, f diff.File) (review.FileJudgement, error) {
	j.mu.Lock()
	j.asked++
	j.mu.Unlock()
	if j.block && f.Path() != j.answerFirst {
		<-ctx.Done()
		j.once.Do(func() { close(j.stopped) })
		return review.FileJudgement{}, ctx.Err()
	}
	return j.answers[f.Path()], j.fail
}

type noAgent struct{}

func (noAgent) Run(context.Context, review.AgentRun) error { return errors.New("no agent expected") }

type noSource struct{}

func (noSource) Open(context.Context, pr.Repo, *pr.PR) (review.Code, error) {
	return nil, errors.New("no code expected")
}

// diffTabWith opens a PR of a.go, b.go and c.go on the Diff tab, with judge behind the reading order.
func diffTabWith(judge review.Judge) *Model {
	repo := pr.Repo{Platform: pr.GitHub, Hostname: "github.com", Owner: "o", Name: "r"}
	m := New(Deps{PRs: pr.NewService(&failingHost{}, repo), Reviews: review.NewService(noAgent{}, noSource{}, judge, 5, 1)})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.screen, m.pr = screenPR, newPRModel(1)
	var files []diff.File
	for _, p := range []string{"a.go", "b.go", "c.go"} {
		files = append(files, diff.File{OldPath: p, NewPath: p, Additions: 1, Hunks: []diff.Hunk{{Header: "@@ -1 +1 @@", Lines: []diff.Line{{Kind: diff.Add, Text: "x", NewNo: 1}}}}})
	}
	m.pr.setData(&pr.Details{PR: &pr.PR{Summary: pr.Summary{Number: 1, Title: "t"}, HeadSHA: "h", BaseSHA: "b"}, Files: files})
	m.pr.tab = tabDiff
	return m
}

// cOrder has Jev put c.go first, then a.go, then b.go.
func cOrder() *tableJudge {
	return &tableJudge{answers: map[string]review.FileJudgement{
		"a.go": {Kind: review.KindTest, Central: 0.9},
		"b.go": {Kind: review.KindGenerated},
		"c.go": {Kind: review.KindBehaviour, Central: 0.9, Described: 0.9},
	}}
}

func press(m *Model, key string) {
	run(m, update(m, tea.KeyPressMsg{Code: rune(key[0]), Text: key}))
}

// sidebarFiles lists the file names in the sidebar, top to bottom.
func sidebarFiles(m *Model) string {
	var names []string
	for _, l := range strings.Split(ansi.Strip(m.sidebar(20)), "\n") {
		for _, f := range []string{"a.go", "b.go", "c.go"} {
			if strings.Contains(l, f) {
				names = append(names, strings.TrimSpace(l))
			}
		}
	}
	return strings.Join(names, " | ")
}

func TestOShowsTheReadingOrderAndBracketsFollowIt(t *testing.T) {
	// given
	// ... a PR on the Diff tab, and Jev judging c.go to read, a.go to skim and b.go to skip
	m := diffTabWith(cOrder())

	// when
	// ... O is pressed and the order arrives
	press(m, "O")

	// then
	// ... the sidebar numbers the files in the reading order, and the diff opens at the first one with its reason above it
	if got := sidebarFiles(m); got != "1 ● c.go | 2 ◐ a.go | 3 · b.go" {
		t.Errorf("sidebar: %q", got)
	}
	if !strings.Contains(ansi.Strip(m.diffView(120, 30)), "read  behaviour change · central to the PR") {
		t.Errorf("no reason above the diff:\n%s", ansi.Strip(m.diffView(120, 30)))
	}

	// ... ] and [ move along the reading order
	press(m, "]")
	if f, _ := m.pr.file(); f.Path() != "a.go" {
		t.Errorf("] went to %s, want a.go", f.Path())
	}
	press(m, "]")
	press(m, "[")
	if f, _ := m.pr.file(); f.Path() != "a.go" {
		t.Errorf("] then [ went to %s, want a.go", f.Path())
	}
}

func TestOTogglesBetweenTheReadingOrderAndPathOrderWithoutAskingAgain(t *testing.T) {
	// given
	// ... a reading order that has arrived
	judge := cOrder()
	m := diffTabWith(judge)
	press(m, "O")

	// when
	// ... O is pressed once, then again
	press(m, "O")
	pathOrder := sidebarFiles(m)
	press(m, "O")

	// then
	// ... the first press restores path order, the second shows the reading order again, and Jev was asked once per file
	if pathOrder != "a.go | b.go | c.go" {
		t.Errorf("path order: %q", pathOrder)
	}
	if got := sidebarFiles(m); got != "1 ● c.go | 2 ◐ a.go | 3 · b.go" {
		t.Errorf("reading order: %q", got)
	}
	if judge.asked != 3 {
		t.Errorf("Jev asked %d times, want 3", judge.asked)
	}
}

func TestOWhileTheReadingOrderRunsCancelsIt(t *testing.T) {
	// given
	// ... a reading order that is running
	judge := &tableJudge{block: true, stopped: make(chan struct{})}
	m := diffTabWith(judge)
	press(m, "O")
	running := m.status

	// when
	// ... O is pressed again
	press(m, "O")

	// then
	// ... the status said it was running, it is cancelled, and the sidebar stays in path order
	if !strings.Contains(running, "reading order running") {
		t.Errorf("status while running: %q", running)
	}
	if !strings.Contains(m.status, "cancelled") {
		t.Errorf("status after cancelling: %q", m.status)
	}
	if got := sidebarFiles(m); got != "a.go | b.go | c.go" {
		t.Errorf("sidebar: %q", got)
	}
}

func TestARunningReadingOrderShowsItsProgressUntilItEnds(t *testing.T) {
	// given
	// ... Jev has judged a.go and is still judging the other two
	judge := &tableJudge{block: true, answerFirst: "a.go", stopped: make(chan struct{}),
		answers: map[string]review.FileJudgement{"a.go": {Kind: review.KindTest}}}
	m := diffTabWith(judge)
	press(m, "O")
	t.Cleanup(func() { press(m, "O") }) // cancel the judgements still waiting

	// when
	// ... the status line has cleared
	update(m, clearStatusMsg{at: m.statusAt})

	// then
	// ... the file list header and the app header still say it is ordering, with its progress
	if m.status != "" {
		t.Errorf("status not cleared: %q", m.status)
	}
	if head := ansi.Strip(m.sidebar(20)); !strings.Contains(head, "judged 1/3 files") {
		t.Errorf("file list header: %q", strings.SplitN(head, "\n", 2)[0])
	}
	if head := ansi.Strip(m.headerView()); !strings.Contains(head, "reading order") {
		t.Errorf("app header: %q", head)
	}
}

func TestAReadingOrderThatArrivesOffTheDiffTabMarksTheTab(t *testing.T) {
	// given
	// ... a reading order asked for, and the user gone to the Overview tab before it arrives
	m := diffTabWith(cOrder())
	m.pr.tab = tabOverview

	// when
	// ... the order arrives
	run(m, m.readingOrderKey())

	// then
	// ... the Diff tab is marked until the user opens it
	if tabs := ansi.Strip(m.prView(120, 30)); !strings.Contains(tabs, "2 Diff ●") {
		t.Errorf("Diff tab not marked: %q", strings.SplitN(tabs, "\n", 2)[0])
	}
	press(m, "2")
	m.prView(120, 30)
	if tabs := ansi.Strip(m.prView(120, 30)); strings.Contains(tabs, "2 Diff ●") {
		t.Errorf("Diff tab still marked after opening it: %q", strings.SplitN(tabs, "\n", 2)[0])
	}
}

func TestClosingThePRCancelsARunningReadingOrder(t *testing.T) {
	// given
	// ... a reading order that is running
	judge := &tableJudge{block: true, stopped: make(chan struct{})}
	m := diffTabWith(judge)
	press(m, "O")

	// when
	// ... the PR is closed
	press(m, "q")

	// then
	// ... the order's work is cancelled rather than left running
	select {
	case <-judge.stopped:
	case <-time.After(time.Second):
		t.Fatal("the reading order still runs after the PR closed")
	}
}

func TestAFailedReadingOrderShowsItsErrorAndKeepsPathOrder(t *testing.T) {
	// given
	// ... Jev refusing every request
	m := diffTabWith(&tableJudge{fail: errors.New("429 Too Many Requests")})

	// when
	// ... O is pressed
	press(m, "O")

	// then
	// ... the error is shown and the sidebar stays in path order
	if !m.statusErr || !strings.Contains(m.status, "429") {
		t.Errorf("status: %q (error %v)", m.status, m.statusErr)
	}
	if got := sidebarFiles(m); got != "a.go | b.go | c.go" {
		t.Errorf("sidebar: %q", got)
	}
}
