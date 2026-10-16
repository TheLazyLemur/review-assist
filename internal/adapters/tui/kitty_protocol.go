package tui

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"fmt"
	"hash/maphash"
	"image"
	"strings"
	"sync"
	"time"
)

const (
	kittyChunk     = 4096 // the protocol's limit on one escape's payload
	kittyDeleteAll = "\x1b_Ga=d,d=A,q=2\x1b\\"
	// Image ids are shared with whatever else has drawn in this terminal;
	// ours count up from here.
	kittyIDBase = 7340000
	// A pane that scrolls sits beside ones that do not. Tiles this narrow
	// keep their pixels apart, so the scrolled ones match what was sent.
	kittyTileCols = 8
	// A multiplexer frees an image on the terminal as soon as it is told to,
	// but shows a frame only when it next draws, which may be several frames
	// on. An image freed the moment it leaves the screen goes blank there
	// until then. Kept this long, it also serves a scroll back.
	kittyKeep = 2 * time.Second
)

var kittySeed = maphash.MakeSeed()

type kittyImage struct {
	id, uses int
	hash     uint64
	unused   time.Time // since when no tile shows it
}

// kittyTiles is what the terminal holds of the screen: an image for each
// distinct tile of pixels, placed wherever the screen shows it. Pixels that
// only moved are placed again rather than sent again, which matters behind a
// multiplexer that passes every image on uncompressed.
type kittyTiles struct {
	cols, rows, cellW, cellH int
	shm                      bool // hand pixels over in shared memory

	images map[uint64]*kittyImage // by the hash of their pixels
	shown  []*kittyImage          // by tile, a row after another
	lastID int
	frames int

	// The placements the last frame covered, deleted by the next: a
	// terminal that does not draw a frame at once never shows the cells bare.
	stale string

	now func() time.Time
}

func newKittyTiles(cols, rows, cellW, cellH int, shm bool) *kittyTiles {
	t := &kittyTiles{cols: cols, rows: rows, cellW: cellW, cellH: cellH, shm: shm, lastID: kittyIDBase, now: time.Now}
	t.images = map[uint64]*kittyImage{}
	t.shown = make([]*kittyImage, rows*t.perRow())
	return t
}

func (t *kittyTiles) perRow() int { return (t.cols + kittyTileCols - 1) / kittyTileCols }

// span is a tile's first cell column and its width in cells.
func (t *kittyTiles) span(tile int) (col, cols int) {
	col = tile % t.perRow() * kittyTileCols
	return col, min(kittyTileCols, t.cols-col)
}

// lines calls f with each pixel row of a tile, top to bottom.
func (t *kittyTiles) lines(img *image.RGBA, tile int, f func([]byte)) {
	col, cols := t.span(tile)
	top := tile / t.perRow() * t.cellH
	for y := top; y < top+t.cellH; y++ {
		lo := y*img.Stride + col*t.cellW*4
		f(img.Pix[lo : lo+cols*t.cellW*4])
	}
}

// free deletes the images by id. On quit the escape reaches the terminal
// after the alt screen is left, where deleting all would take the images in
// the user's scrollback.
func (t *kittyTiles) free() string {
	var b strings.Builder
	for _, im := range t.images {
		fmt.Fprintf(&b, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", im.id)
	}
	return b.String()
}

// frame returns the escapes that turn what the terminal shows into img. The
// first frame clears the terminal's images, which the tiles know nothing of.
func (t *kittyTiles) frame(img *image.RGBA) string {
	hashes := make([]uint64, len(t.shown))
	var wg sync.WaitGroup
	for row := range t.rows {
		wg.Go(func() {
			var h maphash.Hash
			for tile := row * t.perRow(); tile < (row+1)*t.perRow(); tile++ {
				h.SetSeed(kittySeed)
				t.lines(img, tile, func(line []byte) { h.Write(line) })
				hashes[tile] = h.Sum64()
			}
		})
	}
	wg.Wait()

	type replaced struct {
		im   *kittyImage
		tile int
	}
	var fresh []int // tiles whose pixels the terminal has not got
	var old []replaced
	var places, drops strings.Builder
	t.frames++
	for tile, hash := range hashes {
		im := t.images[hash]
		if im != nil && im == t.shown[tile] {
			continue
		}
		if im == nil {
			t.lastID++
			im = &kittyImage{id: t.lastID, hash: hash}
			t.images[hash] = im
			fresh = append(fresh, tile)
		}
		im.uses++
		col, cols := t.span(tile)
		// An image is placed once per tile it shows in; the tile names the
		// placement. A later frame's placements cover an earlier one's.
		fmt.Fprintf(&places, "\x1b[%d;%dH\x1b_Ga=p,i=%d,p=%d,z=%d,c=%d,r=1,C=1,q=2\x1b\\", tile/t.perRow()+1, col+1, im.id, tile+1, t.frames, cols)
		if was := t.shown[tile]; was != nil {
			was.uses--
			old = append(old, replaced{was, tile})
		}
		t.shown[tile] = im
	}
	if places.Len() == 0 {
		return ""
	}
	now := t.now()
	for _, r := range old {
		fmt.Fprintf(&drops, "\x1b_Ga=d,d=i,i=%d,p=%d,q=2\x1b\\", r.im.id, r.tile+1)
		if r.im.uses == 0 {
			r.im.unused = now
		}
	}
	var frees strings.Builder
	for hash, im := range t.images {
		if im.uses == 0 && now.Sub(im.unused) > kittyKeep {
			delete(t.images, hash)
			fmt.Fprintf(&frees, "\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", im.id)
		}
	}

	sends := make([]string, len(fresh))
	for i, tile := range fresh {
		wg.Go(func() {
			_, cols := t.span(tile)
			pix := make([]byte, 0, cols*t.cellW*t.cellH*4)
			t.lines(img, tile, func(line []byte) { pix = append(pix, line...) })
			sends[i] = kittySend(t.shown[tile].id, pix, cols*t.cellW, t.cellH, t.shm)
		})
	}
	wg.Wait()

	clear := t.stale
	if t.frames == 1 {
		clear = kittyDeleteAll
	}
	t.stale = drops.String()
	// Synchronised output, with the cursor saved: the tiles land as one frame
	// and bubbletea's renderer finds the cursor where it left it.
	return "\x1b[?2026h\x1b7" + clear + frees.String() + strings.Join(sends, "") + places.String() + "\x1b8\x1b[?2026l"
}

// kittySend transmits an image's pixels without showing it. q=2 keeps the
// terminal from answering, which would arrive as key presses.
func kittySend(id int, pix []byte, w, h int, shm bool) string {
	var medium, data string
	if shm {
		// Out of descriptors or memory, the image still goes inline.
		if name, err := shmPut(pix); err == nil {
			medium, data = "t=s", base64.StdEncoding.EncodeToString([]byte(name))
		}
	}
	if medium == "" {
		var z bytes.Buffer
		zw, err := zlib.NewWriterLevel(&z, zlib.BestSpeed)
		if err != nil {
			panic(err)
		}
		zw.Write(pix)
		zw.Close()
		medium, data = "o=z", base64.StdEncoding.EncodeToString(z.Bytes())
	}

	var b strings.Builder
	ctl := fmt.Sprintf("a=t,f=32,%s,s=%d,v=%d,i=%d,q=2,", medium, w, h, id)
	for {
		n := min(len(data), kittyChunk)
		more := 0
		if n < len(data) {
			more = 1
		}
		fmt.Fprintf(&b, "\x1b_G%sm=%d;%s\x1b\\", ctl, more, data[:n])
		// Only the first chunk carries the control data.
		data, ctl = data[n:], ""
		if more == 0 {
			return b.String()
		}
	}
}
