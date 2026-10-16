package tui

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/gobolditalic"
	"golang.org/x/image/font/gofont/goitalic"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"golang.org/x/image/vector"
)

func hex(v uint32) color.RGBA {
	return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}
}

// alpha returns c at opacity a; color.RGBA is premultiplied.
func alpha(c color.RGBA, a uint8) color.RGBA {
	mul := func(v uint8) uint8 { return uint8(uint16(v) * uint16(a) / 255) }
	return color.RGBA{mul(c.R), mul(c.G), mul(c.B), a}
}

var (
	pBg     = hex(0x17181f)
	pPanel  = hex(0x20212b)
	pRaise  = hex(0x2c2e3d)
	pLine   = hex(0x363849)
	pText   = hex(0xe9e9f1)
	pMuted  = hex(0xa6a8bc)
	pFaint  = hex(0x6d7088)
	pPurple = hex(0x7d56f4)
	pLilac  = hex(0xbd93f9)
	pGreen  = hex(0x2fd08a)
	pRed    = hex(0xff5c7c)
	pYellow = hex(0xf1e27a)
	pOrange = hex(0xffb86c)
	pBlue   = hex(0x8be9fd)
	pPink   = hex(0xff79c6)
	pWhite  = hex(0xfafafa)
	pAddBg  = hex(0x16302a)
	pDelBg  = hex(0x3a1b28)
)

type fontStyle int

const (
	sans fontStyle = iota
	sansBold
	sansItalic
	sansBoldItalic
	mono
	monoBold
)

var fontData = map[fontStyle][]byte{
	sans:           goregular.TTF,
	sansBold:       gobold.TTF,
	sansItalic:     goitalic.TTF,
	sansBoldItalic: gobolditalic.TTF,
	mono:           gomono.TTF,
	monoBold:       gomonobold.TTF,
}

type glyph struct {
	mask *image.Alpha
	off  image.Point // from the pen on the baseline to the mask's top left
	adv  fixed.Int26_6
}

// typeface is one font at one pixel size. Glyphs are rasterised once, at a
// whole-pixel pen position, and kept for the life of the program.
type typeface struct {
	face    font.Face
	ascent  int
	height  int
	glyphs  map[rune]glyph
	spaceW  int
	isMono  bool
	advance int // of one cell, for mono faces
}

func (t *typeface) glyph(r rune) glyph {
	g, ok := t.glyphs[r]
	if ok {
		return g
	}
	dr, mask, maskp, adv, _ := t.face.Glyph(fixed.Point26_6{}, r)
	if !dr.Empty() {
		// The face reuses its mask buffer between calls.
		g.mask = image.NewAlpha(image.Rect(0, 0, dr.Dx(), dr.Dy()))
		draw.Draw(g.mask, g.mask.Bounds(), mask, maskp, draw.Src)
	}
	g.off, g.adv = dr.Min, adv
	t.glyphs[r] = g
	return g
}

func (t *typeface) width(s string) int {
	var w fixed.Int26_6
	for _, r := range s {
		w += t.glyph(r).adv
	}
	return w.Round()
}

// fit truncates s to w pixels, ending it with an ellipsis.
func (t *typeface) fit(s string, w int) string {
	if t.width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && t.width(string(r)+"…") > w {
		r = r[:len(r)-1]
	}
	return strings.TrimRight(string(r), " ") + "…"
}

// wrap breaks s at spaces into lines of at most w pixels. A word wider than
// w is split.
func (t *typeface) wrap(s string, w int) []string {
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			for t.width(word) > w && len([]rune(word)) > 1 {
				r := []rune(word)
				n := len(r) - 1
				for n > 1 && t.width(string(r[:n])) > w {
					n--
				}
				if line != "" {
					lines, line = append(lines, line), ""
				}
				lines = append(lines, string(r[:n]))
				word = string(r[n:])
			}
			switch {
			case line == "":
				line = word
			case t.width(line+" "+word) <= w:
				line += " " + word
			default:
				lines, line = append(lines, line), word
			}
		}
		lines = append(lines, line)
	}
	return lines
}

type fontKey struct {
	style fontStyle
	px    int
}

type fontCache struct {
	parsed map[fontStyle]*opentype.Font
	faces  map[fontKey]*typeface
}

func newFontCache() *fontCache {
	return &fontCache{parsed: map[fontStyle]*opentype.Font{}, faces: map[fontKey]*typeface{}}
}

func (fc *fontCache) get(style fontStyle, px int) *typeface {
	key := fontKey{style, px}
	if t, ok := fc.faces[key]; ok {
		return t
	}
	f, ok := fc.parsed[style]
	if !ok {
		var err error
		if f, err = opentype.Parse(fontData[style]); err != nil {
			panic("embedded font does not parse: " + err.Error())
		}
		fc.parsed[style] = f
	}
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: float64(px), DPI: 72, Hinting: font.HintingNone})
	if err != nil {
		panic("font face: " + err.Error())
	}
	m := face.Metrics()
	t := &typeface{face: face, ascent: m.Ascent.Ceil(), glyphs: map[rune]glyph{}, isMono: style == mono || style == monoBold}
	t.height = t.ascent + m.Descent.Ceil()
	t.spaceW = t.width(" ")
	t.advance = t.width("0")
	fc.faces[key] = t
	return t
}

// canvas draws in device pixels. Sizes are given in points and scaled by u,
// so the UI follows the terminal's font size.
type canvas struct {
	img   *image.RGBA
	u     float64
	fonts *fontCache
	dry   bool // measure only: every drawing call is a no-op
}

func (c *canvas) pt(n float64) int { return int(math.Round(n * c.u)) }

func (c *canvas) font(style fontStyle, pt float64) *typeface {
	return c.fonts.get(style, c.pt(pt))
}

func (c *canvas) clip(r image.Rectangle) *canvas {
	cc := *c
	cc.img = c.img.SubImage(r).(*image.RGBA)
	return &cc
}

func (c *canvas) measuring() *canvas {
	cc := *c
	cc.dry = true
	return &cc
}

func (c *canvas) fill(r image.Rectangle, col color.RGBA) {
	if c.dry {
		return
	}
	draw.Draw(c.img, r, image.NewUniform(col), image.Point{}, draw.Over)
}

// dim darkens r, as a backdrop behind a modal. A translucent fill of the
// whole screen through image/draw costs several frames.
func (c *canvas) dim(r image.Rectangle) {
	if c.dry {
		return
	}
	r = r.Intersect(c.img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		row := c.img.Pix[c.img.PixOffset(r.Min.X, y):c.img.PixOffset(r.Max.X, y)]
		for i := 0; i < len(row); i += 4 {
			for j := range 3 {
				row[i+j] = uint8(uint16(row[i+j]) * 2 / 5)
			}
		}
	}
}

var circleMasks = map[int]*image.Alpha{}

// circleMask is a disc of radius r, anti-aliased by 4x4 supersampling.
func circleMask(r int) *image.Alpha {
	if m, ok := circleMasks[r]; ok {
		return m
	}
	m := image.NewAlpha(image.Rect(0, 0, 2*r, 2*r))
	for y := range 2 * r {
		for x := range 2 * r {
			hit := 0
			for sy := range 4 {
				for sx := range 4 {
					dx := float64(x) + (float64(sx)+0.5)/4 - float64(r)
					dy := float64(y) + (float64(sy)+0.5)/4 - float64(r)
					if dx*dx+dy*dy <= float64(r*r) {
						hit++
					}
				}
			}
			m.SetAlpha(x, y, color.Alpha{uint8(hit * 255 / 16)})
		}
	}
	circleMasks[r] = m
	return m
}

// round fills a rectangle with rounded corners.
func (c *canvas) round(r image.Rectangle, rad int, col color.RGBA) {
	if c.dry || r.Empty() {
		return
	}
	rad = min(rad, r.Dx()/2, r.Dy()/2)
	if rad <= 0 {
		c.fill(r, col)
		return
	}
	src := image.NewUniform(col)
	c.fill(image.Rect(r.Min.X, r.Min.Y+rad, r.Max.X, r.Max.Y-rad), col)
	c.fill(image.Rect(r.Min.X+rad, r.Min.Y, r.Max.X-rad, r.Min.Y+rad), col)
	c.fill(image.Rect(r.Min.X+rad, r.Max.Y-rad, r.Max.X-rad, r.Max.Y), col)
	m := circleMask(rad)
	for _, q := range []struct{ x, y, mx, my int }{
		{r.Min.X, r.Min.Y, 0, 0}, {r.Max.X - rad, r.Min.Y, rad, 0},
		{r.Min.X, r.Max.Y - rad, 0, rad}, {r.Max.X - rad, r.Max.Y - rad, rad, rad},
	} {
		draw.DrawMask(c.img, image.Rect(q.x, q.y, q.x+rad, q.y+rad), src, image.Point{}, m, image.Pt(q.mx, q.my), draw.Over)
	}
}

// outline is a rounded rectangle with a border of w pixels.
func (c *canvas) outline(r image.Rectangle, rad, w int, border, fill color.RGBA) {
	c.round(r, rad, border)
	c.round(r.Inset(w), rad-w, fill)
}

// card is a rounded panel with an accent bar down its left edge.
func (c *canvas) card(r image.Rectangle, accent, fill color.RGBA) {
	rad := c.pt(6)
	c.round(r, rad, accent)
	c.round(image.Rect(r.Min.X+c.pt(3), r.Min.Y, r.Max.X, r.Max.Y), rad, fill)
}

func (c *canvas) disc(cx, cy, r int, col color.RGBA) {
	c.round(image.Rect(cx-r, cy-r, cx+r, cy+r), r, col)
}

// poly fills a polygon given as x, y pairs.
func (c *canvas) poly(col color.RGBA, pts ...float64) {
	if c.dry {
		return
	}
	minX, minY, maxX, maxY := pts[0], pts[1], pts[0], pts[1]
	for i := 0; i < len(pts); i += 2 {
		minX, maxX = min(minX, pts[i]), max(maxX, pts[i])
		minY, maxY = min(minY, pts[i+1]), max(maxY, pts[i+1])
	}
	box := image.Rect(int(minX), int(minY), int(maxX)+1, int(maxY)+1)
	z := vector.NewRasterizer(box.Dx(), box.Dy())
	ox, oy := float64(box.Min.X), float64(box.Min.Y)
	z.MoveTo(float32(pts[0]-ox), float32(pts[1]-oy))
	for i := 2; i < len(pts); i += 2 {
		z.LineTo(float32(pts[i]-ox), float32(pts[i+1]-oy))
	}
	z.ClosePath()
	// The rasteriser does not clip to its destination; image/draw does.
	mask := image.NewAlpha(image.Rect(0, 0, box.Dx(), box.Dy()))
	z.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})
	draw.DrawMask(c.img, box, image.NewUniform(col), image.Point{}, mask, image.Point{}, draw.Over)
}

func (c *canvas) line(x1, y1, x2, y2, w float64, col color.RGBA) {
	dx, dy := x2-x1, y2-y1
	l := math.Hypot(dx, dy)
	if l == 0 {
		return
	}
	nx, ny := -dy/l*w/2, dx/l*w/2
	c.poly(col, x1+nx, y1+ny, x2+nx, y2+ny, x2-nx, y2-ny, x1-nx, y1-ny)
}

// text draws s with the top of its line box at y and returns its width.
func (c *canvas) text(x, y int, s string, f *typeface, col color.RGBA) int {
	pen := fixed.I(x)
	b := image.Rectangle{}
	if !c.dry {
		b = c.img.Bounds()
	}
	visible := !c.dry && y < b.Max.Y && y+f.height > b.Min.Y
	src := image.NewUniform(col)
	for _, r := range s {
		g := f.glyph(r)
		if visible && g.mask != nil {
			at := image.Pt(pen.Round(), y+f.ascent).Add(g.off)
			draw.DrawMask(c.img, g.mask.Bounds().Add(at), src, image.Point{}, g.mask, image.Point{}, draw.Over)
		}
		pen += g.adv
	}
	return pen.Round() - x
}

// textIn draws s vertically centred in r, from r's left edge.
func (c *canvas) textIn(r image.Rectangle, s string, f *typeface, col color.RGBA) int {
	return c.text(r.Min.X, r.Min.Y+(r.Dy()-f.height)/2, s, f, col)
}

func (c *canvas) textRight(right, y int, s string, f *typeface, col color.RGBA) int {
	w := f.width(s)
	c.text(right-w, y, s, f, col)
	return w
}

// ---- widgets ----

// pill draws a small rounded label and returns its width.
func (c *canvas) pill(x, y int, s string, fg, bg color.RGBA) int {
	f := c.font(sansBold, 10.5)
	h, pad := c.pt(18), c.pt(8)
	w := f.width(s) + 2*pad
	r := image.Rect(x, y, x+w, y+h)
	c.round(r, h/2, bg)
	c.textIn(r.Add(image.Pt(pad, 0)), s, f, fg)
	return w
}

// tint is the usual pill: coloured text on a faded fill of the same colour.
func (c *canvas) tint(x, y int, s string, col color.RGBA) int {
	return c.pill(x, y, s, col, blend(pBg, col, 0.2))
}

func blend(a, b color.RGBA, t float64) color.RGBA {
	mix := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t) }
	return color.RGBA{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B), 255}
}

func (c *canvas) keycap(x, y int, key string) int {
	f := c.font(mono, 10.5)
	h, pad := c.pt(18), c.pt(6)
	w := max(f.width(key)+2*pad, h)
	r := image.Rect(x, y, x+w, y+h)
	c.outline(r, c.pt(4), max(c.pt(1), 1), pLine, pRaise)
	c.text(x+(w-f.width(key))/2, y+(h-f.height)/2, key, f, pPink)
	return w
}

// hints draws key/label pairs in a row and returns the width used.
func (c *canvas) hints(x, y int, pairs ...string) int {
	f := c.font(sans, 11)
	start := x
	for i := 0; i+1 < len(pairs); i += 2 {
		x += c.keycap(x, y, pairs[i]) + c.pt(5)
		x += c.text(x, y+(c.pt(18)-f.height)/2, pairs[i+1], f, pFaint) + c.pt(14)
	}
	return x - start
}

var avatarColours = []color.RGBA{pPurple, hex(0x3d8bfd), hex(0x1fa588), hex(0xd9730d), hex(0xc2417d), hex(0x7a8a2a)}

// avatar is a disc in a colour picked by the login, with its initial.
func (c *canvas) avatar(x, y, d int, login string) {
	sum := 0
	for _, r := range login {
		sum += int(r)
	}
	c.disc(x+d/2, y+d/2, d/2, avatarColours[sum%len(avatarColours)])
	if login == "" {
		return
	}
	f := c.fonts.get(sansBold, d*11/20)
	s := strings.ToUpper(string([]rune(login)[:1]))
	c.text(x+(d-f.width(s))/2, y+(d-f.height)/2, s, f, pWhite)
}

func (c *canvas) check(cx, cy, size int, col color.RGBA) {
	s, w := float64(size), max(float64(size)/5, 1.5)
	x, y := float64(cx), float64(cy)
	c.line(x-s*0.45, y, x-s*0.12, y+s*0.33, w, col)
	c.line(x-s*0.18, y+s*0.33, x+s*0.45, y-s*0.33, w, col)
}

func (c *canvas) cross(cx, cy, size int, col color.RGBA) {
	s, w := float64(size)*0.36, max(float64(size)/5, 1.5)
	x, y := float64(cx), float64(cy)
	c.line(x-s, y-s, x+s, y+s, w, col)
	c.line(x-s, y+s, x+s, y-s, w, col)
}

func (c *canvas) diamond(cx, cy, size int, col color.RGBA) {
	x, y, s := float64(cx), float64(cy), float64(size)/2
	c.poly(col, x, y-s, x+s, y, x, y+s, x-s, y)
}

// spinner is a ring of dots whose brightest one moves with phase.
func (c *canvas) spinner(cx, cy, r int, phase float64) {
	const dots = 8
	for i := range dots {
		a := 2 * math.Pi * float64(i) / dots
		age := math.Mod(float64(i)/dots-phase+1, 1)
		c.disc(cx+int(math.Round(float64(r)*math.Cos(a))), cy+int(math.Round(float64(r)*math.Sin(a))),
			max(r/4, 1), alpha(pLilac, uint8(255-200*age)))
	}
}

// bar splits r between green and red by the share of additions.
func (c *canvas) bar(r image.Rectangle, add, del int) {
	c.round(r, r.Dy()/2, pLine)
	if add+del == 0 {
		return
	}
	split := r.Min.X + r.Dx()*add/(add+del)
	if del == 0 {
		c.round(r, r.Dy()/2, pGreen)
		return
	}
	c.round(r, r.Dy()/2, pRed)
	if add > 0 {
		c.round(image.Rect(r.Min.X, r.Min.Y, max(split, r.Min.X+r.Dy()), r.Max.Y), r.Dy()/2, pGreen)
	}
}

// scrollbar draws a thumb in track for a view of size shown at offset into total.
func (c *canvas) scrollbar(track image.Rectangle, total, shown, offset int) {
	if total <= shown {
		return
	}
	h := max(track.Dy()*shown/total, c.pt(20))
	y := track.Min.Y + (track.Dy()-h)*offset/(total-shown)
	c.round(image.Rect(track.Min.X, y, track.Max.X, y+h), track.Dx()/2, pLine)
}
