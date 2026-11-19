package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B789: block land with `native.daemon: false` runs in-process. The audit's
// variant 47 (proof.isolation: worktree + native.daemon: false) closed B1
// and then `block land B1` refused with land-config-daemon; variant 28
// (export isolation + native.daemon: false) has no block worktree, so land
// gives its plain refusal and never needs the daemon.

type b789Fixture struct {
	t                           *testing.T
	home, repo, tree, run, base string
	report, transcript          string
	record, entry               ledger.Object
	worktree                    bool
}

func newB789Fixture(t *testing.T, worktree bool, status string) *b789Fixture {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NATIVE", "1")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &b789Fixture{t: t, home: home, repo: repo, run: "r-b789", worktree: worktree}
	cfg := landTestConfig([]any{"/bin/sh", "-c", "exit 0"})
	if worktree {
		cfg["proof"] = map[string]any{"isolation": "worktree"}
	}
	cfg["native"] = map[string]any{"daemon": false}
	yml, err := json.Marshal(cfg)
	f.must(err)
	f.must(os.WriteFile(filepath.Join(repo, ".code-forge.yml"), yml, 0o600))
	f.must(os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".code-forge/\n"), 0o600))
	f.must(os.WriteFile(filepath.Join(repo, "sample.mjs"), []byte("old\n"), 0o600))
	f.git(repo, "init", "-qb", "main")
	f.git(repo, "config", "user.name", "Fixture")
	f.git(repo, "config", "user.email", "f@example.invalid")
	f.git(repo, "config", "commit.gpgsign", "false")
	f.git(repo, "add", ".")
	f.git(repo, "commit", "-qm", "base")
	f.base = f.git(repo, "rev-parse", "HEAD")
	f.tree = repo
	f.entry = ledger.Object{{Key: "status", Value: status}, {Key: "owned_files", Value: []any{"sample.mjs"}}, {Key: "base_sha", Value: f.base}, {Key: "base_branch", Value: "main"}, {Key: "tree_at_open", Value: ledger.Object{}}, {Key: "kind", Value: "code"}, {Key: "test_files", Value: []any{}}}
	if worktree {
		f.tree = filepath.Join(repo, ".code-forge/worktrees/B1")
		f.git(repo, "worktree", "add", "-qb", "block-B1", f.tree, f.base)
		f.entry.Set("worktree", ledger.Object{{Key: "path", Value: f.tree}, {Key: "branch", Value: "block-B1"}, {Key: "base_sha", Value: f.base}})
	}
	f.must(os.WriteFile(filepath.Join(f.tree, "sample.mjs"), []byte("new\n"), 0o600))
	if worktree {
		f.git(f.tree, "commit", "-qam", "change")
	}
	// The run's config snapshot carries native.daemon: false, as run start writes it.
	snapshot, err := ledger.DecodeObject(yml)
	f.must(err)
	canonical, err := canon.Marshal(snapshot)
	f.must(err)
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])
	pid := float64(os.Getpid())
	f.record = ledger.Object{{Key: "run_id", Value: f.run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: repo}, {Key: "status", Value: "active"},
		{Key: "config", Value: ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: snapshot}}}}},
		{Key: "worker", Value: ledger.Object{{Key: "pid", Value: pid}, {Key: "started_at", Value: statusStart(pid)}}},
		{Key: "blocks", Value: ledger.Object{{Key: "B1", Value: f.entry}}}, {Key: "orphans", Value: []any{}}}
	f.must(state.SaveRun(home, f.record))
	f.must(os.WriteFile(filepath.Join(home, ".code-forge/runs", f.run+".key"), make([]byte, 32), 0o600))
	// The run's live worker (this test process), as the review join reads it.
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	id := strconv.Itoa(os.Getpid())
	worker := filepath.Join(repo, ".code-forge", "workers", id+"-unknown.json")
	f.must(os.MkdirAll(filepath.Dir(worker), 0o700))
	f.must(os.WriteFile(worker, []byte(`{"pid":`+id+`,"start_time":"unknown","run":"`+f.run+`","started_at":"`+stamp+`","heartbeat_at":"`+stamp+`"}`), 0o600))
	hashNow, err := gitx.ContentHash(f.tree, "sample.mjs")
	f.must(err)
	_, err = state.WriteSigned(home, f.run, ledger.Object{{Key: "event", Value: "dispatch"}, {Key: "block", Value: "B1"}, {Key: "level", Value: "L2"}})
	f.must(err)
	_, err = state.WriteSigned(home, f.run, ledger.Object{{Key: "event", Value: "review.approved"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.mjs"}, {Key: "content_hash", Value: hashNow}})
	f.must(err)
	f.report, f.transcript = filepath.Join(home, "report"), filepath.Join(home, "transcript")
	f.must(os.WriteFile(f.report, []byte("TESTS go test exit=0\n===BLOCK B1 COMPLETE===\n"), 0o600))
	f.must(os.WriteFile(f.transcript, []byte("Completed implementation.\n"), 0o600))
	return f
}

func (f *b789Fixture) must(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f *b789Fixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitx.ChildEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// approve queues the block's review ticket through the in-process join and
// marks it reviewed and approved, as a worker would.
func (f *b789Fixture) approve() {
	f.t.Helper()
	seed := reviewWaitBlockJoin(f.tree, f.repo, f.run, "B1", 0, io.Discard)
	files, _ := seed.answer.Get("files").([]any)
	if seed.handed || seed.refusal != nil || len(files) != 1 {
		f.t.Fatalf("seed join = %+v, want one queued ticket", seed)
	}
	ticket := blockString(blockObject(files[0]).Get("ticket"))
	if ticket == "" {
		f.t.Fatalf("seed join queued no ticket: %v", seed.answer)
	}
	f.must(os.WriteFile(filepath.Join(f.repo, ".code-forge", "queue", ticket+".done"), []byte(`{"ticket":"`+ticket+`","status":"reviewed","approved":true,"open_findings":false}`), 0o600))
}

// land runs block land B1 in-process and returns its exit code, output and
// the hand-over reason it asked for ("" when none).
func (f *b789Fixture) land(extra map[string]any) (int, string, string) {
	f.t.Helper()
	flags := map[string]any{"report": f.report, "transcript": f.transcript, "max": "20s"}
	for k, v := range extra {
		flags[k] = v
	}
	capture, err := os.CreateTemp(f.home, "b789-land-")
	f.must(err)
	out, errOut := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = capture, capture
	handed := ""
	code := landExecuteWithReason(nil, flags, f.home, f.run, "B1", f.record, f.entry, func(reason handoverReason) int {
		handed = handoverReasons[reason]
		return 127
	})
	os.Stdout, os.Stderr = out, errOut
	f.must(capture.Close())
	data, err := os.ReadFile(capture.Name())
	f.must(err)
	return code, string(data), handed
}

// Variant 47, block already closed: land finishes the merge step with no daemon.
func TestB789LandDaemonOffClosedWorktree(t *testing.T) {
	f := newB789Fixture(t, true, "closed")
	// block close's done record names the commit it checked.
	done := filepath.Join(f.home, ".code-forge/runs", f.run, "blocks", "B1.done.json")
	f.must(os.MkdirAll(filepath.Dir(done), 0o700))
	f.must(os.WriteFile(done, []byte(`{"status":"closed","commit":"`+f.git(f.tree, "rev-parse", "HEAD")+`"}`), 0o600))
	code, output, handed := f.land(nil)
	if handed != "" || code != 0 {
		t.Fatalf("land = %d handed=%q, want 0 with no hand-over:\n%s", code, handed, output)
	}
	want := "block B1 closed; merge: git -C " + f.repo + " merge --no-ff --no-edit -- block-B1"
	if !strings.Contains(output, want) {
		t.Fatalf("land output lacks %q:\n%s", want, output)
	}
}

// Variant 47, open block: land joins the reviews in-process, runs the gates,
// closes the block and merges it, with no daemon socket anywhere.
func TestB789LandDaemonOffOpenWorktree(t *testing.T) {
	f := newB789Fixture(t, true, "open")
	f.approve()
	code, output, handed := f.land(map[string]any{"merge": true, "no-merge-gate": true})
	if handed != "" || code != 0 {
		t.Fatalf("land = %d handed=%q, want 0 with no hand-over:\n%s", code, handed, output)
	}
	record, err := state.ReadRun(f.home, f.run)
	f.must(err)
	if got := blockObject(blockObject(record.Get("blocks")).Get("B1")).Get("status"); got != "closed" {
		t.Fatalf("block status = %v, want closed:\n%s", got, output)
	}
	if got := f.git(f.repo, "show", "HEAD:sample.mjs"); got != "new" {
		t.Fatalf("main sample.mjs = %q, want the merged block change:\n%s", got, output)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".code-forge/runs", f.run, "native.sock")); !os.IsNotExist(err) {
		t.Fatalf("land must not use a daemon socket: %v", err)
	}
}

// Variant 47, open block, review not finished: the in-process join waits to
// --max and land stops at join with the retry command, not a hand-over.
func TestB789LandDaemonOffJoinTimeout(t *testing.T) {
	f := newB789Fixture(t, true, "open")
	code, output, handed := f.land(map[string]any{"max": "1s"})
	if handed != "" || code != 1 {
		t.Fatalf("land = %d handed=%q, want 1 with no hand-over:\n%s", code, handed, output)
	}
	if !strings.Contains(output, "block B1 land stopped at join: wait_timeout") {
		t.Fatalf("land output lacks the join timeout:\n%s", output)
	}
}

// Variant 28 (export isolation, daemon off): no block worktree, so land
// refuses plainly, as it does with the daemon on.
func TestB789LandDaemonOffExport(t *testing.T) {
	for _, status := range []string{"open", "closed"} {
		t.Run(status, func(t *testing.T) {
			f := newB789Fixture(t, false, status)
			code, output, handed := f.land(nil)
			want := map[string]string{"open": "block has no recorded worktree and branch", "closed": "block is closed, not open"}[status]
			if handed != "" || code != 1 || !strings.Contains(output, "block B1 land stopped at join: "+want) {
				t.Fatalf("land = %d handed=%q, want 1 %q:\n%s", code, handed, want, output)
			}
		})
	}
}
