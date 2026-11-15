package humanask

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
)

// B677: testdata/b677-select.jsonl holds @clack/prompts 1.8.1 select's own
// bytes for each key sequence (fake TTY streams, NO_COLOR=1 or FORCE_COLOR=1,
// the given columns), and the value it resolved to ("CANCEL" when cancelled).
func TestB677SelectMatchesClack(t *testing.T) {
	file, err := os.Open("testdata/b677-select.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	count := 0
	for scanner.Scan() {
		var tc struct {
			Message string   `json:"message"`
			Options []string `json:"options"`
			Initial int      `json:"initial"`
			Keys    []string `json:"keys"`
			Cols    int      `json:"cols"`
			Color   bool     `json:"color"`
			Value   string   `json:"value"`
			Out     string   `json:"out"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &tc); err != nil {
			t.Fatal(err)
		}
		count++
		var out bytes.Buffer
		index, result, err := AskSelect(strings.NewReader(strings.Join(tc.Keys, "")), &out, tc.Message, tc.Options, tc.Initial, Options{Width: tc.Cols, Color: tc.Color})
		got := "CANCEL"
		if result == Confirmed {
			got = strconv.Itoa(index)
		}
		if err != nil || got != tc.Value || out.String() != tc.Out {
			t.Errorf("case %d keys %q: value %s err %v frame\n%q\nwant %s\n%q", count, tc.Keys, got, err, out.String(), tc.Value, tc.Out)
		}
	}
	if count != 8 {
		t.Fatalf("golden cases = %d, want 8", count)
	}
}

func TestB677SelectInputFailureCancels(t *testing.T) {
	var out bytes.Buffer
	_, result, err := AskSelect(strings.NewReader("j"), &out, "Pick?", []string{"A", "B"}, 0, Options{Width: 80})
	if result != Cancelled || !errors.Is(err, io.EOF) || !strings.HasSuffix(out.String(), "■  Pick?\n│  B\n│\n\x1b[?25h") {
		t.Fatalf("EOF must cancel on the current option and restore the cursor: %v %v %q", result, err, out.String())
	}
}
