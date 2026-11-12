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

// Expected rows come from fast-wrap-ansi wrapAnsi(text, columns, {hard: true,
// trim: false}), the call clack makes for a confirm message (B490).
func TestWrapMatchesClack(t *testing.T) {
	for _, tc := range []struct {
		text    string
		columns int
		want    []string
	}{
		{"Approve review.max_rounds_per_file = {\"b\":1,\"a\":[true,\"x\"]} for run r-x until 2026-11-12T05:00:00.000Z? The old value comes back then.", 67, []string{"Approve review.max_rounds_per_file = {\"b\":1,\"a\":[true,\"x\"]} for run", " r-x until 2026-11-12T05:00:00.000Z? The old value comes back then."}},
		{"Approve budget.coding = \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\" for run r-1 until 2026-11-12T05:00:00.000Z? The old value comes back then.", 67, []string{"Approve budget.coding = \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\" for run r-1 until", " 2026-11-12T05:00:00.000Z? The old value comes back then."}},
		{"a  b   c    xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx  end", 67, []string{"a  b   c    xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", "xxxxxxxxxxxxxxx  end"}},
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 67, []string{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}},
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 67, []string{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", "x"}},
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy", 67, []string{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx ", "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy", "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy", "yyyyyy"}},
		{"ends with space ", 67, []string{"ends with space "}},
		{"", 67, []string{""}},
		{"Approve review.max_rounds_per_file = {\"b\":1,\"a\":[true,\"x\"]} for run r-x until 2026-11-12T05:00:00.000Z? The old value comes back then.", 77, []string{"Approve review.max_rounds_per_file = {\"b\":1,\"a\":[true,\"x\"]} for run r-x until", " 2026-11-12T05:00:00.000Z? The old value comes back then."}},
		{"Approve budget.coding = \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\" for run r-1 until 2026-11-12T05:00:00.000Z? The old value comes back then.", 77, []string{"Approve budget.coding = \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "aaaaaaaaaaaaaaaaaaaaaaaaaaaa\" for run r-1 until 2026-11-12T05:00:00.000Z? The", " old value comes back then."}},
		{"a  b   c    xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx  end", 77, []string{"a  b   c    ", "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx  end"}},
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 77, []string{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}},
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 77, []string{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}},
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy", 77, []string{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx ", "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy", "yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy"}},
		{"ends with space ", 77, []string{"ends with space "}},
		{"", 77, []string{""}},
		{"Approve review.max_rounds_per_file = {\"b\":1,\"a\":[true,\"x\"]} for run r-x until 2026-11-12T05:00:00.000Z? The old value comes back then.", 10, []string{"Approve ", "review.max", "_rounds_pe", "r_file = ", "{\"b\":1,\"a\"", ":[true,\"x\"", "]} for run", " r-x until", " 2026-11-1", "2T05:00:00", ".000Z? The", " old value", " comes ", "back then."}},
		{"Approve budget.coding = \"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\" for run r-1 until 2026-11-12T05:00:00.000Z? The old value comes back then.", 10, []string{"Approve ", "budget.cod", "ing = ", "\"aaaaaaaaa", "aaaaaaaaaa", "aaaaaaaaaa", "aaaaaaaaaa", "aaaaaaaaaa", "aaaaaaaaaa", "aaaaaaaaaa", "aaaaaaaaaa", "aaaaaaaaaa", "aaaaaaaaaa", "aaaaaaaaaa", "aaaaaaaaaa", "aaaaaaaaaa", "aaaaaaaaaa", "aaaaaaaaaa", "aaaaaaaa\" ", "for run ", "r-1 until ", "2026-11-12", "T05:00:00.", "000Z? The ", "old value ", "comes back", " then."}},
		{"a  b   c    xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx  end", 10, []string{"a  b   c  ", "  ", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "  end"}},
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 10, []string{"xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxx"}},
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx", 10, []string{"xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxx"}},
		{"xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx yyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyyy", 10, []string{"xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxxxxxx", "xxxxxx ", "yyyyyyyyyy", "yyyyyyyyyy", "yyyyyyyyyy", "yyyyyyyyyy", "yyyyyyyyyy", "yyyyyyyyyy", "yyyyyyyyyy", "yyyyyyyyyy", "yyyyyyyyyy", "yyyyyyyyyy", "yyyyyyyyyy", "yyyyyyyyyy", "yyyyyyyyyy", "yyyyyyyyyy"}},
		{"ends with space ", 10, []string{"ends with ", "space "}},
		{"", 10, []string{""}},
	} {
		if got := wrapHard(tc.text, tc.columns); strings.Join(got, "\n") != strings.Join(tc.want, "\n") {
			t.Errorf("wrap(%q, %d) = %q; want %q", tc.text, tc.columns, got, tc.want)
		}
	}
}

// An 80-column terminal with color wraps the first message line at 67 columns
// and keeps the space that starts the next line, as clack does.
func TestWrappedFrameMatchesClack(t *testing.T) {
	message := "Approve review.max_rounds_per_file = {\"b\":1,\"a\":[true,\"x\"]} for run r-x until 2026-11-12T05:00:00.000Z? The old value comes back then."
	for _, tc := range []struct {
		color bool
		want  string
	}{
		{true, "\x1b[36m◆\x1b[39m  Approve review.max_rounds_per_file = {\"b\":1,\"a\":[true,\"x\"]} for run\n\x1b[90m│\x1b[39m   r-x until 2026-11-12T05:00:00.000Z? The old value comes back then.\n"},
		{false, "◆  Approve review.max_rounds_per_file = {\"b\":1,\"a\":[true,\"x\"]} for run r-x until\n│   2026-11-12T05:00:00.000Z? The old value comes back then.\n"},
	} {
		var out bytes.Buffer
		if _, err := Ask(strings.NewReader("n"), &out, message, Options{Width: 80, Color: tc.color}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), tc.want) {
			t.Errorf("color=%v frame = %q; want it to hold %q", tc.color, out.String(), tc.want)
		}
	}
}
