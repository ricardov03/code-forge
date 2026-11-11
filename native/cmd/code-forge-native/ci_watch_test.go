package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/healq"
)

// ciFixture: a repo with a merge commit whose subjects name B501 and B502, a
// fake gh first on PATH that answers from files in $HOME, and fast polling.
type ciFixture struct {
	t               *testing.T
	home, repo, sha string
	stdout, stderr  string
}

func newCIFixture(t *testing.T, runs []string, log string) *ciFixture {
	t.Helper()
	f := &ciFixture{t: t, home: t.TempDir(), repo: t.TempDir()}
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = f.repo, gitx.ChildEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-qb", "main")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "f@example.invalid")
	git("config", "commit.gpgsign", "false")
	git("commit", "-q", "--allow-empty", "-m", "B400: base")
	git("checkout", "-qb", "side")
	git("commit", "-q", "--allow-empty", "-m", "B502: side work")
	git("checkout", "-q", "main")
	git("merge", "-q", "--no-ff", "-m", "Merge B501 lane", "side")
	f.sha = git("rev-parse", "HEAD")
	bin := filepath.Join(f.home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	// Each `gh run list` call answers with the next runs-<n> file (the last repeats).
	gh := `#!/bin/sh
printf '%s\n' "$*" >> "$HOME/gh-calls"
case "$1 $2" in
"run list")
  n=$(cat "$HOME/list-n" 2>/dev/null || echo 0)
  if [ -f "$HOME/runs-$((n+1))" ]; then n=$((n+1)); echo "$n" > "$HOME/list-n"; fi
  cat "$HOME/runs-$n" ;;
"run view") cat "$HOME/log-$3" ;;
*) exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(gh), 0o700); err != nil {
		t.Fatal(err)
	}
	for i, runs := range runs {
		f.write("runs-"+string(rune('1'+i)), runs)
	}
	f.write("runs-0", "[]")
	if log != "" {
		f.write("log-77", log)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	poll, noRun, sleep := ciPollInterval, ciNoRunAfter, ciSleep
	ciPollInterval, ciNoRunAfter = time.Millisecond, 50*time.Millisecond
	ciSleep = func(time.Duration) { time.Sleep(5 * time.Millisecond) }
	t.Cleanup(func() { ciPollInterval, ciNoRunAfter, ciSleep = poll, noRun, sleep })
	return f
}

func (f *ciFixture) write(name, content string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.home, name), []byte(content), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// watch runs the public route the way the binary does: run(["ci", ...]).
func (f *ciFixture) watch(extra ...string) int {
	f.t.Helper()
	out, errOut := filepath.Join(f.home, "stdout"), filepath.Join(f.home, "stderr")
	o, _ := os.Create(out)
	e, _ := os.Create(errOut)
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = o, e
	code, _ := run(append([]string{"ci", "watch", "--sha", f.sha, "--run", "r-ci-unit", "--cwd", f.repo}, extra...))
	os.Stdout, os.Stderr = stdout, stderr
	_, _ = o.Close(), e.Close()
	a, _ := os.ReadFile(out)
	b, _ := os.ReadFile(errOut)
	f.stdout, f.stderr = string(a), string(b)
	return code
}

func TestCIWatchSuccessExitsZero(t *testing.T) {
	f := newCIFixture(t, []string{
		`[{"databaseId":77,"status":"in_progress","conclusion":"","name":"CI"}]`,
		`[{"databaseId":77,"status":"completed","conclusion":"success","name":"CI"},{"databaseId":78,"status":"completed","conclusion":"skipped","name":"Docs"}]`,
	}, "")
	if code := f.watch(); code != 0 {
		t.Fatalf("exit=%d, want 0; stdout=%q stderr=%q", code, f.stdout, f.stderr)
	}
	if f.stdout != "ci watch: CI passed for "+f.sha+" (2 runs)\n" {
		t.Errorf("stdout=%q", f.stdout)
	}
	if items := queuedHeal(t, f.home, "r-ci-unit"); len(items) != 0 {
		t.Errorf("heal items=%d, want 0 on success", len(items))
	}
	calls, _ := os.ReadFile(filepath.Join(f.home, "gh-calls"))
	if want := "run list --commit " + f.sha + " --json databaseId,status,conclusion,name\n"; strings.Count(string(calls), want) != 2 {
		t.Errorf("gh calls=%q, want the run list twice (in progress, then done)", calls)
	}
}

func TestCIWatchFailureQueuesHealItem(t *testing.T) {
	log := "test (ubuntu)\tRun go test\t2026-11-11T10:00:00Z --- FAIL: TestLandPush (0.20s)\n" +
		"test (ubuntu)\tRun node tests\t2026-11-11T10:00:01Z not ok 3 - land pushes main\n"
	f := newCIFixture(t, []string{`[{"databaseId":77,"status":"completed","conclusion":"failure","name":"CI"}]`}, log)
	if code := f.watch(); code != 1 {
		t.Fatalf("exit=%d, want 1; stdout=%q stderr=%q", code, f.stdout, f.stderr)
	}
	items := queuedHeal(t, f.home, "r-ci-unit")
	if len(items) != 1 {
		t.Fatalf("heal items=%d, want 1", len(items))
	}
	item := items[0]
	want := "CI failed for " + f.sha + "; jobs: CI / test (ubuntu); tests: TestLandPush, land pushes main"
	if item.Kind != healq.KindCI || item.Status != healq.StatusOpen || item.Tries != 0 || item.Block != "B501,B502" || item.Detail != want || item.Cwd != f.repo {
		t.Errorf("heal item=%+v, want kind ci, block B501,B502, cwd the repo and detail %q", item, want)
	}
	if wantArgv := "ci watch --sha " + f.sha + " --cwd " + f.repo + " --run r-ci-unit"; strings.Join(item.Argv, " ") != wantArgv {
		t.Errorf("argv=%q, want %q", item.Argv, wantArgv)
	}
	if !strings.Contains(f.stdout, "heal: queued "+item.ID+"\n") {
		t.Errorf("stdout must name the heal item: %q", f.stdout)
	}
}

func TestCIWatchNoRunExitsTwo(t *testing.T) {
	f := newCIFixture(t, nil, "")
	if code := f.watch(); code != 2 {
		t.Fatalf("exit=%d, want 2; stdout=%q stderr=%q", code, f.stdout, f.stderr)
	}
	if f.stdout != "ci watch: no CI run for "+f.sha+"\n" {
		t.Errorf("stdout=%q, want the no-run line", f.stdout)
	}
	if items := queuedHeal(t, f.home, "r-ci-unit"); len(items) != 0 {
		t.Errorf("heal items=%d, want 0 when CI never started", len(items))
	}
}
