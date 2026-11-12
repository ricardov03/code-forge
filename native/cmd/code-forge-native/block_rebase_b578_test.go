package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B578: the guarded run states `block rebase` handed to a Node child run in
// Go. Node is unreachable here (CODE_FORGE_NODE=/nonexistent): a hand-over
// fails these tests. The Node outputs they hold were taken with
// CODE_FORGE_NATIVE=0 ./bin/code-forge block rebase … in a temp HOME.

// rebaseB578Record changes the saved run record.
func rebaseB578Record(f *rebaseCmdFixture, change func(record *ledger.Object)) {
	f.t.Helper()
	if err := state.UpdateRun(f.home, "r-rebase-unit", func(record *ledger.Object) error { change(record); return nil }); err != nil {
		f.t.Fatal(err)
	}
}

func rebaseB578JSON(t *testing.T, text string) any {
	t.Helper()
	value, err := ledger.DecodeValue([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

// rebaseB578Plain is an open worktree block whose branch main has moved past.
func rebaseB578Plain(t *testing.T) (*rebaseCmdFixture, string) {
	f := newRebaseCmdFixture(t, map[string]string{"a.go": "package a\n", "b.go": "package b\n", ".code-forge.yml": "review:\n  max_rounds_per_file: 5 # by hand\n"})
	f.write(f.tree, "a.go", "package a\n\nfunc A() {}\n")
	f.git(f.tree, "commit", "-qam", "A")
	f.write(f.repo, "b.go", "package b\n\nfunc B() {}\n")
	f.git(f.repo, "commit", "-qam", "B lands")
	head := f.git(f.repo, "rev-parse", "HEAD")
	f.save("a.go")
	return f, head
}

func rebaseB578Rebased(t *testing.T, f *rebaseCmdFixture, head, name string, code int, stdout, stderr, wantErr string) {
	t.Helper()
	if code != 0 || stderr != wantErr || stdout != "block A rebased "+f.base[:12]+" → "+head[:12]+"\n" {
		t.Fatalf("%s: code=%d stdout=%q stderr=%q", name, code, stdout, stderr)
	}
	if got := f.block().Get("base_sha"); got != head {
		t.Fatalf("%s: base_sha=%v want %s", name, got, head)
	}
	if got := f.git(f.tree, "show", "HEAD:b.go"); !strings.Contains(got, "func B()") {
		t.Fatalf("%s: tree not on main: %q", name, got)
	}
}

// An approval that is active but not due yet, and run records the old
// end-safe guard refused (no orphans list, an odd sibling block, a config
// snapshot status cannot read): Node rebases them like any other run.
func TestB578RebaseGuardedRecordsRunInGo(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(record *ledger.Object)
	}{
		{"active approval not due", func(record *ledger.Object) {
			approval := strings.Replace(apApproval("apr-1", "review.max_rounds_per_file", ""), "2026-11-11T09:00:00.000Z", "2099-01-01T00:00:00.000Z", 1)
			record.Set("autopilot_approvals", rebaseB578JSON(t, `[`+approval+`]`))
		}},
		{"no orphans list", func(record *ledger.Object) { record.Delete("orphans") }},
		{"sibling with odd owned files", func(record *ledger.Object) {
			blocks := blockObject(record.Get("blocks"))
			blocks.Set("B", ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{float64(1)}}})
			record.Set("blocks", blocks)
		}},
		{"config snapshot not readable", func(record *ledger.Object) {
			record.Set("config", ledger.Object{{Key: "hash", Value: "nope"}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, head := rebaseB578Plain(t)
			rebaseB578Record(f, tc.change)
			code, stdout, stderr := f.rebase()
			rebaseB578Rebased(t, f, head, tc.name, code, stdout, stderr, "")
		})
	}
}

// A due approval is restored first (Node's expire(): checkApprovals), its
// line printed before the rebase; a key changed by hand is left alone.
func TestB578RebaseRestoresDueApprovalInGo(t *testing.T) {
	f, head := rebaseB578Plain(t)
	rebaseB578Record(f, func(record *ledger.Object) {
		record.Set("autopilot_approvals", rebaseB578JSON(t, `[`+apApproval("apr-1", "review.max_rounds_per_file", "")+`]`))
	})
	now := rebaseNow
	rebaseNow = func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { rebaseNow = now })
	code, stdout, stderr := f.rebase()
	rebaseB578Rebased(t, f, head, "due approval", code, stdout, stderr, "autopilot: approval apr-1 expired but its key was changed by hand; left for the owner\n")
	record, err := state.ReadRun(f.home, "r-rebase-unit")
	if err != nil {
		t.Fatal(err)
	}
	approval := record.Get("autopilot_approvals").([]any)[0].(ledger.Object)
	if approval.Get("status") != "restore_skipped" || approval.Get("ended_at") != "2026-11-11T10:00:00.000Z" {
		t.Fatalf("approval=%v", approval)
	}
	events := []any{}
	for _, row := range f.rows() {
		events = append(events, row["event"])
	}
	if len(events) != 2 || events[0] != "autopilot.restore_skipped" || events[1] != "block.rebase" {
		t.Fatalf("events=%v", events)
	}
	if data, _ := os.ReadFile(filepath.Join(f.repo, ".code-forge.yml")); string(data) != "review:\n  max_rounds_per_file: 5 # by hand\n" {
		t.Fatalf("config changed: %q", data)
	}
}

// Node tests rebase_check for truth: a falsy checkpoint is none, any other
// value that is not a signed row is the invalid-checkpoint refusal.
func TestB578RebaseCheckpointShapesRunInGo(t *testing.T) {
	f, head := closedFixture(t, "closed")
	oldHead := f.git(f.tree, "rev-parse", "HEAD")
	f.setBlock(func(block *ledger.Object) { block.Set("rebase_check", "pending") })
	code, stdout, stderr := f.rebase()
	if code != 1 || stdout != "" || stderr != "block rebase: the pending rebase checkpoint is invalid\n" {
		t.Fatalf("truthy: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if got := f.git(f.tree, "rev-parse", "HEAD"); got != oldHead || f.block().Get("rebase_check") != "pending" || f.block().Get("base_sha") != f.base {
		t.Fatalf("truthy moved: head=%s block=%v", got, f.block())
	}
	// A falsy checkpoint: the closed block's changed file needs a review,
	// exactly as without one.
	f.setBlock(func(block *ledger.Object) { block.Set("rebase_check", false) })
	code, stdout, stderr = f.rebase()
	if code != 1 || stdout != "" || !strings.HasPrefix(stderr, "block rebase: rebase-changed-reviewed a.go\n") {
		t.Fatalf("falsy: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	// With nothing changed by the replay a falsy checkpoint rebases and is
	// dropped, as Node's `delete block.rebase_check`.
	g := newRebaseCmdFixture(t, map[string]string{"a.go": "package a\n", "b.go": "package b\n"})
	g.write(g.tree, "a.go", "package a\n\nfunc A() {}\n")
	g.git(g.tree, "commit", "-qam", "A")
	g.write(g.repo, "b.go", "package b\n\nfunc B() {}\n")
	g.git(g.repo, "commit", "-qam", "B lands")
	head = g.git(g.repo, "rev-parse", "HEAD")
	g.save("a.go")
	g.setBlock(func(block *ledger.Object) { block.Set("status", "closed"); block.Set("rebase_check", float64(0)) })
	code, stdout, stderr = g.rebase()
	if code != 0 || stderr != "" || stdout != "block A rebased "+g.base[:12]+" → "+head[:12]+"\n" {
		t.Fatalf("falsy clean: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if _, has := rebaseB578Lookup(g.block(), "rebase_check"); has || g.block().Get("base_sha") != head {
		t.Fatalf("falsy clean: block=%v", g.block())
	}
}

func rebaseB578Lookup(o ledger.Object, key string) (any, bool) {
	for _, field := range o {
		if field.Key == key {
			return field.Value, true
		}
	}
	return nil, false
}

// No HOME: Node's os.homedir() reads the user database; so does Go.
func TestB578RebaseHomeFromUserDatabase(t *testing.T) {
	f, head := rebaseB578Plain(t)
	seam := rebaseUserHome
	rebaseUserHome = func() string { return f.home }
	t.Cleanup(func() { rebaseUserHome = seam })
	t.Setenv("HOME", "")
	code, stdout, stderr := f.rebase()
	t.Setenv("HOME", f.home)
	rebaseB578Rebased(t, f, head, "no HOME", code, stdout, stderr, "")
}

// The narrow states Go does not serve keep a named hand-over, decided
// before anything is written.
func TestB578RebaseNarrowHandovers(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		setup        func(f *rebaseCmdFixture)
	}{
		{"worktree not an object", "rebase-record-shape", func(f *rebaseCmdFixture) {
			f.setBlock(func(block *ledger.Object) { block.Set("worktree", "x") })
		}},
		{"approval without a date Go reads", "rebase-autopilot-shape", func(f *rebaseCmdFixture) {
			rebaseB578Record(f, func(record *ledger.Object) {
				approval := strings.Replace(apApproval("apr-1", "review.max_rounds_per_file", ""), "2026-11-11T09:00:00.000Z", "soon", 1)
				record.Set("autopilot_approvals", rebaseB578JSON(t, `[`+approval+`]`))
			})
		}},
		{"key not readable", "rebase-key-unreadable", func(f *rebaseCmdFixture) {
			key := filepath.Join(f.home, ".code-forge", "runs", "r-rebase-unit.key")
			if err := os.Remove(key); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(key, 0700); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := closedFixture(t, "closed")
			t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
			tc.setup(f)
			before := f.stateFiles()
			oldHead := f.git(f.tree, "rev-parse", "HEAD")
			code, stdout, stderr := f.rebase()
			if code != 127 || stdout != "" || !strings.Contains(stderr, `node executable "/nonexistent" not found`) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
			}
			data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var row map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(data), &row); err != nil || row["reason"] != tc.reason {
				t.Fatalf("handover rows=%s", data)
			}
			after := f.stateFiles()
			for name, body := range before {
				if after[name] != body {
					t.Fatalf("%s changed before the hand-over", name)
				}
			}
			if got := f.git(f.tree, "rev-parse", "HEAD"); got != oldHead {
				t.Fatalf("head moved: %s", got)
			}
		})
	}
}
