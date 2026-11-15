package gates

import (
	"syscall"
	"unsafe"
)

// sigSetMask is SIG_SETMASK in <sys/signal.h> on macOS.
const sigSetMask = 3

// sigInheritedIgnored reports whether signal n's action is SIG_IGN. It reads
// the action with sigaction(n, NULL, &old); old is a struct sigaction
// (handler, sa_mask, sa_flags: 16 bytes), so 32 bytes leave room.
func sigInheritedIgnored(n int) bool {
	var old [4]uint64
	_, _, errno := syscall.RawSyscall(syscall.SYS_SIGACTION, uintptr(n), 0, uintptr(unsafe.Pointer(&old[0])))
	return errno == 0 && uintptr(old[0]) == 1 // SIG_IGN
}

// sigClearMask empties the blocked-signal mask of the calling thread; exec
// gives the new program that thread's mask. macOS keeps the mask per thread,
// so the caller must hold the thread (runtime.LockOSThread).
func sigClearMask() error {
	var set uint32
	_, _, errno := syscall.RawSyscall(syscall.SYS___PTHREAD_SIGMASK, sigSetMask, uintptr(unsafe.Pointer(&set)), 0)
	if errno != 0 {
		return errno
	}
	return nil
}
