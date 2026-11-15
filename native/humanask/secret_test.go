package humanask

import (
	"io"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

// b678Pipe is a pipe fed with keys; the write end closes after them when
// close is true.
func b678Pipe(t *testing.T, keys string, close bool) (*os.File, *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close(); _ = w.Close() })
	if _, err := w.WriteString(keys); err != nil {
		t.Fatal(err)
	}
	if close {
		_ = w.Close()
	}
	return r, w
}

func TestB678ReadLineKeys(t *testing.T) {
	for _, tc := range []struct {
		name, keys, value string
		close             bool
		want              Result
	}{
		{"enter", "s3cret\r", "s3cret", false, Confirmed},
		{"line feed", "s3cret\n", "s3cret", false, Confirmed},
		{"backspace", "s3cr\x7fet\r", "s3cet", false, Confirmed},
		{"control-h", "ab\bc\r", "ac", false, Confirmed},
		{"utf-8", "clé\r", "clé", false, Confirmed},
		{"other control keys ignored", "a\x01b\r", "ab", false, Confirmed},
		{"empty", "\r", "", false, Confirmed},
		{"ctrl-c", "abc\x03", "", false, Cancelled},
		{"escape", "abc\x1b", "", false, Cancelled},
		{"ctrl-d on empty line", "\x04", "", false, Ended},
		{"ctrl-d after text is ignored", "ab\x04c\r", "abc", false, Confirmed},
		{"input ends", "abc", "", true, Ended},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := b678Pipe(t, tc.keys, tc.close)
			value, res := ReadLine(r)
			if value != tc.value || res != tc.want {
				t.Fatalf("ReadLine(%q) = %q, %v; want %q, %v", tc.keys, value, res, tc.value, tc.want)
			}
		})
	}
}

// TestB678ReadSecretNotATerminal: a pipe is no terminal; nothing is read.
func TestB678ReadSecretNotATerminal(t *testing.T) {
	r, w := b678Pipe(t, "s3cret\r", false)
	called := false
	value, res, err := ReadSecret(r, func(io.Reader) (string, Result) { called = true; return "x", Confirmed })
	if value != "" || res != NoTTY || err != nil || called {
		t.Fatalf("ReadSecret on a pipe = %q, %v, %v (read called %v); want NoTTY", value, res, err, called)
	}
	_ = w.Close()
	rest, _ := io.ReadAll(r)
	if string(rest) != "s3cret\r" {
		t.Fatalf("the input was read: %q left", rest)
	}
}

// b678FakeTerminal makes every descriptor a terminal with echo on and
// records each mode set on it.
type b678FakeTerminal struct {
	mu    sync.Mutex
	saved syscall.Termios
	sets  []syscall.Termios
}

func (f *b678FakeTerminal) install(t *testing.T) {
	f.saved.Lflag = syscall.ECHO | syscall.ICANON | syscall.ISIG
	f.saved.Iflag = syscall.ICRNL
	getSaved, setSaved := termGet, termSet
	termGet = func(uintptr) (syscall.Termios, error) { return f.saved, nil }
	termSet = func(_ uintptr, mode syscall.Termios) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.sets = append(f.sets, mode)
		return nil
	}
	t.Cleanup(func() { termGet, termSet = getSaved, setSaved })
}

func (f *b678FakeTerminal) current() (syscall.Termios, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sets) == 0 {
		return f.saved, 0
	}
	return f.sets[len(f.sets)-1], len(f.sets)
}

func TestB678ReadSecretEchoOffAndRestored(t *testing.T) {
	for _, tc := range []struct {
		name, keys, value string
		want              Result
	}{
		{"submit", "s3cret\r", "s3cret", Confirmed},
		{"ctrl-c", "s3\x03", "", Cancelled},
		{"input ends", "\x04", "", Ended},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := &b678FakeTerminal{}
			term.install(t)
			r, _ := b678Pipe(t, tc.keys, false)
			echoDuringRead := true
			value, res, err := ReadSecret(r, func(in io.Reader) (string, Result) {
				mode, _ := term.current()
				echoDuringRead = mode.Lflag&syscall.ECHO != 0 || mode.Lflag&syscall.ICANON != 0 || mode.Lflag&syscall.ISIG != 0
				return ReadLine(in)
			})
			if value != tc.value || res != tc.want || err != nil {
				t.Fatalf("ReadSecret = %q, %v, %v; want %q, %v", value, res, err, tc.value, tc.want)
			}
			if echoDuringRead {
				t.Fatal("echo, line mode or signal keys stayed on while the secret was read")
			}
			if mode, n := term.current(); n != 2 || mode != term.saved {
				t.Fatalf("terminal not restored after the read: %d sets, last %+v", n, mode)
			}
		})
	}
}

func TestB678ReadSecretRestoresOnPanic(t *testing.T) {
	term := &b678FakeTerminal{}
	term.install(t)
	r, _ := b678Pipe(t, "", false)
	func() {
		defer func() { _ = recover() }()
		_, _, _ = ReadSecret(r, func(io.Reader) (string, Result) { panic("boom") })
	}()
	if mode, n := term.current(); n != 2 || mode != term.saved {
		t.Fatalf("terminal not restored after a panic: %d sets, last %+v", n, mode)
	}
}

// TestB678ReadSecretRestoresOnSignal: a signal sent while the prompt waits
// restores the terminal first, then ends the process (here recorded).
func TestB678ReadSecretRestoresOnSignal(t *testing.T) {
	term := &b678FakeTerminal{}
	term.install(t)
	got := make(chan os.Signal, 1)
	restoredFirst := make(chan bool, 1)
	savedResignal := resignal
	resignal = func(sig os.Signal) {
		mode, _ := term.current()
		restoredFirst <- mode == term.saved
		got <- sig
	}
	t.Cleanup(func() { resignal = savedResignal })
	r, w := b678Pipe(t, "", false)
	reading := make(chan struct{})
	result := make(chan Result, 1)
	go func() {
		_, res, _ := ReadSecret(r, func(in io.Reader) (string, Result) {
			close(reading)
			return ReadLine(in)
		})
		result <- res
	}()
	<-reading
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case sig := <-got:
		if sig != syscall.SIGHUP || !<-restoredFirst {
			t.Fatalf("signal %v: terminal restored before it went on: false", sig)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the signal was not handled")
	}
	_, _ = w.WriteString("\r")
	if res := <-result; res != Confirmed {
		t.Fatalf("read after the signal = %v", res)
	}
	if mode, _ := term.current(); mode != term.saved {
		t.Fatalf("terminal not restored at the end: %+v", mode)
	}
}
