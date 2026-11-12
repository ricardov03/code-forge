package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B584: `block rebase` runs Node's expiry checks (grant expiry, due
// approvals) and only later may hand a narrow state to Node, whose own
// checks run again. The lines of a check Node repeats (a failed grant
// expiry, an approval not restored yet) must reach the user once, as Node
// alone prints them. Node goldens: testdata/b584_rebase (parity.py.txt,
// CODE_FORGE_NATIVE=0 ./bin/code-forge block rebase … in a temp HOME).

type b584Golden struct {
	Node struct {
		Exit   int    `json:"exit"`
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	} `json:"node"`
}

func b584ReadGolden(t *testing.T, name string) b584Golden {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "b584_rebase", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var g b584Golden
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// b584Fixture is the parity fixture: a closed worktree block whose owned
// file main also changed, with the repo's .code-forge.yml committed.
func b584Fixture(t *testing.T) *rebaseCmdFixture {
	f := newRebaseCmdFixture(t, map[string]string{"a.go": "package a\n\n// one\n// two\n// three\n", "b.go": "package b\n", ".code-forge.yml": "review:\n  max_rounds_per_file: 5 # by hand\n"})
	f.write(f.tree, "a.go", "package a\n\n// one\n// two\n// three A\n")
	f.git(f.tree, "commit", "-qam", "A")
	f.write(f.repo, "a.go", "package a // main\n\n// one\n// two\n// three\n")
	f.git(f.repo, "commit", "-qam", "main touches a")
	f.save("a.go")
	f.setBlock(func(block *ledger.Object) { block.Set("status", "closed") })
	return f
}

func b584ExpiredGrant(t *testing.T) func(record *ledger.Object) {
	return func(record *ledger.Object) {
		record.Set("autopilot", rebaseB578JSON(t, `{"grant_id":"g1","status":"active","scopes":["close:routine"],"deny":[],"delegate":"L2","until":"2026-01-02T00:00:00.000Z","started_at":"2026-01-01T00:00:00.000Z","caps":{},"stop_at":0.5}`))
	}
}

func b584DueApproval(t *testing.T) func(record *ledger.Object) {
	return func(record *ledger.Object) {
		record.Set("autopilot_approvals", rebaseB578JSON(t, `[`+apApproval("apr-1", "review.max_rounds_per_file", "")+`]`))
	}
}

// b584KeyDir leaves the run key a folder: every signed write fails, and the
// rebase hands over with rebase-key-unreadable.
func b584KeyDir(f *rebaseCmdFixture) {
	key := filepath.Join(f.home, ".code-forge", "runs", "r-rebase-unit.key")
	if err := os.Remove(key); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Mkdir(key, 0700); err != nil {
		f.t.Fatal(err)
	}
}

// b584QueueFile makes the review queue a file: rebase-queue-unreadable.
func b584QueueFile(f *rebaseCmdFixture) {
	if err := os.MkdirAll(filepath.Join(f.repo, ".code-forge"), 0700); err != nil {
		f.t.Fatal(err)
	}
	f.write(filepath.Join(f.repo, ".code-forge"), "queue", "not a folder\n")
}

// TestB584RebaseChild runs `block rebase A --run r-rebase-unit` in a child
// process, so a hand-over can exec the fake node.
func TestB584RebaseChild(t *testing.T) {
	if os.Getenv("B584_REBASE_CHILD") != "1" {
		return
	}
	os.Exit(runBlockRebase([]string{"A", "--run", "r-rebase-unit"}))
}

// Each case prints an expiry check line in Go, then hands over. The fake
// node prints what the real Node printed after the Go part in the end-to-end
// parity run (Go binary handing over to real Node): its own check fails
// again, so it prints the whole Node-alone stderr; an approval Go already
// skipped is not due for Node, so it prints only its refusal.
func TestB584RebaseHandoverPrintsExpiryLinesOnce(t *testing.T) {
	for _, tc := range []struct {
		golden, reason, nodeAfter string
		change                    func(t *testing.T) func(record *ledger.Object)
		setup                     func(f *rebaseCmdFixture)
	}{
		{"expired-grant-then-key-unreadable", "rebase-key-unreadable", "", b584ExpiredGrant, b584KeyDir},
		{"due-approval-then-key-unreadable", "rebase-key-unreadable", "", b584DueApproval, b584KeyDir},
		{"due-approval-changed-by-hand-then-queue-unreadable", "rebase-queue-unreadable", "block rebase: the review queue cannot be read\n", b584DueApproval, b584QueueFile},
		{"expired-grant-then-queue-unreadable", "rebase-queue-unreadable", "", b584ExpiredGrant, b584QueueFile},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			golden := b584ReadGolden(t, tc.golden)
			nodeAfter := tc.nodeAfter
			if nodeAfter == "" {
				nodeAfter = golden.Node.Stderr
			}
			f := b584Fixture(t)
			rebaseB578Record(f, tc.change(t))
			tc.setup(f)
			bin := t.TempDir()
			nodeErr := filepath.Join(bin, "node-stderr.txt")
			if err := os.WriteFile(nodeErr, []byte(nodeAfter), 0600); err != nil {
				t.Fatal(err)
			}
			fakeNode := filepath.Join(bin, "fake-node")
			script := "#!/bin/sh\ncat \"" + nodeErr + "\" >&2\nexit " + strconv.Itoa(golden.Node.Exit) + "\n"
			if err := os.WriteFile(fakeNode, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestB584RebaseChild$")
			cmd.Dir = f.repo
			cmd.Env = append(os.Environ(), "B584_REBASE_CHILD=1", "CODE_FORGE_NODE="+fakeNode, "CODE_FORGE_HANDOVER_LOG=1")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			code := 0
			if err := cmd.Run(); err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			if code != golden.Node.Exit || stdout.String() != golden.Node.Stdout || stderr.String() != golden.Node.Stderr {
				t.Fatalf("code=%d stdout=%q stderr=%q\nnode alone: code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String(), golden.Node.Exit, golden.Node.Stdout, golden.Node.Stderr)
			}
			data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			var row map[string]any
			if err := json.Unmarshal(bytes.TrimSpace(data), &row); err != nil || row["reason"] != tc.reason {
				t.Fatalf("handover rows=%s", data)
			}
		})
	}
}

// When Go serves the command, the held lines print in Node's place and
// wording: a read of the key folder names no path, as Node's fd read.
func TestB584RebaseServedPrintsExpiryLinesLikeNode(t *testing.T) {
	for _, tc := range []struct {
		golden string
		change func(t *testing.T) func(record *ledger.Object)
	}{
		{"expired-grant-key-unreadable-dirty-worktree", b584ExpiredGrant},
		{"due-approval-key-unreadable-dirty-worktree", b584DueApproval},
	} {
		t.Run(tc.golden, func(t *testing.T) {
			golden := b584ReadGolden(t, tc.golden)
			f := b584Fixture(t)
			t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
			rebaseB578Record(f, tc.change(t))
			b584KeyDir(f)
			f.write(f.tree, "untracked.txt", "x\n")
			code, stdout, stderr := f.rebase()
			if code != golden.Node.Exit || stdout != golden.Node.Stdout || stderr != golden.Node.Stderr {
				t.Fatalf("code=%d stdout=%q stderr=%q\nnode: code=%d stdout=%q stderr=%q", code, stdout, stderr, golden.Node.Exit, golden.Node.Stdout, golden.Node.Stderr)
			}
			if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl")); !os.IsNotExist(err) {
				t.Fatalf("handed over: %v", err)
			}
			if !strings.HasPrefix(stderr, "autopilot: WARN ") {
				t.Fatalf("no expiry line: %q", stderr)
			}
		})
	}
}
