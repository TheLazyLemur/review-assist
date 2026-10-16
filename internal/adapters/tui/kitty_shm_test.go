//go:build darwin || linux

package tui

import (
	"bytes"
	"encoding/base64"
	"image"
	"testing"

	"golang.org/x/sys/unix"
)

func TestFrameHandsATileOverInSharedMemory(t *testing.T) {
	// given
	// ... an undrawn screen of one cell row of 80x20 pixels
	img := image.NewRGBA(image.Rect(0, 0, 80, 20))
	for i := range img.Pix {
		img.Pix[i] = uint8(i)
	}

	// when
	// ... a frame is encoded for a terminal that reads shared memory
	frame := newKittyTiles(2, 1, 40, 20, true).frame(img)

	// then
	// ... it sends one image, naming an object that holds the row's pixels
	escapes := kittyEscape.FindAllStringSubmatch(frame, -1)
	if len(escapes) != 1 || escapes[0][1] != "a=t,f=32,t=s,s=80,v=20,i=7340001,q=2,m=0" {
		t.Fatalf("want only image 1 sent, from shared memory, got %q", escapes)
	}
	name, err := base64.StdEncoding.DecodeString(escapes[0][2])
	if err != nil {
		t.Fatal(err)
	}
	fd, err := shmOpen(string(name), unix.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer unix.Close(fd)
	defer shmUnlink(string(name))
	got, err := unix.Mmap(fd, 0, len(img.Pix), unix.PROT_READ, unix.MAP_SHARED)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Munmap(got)
	if !bytes.Equal(got, img.Pix) {
		t.Error("the object does not hold the row's pixels")
	}
}
