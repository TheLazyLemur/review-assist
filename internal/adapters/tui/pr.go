package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

const (
	tabOverview = iota
	tabDiff
	tabAgent
)

var tabNames = []string{"1 Overview", "2 Diff", "3 Agent"}

type prModel struct {
	number  int
	loading bool
	loadErr error
	data    *pr.Details

	tab int

	// overview
	ovScroll int
	ovCache  []string
	ovWidth  int

	// diff
	fileIdx      int
	rows         []diffRow
	rowsWidth    int
	cursor       int
	scroll       int
	xoff         int
	selecting    bool
	anchor       int
	hideComments bool
	hideSidebar  bool

	// agent
	findCursor  int
	agentScroll int
}

func newPRModel(number int) *prModel {
	return &prModel{number: number, loading: true}
}

func (p *prModel) setData(d *pr.Details) {
	path := ""
	if p.data != nil && p.fileIdx < len(p.data.Files) {
		path = p.data.Files[p.fileIdx].Path()
	}
	p.data, p.loadErr = d, nil
	p.fileIdx = 0
	for i, f := range d.Files {
		if f.Path() == path {
			p.fileIdx = i
		}
	}
	p.invalidate()
}

func (p *prModel) invalidate() {
	p.ovCache = nil
	p.rows = nil
}

func (m *Model) prKey(number int) string {
	return fmt.Sprintf("%s#%d", m.deps.PRs.Repo().Qualified(), number)
}

func (m *Model) updatePR(key string) tea.Cmd {
	p := m.pr
	switch key {
	case "q":
		if p.selecting {
			p.selecting = false
			return nil
		}
		m.screen, m.pr = screenList, nil
		return nil
	case "esc":
		if p.selecting {
			p.selecting = false
			return nil
		}
		m.screen, m.pr = screenList, nil
		return nil
	case "1":
		p.tab = tabOverview
		return nil
	case "2":
		p.tab = tabDiff
		return nil
	case "3":
		p.tab = tabAgent
		return nil
	case "tab":
		p.tab = (p.tab + 1) % len(tabNames)
		return nil
	case "shift+tab":
		p.tab = (p.tab + len(tabNames) - 1) % len(tabNames)
		return nil
	case "r":
		p.loading = true
		return m.loadPR(p.number)
	case "o":
		return m.act("open in browser", false, func(ctx context.Context) error {
			return m.deps.PRs.OpenInBrowser(ctx, p.number)
		})
	}

	if p.data == nil {
		return nil
	}
	switch key {
	case "a":
		return m.approve()
	case "x":
		return m.requestChanges()
	case "C":
		return m.prComment()
	case "m":
		m.modal = m.actionsMenu()
		return nil
	case "A":
		return m.agentKey()
	}

	switch p.tab {
	case tabOverview:
		return m.updateOverview(key)
	case tabDiff:
		return m.updateDiff(key)
	case tabAgent:
		return m.updateAgentTab(key)
	}
	return nil
}

// ---- actions ----

func (m *Model) approve() tea.Cmd {
	return m.verdictEditor(pr.Approve, fmt.Sprintf("Approve #%d", m.pr.number), "Optional message. alt+enter approves.", false)
}

func (m *Model) requestChanges() tea.Cmd {
	return m.verdictEditor(pr.RequestChanges, fmt.Sprintf("Request changes on #%d", m.pr.number), "Explain what needs to change.", true)
}

// verdictEditor warns up front when the verdict will post as a comment.
func (m *Model) verdictEditor(decision pr.Decision, title, hint string, required bool) tea.Cmd {
	p, viewer := m.pr.data.PR, m.viewer
	if m.deps.PRs.VerdictPostsAsComment(p, viewer) {
		hint += "\nThis is your PR, so it posts as a comment headed with the verdict."
	}
	what := strings.ToLower(title[:1]) + title[1:]
	e := newEditor(title, hint, "", required, func(message string) (string, func(context.Context) error) {
		return what, func(ctx context.Context) error {
			_, err := m.deps.PRs.SubmitVerdict(ctx, p, viewer, decision, message)
			return err
		}
	})
	m.modal = e
	return e.focus()
}

func (m *Model) prComment() tea.Cmd {
	n := m.pr.number
	m.modal = newEditor(fmt.Sprintf("Comment on #%d", n), "A comment on the whole pull request. Markdown supported.", "", true,
		func(body string) (string, func(context.Context) error) {
			return fmt.Sprintf("comment on #%d", n), func(ctx context.Context) error {
				return m.deps.PRs.PostComment(ctx, n, pr.NewComment{Body: body})
			}
		})
	return m.modal.(*editorModal).focus()
}

func (m *Model) confirmAct(prompt, what string, fn func(ctx context.Context) error) {
	m.modal = &confirmModal{prompt: prompt, onYes: func() tea.Cmd { return m.act(what, true, fn) }}
}

func (m *Model) actionsMenu() *menuModal {
	p := m.pr
	n := p.number
	info := p.data.PR
	gc := m.deps.PRs
	items := []menuItem{
		{"a", "Approve", m.approve},
		{"x", "Request changes", m.requestChanges},
		{"C", "Add PR comment", m.prComment},
	}
	if info.State == "OPEN" {
		for _, mm := range []struct {
			key, label string
			method     pr.MergeMethod
		}{{"M", "Merge (merge commit)", pr.MergeCommit}, {"S", "Squash and merge", pr.Squash}, {"B", "Rebase and merge", pr.Rebase}} {
			mm := mm
			items = append(items, menuItem{mm.key, mm.label, func() tea.Cmd {
				m.confirmAct(fmt.Sprintf("%s #%d into %s?", mm.label, n, info.BaseRef), strings.ToLower(mm.label)+fmt.Sprintf(" #%d", n),
					func(ctx context.Context) error { return gc.Merge(ctx, n, mm.method) })
				return nil
			}})
		}
		items = append(items, menuItem{"c", "Close PR", func() tea.Cmd {
			m.confirmAct(fmt.Sprintf("Close #%d without merging?", n), fmt.Sprintf("close #%d", n),
				func(ctx context.Context) error { return gc.Close(ctx, n) })
			return nil
		}})
		if info.IsDraft {
			items = append(items, menuItem{"R", "Mark ready for review", func() tea.Cmd {
				return m.act(fmt.Sprintf("mark #%d ready", n), true, func(ctx context.Context) error { return gc.MarkReady(ctx, n) })
			}})
		} else {
			items = append(items, menuItem{"D", "Convert to draft", func() tea.Cmd {
				m.confirmAct(fmt.Sprintf("Convert #%d to draft?", n), fmt.Sprintf("convert #%d to draft", n),
					func(ctx context.Context) error { return gc.ConvertToDraft(ctx, n) })
				return nil
			}})
		}
	}
	if info.State == "CLOSED" {
		items = append(items, menuItem{"O", "Reopen PR", func() tea.Cmd {
			return m.act(fmt.Sprintf("reopen #%d", n), true, func(ctx context.Context) error { return gc.Reopen(ctx, n) })
		}})
	}
	if m.deps.Remote != "" {
		items = append(items, menuItem{"k", "Check out branch locally", func() tea.Cmd {
			m.confirmAct(fmt.Sprintf("Check out the branch of #%d in %s?", n, m.deps.Cwd), fmt.Sprintf("checkout #%d", n),
				func(ctx context.Context) error { return gc.Checkout(ctx, n) })
			return nil
		}})
	}
	if mine := m.myPRComments(); len(mine) > 0 {
		items = append(items, menuItem{"d", "Delete one of my PR comments", func() tea.Cmd {
			m.modal = m.deletePRCommentMenu(mine)
			return nil
		}})
	}
	items = append(items,
		menuItem{"A", "Run agent review", m.agentKey},
		menuItem{"o", "Open in browser", func() tea.Cmd {
			return m.act("open in browser", false, func(ctx context.Context) error { return gc.OpenInBrowser(ctx, n) })
		}},
		menuItem{"r", "Refresh", func() tea.Cmd { return m.loadPR(n) }},
	)
	return &menuModal{title: fmt.Sprintf("Actions for #%d", n), items: items}
}

// myPRComments lists the viewer's deletable comments on the whole pull request.
func (m *Model) myPRComments() []pr.Comment {
	var mine []pr.Comment
	for _, c := range m.pr.data.Comments {
		if c.Anchor == nil && c.ID != 0 && m.viewer != "" && c.Author == m.viewer {
			mine = append(mine, c)
		}
	}
	return mine
}

func (m *Model) deletePRCommentMenu(mine []pr.Comment) *menuModal {
	n := m.pr.number
	var items []menuItem
	for i, c := range mine {
		key := ""
		if i < 9 {
			key = fmt.Sprint(i + 1)
		}
		items = append(items, menuItem{key, fit(oneLine(c.Body), 60), func() tea.Cmd {
			m.confirmAct("Delete this comment?\n\n"+fit(oneLine(c.Body), 60), "delete comment",
				func(ctx context.Context) error { return m.deps.PRs.DeleteComment(ctx, n, c) })
			return nil
		}})
	}
	return &menuModal{title: "Delete which comment?", items: items}
}

// ---- view ----

func (m *Model) prView(w, h int) string {
	p := m.pr
	var tabs []string
	for i, name := range tabNames {
		if i == tabAgent {
			if r := m.agentRunFor(p.number); r != nil {
				name += " " + r.badge()
			}
		}
		if i == p.tab {
			tabs = append(tabs, activeTab.Render(name))
		} else {
			tabs = append(tabs, tabStyle.Render(name))
		}
	}
	title := fmt.Sprintf("#%d", p.number)
	if p.data != nil {
		title += " " + p.data.PR.Title
	}
	head := " " + titleStyle.Render(fit(title, max(w-lipgloss.Width(strings.Join(tabs, ""))-4, 10))) + "  " + strings.Join(tabs, "")
	bodyH := h - 2

	var body string
	switch {
	case p.data == nil && p.loadErr != nil:
		body = errStyle.Render("  " + p.loadErr.Error())
	case p.data == nil:
		body = subtleStyle.Render("  " + m.spinner.View() + " loading PR…")
	case p.tab == tabOverview:
		body = m.overviewView(w, bodyH)
	case p.tab == tabDiff:
		body = m.diffView(w, bodyH)
	case p.tab == tabAgent:
		body = m.agentView(w, bodyH)
	}
	return head + "\n\n" + body
}

func (m *Model) prHelp() string {
	p := m.pr
	common := []string{"1/2/3", "tabs", "a", "approve", "x", "req changes", "C", "comment", "m", "actions", "A", "agent", "?", "help", "q", "back"}
	switch p.tab {
	case tabDiff:
		if p.selecting {
			return helpLine("j/k", "extend", "c", "comment on range", "esc", "cancel")
		}
		return helpLine(append([]string{"j/k", "line", "]/[", "file", "}/{", "hunk", "c", "comment", "v", "select", "n/N", "finding"}, common...)...)
	case tabAgent:
		return helpLine(append([]string{"j/k", "finding", "enter", "go to line", "c", "draft comment"}, common...)...)
	default:
		return helpLine(append([]string{"j/k", "scroll"}, common...)...)
	}
}

// ---- overview ----

func (m *Model) updateOverview(key string) tea.Cmd {
	p := m.pr
	page := max(m.height-8, 1)
	switch key {
	case "j", "down":
		p.ovScroll++
	case "k", "up":
		p.ovScroll--
	case "ctrl+d":
		p.ovScroll += page / 2
	case "ctrl+u":
		p.ovScroll -= page / 2
	case "ctrl+f", "pgdown":
		p.ovScroll += page
	case "ctrl+b", "pgup":
		p.ovScroll -= page
	case "gg":
		p.ovScroll = 0
	case "G":
		p.ovScroll = 1 << 30
	case "enter", "l":
		p.tab = tabDiff
	}
	p.ovScroll = max(p.ovScroll, 0)
	return nil
}

func (m *Model) overviewView(w, h int) string {
	p := m.pr
	if p.ovCache == nil || p.ovWidth != w {
		p.ovCache = strings.Split(m.markdown(overviewMarkdown(p.data), w-4), "\n")
		p.ovWidth = w
	}
	p.ovScroll = min(p.ovScroll, max(len(p.ovCache)-h, 0))
	end := min(p.ovScroll+h, len(p.ovCache))
	return strings.Join(p.ovCache[p.ovScroll:end], "\n")
}

func overviewMarkdown(d *pr.Details) string {
	info := d.PR
	var b strings.Builder
	fmt.Fprintf(&b, "**#%d** by **@%s** · `%s` ← `%s` · **+%d −%d** in %d files · %s",
		info.Number, info.Author, info.BaseRef, info.HeadRef, info.Additions, info.Deletions, info.ChangedFiles, strings.ToLower(info.State))
	if info.IsDraft {
		b.WriteString(" · draft")
	}
	if info.ReviewDecision != "" {
		b.WriteString(" · " + strings.ToLower(strings.ReplaceAll(info.ReviewDecision, "_", " ")))
	}
	if info.Mergeable != "" && info.State == "OPEN" {
		b.WriteString(" · mergeable: " + strings.ToLower(info.Mergeable))
	}
	b.WriteString("\n\n")
	if len(info.Labels) > 0 {
		var ls []string
		for _, l := range info.Labels {
			ls = append(ls, "`"+l+"`")
		}
		b.WriteString("Labels: " + strings.Join(ls, " ") + "\n\n")
	}

	b.WriteString("## Description\n\n")
	if strings.TrimSpace(info.Body) == "" {
		b.WriteString("_No description._\n\n")
	} else {
		b.WriteString(info.Body + "\n\n")
	}

	if len(info.Checks) > 0 {
		b.WriteString("## Checks\n\n")
		for _, c := range info.Checks {
			icon := "•"
			switch c.Result {
			case "success", "neutral", "skipped":
				icon = "✓"
			case "failure", "error", "cancelled", "timed_out", "action_required":
				icon = "✗"
			}
			fmt.Fprintf(&b, "- %s %s — %s\n", icon, c.Name, c.Result)
		}
		b.WriteString("\n")
	}

	type entry struct {
		at   string
		text string
	}
	var entries []entry
	verdictWords := map[pr.Decision]string{pr.Approve: "approved", pr.RequestChanges: "changes requested"}
	for _, v := range info.Verdicts {
		text := fmt.Sprintf("### @%s — %s\n\n%s\n", v.Author, verdictWords[v.Decision], v.Message)
		entries = append(entries, entry{v.At.Format("2006-01-02 15:04"), text})
	}
	onCode := 0
	for _, c := range d.Comments {
		if c.Anchor != nil {
			onCode++
			continue
		}
		text := fmt.Sprintf("### @%s commented\n\n%s\n", c.Author, c.Body)
		entries = append(entries, entry{c.CreatedAt.Format("2006-01-02 15:04"), text})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].at < entries[j].at })
	if len(entries) > 0 {
		b.WriteString("## Conversation\n\n")
		for _, e := range entries {
			b.WriteString(e.text + "\n_" + e.at + "_\n\n")
		}
	}
	fmt.Fprintf(&b, "---\n\n%d comment(s) on code — press `2` for the diff.\n", onCode)
	return b.String()
}
