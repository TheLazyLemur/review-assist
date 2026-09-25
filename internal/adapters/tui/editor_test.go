package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

// failingHost refuses to post. Other Host methods are not reached.
type failingHost struct {
	pr.Host
	posted []string
}

func (h *failingHost) Comment(_ context.Context, _ int, body string) error {
	h.posted = append(h.posted, body)
	return errors.New("Can not comment on this pull request")
}

func TestFailedPostKeepsTheEditorAndTheText(t *testing.T) {
	// given
	// ... an open PR, and a host that refuses every comment
	host := &failingHost{}
	repo := pr.Repo{Host: "github.com", Owner: "o", Name: "r"}
	m := New(Deps{PRs: pr.NewService(host, repo)})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.screen, m.pr = screenPR, newPRModel(1)
	m.pr.setData(&pr.Details{PR: &pr.PR{Summary: pr.Summary{Number: 1, Author: "o"}, HeadSHA: "h", BaseSHA: "b"}})

	// when
	// ... the user writes a PR comment and submits it
	run(m, m.prComment())
	for _, r := range "looks good" {
		run(m, update(m, tea.KeyPressMsg{Code: r, Text: string(r)}))
	}
	run(m, update(m, tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}))

	// then
	// ... the host got the text, and the editor is still open with the text and the host's reason
	if len(host.posted) != 1 || host.posted[0] != "looks good" {
		t.Fatalf("posted %q", host.posted)
	}
	e, ok := m.modal.(*editorModal)
	if !ok {
		t.Fatalf("editor closed after a failed post; modal is %T", m.modal)
	}
	if e.ta.Value() != "looks good" {
		t.Errorf("text lost: %q", e.ta.Value())
	}
	if !strings.Contains(e.err, "Can not comment on this pull request") {
		t.Errorf("editor does not show the reason: %q", e.err)
	}
}

func update(m *Model, msg tea.Msg) tea.Cmd {
	_, cmd := m.Update(msg)
	return cmd
}

// run executes a command and feeds this package's messages back, as the
// program would. Library messages (cursor blink) and timers are dropped:
// they reschedule themselves or sleep.
func run(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(200 * time.Millisecond):
		return // a timer
	}
	switch msg := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range msg {
			run(m, c)
		}
	default:
		if reflect.TypeOf(msg).PkgPath() != reflect.TypeOf(Model{}).PkgPath() {
			return
		}
		run(m, update(m, msg))
	}
}
