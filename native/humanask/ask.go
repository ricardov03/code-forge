// Package humanask implements the clack yes/no terminal confirmation used by
// block commands. Input and output are injectable; production always checks
// both descriptors and restores the terminal before returning.
package humanask

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

type Result int

const (
	NoTTY Result = iota
	Confirmed
	Cancelled
)

func ioctl(fd uintptr, request uintptr, value unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(value))
	if errno != 0 {
		return errno
	}
	return nil
}

func IsTTY(file *os.File) bool {
	var term syscall.Termios
	return ioctl(file.Fd(), getTermios, unsafe.Pointer(&term)) == nil
}

func Available() bool { return IsTTY(os.Stdin) && IsTTY(os.Stdout) }

// Confirm never accepts an environment variable as an answer or a TTY override.
func Confirm(message string) (Result, error) {
	if !Available() {
		return NoTTY, nil
	}
	var saved syscall.Termios
	if err := ioctl(os.Stdin.Fd(), getTermios, unsafe.Pointer(&saved)); err != nil {
		return NoTTY, err
	}
	raw := saved
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Cflag |= syscall.CS8
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cc[syscall.VMIN], raw.Cc[syscall.VTIME] = 1, 0
	if err := ioctl(os.Stdin.Fd(), setTermios, unsafe.Pointer(&raw)); err != nil {
		return NoTTY, err
	}
	defer ioctl(os.Stdin.Fd(), setTermios, unsafe.Pointer(&saved))
	var size [4]uint16
	_ = ioctl(os.Stdout.Fd(), syscall.TIOCGWINSZ, unsafe.Pointer(&size))
	width := int(size[1])
	if width == 0 {
		width = 80
	}
	color := os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	if v, ok := os.LookupEnv("FORCE_COLOR"); ok {
		color = v != "0"
	}
	return Ask(os.Stdin, os.Stdout, message, Options{Width: width, Color: color, ASCII: os.Getenv("TERM") == "linux"})
}

type Options struct {
	Width        int
	Color, ASCII bool
}

// Ask starts with No selected. y/n submit immediately; Enter submits the
// selected value; arrows, Space and vim navigation toggle it. Escape and
// Ctrl-C cancel. An input failure fails closed and restores the cursor.
func Ask(input io.Reader, output io.Writer, message string, options Options) (Result, error) {
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
	selected := false
	frame := func(state string) string {
		symbol, color := active, "36"
		if state == "submit" {
			symbol, color = submit, "32"
		}
		if state == "cancel" {
			symbol, color = cancel, "31"
		}
		// Clack's wrapTextWithPrefix wraps at the terminal width minus the
		// JS length of the styled guide prefix (gray bar + two spaces), counted
		// with its color codes.
		width := options.Width - len([]rune(gray)) - 2
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
				lines[i] = gray + "  " + lines[i]
			}
		}
		prefix := gray + "\n" + strings.Join(lines, "\n") + "\n"
		label := "No"
		if selected {
			label = "Yes"
		}
		if state == "submit" {
			return prefix + gray + "  " + style("2", label)
		}
		if state == "cancel" {
			return prefix + gray + "  " + style("9;2", label) + "\n" + gray
		}
		yes, no := style("2", off)+" "+style("2", "Yes"), style("32", on)+" No"
		if selected {
			yes, no = style("32", on)+" Yes", style("2", off)+" "+style("2", "No")
		}
		return prefix + style("36", bar) + "  " + yes + " " + style("2", "/") + " " + no + "\n" + style("36", bottom) + "\n"
	}
	previous := frame("active")
	if _, err := fmt.Fprint(output, "\x1b[?25l", previous); err != nil {
		return Cancelled, err
	}
	showCursor := true
	defer func() {
		if showCursor {
			fmt.Fprint(output, "\x1b[?25h")
		}
	}()
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
	r := bufio.NewReader(input)
	for {
		b, err := r.ReadByte()
		if err != nil {
			render(frame("cancel"))
			fmt.Fprintln(output)
			return Cancelled, err
		}
		switch b {
		case 'y', 'Y', 'n', 'N':
			selected = b == 'y' || b == 'Y'
			// ConfirmPrompt submits letter answers before its final render.
			fmt.Fprint(output, "\x1b[1A\n\x1b[?25h")
			showCursor = false
			render(frame("submit"))
			fmt.Fprintln(output)
			if selected {
				return Confirmed, nil
			}
			return Cancelled, nil
		case '\r', '\n':
			render(frame("submit"))
			fmt.Fprintln(output)
			if selected {
				return Confirmed, nil
			}
			return Cancelled, nil
		case 3, 27:
			if b == 27 && r.Buffered() >= 2 {
				a, _ := r.Peek(2)
				if a[0] == '[' && strings.ContainsRune("ABCD", rune(a[1])) {
					_, _ = r.Discard(2)
					selected = !selected
					render(frame("active"))
					continue
				}
			}
			if b == 27 {
				selected = !selected
			}
			render(frame("cancel"))
			fmt.Fprintln(output)
			return Cancelled, nil
		case ' ', 'h', 'j', 'k', 'l':
			selected = !selected
			render(frame("active"))
		}
	}
}

// wrapHard is fast-wrap-ansi's exec with {hard: true, trim: false} for text
// of one-column characters: split on single spaces, keep every space, start a
// new row when a word does not fit, and cut words longer than a row.
func wrapHard(text string, columns int) []string {
	rows := []string{""}
	rowLength := 0
	for index, word := range strings.Split(text, " ") {
		if index != 0 {
			if rowLength >= columns {
				rows = append(rows, "")
				rowLength = 0
			}
			rows[len(rows)-1] += " "
			rowLength++
		}
		chars := []rune(word)
		wordLength := len(chars)
		if wordLength > columns {
			remaining := columns - rowLength
			breaksThisLine := 1 + (wordLength-remaining-1)/columns
			breaksNextLine := (wordLength - 1) / columns
			if breaksNextLine < breaksThisLine {
				rows = append(rows, "")
			}
			visible := len([]rune(rows[len(rows)-1]))
			for i, char := range chars {
				if visible+1 <= columns {
					rows[len(rows)-1] += string(char)
				} else {
					rows = append(rows, string(char))
					visible = 0
				}
				visible++
				if visible == columns && i < len(chars)-1 {
					rows = append(rows, "")
					visible = 0
				}
			}
			rowLength = len([]rune(rows[len(rows)-1]))
			continue
		}
		if rowLength+wordLength > columns && rowLength > 0 && wordLength > 0 {
			rows = append(rows, "")
			rowLength = 0
		}
		rows[len(rows)-1] += word
		rowLength += wordLength
	}
	return rows
}
