package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B738: bin/code-forge.mjs clears the reported error kind before a verb runs
// and takes it after the verb ends, so a kind one verb reported never reaches
// the next verb's errors.jsonl entry. In one test binary, a verb body run
// below the router (keys set --op with no op CLI reports op_missing, as
// TestB678KeysOpMissing does) left the kind set, and a later failing verb was
// logged with it (B689 saw op_timeout for an `error` row).

func b738ErrorKinds(t *testing.T, home string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	if err != nil {
		t.Fatalf("no errors.jsonl entry: %v", err)
	}
	kinds := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("entry %q: %v", line, err)
		}
		kind, _ := entry["kind"].(string)
		kinds = append(kinds, kind)
	}
	return kinds
}

// b738LeakOpMissing runs the keys set --op body with no op CLI on PATH: it
// fails with exit 1 after reporting op_missing.
func b738LeakOpMissing(t *testing.T) {
	t.Helper()
	_, project, _ := b678Op(t, "ok", "", true)
	if code, _, stderr := b456Keys(t, project, "set", "foo", "--op", "op://v/i/f"); code != 1 || !strings.Contains(stderr, "1Password CLI not found") {
		t.Fatalf("leak step: exit %d %q", code, stderr)
	}
}

func TestB738ReportedKindDoesNotLeakIntoTheNextVerb(t *testing.T) {
	b738LeakOpMissing(t)
	home := b613Env(t)
	code, stdout, stderr := runCaptured(t, "keys", "get", "b738-none")
	if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "usage: code-forge keys ") {
		t.Fatalf("keys get (no such subcommand): exit %d %q %q", code, stdout, stderr)
	}
	if kinds := b738ErrorKinds(t, home); len(kinds) != 1 || kinds[0] != "usage" {
		t.Fatalf("errors.jsonl kinds %q, want [\"usage\"]", kinds)
	}
}

// A verb that reports a kind and fails while the error log is off still takes
// the kind (Node's takeErrorKind runs on every exit), so the next failing
// verb, run straight from its route, is logged with its own kind.
func TestB738ReportedKindTakenWhenTheLogIsOff(t *testing.T) {
	_, project, _ := b678Op(t, "ok", "", true)
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	b460Chdir(t, project)
	if code, _, stderr := runCapturedRoute(t, routes["keys"], "set", "foo", "--op", "op://v/i/f"); code != 1 || !strings.Contains(stderr, "1Password CLI not found") {
		t.Fatalf("keys set --op with no op: exit %d %q", code, stderr)
	}
	home := b613Env(t)
	code, stdout, stderr := runCapturedRoute(t, routes["keys"], "get", "b738-none")
	if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "usage: code-forge keys ") {
		t.Fatalf("keys get (no such subcommand): exit %d %q %q", code, stdout, stderr)
	}
	if kinds := b738ErrorKinds(t, home); len(kinds) != 1 || kinds[0] != "usage" {
		t.Fatalf("errors.jsonl kinds %q, want [\"usage\"]", kinds)
	}
}

// A worker run names its warnings "worker"; a spawn run after it in the same
// process must log its budget warning under "spawn" again, as a new Node
// process does (TestB482SpawnRunBudgetWarning after
// TestB489WorkerOnceServesInGo failed under -shuffle).
func TestB738WarningVerbDoesNotLeakFromWorkerToSpawn(t *testing.T) {
	t.Run("worker", TestB489WorkerOnceServesInGo)
	t.Run("spawn", TestB482SpawnRunBudgetWarning)
}
