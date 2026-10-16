//go:build !unix

package tui

import (
	"errors"
	"os"
)

func ttyPixels(*os.File) (cols, rows, xpix, ypix int, err error) {
	return 0, 0, 0, 0, errors.New("not supported on this platform")
}
