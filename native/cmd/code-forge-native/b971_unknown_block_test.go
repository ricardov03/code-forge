package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B971: `review-file --wait-block <id>` for a block the run does not have,
// or for a run this repository has no record of, says so: the refusal names
// the id and the command that fixes it, instead of "the block scope cannot
// be read".
func TestB971UnknownBlockNamesTheFix(t *testing.T) {
	for _, tc := range []struct {
		name, record, reason string
		says                 []string
	}{
		{"unknown block", `{"run_id":"r-test","blocks":{"B1":{"owned_files":["a.go"]}}}`, "unknown-block",
			[]string{"block B9 is not a block of run r-test", "code-forge block open B9 --run r-test", "code-forge run status --run r-test"}},
		{"no blocks", `{"run_id":"r-test","blocks":{}}`, "unknown-block",
			[]string{"block B9 is not a block of run r-test", "code-forge block open B9 --run r-test"}},
		{"no run record", "", "unknown-run",
			[]string{"run r-test has no record in this repository", "code-forge run start --cwd . --run r-test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			repo := reviewFileRepo(t, home)
			if tc.record != "" {
				dir := filepath.Join(repo, ".code-forge", "runs")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "r-test.json"), []byte(tc.record), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got := reviewFileExec(t, repo, home, false, "--wait-block", "B9", "--run", "r-test", "--max=0ms")
			all := got.stdout + got.stderr
			if got.code != 1 || !strings.Contains(got.stdout, `{"status":"refused","reason":"`+tc.reason+`"`) || strings.Contains(all, "cannot be read") {
				t.Fatalf("got %+v, want %s with exit 1", got, tc.reason)
			}
			for _, want := range tc.says {
				if !strings.Contains(got.stdout, want) {
					t.Fatalf("%q missing from %+v", want, got)
				}
			}
		})
	}
}
