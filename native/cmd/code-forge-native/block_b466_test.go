package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B466: block close, open, claim, stop and land run in Go the cases B451
// left on the Node hand-over.

func b466Close(f *closeReviewFixture, transcript string) (int, string, string, handoverReason) {
	var out, errs bytes.Buffer
	var reason handoverReason
	code := blockCloseStepWithReason([]string{"B1", "--run", "r-close-fixture", "--report", f.report, "--transcript", transcript}, &landStep{stdout: &out, stderr: &errs}, &reason)
	return code, out.String(), errs.String(), reason
}

// close-transcript-read: an unreadable --transcript is Node's readFile error,
// exit 1, and no ledger row (checked against Node in a scratch probe).
func TestB466CloseTranscriptReadErrorIsNative(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(home string) string
		want func(home string) string
	}{
		{"directory", func(h string) string { d := filepath.Join(h, "tdir"); _ = os.MkdirAll(d, 0700); return d },
			func(string) string { return "block close: EISDIR: illegal operation on a directory, read\n" }},
		{"missing", func(h string) string { return filepath.Join(h, "nope.txt") },
			func(h string) string {
				return "block close: ENOENT: no such file or directory, open '" + filepath.Join(h, "nope.txt") + "'\n"
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCloseReviewFixture(t, 1)
			ledgerFile := filepath.Join(f.home, ".code-forge/ledger/fixture.jsonl")
			before, _ := os.ReadFile(ledgerFile)
			code, out, errs, reason := b466Close(f, tc.path(f.home))
			if reason != 0 {
				t.Fatalf("handed over to Node (reason %d); want Go", reason)
			}
			if code != 1 || out != "" || errs != tc.want(f.home) {
				t.Fatalf("exit=%d stdout=%q stderr=%q; want exit 1 and %q", code, out, errs, tc.want(f.home))
			}
			if after, _ := os.ReadFile(ledgerFile); !bytes.Equal(before, after) {
				t.Fatalf("ledger changed:\n%s", after)
			}
		})
	}
}

// close-transcript-format: a Codex exec --json log and a reviews-dir write are
// scanned in Go; the close refuses with Node's rule_break lines.
func TestB466CloseCodexTranscriptIsNative(t *testing.T) {
	f := newCloseReviewFixture(t, 1)
	codex := `{"type":"thread.started","thread_id":"t"}` + "\n" +
		`{"type":"item.completed","item":{"type":"command_execution","command":"bash -lc 'git stash'","aggregated_output":"","exit_code":0}}` + "\n" +
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"` + ".code-" + "forge/reviews" + `/a.json","kind":"add"}]}}` + "\n"
	closeFixtureWrite(t, f.transcript, []byte(codex))
	done := f.reviewer("approved")
	code, out, errs, reason := b466Close(f, f.transcript)
	if err := <-done; err != nil {
		t.Error(err)
	}
	if reason != 0 {
		t.Fatalf("handed over to Node (reason %d); want Go", reason)
	}
	fix := "  fix: code-forge block close B1 --run r-close-fixture --decide --reason '<reason>' --report " + f.report + " --transcript " + f.transcript + "\n"
	for _, want := range []string{"rule_break - transcript line 2: git-stash\n" + fix, "rule_break - transcript line 3: code-forge-reviews-write\n" + fix} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if code != 1 || !strings.HasSuffix(errs, "\nblock B1 open: rule_break transcript line 2: git-stash; rule_break transcript line 3: code-forge-reviews-write\n") {
		t.Fatalf("exit=%d stderr=%q; want exit 1 and the open summary with both rule breaks", code, errs)
	}
}
