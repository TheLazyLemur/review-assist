package tui

import (
	"fmt"
	"image"
	"image/color"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/TheLazyLemur/review-assist/internal/core/diff"
	"github.com/TheLazyLemur/review-assist/internal/core/pr"
	"github.com/TheLazyLemur/review-assist/internal/core/review"
)

func (k *Kitty) pr(c *canvas, r image.Rectangle) {
	m, p := k.m, k.m.pr
	pad, headH := c.pt(16), c.pt(52)
	head := image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+headH)

	labels := append([]string(nil), tabNames...)
	if run := m.agentRunFor(p.number); run != nil {
		switch {
		case run.running():
			labels[tabAgent] += "  ·  running"
		case run.err != nil:
			labels[tabAgent] += "  ·  failed"
		default:
			labels[tabAgent] += fmt.Sprintf("  ·  %d", len(run.result.Findings))
		}
	}
	tabsW := c.measuring().segments(0, 0, labels, p.tab)
	c.segments(r.Max.X-pad-tabsW, head.Min.Y+(headH-c.pt(28))/2, labels, p.tab)

	x := r.Min.X + pad
	x += c.textIn(image.Rect(x, head.Min.Y, head.Max.X, head.Max.Y), fmt.Sprintf("#%d", p.number), c.font(mono, 15), pFaint) + c.pt(10)
	if p.data != nil {
		state, col := stateTint(p.data.PR.State, p.data.PR.IsDraft)
		pillW := c.measuring().tint(0, 0, state, col)
		f := c.font(sansBold, 17)
		x += c.textIn(image.Rect(x, head.Min.Y, head.Max.X, head.Max.Y),
			f.fit(p.data.PR.Title, r.Max.X-pad-tabsW-x-pillW-c.pt(30)), f, pText) + c.pt(12)
		c.tint(x, head.Min.Y+(headH-c.pt(18))/2, state, col)
	}
	c.fill(image.Rect(r.Min.X, head.Max.Y-hair(c), r.Max.X, head.Max.Y), pLine)

	body := image.Rect(r.Min.X, head.Max.Y, r.Max.X, r.Max.Y)
	c = c.clip(body)
	f := c.font(sans, 13)
	switch {
	case p.data == nil && p.loadErr != nil:
		for i, line := range f.wrap(p.loadErr.Error(), body.Dx()-2*pad) {
			c.text(body.Min.X+pad, body.Min.Y+pad+i*c.pt(20), line, f, pRed)
		}
	case p.data == nil:
		c.spinner(body.Min.X+pad+c.pt(8), body.Min.Y+pad+c.pt(9), c.pt(7), spinPhase())
		c.text(body.Min.X+pad+c.pt(26), body.Min.Y+pad, "Loading pull request…", f, pMuted)
	case p.tab == tabOverview:
		k.overview(c, body)
	case p.tab == tabDiff:
		k.diff(c, body)
	case p.tab == tabAgent:
		k.agent(c, body)
	}
}

// panel draws a card around body, which returns its bottom edge, and returns
// the card's bottom edge.
func (c *canvas) panel(x, y, w int, fill color.RGBA, body func(c *canvas, x, y, w int) int) int {
	pad := c.pt(14)
	bottom := body(c.measuring(), x+pad, y+pad, w-2*pad) + pad
	c.round(image.Rect(x, y, x+w, bottom), c.pt(8), fill)
	body(c, x+pad, y+pad, w-2*pad)
	return bottom
}

// caption is a small upper-case heading. It returns the y below it.
func (c *canvas) caption(x, y int, s string) int {
	c.text(x, y, strings.ToUpper(s), c.font(sansBold, 10), pFaint)
	return y + c.pt(20)
}

func (c *canvas) diffstat(x, y int, add, del int) int {
	f := c.font(mono, 11.5)
	start := x
	x += c.text(x, y, fmt.Sprintf("+%d", add), f, pGreen) + c.pt(8)
	x += c.text(x, y, fmt.Sprintf("−%d", del), f, pRed) + c.pt(10)
	c.bar(image.Rect(x, y+f.height/2-c.pt(3), x+c.pt(44), y+f.height/2+c.pt(3)), add, del)
	return x + c.pt(44) - start
}

// ---- overview ----

func (k *Kitty) overview(c *canvas, r image.Rectangle) {
	p := k.m.pr
	pad := c.pt(16)
	sideW := c.pt(300)
	if r.Dx() < c.pt(760) {
		sideW = 0
	}
	mainW := min(r.Dx()-sideW-3*pad, c.pt(940))
	x, top := r.Min.X+pad, r.Min.Y+pad

	total := k.overviewMain(c.measuring(), p.data, 0, 0, mainW)
	viewH := r.Max.Y - top - pad/2
	step := k.scrollStep()
	maxScroll := max(total-viewH, 0)
	p.ovScroll = min(p.ovScroll, (maxScroll+step-1)/step)
	off := min(p.ovScroll*step, maxScroll)
	k.overviewMain(c.clip(image.Rect(x, top, x+mainW, r.Max.Y)), p.data, x, top-off, mainW)
	c.scrollbar(image.Rect(x+mainW+c.pt(5), top, x+mainW+c.pt(10), r.Max.Y-pad/2), total, viewH, off)
	if sideW > 0 {
		k.overviewSide(c, p.data, x+mainW+pad+c.pt(6), top, sideW)
	}
}

type conversationEntry struct {
	at      time.Time
	author  string
	body    string
	verdict pr.Decision // "" for a comment
}

func (k *Kitty) overviewMain(c *canvas, d *pr.Details, x, y, w int) int {
	info := d.PR
	body := mdText{size: 13, col: pText}
	y = c.panel(x, y, w, pPanel, func(c *canvas, x, y, w int) int {
		y = c.caption(x, y, "Description")
		if strings.TrimSpace(info.Body) == "" {
			c.text(x, y, "No description.", c.font(sansItalic, 13), pFaint)
			return y + c.pt(20)
		}
		return y + c.markdown(info.Body, x, y, w, body)
	}) + c.pt(14)

	if len(info.Checks) > 0 {
		y = c.panel(x, y, w, pPanel, func(c *canvas, x, y, w int) int {
			y = c.caption(x, y, "Checks")
			f := c.font(sans, 12.5)
			for _, ch := range info.Checks {
				cy := y + c.pt(11)
				switch ch.Result {
				case "success", "neutral", "skipped":
					c.check(x+c.pt(7), cy, c.pt(11), pGreen)
				case "failure", "error", "cancelled", "timed_out", "action_required":
					c.cross(x+c.pt(7), cy, c.pt(11), pRed)
				default:
					c.disc(x+c.pt(7), cy, c.pt(3.5), pYellow)
				}
				c.text(x+c.pt(22), y+(c.pt(22)-f.height)/2, f.fit(ch.Name, w-c.pt(160)), f, pText)
				c.textRight(x+w, y+(c.pt(22)-f.height)/2, ch.Result, f, pFaint)
				y += c.pt(24)
			}
			return y - c.pt(4)
		}) + c.pt(14)
	}

	var entries []conversationEntry
	for _, v := range info.Verdicts {
		entries = append(entries, conversationEntry{v.At, v.Author, v.Message, v.Decision})
	}
	onCode := 0
	for _, cm := range d.Comments {
		if cm.Anchor != nil {
			onCode++
			continue
		}
		entries = append(entries, conversationEntry{at: cm.CreatedAt, author: cm.Author, body: cm.Body})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].at.Before(entries[j].at) })
	if len(entries) > 0 {
		y = c.caption(x+c.pt(2), y+c.pt(4), "Conversation")
	}
	for _, e := range entries {
		accent, verb, verbCol := pLine, "commented", pFaint
		switch e.verdict {
		case pr.Approve:
			accent, verb, verbCol = pGreen, "approved", pGreen
		case pr.RequestChanges:
			accent, verb, verbCol = pRed, "requested changes", pRed
		}
		pad := c.pt(14)
		draw := func(c *canvas, x, y, w int) int {
			f := c.font(sansBold, 12.5)
			c.avatar(x, y, c.pt(20), e.author)
			tx := x + c.pt(28)
			tx += c.text(tx, y+(c.pt(20)-f.height)/2, e.author, f, pLilac) + c.pt(8)
			if e.verdict == "" {
				c.text(tx, y+(c.pt(20)-f.height)/2, verb, c.font(sans, 12.5), verbCol)
			} else {
				c.tint(tx, y+c.pt(1), verb, verbCol)
			}
			c.textRight(x+w, y+(c.pt(20)-f.height)/2, e.at.Format("2 Jan 2006 15:04"), c.font(sans, 11.5), pFaint)
			y += c.pt(20)
			if strings.TrimSpace(e.body) != "" {
				y += c.pt(10)
				y += c.markdown(e.body, x, y, w, body)
			}
			return y
		}
		bottom := draw(c.measuring(), x+pad, y+pad, w-2*pad) + pad
		c.card(image.Rect(x, y, x+w, bottom), accent, pPanel)
		draw(c, x+pad, y+pad, w-2*pad)
		y = bottom + c.pt(10)
	}
	f := c.font(sans, 12)
	tx := x + c.pt(2) + c.text(x+c.pt(2), y+c.pt(6), fmt.Sprintf("%d comment(s) on code — press", onCode), f, pFaint) + c.pt(6)
	tx += c.keycap(tx, y+c.pt(5), "2") + c.pt(6)
	c.text(tx, y+c.pt(6), "for the diff", f, pFaint)
	return y + c.pt(34)
}

func (k *Kitty) overviewSide(c *canvas, d *pr.Details, x, y, w int) {
	info := d.PR
	c.panel(x, y, w, pPanel, func(c *canvas, x, y, w int) int {
		f := c.font(sans, 12.5)
		y = c.caption(x, y, "Author")
		c.avatar(x, y, c.pt(22), info.Author)
		c.text(x+c.pt(30), y+(c.pt(22)-f.height)/2, info.Author, c.font(sansBold, 12.5), pLilac)
		y += c.pt(36)

		y = c.caption(x, y, "Branches")
		branch := c.font(mono, 11)
		for _, b := range [][2]string{{"from", info.HeadRef}, {"into", info.BaseRef}} {
			c.text(x, y+c.pt(2), b[0], f, pFaint)
			s := branch.fit(b[1], w-c.pt(56))
			bw := branch.width(s) + c.pt(12)
			c.round(image.Rect(x+c.pt(40), y, x+c.pt(40)+bw, y+c.pt(20)), c.pt(4), blend(pBg, pBlue, 0.12))
			c.text(x+c.pt(46), y+(c.pt(20)-branch.height)/2, s, branch, pBlue)
			y += c.pt(25)
		}
		y += c.pt(11)

		y = c.caption(x, y, "Changes")
		dw := c.diffstat(x, y, info.Additions, info.Deletions)
		c.text(x+dw+c.pt(10), y-c.pt(1), fmt.Sprintf("%d files", info.ChangedFiles), f, pMuted)
		y += c.pt(32)

		y = c.caption(x, y, "Status")
		state, col := stateTint(info.State, info.IsDraft)
		px := x + c.tint(x, y, state, col) + c.pt(6)
		if s, col := decisionTint(info.ReviewDecision); s != "" {
			c.tint(px, y, s, col)
		}
		y += c.pt(24)
		if info.Mergeable != "" && info.State == "OPEN" {
			c.text(x, y, "mergeable: "+strings.ToLower(info.Mergeable), f, pMuted)
			y += c.pt(22)
		}
		y += c.pt(8)

		if len(info.Labels) > 0 {
			y = c.caption(x, y, "Labels")
			lx := x
			for _, l := range info.Labels {
				lw := c.measuring().tint(0, 0, l, pBlue)
				if lx+lw > x+w && lx > x {
					lx, y = x, y+c.pt(23)
				}
				lx += c.tint(lx, y, l, pBlue) + c.pt(6)
			}
			y += c.pt(32)
		}

		if len(info.Checks) > 0 {
			y = c.caption(x, y, "Checks")
			passed := 0
			seg := (w - (len(info.Checks)-1)*c.pt(3)) / len(info.Checks)
			for i, ch := range info.Checks {
				col := pYellow
				switch ch.Result {
				case "success", "neutral", "skipped":
					col = pGreen
					passed++
				case "failure", "error", "cancelled", "timed_out", "action_required":
					col = pRed
				}
				sx := x + i*(seg+c.pt(3))
				c.round(image.Rect(sx, y, sx+seg, y+c.pt(6)), c.pt(3), col)
			}
			y += c.pt(14)
			c.text(x, y, fmt.Sprintf("%d of %d passing", passed, len(info.Checks)), f, pMuted)
			y += c.pt(22)
		}
		return y - c.pt(6)
	})
}

// ---- diff ----

func (k *Kitty) diff(c *canvas, r image.Rectangle) {
	m, p := k.m, k.m.pr
	m.ensureRows()
	f, ok := p.file()
	if !ok {
		c.text(r.Min.X+c.pt(16), r.Min.Y+c.pt(16), "This PR has no file changes.", c.font(sans, 13), pMuted)
		return
	}
	x := r.Min.X
	if !p.hideSidebar {
		side := image.Rect(x, r.Min.Y, x+c.pt(290), r.Max.Y)
		k.fileList(c.clip(side), side)
		x = side.Max.X
	}

	head := image.Rect(x, r.Min.Y, r.Max.X, r.Min.Y+c.pt(38))
	c.fill(head, pPanel)
	c.fill(image.Rect(head.Min.X, head.Max.Y-hair(c), head.Max.X, head.Max.Y), pLine)
	hx := head.Min.X + c.pt(16)
	dir, base := path.Split(f.Path())
	hx += c.textIn(image.Rect(hx, head.Min.Y, head.Max.X, head.Max.Y), dir, c.font(mono, 12.5), pFaint)
	hx += c.textIn(image.Rect(hx, head.Min.Y, head.Max.X, head.Max.Y), base, c.font(monoBold, 12.5), pText) + c.pt(16)
	hx += c.diffstat(hx, head.Min.Y+(head.Dy()-c.font(mono, 11.5).height)/2, f.Additions, f.Deletions) + c.pt(14)
	small := c.font(sans, 11.5)
	if f.OldPath != "" && f.OldPath != f.NewPath && !f.Deleted {
		c.textIn(image.Rect(hx, head.Min.Y, head.Max.X, head.Max.Y), "renamed from "+f.OldPath, small, pFaint)
	}
	c.textRight(head.Max.X-c.pt(22), head.Min.Y+(head.Dy()-small.height)/2, fmt.Sprintf("file %d of %d", p.fileIdx+1, len(p.data.Files)), small, pFaint)

	mapW := c.pt(12)
	rows := image.Rect(x, head.Max.Y, r.Max.X-mapW, r.Max.Y)
	ys := k.diffRows(c.clip(rows), rows)

	// The minimap: the whole file's rows squeezed into the view's height.
	strip := image.Rect(rows.Max.X, rows.Min.Y, r.Max.X, r.Max.Y)
	c.fill(strip, pPanel)
	total := max(ys[len(ys)-1], rows.Dy())
	at := func(y int) int { return strip.Min.Y + y*strip.Dy()/total }
	for i, row := range p.rows {
		var col color.RGBA
		switch {
		case row.kind == rowNote:
			col = pOrange
		case row.kind == rowLine && row.line.Kind == diff.Add:
			col = pGreen
		case row.kind == rowLine && row.line.Kind == diff.Del:
			col = pRed
		default:
			continue
		}
		c.fill(image.Rect(strip.Min.X+c.pt(2), at(ys[i]), strip.Max.X-c.pt(2), max(at(ys[i+1]), at(ys[i])+1)), col)
	}
	c.round(image.Rect(strip.Min.X, at(k.diffTop), strip.Max.X, at(k.diffTop+rows.Dy())), c.pt(2), alpha(pWhite, 46))
}

// diffRows draws the rows in view and returns where each row starts, in
// pixels from the top of the file, with the file's height last.
func (k *Kitty) diffRows(c *canvas, r image.Rectangle) []int {
	p := k.m.pr
	code, small := c.font(mono, 12.5), c.font(mono, 10.5)
	lineH, hunkH := c.pt(21), c.pt(30)
	numW := small.width("00000")
	gutterW := c.pt(14) + 2*numW + c.pt(22)
	codeX := r.Min.X + gutterW + code.advance*2
	noteX, noteW := codeX, min(r.Max.X-codeX-c.pt(20), c.pt(820))

	ys := make([]int, len(p.rows)+1)
	for i, row := range p.rows {
		h := lineH
		switch row.kind {
		case rowHunk:
			h = hunkH
		case rowNote:
			h = k.note(c.measuring(), row, noteX, 0, noteW)
		}
		ys[i+1] = ys[i] + h
	}
	if len(p.rows) == 0 {
		return ys
	}
	viewH, margin := r.Dy(), 2*lineH
	// Whole cell rows, as scrollStep has it.
	if top := ys[p.cursor] - margin; top < k.diffTop {
		k.diffTop = top / k.cellH * k.cellH
	}
	if bottom := ys[p.cursor+1] + margin; bottom > k.diffTop+viewH {
		k.diffTop = (bottom - viewH + k.cellH - 1) / k.cellH * k.cellH
	}
	k.diffTop = max(min(k.diffTop, ys[len(p.rows)]-viewH), 0)

	selA, selB := -1, -1
	if p.selecting {
		selA, selB = min(p.anchor, p.cursor), max(p.anchor, p.cursor)
	}
	text := c.clip(image.Rect(codeX, r.Min.Y, r.Max.X, r.Max.Y))
	for i, row := range p.rows {
		top := r.Min.Y + ys[i] - k.diffTop
		box := image.Rect(r.Min.X, top, r.Max.X, top+ys[i+1]-ys[i])
		if box.Max.Y < r.Min.Y || box.Min.Y > r.Max.Y {
			continue
		}
		switch row.kind {
		case rowHunk:
			band := image.Rect(box.Min.X, box.Min.Y+c.pt(4), box.Max.X, box.Max.Y-c.pt(4))
			c.fill(band, blend(pBg, pBlue, 0.07))
			c.textIn(image.Rect(codeX, band.Min.Y, band.Max.X, band.Max.Y), row.text, c.font(mono, 11), blend(pBg, pBlue, 0.6))
		case rowNote:
			k.note(c, row, noteX, top, noteW)
		case rowLine:
			l := row.line
			marker, col, textCol := "", pFaint, hex(0xcdd0e4)
			switch l.Kind {
			case diff.Add:
				c.fill(box, pAddBg)
				marker, col, textCol = "+", pGreen, hex(0xd9f7e6)
			case diff.Del:
				c.fill(box, pDelBg)
				marker, col, textCol = "−", pRed, hex(0xfadbe2)
			}
			numCol := pFaint
			switch {
			case i == p.cursor:
				c.fill(box, alpha(pWhite, 26))
				c.fill(image.Rect(box.Min.X, box.Min.Y, box.Min.X+c.pt(3), box.Max.Y), pPurple)
				numCol = pText
			case i >= selA && i <= selB:
				c.fill(box, alpha(pBlue, 34))
				c.fill(image.Rect(box.Min.X, box.Min.Y, box.Min.X+c.pt(3), box.Max.Y), pBlue)
				numCol = pMuted
			}
			ny := top + (lineH-small.height)/2
			nx := r.Min.X + c.pt(14) + numW
			if l.OldNo > 0 {
				c.textRight(nx, ny, fmt.Sprint(l.OldNo), small, numCol)
			}
			if l.NewNo > 0 {
				c.textRight(nx+c.pt(10)+numW, ny, fmt.Sprint(l.NewNo), small, numCol)
			}
			ty := top + (lineH-code.height)/2
			c.text(codeX-code.advance*2, ty, marker, code, col)
			text.text(codeX-p.xoff*code.advance, ty, expandTabs(l.Text), code, textCol)
		}
	}
	return ys
}

// note draws the card of a comment or an agent finding under its line and
// returns the height of its row.
func (k *Kitty) note(c *canvas, row diffRow, x, y, w int) int {
	margin, pad := c.pt(6), c.pt(11)
	accent := pOrange
	var draw func(c *canvas, x, y, w int) int
	switch {
	case row.comment != nil:
		cm := row.comment
		if cm.ReplyTo != 0 {
			x, w = x+c.pt(24), w-c.pt(24)
		}
		draw = func(c *canvas, x, y, w int) int {
			f := c.font(sansBold, 12)
			c.avatar(x, y, c.pt(18), cm.Author)
			tx := x + c.pt(25)
			tx += c.text(tx, y+(c.pt(18)-f.height)/2, cm.Author, f, pLilac) + c.pt(8)
			if row.fileLevel {
				tx += c.tint(tx, y, "whole file", pBlue) + c.pt(8)
			}
			if cm.ReplyTo != 0 {
				tx += c.text(tx, y+(c.pt(18)-f.height)/2, "replied", c.font(sans, 12), pFaint) + c.pt(8)
			}
			c.textRight(x+w, y+(c.pt(18)-f.height)/2, ago(cm.CreatedAt), c.font(sans, 11), pFaint)
			y += c.pt(26)
			return y + c.markdown(cm.Body, x, y, w, mdText{size: 12.5, col: pText})
		}
	case row.finding != nil:
		fd := row.finding
		accent = severityTint(fd.Severity)
		draw = func(c *canvas, x, y, w int) int {
			f := c.font(sansBold, 12.5)
			tx := x + c.pill(x, y, "agent", pBg, accent) + c.pt(6)
			tx += c.tint(tx, y, fd.Severity, accent) + c.pt(10)
			c.text(tx, y+(c.pt(18)-f.height)/2, f.fit(fd.Title, x+w-tx), f, pText)
			y += c.pt(26)
			return y + c.markdown(fd.Explanation, x, y, w, mdText{size: 12.5, col: pMuted})
		}
	default:
		panic("kitty graphics: a note row with neither a comment nor a finding")
	}
	bottom := draw(c.measuring(), x+pad, y+margin+pad, w-2*pad) + pad
	c.card(image.Rect(x, y+margin, x+w, bottom), accent, pPanel)
	draw(c, x+pad, y+margin+pad, w-2*pad)
	return bottom + margin - y
}

func (k *Kitty) fileList(c *canvas, r image.Rectangle) {
	m, p := k.m, k.m.pr
	c.fill(image.Rect(r.Max.X-hair(c), r.Min.Y, r.Max.X, r.Max.Y), pLine)
	pad := c.pt(12)
	y := r.Min.Y + c.pt(13)
	cx := r.Min.X + pad + c.text(r.Min.X+pad, y, "FILES", c.font(sansBold, 10), pFaint) + c.pt(8)
	c.text(cx, y, fmt.Sprint(len(p.data.Files)), c.font(sansBold, 10), pMuted)

	findings := map[string]int{}
	if run := m.agentRunFor(p.number); run != nil && run.result != nil {
		for _, f := range run.result.Findings {
			findings[f.Path]++
		}
	}
	comments := map[string]int{}
	for _, cm := range p.data.Comments {
		if cm.Anchor != nil {
			comments[cm.Anchor.Path]++
		}
	}

	top, rowH := r.Min.Y+c.pt(36), c.pt(40)
	visible := max((r.Max.Y-top)/rowH, 1)
	start := max(min(p.fileIdx-visible/2, len(p.data.Files)-visible), 0)
	name, dirFace := c.font(sans, 12.5), c.font(sans, 10.5)
	for i := start; i < min(len(p.data.Files), start+visible); i++ {
		f := p.data.Files[i]
		row := image.Rect(r.Min.X+c.pt(6), top+(i-start)*rowH, r.Max.X-c.pt(8), top+(i-start+1)*rowH)
		nameCol := pText
		if i == p.fileIdx {
			c.card(row.Inset(c.pt(1)), pPurple, pRaise)
			name, nameCol = c.font(sansBold, 12.5), pWhite
		} else {
			name = c.font(sans, 12.5)
		}
		dot := pYellow
		switch {
		case f.Added:
			dot = pGreen
		case f.Deleted:
			dot = pRed
		}
		c.disc(row.Min.X+c.pt(15), row.Min.Y+c.pt(14), c.pt(3.5), dot)

		rx := row.Max.X - c.pt(8)
		c.bar(image.Rect(rx-c.pt(28), row.Min.Y+c.pt(12), rx, row.Min.Y+c.pt(17)), f.Additions, f.Deletions)
		rx -= c.pt(34)
		if n := findings[f.Path()]; n > 0 {
			rx -= c.tintRight(rx, row.Min.Y+c.pt(5), fmt.Sprint(n), pRed) + c.pt(4)
		}
		if n := comments[f.Path()]; n > 0 {
			rx -= c.tintRight(rx, row.Min.Y+c.pt(5), fmt.Sprint(n), pOrange) + c.pt(4)
		}
		tx := row.Min.X + c.pt(27)
		dir, base := path.Split(f.Path())
		c.text(tx, row.Min.Y+c.pt(5), name.fit(base, rx-tx-c.pt(4)), name, nameCol)
		c.text(tx, row.Min.Y+c.pt(22), fitLeft(dirFace, strings.TrimSuffix(dir, "/"), row.Max.X-tx-c.pt(8)), dirFace, pFaint)
	}
	c.scrollbar(image.Rect(r.Max.X-c.pt(6), top, r.Max.X-c.pt(2), r.Max.Y-c.pt(6)), len(p.data.Files), visible, start)
}

// fitLeft truncates s from its start, keeping the end of a path.
func fitLeft(f *typeface, s string, w int) string {
	if f.width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && f.width("…"+string(r)) > w {
		r = r[1:]
	}
	return "…" + string(r)
}

// ---- agent ----

func (k *Kitty) agent(c *canvas, r image.Rectangle) {
	m, p := k.m, k.m.pr
	pad := c.pt(16)
	x, y := r.Min.X+pad, r.Min.Y+pad
	run := m.agentRunFor(p.number)
	if run == nil {
		c.panel(x, y, min(r.Dx()-2*pad, c.pt(820)), pPanel, func(c *canvas, x, y, w int) int {
			return y + c.markdown(m.agentIntroMarkdown(), x, y, w, mdText{size: 13, col: pText})
		})
		return
	}
	elapsed := time.Since(run.started)
	if !run.running() {
		elapsed = run.ended.Sub(run.started)
	}
	f := c.font(sans, 12.5)
	tx := x + c.tint(x, y, "level "+run.level.String(), pLilac) + c.pt(10)
	tx += c.text(tx, y+(c.pt(18)-f.height)/2, "model "+m.deps.ModelName, f, pMuted) + c.pt(12)
	c.text(tx, y+(c.pt(18)-f.height)/2, elapsed.Round(time.Second).String(), c.font(mono, 12), pFaint)
	y += c.pt(32)

	if run.running() || run.result == nil {
		rowH := c.pt(38)
		for _, a := range run.agents {
			row := image.Rect(x, y, r.Max.X-pad, y+rowH-c.pt(6))
			c.round(row, c.pt(8), pPanel)
			cy := row.Min.Y + row.Dy()/2
			switch a.state {
			case review.EventDone:
				c.check(x+c.pt(18), cy, c.pt(12), pGreen)
			case review.EventFailed:
				c.cross(x+c.pt(18), cy, c.pt(12), pRed)
			default:
				c.spinner(x+c.pt(18), cy, c.pt(7), spinPhase())
			}
			bold := c.font(sansBold, 12.5)
			nameW := c.pt(300)
			c.textIn(image.Rect(x+c.pt(36), row.Min.Y, row.Max.X, row.Max.Y), bold.fit(a.name, nameW), bold, pText)
			tools := x + c.pt(36) + nameW + c.pt(10)
			c.tint(tools, row.Min.Y+(row.Dy()-c.pt(18))/2, fmt.Sprintf("%d tools", a.tools), pBlue)
			lx := tools + c.pt(90)
			c.textIn(image.Rect(lx, row.Min.Y, row.Max.X, row.Max.Y), f.fit(oneLine(a.last), row.Max.X-lx-c.pt(12)), f, pMuted)
			y += rowH
		}
		y += c.pt(8)
		switch {
		case run.err != nil:
			for _, line := range f.wrap("review failed: "+run.err.Error(), r.Dx()-2*pad) {
				c.text(x, y, line, f, pRed)
				y += c.pt(20)
			}
			c.hints(x, y+c.pt(6), "A", "run again")
		case run.running():
			tx := x + c.text(x, y+c.pt(2), "Agents are read-only; they only suggest.", f, pFaint) + c.pt(12)
			c.hints(tx, y, "A", "cancel")
		}
		return
	}
	k.findings(c, run.result, image.Rect(x, y, r.Max.X-pad, r.Max.Y))
}

func (k *Kitty) findings(c *canvas, res *review.Result, r image.Rectangle) {
	p := k.m.pr
	x, y := r.Min.X, r.Min.Y
	f := c.font(sans, 12.5)
	if res.Assessment != "" {
		y = c.panel(x, y, r.Dx(), pPanel, func(c *canvas, x, y, w int) int {
			y = c.caption(x, y, "Assessment")
			return y + c.markdown(res.Assessment, x, y, w, mdText{size: 13, col: pText})
		}) + c.pt(10)
	}
	for _, n := range res.Notes {
		c.disc(x+c.pt(6), y+c.pt(9), c.pt(3), pOrange)
		c.text(x+c.pt(18), y, f.fit(n, r.Dx()-c.pt(18)), f, pOrange)
		y += c.pt(22)
	}
	for _, fail := range res.Failures {
		c.cross(x+c.pt(6), y+c.pt(9), c.pt(10), pRed)
		c.text(x+c.pt(18), y, f.fit(oneLine(fail), r.Dx()-c.pt(18)), f, pRed)
		y += c.pt(22)
	}
	if len(res.Findings) == 0 {
		c.check(x+c.pt(7), y+c.pt(10), c.pt(12), pGreen)
		c.text(x+c.pt(22), y, "No suggestions: the agents found nothing they could back with a concrete scenario.", f, pGreen)
		return
	}
	y += c.pt(4)

	listW := min(max(r.Dx()*2/5, c.pt(320)), c.pt(520))
	rowH := c.pt(52)
	visible := max((r.Max.Y-y)/rowH, 1)
	start := max(min(p.findCursor-visible/2, len(res.Findings)-visible), 0)
	bold, loc := c.font(sansBold, 12.5), c.font(mono, 10.5)
	for i := start; i < min(len(res.Findings), start+visible); i++ {
		fd := res.Findings[i]
		row := image.Rect(x, y+(i-start)*rowH, x+listW, y+(i-start+1)*rowH)
		col := pText
		if i == p.findCursor {
			c.card(row.Inset(c.pt(2)), pPurple, pRaise)
			col = pWhite
		}
		c.diamond(row.Min.X+c.pt(18), row.Min.Y+c.pt(18), c.pt(9), severityTint(fd.Severity))
		tx := row.Min.X + c.pt(32)
		c.text(tx, row.Min.Y+c.pt(9), bold.fit(fd.Title, row.Max.X-tx-c.pt(10)), bold, col)
		c.text(tx, row.Min.Y+c.pt(29), fitLeft(loc, fmt.Sprintf("%s:%d", fd.Path, fd.Line), row.Max.X-tx-c.pt(10)), loc, pFaint)
	}
	c.fill(image.Rect(x+listW+c.pt(8), y, x+listW+c.pt(8)+hair(c), r.Max.Y), pLine)

	fd := res.Findings[min(p.findCursor, len(res.Findings)-1)]
	detail := image.Rect(x+listW+c.pt(24), y, r.Max.X-c.pt(12), r.Max.Y)
	total := k.finding(c.measuring(), fd, 0, 0, detail.Dx())
	step := k.scrollStep()
	maxScroll := max(total-detail.Dy(), 0)
	p.agentScroll = min(p.agentScroll, (maxScroll+step-1)/step)
	off := min(p.agentScroll*step, maxScroll)
	k.finding(c.clip(detail), fd, detail.Min.X, detail.Min.Y-off, detail.Dx())
	c.scrollbar(image.Rect(r.Max.X-c.pt(6), y, r.Max.X-c.pt(1), r.Max.Y-c.pt(6)), total, detail.Dy(), off)
}

func (k *Kitty) finding(c *canvas, fd review.Finding, x, y, w int) int {
	title := c.font(sansBold, 17)
	for _, line := range title.wrap(fd.Title, w) {
		c.text(x, y, line, title, pText)
		y += c.pt(25)
	}
	y += c.pt(6)
	sev := severityTint(fd.Severity)
	tx := x + c.tint(x, y, fd.Severity, sev) + c.pt(6)
	tx += c.tint(tx, y, fd.Confidence+" confidence", pMuted) + c.pt(6)
	if fd.Lens != "" {
		c.tint(tx, y, fd.Lens, pLilac)
	}
	y += c.pt(30)
	loc := c.font(mono, 11.5)
	where := fmt.Sprintf("%s  line %d (%s)", fd.Path, fd.Line, strings.ToLower(string(fd.Side)))
	c.text(x, y, fitLeft(loc, where, w), loc, pBlue)
	y += c.pt(22)
	body := mdText{size: 13, col: pText}
	if !fd.Anchored {
		y += c.markdown("_Not on a diff line; `c` drafts a comment on the whole file._", x, y, w, mdText{size: 12, col: pFaint}) + c.pt(4)
	}
	y += c.pt(6)
	y += c.markdown(fd.Explanation, x, y, w, body) + c.pt(20)
	y = c.caption(x, y, "Suggested comment")
	pad := c.pt(12)
	bottom := y + pad + c.measuring().markdown(fd.SuggestedComment, 0, 0, w-2*pad, body) + pad
	c.card(image.Rect(x, y, x+w, bottom), pPurple, pPanel)
	c.markdown(fd.SuggestedComment, x+pad, y+pad, w-2*pad, body)
	y = bottom + c.pt(14)
	c.hints(x, y, "enter", "go to line", "c", "draft this comment (you edit and post)")
	return y + c.pt(30)
}
