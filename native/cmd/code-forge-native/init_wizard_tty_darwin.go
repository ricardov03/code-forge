package main

import (
	"syscall"
	"unsafe"
)

// initWizRaw puts fd in libuv's raw mode (what setRawMode(true) does) and
// returns the restore function; ok is false when fd is not a terminal.
func initWizRaw(fd uintptr) (restore func(), ok bool) {
	var old syscall.Termios
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGETA, uintptr(unsafe.Pointer(&old))); errno != 0 {
		return nil, false
	}
	raw := old
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Oflag |= syscall.ONLCR
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCSETA, uintptr(unsafe.Pointer(&raw))); errno != 0 {
		return nil, false
	}
	return func() {
		_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCSETA, uintptr(unsafe.Pointer(&old)))
	}, true
}

// initWizSize is the terminal size of fd (columns, rows); ok is false when
// fd is not a terminal.
func initWizSize(fd uintptr) (cols, rows int, ok bool) {
	var ws struct{ row, col, x, y uint16 }
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))); errno != 0 {
		return 0, 0, false
	}
	return int(ws.col), int(ws.row), true
}
