package tui

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"image"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"
)

var kittyEscape = regexp.MustCompile(`\x1b_G([^;\x1b]*);([^\x1b]*)\x1b\\`)

// placedRows reads a frame the way a terminal does: it joins the chunks of
// each image and inflates them, keyed by the image's control data.
func placedRows(t *testing.T, frame string) map[string][]byte {
	t.Helper()
	payloads := map[string]*strings.Builder{}
	ctl := ""
	for _, m := range kittyEscape.FindAllStringSubmatch(frame, -1) {
		if first, _, _ := strings.Cut(m[1], ",m="); first != m[1] {
			ctl = first
			payloads[ctl] = &strings.Builder{}
		}
		payloads[ctl].WriteString(m[2])
	}
	rows := map[string][]byte{}
	for ctl, b64 := range payloads {
		packed, err := base64.StdEncoding.DecodeString(b64.String())
		if err != nil {
			t.Fatalf("%s: %v", ctl, err)
		}
		z, err := zlib.NewReader(bytes.NewReader(packed))
		if err != nil {
			t.Fatalf("%s: %v", ctl, err)
		}
		if rows[ctl], err = io.ReadAll(z); err != nil {
			t.Fatalf("%s: %v", ctl, err)
		}
	}
	return rows
}

var kittyPlacement = regexp.MustCompile(`\x1b\[(\d+;\d+)H\x1b_Ga=p,i=(\d+),`)

// placedAt maps each cell a frame places an image at to that image's id.
func placedAt(frame string) map[string]string {
	at := map[string]string{}
	for _, m := range kittyPlacement.FindAllStringSubmatch(frame, -1) {
		at[m[1]] = m[2]
	}
	return at
}

// noiseScreen is a screen of 2 cells of 40x20 pixels wide whose every cell
// row is different. Noise keeps a row's payload longer than one chunk.
func noiseScreen(rows int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 80, rows*20))
	for i := range img.Pix {
		img.Pix[i] = uint8(i * 2654435761 >> 13)
	}
	return img
}

func TestFrameSendsOnlyTheTilesThatChanged(t *testing.T) {
	// given
	// ... a screen of 3 cell rows, already drawn, whose middle row alone
	// ... then changes
	tiles := newKittyTiles(2, 3, 40, 20, false)
	img := noiseScreen(3)
	tiles.frame(img)
	middle := img.Pix[20*img.Stride : 40*img.Stride]
	for i := range middle {
		middle[i] ^= 0xff
	}

	// when
	// ... the next frame is encoded
	frame := tiles.frame(img)

	// then
	// ... it sends one image, holding that row's pixels, and places it on
	// ... the second row
	sent := placedRows(t, frame)
	got, ok := sent["a=t,f=32,o=z,s=80,v=20,i=7340004,q=2"]
	if len(sent) != 1 || !ok {
		t.Fatalf("want only image 4 sent, got %d images", len(sent))
	}
	if !bytes.Equal(got, middle) {
		t.Error("image 4 does not hold the middle row's pixels")
	}
	if at := placedAt(frame); len(at) != 1 || at["2;1"] != "7340004" {
		t.Errorf("want only image 4 placed, at the start of row 2, got %v", at)
	}
}

func TestFrameMovesScrolledTilesWithoutSendingThemAgain(t *testing.T) {
	// given
	// ... a screen of 3 cell rows, already drawn, that then scrolls up by
	// ... one row, bringing new pixels onto the last
	tiles := newKittyTiles(2, 3, 40, 20, false)
	img := noiseScreen(3)
	tiles.frame(img)
	copy(img.Pix, img.Pix[20*img.Stride:])
	last := img.Pix[40*img.Stride:]
	for i := range last {
		last[i] ^= 0xff
	}

	// when
	// ... the next frame is encoded
	frame := tiles.frame(img)

	// then
	// ... it sends the new row alone and places the images of the old
	// ... second and third rows a row higher
	if sent := placedRows(t, frame); len(sent) != 1 {
		t.Errorf("want one image sent, got %d", len(sent))
	}
	at := placedAt(frame)
	if at["1;1"] != "7340002" || at["2;1"] != "7340003" || at["3;1"] != "7340004" {
		t.Errorf("want images 2, 3 and 4 on rows 1, 2 and 3, got %v", at)
	}
}

func TestFrameDeletesACoveredPlacementInTheNextFrame(t *testing.T) {
	// given
	// ... a screen of 2 cell rows, already drawn
	tiles := newKittyTiles(2, 2, 40, 20, false)
	img := noiseScreen(2)
	tiles.frame(img)
	const deleted = "\x1b_Ga=d,d=i,i=7340001,p=1,q=2\x1b\\"

	// when
	// ... a change to the first row is encoded, and then one to the second
	img.Pix[0] ^= 0xff
	covering := tiles.frame(img)
	img.Pix[20*img.Stride] ^= 0xff
	next := tiles.frame(img)

	// then
	// ... the first row's old placement outlives the frame that covers it
	// ... and is deleted by the one after
	if strings.Contains(covering, deleted) {
		t.Error("the placement is deleted by the frame that covers it")
	}
	if !strings.Contains(next, deleted) {
		t.Error("the placement is not deleted by the next frame")
	}
}

func TestFrameFreesAnImageOnlyOnceItHasGoneUnusedAWhile(t *testing.T) {
	// given
	// ... a screen of 2 cell rows, already drawn, whose first row has
	// ... changed, leaving its old image unused
	now := time.Unix(0, 0)
	tiles := newKittyTiles(2, 2, 40, 20, false)
	tiles.now = func() time.Time { return now }
	img := noiseScreen(2)
	tiles.frame(img)
	img.Pix[0] ^= 0xff
	tiles.frame(img)
	const freed = "\x1b_Ga=d,d=I,i=7340001,q=2\x1b\\"

	// when
	// ... a frame is encoded a moment later, and another after a while
	now = now.Add(100 * time.Millisecond)
	img.Pix[20*img.Stride] ^= 0xff
	soon := tiles.frame(img)
	now = now.Add(kittyKeep)
	img.Pix[20*img.Stride] ^= 0xff
	later := tiles.frame(img)

	// then
	// ... only the later frame frees the image
	if strings.Contains(soon, freed) {
		t.Error("image 1 is freed while a terminal may still show it")
	}
	if !strings.Contains(later, freed) {
		t.Error("image 1 is never freed")
	}
}
