package gates

import (
	"syscall"
	"unsafe"
)

// sigSetMask is SIG_SETMASK in <asm-generic/signal-defs.h> on Linux.
const sigSetMask = 2

// sigInheritedIgnored reports whether signal n's action is SIG_IGN. It reads
// the action with rt_sigaction(n, NULL, &old, 8); the kernel's struct
// sigaction starts with the handler and is at most 32 bytes on amd64 and
// arm64.
func sigInheritedIgnored(n int) bool {
	var old [4]uint64
	_, _, errno := syscall.RawSyscall6(syscall.SYS_RT_SIGACTION, uintptr(n), 0, uintptr(unsafe.Pointer(&old[0])), 8, 0, 0)
	return errno == 0 && uintptr(old[0]) == 1 // SIG_IGN
}

// sigClearMask empties the blocked-signal mask of the calling thread; exec
// gives the new program that thread's mask. Linux keeps the mask per thread,
// so the caller must hold the thread (runtime.LockOSThread).
func sigClearMask() error {
	var set uint64
	_, _, errno := syscall.RawSyscall6(syscall.SYS_RT_SIGPROCMASK, sigSetMask, uintptr(unsafe.Pointer(&set)), 0, 8, 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}
