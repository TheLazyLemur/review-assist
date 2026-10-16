//go:build unix

package tui

import (
	"os"

	"golang.org/x/sys/unix"
)

// ttyPixels is the terminal's size in cells and in pixels.
func ttyPixels(tty *os.File) (cols, rows, xpix, ypix int, err error) {
	ws, err := unix.IoctlGetWinsize(int(tty.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	return int(ws.Col), int(ws.Row), int(ws.Xpixel), int(ws.Ypixel), nil
}
