package tui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type modal interface {
	update(m *Model, msg tea.Msg) tea.Cmd
	view(m *Model, maxW, maxH int) string
}

// ---- editor ----

type editorModal struct {
	title       string
	context     string // rendered above the text area (e.g. the code being commented on)
	ta          textarea.Model
	required    bool
	submit      func(body string) tea.Cmd
	armedCancel bool
	err         string
}

func newEditor(title, context, initial string, required bool, submit func(string) tea.Cmd) *editorModal {
	ta := textarea.New()
	ta.Placeholder = "Write in Markdown…"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.SetValue(initial)
	return &editorModal{title: title, context: context, ta: ta, required: required, submit: submit}
}

func (e *editorModal) focus() tea.Cmd { return e.ta.Focus() }

func (e *editorModal) update(m *Model, msg tea.Msg) tea.Cmd {
	if k, ok := msg.(tea.KeyPressMsg); ok {
		switch k.String() {
		// ctrl+s is taken by multiplexers such as zellij; alt+enter is not.
		case "alt+enter", "ctrl+enter", "ctrl+s":
			body := strings.TrimSpace(e.ta.Value())
			if e.required && body == "" {
				e.err = "a message is required"
				return nil
			}
			m.modal = nil
			return e.submit(body)
		case "esc":
			if strings.TrimSpace(e.ta.Value()) != "" && !e.armedCancel {
				e.armedCancel = true
				e.err = "esc again to discard"
				return nil
			}
			m.modal = nil
			return nil
		case "ctrl+c":
			return m.quit()
		}
		e.armedCancel, e.err = false, ""
	}
	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(msg)
	return cmd
}

func (e *editorModal) view(m *Model, maxW, maxH int) string {
	w := min(maxW-4, 100)
	e.ta.SetWidth(w)
	ctxLines := 0
	var b strings.Builder
	b.WriteString(titleStyle.Render(e.title) + "\n")
	if e.context != "" {
		c := lipgloss.NewStyle().Width(w).Render(e.context)
		ctxLines = lipgloss.Height(c)
		b.WriteString(c + "\n")
	}
	e.ta.SetHeight(max(min(12, maxH-ctxLines-6), 3))
	b.WriteString("\n" + e.ta.View() + "\n\n")
	if e.err != "" {
		b.WriteString(errStyle.Render(e.err) + "  ")
	}
	b.WriteString(helpLine("alt+enter", "submit", "esc", "cancel"))
	return b.String()
}

// ---- confirm ----

type confirmModal struct {
	prompt string
	onYes  func() tea.Cmd
}

func (c *confirmModal) update(m *Model, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "y", "Y":
		m.modal = nil
		return c.onYes()
	case "n", "N", "esc", "q":
		m.modal = nil
	case "ctrl+c":
		return m.quit()
	}
	return nil
}

func (c *confirmModal) view(m *Model, maxW, maxH int) string {
	return lipgloss.NewStyle().Width(min(maxW-4, 70)).Render(c.prompt) + "\n\n" + helpLine("y", "yes", "n", "no")
}

// ---- menu ----

type menuItem struct {
	key   string
	label string
	run   func() tea.Cmd
}

type menuModal struct {
	title  string
	items  []menuItem
	cursor int
}

func (mm *menuModal) update(m *Model, msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return nil
	}
	key := k.String()
	switch key {
	case "j", "down":
		mm.cursor = min(mm.cursor+1, len(mm.items)-1)
		return nil
	case "k", "up":
		mm.cursor = max(mm.cursor-1, 0)
		return nil
	case "G":
		mm.cursor = len(mm.items) - 1
		return nil
	case "esc", "q":
		m.modal = nil
		return nil
	case "ctrl+c":
		return m.quit()
	case "enter", "l":
		m.modal = nil
		return mm.items[mm.cursor].run()
	}
	for _, it := range mm.items {
		if it.key != "" && it.key == key {
			m.modal = nil
			return it.run()
		}
	}
	return nil
}

func (mm *menuModal) view(m *Model, maxW, maxH int) string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(mm.title) + "\n\n")
	for i, it := range mm.items {
		key := "   "
		if it.key != "" {
			key = keyStyle.Render(it.key) + strings.Repeat(" ", 3-len(it.key))
		}
		label := it.label
		if i == mm.cursor {
			label = selectedStyle.Render(" " + label + " ")
		} else {
			label = " " + label
		}
		b.WriteString(key + label + "\n")
	}
	b.WriteString("\n" + helpLine("j/k", "move", "enter", "select", "esc", "close"))
	return b.String()
}

// ---- help ----

type helpModal struct{ text string }

func newHelp(s screen, p *prModel) *helpModal {
	sections := [][]string{
		{"Global", "ctrl+c", "quit", "?", "this help", "q / esc", "back / quit", "gg / G", "top / bottom", "ctrl+d / ctrl+u", "half page down / up"},
	}
	if s == screenList {
		sections = append(sections, []string{"PR list", "j / k", "move", "enter / l", "open PR", "/", "filter (esc clears)", "s", "cycle open/closed/merged/all", "r", "refresh", "o", "open in browser"})
	} else {
		sections = append(sections,
			[]string{"PR", "1 2 3 / tab", "overview, diff, agent", "a", "approve", "x", "request changes", "C", "PR-level comment", "m", "all actions (merge, close, draft, checkout…)", "A", "run / cancel agent review", "r", "refresh", "o", "open in browser"},
			[]string{"Diff", "j / k", "next / previous line", "] / [", "next / previous file", "} / {", "next / previous hunk", "h / l", "scroll left / right", "0", "reset horizontal scroll", "f", "file list on/off", "c", "comment on line (or selection)", "v / V", "start visual selection", "F", "comment on whole file", "R", "reply to thread on line", "D", "delete my comment on line", "t", "show/hide comments", "n / N", "next / previous agent finding"},
			[]string{"Agent", "j / k", "select finding", "enter", "jump to line in diff", "c", "draft a comment from the suggestion (you edit and post it)", "ctrl+d / ctrl+u", "scroll the explanation"},
		)
	}
	render := func(secs [][]string) string {
		var b strings.Builder
		for _, sec := range secs {
			b.WriteString(titleStyle.Render(sec[0]) + "\n")
			for i := 1; i+1 < len(sec); i += 2 {
				b.WriteString("  " + keyStyle.Render(fit(sec[i], 16)) + " " + sec[i+1] + "\n")
			}
			b.WriteString("\n")
		}
		return b.String()
	}
	half := (len(sections) + 1) / 2
	cols := lipgloss.JoinHorizontal(lipgloss.Top, render(sections[:half]), "    ", render(sections[half:]))
	return &helpModal{text: cols + "\n" + faintStyle.Render("Agents are read-only: they only suggest. Nothing is posted unless you submit it.")}
}

func (h *helpModal) update(m *Model, msg tea.Msg) tea.Cmd {
	if _, ok := msg.(tea.KeyPressMsg); ok {
		m.modal = nil
	}
	return nil
}

func (h *helpModal) view(m *Model, maxW, maxH int) string { return h.text }
