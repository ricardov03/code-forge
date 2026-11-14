package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/sign"
)

// B627 (issue #37 C1): when the needs_file round's session times out, the
// ledger gets one signed review.needs_file_timeout row and the next ticket
// for the file starts with the requested files attached, in a session of its
// own (a fresh clock). One retry: a second timeout is today's unavailable.

// b627Claude is the fake `claude` CLI: it logs each call (attached or plain);
// a packet with b.txt attached sleeps past the timeout while $SLOW exists;
// a plain packet asks for b.txt (plus $EXTRA when set), an attached one passes.
func b627Claude(calls, slow string, extra string) string {
	needs := `"needs_file":["b.txt"` + extra + `]`
	return `#!/bin/sh
in=$(cat)
heads=$(printf '%s\n' "$in" | grep -E '^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@' | sed -E 's/^(@@ -[0-9,]+ \+[0-9,]+ @@).*/\1/' | awk '!s[$0]++' | sed 's/.*/"&"/' | paste -sd, -)
case "$in" in
*'## attached files'*'### b.txt'*)
  case "$in" in *'OUTSIDE-SECRET'*) echo leaked >> ` + calls + ` ;; esac
  echo attached >> ` + calls + `
  if [ -f ` + slow + ` ]; then exec sleep 30; fi
  printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"with b","reviewed_hunks":['"$heads"'],"findings":[]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
*)
  echo plain >> ` + calls + `
  printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"asks","reviewed_hunks":['"$heads"'],"findings":[],` + needs + `},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
esac
`
}

type b627Env struct {
	fx           *b488Fix
	w            *TicketRunner
	calls, slow  string
	contentCount int
}

func b627Setup(t *testing.T, extra string) *b627Env {
	t.Helper()
	clock := fixedNow
	fx, w, _ := b497Fixture(t, &clock)
	w.Cfg = b492Cfg(t, `{"levels":{"L2":{"model":"claude-x","provider":"anthropic"}},"review":{"session_timeout_s":3}}`)
	dir := t.TempDir()
	e := &b627Env{fx: fx, w: w, calls: filepath.Join(dir, "calls"), slow: filepath.Join(dir, "slow")}
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(b627Claude(e.calls, e.slow, extra)), 0o755); err != nil {
		t.Fatal(err)
	}
	w.SessionDeps.Bins = map[string]string{"claude": bin}
	return e
}

// next writes new content to a.txt and runs one ticket for it; it returns the
// ticket id, the calls it made and its result file.
func (e *b627Env) next(t *testing.T, slow bool) (string, string, string) {
	t.Helper()
	if slow {
		if err := os.WriteFile(e.slow, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	} else if err := os.Remove(e.slow); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	e.contentCount++
	content := "hello\nworld\n" + strings.Repeat("more\n", e.contentCount-1)
	if err := os.WriteFile(filepath.Join(e.fx.root, "a.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	before := ""
	if data, err := os.ReadFile(e.calls); err == nil {
		before = string(data)
	}
	id := e.fx.ticket(t, "r1", "B1", "a.txt", sha(content))
	n, poison, err := e.w.RunTicket(id)
	if err != nil || n != 1 || poison {
		t.Fatalf("RunTicket = %d, %v, %v; want the ticket recorded in Go", n, poison, err)
	}
	after := readFile(t, e.calls)
	res := readFile(t, filepath.Join(e.fx.root, ".code-forge", "reviews", "r1", id+".json"))
	return id, strings.TrimPrefix(after, before), res
}

// rows is every ledger row of one event, decoded.
func (e *b627Env) rows(t *testing.T, event string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, l := range strings.Split(readFile(t, filepath.Join(e.fx.home, ".code-forge", "ledger", "unit.jsonl")), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		if m["event"] == event {
			out = append(out, m)
		}
	}
	return out
}

func b627Paths(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// TestB627NeedsFileTimeoutRetriesWithFiles: the round times out (unavailable,
// one review.needs_file_timeout row, retry true); the next ticket's FIRST
// session already has b.txt attached and passes (stored as reviewed, the
// carry marked in the review.result row); the ticket after that is plain.
func TestB627NeedsFileTimeoutRetriesWithFiles(t *testing.T) {
	e := b627Setup(t, "")
	id1, calls, res := e.next(t, true)
	if !strings.HasPrefix(calls, "plain\nattached\n") {
		t.Fatalf("ticket 1 calls %q, want a plain session then the needs_file round", calls)
	}
	if !strings.Contains(res, `"status":"unavailable"`) || !strings.Contains(res, `"reason":"timeout"`) {
		t.Fatalf("ticket 1 result %s, want unavailable: timeout", res)
	}
	rows := e.rows(t, "review.needs_file_timeout")
	if len(rows) != 1 {
		t.Fatalf("needs_file_timeout rows %d, want 1", len(rows))
	}
	r := rows[0]
	if r["ticket"] != id1 || r["file"] != "a.txt" || r["block"] != "B1" || r["run"] != "r1" || b627Paths(r["requested_paths"]) != `["b.txt"]` ||
		r["attempt"] != 1.0 || r["retry"] != true || r["mac"] == nil {
		t.Fatalf("row %v", r)
	}
	_, calls, res = e.next(t, false)
	if calls != "attached\n" {
		t.Fatalf("ticket 2 calls %q, want one session with b.txt already attached", calls)
	}
	if !strings.Contains(res, `"status":"reviewed","approved":true`) || !strings.Contains(res, `"needs_file_carried":2`) {
		t.Fatalf("ticket 2 result %s, want the review stored with the carry marked", res)
	}
	results := e.rows(t, "review.result")
	last := results[len(results)-1]
	if !strings.Contains(b627Paths(last["sessions"]), `"needs_file_carried":2`) {
		t.Fatalf("review.result sessions %v lack the carry marker", last["sessions"])
	}
	if len(e.rows(t, "review.needs_file_timeout")) != 1 {
		t.Fatal("a passing retry wrote another needs_file_timeout row")
	}
	_, calls, _ = e.next(t, false)
	if !strings.HasPrefix(calls, "plain\n") {
		t.Fatalf("ticket 3 calls %q: the carry was used twice", calls)
	}
}

// TestB627SecondTimeoutFallsBack: the retry with the files attached times out
// too: a second row (attempt 2, retry false), the ticket is unavailable as
// today, and the next ticket is plain (no loop).
func TestB627SecondTimeoutFallsBack(t *testing.T) {
	e := b627Setup(t, "")
	e.next(t, true)
	id2, calls, res := e.next(t, true)
	if !strings.HasPrefix(calls, "attached\n") {
		t.Fatalf("ticket 2 calls %q, want the files attached first", calls)
	}
	if !strings.Contains(res, `"status":"unavailable"`) || !strings.Contains(res, `"reason":"timeout"`) {
		t.Fatalf("ticket 2 result %s, want unavailable: timeout", res)
	}
	rows := e.rows(t, "review.needs_file_timeout")
	if len(rows) != 2 {
		t.Fatalf("needs_file_timeout rows %d, want 2", len(rows))
	}
	if r := rows[1]; r["ticket"] != id2 || r["attempt"] != 2.0 || r["retry"] != false || b627Paths(r["requested_paths"]) != `["b.txt"]` {
		t.Fatalf("second row %v", r)
	}
	_, calls, _ = e.next(t, false)
	if !strings.HasPrefix(calls, "plain\n") {
		t.Fatalf("ticket 3 calls %q, want today's plain start after a second timeout", calls)
	}
}

// TestB627OutsidePathRefused: a requested path outside the project is kept in
// the row but refused again when the retry attaches the files; its content
// never reaches the session.
func TestB627OutsidePathRefused(t *testing.T) {
	e := b627Setup(t, `,"../outside.txt"`)
	if err := os.WriteFile(filepath.Join(filepath.Dir(e.fx.root), "outside.txt"), []byte("OUTSIDE-SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.next(t, true)
	rows := e.rows(t, "review.needs_file_timeout")
	if len(rows) != 1 || b627Paths(rows[0]["requested_paths"]) != `["b.txt","../outside.txt"]` {
		t.Fatalf("rows %v", rows)
	}
	_, calls, res := e.next(t, false)
	if calls != "attached\n" {
		t.Fatalf("ticket 2 calls %q, want b.txt attached with the outside path refused", calls)
	}
	if !strings.Contains(res, `"status":"reviewed"`) {
		t.Fatalf("result %s", res)
	}
}

// TestB627CarryReadsSignedRowsOnly: the read-back takes only signed rows of
// this run, block and file; a forged, unsigned or other-file row is no carry,
// a retry: false row ends it and a result that used the carry consumes it.
func TestB627CarryReadsSignedRowsOnly(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	signed := func(m map[string]any) map[string]any {
		out, err := sign.SignRow(m, key)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	row := func(file string, attempt float64, retry bool) map[string]any {
		return map[string]any{"run": "r1", "event": "review.needs_file_timeout", "ticket": "t", "requested_paths": []any{"b.txt"},
			"attempt": attempt, "retry": retry, "block": "B1", "file": file}
	}
	used := map[string]any{"run": "r1", "event": "review.result", "block": "B1", "file": "a.txt", "sessions": []any{map[string]any{"needs_file_carried": 2.0}}}
	plainResult := map[string]any{"run": "r1", "event": "review.result", "block": "B1", "file": "a.txt", "sessions": []any{}}
	for _, c := range []struct {
		name string
		rows []any
		want bool
	}{
		{"signed", []any{signed(row("a.txt", 1, true))}, true},
		{"signed-then-plain-result", []any{signed(row("a.txt", 1, true)), signed(plainResult)}, true},
		{"unsigned", []any{row("a.txt", 1, true)}, false},
		{"forged", []any{func() map[string]any {
			r := signed(row("a.txt", 1, true))
			r["requested_paths"] = []any{"/etc/passwd"}
			return r
		}()}, false},
		{"other-file", []any{signed(row("c.txt", 1, true))}, false},
		{"second-timeout", []any{signed(row("a.txt", 1, true)), signed(row("a.txt", 2, false))}, false},
		{"consumed", []any{signed(row("a.txt", 1, true)), signed(used)}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := needsFileCarry(func() ([]any, error) { return c.rows, nil }, key, "r1", "B1", "a.txt")()
			if err != nil {
				t.Fatal(err)
			}
			if (got != nil) != c.want {
				t.Fatalf("carry %v, want %v", got, c.want)
			}
			if got != nil && (got.Attempt != 2 || len(got.Paths) != 1 || got.Paths[0] != "b.txt") {
				t.Fatalf("carry %+v", got)
			}
		})
	}
}
