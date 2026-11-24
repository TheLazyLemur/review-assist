package tui

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"os"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/TheLazyLemur/review-assist/internal/core/pr"
)

// Kitty is the app drawn as pixels over the kitty graphics protocol. The
// Model still owns state and keys; Kitty only replaces its view. The screen
// holds no text: each cell row is one image.
type Kitty struct {
	m     *Model
	tty   *os.File
	fonts *fontCache

	shm bool // the terminal reads images from shared memory

	cols, rows, cellH int
	u                 float64 // device pixels per point
	cur               *image.RGBA
	tiles             *kittyTiles
	resizes           int

	diffTop int // pixels scrolled; diff rows are not all one height
}

type kittyResizedMsg struct{ seq int }

// The renderer clears the alt screen when it first draws and after a resize,
// and that takes the images with it. Painting waits for it.
const kittySettle = 60 * time.Millisecond

func NewKitty(deps Deps, tty *os.File) (*Kitty, error) {
	_, _, xpix, ypix, err := ttyPixels(tty)
	if err != nil {
		return nil, fmt.Errorf("kitty graphics: %w", err)
	}
	if xpix == 0 || ypix == 0 {
		return nil, errors.New("kitty graphics: the terminal does not report its size in pixels")
	}
	m := New(deps)
	return &Kitty{m: m, tty: tty, fonts: newFontCache(), shm: kittyReadsShm()}, nil
}

func (k *Kitty) Init() tea.Cmd { return k.m.Init() }

func (k *Kitty) View() tea.View {
	// The renderer erases the screen, and the images with it, on every
	// frame of a view with no content.
	v := tea.NewView(" ")
	v.AltScreen = true
	return v
}

func (k *Kitty) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(kittyResizedMsg); ok {
		if msg.seq != k.resizes {
			return k, nil
		}
		k.resize()
		return k, k.paint()
	}
	_, cmd := k.m.Update(msg)
	switch msg.(type) {
	case tea.WindowSizeMsg:
		k.resizes++
		seq := k.resizes
		return k, tea.Batch(cmd, tea.Tick(kittySettle, func(time.Time) tea.Msg { return kittyResizedMsg{seq} }))
	case spinner.TickMsg:
		if k.m.busy == 0 && k.m.runningAgents() == 0 {
			return k, cmd
		}
	}
	if k.cur == nil {
		return k, cmd
	}
	return k, tea.Batch(cmd, k.paint())
}

func (k *Kitty) resize() {
	cols, rows, xpix, ypix, err := ttyPixels(k.tty)
	if err != nil || xpix == 0 || ypix == 0 || cols == 0 || rows == 0 {
		panic(fmt.Sprintf("kitty graphics: terminal size %dx%d cells, %dx%d pixels: %v", cols, rows, xpix, ypix, err))
	}
	k.cols, k.rows, k.cellH = cols, rows, ypix/rows
	size := image.Rect(0, 0, cols*(xpix/cols), rows*k.cellH)
	k.cur = image.NewRGBA(size)
	k.tiles = newKittyTiles(cols, rows, xpix/cols, k.cellH, k.shm)
	// A cell is about 1.3 em high, and the UI's body text is 13 points.
	k.u = float64(k.cellH) / 1.3 / 13
}

func (k *Kitty) paint() tea.Cmd {
	k.draw(&canvas{img: k.cur, u: k.u, fonts: k.fonts})
	seq := k.tiles.frame(k.cur)
	k.m.quitSeq = k.tiles.free()
	if seq == "" {
		return nil
	}
	return tea.Raw(seq)
}

// scrollStep is how far one step scrolls a pane. Whole cell rows: the pixels
// then land on tiles as they did before, and the terminal has them already.
func (k *Kitty) scrollStep() int { return 2 * k.cellH }

func spinPhase() float64 { return float64(time.Now().UnixMilli()%900) / 900 }

func (k *Kitty) draw(c *canvas) {
	b := c.img.Bounds()
	c.fill(b, pBg)
	headH, footH := c.pt(38), c.pt(32)
	k.header(c, image.Rect(0, 0, b.Max.X, headH))
	body := image.Rect(0, headH, b.Max.X, b.Max.Y-footH)
	switch k.m.screen {
	case screenList:
		k.list(c.clip(body), body)
	case screenPR:
		k.pr(c.clip(body), body)
	}
	k.footer(c, image.Rect(0, b.Max.Y-footH, b.Max.X, b.Max.Y))
	if k.m.modal != nil {
		k.modal(c, b)
	}
}

func hair(c *canvas) int { return max(c.pt(1), 1) }

func (k *Kitty) header(c *canvas, r image.Rectangle) {
	m := k.m
	c.fill(r, pPanel)
	c.fill(image.Rect(r.Min.X, r.Max.Y-hair(c), r.Max.X, r.Max.Y), pLine)
	from := func(x int) image.Rectangle { return image.Rect(x, r.Min.Y, r.Max.X, r.Max.Y) }
	pillY := r.Min.Y + (r.Dy()-c.pt(18))/2

	x, mark := c.pt(14), c.pt(20)
	my := r.Min.Y + (r.Dy()-mark)/2
	c.round(image.Rect(x, my, x+mark, my+mark), c.pt(5), pPurple)
	c.check(x+mark/2, my+mark/2, mark*6/10, pWhite)
	x += mark + c.pt(9)
	x += c.textIn(from(x), "review-assist", c.font(sansBold, 13), pText) + c.pt(12)
	x += c.textIn(from(x), m.deps.PRs.Repo().Qualified(), c.font(sans, 13), pMuted) + c.pt(12)
	if m.deps.Remote != "" {
		c.tint(x, pillY, "remote "+m.deps.Remote, pLilac)
	}

	rx := r.Max.X - c.pt(14)
	if m.viewer != "" {
		f := c.font(sans, 12)
		rx -= f.width("@" + m.viewer)
		c.textIn(from(rx), "@"+m.viewer, f, pMuted)
		rx -= c.pt(24)
		c.avatar(rx, r.Min.Y+(r.Dy()-c.pt(18))/2, c.pt(18), m.viewer)
		rx -= c.pt(14)
	}
	if n := m.runningAgents(); n > 0 {
		s := fmt.Sprintf("%d agent run(s) in progress", n)
		rx -= c.measuring().tint(0, 0, s, pOrange)
		c.tint(rx, pillY, s, pOrange)
		rx -= c.pt(14)
	}
	if m.busy > 0 {
		c.spinner(rx-c.pt(8), r.Min.Y+r.Dy()/2, c.pt(7), spinPhase())
	}
}

func (k *Kitty) footer(c *canvas, r image.Rectangle) {
	m := k.m
	c.fill(r, pPanel)
	c.fill(image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+hair(c)), pLine)
	right := r.Max.X - c.pt(14)
	if m.status != "" {
		col := pGreen
		if m.statusErr {
			col = pRed
		}
		f := c.font(sans, 12)
		s := f.fit(oneLine(m.status), r.Dx()/2)
		right -= f.width(s)
		c.textIn(image.Rect(right, r.Min.Y, r.Max.X, r.Max.Y), s, f, col)
		right -= c.pt(12)
		c.disc(right, r.Min.Y+r.Dy()/2, c.pt(3), col)
		right -= c.pt(14)
	}
	pairs := listHelp
	if m.screen == screenPR {
		pairs = m.prHelp()
	}
	c.clip(image.Rect(r.Min.X, r.Min.Y, right, r.Max.Y)).hints(c.pt(14), r.Min.Y+(r.Dy()-c.pt(18))/2, pairs...)
}

// segments draws a segmented control and returns its width.
func (c *canvas) segments(x, y int, labels []string, active int) int {
	h, pad, in := c.pt(28), c.pt(12), c.pt(3)
	start := x
	x += in
	type seg struct {
		x, w int
		f    *typeface
	}
	var segs []seg
	for i, l := range labels {
		f := c.font(sans, 12)
		if i == active {
			f = c.font(sansBold, 12)
		}
		w := f.width(l) + 2*pad
		segs = append(segs, seg{x, w, f})
		x += w
	}
	x += in
	c.round(image.Rect(start, y, x, y+h), c.pt(8), pPanel)
	for i, s := range segs {
		r := image.Rect(s.x, y+in, s.x+s.w, y+h-in)
		col := pMuted
		if i == active {
			c.round(r, c.pt(6), pPurple)
			col = pWhite
		}
		c.textIn(r.Add(image.Pt(pad, 0)), labels[i], s.f, col)
	}
	return x - start
}

func stateTint(state string, draft bool) (string, color.RGBA) {
	switch {
	case draft:
		return "draft", pMuted
	case state == "OPEN":
		return "open", pGreen
	case state == "MERGED":
		return "merged", pLilac
	case state == "CLOSED":
		return "closed", pRed
	default:
		return strings.ToLower(state), pMuted
	}
}

func decisionTint(d string) (string, color.RGBA) {
	switch d {
	case "APPROVED":
		return "approved", pGreen
	case "CHANGES_REQUESTED":
		return "changes requested", pRed
	case "REVIEW_REQUIRED":
		return "review required", pYellow
	default:
		return "", pMuted
	}
}

func severityTint(sev string) color.RGBA {
	switch sev {
	case "blocking":
		return pRed
	case "major":
		return pOrange
	default:
		return pYellow
	}
}

// tintRight draws a pill ending at right and returns its width.
func (c *canvas) tintRight(right, y int, s string, col color.RGBA) int {
	w := c.measuring().tint(0, 0, s, col)
	c.tint(right-w, y, s, col)
	return w
}

// ---- PR list ----

func (k *Kitty) list(c *canvas, r image.Rectangle) {
	l := &k.m.list
	pad := c.pt(16)
	x, y := r.Min.X+pad, r.Min.Y+c.pt(12)
	labels := make([]string, len(pr.States))
	active := 0
	for i, s := range pr.States {
		labels[i] = string(s)
		if s == l.state {
			active = i
		}
	}
	x += c.segments(x, y, labels, active) + c.pt(14)
	box := image.Rect(x, y, x+c.pt(280), y+c.pt(28))
	f := c.font(sans, 12.5)
	switch {
	case l.filtering:
		c.outline(box, c.pt(8), max(c.pt(1.5), 1), pPurple, pBg)
		tx := box.Min.X + c.pt(10)
		tx += c.textIn(image.Rect(tx, box.Min.Y, box.Max.X, box.Max.Y), "/", c.font(monoBold, 12.5), pPink) + c.pt(6)
		tx += c.textIn(image.Rect(tx, box.Min.Y, box.Max.X, box.Max.Y), l.filter, f, pText)
		c.fill(image.Rect(tx+c.pt(1), box.Min.Y+c.pt(6), tx+c.pt(3), box.Max.Y-c.pt(6)), pPurple)
	case l.filter != "":
		c.tint(x, y+c.pt(5), "filter: "+l.filter, pBlue)
	}
	count := fmt.Sprintf("%d pull requests", len(l.shown))
	c.textRight(r.Max.X-pad, y+(c.pt(28)-f.height)/2, count, f, pFaint)

	top := r.Min.Y + c.pt(52)
	if len(l.shown) == 0 {
		msg := "No pull requests."
		if l.loading {
			msg = "Loading pull requests…"
			c.spinner(r.Min.X+pad+c.pt(8), top+c.pt(10), c.pt(7), spinPhase())
		}
		c.text(r.Min.X+pad+c.pt(24), top, msg, c.font(sans, 13), pMuted)
		return
	}
	rowH := c.pt(56)
	rows := max((r.Max.Y-top)/rowH, 1)
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+rows {
		l.offset = l.cursor - rows + 1
	}
	for i := l.offset; i < min(len(l.shown), l.offset+rows); i++ {
		ry := top + (i-l.offset)*rowH
		k.listRow(c, image.Rect(r.Min.X+pad/2, ry, r.Max.X-pad, ry+rowH), l.shown[i], i == l.cursor)
	}
	c.scrollbar(image.Rect(r.Max.X-c.pt(9), top, r.Max.X-c.pt(4), r.Max.Y-c.pt(6)), len(l.shown), rows, l.offset)
}

func (k *Kitty) listRow(c *canvas, r image.Rectangle, p pr.Summary, selected bool) {
	if selected {
		c.card(r.Inset(c.pt(2)), pPurple, pRaise)
	} else {
		c.fill(image.Rect(r.Min.X+c.pt(14), r.Max.Y-hair(c), r.Max.X, r.Max.Y), pPanel)
	}
	x := r.Min.X + c.pt(18)
	line1, line2 := r.Min.Y+c.pt(10), r.Min.Y+c.pt(31)
	pillY := r.Min.Y + (r.Dy()-c.pt(18))/2

	// Right to left: the diffstat in fixed columns, then the pills.
	rx := r.Max.X - c.pt(14)
	c.bar(image.Rect(rx-c.pt(44), r.Min.Y+r.Dy()/2-c.pt(3), rx, r.Min.Y+r.Dy()/2+c.pt(3)), p.Additions, p.Deletions)
	rx -= c.pt(54)
	num := c.font(mono, 11.5)
	ny := r.Min.Y + (r.Dy()-num.height)/2
	c.textRight(rx, ny, fmt.Sprintf("−%d", p.Deletions), num, pRed)
	c.textRight(rx-c.pt(46), ny, fmt.Sprintf("+%d", p.Additions), num, pGreen)
	rx -= c.pt(106)
	state, col := stateTint(p.State, p.IsDraft)
	c.tintRight(rx, pillY, state, col)
	rx -= c.pt(72)
	if d, col := decisionTint(p.ReviewDecision); d != "" {
		rx -= c.tintRight(rx, pillY, d, col) + c.pt(8)
	}
	if k.m.agentRunFor(p.Number) != nil {
		rx -= c.tintRight(rx, pillY, "agent", pOrange) + c.pt(8)
	}

	title := c.font(sansBold, 13.5)
	x2 := x + c.text(x, line1, title.fit(p.Title, rx-x-c.pt(10)), title, pText) + c.pt(10)
	for _, label := range p.Labels {
		w := c.measuring().tint(0, 0, label, pBlue)
		if x2+w > rx {
			break
		}
		x2 += c.tint(x2, line1-c.pt(1), label, pBlue) + c.pt(6)
	}

	small := c.font(sans, 11.5)
	x += c.text(x, line2, fmt.Sprintf("#%d", p.Number), c.font(mono, 11.5), pFaint) + c.pt(12)
	c.avatar(x, line2, c.pt(14), p.Author)
	x += c.pt(19)
	x += c.text(x, line2, p.Author, small, pLilac) + c.pt(12)
	if p.HeadRef != "" {
		branch := c.font(mono, 10.5)
		s := branch.fit(p.HeadRef, c.pt(260))
		bw := branch.width(s) + c.pt(12)
		c.round(image.Rect(x, line2-c.pt(1), x+bw, line2+c.pt(15)), c.pt(4), blend(pBg, pBlue, 0.12))
		c.text(x+c.pt(6), line2+(c.pt(14)-branch.height)/2, s, branch, pBlue)
		x += bw + c.pt(12)
	}
	c.text(x, line2, ago(p.UpdatedAt), small, pFaint)
}

// ---- modals ----

var columnGap = regexp.MustCompile(` {2,}`)

func (k *Kitty) modal(c *canvas, screen image.Rectangle) {
	c.dim(screen)
	pad := c.pt(22)
	maxW := screen.Dx() - 2*pad
	var w int
	var body func(c *canvas, x, y, w int) int
	switch md := k.m.modal.(type) {
	case *menuModal:
		w, body = k.menu(c, md)
	case *confirmModal:
		w = c.pt(440)
		body = func(c *canvas, x, y, w int) int {
			f := c.font(sans, 13.5)
			for _, line := range f.wrap(md.prompt, w) {
				c.text(x, y, line, f, pText)
				y += c.pt(21)
			}
			y += c.pt(12)
			c.hints(x, y, "y", "yes", "n", "no")
			return y + c.pt(18)
		}
	case *helpModal:
		w = c.pt(880)
		body = func(c *canvas, x, y, w int) int { return k.help(c, md, x, y, w) }
	case *editorModal:
		w = c.pt(780)
		body = func(c *canvas, x, y, w int) int { return k.editor(c, md, x, y, w) }
	default:
		panic(fmt.Sprintf("kitty graphics: no drawing for modal %T", md))
	}
	w = min(w+2*pad, maxW)
	h := body(c.measuring(), 0, 0, w-2*pad) + 2*pad
	x, y := (screen.Dx()-w)/2, max((screen.Dy()-h)/2, 0)
	box := image.Rect(x, y, x+w, y+h)
	for i := 3; i >= 1; i-- {
		c.round(box.Inset(-i*c.pt(4)).Add(image.Pt(0, c.pt(5))), c.pt(10)+i*c.pt(4), alpha(hex(0x000000), 40))
	}
	c.outline(box, c.pt(10), hair(c), pLine, pPanel)
	body(c, x+pad, y+pad, w-2*pad)
}

// menu lays labels out in columns wherever they are spaced apart, so text
// padded for a fixed-width font still lines up.
func (k *Kitty) menu(c *canvas, mm *menuModal) (int, func(c *canvas, x, y, w int) int) {
	f, bold := c.font(sans, 13), c.font(sansBold, 13)
	rows := make([][]string, len(mm.items))
	var widths []int
	for i, it := range mm.items {
		rows[i] = columnGap.Split(strings.TrimSpace(it.label), -1)
		for j, cell := range rows[i] {
			if j == len(widths) {
				widths = append(widths, 0)
			}
			widths[j] = max(widths[j], bold.width(cell))
		}
	}
	keyW, gap := c.pt(34), c.pt(18)
	natural := keyW + c.pt(20)
	for _, cw := range widths {
		natural += cw + gap
	}
	title := c.font(sansBold, 14.5)
	natural = max(natural, title.width(mm.title))
	return natural, func(c *canvas, x, y, w int) int {
		c.text(x, y, title.fit(mm.title, w), title, pText)
		y += c.pt(32)
		rowH := c.pt(30)
		for i, it := range mm.items {
			r := image.Rect(x, y, x+w, y+rowH)
			col, face := pText, f
			if i == mm.cursor {
				c.round(r, c.pt(6), pPurple)
				col, face = pWhite, bold
			}
			if it.key != "" {
				c.keycap(x+c.pt(8), y+(rowH-c.pt(18))/2, it.key)
			}
			cx := x + keyW + c.pt(10)
			for j, cell := range rows[i] {
				c.textIn(image.Rect(cx, y, x+w, y+rowH), cell, face, col)
				cx += widths[j] + gap
				// Later columns explain the first.
				if i != mm.cursor {
					col = pMuted
				}
			}
			y += rowH
		}
		y += c.pt(14)
		c.hints(x, y, "j/k", "move", "enter", "select", "esc", "close")
		return y + c.pt(18)
	}
}

func (k *Kitty) help(c *canvas, h *helpModal, x, y, w int) int {
	colW := (w - c.pt(30)) / 2
	title, key, desc := c.font(sansBold, 13), c.font(mono, 11.5), c.font(sans, 12)
	column := func(secs [][]string, x, y int) int {
		for _, sec := range secs {
			c.text(x, y, sec[0], title, pLilac)
			y += c.pt(24)
			for i := 1; i+1 < len(sec); i += 2 {
				c.text(x+c.pt(4), y, sec[i], key, pPink)
				c.text(x+c.pt(140), y, desc.fit(sec[i+1], colW-c.pt(140)), desc, pText)
				y += c.pt(20)
			}
			y += c.pt(14)
		}
		return y
	}
	half := (len(h.sections) + 1) / 2
	bottom := max(column(h.sections[:half], x, y), column(h.sections[half:], x+colW+c.pt(30), y))
	c.text(x, bottom, helpFootnote, desc, pFaint)
	return bottom + c.pt(18)
}

// softWrap splits a line into segments of at most width runes, breaking
// after a space where there is one.
func softWrap(r []rune, width int) [][2]int {
	var out [][2]int
	start := 0
	for len(r)-start > width {
		brk := start + width
		for i := brk; i > start; i-- {
			if r[i-1] == ' ' {
				brk = i
				break
			}
		}
		out = append(out, [2]int{start, brk})
		start = brk
	}
	return append(out, [2]int{start, len(r)})
}

const editorLines = 11

func (k *Kitty) editor(c *canvas, e *editorModal, x, y, w int) int {
	c.text(x, y, e.title, c.font(sansBold, 15), pText)
	y += c.pt(30)
	if e.context != "" {
		lines := strings.Split(ansi.Strip(e.context), "\n")
		small := c.font(sans, 11.5)
		for _, line := range small.wrap(lines[0], w) {
			c.text(x, y, line, small, pFaint)
			y += c.pt(18)
		}
		if quote := lines[1:]; len(quote) > 0 {
			f := c.font(mono, 11.5)
			h := len(quote)*c.pt(17) + c.pt(12)
			box := image.Rect(x, y+c.pt(4), x+w, y+c.pt(4)+h)
			c.card(box, pLine, hex(0x121319))
			in := c.clip(box)
			for i, line := range quote {
				in.text(x+c.pt(14), box.Min.Y+c.pt(6)+i*c.pt(17), line, f, pMuted)
			}
			y = box.Max.Y
		}
		y += c.pt(12)
	}

	f := c.font(mono, 13)
	lineH, in := c.pt(20), c.pt(12)
	chars := max((w-2*in)/f.advance, 10)
	// The text area moves the cursor by its own wrapping; keep it in step.
	e.ta.SetWidth(chars)
	e.ta.SetHeight(editorLines)
	box := image.Rect(x, y, x+w, y+editorLines*lineH+2*in)
	c.outline(box, c.pt(8), max(c.pt(1.5), 1), pPurple, pBg)

	type visual struct {
		text   string
		caret  int // rune offset of the caret in this line, or -1
		isLast bool
	}
	var vis []visual
	caretAt := 0
	for i, line := range strings.Split(e.ta.Value(), "\n") {
		r := []rune(line)
		segs := softWrap(r, chars)
		for j, s := range segs {
			v := visual{text: string(r[s[0]:s[1]]), caret: -1}
			col := e.ta.Column()
			if i == e.ta.Line() && col >= s[0] && (col < s[1] || (col == s[1] && j == len(segs)-1)) {
				v.caret = col - s[0]
				caretAt = len(vis)
			}
			vis = append(vis, v)
		}
	}
	first := max(caretAt-editorLines+1, 0)
	if e.ta.Value() == "" {
		c.text(x+in, y+in+(lineH-f.height)/2, "Write in Markdown…", f, pFaint)
	}
	for i := first; i < min(len(vis), first+editorLines); i++ {
		ly := y + in + (i-first)*lineH
		c.text(x+in, ly+(lineH-f.height)/2, expandTabs(vis[i].text), f, pText)
		if vis[i].caret >= 0 && !e.posting {
			cx := x + in + vis[i].caret*f.advance
			c.fill(image.Rect(cx, ly+c.pt(2), cx+max(c.pt(2), 2), ly+lineH-c.pt(2)), pLilac)
		}
	}
	c.scrollbar(image.Rect(box.Max.X-c.pt(8), box.Min.Y+in, box.Max.X-c.pt(4), box.Max.Y-in), len(vis), editorLines, first)
	y = box.Max.Y + c.pt(14)

	small := c.font(sans, 12)
	switch {
	case e.posting:
		c.spinner(x+c.pt(8), y+c.pt(9), c.pt(7), spinPhase())
		c.text(x+c.pt(24), y+(c.pt(18)-small.height)/2, "posting…", small, pMuted)
	case e.err != "":
		hw := c.hints(x, y, "alt+enter", "submit", "esc", "cancel")
		c.text(x+hw+c.pt(6), y+(c.pt(18)-small.height)/2, small.fit(e.err, w-hw-c.pt(6)), small, pRed)
	default:
		c.hints(x, y, "alt+enter", "submit", "esc", "cancel")
	}
	return y + c.pt(18)
}
