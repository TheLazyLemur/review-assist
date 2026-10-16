package tui

import (
	"fmt"
	"image"
	"image/color"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

var mdParser = goldmark.New(goldmark.WithExtensions(extension.GFM)).Parser()

// A frame lays the same text out several times (to measure, then to draw),
// and the next frame does it again.
var mdCache = map[string]ast.Node{}

func parseMarkdown(src string) ast.Node {
	if n, ok := mdCache[src]; ok {
		return n
	}
	if len(mdCache) > 512 {
		clear(mdCache)
	}
	n := mdParser.Parse(text.NewReader([]byte(src)))
	mdCache[src] = n
	return n
}

type mdStyle struct {
	bold, italic, code, link, strike bool
}

type mdSpan struct {
	text string
	st   mdStyle
	brk  bool // a hard line break
}

// mdText is the type a block of text is set in.
type mdText struct {
	size float64
	col  color.RGBA
	bold bool
}

type mdDoc struct {
	c   *canvas
	src []byte
}

// markdown draws src in a column w wide from (x, y) and returns its height.
func (c *canvas) markdown(src string, x, y, w int, t mdText) int {
	d := mdDoc{c, []byte(src)}
	return d.blocks(parseMarkdown(src), x, y, w, t) - y
}

func (d mdDoc) blocks(parent ast.Node, x, y, w int, t mdText) int {
	c := d.c
	gap := c.pt(t.size * 0.7)
	first := true
	for n := parent.FirstChild(); n != nil; n = n.NextSibling() {
		if !first {
			y += gap
		}
		first = false
		switch n := n.(type) {
		case *ast.Heading:
			size := map[int]float64{1: 1.55, 2: 1.3, 3: 1.1}[n.Level]
			if size == 0 {
				size = 1
			}
			if y > 0 && n.PreviousSibling() != nil {
				y += gap / 2
			}
			y += d.inline(d.spans(n, mdStyle{}), x, y, w, mdText{t.size * size, pText, true})
			if n.Level <= 2 {
				y += c.pt(5)
				c.fill(image.Rect(x, y, x+w, y+max(c.pt(1), 1)), pLine)
				y += max(c.pt(1), 1)
			}
		case *ast.Paragraph, *ast.TextBlock:
			y += d.inline(d.spans(n, mdStyle{}), x, y, w, t)
		case *ast.List:
			y = d.list(n, x, y, w, t)
		case *ast.Blockquote:
			in := c.pt(14)
			end := d.blocks(n, x+in, y, w-in, mdText{t.size, pMuted, t.bold})
			c.round(image.Rect(x, y, x+c.pt(3), end), c.pt(1.5), pPurple)
			y = end
		case *ast.FencedCodeBlock:
			y = d.code(n, x, y, w, t)
		case *ast.CodeBlock:
			y = d.code(n, x, y, w, t)
		case *ast.HTMLBlock:
			y = d.code(n, x, y, w, t)
		case *ast.ThematicBreak:
			y += gap / 2
			c.fill(image.Rect(x, y, x+w, y+max(c.pt(1), 1)), pLine)
			y += gap / 2
		case *east.Table:
			y = d.table(n, x, y, w, t)
		default:
			y = d.blocks(n, x, y, w, t)
		}
	}
	return y
}

func (d mdDoc) list(l *ast.List, x, y, w int, t mdText) int {
	c := d.c
	f := c.font(sans, t.size)
	in := c.pt(t.size * 1.6)
	num := l.Start
	first := true
	for item := l.FirstChild(); item != nil; item = item.NextSibling() {
		if !first {
			y += c.pt(t.size * 0.3)
		}
		first = false
		if l.IsOrdered() {
			c.textRight(x+in-c.pt(6), y+c.pt(t.size*0.2), fmt.Sprintf("%d.", num), f, pFaint)
			num++
		} else {
			c.disc(x+in/2, y+c.pt(t.size*0.2)+f.height/2, max(c.pt(2), 1), pFaint)
		}
		y = d.blocks(item, x+in, y, w-in, t)
	}
	return y
}

type lineSource interface{ Lines() *text.Segments }

func (d mdDoc) code(n lineSource, x, y, w int, t mdText) int {
	c := d.c
	f := c.font(mono, t.size*0.92)
	pad := c.pt(10)
	lines := n.Lines()
	h := lines.Len()*(f.height+c.pt(2)) + 2*pad
	box := image.Rect(x, y, x+w, y+h)
	c.round(box, c.pt(6), hex(0x121319))
	in := c.clip(box.Inset(pad / 2))
	for i := range lines.Len() {
		seg := lines.At(i)
		in.text(x+pad, y+pad+i*(f.height+c.pt(2)), expandTabs(strings.TrimRight(string(seg.Value(d.src)), "\n")), f, hex(0xcfd2e6))
	}
	return y + h
}

func (d mdDoc) table(tbl *east.Table, x, y, w int, t mdText) int {
	c := d.c
	pad := c.pt(10)
	var rows [][][]mdSpan
	var header []bool
	for row := tbl.FirstChild(); row != nil; row = row.NextSibling() {
		var cells [][]mdSpan
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, d.spans(cell, mdStyle{}))
		}
		_, isHeader := row.(*east.TableHeader)
		rows, header = append(rows, cells), append(header, isHeader)
	}
	if len(rows) == 0 {
		return y
	}
	// Each column is as wide as its widest cell; the widest gives way when
	// the table does not fit.
	widths := make([]int, len(rows[0]))
	for r, cells := range rows {
		for i, cell := range cells {
			if i < len(widths) {
				_, natural := d.layout(cell, 1<<20, mdText{t.size, t.col, header[r]})
				widths[i] = max(widths[i], natural)
			}
		}
	}
	total := func() int {
		sum := 0
		for _, cw := range widths {
			sum += cw + 2*pad
		}
		return sum
	}
	for over := total() - w; over > 0; over = total() - w {
		widest := 0
		for i := range widths {
			if widths[i] > widths[widest] {
				widest = i
			}
		}
		if widths[widest] <= c.pt(60) {
			break
		}
		widths[widest] = max(widths[widest]-over, c.pt(60))
	}
	for r, cells := range rows {
		ct := mdText{t.size, t.col, header[r]}
		rowH := 0
		for i, cell := range cells {
			if i < len(widths) {
				rowH = max(rowH, mdDoc{c.measuring(), d.src}.inline(cell, 0, 0, widths[i], ct))
			}
		}
		if header[r] {
			c.round(image.Rect(x, y, x+total(), y+rowH+pad), c.pt(4), pRaise)
		}
		cx := x
		for i, cell := range cells {
			if i < len(widths) {
				d.inline(cell, cx+pad, y+pad/2, widths[i], ct)
				cx += widths[i] + 2*pad
			}
		}
		y += rowH + pad
		c.fill(image.Rect(x, y, x+total(), y+max(c.pt(1), 1)), pLine)
	}
	return y
}

func (d mdDoc) plain(n ast.Node) string {
	var b strings.Builder
	for _, s := range d.spans(n, mdStyle{}) {
		b.WriteString(s.text)
	}
	return b.String()
}

func (d mdDoc) spans(n ast.Node, st mdStyle) []mdSpan {
	var out []mdSpan
	for ch := n.FirstChild(); ch != nil; ch = ch.NextSibling() {
		switch ch := ch.(type) {
		case *ast.Text:
			out = append(out, mdSpan{text: string(ch.Segment.Value(d.src)), st: st})
			if ch.HardLineBreak() {
				out = append(out, mdSpan{brk: true})
			} else if ch.SoftLineBreak() {
				out = append(out, mdSpan{text: " ", st: st})
			}
		case *ast.String:
			out = append(out, mdSpan{text: string(ch.Value), st: st})
		case *ast.Emphasis:
			s := st
			if ch.Level >= 2 {
				s.bold = true
			} else {
				s.italic = true
			}
			out = append(out, d.spans(ch, s)...)
		case *ast.CodeSpan:
			s := st
			s.code = true
			out = append(out, mdSpan{text: d.plain(ch), st: s})
		case *ast.Link:
			s := st
			s.link = true
			out = append(out, d.spans(ch, s)...)
		case *ast.AutoLink:
			s := st
			s.link = true
			out = append(out, mdSpan{text: string(ch.URL(d.src)), st: s})
		case *ast.Image:
			s := st
			s.italic = true
			out = append(out, mdSpan{text: "[image: " + d.plain(ch) + "]", st: s})
		case *east.Strikethrough:
			s := st
			s.strike = true
			out = append(out, d.spans(ch, s)...)
		case *east.TaskCheckBox:
			box := "[ ] "
			if ch.IsChecked {
				box = "[x] "
			}
			s := st
			s.code = true
			out = append(out, mdSpan{text: box, st: s}, mdSpan{text: " ", st: st})
		case *ast.RawHTML:
			for i := range ch.Segments.Len() {
				seg := ch.Segments.At(i)
				out = append(out, mdSpan{text: string(seg.Value(d.src)), st: st})
			}
		default:
			out = append(out, d.spans(ch, st)...)
		}
	}
	return out
}

type mdWord struct {
	x    int
	text string
	st   mdStyle
	f    *typeface
}

func (d mdDoc) face(st mdStyle, t mdText) *typeface {
	switch {
	case st.code:
		return d.c.font(mono, t.size*0.92)
	case (st.bold || t.bold) && st.italic:
		return d.c.font(sansBoldItalic, t.size)
	case st.bold || t.bold:
		return d.c.font(sansBold, t.size)
	case st.italic:
		return d.c.font(sansItalic, t.size)
	default:
		return d.c.font(sans, t.size)
	}
}

// layout breaks spans into lines of at most w pixels. It also returns the
// width of the widest line.
func (d mdDoc) layout(spans []mdSpan, w int, t mdText) ([][]mdWord, int) {
	var lines [][]mdWord
	var line []mdWord
	x, widest, space := 0, 0, false
	flush := func() {
		lines, line = append(lines, line), nil
		widest, x, space = max(widest, x), 0, false
	}
	place := func(s string, st mdStyle) {
		f := d.face(st, t)
		gap := 0
		if space && x > 0 {
			gap = d.c.font(sans, t.size).spaceW
		}
		ww := f.width(s)
		if x+gap+ww > w && x > 0 {
			flush()
			gap = 0
		}
		if ww > w {
			s = f.fit(s, w)
			ww = f.width(s)
		}
		line = append(line, mdWord{x + gap, s, st, f})
		x += gap + ww
		space = false
	}
	for _, s := range spans {
		switch {
		case s.brk:
			flush()
		case s.st.code:
			place(s.text, s.st)
		default:
			if strings.HasPrefix(s.text, " ") {
				space = true
			}
			for _, word := range strings.Fields(s.text) {
				place(word, s.st)
				space = true
			}
			space = strings.HasSuffix(s.text, " ") || strings.HasSuffix(s.text, "\n")
		}
	}
	if len(line) > 0 || len(lines) == 0 {
		flush()
	}
	return lines, widest
}

func (d mdDoc) inline(spans []mdSpan, x, y, w int, t mdText) int {
	c := d.c
	lines, _ := d.layout(spans, w, t)
	lineH := c.pt(t.size * 1.5)
	for i, line := range lines {
		top := y + i*lineH
		for _, word := range line {
			wy := top + (lineH-word.f.height)/2
			col := t.col
			switch {
			case word.st.code:
				ww := word.f.width(word.text)
				c.round(image.Rect(x+word.x-c.pt(3), top+c.pt(1), x+word.x+ww+c.pt(3), top+lineH-c.pt(1)), c.pt(4), pRaise)
				col = pOrange
			case word.st.link:
				col = pBlue
			}
			ww := c.text(x+word.x, wy, word.text, word.f, col)
			if word.st.link {
				c.fill(image.Rect(x+word.x, wy+word.f.ascent+c.pt(2), x+word.x+ww, wy+word.f.ascent+c.pt(2)+max(c.pt(1), 1)), alpha(pBlue, 120))
			}
			if word.st.strike {
				c.fill(image.Rect(x+word.x, top+lineH/2, x+word.x+ww, top+lineH/2+max(c.pt(1), 1)), col)
			}
		}
	}
	return len(lines) * lineH
}
