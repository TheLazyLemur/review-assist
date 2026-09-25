package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

type listModel struct {
	all       []pr.Summary
	shown     []pr.Summary
	cursor    int
	offset    int
	state     pr.State
	filter    string
	filtering bool
	loading   bool
}

func (l *listModel) setPRs(prs []pr.Summary) {
	l.all = prs
	l.apply()
}

func (l *listModel) apply() {
	l.shown = l.shown[:0]
	f := strings.ToLower(l.filter)
	for _, p := range l.all {
		hay := strings.ToLower(fmt.Sprintf("#%d %s %s %s", p.Number, p.Title, p.Author, p.HeadRef))
		if f == "" || strings.Contains(hay, f) {
			l.shown = append(l.shown, p)
		}
	}
	l.cursor = min(l.cursor, max(len(l.shown)-1, 0))
}

func (l *listModel) selected() (pr.Summary, bool) {
	if l.cursor < 0 || l.cursor >= len(l.shown) {
		return pr.Summary{}, false
	}
	return l.shown[l.cursor], true
}

func (m *Model) updateList(key string, k tea.KeyPressMsg) tea.Cmd {
	l := &m.list
	if l.filtering {
		switch key {
		case "esc":
			l.filtering, l.filter = false, ""
		case "enter":
			l.filtering = false
		case "backspace":
			if r := []rune(l.filter); len(r) > 0 {
				l.filter = string(r[:len(r)-1])
			}
		default:
			if k.Text != "" {
				l.filter += k.Text
			}
		}
		l.apply()
		return nil
	}

	page := max(m.height-6, 1)
	switch key {
	case "q":
		return m.quit()
	case "j", "down":
		l.cursor = min(l.cursor+1, max(len(l.shown)-1, 0))
	case "k", "up":
		l.cursor = max(l.cursor-1, 0)
	case "ctrl+d":
		l.cursor = min(l.cursor+page/2, max(len(l.shown)-1, 0))
	case "ctrl+u":
		l.cursor = max(l.cursor-page/2, 0)
	case "gg":
		l.cursor = 0
	case "G":
		l.cursor = max(len(l.shown)-1, 0)
	case "/":
		l.filtering = true
	case "esc":
		l.filter = ""
		l.apply()
	case "s":
		for i, s := range pr.States {
			if s == l.state {
				l.state = pr.States[(i+1)%len(pr.States)]
				break
			}
		}
		l.cursor = 0
		return m.loadPRs()
	case "r":
		return m.loadPRs()
	case "enter", "l":
		if p, ok := l.selected(); ok {
			return m.openPR(p.Number)
		}
	case "o":
		if p, ok := l.selected(); ok {
			return m.act(fmt.Sprintf("open #%d in browser", p.Number), false, func(ctx context.Context) error {
				return m.deps.PRs.OpenInBrowser(ctx, p.Number)
			})
		}
	}
	return nil
}

func (m *Model) listView(w, h int) string {
	l := &m.list
	var b strings.Builder
	states := make([]string, 0, len(pr.States))
	for _, s := range pr.States {
		if s == l.state {
			states = append(states, activeTab.Render(string(s)))
		} else {
			states = append(states, tabStyle.Render(string(s)))
		}
	}
	b.WriteString(" " + strings.Join(states, "") + "  ")
	switch {
	case l.filtering:
		b.WriteString(keyStyle.Render("/") + l.filter + "█")
	case l.filter != "":
		b.WriteString(faintStyle.Render("filter: ") + l.filter)
	}
	b.WriteString("\n\n")

	rows := h - 2
	if len(l.shown) == 0 {
		msg := "No pull requests."
		if l.loading {
			msg = "Loading pull requests…"
		}
		b.WriteString(subtleStyle.Render("  " + msg))
		return b.String()
	}
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+rows {
		l.offset = l.cursor - rows + 1
	}
	for i := l.offset; i < min(len(l.shown), l.offset+rows); i++ {
		b.WriteString(m.listRow(l.shown[i], i == l.cursor, w) + "\n")
	}
	return b.String()
}

func (m *Model) listRow(p pr.Summary, selected bool, w int) string {
	num := fmt.Sprintf("#%-5d", p.Number)
	stats := addStyle.Render(fmt.Sprintf("+%d", p.Additions)) + " " + delStyle.Render(fmt.Sprintf("-%d", p.Deletions))
	meta := fmt.Sprintf("%s  %s  %s  %s",
		authorStyle.Render("@"+p.Author),
		stateBadge(p.State, p.IsDraft),
		stats,
		faintStyle.Render(ago(p.UpdatedAt)))
	if d := decisionBadge(p.ReviewDecision); d != "" {
		meta += "  " + d
	}
	if m.agentRunFor(p.Number) != nil {
		meta += "  " + lipgloss.NewStyle().Foreground(orange).Render("◆ agent")
	}
	titleW := max(w-lipgloss.Width(meta)-lipgloss.Width(num)-6, 20)
	title := fit(p.Title, titleW)
	cursor := "  "
	if selected {
		cursor = keyStyle.Render("▌ ")
		title = selectedStyle.Render(title)
	}
	return fit(cursor+faintStyle.Render(num)+" "+title+"  "+meta, w)
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case t.IsZero():
		return ""
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
