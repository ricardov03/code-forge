package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/healq"
)

// healFixture is a run "fixture" with one open block B1 in a temp HOME and a
// call helper that runs the public router in a child (TestGatesProcess).
func healFixture(t *testing.T) (repo, home string, call func(...string) (int, string, string)) {
	t.Helper()
	root := t.TempDir()
	repo, home = filepath.Join(root, "repo"), filepath.Join(root, "home")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(repo, 0755))
	must(os.MkdirAll(filepath.Join(home, ".code-forge", "runs"), 0700))
	config := `{"gates":{"test":["/bin/sh","-c","printf green"],"lint":null,"types":null,"format":null,"rerun_failed":0}}`
	must(os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte(config), 0600))
	record := map[string]any{"run_id": "fixture", "workspace": repo, "status": "active", "project": "fixture", "blocks": map[string]any{"B1": map[string]any{"status": "open", "kind": "code", "owned_files": []string{}}}}
	data, _ := json.Marshal(record)
	must(os.WriteFile(filepath.Join(home, ".code-forge", "runs", "fixture.json"), data, 0600))
	exe, err := os.Executable()
	must(err)
	call = func(args ...string) (int, string, string) {
		t.Helper()
		encoded, _ := json.Marshal(args)
		cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
		cmd.Dir = repo
		cmd.Env = append(envWithoutGit(), "HOME="+home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded))
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			e, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatal(err)
			}
			code = e.ExitCode()
		}
		return code, stdout.String(), stderr.String()
	}
	return repo, home, call
}

// B429: gates run with a held run lock still fails, and leaves exactly one
// open lock item that stores its own argv and cwd.
func TestGatesRunHeldLockQueuesOneHealItem(t *testing.T) {
	repo, home, call := healFixture(t)
	lock := filepath.Join(home, ".code-forge", "runs", "fixture.lock")
	if err := os.Mkdir(lock, 0755); err != nil {
		t.Fatal(err)
	}
	argv := []string{"gates", "run", "--run", "fixture", "--block", "B1"}
	code, _, stderr := call(argv...)
	if code == 0 {
		t.Fatalf("gates run with a held lock exit 0, want non-zero: %s", stderr)
	}
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	last := lines[len(lines)-1]
	m := regexp.MustCompile(`^heal: queued (h\S+)$`).FindStringSubmatch(last)
	if m == nil || !strings.Contains(stderr, "run fixture is locked") {
		t.Fatalf("stderr must name the lock and end with `heal: queued <id>`:\n%s", stderr)
	}
	items, err := healq.Load(home, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("queue has %d items, want 1: %+v", len(items), items)
	}
	it := items[0]
	real, _ := filepath.EvalSymlinks(repo)
	cwd, _ := filepath.EvalSymlinks(it.Cwd)
	if it.ID != m[1] || it.Status != healq.StatusOpen || it.Kind != healq.KindLock || it.Block != "B1" || it.Run != "fixture" || it.Tries != 0 || !reflect.DeepEqual(it.Argv, argv) || cwd != real {
		t.Fatalf("item = %+v, want open lock item %s for fixture/B1 with argv %q in %s", it, m[1], argv, real)
	}
	if !strings.Contains(it.Detail, lock) {
		t.Fatalf("detail must keep the lock path %s: %q", lock, it.Detail)
	}
	// A green gates run queues nothing.
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if code, _, stderr := call(argv...); code != 0 || strings.Contains(stderr, "heal:") {
		t.Fatalf("green gates run: exit %d stderr %q, want 0 and no heal line", code, stderr)
	}
	if items, _ := healq.Load(home, "fixture"); len(items) != 1 {
		t.Fatalf("queue has %d items after a green run, want still 1", len(items))
	}
}

// B429: a second heal run on the same run stops at once with a busy message.
func TestHealRunSecondIsBusy(t *testing.T) {
	_, home, call := healFixture(t)
	t.Setenv("HOME", home)
	release, err := healq.Acquire("fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	code, stdout, stderr := call("heal", "run", "--run", "fixture", "--once")
	want := "heal run: busy: another heal run (pid " + strconv.Itoa(os.Getpid()) + ") is healing run fixture; this one stops\n"
	if code != 1 || stderr != want || stdout != "" {
		t.Fatalf("second heal run: exit %d stdout %q stderr %q, want exit 1 and %q", code, stdout, stderr, want)
	}
	release()
	code, stdout, stderr = call("heal", "run", "--run", "fixture", "--once")
	if code != 0 || stdout != "heal run fixture: 0 healed, 0 need a coder\n" {
		t.Fatalf("free heal run: exit %d stdout %q stderr %q, want 0 and the summary", code, stdout, stderr)
	}
}
