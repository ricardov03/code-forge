package humanask

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, keys string
		want       Result
	}{
		{"yes", "y", Confirmed}, {"uppercase", "Y", Confirmed}, {"no", "n", Cancelled},
		{"default", "\r", Cancelled}, {"tab keeps default", "\t\r", Cancelled},
		{"space", " \r", Confirmed}, {"right", "\x1b[C\r", Confirmed},
		{"vim", "l\r", Confirmed}, {"interrupt", "\x03", Cancelled}, {"escape", "\x1b", Cancelled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			got, err := Ask(strings.NewReader(tc.keys), &out, "Question?", Options{Width: 240})
			if err != nil || got != tc.want {
				t.Fatalf("answer %q = %v, %v; want %v", tc.keys, got, err, tc.want)
			}
			if strings.Count(out.String(), "\x1b[?25l") != 1 || strings.Count(out.String(), "\x1b[?25h") != 1 {
				t.Fatalf("one cursor hide and restore required: %q", out.String())
			}
		})
	}
}

func TestLetterFrame(t *testing.T) {
	var out bytes.Buffer
	got, err := Ask(strings.NewReader("y"), &out, "Question?", Options{Width: 240})
	want := "\x1b[?25l│\n◆  Question?\n│  ○ Yes / ● No\n└\n\x1b[1A\n\x1b[?25h\x1b[999D\x1b[4A\x1b[1B\x1b[J◇  Question?\n│  Yes\n"
	if err != nil || got != Confirmed || out.String() != want {
		t.Fatalf("exact clack yes frame = %q, %v, %v; want %q", out.String(), got, err, want)
	}
}

func TestInputFailureCancels(t *testing.T) {
	var out bytes.Buffer
	got, err := Ask(strings.NewReader(""), &out, "Question?", Options{Width: 240})
	if got != Cancelled || !errors.Is(err, io.EOF) || !strings.HasSuffix(out.String(), "\x1b[?25h") {
		t.Fatalf("EOF must cancel and restore cursor: %v %v %q", got, err, out.String())
	}
}
