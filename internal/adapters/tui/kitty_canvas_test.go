package tui

import (
	"image"
	"testing"
)

func TestShapesScrolledPastTheCanvasEdgeAreClipped(t *testing.T) {
	// given
	// ... a canvas, as the overview is when its checks are scrolled out of view
	c := &canvas{img: image.NewRGBA(image.Rect(0, 0, 40, 40)), u: 1, fonts: newFontCache()}

	// when
	// ... a tick is drawn across the bottom edge and another wholly below it
	c.check(20, 39, 12, pGreen)
	c.check(20, 400, 12, pGreen)

	// then
	// ... the part inside the canvas is drawn
	if c.img.RGBAAt(17, 39).A == 0 {
		t.Error("the tick across the edge left no pixels inside the canvas")
	}
}
