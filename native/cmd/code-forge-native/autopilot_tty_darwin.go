package main

import (
	"syscall"
	"unsafe"
)

// stdinIsTerminal is isatty(0), Node's process.stdin.isTTY.
func stdinIsTerminal() bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, 0, syscall.TIOCGETA, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}
