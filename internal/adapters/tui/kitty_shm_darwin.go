package tui

import (
	"syscall"
	"unsafe"
)

// x/sys/unix has no shm_open for darwin, where it is a system call of its
// own rather than a file under /dev/shm.
func shmOpen(name string, flag int, perm uint32) (int, error) {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return -1, err
	}
	fd, _, errno := syscall.Syscall(syscall.SYS_SHM_OPEN, uintptr(unsafe.Pointer(p)), uintptr(flag), uintptr(perm))
	if errno != 0 {
		return -1, errno
	}
	return int(fd), nil
}

func shmUnlink(name string) error {
	p, err := syscall.BytePtrFromString(name)
	if err != nil {
		return err
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_SHM_UNLINK, uintptr(unsafe.Pointer(p)), 0, 0); errno != 0 {
		return errno
	}
	return nil
}
