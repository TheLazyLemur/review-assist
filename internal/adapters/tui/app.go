// Package tui is the bubbletea front end: a PR list, a PR screen with
// overview / diff / agent tabs, and modal editors, menus and confirmations.
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

type Deps struct {
	PRs       *pr.Service
	Reviews   *review.Service
	ModelName string // shown to the user; the model itself is behind Reviews
	Cwd       string
	LocalRepo bool // the cwd is a checkout of the PR repo (enables `gh pr checkout`)
	OpenPR    int  // open this PR directly; 0 starts on the list
	Dark      bool
}

type screen int

const (
	screenList screen = iota
	screenPR
)

type Model struct {
	deps   Deps
	width  int
	height int
	viewer string

	screen screen
	list   listModel
	pr     *prModel
	modal  modal

	status    string
	statusErr bool
	statusAt  time.Time
	busy      int
	spinner   spinner.Model
	pendingG  bool

	runs map[string]*agentRun // by PR key (repo#number)

	mdWidth int
	md      *glamour.TermRenderer
}

func New(deps Deps) *Model {
	m := &Model{
		deps:    deps,
		list:    listModel{state: pr.Open},
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(lipgloss.NewStyle().Foreground(purple))),
		runs:    map[string]*agentRun{},
	}
	if deps.OpenPR > 0 {
		m.screen = screenPR
		m.pr = newPRModel(deps.OpenPR)
	}
	return m
}

func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick, m.loadPRs(), m.loadViewer()}
	if m.pr != nil {
		cmds = append(cmds, m.loadPR(m.pr.number))
	}
	return tea.Batch(cmds...)
}

// ---- messages ----

type prsLoadedMsg struct {
	prs []pr.Summary
	err error
}

type viewerMsg struct{ login string }

type prLoadedMsg struct {
	number int
	data   *pr.Details
	err    error
}

type actionDoneMsg struct {
	what    string
	err     error
	refresh bool // reload the open PR and the list afterwards
}

type clearStatusMsg struct{ at time.Time }

func (m *Model) ctx() context.Context { return context.Background() }

func (m *Model) loadPRs() tea.Cmd {
	m.busy++
	m.list.loading = true
	state := m.list.state
	return func() tea.Msg {
		prs, err := m.deps.PRs.List(m.ctx(), state)
		return prsLoadedMsg{prs, err}
	}
}

func (m *Model) loadViewer() tea.Cmd {
	return func() tea.Msg {
		login, _ := m.deps.PRs.Viewer(m.ctx())
		return viewerMsg{login}
	}
}

func (m *Model) loadPR(number int) tea.Cmd {
	m.busy++
	return func() tea.Msg {
		d, err := m.deps.PRs.Load(m.ctx(), number)
		return prLoadedMsg{number, d, err}
	}
}

// act runs a GitHub action in the background and reports it in the status line.
func (m *Model) act(what string, refresh bool, fn func(ctx context.Context) error) tea.Cmd {
	m.busy++
	m.setStatus(what+"…", false)
	return func() tea.Msg {
		return actionDoneMsg{what: what, err: fn(m.ctx()), refresh: refresh}
	}
}

func (m *Model) setStatus(s string, isErr bool) tea.Cmd {
	m.status, m.statusErr, m.statusAt = s, isErr, time.Now()
	at := m.statusAt
	return tea.Tick(6*time.Second, func(time.Time) tea.Msg { return clearStatusMsg{at} })
}

// ---- update ----

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if m.pr != nil {
			m.pr.invalidate()
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case clearStatusMsg:
		if msg.at.Equal(m.statusAt) && !m.statusErr {
			m.status = ""
		}
		return m, nil

	case viewerMsg:
		m.viewer = msg.login
		return m, nil

	case prsLoadedMsg:
		m.busy--
		m.list.loading = false
		if msg.err != nil {
			return m, m.setStatus(msg.err.Error(), true)
		}
		m.list.setPRs(msg.prs)
		return m, nil

	case prLoadedMsg:
		m.busy--
		if m.pr == nil || m.pr.number != msg.number {
			return m, nil
		}
		m.pr.loading = false
		if msg.err != nil {
			m.pr.loadErr = msg.err
			return m, m.setStatus(msg.err.Error(), true)
		}
		m.pr.setData(msg.data)
		return m, nil

	case actionDoneMsg:
		m.busy--
		var cmds []tea.Cmd
		if msg.err != nil {
			cmds = append(cmds, m.setStatus(msg.what+" failed: "+msg.err.Error(), true))
		} else {
			cmds = append(cmds, m.setStatus(msg.what+" ✓", false))
		}
		if msg.refresh {
			cmds = append(cmds, m.loadPRs())
			if m.pr != nil {
				cmds = append(cmds, m.loadPR(m.pr.number))
			}
		}
		return m, tea.Batch(cmds...)

	case editorDoneMsg:
		m.busy--
		if msg.err != nil {
			msg.editor.posting = false
			msg.editor.err = msg.what + " failed: " + msg.err.Error()
			return m, nil
		}
		if m.modal == msg.editor {
			m.modal = nil
		}
		cmds := []tea.Cmd{m.setStatus(msg.what+" ✓", false), m.loadPRs()}
		if m.pr != nil {
			cmds = append(cmds, m.loadPR(m.pr.number))
		}
		return m, tea.Batch(cmds...)

	case agentEventMsg, agentDoneMsg:
		return m, m.handleAgentMsg(msg)
	}

	if m.modal != nil {
		return m, m.modal.update(m, msg)
	}

	if key, ok := msg.(tea.KeyPressMsg); ok {
		return m, m.handleKey(key)
	}
	return m, nil
}

func (m *Model) handleKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if key == "ctrl+c" {
		return m.quit()
	}
	// "gg" is the one two-key vim motion we support globally.
	if m.pendingG {
		m.pendingG = false
		if key == "g" {
			key = "gg"
		}
	} else if key == "g" && !m.textEntry() {
		m.pendingG = true
		return nil
	}
	if key == "?" && !m.textEntry() {
		m.modal = newHelp(m.screen, m.pr)
		return nil
	}

	switch m.screen {
	case screenList:
		return m.updateList(key, k)
	case screenPR:
		return m.updatePR(key)
	}
	return nil
}

// textEntry is true while the list filter captures keystrokes.
func (m *Model) textEntry() bool { return m.screen == screenList && m.list.filtering }

func (m *Model) quit() tea.Cmd {
	for _, r := range m.runs {
		if r.cancel != nil {
			r.cancel()
		}
	}
	return tea.Quit
}

func (m *Model) openPR(number int) tea.Cmd {
	m.screen = screenPR
	m.pr = newPRModel(number)
	return m.loadPR(number)
}

// ---- view ----

func (m *Model) View() tea.View {
	var body string
	if m.width == 0 {
		body = "loading…"
	} else {
		header := m.headerView()
		footer := m.footerView()
		h := m.height - lipgloss.Height(header) - lipgloss.Height(footer)
		var main string
		switch m.screen {
		case screenList:
			main = m.listView(m.width, h)
		case screenPR:
			main = m.prView(m.width, h)
		}
		main = lipgloss.NewStyle().Height(h).MaxHeight(h).Render(main)
		body = lipgloss.JoinVertical(lipgloss.Left, header, main, footer)
		if m.modal != nil {
			body = m.overlay(body, m.modal.view(m, m.width-4, m.height-4))
		}
	}
	v := tea.NewView(body)
	v.AltScreen = true
	return v
}

func (m *Model) headerView() string {
	repo := titleStyle.Render(" review-assist ") + subtleStyle.Render(m.deps.PRs.Repo().Qualified())
	if m.viewer != "" {
		repo += faintStyle.Render("  as @" + m.viewer)
	}
	right := ""
	if m.busy > 0 {
		right = m.spinner.View() + " "
	}
	if n := m.runningAgents(); n > 0 {
		right += lipgloss.NewStyle().Foreground(orange).Render(fmt.Sprintf("agents running: %d ", n))
	}
	gap := m.width - lipgloss.Width(repo) - lipgloss.Width(right)
	return repo + strings.Repeat(" ", max(gap, 1)) + right
}

func (m *Model) footerView() string {
	var status string
	if m.status != "" {
		st := okStyle
		if m.statusErr {
			st = errStyle
		}
		status = st.Render(" " + oneLine(m.status))
	}
	var help string
	switch m.screen {
	case screenList:
		help = helpLine("j/k", "move", "enter", "open", "/", "filter", "s", "state", "r", "refresh", "o", "browser", "?", "help", "q", "quit")
	case screenPR:
		help = m.prHelp()
	}
	return fit(status, m.width) + "\n" + fit(" "+help, m.width)
}

// overlay centres a modal over the screen.
func (m *Model) overlay(bg, fg string) string {
	fgBox := modalStyle.Render(fg)
	x := max((m.width-lipgloss.Width(fgBox))/2, 0)
	y := max((m.height-lipgloss.Height(fgBox))/2, 0)
	return lipgloss.NewCompositor(
		lipgloss.NewLayer(bg),
		lipgloss.NewLayer(fgBox).X(x).Y(y).Z(1),
	).Render()
}

// markdown renders with glow's renderer (glamour), cached per width.
func (m *Model) markdown(s string, width int) string {
	width = max(width, 20)
	if m.md == nil || m.mdWidth != width {
		style := "dark"
		if !m.deps.Dark {
			style = "light"
		}
		r, err := glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(width), glamour.WithEmoji())
		if err != nil {
			return s
		}
		m.md, m.mdWidth = r, width
	}
	out, err := m.md.Render(s)
	if err != nil {
		return s
	}
	return strings.Trim(out, "\n")
}
