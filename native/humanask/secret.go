package humanask

import (
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"unicode/utf8"
	"unsafe"
)

// Ended is a hidden read whose input closed before Enter (EOF, or Ctrl-D on
// an empty line).
const Ended Result = Cancelled + 1

// Seams for tests: the terminal mode of a descriptor, the signals that
// restore it, and what happens to such a signal after the restore.
var (
	termGet = func(fd uintptr) (syscall.Termios, error) {
		var t syscall.Termios
		err := ioctl(fd, getTermios, unsafe.Pointer(&t))
		return t, err
	}
	termSet = func(fd uintptr, t syscall.Termios) error {
		return ioctl(fd, setTermios, unsafe.Pointer(&t))
	}
	secretSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT}
	// resignal ends the process the way the signal would have: default
	// action restored, then the signal sent again.
	resignal = func(sig os.Signal) {
		signal.Reset(sig)
		if s, ok := sig.(syscall.Signal); ok {
			_ = syscall.Kill(os.Getpid(), s)
		}
	}
)

// rawMode is libuv's raw mode (Node's setRawMode(true), what clack prompts
// run in): no echo, no line buffering, no signal keys, so Ctrl-C reaches
// the reader as a byte.
func rawMode(t syscall.Termios) syscall.Termios {
	raw := t
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Oflag |= syscall.ONLCR
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cc[syscall.VMIN], raw.Cc[syscall.VTIME] = 1, 0
	return raw
}

// ReadSecret reads one hidden line from the terminal in: echo is off while
// read runs, and the saved terminal mode comes back on every path (a
// return, a panic, and SIGINT/SIGTERM/SIGHUP/SIGQUIT sent to the process,
// which then still ends it). read is the line editor (nil: ReadLine); it
// must never write the typed text. A descriptor that is not a terminal
// gives NoTTY without reading anything.
func ReadSecret(in *os.File, read func(io.Reader) (string, Result)) (string, Result, error) {
	if read == nil {
		read = ReadLine
	}
	fd := in.Fd()
	saved, err := termGet(fd)
	if err != nil {
		return "", NoTTY, nil
	}
	var once sync.Once
	restore := func() { once.Do(func() { _ = termSet(fd, saved) }) }
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	finished := make(chan struct{})
	signal.Notify(signals, secretSignals...)
	go func() {
		defer close(finished)
		select {
		case sig := <-signals:
			restore()
			resignal(sig)
		case <-done:
		}
	}()
	defer func() {
		signal.Stop(signals)
		close(done)
		<-finished
		restore()
	}()
	if err := termSet(fd, rawMode(saved)); err != nil {
		return "", NoTTY, err
	}
	value, res := read(in)
	return value, res, nil
}

// ReadLine is a plain hidden line editor: printable text is kept and never
// written anywhere; Backspace removes the last character; Enter (CR or LF)
// submits; Ctrl-C or Escape cancels; Ctrl-D on an empty line, or the end
// of the input, ends it.
func ReadLine(in io.Reader) (string, Result) {
	line := []rune{}
	buf := make([]byte, 1)
	pending := []byte{}
	for {
		n, err := in.Read(buf)
		if n == 0 {
			if err != nil {
				return "", Ended
			}
			continue
		}
		b := buf[0]
		switch {
		case b == '\r' || b == '\n':
			return string(line), Confirmed
		case b == 0x03 || b == 0x1b:
			return "", Cancelled
		case b == 0x04:
			if len(line) == 0 {
				return "", Ended
			}
		case b == 0x7f || b == 0x08:
			if len(line) > 0 {
				line = line[:len(line)-1]
			}
		case b < 0x20:
			// other control keys do nothing
		default:
			pending = append(pending, b)
			if utf8.FullRune(pending) {
				r, _ := utf8.DecodeRune(pending)
				line = append(line, r)
				pending = pending[:0]
			}
		}
	}
}
