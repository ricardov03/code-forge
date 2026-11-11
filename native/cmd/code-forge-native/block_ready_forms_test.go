package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B449: block ready runs in Go for an on_block_end hook and for the
// routine-close preview of an active autopilot grant. The fixture's Node is
// unreachable, so a hand-over cannot pass these tests.

// newReadyFixture is the preflight fixture's run without a workspace
// .code-forge.yml (the run's snapshot is its config).
func newReadyFixture(t *testing.T) *preflightFixture {
	t.Helper()
	root := t.TempDir()
	f := &preflightFixture{t: t, home: filepath.Join(root, "home"), repo: filepath.Join(root, "repo"), tree: filepath.Join(root, "tree"), argvLog: filepath.Join(root, "argv.log"), run: "r-ready"}
	for _, dir := range []string{f.home, f.repo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.write(f.repo, ".gitignore", ".code-forge/\n")
	f.write(f.repo, "sample.txt", "old\n")
	f.git(f.repo, "init", "-qb", "main")
	f.git(f.repo, "add", ".")
	f.commit(f.repo, "base")
	f.base = f.git(f.repo, "rev-parse", "HEAD")
	f.git(f.repo, "worktree", "add", "-qb", "forge-b1", f.tree, f.base)
	f.write(f.tree, "sample.txt", "new\n")
	f.git(f.tree, "add", "sample.txt")
	f.commit(f.tree, "work")
	entry := ledger.Object{{Key: "status", Value: "open"}, {Key: "kind", Value: "code"}, {Key: "owned_files", Value: []any{"sample.txt"}},
		{Key: "test_files", Value: []any{}}, {Key: "base_sha", Value: f.base}, {Key: "base_branch", Value: "main"},
		{Key: "worktree", Value: ledger.Object{{Key: "path", Value: f.tree}, {Key: "branch", Value: "forge-b1"}, {Key: "base_sha", Value: f.base}}}}
	record := ledger.Object{{Key: "run_id", Value: f.run}, {Key: "project", Value: "ready"}, {Key: "workspace", Value: f.repo},
		{Key: "status", Value: "active"}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}}
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = 41
	}
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", f.run+".key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	f.sign(ledger.Object{{Key: "event", Value: "dispatch"}, {Key: "block", Value: "B1"}, {Key: "level", Value: "L2"}})
	return f
}

// setSnapshot records cfg (JSON) as the run's config snapshot and, when grant
// is not nil, the run's autopilot grant.
func (f *preflightFixture) setSnapshot(cfg string, grant ledger.Object) {
	f.t.Helper()
	snapshot, err := ledger.DecodeObject([]byte(cfg))
	if err != nil {
		f.t.Fatal(err)
	}
	canonical, err := canon.Marshal(snapshot)
	if err != nil {
		f.t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	hash := hex.EncodeToString(digest[:])
	err = state.UpdateRun(f.home, f.run, func(record *ledger.Object) error {
		record.Set("config", ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: snapshot}}}})
		if grant != nil {
			record.Set("autopilot", grant)
		}
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *preflightFixture) transcript(text string) string {
	f.t.Helper()
	file := filepath.Join(f.home, "coder.log")
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		f.t.Fatal(err)
	}
	return file
}

const readyBaseConfig = `{"gates":{"test":null,"lint":null,"types":null,"format":null,"rerun_failed":0}`

func readyGrant(until string) ledger.Object {
	return ledger.Object{{Key: "grant_id", Value: "g-ready"}, {Key: "status", Value: "active"}, {Key: "scopes", Value: []any{"close"}},
		{Key: "deny", Value: []any{}}, {Key: "delegate", Value: "L2"}, {Key: "until", Value: until},
		{Key: "started_at", Value: "2026-01-01T00:00:00.000Z"}, {Key: "caps", Value: ledger.Object{}}, {Key: "stop_at", Value: 0.5}}
}

// A hook in the snapshot changes nothing a ready preview reports when the
// transcript never names the hook's command.
func TestBlockReadyEndHookRunsInGo(t *testing.T) {
	f := newReadyFixture(t)
	log := f.transcript("plain coder notes: ran go test ./...\n")
	f.setSnapshot(readyBaseConfig+`}`, nil)
	wantCode, wantOut, wantErr := f.call("block", "ready", "B1", "--run", f.run, "--transcript", log)
	if strings.Contains(wantErr, "node") || !strings.Contains(wantOut, "block B1 ") {
		t.Fatalf("baseline without hook: code=%d stdout=%q stderr=%q", wantCode, wantOut, wantErr)
	}
	f.setSnapshot(readyBaseConfig+`,"hooks":{"on_block_end":{"argv":["/usr/local/bin/notify-owner","done"],"timeout_s":5}}}`, nil)
	for _, args := range [][]string{{"--transcript", log}, {"--transcript", log, "--json"}} {
		code, stdout, stderr := f.call(append([]string{"block", "ready", "B1", "--run", f.run}, args...)...)
		if args[len(args)-1] != "--json" && (code != wantCode || stdout != wantOut || stderr != wantErr) {
			t.Fatalf("with hook: code=%d stdout=%q stderr=%q; want code=%d stdout=%q stderr=%q", code, stdout, stderr, wantCode, wantOut, wantErr)
		}
		if args[len(args)-1] == "--json" && (code != wantCode || !strings.HasPrefix(stdout, `{"block":"B1","ready":`) || strings.Contains(stderr, "node")) {
			t.Fatalf("with hook --json: code=%d stdout=%q stderr=%q", code, stdout, stderr)
		}
	}
	// No transcript at all: nothing to scan.
	code, stdout, stderr := f.call("block", "ready", "B1", "--run", f.run)
	if strings.Contains(stderr, "node") || !strings.Contains(stdout, "block B1 ") {
		t.Fatalf("with hook, no transcript: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

// Under close.require_human an active autopilot grant adds Node's
// close-not-routine blocker; an expired grant adds none.
func TestBlockReadyRoutinePreviewRunsInGo(t *testing.T) {
	f := newReadyFixture(t)
	cfg := readyBaseConfig + `,"close":{"require_human":true}}`
	f.setSnapshot(cfg, readyGrant("2999-01-01T00:00:00.000Z"))
	code, stdout, stderr := f.call("block", "ready", "B1", "--run", f.run)
	if code != 1 || strings.Contains(stderr, "node") {
		t.Fatalf("active grant: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "close-not-routine - ") || !strings.Contains(stdout, "no signed green gate row") ||
		!strings.Contains(stdout, "  fix: code-forge block close B1 --run "+f.run+" --decide --reason '<reason>'\n") {
		t.Fatalf("active grant: want a close-not-routine blocker with its decide fix; stdout=%q", stdout)
	}
	code, stdout, stderr = f.call("block", "ready", "B1", "--run", f.run, "--json")
	if code != 1 || !strings.Contains(stdout, `"code":"close-not-routine"`) || strings.Count(stdout, "\n") != 1 {
		t.Fatalf("active grant --json: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	g := newReadyFixture(t)
	g.setSnapshot(cfg, readyGrant("2020-01-01T00:00:00.000Z"))
	code, stdout, stderr = g.call("block", "ready", "B1", "--run", g.run)
	if strings.Contains(stderr, "node") || strings.Contains(stdout, "close-not-routine") || !strings.Contains(stdout, "block B1 ") {
		t.Fatalf("expired grant: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
}
