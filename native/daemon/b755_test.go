//go:build unix

package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/ticket"
)

// b755Ticket writes one review ticket of run/block into s's queue.
func b755Ticket(t *testing.T, s *server, run, block string) {
	t.Helper()
	p := ticket.Parts{Run: run, Block: block, File: "a.go", ContentHash: "hash"}
	id := ticket.ReviewID(p)
	writeObject(t, filepath.Join(s.queue.Dir, id+".json"), object{"ticket": id, "run": p.Run, "block": p.Block, "file": p.File, "content_hash": p.ContentHash})
}

// b755Sweep runs one sweep now (as the daemon does: refresh, then sweep)
// and returns how long the sweep took.
func b755Sweep(t *testing.T, s *server) time.Duration {
	t.Helper()
	if err := s.refresh(); err != nil {
		t.Fatal(err)
	}
	s.lastSweep = time.Time{}
	start := time.Now()
	s.sweep(time.Now())
	return time.Since(start)
}

// TestB755SecondSweepFast: a second sweep over 100k keyed ended runs reuses
// the keys the first sweep read and prunes the same runs (B755).
func TestB755SecondSweepFast(t *testing.T) {
	t.Setenv("CODE_FORGE_PRUNE_GRACE", "10m")
	n := 100000
	if v, err := strconv.Atoi(os.Getenv("B755_N")); err == nil && v > 0 {
		n = v
	}
	f := b749Write(t, n, 1, func(int) bool { return true }, func(i int) bool { return i%1000 == 7 })
	s := f.server()
	// Tickets: an ended keyed run, a forged run and a run with no run.end.
	ended, forged := "r-old-1", "r-old-7"
	if !f.ended[ended] || f.ended[forged] {
		t.Fatalf("fixture: ended=%v forged=%v", f.ended[ended], f.ended[forged])
	}
	for _, run := range []string{ended, forged, "r-none"} {
		b755Ticket(t, s, run, "B1")
	}
	first := b755Sweep(t, s)
	want := map[string]int{ended: 0, forged: 1, "r-none": 1}
	for run, count := range want {
		if got := len(s.queue.Block(run, "B1", nil, nil)); got != count {
			t.Errorf("first sweep: %s tickets = %d, want %d", run, got, count)
		}
	}
	// New tickets for the same runs: the second sweep prunes the same runs.
	b755Ticket(t, s, ended, "B2")
	b755Ticket(t, s, forged, "B2")
	second := b755Sweep(t, s)
	for run, count := range map[string]int{ended: 0, forged: 1} {
		if got := len(s.queue.Block(run, "B2", nil, nil)); got != count {
			t.Errorf("second sweep: %s B2 tickets = %d, want %d", run, got, count)
		}
	}
	t.Logf("rows=%d ended-runs=%d first-sweep=%v second-sweep=%v", n, len(f.ended), first.Round(time.Millisecond), second.Round(time.Millisecond))
	if budget := b755Budget(n); second > budget {
		t.Fatalf("second sweep over %d keyed ended runs took %v, budget %v (first %v)", len(f.ended), second, budget, first)
	}
}

// b755Budget is the second-sweep budget: 3 s per 100k rows, room for a
// loaded machine (B755 measured the numbers in the report).
func b755Budget(n int) time.Duration {
	return time.Duration(n) * 3 * time.Second / 100000
}

// b755Rewrite writes data into file in place (same inode, same size) and
// sets its mtime back, so only its ctime tells that it changed.
func b755Rewrite(t *testing.T, file string, data []byte) {
	t.Helper()
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.OpenFile(file, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(file)
	if err != nil || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
		t.Fatalf("rewrite changed more than the content: %v", err)
	}
}

// TestB755ChangedRunReadAgain: the sweep cache never hides a change. A key
// file rewritten in place (same size, inode and mtime), a key file chmodded
// open, and a run.end row replaced in the ledger are each read again on the
// next sweep, and what the sweep prunes changes with them (B755).
func TestB755ChangedRunReadAgain(t *testing.T) {
	t.Setenv("CODE_FORGE_PRUNE_GRACE", "10m")
	f := b749Write(t, 20, 1, func(int) bool { return true }, func(int) bool { return false })
	s := f.server()
	run := "r-old-1"
	if !f.ended[run] {
		t.Fatalf("fixture: %s is not an ended keyed run", run)
	}
	keyFile := filepath.Join(f.home, ".code-forge", "runs", run+".key")
	key, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	other := make([]byte, 32)
	for i := range other {
		other[i] = byte(200 - i)
	}
	step := 0
	expect := func(pruned bool, what string) {
		t.Helper()
		step++
		block := "S" + strconv.Itoa(step)
		b755Ticket(t, s, run, block)
		b755Sweep(t, s)
		want := 1
		if pruned {
			want = 0
		}
		if got := len(s.queue.Block(run, block, nil, nil)); got != want {
			t.Fatalf("step %d (%s): %s tickets = %d, want %d", step, what, run, got, want)
		}
	}
	expect(true, "first sweep verifies the run")
	expect(true, "second sweep, nothing changed")
	b755Rewrite(t, keyFile, other)
	expect(false, "key rewritten in place with another key")
	b755Rewrite(t, keyFile, key)
	expect(true, "key rewritten back")
	if err := os.Chmod(keyFile, 0644); err != nil {
		t.Fatal(err)
	}
	expect(false, "key open to others")
	if err := os.Chmod(keyFile, 0600); err != nil {
		t.Fatal(err)
	}
	expect(true, "key private again")
	// The run.end row itself changes: the ledger is replaced by one whose
	// run.end row for run carries a forged mac.
	ledgerFile := filepath.Join(f.home, ".code-forge", "ledger", f.project+".jsonl")
	ts := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	signed, err := sign.SignRow(object{"run": run, "block": "B1", "event": "run.end", "ts": ts}, key)
	if err != nil {
		t.Fatal(err)
	}
	signed["mac"] = strings.Repeat("0", 64)
	line, _ := json.Marshal(signed)
	replacement := filepath.Join(filepath.Dir(ledgerFile), "next.tmp")
	if err := os.WriteFile(replacement, append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, ledgerFile); err != nil {
		t.Fatal(err)
	}
	expect(false, "run.end row forged")
	signed, err = sign.SignRow(object{"run": run, "block": "B1", "event": "run.end", "ts": ts}, key)
	if err != nil {
		t.Fatal(err)
	}
	line, _ = json.Marshal(signed)
	if err := os.WriteFile(replacement, append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, ledgerFile); err != nil {
		t.Fatal(err)
	}
	expect(true, "run.end row signed again")
	// A removed key file ends pruning; a new key file starts it again.
	if err := os.Remove(keyFile); err != nil {
		t.Fatal(err)
	}
	expect(false, "key removed")
	if err := os.WriteFile(keyFile, key, 0600); err != nil {
		t.Fatal(err)
	}
	expect(true, "key written again")
}
