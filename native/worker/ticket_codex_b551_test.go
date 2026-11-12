package worker

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// B551: testdata/b551_node_codex_review.golden is what Node's createWorker
// (processTicket, real engine and fix loop, default ledger) wrote for the
// B497 fixture with an openai L2 level and review.allow_open_book_codex, the
// fake `codex` below as the only model CLI (it writes its answer to the -o
// file and prints Codex's JSONL events). Normalized like B497.

const b551FakeCodex = `#!/bin/sh
out=""; prev=""
for a in "$@"; do [ "$prev" = "-o" ] && out="$a"; prev="$a"; done
in=$(cat)
heads=$(printf '%s\n' "$in" | grep -E '^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@' | sed -E 's/^(@@ -[0-9,]+ \+[0-9,]+ @@).*/\1/' | awk '!s[$0]++' | sed 's/.*/"&"/' | paste -sd, -)
case "$in" in
*WARNME*) printf '%s' '{"passed":false,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[{"id":"F1","file":"b.txt","line_start":2,"line_end":2,"severity":"warning","category":"style","claim":"a warning","evidence":"the line says WARNME","fix":"remove it","agreement":null}],"resolved":null,"needs_file":null}' > "$out" ;;
*) printf '%s' '{"passed":true,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[],"resolved":null,"needs_file":null}' > "$out" ;;
esac
echo '{"type":"thread.started","thread_id":"t"}'
echo '{"type":"turn.completed","usage":{"input_tokens":1000,"cached_input_tokens":0,"output_tokens":300}}'
`

// TestB551CodexTicketMatchesNodeBytes: a worker ticket whose level is openai
// runs in Go end to end (no hand-over) and writes Node's rows and files.
func TestB551CodexTicketMatchesNodeBytes(t *testing.T) {
	clock := fixedNow
	fx, w, stderr := b497Fixture(t, &clock)
	bin := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(bin, []byte(b551FakeCodex), 0o755); err != nil {
		t.Fatal(err)
	}
	w.Cfg = b492Cfg(t, `{"levels":{"L2":{"model":"gpt-x","provider":"openai"}},"review":{"allow_open_book_codex":true}}`)
	w.SessionDeps.Bins = map[string]string{"codex": bin}
	ids := []string{
		fx.ticket(t, "r1", "B1", "a.txt", sha("hello\nworld\n")),
		fx.ticket(t, "r1", "B2", "b.txt", sha("one\nWARNME\n")),
	}
	for _, id := range ids {
		done, poison, err := w.RunTicket(id)
		if err != nil || done != 1 || poison {
			t.Fatalf("RunTicket(%s) = %d, %v, %v (an error here is the hand-over)", id, done, poison, err)
		}
		clock = clock.Add(time.Second)
	}
	got := b497Normalize(b497Files(t, fx, ids))
	want := b497Normalize(readFile(t, filepath.Join("testdata", "b551_node_codex_review.golden")))
	b497SameLines(t, got, want)
	line := "level=L2 provider=openai model=gpt-x effort=none fallback_step=0\n"
	if stderr.String() != line+line {
		t.Errorf("stderr = %q, Node prints %q twice", stderr.String(), line)
	}
}

// A codex effort Node's builder throws on still hands the ticket over before
// anything is written or run.
func TestB551CodexTicketRefusalsStayNarrow(t *testing.T) {
	for _, cfg := range []string{
		`{"levels":{"L2":{"model":"gpt-x","provider":"openai","effort":"turbo"}},"review":{"allow_open_book_codex":true}}`,
	} {
		clock := fixedNow
		fx, w, stderr := b497Fixture(t, &clock)
		w.Cfg = b492Cfg(t, cfg)
		id := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\nworld\n"))
		_, _, err := w.RunTicket(id)
		if !IsUnsupported(err) {
			t.Fatalf("%s: err = %v, want unsupported", cfg, err)
		}
		if _, err := os.Stat(filepath.Join(fx.home, ".code-forge", "ledger", "unit.jsonl")); !os.IsNotExist(err) {
			t.Fatalf("%s: a row was written", cfg)
		}
		if stderr.Len() != 0 {
			t.Fatalf("%s: a session ran: %q", cfg, stderr.String())
		}
	}
}
