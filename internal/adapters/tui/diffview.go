package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

type rowKind int

const (
	rowHunk rowKind = iota
	rowLine
	rowNote // comment or agent finding rendered under a line
)

type diffRow struct {
	kind rowKind
	line diff.Line
	text string // for hunk and note rows
}

const sidebarWidth = 34

func (p *prModel) file() (diff.File, bool) {
	if p.data == nil || p.fileIdx < 0 || p.fileIdx >= len(p.data.Files) {
		return diff.File{}, false
	}
	return p.data.Files[p.fileIdx], true
}

// commentsAt groups this file's comments by line, plus those about the whole file.
func (p *prModel) commentsAt(path string) (map[string][]pr.Comment, []pr.Comment) {
	byLine := map[string][]pr.Comment{}
	var fileLevel []pr.Comment
	for _, c := range p.data.Comments {
		if c.Anchor == nil || c.Anchor.Path != path {
			continue
		}
		if c.Anchor.Line == 0 {
			fileLevel = append(fileLevel, c)
			continue
		}
		k := fmt.Sprintf("%s:%d", c.Anchor.Side, c.Anchor.Line)
		byLine[k] = append(byLine[k], c)
	}
	return byLine, fileLevel
}

func anchorKey(l diff.Line) string {
	s, n := l.Target()
	return fmt.Sprintf("%s:%d", s, n)
}

func (m *Model) findingsAt(path string) map[string][]review.Finding {
	out := map[string][]review.Finding{}
	r := m.agentRunFor(m.pr.number)
	if r == nil || r.result == nil {
		return out
	}
	for _, f := range r.result.Findings {
		if f.Path == path && f.Anchored {
			k := fmt.Sprintf("%s:%d", f.Side, f.Line)
			out[k] = append(out[k], f)
		}
	}
	return out
}

// buildRows lays out the current file: hunk headers, lines, and notes
// (comments and agent findings) under the lines they anchor to.
func (m *Model) buildRows(width int) {
	p := m.pr
	p.rows = p.rows[:0]
	p.rowsWidth = width
	f, ok := p.file()
	if !ok {
		return
	}
	comments, fileLevel := p.commentsAt(f.Path())
	findings := m.findingsAt(f.Path())
	noteW := max(width-14, 20)

	if !p.hideComments {
		for _, c := range fileLevel {
			p.rows = append(p.rows, diffRow{kind: rowNote, text: commentNote(c, noteW, "file")})
		}
	}
	if f.Binary {
		p.rows = append(p.rows, diffRow{kind: rowHunk, text: "(binary file)"})
	}
	for _, h := range f.Hunks {
		p.rows = append(p.rows, diffRow{kind: rowHunk, text: h.Header})
		for _, l := range h.Lines {
			p.rows = append(p.rows, diffRow{kind: rowLine, line: l})
			if p.hideComments {
				continue
			}
			k := anchorKey(l)
			for _, c := range comments[k] {
				p.rows = append(p.rows, diffRow{kind: rowNote, text: commentNote(c, noteW, "")})
			}
			for _, fd := range findings[k] {
				p.rows = append(p.rows, diffRow{kind: rowNote, text: findingNote(fd, noteW)})
			}
		}
	}
	p.cursor = min(p.cursor, max(len(p.rows)-1, 0))
	p.snapCursor(1)
}

func commentNote(c pr.Comment, w int, kind string) string {
	who := authorStyle.Render("@" + c.Author)
	if kind != "" {
		who = faintStyle.Render("["+kind+"] ") + who
	}
	if c.ReplyTo != 0 {
		who = faintStyle.Render("↳ ") + who
	}
	return lipgloss.NewStyle().Foreground(orange).Render("  ┃ ") + fit(who+" "+commentStyle.Render(oneLine(c.Body)), w)
}

func findingNote(f review.Finding, w int) string {
	bar := lipgloss.NewStyle().Foreground(severityColor(f.Severity)).Render("  ◆ ")
	return bar + fit(lipgloss.NewStyle().Foreground(severityColor(f.Severity)).Bold(true).Render("agent: ")+commentStyle.Render(f.Title), w)
}

// snapCursor moves the cursor onto a line row, searching in direction dir.
func (p *prModel) snapCursor(dir int) {
	for i := p.cursor; i >= 0 && i < len(p.rows); i += dir {
		if p.rows[i].kind == rowLine {
			p.cursor = i
			return
		}
	}
	for i := p.cursor; i >= 0 && i < len(p.rows); i -= dir {
		if p.rows[i].kind == rowLine {
			p.cursor = i
			return
		}
	}
}

func (p *prModel) moveLines(n int) {
	dir := 1
	if n < 0 {
		dir, n = -1, -n
	}
	for ; n > 0; n-- {
		next := p.cursor + dir
		for next >= 0 && next < len(p.rows) && p.rows[next].kind != rowLine {
			next += dir
		}
		if next < 0 || next >= len(p.rows) {
			return
		}
		p.cursor = next
	}
}

func (p *prModel) selectFile(i int) {
	if p.data == nil || i < 0 || i >= len(p.data.Files) {
		return
	}
	p.fileIdx, p.cursor, p.scroll, p.xoff = i, 0, 0, 0
	p.selecting = false
	p.rows = nil
}

func (m *Model) diffWidth() int {
	if m.pr.hideSidebar {
		return m.width
	}
	return m.width - sidebarWidth - 1
}

func (m *Model) ensureRows() {
	if m.pr.rows == nil || m.pr.rowsWidth != m.diffWidth() {
		m.buildRows(m.diffWidth())
	}
}

func (m *Model) updateDiff(key string) tea.Cmd {
	p := m.pr
	m.ensureRows()
	page := max(m.height-8, 2)
	switch key {
	case "j", "down":
		p.moveLines(1)
	case "k", "up":
		p.moveLines(-1)
	case "ctrl+d":
		p.moveLines(page / 2)
	case "ctrl+u":
		p.moveLines(-page / 2)
	case "ctrl+f", "pgdown":
		p.moveLines(page)
	case "ctrl+b", "pgup":
		p.moveLines(-page)
	case "gg":
		p.cursor = 0
		p.snapCursor(1)
	case "G":
		p.cursor = len(p.rows) - 1
		p.snapCursor(-1)
	case "]", "J":
		p.selectFile(p.fileIdx + 1)
	case "[", "K":
		p.selectFile(p.fileIdx - 1)
	case "}":
		p.jumpHunk(1)
	case "{":
		p.jumpHunk(-1)
	case "h", "left":
		p.xoff = max(p.xoff-8, 0)
	case "l", "right":
		p.xoff += 8
	case "0":
		p.xoff = 0
	case "f":
		p.hideSidebar = !p.hideSidebar
		p.rows = nil
	case "t":
		p.hideComments = !p.hideComments
		p.rows = nil
	case "v", "V":
		if p.selecting {
			p.selecting = false
		} else {
			p.selecting, p.anchor = true, p.cursor
		}
	case "c":
		return m.lineComment()
	case "F":
		return m.fileComment()
	case "R":
		return m.replyOnLine()
	case "D":
		return m.deleteOnLine()
	case "n":
		return m.jumpFinding(1)
	case "N":
		return m.jumpFinding(-1)
	}
	return nil
}

func (p *prModel) jumpHunk(dir int) {
	cur := -1 // header of the hunk the cursor is in
	for i := p.cursor; i >= 0; i-- {
		if p.rows[i].kind == rowHunk {
			cur = i
			break
		}
	}
	target := -1
	if dir > 0 {
		for i := p.cursor + 1; i < len(p.rows); i++ {
			if p.rows[i].kind == rowHunk {
				target = i
				break
			}
		}
	} else {
		for i := cur - 1; i >= 0; i-- {
			if p.rows[i].kind == rowHunk {
				target = i
				break
			}
		}
	}
	if target < 0 {
		return
	}
	p.cursor = target
	p.snapCursor(1)
}

func (p *prModel) cursorLine() (diff.Line, bool) {
	if p.cursor < 0 || p.cursor >= len(p.rows) || p.rows[p.cursor].kind != rowLine {
		return diff.Line{}, false
	}
	return p.rows[p.cursor].line, true
}

// selection returns the first and last selected line rows, in order.
func (p *prModel) selection() (diff.Line, diff.Line) {
	a, b := p.anchor, p.cursor
	if a > b {
		a, b = b, a
	}
	return p.rows[a].line, p.rows[b].line
}

func (m *Model) lineComment() tea.Cmd {
	p := m.pr
	f, ok := p.file()
	if !ok {
		return nil
	}
	line, ok := p.cursorLine()
	if !ok {
		return m.setStatus("move the cursor onto a diff line", true)
	}
	a := &pr.Anchor{Path: f.Path()}
	side, n := line.Target()
	a.Side, a.Line = side, n
	label := fmt.Sprintf("%s:%d (%s)", f.Path(), n, side)
	quote := expandTabs(line.Text)
	if p.selecting {
		first, last := p.selection()
		ss, sn := first.Target()
		es, en := last.Target()
		a.Side, a.Line = es, en
		if !(ss == es && sn == en) {
			a.StartSide, a.StartLine = ss, sn
			label = fmt.Sprintf("%s:%d-%d", f.Path(), sn, en)
			quote = expandTabs(first.Text) + "\n…\n" + expandTabs(last.Text)
		}
		p.selecting = false
	}
	return m.openAnchoredEditor(a, label, quote, "")
}

func (m *Model) openAnchoredEditor(a *pr.Anchor, label, quote, initial string) tea.Cmd {
	n, head := m.pr.number, m.pr.data.PR.HeadSHA
	ctx := faintStyle.Render(label)
	if quote != "" {
		ctx += "\n" + lipgloss.NewStyle().Foreground(midGrey).Render(quote)
	}
	e := newEditor("Comment", ctx, initial, true, func(body string) (string, func(context.Context) error) {
		return "comment on " + label, func(c context.Context) error {
			return m.deps.PRs.PostComment(c, n, pr.NewComment{Body: body, Anchor: a, HeadSHA: head})
		}
	})
	m.modal = e
	return e.focus()
}

func (m *Model) fileComment() tea.Cmd {
	p := m.pr
	f, ok := p.file()
	if !ok {
		return nil
	}
	return m.openAnchoredEditor(&pr.Anchor{Path: f.Path()}, f.Path()+" (whole file)", "", "")
}

func (m *Model) threadOnLine() []pr.Comment {
	p := m.pr
	f, ok := p.file()
	line, ok2 := p.cursorLine()
	if !ok || !ok2 {
		return nil
	}
	byLine, _ := p.commentsAt(f.Path())
	return byLine[anchorKey(line)]
}

func (m *Model) replyOnLine() tea.Cmd {
	thread := m.threadOnLine()
	if len(thread) == 0 {
		return m.setStatus("no comment thread on this line", true)
	}
	last := thread[len(thread)-1]
	n := m.pr.number
	ctx := authorStyle.Render("@"+last.Author) + " " + lipgloss.NewStyle().Foreground(midGrey).Render(fit(oneLine(last.Body), 90))
	e := newEditor("Reply", ctx, "", true, func(body string) (string, func(context.Context) error) {
		return "reply", func(c context.Context) error { return m.deps.PRs.Reply(c, n, last, body) }
	})
	m.modal = e
	return e.focus()
}

func (m *Model) deleteOnLine() tea.Cmd {
	var mine []pr.Comment
	for _, c := range m.threadOnLine() {
		if m.viewer != "" && c.Author == m.viewer {
			mine = append(mine, c)
		}
	}
	if len(mine) == 0 {
		return m.setStatus("none of your comments on this line", true)
	}
	n := m.pr.number
	var items []menuItem
	for i, c := range mine {
		c := c
		items = append(items, menuItem{fmt.Sprint(i + 1), fit(oneLine(c.Body), 60), func() tea.Cmd {
			m.confirmAct("Delete this comment?\n\n"+fit(oneLine(c.Body), 60), "delete comment",
				func(ctx context.Context) error { return m.deps.PRs.DeleteComment(ctx, n, c) })
			return nil
		}})
	}
	m.modal = &menuModal{title: "Delete which comment?", items: items}
	return nil
}

// jumpFinding moves to the next/previous anchored agent finding across files.
func (m *Model) jumpFinding(dir int) tea.Cmd {
	r := m.agentRunFor(m.pr.number)
	if r == nil || r.result == nil || len(r.result.Findings) == 0 {
		return m.setStatus("no agent findings (press A to run a review)", true)
	}
	fs := r.result.Findings
	p := m.pr
	cur := -1
	for i, f := range fs {
		if f.Anchored && p.isAt(f) {
			cur = i
			break
		}
	}
	for step := 1; step <= len(fs); step++ {
		i := ((cur+dir*step)%len(fs) + len(fs)) % len(fs)
		if cur == -1 && dir < 0 {
			i = len(fs) - step
		}
		if fs[i].Anchored {
			p.findCursor = i
			m.gotoFinding(fs[i])
			return nil
		}
	}
	return m.setStatus("no findings anchored to the diff", true)
}

func (p *prModel) isAt(f review.Finding) bool {
	file, ok := p.file()
	line, ok2 := p.cursorLine()
	if !ok || !ok2 || file.Path() != f.Path {
		return false
	}
	s, n := line.Target()
	return s == f.Side && n == f.Line
}

// gotoFinding opens the diff at the finding's line.
func (m *Model) gotoFinding(f review.Finding) {
	p := m.pr
	p.tab = tabDiff
	for i, file := range p.data.Files {
		if file.Path() == f.Path {
			if i != p.fileIdx {
				p.selectFile(i)
			}
			break
		}
	}
	m.ensureRows()
	for i, r := range p.rows {
		if r.kind == rowLine {
			s, n := r.line.Target()
			if s == f.Side && n == f.Line {
				p.cursor = i
				return
			}
		}
	}
}

func (m *Model) diffView(w, h int) string {
	p := m.pr
	m.ensureRows()
	f, ok := p.file()
	if !ok {
		return subtleStyle.Render("  This PR has no file changes.")
	}
	dw := m.diffWidth()

	header := fmt.Sprintf(" %s  %s %s  %s", titleStyle.Render(f.Path()),
		addStyle.Render(fmt.Sprintf("+%d", f.Additions)), delStyle.Render(fmt.Sprintf("-%d", f.Deletions)),
		faintStyle.Render(fmt.Sprintf("file %d/%d", p.fileIdx+1, len(p.data.Files))))
	if f.OldPath != "" && f.OldPath != f.NewPath && !f.Deleted {
		header += faintStyle.Render("  renamed from " + f.OldPath)
	}
	bodyH := h - 1

	if p.cursor < p.scroll+2 {
		p.scroll = max(p.cursor-2, 0)
	}
	if p.cursor >= p.scroll+bodyH-2 {
		p.scroll = p.cursor - bodyH + 3
	}
	p.scroll = max(min(p.scroll, len(p.rows)-bodyH), 0)

	selA, selB := -1, -1
	if p.selecting {
		selA, selB = min(p.anchor, p.cursor), max(p.anchor, p.cursor)
	}
	var lines []string
	for i := p.scroll; i < min(p.scroll+bodyH, len(p.rows)); i++ {
		lines = append(lines, m.renderRow(p.rows[i], i == p.cursor, i >= selA && i <= selB, dw))
	}
	content := fit(header, dw) + "\n" + strings.Join(lines, "\n")
	if p.hideSidebar {
		return content
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, sidebarStyle.Height(h).Render(m.sidebar(h)), content)
}

func (m *Model) renderRow(r diffRow, cursor, selected bool, w int) string {
	switch r.kind {
	case rowHunk:
		return fit(hunkStyle.Render(" "+r.text), w)
	case rowNote:
		return fit(r.text, w)
	}
	l := r.line
	num := func(n int) string {
		if n == 0 {
			return "     "
		}
		return fmt.Sprintf("%5d", n)
	}
	gutter := faintStyle.Render(num(l.OldNo) + " " + num(l.NewNo) + " ")
	marker, st := " ", lipgloss.NewStyle()
	switch l.Kind {
	case diff.Add:
		marker, st = "+", addStyle.Background(addBg)
	case diff.Del:
		marker, st = "-", delStyle.Background(delBg)
	}
	text := ansi.Cut(expandTabs(l.Text), m.pr.xoff, m.pr.xoff+w)
	codeW := max(w-lipgloss.Width(gutter)-2, 1)
	code := st.Render(fit(marker+" "+text, codeW))
	lead := "  "
	switch {
	case cursor:
		lead = keyStyle.Render("▶ ")
		code = lipgloss.NewStyle().Background(cursorBg).Render(ansi.Strip(fit(marker+" "+text, codeW)))
		if l.Kind == diff.Add {
			code = addStyle.Background(cursorBg).Bold(true).Render(ansi.Strip(fit(marker+" "+text, codeW)))
		} else if l.Kind == diff.Del {
			code = delStyle.Background(cursorBg).Bold(true).Render(ansi.Strip(fit(marker+" "+text, codeW)))
		}
	case selected:
		lead = okStyle.Render("┃ ")
		code = lipgloss.NewStyle().Background(selectBg).Render(ansi.Strip(fit(marker+" "+text, codeW)))
	}
	return lead + gutter + code
}

func (m *Model) sidebar(h int) string {
	p := m.pr
	w := sidebarWidth - 1
	var lines []string
	lines = append(lines, faintStyle.Render(fit(fmt.Sprintf(" Files (%d)", len(p.data.Files)), w)))
	findingCount := map[string]int{}
	if r := m.agentRunFor(p.number); r != nil && r.result != nil {
		for _, f := range r.result.Findings {
			findingCount[f.Path]++
		}
	}
	commentCount := map[string]int{}
	for _, c := range p.data.Comments {
		if c.Anchor != nil {
			commentCount[c.Anchor.Path]++
		}
	}
	start := max(p.fileIdx-(h-3)/2, 0)
	for i := start; i < min(len(p.data.Files), start+h-1); i++ {
		f := p.data.Files[i]
		badge := ""
		if n := commentCount[f.Path()]; n > 0 {
			badge += lipgloss.NewStyle().Foreground(orange).Render(fmt.Sprintf(" ┃%d", n))
		}
		if n := findingCount[f.Path()]; n > 0 {
			badge += lipgloss.NewStyle().Foreground(red).Render(fmt.Sprintf(" ◆%d", n))
		}
		name := shortPath(f.Path(), w-ansi.StringWidth(badge)-2)
		if i == p.fileIdx {
			lines = append(lines, fit(selectedStyle.Render(" "+name)+badge, w))
		} else {
			lines = append(lines, fit(" "+name+badge, w))
		}
	}
	return strings.Join(lines, "\n")
}

// shortPath keeps the file name and trims leading directories to fit.
func shortPath(p string, w int) string {
	if ansi.StringWidth(p) <= w || w < 4 {
		return p
	}
	r := []rune(p)
	return "…" + string(r[len(r)-w+1:])
}
