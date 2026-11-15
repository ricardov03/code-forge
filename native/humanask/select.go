package humanask

// B677: clack's select (@clack/prompts select over @clack/core SelectPrompt)
// for the few-option, short-label questions `logs report` asks. Input and
// output are injectable like Ask; Select draws on the real terminal.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// Size is the terminal's columns and rows on stdout (0, 0 when unknown).
func Size() (int, int) {
	var size [4]uint16
	if ioctl(os.Stdout.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&size)) != nil {
		return 0, 0
	}
	return int(size[1]), int(size[0])
}

// Select asks on the terminal; the index is the option on submit. It never
// takes an environment variable as an answer or a TTY override.
func Select(message string, labels []string, initial int) (int, Result, error) {
	if !Available() {
		return initial, NoTTY, nil
	}
	var saved syscall.Termios
	if err := ioctl(os.Stdin.Fd(), getTermios, unsafe.Pointer(&saved)); err != nil {
		return initial, NoTTY, err
	}
	raw := saved
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cc[syscall.VMIN], raw.Cc[syscall.VTIME] = 1, 0
	if err := ioctl(os.Stdin.Fd(), setTermios, unsafe.Pointer(&raw)); err != nil {
		return initial, NoTTY, err
	}
	defer ioctl(os.Stdin.Fd(), setTermios, unsafe.Pointer(&saved))
	width, _ := Size()
	if width == 0 {
		width = 80
	}
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	if v, ok := os.LookupEnv("FORCE_COLOR"); ok {
		color = v != "0"
	}
	return AskSelect(os.Stdin, os.Stdout, message, labels, initial, Options{Width: width, Color: color, ASCII: os.Getenv("TERM") == "linux"})
}

// AskSelect starts on initial. Up/left/k/h and down/right/j/l move the
// cursor (wrapping); Return submits; Escape and Ctrl-C cancel. A line feed,
// Space, y and n change nothing (clack submits on "return" only). An input
// failure cancels and restores the cursor. Every option must fit one row.
func AskSelect(input io.Reader, output io.Writer, message string, labels []string, initial int, options Options) (int, Result, error) {
	style := func(code, text string) string {
		if !options.Color {
			return text
		}
		end := "39"
		if code == "2" {
			end = "22"
		}
		if code == "9;2" {
			return "\x1b[9m\x1b[2m" + text + "\x1b[22m\x1b[29m"
		}
		return "\x1b[" + code + "m" + text + "\x1b[" + end + "m"
	}
	bar, active, submit, cancel, bottom, on, off := "│", "◆", "◇", "■", "└", "●", "○"
	if options.ASCII {
		bar, active, submit, cancel, bottom, on, off = "|", "*", "o", "x", "—", ">", " "
	}
	gray := style("90", bar)
	cursor := initial
	if cursor < 0 || cursor >= len(labels) {
		cursor = 0
	}
	frame := func(state string) string {
		symbol, color := active, "36"
		if state == "submit" {
			symbol, color = submit, "32"
		}
		if state == "cancel" {
			symbol, color = cancel, "31"
		}
		// wrapTextWithPrefix: the columns minus the JS length of the styled
		// state bar and two spaces; later rows carry that state bar.
		guide := style(color, bar)
		width := options.Width - len([]rune(guide)) - 2
		if width < 1 {
			width = 77
		}
		lines := []string{}
		for _, part := range strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n") {
			lines = append(lines, wrapHard(part, width)...)
		}
		for i := range lines {
			if i == 0 {
				lines[i] = style(color, symbol) + "  " + lines[i]
			} else {
				lines[i] = guide + "  " + lines[i]
			}
		}
		head := gray + "\n" + strings.Join(lines, "\n") + "\n"
		switch state {
		case "submit":
			return head + gray + "  " + style("2", labels[cursor])
		case "cancel":
			return head + gray + "  " + style("9;2", labels[cursor]) + "\n" + gray
		}
		prefix := style("36", bar) + "  "
		rows := make([]string, len(labels))
		for i, label := range labels {
			if i == cursor {
				rows[i] = style("32", on) + " " + label
			} else {
				rows[i] = style("2", off) + " " + style("2", label)
			}
		}
		footer := prefix + style("2", "↑/↓") + " to navigate • " + style("2", "Enter:") + " confirm\n" + style("36", bottom)
		return head + prefix + strings.Join(rows, "\n"+prefix) + "\n" + footer + "\n"
	}
	previous := frame("active")
	if _, err := fmt.Fprint(output, "\x1b[?25l", previous); err != nil {
		return cursor, Cancelled, err
	}
	defer fmt.Fprint(output, "\x1b[?25h")
	render := func(next string) {
		if next == previous {
			return
		}
		oldLines, newLines := strings.Split(previous, "\n"), strings.Split(next, "\n")
		fmt.Fprintf(output, "\x1b[999D\x1b[%dA", len(oldLines)-1)
		changes := []int{}
		for i := 0; i < max(len(oldLines), len(newLines)); i++ {
			if i >= len(oldLines) || i >= len(newLines) || oldLines[i] != newLines[i] {
				changes = append(changes, i)
			}
		}
		first := changes[0]
		if first > 0 {
			fmt.Fprintf(output, "\x1b[%dB", first)
		}
		if len(changes) == 1 {
			fmt.Fprint(output, "\x1b[2K\x1b[G", newLines[first])
			if n := len(newLines) - first - 1; n > 0 {
				fmt.Fprintf(output, "\x1b[%dB", n)
			}
		} else {
			fmt.Fprint(output, "\x1b[J", strings.Join(newLines[first:], "\n"))
		}
		previous = next
	}
	move := func(step int) {
		cursor = (cursor + step + len(labels)) % len(labels)
		render(frame("active"))
	}
	r := bufio.NewReader(input)
	for {
		b, err := r.ReadByte()
		if err != nil {
			render(frame("cancel"))
			fmt.Fprintln(output)
			return cursor, Cancelled, err
		}
		switch b {
		case '\r':
			render(frame("submit"))
			fmt.Fprintln(output)
			return cursor, Confirmed, nil
		case 3, 27:
			if b == 27 && r.Buffered() >= 2 {
				a, _ := r.Peek(2)
				if (a[0] == '[' || a[0] == 'O') && strings.ContainsRune("ABCD", rune(a[1])) {
					_, _ = r.Discard(2)
					if a[1] == 'A' || a[1] == 'D' {
						move(-1)
					} else {
						move(1)
					}
					continue
				}
			}
			render(frame("cancel"))
			fmt.Fprintln(output)
			return cursor, Cancelled, nil
		case 'k', 'h':
			move(-1)
		case 'j', 'l':
			move(1)
		}
	}
}
