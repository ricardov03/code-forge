package main

import (
	"testing"

	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B449: --accept-changed asks the owner at a terminal (seams rebaseTTY and
// rebaseConfirm stand in for the terminal here).

func rebaseTestTTY(t *testing.T, tty bool, answer humanask.Result) *[]string {
	asked := []string{}
	oldTTY, oldConfirm := rebaseTTY, rebaseConfirm
	rebaseTTY = func() bool { return tty }
	rebaseConfirm = func(message string) (humanask.Result, error) { asked = append(asked, message); return answer, nil }
	t.Cleanup(func() { rebaseTTY, rebaseConfirm = oldTTY, oldConfirm })
	return &asked
}

func TestBlockRebaseAcceptChangedRunsInGo(t *testing.T) {
	f, head := closedFixture(t, "closed")
	rebaseTestTTY(t, false, humanask.Confirmed)
	code, stdout, stderr := f.call("A", "--run", "r-rebase-unit", "--accept-changed", "a.go", "--reason", "main moved the header")
	if code != 2 || stdout != "" || stderr != "block rebase needs the human at a terminal to confirm; nothing was recorded (there is no --yes)\n" {
		t.Fatalf("no tty: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	asked := rebaseTestTTY(t, true, humanask.Cancelled)
	code, _, stderr = f.call("A", "--run", "r-rebase-unit", "--accept-changed", "a.go", "--reason", "main moved the header")
	if code != 1 || stderr != "block A rebase: cancelled — nothing was recorded\n" || len(*asked) != 1 ||
		(*asked)[0] != "Accept rebased changes in a.go of block A (run r-rebase-unit)? Only the owner may say yes." {
		t.Fatalf("cancel: code=%d stderr=%q asked=%v", code, stderr, *asked)
	}
	rebaseTestTTY(t, true, humanask.Confirmed)
	code, stdout, stderr = f.call("A", "--run", "r-rebase-unit", "--accept-changed", "b.go", "--reason", "main moved the header")
	if code != 2 || stderr != "block rebase: --accept-changed names files not changed by this rebase: b.go\n" {
		t.Fatalf("not changed: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = f.call("A", "--run", "r-rebase-unit", "--accept-changed", "a.go", "--accept-changed", "a.go", "--reason", "main moved the header")
	if code != 0 || stderr != "" || stdout != "block A rebased "+f.base[:12]+" → "+head[:12]+"\n" {
		t.Fatalf("accept: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	rows := f.rows()
	last := rows[len(rows)-1]
	cleared, _ := last["cleared"].([]any)
	if last["event"] != "block.rebase" || len(cleared) != 1 {
		t.Fatalf("row=%v", last)
	}
	entry := cleared[0].(map[string]any)
	if entry["file"] != "a.go" || entry["via"] != "human" || entry["reason"] != "main moved the header" {
		t.Fatalf("cleared=%v", entry)
	}
	if got := f.git(f.tree, "show", "HEAD:a.go"); got != "package a // main\n\n// one\n// two\n// three A" {
		t.Fatalf("a.go=%q", got)
	}
}

func TestBlockRebaseAcceptChangedRefusedUnderActiveGrant(t *testing.T) {
	f, _ := closedFixture(t, "closed")
	err := state.UpdateRun(f.home, "r-rebase-unit", func(record *ledger.Object) error {
		record.Set("autopilot", ledger.Object{{Key: "grant_id", Value: "g1"}, {Key: "status", Value: "active"}, {Key: "scopes", Value: []any{"close:routine"}}, {Key: "deny", Value: []any{}}, {Key: "delegate", Value: "L2"}, {Key: "until", Value: "2999-01-01T00:00:00.000Z"}, {Key: "started_at", Value: "2026-01-01T00:00:00.000Z"}, {Key: "caps", Value: ledger.Object{}}, {Key: "stop_at", Value: 0.5}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	asked := rebaseTestTTY(t, true, humanask.Confirmed)
	code, stdout, stderr := f.call("A", "--run", "r-rebase-unit", "--accept-changed", "a.go", "--reason", "ok")
	if code != 1 || stdout != "" || stderr != "block rebase: block rebase --accept-changed is refused while an autopilot grant is active\n" || len(*asked) != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q asked=%v", code, stdout, stderr, *asked)
	}
}

// The owner confirms, but a block without a worktree has nothing to clear.
func TestBlockRebaseAcceptChangedPlainBlockNeedsWorktree(t *testing.T) {
	f := newRebaseCmdFixture(t, map[string]string{"a.go": "package a\n"})
	f.save("a.go")
	f.setBlock(func(block *ledger.Object) { block.Delete("worktree") })
	asked := rebaseTestTTY(t, true, humanask.Confirmed)
	code, stdout, stderr := f.call("A", "--run", "r-rebase-unit", "--accept-changed", "a.go", "--reason", "ok")
	if code != 2 || stdout != "" || stderr != "block rebase: rebase clearance requires a worktree block\n" || len(*asked) != 1 {
		t.Fatalf("code=%d stdout=%q stderr=%q asked=%v", code, stdout, stderr, *asked)
	}
	if got := f.block().Get("base_sha"); got != f.base {
		t.Fatalf("base moved: %v", got)
	}
}
