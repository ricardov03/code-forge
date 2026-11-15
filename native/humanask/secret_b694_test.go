package humanask

import (
	"io"
	"sync"
	"syscall"
	"testing"
)

// b694Kernel is the macOS tty line discipline as far as PENDIN goes: line
// mode coming back sets PENDIN, a mode set keeps it, and only a look at the
// input queue (read, FIONREAD) drops it.
type b694Kernel struct {
	mu    sync.Mutex
	state syscall.Termios
}

func (k *b694Kernel) install(t *testing.T) {
	k.state.Lflag = syscall.ECHO | syscall.ICANON | syscall.ISIG
	getSaved, setSaved, pendSaved := termGet, termSet, termPend
	termGet = func(uintptr) (syscall.Termios, error) {
		k.mu.Lock()
		defer k.mu.Unlock()
		return k.state, nil
	}
	termSet = func(_ uintptr, mode syscall.Termios) error {
		k.mu.Lock()
		defer k.mu.Unlock()
		pendin := k.state.Lflag & syscall.PENDIN
		if mode.Lflag&syscall.ICANON != 0 && k.state.Lflag&syscall.ICANON == 0 {
			pendin = syscall.PENDIN
		}
		mode.Lflag |= pendin
		k.state = mode
		return nil
	}
	termPend = func(uintptr) {
		k.mu.Lock()
		defer k.mu.Unlock()
		k.state.Lflag &^= syscall.PENDIN
	}
	t.Cleanup(func() { termGet, termSet, termPend = getSaved, setSaved, pendSaved })
}

// After the hidden read the terminal is back in line mode with no PENDIN
// left on, on a return and on a panic.
func TestB694ReadSecretLeavesNoPendin(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func(io.Reader) (string, Result)
	}{
		{"submit", ReadLine},
		{"panic", func(io.Reader) (string, Result) { panic("b694") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := &b694Kernel{}
			k.install(t)
			r, _ := b678Pipe(t, "s3cret\r", false)
			func() {
				defer func() { _ = recover() }()
				_, _, _ = ReadSecret(r, tc.read)
			}()
			if k.state.Lflag&syscall.ICANON == 0 || k.state.Lflag&syscall.ECHO == 0 {
				t.Fatalf("line mode not back: %#x", k.state.Lflag)
			}
			if k.state.Lflag&syscall.PENDIN != 0 {
				t.Fatalf("pendin left on: %#x", k.state.Lflag)
			}
		})
	}
}
