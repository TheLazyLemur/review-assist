//go:build darwin || linux

package tui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

var shmSeq atomic.Uint64

// shmPut copies pix into a new shared memory object and returns its name.
// The terminal unlinks the object once it has read it.
func shmPut(pix []byte) (string, error) {
	// darwin allows a name 31 bytes.
	name := fmt.Sprintf("/ra-%d-%d", os.Getpid(), shmSeq.Add(1))
	fd, err := shmOpen(name, unix.O_CREAT|unix.O_EXCL|unix.O_RDWR, 0o600)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)
	err = unix.Ftruncate(fd, int64(len(pix)))
	if err == nil {
		// darwin does not take write(2) on a shared memory object.
		var mem []byte
		if mem, err = unix.Mmap(fd, 0, len(pix), unix.PROT_WRITE, unix.MAP_SHARED); err == nil {
			copy(mem, pix)
			err = unix.Munmap(mem)
		}
	}
	if err != nil {
		shmUnlink(name)
		return "", err
	}
	return name, nil
}

// kittyReadsShm asks the terminal whether it can read an image from shared
// memory. A terminal on another machine, or a multiplexer in between, cannot.
// It must run before bubbletea starts reading the terminal's replies.
func kittyReadsShm() bool {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false
	}
	defer tty.Close()
	fd := int(tty.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return false
	}
	defer term.Restore(fd, old)
	name, err := shmPut(make([]byte, 4))
	if err != nil {
		return false
	}
	// A terminal that answers a query leaves the object in place.
	defer shmUnlink(name)

	// Every terminal answers the cursor position report that follows, so one
	// without the graphics protocol does not keep us waiting.
	fmt.Fprintf(tty, "\x1b_Ga=q,i=%d,s=1,v=1,f=32,t=s;%s\x1b\\\x1b[6n", kittyIDBase, base64.StdEncoding.EncodeToString([]byte(name)))
	var reply []byte
	buf := make([]byte, 256)
	deadline := time.Now().Add(time.Second)
	for !bytes.HasSuffix(reply, []byte("R")) {
		// kqueue does not take /dev/tty on darwin, so the file has no read deadline.
		n, err := unix.Poll([]unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}, int(time.Until(deadline).Milliseconds()))
		if err == unix.EINTR {
			continue
		}
		if err != nil || n == 0 {
			break
		}
		if n, err = unix.Read(fd, buf); err != nil || n == 0 {
			break
		}
		reply = append(reply, buf[:n]...)
	}
	return bytes.Contains(reply, fmt.Appendf(nil, "\x1b_Gi=%d;OK\x1b\\", kittyIDBase))
}
