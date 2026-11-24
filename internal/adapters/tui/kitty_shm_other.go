//go:build !darwin && !linux

package tui

import "errors"

func shmPut([]byte) (string, error) { return "", errors.New("not supported on this platform") }

func kittyReadsShm() bool { return false }
