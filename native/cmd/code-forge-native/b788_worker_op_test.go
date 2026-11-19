package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// B788: a worker whose Jev key is a 1Password op:// reference with no
// unexpired cached key starts in Go. A fake `op` on PATH (never the real op
// or keychain; temp HOME, CODE_FORGE_KEY_BACKEND=file from the fixture)
// either answers, so the key is read, cached and used, or fails, so the
// worker serves without Jev and prints one warning naming the fix.

const b788Config = `{"keys":{"jev":"op://v/i/f"},"levels":{"L2":{"model":"claude-x","provider":"anthropic"}},"native":{"daemon":false},"version":1}`

// b788Op puts a fake op (script body) in the fixture's PATH directory.
func b788Op(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Dir(b533Which(t))
	args := filepath.Join(t.TempDir(), "op-args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + args + "'\n" + body
	if err := os.WriteFile(filepath.Join(bin, "op"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return args
}

func newB788Fix(t *testing.T) *b489Fix {
	t.Helper()
	f := newB489Fix(t)
	base := strings.TrimSpace(b460Git(t, f.root, "rev-parse", "HEAD"))
	f.withConfig(t, base, b788Config)
	return f
}

// op answers: the worker reads the key once, caches it in the file store
// with the reference (as doctor and keys test do), asks Jev with it and
// hands nothing over.
func TestB788WorkerOpKeyReadAndCached(t *testing.T) {
	f := newB788Fix(t)
	j := b533Serve(t, f)
	key := "fake-op-jev-key-0788"
	args := b788Op(t, "printf '%s\\n' '"+key+"'\n")
	id := f.ticket(t, "B1", "a.txt", "hello\nworld\n")
	code, out, errText := b460Call(t, "worker", "--run", "r1", "--once")
	if code != 0 || out != "" || !strings.HasSuffix(errText, "worker: drained 1 ticket(s)\n") || strings.Contains(errText, key) || strings.Contains(errText, "warning") {
		t.Fatalf("worker --once = %d %q %q", code, out, errText)
	}
	j.check(t, f, key, id)
	if got, _ := os.ReadFile(args); string(got) != "read op://v/i/f\n" {
		t.Fatalf("op calls %q", got)
	}
	stored, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "store", "jev.key"))
	if err != nil || !strings.HasPrefix(string(stored), `{"v":"`+key+`","exp":`) {
		t.Fatalf("cached key %q %v", stored, err)
	}
	index, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "store", "index.json"))
	if !strings.Contains(string(index), `"source": "op"`) || !strings.Contains(string(index), `"ref": "op://v/i/f"`) {
		t.Fatalf("index.json %s", index)
	}
	// The next worker uses the cache: op is not asked again.
	_ = f.ticket(t, "B2", "b.txt", "one\ntwo\n")
	if code, _, errText := b460Call(t, "worker", "--run", "r1", "--once"); code != 0 || !strings.HasSuffix(errText, "worker: drained 1 ticket(s)\n") {
		t.Fatalf("second worker = %d %q", code, errText)
	}
	if got, _ := os.ReadFile(args); string(got) != "read op://v/i/f\n" {
		t.Fatalf("op asked again: %q", got)
	}
}

// op fails (signed out, missing, hanging): the worker serves the queue
// without Jev, prints one warning with op's reason and the fix, and hands
// nothing over.
func TestB788WorkerOpFailureStartsWithoutJev(t *testing.T) {
	// B794/B798: only the hanging op gets a short budget. An op that answers
	// keeps the worker's own budget (6 s), so a loaded machine cannot turn
	// its answer into "did not answer in time"; the hang (sleep 120) always
	// outlasts 300 ms, so that case does not depend on load either. B909: an
	// uncapped worker would wait opTimeout (60 s) twice, so the 60 s bound
	// below tells the cap apart with a margin no machine load closes.
	cases := []struct {
		name, body, reason string
		missing            bool
		budget             time.Duration
	}{
		{"signed out", "echo '[ERROR] You are not currently signed in.' >&2\nexit 1\n", opMessages["op_locked"], false, 0},
		{"not found", "echo '[ERROR] \"i\" isn'\"'\"'t an item in the \"v\" vault.' >&2\nexit 1\n", opMessages["op_not_found"], false, 0},
		{"no op on PATH", "", opMessages["op_missing"], true, 0},
		{"op hangs", "sleep 120\n", opMessages["op_timeout"], false, 300 * time.Millisecond},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.budget != 0 {
				oldBudget := workerOpBudget
				workerOpBudget = c.budget
				t.Cleanup(func() { workerOpBudget = oldBudget })
			}
			f := newB788Fix(t)
			if !c.missing {
				b788Op(t, c.body)
			}
			id := f.ticket(t, "B1", "a.txt", "hello\nworld\n")
			start := time.Now()
			code, out, errText := b460Call(t, "worker", "--run", "r1", "--once")
			want := "worker: warning: jev key not read from 1Password (" + c.reason + "); reviews run without Jev (System 1 falls back to rules). Fix: sign in to 1Password (op signin, or unlock the app) and run code-forge keys test jev; the next worker start uses the cached key\n"
			if code != 0 || out != "" || !strings.HasPrefix(errText, want) || strings.Count(errText, "warning") != 1 || !strings.HasSuffix(errText, "worker: drained 1 ticket(s)\n") {
				t.Fatalf("worker --once = %d %q %q", code, out, errText)
			}
			if f.results(t, id) != 1 {
				t.Fatal("ticket not reviewed once")
			}
			if rows := b460Handovers(t, f.home); len(rows) != 0 {
				t.Fatalf("hand-overs %v", rows)
			}
			if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "store", "jev.key")); err == nil {
				t.Fatal("a key was cached after op failed")
			}
			if c.budget != 0 && time.Since(start) > 60*time.Second {
				t.Fatalf("worker took %s; the op budget did not cap it", time.Since(start))
			}
		})
	}
}

// The worker caps each op attempt below run start's wait for the announce.
func TestB788WorkerOpBudgetUnderLaunchWait(t *testing.T) {
	if 2*workerOpBudget >= runLaunchTimeout {
		t.Fatalf("two op attempts (%s each) do not fit run start's wait (%s)", workerOpBudget, runLaunchTimeout)
	}
	restore := workerCapOpTimeout()
	if got := opAttemptTimeout(1); got != workerOpBudget {
		restore()
		t.Fatalf("capped attempt %s", got)
	}
	restore()
	if got := opAttemptTimeout(1); got != opTimeout {
		t.Fatalf("restored attempt %s, want %s", got, opTimeout)
	}
}

// A key store index Go cannot rewrite like Node (bytes that are not UTF-8)
// with an op:// reference: before B788 the worker handed over; now it
// serves without Jev, warns, and never asks op.
func TestB788WorkerUnreadableIndexStartsWithoutJev(t *testing.T) {
	f := newB788Fix(t)
	args := b788Op(t, "printf 'never-used\\n'\n")
	index := filepath.Join(f.home, ".code-forge", "store", "index.json")
	b460Write(t, index, "{\"jev\":\"\xff\"}\n")
	id := f.ticket(t, "B1", "a.txt", "hello\nworld\n")
	code, _, errText := b460Call(t, "worker", "--run", "r1", "--once")
	want := "worker: warning: jev key not resolved (the key store index " + index + " cannot be updated); reviews run without Jev (System 1 falls back to rules). Fix: run code-forge keys test jev\n"
	if code != 0 || !strings.HasPrefix(errText, want) || !strings.HasSuffix(errText, "worker: drained 1 ticket(s)\n") || f.results(t, id) != 1 {
		t.Fatalf("worker --once = %d %q", code, errText)
	}
	if rows := b460Handovers(t, f.home); len(rows) != 0 {
		t.Fatalf("hand-overs %v", rows)
	}
	if _, err := os.Stat(args); err == nil {
		t.Fatal("op was asked")
	}
}
