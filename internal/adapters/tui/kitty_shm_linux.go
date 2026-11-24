package tui

import "golang.org/x/sys/unix"

func shmOpen(name string, flag int, perm uint32) (int, error) {
	return unix.Open("/dev/shm"+name, flag|unix.O_CLOEXEC|unix.O_NOFOLLOW, perm)
}

func shmUnlink(name string) error { return unix.Unlink("/dev/shm" + name) }
