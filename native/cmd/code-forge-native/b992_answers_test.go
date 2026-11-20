package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B992: `review-file <fix file> --answers <ids> --of <source>` queues its
// answers ticket in Go after an ordinary findings review. The Go worker writes
// a review.state row with every findings result; the old coder-side pre-check
// (reviewq.MissingAnswers) could not read that row and handed the command to
// the removed Node engine (reason enqueue-answers), so --answers never worked.
// The pre-check now reads the close's own rules over unverified rows, like
// Node's namedOnHash: F1 is open and is queued, an id that is not open is
// still answers-not-open, nothing hands over. (The name is the block's
// acceptance id from block open; a reopen needs a model judge, so the test
// covers the review.state rows that every findings review writes.)
func TestB992AnswersAfterReopen(t *testing.T) {
	bin := b553Binary(t)
	f := b553Fixture(t, "findings")
	t.Cleanup(func() { b553Reap(t, f) })
	env := b553Env(f, "CODE_FORGE_HANDOVER_LOG=1", "CODE_FORGE_NODE="+filepath.Join(f.dir, "missing-node"))
	run := "r-b992"
	step := func(argv ...string) (int, string) {
		t.Helper()
		code, out, errText := b969Exec(t, f.repo, env, append([]string{bin}, argv...)...)
		return code, out + errText
	}
	must := func(argv ...string) string {
		t.Helper()
		code, text := step(argv...)
		if code != 0 {
			t.Fatalf("%v = %d\n%s", argv, code, text)
		}
		return text
	}
	must("run", "start", "--cwd", ".", "--run", run)
	if err := os.WriteFile(filepath.Join(f.repo, ".code-forge", "b992-acceptance.yml"), []byte("- clause: answers\n  kind: human_check\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	must("block", "open", "B1", "--run", run, "--level", "L2", "--acceptance", ".code-forge/b992-acceptance.yml", "--owned", "src/a.txt", "src/b.txt")
	must("review-file", "src/a.txt", "--block", "B1", "--run", run)
	_, joined := step("review-file", "--wait-block", "B1", "--run", run, "--max", "90s")
	t.Logf("join: %s", joined)
	answers := func(id string) (int, string) {
		return step("review-file", "src/b.txt", "--block", "B1", "--run", run, "--answers", id, "--of", "src/a.txt")
	}
	code, text := answers("F1")
	if code != 0 || !strings.Contains(text, `"kind":"answers"`) || !strings.Contains(text, `"answers":["F1"]`) {
		t.Fatalf("answers F1 = %d, want a queued answers ticket\n%s", code, text)
	}
	code, text = answers("F9")
	if code != 1 || !strings.Contains(text, "answers-not-open") {
		t.Fatalf("answers F9 = %d, want answers-not-open\n%s", code, text)
	}
	if h := b460Handovers(t, f.home); len(h) != 0 {
		t.Fatalf("hand-overs: %v", h)
	}
}
