package main

import (
	"bytes"
	"os"
	"syscall"
	"unsafe"
)

// b703OpenPTY opens a pseudo-terminal pair on macOS (posix_openpt,
// grantpt, unlockpt and ptsname as ioctls).
func b703OpenPTY() (master, slave *os.File, err error) {
	fd, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	master = os.NewFile(uintptr(fd), "/dev/ptmx")
	fail := func(errno syscall.Errno) (*os.File, *os.File, error) {
		_ = master.Close()
		return nil, nil, errno
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCPTYGRANT, 0); errno != 0 {
		return fail(errno)
	}
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCPTYUNLK, 0); errno != 0 {
		return fail(errno)
	}
	name := make([]byte, 128)
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))); errno != 0 {
		return fail(errno)
	}
	if i := bytes.IndexByte(name, 0); i >= 0 {
		name = name[:i]
	}
	slave, err = os.OpenFile(string(name), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		_ = master.Close()
		return nil, nil, err
	}
	return master, slave, nil
}
