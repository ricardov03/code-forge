package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B462: proof lock, unlock, restore and export --remove run in Go. Before
// B462 every one of these calls handed over to Node (proof-run).

const b462Run = "r-b462-proof"

// b462Case is one call against a fresh run fixture.
type b462Case struct {
	name   string
	args   []string
	blocks ledger.Object
	extra  ledger.Object // more run record fields (status, proof_lock)
	setup  func(t *testing.T, repo string)
	code   int
	stdout string // golden, with <REPO> for the workspace
	stderr string
}

// b462Fixture writes a run record, its key and a git repo with one commit.
func b462Fixture(t *testing.T, c b462Case) (home, repo string) {
	t.Helper()
	home = t.TempDir()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"version":1}`)
	if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	b462Git(t, repo, "init", "-qb", "main")
	b462Git(t, repo, "add", ".code-forge.yml")
	b462Git(t, repo, "-c", "user.name=Fixture", "-c", "user.email=f@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	snapshot, _ := ledger.DecodeObject(data)
	canonical, _ := canon.Marshal(snapshot)
	digest := sha256.Sum256(canonical)
	hash := hex.EncodeToString(digest[:])
	record := ledger.Object{
		{Key: "run_id", Value: b462Run}, {Key: "project", Value: "fixture"},
		{Key: "workspace", Value: repo}, {Key: "status", Value: "active"}, {Key: "orphans", Value: []any{}},
		{Key: "config", Value: ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: snapshot}}}}},
		{Key: "blocks", Value: c.blocks},
	}
	for _, field := range c.extra {
		record.Set(field.Key, field.Value)
	}
	if err := state.SaveRun(home, record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".code-forge", "runs", b462Run+".key"), bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if c.setup != nil {
		c.setup(t, repo)
	}
	return home, repo
}

func b462Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, append(gitx.ChildEnv(), "GIT_AUTHOR_DATE=2026-11-11T00:00:00Z", "GIT_COMMITTER_DATE=2026-11-11T00:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func b462Open(extra ...ledger.Field) ledger.Object {
	block := ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{}}, {Key: "owned_kinds", Value: ledger.Object{}}}
	return append(block, extra...)
}

func b462Write(t *testing.T, file string, data []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(file, mode); err != nil {
		t.Fatal(err)
	}
}

// b462Journal writes a red→green journal in dir: a.txt existed (with mode
// 0640) and new.txt did not; both are now changed.
func b462Journal(t *testing.T, dir string, sha string) {
	t.Helper()
	original := []byte("original\n")
	if sha == "" {
		sum := sha256.Sum256(original)
		sha = hex.EncodeToString(sum[:])
	}
	journal := map[string]any{"version": 1, "pid": 1, "created_at": "2026-11-11T00:00:00.000Z", "files": []any{
		map[string]any{"path": "src/a.txt", "existed": true, "sha256": sha, "mode": 0o640, "content": base64.StdEncoding.EncodeToString(original)},
		map[string]any{"path": "new.txt", "existed": false},
	}}
	data, _ := json.Marshal(journal)
	b462Write(t, filepath.Join(dir, ".code-forge", "proof-journal.json"), append(data, '\n'), 0o600)
	b462Write(t, filepath.Join(dir, "src", "a.txt"), []byte("mutated\n"), 0o644)
	b462Write(t, filepath.Join(dir, "new.txt"), []byte("left behind\n"), 0o644)
}

var b462Cases = []b462Case{
	{name: "lock", args: []string{"proof", "lock", "B1", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		stdout: `{"locked":{"block":"B1","acquired_at":"2026-11-11T08:00:00.000Z"}}` + "\n"},
	{name: "lock-held", args: []string{"proof", "lock", "B1", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		extra:  ledger.Object{{Key: "proof_lock", Value: ledger.Object{{Key: "block", Value: "B1"}, {Key: "acquired_at", Value: "2026-01-01T00:00:00.000Z"}}}},
		stdout: `{"locked":{"block":"B1","acquired_at":"2026-01-01T00:00:00.000Z"}}` + "\n"},
	{name: "lock-busy-holder", args: []string{"proof", "lock", "B1", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}, {Key: "B2", Value: b462Open()}},
		extra: ledger.Object{{Key: "proof_lock", Value: ledger.Object{{Key: "block", Value: "B2"}, {Key: "acquired_at", Value: "2026-01-01T00:00:00.000Z"}}}},
		code:  1, stderr: "proof lock: proof.busy: block B2 holds the proof lock\n"},
	{name: "lock-busy-gate", args: []string{"proof", "lock", "B1", "--run", b462Run}, blocks: ledger.Object{
		{Key: "B1", Value: b462Open()},
		{Key: "B2", Value: b462Open(ledger.Field{Key: "gate_running", Value: true})},
		{Key: "B3", Value: b462Open(ledger.Field{Key: "gate_running", Value: true}, ledger.Field{Key: "gate_pid", Value: float64(os.Getpid())})},
		{Key: "B4", Value: b462Open(ledger.Field{Key: "gate_running", Value: false})},
		{Key: "B5", Value: ledger.Object{{Key: "status", Value: "closed"}, {Key: "gate_running", Value: true}}},
	}, code: 1, stderr: "proof lock: proof.busy: gate running for block B2, B3\n"},
	{name: "lock-worktree", args: []string{"proof", "lock", "B1", "--run", b462Run},
		blocks: ledger.Object{{Key: "B1", Value: b462Open(ledger.Field{Key: "worktree", Value: ledger.Object{{Key: "path", Value: "/x"}}})}},
		code:   2, stderr: "proof lock: block B1 is proven in an export built from its tree; proof lock does not apply\n"},
	{name: "lock-ended", args: []string{"proof", "lock", "B1", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		extra: ledger.Object{{Key: "status", Value: "ended"}}, code: 1, stderr: "proof lock: run " + b462Run + " has ended\n"},
	{name: "lock-not-open", args: []string{"proof", "lock", "B9", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		code: 1, stderr: "proof lock: block B9 is not open in run " + b462Run + "\n"},
	{name: "lock-no-run", args: []string{"proof", "lock", "B1", "--run", "r-none"}, blocks: ledger.Object{},
		code: 1, stderr: "proof lock: no run record for r-none\n"},
	{name: "export-no-run", args: []string{"proof", "export", "B1", "--run", "r-none"}, blocks: ledger.Object{},
		code: 1, stderr: "proof export: no run record for r-none\n"},
	{name: "restore-bad-run-id", args: []string{"proof", "restore", "B1", "--run", "../x"}, blocks: ledger.Object{},
		code: 1, stderr: "proof restore: run id must match /^[a-z0-9][a-z0-9-]{0,63}$/\n"},
	{name: "unlock", args: []string{"proof", "unlock", "B1", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		extra:  ledger.Object{{Key: "proof_lock", Value: ledger.Object{{Key: "block", Value: "B1"}, {Key: "acquired_at", Value: "2026-01-01T00:00:00.000Z"}}}},
		stdout: `{"unlocked":"B1"}` + "\n"},
	{name: "unlock-none", args: []string{"proof", "unlock", "B1", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		code: 1, stderr: "proof unlock: run " + b462Run + " has no proof lock\n"},
	{name: "unlock-other", args: []string{"proof", "unlock", "B1", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		extra: ledger.Object{{Key: "proof_lock", Value: ledger.Object{{Key: "block", Value: "B2"}, {Key: "acquired_at", Value: "2026-01-01T00:00:00.000Z"}}}},
		code:  1, stderr: "proof unlock: block B1 does not hold the proof lock (B2 does)\n"},
	{name: "restore-none", args: []string{"proof", "restore", "B1", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		code: 1, stderr: "proof restore: no red→green journal for block B1 — nothing to restore\n"},
	{name: "restore-workspace", args: []string{"proof", "restore", "B1", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		setup:  func(t *testing.T, repo string) { b462Journal(t, repo, "") },
		stdout: `{"restored":[{"dir":"<REPO>","files":["src/a.txt","new.txt"]}]}` + "\n"},
	{name: "restore-both", args: []string{"proof", "restore", "B1", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		setup: func(t *testing.T, repo string) {
			b462Journal(t, repo, "")
			b462Journal(t, filepath.Join(repo, ".code-forge", "export", "B1"), "")
		},
		stdout: `{"restored":[{"dir":"<REPO>/.code-forge/export/B1","files":["src/a.txt","new.txt"]},{"dir":"<REPO>","files":["src/a.txt","new.txt"]}]}` + "\n"},
	{name: "restore-mismatch", args: []string{"proof", "restore", "B1", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		setup: func(t *testing.T, repo string) { b462Journal(t, repo, strings.Repeat("0", 64)) },
		code:  1, stderr: "proof restore: restore: 1 file(s) do not match the journal (src/a.txt); the journal is kept\n"},
	{name: "restore-bad-version", args: []string{"proof", "restore", "B1", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		setup: func(t *testing.T, repo string) {
			b462Write(t, filepath.Join(repo, ".code-forge", "proof-journal.json"), []byte(`{"version":2,"files":[]}`), 0o600)
		},
		code: 1, stderr: "proof restore: .code-forge/proof-journal.json is not a version 1 journal\n"},
	{name: "restore-bad-path", args: []string{"proof", "restore", "B1", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		setup: func(t *testing.T, repo string) {
			b462Write(t, filepath.Join(repo, ".code-forge", "proof-journal.json"), []byte(`{"version":1,"files":[{"path":"a/../b"}]}`), 0o600)
		},
		code: 1, stderr: "proof restore: journal: no \".\", \"..\" or empty segments\n"},
	{name: "restore-bad-id", args: []string{"proof", "restore", "B 1", "--run", b462Run}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		code: 1, stderr: "proof restore: block id must match /^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/\n"},
	{name: "export-remove", args: []string{"proof", "export", "B1", "--run", b462Run, "--remove"}, blocks: ledger.Object{{Key: "B1", Value: b462Open()}},
		setup: func(t *testing.T, repo string) {
			b462Write(t, filepath.Join(repo, ".code-forge", "export", "B1", "deep", "x.txt"), []byte("x"), 0o644)
		},
		stdout: `{"removed":"B1"}` + "\n"},
	{name: "export-remove-not-open", args: []string{"proof", "export", "B1", "--run", b462Run, "--remove"},
		blocks: ledger.Object{{Key: "B1", Value: ledger.Object{{Key: "status", Value: "landed"}}}},
		code:   1, stderr: "proof export: block B1 is not open in run " + b462Run + "\n"},
}

// b462State is what a call left behind: the run record, the ledger rows
// (without ts and mac, which carry the clock) and the workspace files.
type b462State struct {
	record string
	rows   []string
	files  map[string]string
}

var b462Stamp = regexp.MustCompile(`20\d\d-\d\d-\d\dT\d\d:\d\d:\d\d\.\d\d\dZ`)

func b462Collect(t *testing.T, home, repo string) b462State {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "runs", b462Run+".json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	got := b462State{record: strings.ReplaceAll(string(data), repo, "<REPO>"), files: map[string]string{}}
	_ = filepath.WalkDir(filepath.Join(home, ".code-forge", "ledger"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		text, _ := os.ReadFile(p)
		for _, line := range strings.Split(strings.TrimSpace(string(text)), "\n") {
			row, err := ledger.DecodeObject([]byte(line))
			if err != nil {
				continue
			}
			row.Delete("ts")
			row.Delete("mac")
			out, _ := ledger.Stringify(row, false)
			got.rows = append(got.rows, string(out))
		}
		return nil
	})
	_ = filepath.WalkDir(repo, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(repo, p)
		if d.IsDir() {
			if rel == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		info, _ := d.Info()
		if info.Mode()&fs.ModeSymlink != 0 {
			target, _ := os.Readlink(p)
			got.files[rel] = "link " + strings.ReplaceAll(target, repo, "<REPO>")
			return nil
		}
		text, _ := os.ReadFile(p)
		body := b462Stamp.ReplaceAllString(strings.ReplaceAll(string(text), repo, "<REPO>"), "<T>")
		got.files[rel] = strconv.FormatUint(uint64(info.Mode().Perm()), 8) + " " + body
		return nil
	})
	sort.Strings(got.rows)
	return got
}

func b462GoCall(t *testing.T, home, cwd string, args []string) (int, string, string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b462-node")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "0")
	t.Setenv("CODE_FORGE_TMP_ROOT", "")
	saved := proofNow
	proofNow = func() time.Time { return time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC) }
	defer func() { proofNow = saved }()
	here, _ := os.Getwd()
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(here)
	return runCaptured(t, args...)
}

func TestB462ProofRunGo(t *testing.T) {
	for _, c := range b462Cases {
		t.Run(c.name, func(t *testing.T) {
			home, repo := b462Fixture(t, c)
			code, stdout, stderr := b462GoCall(t, home, repo, c.args)
			stdout = strings.ReplaceAll(stdout, repo, "<REPO>")
			if code != c.code || stdout != c.stdout || stderr != c.stderr {
				t.Fatalf("got code %d stdout %q stderr %q\nwant code %d stdout %q stderr %q", code, stdout, stderr, c.code, c.stdout, c.stderr)
			}
			after := b462Collect(t, home, repo)
			switch c.name {
			case "lock":
				record, _ := ledger.DecodeObject([]byte(after.record))
				if lock, _ := ledger.Stringify(record.Get("proof_lock"), false); string(lock) != `{"block":"B1","acquired_at":"2026-11-11T08:00:00.000Z"}` {
					t.Fatalf("record has no lock: %s", after.record)
				}
				if want := []string{`{"run":"r-b462-proof","step":"lock","event":"proof","block":"B1","isolation":"lock","tokens_source":null,"cost_source":null}`}; !reflect.DeepEqual(after.rows, want) {
					t.Fatalf("rows %v, want %v", after.rows, want)
				}
			case "unlock":
				if strings.Contains(after.record, "proof_lock") {
					t.Fatalf("lock kept: %s", after.record)
				}
				if want := []string{`{"run":"r-b462-proof","step":"unlock","event":"proof","block":"B1","isolation":"lock","tokens_source":null,"cost_source":null}`}; !reflect.DeepEqual(after.rows, want) {
					t.Fatalf("rows %v, want %v", after.rows, want)
				}
			case "restore-workspace", "restore-both":
				if after.files["src/a.txt"] != "640 original\n" {
					t.Fatalf("files not restored: %v", after.files)
				}
				if _, ok := after.files["new.txt"]; ok {
					t.Fatalf("new.txt kept: %v", after.files)
				}
				if _, ok := after.files[".code-forge/proof-journal.json"]; ok {
					t.Fatalf("journal kept: %v", after.files)
				}
			case "restore-mismatch":
				if _, ok := after.files[".code-forge/proof-journal.json"]; !ok {
					t.Fatalf("journal removed after a mismatch: %v", after.files)
				}
			case "export-remove":
				if _, err := os.Stat(filepath.Join(repo, ".code-forge", "export", "B1")); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("export dir kept: %v", err)
				}
			case "export-build", "export-config-lists":
				if len(after.rows) != 1 || !strings.Contains(after.rows[0], `"step":"export"`) {
					t.Fatalf("rows %v, want one export row", after.rows)
				}
			default:
				if len(after.rows) != 0 {
					t.Fatalf("a refused call wrote rows: %v", after.rows)
				}
			}
		})
	}
}

// TestB462ProofRunMatchesNode runs each case through both engines on twin
// fixtures and compares output, exit code, run record, ledger rows and files.
func TestB462ProofRunMatchesNode(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH; the Go side is checked by TestB462ProofRunGo")
	}
	for _, c := range b462Cases {
		t.Run(c.name, func(t *testing.T) {
			goHome, goRepo := b462Fixture(t, c)
			goCode, goOut, goErr := b462GoCall(t, goHome, goRepo, c.args)
			nodeHome, nodeRepo := b462Fixture(t, c)
			cmd := exec.Command(node, append([]string{filepath.Join(b458Repo(t), "bin", "code-forge.mjs")}, c.args...)...)
			cmd.Dir = nodeRepo
			cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + nodeHome, "TMPDIR=" + os.TempDir(), "CODE_FORGE_NATIVE=0", "CODE_FORGE_HANDOVER_LOG=0", "CODE_FORGE_NO_ERROR_LOG=1", "NO_COLOR=1"}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			nodeCode := 0
			if err := cmd.Run(); err != nil {
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatal(err)
				}
				nodeCode = exit.ExitCode()
			}
			norm := func(s, repo string) string {
				return b462Stamp.ReplaceAllString(strings.ReplaceAll(s, repo, "<REPO>"), "<T>")
			}
			if goCode != nodeCode || norm(goOut, goRepo) != norm(stdout.String(), nodeRepo) || goErr != stderr.String() {
				t.Fatalf("go %d %q %q\nnode %d %q %q", goCode, goOut, goErr, nodeCode, stdout.String(), stderr.String())
			}
			goState, nodeState := b462Collect(t, goHome, goRepo), b462Collect(t, nodeHome, nodeRepo)
			goState.record, nodeState.record = b462Stamp.ReplaceAllString(goState.record, "<T>"), b462Stamp.ReplaceAllString(nodeState.record, "<T>")
			if !reflect.DeepEqual(goState, nodeState) {
				t.Fatalf("state differs\ngo   %+v\nnode %+v", goState, nodeState)
			}
		})
	}
}
