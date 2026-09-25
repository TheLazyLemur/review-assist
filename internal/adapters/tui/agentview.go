package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

type agentStatus struct {
	name  string
	state review.EventKind
	tools int
	last  string
}

type agentRun struct {
	number  int
	level   review.Level
	cancel  context.CancelFunc
	ch      chan tea.Msg
	agents  []*agentStatus
	started time.Time
	ended   time.Time
	result  *review.Result
	err     error
}

func (r *agentRun) running() bool { return r.ended.IsZero() }

func (r *agentRun) badge() string {
	switch {
	case r.running():
		return lipgloss.NewStyle().Foreground(orange).Render("…")
	case r.err != nil:
		return errStyle.Render("✗")
	default:
		return lipgloss.NewStyle().Foreground(orange).Render(fmt.Sprintf("◆%d", len(r.result.Findings)))
	}
}

// Agent messages carry their run, so a superseded run never touches the
// run that replaced it.
type agentEventMsg struct {
	run *agentRun
	ev  review.Event
}

type agentDoneMsg struct {
	run *agentRun
	res *review.Result
	err error
}

func waitAgent(ch chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m *Model) agentRunFor(number int) *agentRun { return m.runs[m.prKey(number)] }

func (m *Model) runningAgents() int {
	n := 0
	for _, r := range m.runs {
		if r.running() {
			n++
		}
	}
	return n
}

func (m *Model) handleAgentMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case agentEventMsg:
		r := msg.run
		var st *agentStatus
		for _, a := range r.agents {
			if a.name == msg.ev.Agent {
				st = a
			}
		}
		if st == nil {
			st = &agentStatus{name: msg.ev.Agent}
			r.agents = append(r.agents, st)
		}
		switch msg.ev.Kind {
		case review.EventTool:
			st.tools++
			st.last = msg.ev.Detail
		default:
			st.state = msg.ev.Kind
			st.last = msg.ev.Detail
		}
		return waitAgent(r.ch)
	case agentDoneMsg:
		r := msg.run
		r.ended, r.err = time.Now(), msg.err
		if msg.err == nil {
			r.result = msg.res
		}
		if m.pr != nil {
			m.pr.rows = nil // show findings inline
		}
		var status tea.Cmd
		if msg.err != nil {
			status = m.setStatus(fmt.Sprintf("agent review of #%d failed: %v", r.number, msg.err), true)
		} else {
			status = m.setStatus(fmt.Sprintf("agent review of #%d done: %d suggestions", r.number, len(msg.res.Findings)), false)
		}
		return tea.Batch(status, waitAgent(r.ch))
	}
	return nil
}

// agentKey starts a review (level picker) or offers to cancel a running one.
func (m *Model) agentKey() tea.Cmd {
	p := m.pr
	if p == nil || p.data == nil {
		return nil
	}
	if r := m.agentRunFor(p.number); r != nil && r.running() {
		m.modal = &confirmModal{prompt: fmt.Sprintf("Cancel the running agent review of #%d?", p.number), onYes: func() tea.Cmd {
			r.cancel()
			return m.setStatus("cancelling agent review…", false)
		}}
		return nil
	}
	var items []menuItem
	for _, lvl := range review.Levels {
		lvl := lvl
		// Rules-file presence is only known after fetching; count without it.
		n := lvl.Agents(p.data.Files, false)
		label := fmt.Sprintf("%-9s %2d agent(s)  %s", lvl, n, levelBlurb(lvl))
		items = append(items, menuItem{fmt.Sprint(int(lvl)), label, func() tea.Cmd { return m.startAgent(lvl) }})
	}
	mm := &menuModal{title: fmt.Sprintf("Agent review level for #%d  ·  model %s", p.number, m.deps.ModelName), items: items}
	mm.cursor = 1
	m.modal = mm
	return nil
}

func levelBlurb(l review.Level) string {
	switch l {
	case review.LevelQuick:
		return "one correctness pass"
	case review.LevelStandard:
		return "correctness, robustness, security + verifier"
	case review.LevelThorough:
		return "correctness split by files + risk lenses + verifier"
	default:
		return "widest fan-out (up to 10 specialists) + verifier"
	}
}

func (m *Model) startAgent(level review.Level) tea.Cmd {
	p := m.pr
	details := p.data
	ctx, cancel := context.WithCancel(context.Background())
	r := &agentRun{number: p.number, level: level, cancel: cancel, ch: make(chan tea.Msg, 512), started: time.Now()}
	m.runs[m.prKey(p.number)] = r
	p.tab = tabAgent
	p.findCursor, p.agentScroll = 0, 0
	reviews, repo := m.deps.Reviews, m.deps.PRs.Repo()

	go func() {
		defer close(r.ch)
		defer cancel()
		emit := func(ev review.Event) { r.ch <- agentEventMsg{r, ev} }
		res, err := reviews.Review(ctx, repo, details, level, emit)
		r.ch <- agentDoneMsg{r, &res, err}
	}()
	return tea.Batch(waitAgent(r.ch), m.setStatus(fmt.Sprintf("agent review (%s) of #%d started", level, p.number), false))
}

func (m *Model) updateAgentTab(key string) tea.Cmd {
	p := m.pr
	r := m.agentRunFor(p.number)
	if r == nil || r.result == nil {
		if key == "enter" && (r == nil || !r.running()) {
			return m.agentKey()
		}
		return nil
	}
	fs := r.result.Findings
	switch key {
	case "j", "down":
		p.findCursor = min(p.findCursor+1, max(len(fs)-1, 0))
		p.agentScroll = 0
	case "k", "up":
		p.findCursor = max(p.findCursor-1, 0)
		p.agentScroll = 0
	case "gg":
		p.findCursor, p.agentScroll = 0, 0
	case "G":
		p.findCursor, p.agentScroll = max(len(fs)-1, 0), 0
	case "ctrl+d":
		p.agentScroll += 5
	case "ctrl+u":
		p.agentScroll = max(p.agentScroll-5, 0)
	case "enter", "l":
		if len(fs) == 0 {
			return nil
		}
		f := fs[p.findCursor]
		if !f.Anchored {
			m.gotoFile(f.Path)
			return m.setStatus("finding is not on a diff line; opened the file", true)
		}
		m.gotoFinding(f)
	case "c":
		if len(fs) == 0 {
			return nil
		}
		return m.draftFromFinding(fs[p.findCursor])
	}
	return nil
}

func (m *Model) gotoFile(path string) {
	p := m.pr
	for i, f := range p.data.Files {
		if f.Path() == path {
			p.tab = tabDiff
			p.selectFile(i)
			return
		}
	}
}

// draftFromFinding opens the comment editor prefilled with the agent's
// suggestion. The human edits and submits; the agent never posts.
func (m *Model) draftFromFinding(f review.Finding) tea.Cmd {
	p := m.pr
	if !f.Anchored {
		// A code host rejects a line anchor off the diff; anchor to the file.
		return m.openAnchoredEditor(&pr.Anchor{Path: f.Path}, f.Path+" (whole file; line not in diff)  · drafted from agent suggestion", "", f.SuggestedComment)
	}
	label := fmt.Sprintf("%s:%d (%s)", f.Path, f.Line, f.Side)
	quote := ""
	m.gotoFinding(f)
	if l, ok := p.cursorLine(); ok {
		quote = expandTabs(l.Text)
	}
	p.tab = tabAgent
	return m.openAnchoredEditor(&pr.Anchor{Path: f.Path, Line: f.Line, Side: f.Side}, label+"  · drafted from agent suggestion", quote, f.SuggestedComment)
}

func (m *Model) agentView(w, h int) string {
	p := m.pr
	r := m.agentRunFor(p.number)
	if r == nil {
		return m.agentIntro(w)
	}
	var b strings.Builder
	elapsed := time.Since(r.started)
	if !r.running() {
		elapsed = r.ended.Sub(r.started)
	}
	fmt.Fprintf(&b, " level %s · model %s · %s\n", titleStyle.Render(r.level.String()), m.deps.ModelName, elapsed.Round(time.Second))

	if r.running() || r.result == nil {
		b.WriteString("\n")
		for _, a := range r.agents {
			icon := m.spinner.View()
			switch a.state {
			case review.EventDone:
				icon = okStyle.Render("✓")
			case review.EventFailed:
				icon = errStyle.Render("✗")
			}
			line := fmt.Sprintf(" %s %s %s  %s", icon, fit(a.name, 48), faintStyle.Render(fmt.Sprintf("%3d tools", a.tools)), subtleStyle.Render(oneLine(a.last)))
			b.WriteString(fit(line, w) + "\n")
		}
		if r.err != nil {
			b.WriteString("\n" + errStyle.Width(w-2).Render(" review failed: "+r.err.Error()) + "\n")
			b.WriteString(faintStyle.Render(" press A to run again"))
		} else if r.running() {
			b.WriteString("\n" + faintStyle.Render(" Agents are read-only; they only suggest. Press A to cancel."))
		}
		return b.String()
	}
	return b.String() + m.findingsView(r, w, h-1)
}

func (m *Model) agentIntro(w int) string {
	var s strings.Builder
	s.WriteString("# Agent review\n\n")
	s.WriteString("Press **A** (or enter) to run a review with **" + m.deps.ModelName + "**.\n\n")
	s.WriteString("Agents are **read-only**. They read the diff and the repository at the PR's head and base (files, grep, history) and suggest where a comment could go. They never post anything; you decide what to send.\n\n")
	s.WriteString("| level | agents | what runs |\n|---|---|---|\n")
	for _, l := range review.Levels {
		fmt.Fprintf(&s, "| %d %s | %d | %s |\n", int(l), l, l.Agents(m.pr.data.Files, false), levelBlurb(l))
	}
	return m.markdown(s.String(), min(w-4, 110))
}

func (m *Model) findingsView(r *agentRun, w, h int) string {
	p := m.pr
	res := r.result
	var top []string
	if res.Assessment != "" {
		top = append(top, lipgloss.NewStyle().Width(w-1).PaddingLeft(1).Render(titleStyle.Render("Assessment: ")+oneLine(res.Assessment)))
	}
	for _, n := range res.Notes {
		top = append(top, lipgloss.NewStyle().Foreground(orange).Render(fit(" ! "+n, w)))
	}
	for _, f := range res.Failures {
		top = append(top, errStyle.Render(fit(" ✗ "+oneLine(f), w)))
	}
	header := strings.Join(top, "\n")
	if len(res.Findings) == 0 {
		return header + "\n\n" + okStyle.Render(" No suggestions: the agents found nothing they could back with a concrete scenario.")
	}

	listW := min(max(w*2/5, 36), 70)
	detailW := max(w-listW-3, 20)
	bodyH := max(h-lipgloss.Height(header)-1, 3)
	if header == "" {
		bodyH = max(h-1, 3)
	}

	var list []string
	start := max(min(p.findCursor-bodyH/2, len(res.Findings)-bodyH), 0)
	for i := start; i < min(len(res.Findings), start+bodyH); i++ {
		f := res.Findings[i]
		loc := fmt.Sprintf("%s:%d", shortPath(f.Path, 24), f.Line)
		titleW := max(listW-len([]rune(loc))-6, 8)
		if i == p.findCursor {
			list = append(list, keyStyle.Render("▌")+selectedStyle.Render(fit(" ◆ "+fit(f.Title, titleW)+" "+loc, listW-1)))
			continue
		}
		dot := lipgloss.NewStyle().Foreground(severityColor(f.Severity)).Render("◆")
		list = append(list, fit("  "+dot+" "+fit(f.Title, titleW)+" "+faintStyle.Render(loc), listW))
	}

	f := res.Findings[min(p.findCursor, len(res.Findings)-1)]
	anchor := fmt.Sprintf("`%s` line %d (%s)", f.Path, f.Line, strings.ToLower(string(f.Side)))
	if !f.Anchored {
		anchor += " — _not on a diff line; c drafts a file comment_"
	}
	meta := fmt.Sprintf("**%s** · %s confidence", strings.ToUpper(f.Severity), f.Confidence)
	if f.Lens != "" {
		meta += " · " + f.Lens
	}
	md := fmt.Sprintf("## %s\n\n%s\n\n%s\n\n%s\n\n### Suggested comment\n\n> %s\n\n_enter: go to line · c: draft this comment (you edit and post)_",
		f.Title, meta, anchor, f.Explanation,
		strings.ReplaceAll(f.SuggestedComment, "\n", "\n> "))
	detail := strings.Split(m.markdown(md, detailW), "\n")
	p.agentScroll = min(p.agentScroll, max(len(detail)-bodyH, 0))
	detail = detail[p.agentScroll:min(len(detail), p.agentScroll+bodyH)]

	left := lipgloss.NewStyle().Width(listW).Height(bodyH).Render(strings.Join(list, "\n"))
	right := lipgloss.NewStyle().Width(detailW).Height(bodyH).Render(strings.Join(detail, "\n"))
	sep := faintStyle.Render(strings.TrimSuffix(strings.Repeat("│\n", bodyH), "\n"))
	body := lipgloss.JoinHorizontal(lipgloss.Top, left, " ", sep, " ", right)
	if header == "" {
		return body
	}
	return header + "\n" + body
}
