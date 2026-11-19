package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/procguard"
)

// B884, the customer's case end to end (Laravel + Pest, 0.25.1): a worker
// launched by `bash -c 'code-forge run start --add-worker --run <r>'` met a
// ticket Go cannot serve and died in silence; the ticket stayed queued, so
// every new worker died on it again (the poison loop). Here the built binary
// serves one block with a good file and a file Go cannot review (not UTF-8):
// the worker stays alive, the bad ticket is closed as unsupported with the
// signed row, the good one is approved, block ready names the reason and the
// fix; a forced stop leaves the signed worker.exit row and the worker log,
// and block ready's worker.down line names that exit and --add-worker.

const b884Claude = `#!/bin/sh
in=$(cat)
heads=$(printf '%s\n' "$in" | grep -E '^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@' | sed -E 's/^(@@ -[0-9,]+ \+[0-9,]+ @@).*/\1/' | awk '!s[$0]++' | sed 's/.*/"&"/' | paste -sd, -)
printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[]},"usage":{"input_tokens":1000,"output_tokens":300}}'
`

const b884Config = `version: 1
project:
  name: repo
  slug: repo
provider: anthropic
levels:
  L0:
    model: claude-haiku-4-5-20251001
  L1:
    model: claude-sonnet-5
  L2:
    model: claude-opus-5-5
  L3:
    model: claude-fable-5-2
caps:
  coders: 1
review:
  multimodel: false
engine: auto
harnesses:
  - claude
system1:
  fallback: rules
gates:
  test: null
  lint: null
  types: null
  format: null
proof:
  tiers:
    high:
      paths: []
  isolation: export
  export:
    link_dirs: []
    copy_untracked: []
autonomy:
  coder_may_push: false
  coder_may_open_draft_pr: false
`

type b884Fix struct {
	dir, home, repo, bin, tmp string
	env                       []string
}

// b884Sh runs script with bash in the repo; it returns exit code, stdout and
// stderr.
// initFiles writes what `code-forge init --no-jev` writes: the config and
// the .gitignore line .code-forge/ (B828: run start refuses without it).
func (f b884Fix) initFiles(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.repo, ".code-forge.yml"), []byte(b884Config), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.repo, ".gitignore"), []byte(".code-forge/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f b884Fix) sh(t *testing.T, script string) (int, string, string) {
	t.Helper()
	cmd := exec.Command("/bin/bash", "-c", script)
	cmd.Dir = f.repo
	cmd.Env = f.env
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
	return code, stdout.String(), stderr.String()
}

func (f b884Fix) must(t *testing.T, script string) string {
	t.Helper()
	code, out, errText := f.sh(t, script)
	if code != 0 {
		t.Fatalf("%s = %d\n%s\n%s", script, code, out, errText)
	}
	return out
}

// b884Workers is the pids of the run's live Go workers (ps).
func b884Workers(run string) []int {
	out, _ := exec.Command("pgrep", "-f", "worker --run "+run).Output()
	pids := []int{}
	for _, field := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(field); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids
}

func b884Ledger(t *testing.T, home string) []map[string]any {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(home, ".code-forge", "ledger", "*.jsonl"))
	rows := []map[string]any{}
	for _, file := range files {
		data, _ := os.ReadFile(file)
		for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			var row map[string]any
			if len(line) > 0 && json.Unmarshal(line, &row) == nil {
				rows = append(rows, row)
			}
		}
	}
	return rows
}

func TestB884CustomerPoisonTicketKeepsTheWorker(t *testing.T) {
	binary := b553Binary(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := b884Fix{dir: dir, home: filepath.Join(dir, "home"), repo: filepath.Join(dir, "repo"), bin: filepath.Join(dir, "bin"), tmp: filepath.Join(dir, "tmp")}
	for _, d := range []string{f.home, f.repo, f.bin, f.tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.bin, "claude"), []byte(b884Claude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.bin, "code-forge"), []byte("#!/bin/sh\nexec '"+binary+"' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.env = []string{"PATH=" + f.bin + ":/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + f.home, "TMPDIR=" + f.tmp, "CODE_FORGE_TMP_ROOT=" + f.tmp,
		"CODE_FORGE_KEY_BACKEND=file", "CODE_FORGE_KEY_JEV=", "CODE_FORGE_NODE=" + filepath.Join(dir, "missing-node"), "CODE_FORGE_HANDOVER_LOG=1",
		"GIT_CONFIG_GLOBAL=/dev/null", procguard.Env + "=" + os.Getenv(procguard.Env)}
	run := ""
	t.Cleanup(func() {
		if run == "" {
			return
		}
		for _, pid := range b884Workers(run) {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
		deadline := time.Now().Add(30 * time.Second)
		for len(b884Workers(run)) > 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		for _, pid := range b884Workers(run) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("worker %d left", pid)
		}
	})

	gitc := "git -c user.name=t -c user.email=t@t -c commit.gpgsign=false"
	f.must(t, "git init -q -b main && echo hello > a.txt && echo one > b.txt && "+gitc+" add -A && "+gitc+" commit -q -m base")
	// The config `code-forge init --no-jev` writes (init needs the npm
	// package layout, which a test-built binary has not).
	f.initFiles(t)
	f.must(t, gitc+" add -A && "+gitc+" commit -q -m cfg")
	out := f.must(t, "code-forge run start 2>&1")
	m := regexp.MustCompile(`run (\S+) started`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("run start: %s", out)
	}
	run = m[1]
	if err := os.WriteFile(filepath.Join(dir, "acc.json"), []byte(`[{"clause":"a","tests":["t1"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	f.must(t, "code-forge block open B1 --run "+run+" --level L1 --owned a.txt --owned b.txt --acceptance "+filepath.Join(dir, "acc.json"))
	// The first worker is gone, as in the report; the customer adds one from
	// a shell that exits at once.
	for _, pid := range b884Workers(run) {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	for deadline := time.Now().Add(30 * time.Second); len(b884Workers(run)) > 0; {
		if time.Now().After(deadline) {
			t.Fatal("the first worker did not stop")
		}
		time.Sleep(50 * time.Millisecond)
	}
	f.must(t, "bash -c 'code-forge run start --add-worker --run "+run+"'")
	workers := b884Workers(run)
	if len(workers) != 1 {
		t.Fatalf("workers after --add-worker: %v", workers)
	}
	pid := workers[0]

	// One good file, one file Go cannot review (not UTF-8), the bad one first.
	f.must(t, `echo world >> a.txt; printf 'caf\351\n' >> b.txt`)
	f.must(t, "code-forge review-file b.txt --block B1 --run "+run+" >/dev/null")
	f.must(t, "code-forge review-file a.txt --block B1 --run "+run+" >/dev/null")
	queue := filepath.Join(f.repo, ".code-forge", "queue")
	dones := func() []string { d, _ := filepath.Glob(filepath.Join(queue, "*.done")); return d }
	for deadline := time.Now().Add(120 * time.Second); len(dones()) < 2; {
		if time.Now().After(deadline) {
			t.Fatalf("done markers %v", dones())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := b884Workers(run); len(got) != 1 || got[0] != pid {
		t.Fatalf("worker %d after the unsupported ticket: live %v", pid, got)
	}
	statuses := map[string]string{}
	for _, file := range dones() {
		data, _ := os.ReadFile(file)
		var done map[string]any
		_ = json.Unmarshal(data, &done)
		status, _ := done["status"].(string)
		reason, _ := done["reason"].(string)
		statuses[status] = reason
	}
	if !strings.Contains(statuses["unsupported"], "not UTF-8") || len(statuses) != 2 {
		t.Fatalf("done statuses %v", statuses)
	}
	rows := b884Ledger(t, f.home)
	unsupported, approved := 0, 0
	for _, row := range rows {
		if row["event"] == "review.unsupported" && row["file"] == "b.txt" && row["block"] == "B1" && row["run"] == run && strings.Contains(row["reason"].(string), "not UTF-8") && row["mac"] != nil {
			unsupported++
		}
		if row["event"] == "review.approved" && row["file"] == "a.txt" {
			approved++
		}
	}
	if unsupported != 1 || approved != 1 {
		t.Fatalf("review.unsupported rows %d, a.txt approved rows %d", unsupported, approved)
	}
	_, ready, readyErr := f.sh(t, "code-forge block ready B1 --run "+run)
	text := ready + readyErr
	if !strings.Contains(text, "unreviewed b.txt") || !strings.Contains(text, "the Go worker could not review it: ") || !strings.Contains(text, "not UTF-8") ||
		!strings.Contains(text, "fix: code-forge review-file b.txt --block B1 --run "+run+" once the cause is fixed") || strings.Contains(text, "worker.down") {
		t.Fatalf("block ready with a live worker:\n%s", text)
	}

	// A forced stop: the signed worker.exit row, the worker log, and block
	// ready's worker.down line with the reason and the add-worker fix.
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(30 * time.Second); len(b884Workers(run)) > 0; {
		if time.Now().After(deadline) {
			t.Fatal("the worker did not stop on SIGTERM")
		}
		time.Sleep(50 * time.Millisecond)
	}
	exits := 0
	for _, row := range b884Ledger(t, f.home) {
		who, _ := row["worker"].(map[string]any)
		if row["event"] == "worker.exit" && row["run"] == run && who["pid"] == float64(pid) && row["reason"] == "signal" && row["detail"] == "stopped by signal terminated" && row["mac"] != nil {
			exits++
		}
	}
	if exits != 1 {
		t.Fatalf("%d worker.exit rows for the stopped worker", exits)
	}
	log, err := os.ReadFile(filepath.Join(f.repo, ".code-forge", "logs", "worker-"+strconv.Itoa(pid)+".log"))
	if err != nil || len(bytes.TrimSpace(log)) == 0 {
		t.Fatalf("worker log: %q %v", log, err)
	}
	_, ready, readyErr = f.sh(t, "code-forge block ready B1 --run "+run)
	text = ready + readyErr
	if !strings.Contains(text, "worker.down") || !strings.Contains(text, "last worker exit: signal (stopped by signal terminated)") ||
		!strings.Contains(text, "fix: code-forge run start --add-worker --run "+run) {
		t.Fatalf("block ready with the worker gone:\n%s", text)
	}
}
