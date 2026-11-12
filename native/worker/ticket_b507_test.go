package worker

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// B507: testdata/b507_node_worker_error.golden is what Node's createWorker
// (src/worker/loop.mjs drain, default ledger, {version:1} then an adaptive
// L2 boot config) wrote for this fixture: blocks B6 (worktree: a plain
// directory, not on its branch) and B7 (worktree: a file), B1 with the run
// record unreadable (chmod 000: readRun rethrows, worker-error) and B2 with
// the ledger directory unlistable (chmod 0300: the routing step's readRows
// throws, worker-error with its detail). Paths are written as @ROOT@ and
// @HOME@, the rest normalized like B488.
func TestB507WorkerErrorAndTreeCodesMatchNodeBytes(t *testing.T) {
	fx := newB488Fix(t)
	real := sha("hello\n")
	base := strings.Repeat("a", 40)
	clock := fixedNow
	w := fx.runner(&clock)
	w.Cfg = b492Cfg(t, `{"version":1}`)
	run := func(id string) {
		t.Helper()
		n, poison, err := w.RunTicket(id)
		if err != nil || n != 1 || poison {
			t.Fatalf("RunTicket(%s) = %d, %v, %v; want 1, false, nil", id, n, poison, err)
		}
	}
	fx.blocksRecord(t, `,"blocks":{"B6":{"worktree":{"path":"`+fx.root+`","branch":"b","base_sha":"`+base+`"}},"B7":{"worktree":{"path":"`+filepath.Join(fx.root, "a.txt")+`","branch":"b","base_sha":"`+base+`"}}}`)
	ids := []string{fx.ticket(t, "r1", "B6", "a.txt", real)}
	run(ids[0])
	ids = append(ids, fx.ticket(t, "r1", "B7", "a.txt", real))
	run(ids[1])

	fx.blocksRecord(t, "")
	ids = append(ids, fx.ticket(t, "r1", "B1", "a.txt", real))
	rec := filepath.Join(fx.home, ".code-forge", "runs", "r1.json")
	if err := os.Chmod(rec, 0); err != nil {
		t.Fatal(err)
	}
	run(ids[2])
	if err := os.Chmod(rec, 0o600); err != nil {
		t.Fatal(err)
	}

	w.Cfg = b492Cfg(t, `{"levels":{"L2":{"model":"claude-x","provider":"anthropic"}}}`)
	ids = append(ids, fx.ticket(t, "r1", "B2", "a.txt", real))
	dir := filepath.Join(fx.home, ".code-forge", "ledger")
	if err := os.Chmod(dir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	run(ids[3])
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	got := fx.files(t, ids) + "== ledger\n" + readFile(t, filepath.Join(dir, "unit.jsonl"))
	got = strings.ReplaceAll(strings.ReplaceAll(got, fx.root, "@ROOT@"), fx.home, "@HOME@")
	b497SameLines(t, b488Normalize(got), b488Normalize(readFile(t, filepath.Join("testdata", "b507_node_worker_error.golden"))))
}

// Node's configRecord: a run record that cannot be read at the config read
// gets ONE retry after CONFIG_RETRY_MS (200 ms); a second failure is
// `unavailable` / `config-snapshot`, a good second read goes on.
func TestB507ConfigReadRetriesOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failures int // config reads that fail (after the content root's good read)
		fail     func(path string) ([]byte, error)
	}{
		{"io error twice", 2, func(p string) ([]byte, error) {
			return nil, &os.PathError{Op: "open", Path: p, Err: errors.New("busy")}
		}},
		{"torn record twice", 2, func(string) ([]byte, error) { return []byte(`{"run_id":`), nil }},
		{"io error once", 1, func(p string) ([]byte, error) {
			return nil, &os.PathError{Op: "open", Path: p, Err: errors.New("busy")}
		}},
		{"torn record once", 1, func(string) ([]byte, error) { return []byte(`{"run_id":`), nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newB488Fix(t)
			id := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\n"))
			clock := fixedNow
			w := fx.runner(&clock)
			w.Cfg = b492Cfg(t, `{"version":1}`)
			reads := 0
			w.ReadRecordFile = func(p string) ([]byte, error) {
				reads++
				if reads >= 2 && reads < 2+tc.failures {
					return tc.fail(p)
				}
				return os.ReadFile(p)
			}
			var slept []time.Duration
			w.Sleep = func(d time.Duration) { slept = append(slept, d) }
			n, poison, err := w.RunTicket(id)
			if len(slept) != 1 || slept[0] != 200*time.Millisecond {
				t.Fatalf("slept %v, want one 200ms wait", slept)
			}
			if tc.failures == 2 {
				if err != nil || n != 1 || poison {
					t.Fatalf("RunTicket = %d, %v, %v", n, poison, err)
				}
				if reads != 3 {
					t.Fatalf("reads = %d, want 3", reads)
				}
				res := readFile(t, filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json"))
				if !strings.Contains(res, `"status":"unavailable","reason":"config-snapshot","approved":false`) {
					t.Fatalf("result = %s", res)
				}
				return
			}
			// the retry read the record: the ticket goes on past the config read to
			// the engine, which refuses a record with no blocks entry (Node's)
			if err != nil || n != 1 || poison {
				t.Fatalf("RunTicket = %d, %v, %v", n, poison, err)
			}
			res := readFile(t, filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json"))
			if !strings.Contains(res, `"engine":"adaptive","reason":"run-record-unreadable"`) {
				t.Fatalf("result = %s", res)
			}
		})
	}
}

// Every blockTree code reaches the ticket as Node's reason: foreign,
// workspace-missing and git-failed (no git on PATH), next to the golden's
// branch-mismatch and not-dir.
func TestB507TreeCodes(t *testing.T) {
	for _, tc := range []struct {
		name, reason string
		setup        func(t *testing.T, fx *b488Fix) string // the worktree record
	}{
		{"foreign", "worktree-foreign", func(t *testing.T, fx *b488Fix) string {
			b497Repo(t, fx.root)
			other := t.TempDir()
			b497Repo(t, other)
			b507Git(t, other, "checkout", "-q", "-b", "b")
			return other
		}},
		{"workspace-missing", "worktree-workspace-missing", func(t *testing.T, fx *b488Fix) string {
			b497Repo(t, fx.root)
			wt := filepath.Join(t.TempDir(), "wt")
			b507Git(t, fx.root, "worktree", "add", "-q", "-b", "b", wt)
			// the workspace is a directory the worktree does not have
			ws := filepath.Join(fx.root, "sub")
			if err := os.Mkdir(ws, 0o755); err != nil {
				t.Fatal(err)
			}
			fx.workspace = ws
			return wt
		}},
		{"git-failed", "git-failed", func(t *testing.T, fx *b488Fix) string {
			t.Setenv("PATH", t.TempDir())
			return fx.root
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newB488Fix(t)
			tree := tc.setup(t, fx)
			ws := fx.root
			if fx.workspace != "" {
				ws = fx.workspace
			}
			fx.record(t, `{"run_id":"r1","project":"unit","workspace":"`+ws+`","status":"active","blocks":{"B1":{"worktree":{"path":"`+tree+`","branch":"b","base_sha":"`+strings.Repeat("a", 40)+`"}}}}`)
			id := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\n"))
			clock := fixedNow
			w := fx.runner(&clock)
			w.Cfg = b492Cfg(t, `{"version":1}`)
			if n, poison, err := w.RunTicket(id); err != nil || n != 1 || poison {
				t.Fatalf("RunTicket = %d, %v, %v", n, poison, err)
			}
			res := readFile(t, filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json"))
			if !strings.Contains(res, `"status":"unavailable","reason":"`+tc.reason+`","approved":false`) {
				t.Fatalf("result = %s", res)
			}
		})
	}
}

func b507Git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// A review session that ran and needs a step Go has not ported
// (*review.GapError, here a needs_file answer) is never handed to Node (Node
// would spawn again): the ticket is recorded in Go and nothing is left queued.
func TestB507GapErrorNeverHandsOver(t *testing.T) {
	clock := fixedNow
	fx, w, stderr := b497Fixture(t, &clock)
	needs := strings.Replace(b497FakeClaude, `"findings":[]}`, `"findings":[],"needs_file":["b.txt"]}`, 1)
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte(needs), 0o755); err != nil {
		t.Fatal(err)
	}
	w.SessionDeps.Bins = map[string]string{"claude": bin}
	id := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\nworld\n"))
	n, poison, err := w.RunTicket(id)
	if err != nil || n != 1 || poison {
		t.Fatalf("RunTicket = %d, %v, %v; want the ticket recorded in Go", n, poison, err)
	}
	if !IsDone(fx.root, id) {
		t.Fatal("no done marker")
	}
	if stderr.Len() == 0 {
		t.Fatal("no session ran: the fixture did not reach the gap")
	}
	res := readFile(t, filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json"))
	if !strings.Contains(res, `"status":"unavailable"`) || !strings.Contains(res, "needs-file is not ported to Go") {
		t.Fatalf("result = %s", res)
	}
}
