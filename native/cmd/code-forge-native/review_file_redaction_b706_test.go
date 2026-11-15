package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// B706: an unknown review-file flag holding an arbitrary value is served by
// Go (it handed over as enqueue-flag-redaction before): Node's usage text on
// stderr, exit 2, no hand-over row, and an error-log entry whose message,
// flags, cleaned counts and fingerprint are Node buildEntry's (goldens
// recorded from src/util/error-log.mjs buildEntry with the same text).
func TestB706ReviewFileUnknownValueFlagLogsInGo(t *testing.T) {
	cases := []struct{ name, arg, message, cleaned, fp string }{
		{"a path value", "--x=/private/value",
			"review-file: unknown flag \"--x=/private/value\"\n" + b706ReviewUsageTrimmed, `[]`, "3349b37ed0dd"},
		{"an op ref, a no-break space, an email and an item id", "--x=op://v/i/f\u00a0tail\u3000me@example.com_abcdefghijklmnopqrstuvwxyz",
			"review-file: unknown flag \"--x=op://<ref>\u00a0tail\u3000<email>_<item-id>\"\n" + b706ReviewUsageTrimmed, `[{"count":1,"rule":"email"},{"count":1,"rule":"op_ref"},{"count":1,"rule":"item_id"}]`, "3349b37ed0dd"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			repo := reviewFileRepo(t, home)
			got := reviewFileExec(t, repo, home, true, c.arg)
			quoted, _ := json.Marshal(c.arg)
			if want := (reviewFileRun{"", "review-file: unknown flag " + string(quoted) + "\n" + reviewWaitUsage, 2}); got != want {
				t.Fatalf("got %+v\nwant %+v", got, want)
			}
			if _, err := os.Stat(filepath.Join(home, ".code-forge", "native", "handovers.jsonl")); !os.IsNotExist(err) {
				t.Fatalf("a hand-over row was written: %v", err)
			}
			data, err := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var row map[string]any
			if lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'}); len(lines) != 1 || json.Unmarshal(lines[0], &row) != nil {
				t.Fatalf("log %q", data)
			}
			cleaned, _ := json.Marshal(row["cleaned"])
			flags, _ := json.Marshal(row["flags"])
			if row["verb"] != "review-file" || row["sub"] != nil || row["exit"] != float64(2) || row["kind"] != "usage" || row["message"] != c.message || string(flags) != `["--x"]` || string(cleaned) != c.cleaned || row["fp"] != c.fp {
				t.Fatalf("entry %s", data)
			}
		})
	}
}

var b706ReviewUsageTrimmed = jsTrim(reviewWaitUsage)
