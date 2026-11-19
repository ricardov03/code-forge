package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// b870Fixture is the customer's case (Laravel 13 + Pest, several coders):
// gates.test is [php, artisan, test], blocks A and B are open in one run.
// A owns app/Models/Invoice.php and tests/Feature/InvoiceTest.php; B has a
// half-done, failing edit in app/Models/Order.php. A fake php on PATH logs
// its argv and fails a run that tests Order while Order.php holds BROKEN.
type b870Fixture struct {
	root, repo, home, log, bin string
}

func newB870Fixture(t *testing.T, worktreeA bool) b870Fixture {
	t.Helper()
	root := t.TempDir()
	f := b870Fixture{root: root, repo: filepath.Join(root, "repo"), home: filepath.Join(root, "home"), log: filepath.Join(root, "php.log"), bin: filepath.Join(root, "bin")}
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write(filepath.Join(f.bin, "php"), `#!/bin/sh
{ echo "CALL $#"; for a in "$@"; do echo "$a"; done; } >> '`+f.log+`'
order=0
files=0
for a in "$@"; do
  case "$a" in
    tests/*) files=1 ;;
  esac
  case "$a" in
    tests/Feature/OrderTest.php) order=1 ;;
  esac
done
if [ "$files" = 0 ]; then order=1; fi
if [ "$order" = 1 ] && grep -q BROKEN app/Models/Order.php; then
  echo "FAILED Tests\Feature\OrderTest" >&2
  exit 1
fi
exit 0
`, 0o755)
	config, _ := json.Marshal(map[string]any{"gates": map[string]any{"test": []string{"php", "artisan", "test"}, "lint": nil, "types": nil, "format": nil, "rerun_failed": 0}})
	write(filepath.Join(f.repo, ".code-forge.yml"), string(config), 0o600)
	for name, body := range map[string]string{
		"artisan":                       "#!/usr/bin/env php\n<?php\n",
		"composer.json":                 `{"name":"acme/shop","require":{"laravel/framework":"^13.0"},"require-dev":{"pestphp/pest":"^4.0"}}`,
		"app/Models/Invoice.php":        "<?php\nclass Invoice {}\n",
		"app/Models/Order.php":          "<?php\nclass Order {}\n",
		"tests/Feature/InvoiceTest.php": "<?php\nit('totals', fn () => expect(true)->toBeTrue());\n",
		"tests/Feature/OrderTest.php":   "<?php\nit('ships', fn () => expect(true)->toBeTrue());\n",
	} {
		write(filepath.Join(f.repo, name), body, 0o600)
	}
	git(f.repo, "init", "-q", "-b", "main")
	git(f.repo, "add", ".")
	git(f.repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	base := git(f.repo, "rev-parse", "HEAD")
	blockA := map[string]any{"status": "open", "kind": "code", "base_branch": "main", "owned_files": []string{"app/Models/Invoice.php", "tests/Feature/InvoiceTest.php"}, "test_files": []string{}}
	aTree := f.repo
	if worktreeA {
		aTree = filepath.Join(root, "wt-A")
		git(f.repo, "worktree", "add", "-q", "-b", "cf/A", aTree, "main")
		real, _ := filepath.EvalSymlinks(aTree)
		blockA["worktree"] = map[string]any{"path": real, "branch": "cf/A", "base_sha": base}
		blockA["base_sha"] = base
	} else {
		git(f.repo, "checkout", "-qb", "work")
	}
	// Block A's own edit, in its tree; block B's failing edit in the main checkout.
	write(filepath.Join(aTree, "app/Models/Invoice.php"), "<?php\nclass Invoice { public int $total = 0; }\n", 0o600)
	write(filepath.Join(f.repo, "app/Models/Order.php"), "<?php\nclass Order { BROKEN }\n", 0o600)
	blockB := map[string]any{"status": "open", "kind": "code", "base_branch": "main", "owned_files": []string{"app/Models/Order.php", "tests/Feature/OrderTest.php"}, "test_files": []string{}}
	data, _ := json.Marshal(map[string]any{"run_id": "shop-run", "workspace": f.repo, "status": "active", "project": "shop", "blocks": map[string]any{"A": blockA, "B": blockB}})
	write(filepath.Join(f.home, ".code-forge", "runs", "shop-run.json"), string(data), 0o600)
	if worktreeA {
		f.repo = aTree
	}
	return f
}

func (f b870Fixture) gates(t *testing.T, flags ...string) (int, string, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(f.log)
	encoded, _ := json.Marshal(append([]string{"gates", "run", "--run", "shop-run", "--block", "A"}, flags...))
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(), "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"), "HOME="+f.home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_TEST_QUEUE=off", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded))
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
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

func (f b870Fixture) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.log)
	if err != nil {
		t.Fatalf("php never ran: %v", err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// lastDone is the last gate.done row of block A.
func (f b870Fixture) lastDone(t *testing.T) map[string]any {
	t.Helper()
	rows, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge", "ledger"), "shop")
	if err != nil {
		t.Fatal(err)
	}
	var done map[string]any
	for _, value := range rows {
		if row, ok := value.(map[string]any); ok && row["event"] == "gate.done" && row["block"] == "A" {
			done = row
		}
	}
	if done == nil {
		t.Fatalf("no gate.done row for block A in %d rows", len(rows))
	}
	return done
}

const b870SharedLine = "gates run: full suite in a shared tree (1 other open blocks); for this block's tests use --changed, or proof.isolation: worktree"

// --changed runs only block A's test while block B's failing edit sits in the
// same tree; the gate.done row names that one file and no shared tree.
func TestB870ChangedRunsOnlyTheBlockTests(t *testing.T) {
	f := newB870Fixture(t, false)
	code, stdout, stderr := f.gates(t, "--changed")
	if code != 0 {
		t.Fatalf("--changed exit %d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if got, want := f.calls(t), []string{"CALL 3", "artisan", "test", "tests/Feature/InvoiceTest.php"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("php calls=%q, want %q (only the block's test)", got, want)
	}
	if strings.Contains(stderr, "shared tree") {
		t.Fatalf("--changed printed the shared-tree line: %s", stderr)
	}
	row := f.lastDone(t)
	if files, _ := json.Marshal(row["test_files"]); string(files) != `["tests/Feature/InvoiceTest.php"]` {
		t.Fatalf("gate.done test_files=%s, want only tests/Feature/InvoiceTest.php: %v", files, row)
	}
	if _, has := row["shared_tree"]; has {
		t.Fatalf("gate.done has shared_tree under --changed: %v", row)
	}
}

// A plain gates run --block on the shared tree runs the whole suite, sees
// block B's edit, and says so with the way out; the row counts the blocks.
func TestB870FullSuiteSharedTreeLine(t *testing.T) {
	f := newB870Fixture(t, false)
	code, stdout, stderr := f.gates(t)
	if got, want := f.calls(t), []string{"CALL 2", "artisan", "test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("php calls=%q, want %q (the full suite)", got, want)
	}
	if code != 1 {
		t.Fatalf("exit %d, want 1 (block B's edit breaks the full suite)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if strings.Count(stderr, b870SharedLine+"\n") != 1 {
		t.Fatalf("stderr lacks the one shared-tree line %q:\n%s", b870SharedLine, stderr)
	}
	if n, _ := f.lastDone(t)["shared_tree"].(float64); n != 1 {
		t.Fatalf("gate.done shared_tree=%v, want 1: %v", f.lastDone(t)["shared_tree"], f.lastDone(t))
	}
}

// Under worktree isolation block A's tree does not hold block B's edit: the
// full suite there is green and no shared-tree line is printed.
func TestB870WorktreeIsolationHidesOtherBlock(t *testing.T) {
	f := newB870Fixture(t, true)
	code, stdout, stderr := f.gates(t)
	if code != 0 {
		t.Fatalf("worktree gate exit %d, want 0\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	if got, want := f.calls(t), []string{"CALL 2", "artisan", "test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("php calls=%q, want %q", got, want)
	}
	if strings.Contains(stderr, "shared tree") {
		t.Fatalf("worktree block printed the shared-tree line: %s", stderr)
	}
	if _, has := f.lastDone(t)["shared_tree"]; has {
		t.Fatalf("gate.done has shared_tree in a worktree: %v", f.lastDone(t))
	}
}

// b870InitHint is the proof.isolation hint init's JSON settings carry, and
// whether the summary log shows it.
func b870InitHint(t *testing.T, files map[string]string) (string, bool) {
	t.Helper()
	side := b527Go(t, []string{"--no-interaction", "--skip-doctor", "--no-jev"}, b527Files(files))
	if side.code != 0 {
		t.Fatalf("init exit %d: %#v", side.code, side)
	}
	var body struct {
		Settings map[string]struct {
			Value any
			Hint  string
		}
		LogTail []string `json:"log_tail"`
	}
	if err := json.Unmarshal([]byte(side.stdout), &body); err != nil {
		t.Fatalf("init JSON: %v: %s", err, side.stdout)
	}
	hint := body.Settings["proof.isolation"].Hint
	shown := false
	for _, line := range body.LogTail {
		if hint != "" && line == "    "+hint {
			shown = true
		}
	}
	return hint, shown
}

// init shows the worktree hint for a Laravel project and for review.workers
// above 1, and not once proof.isolation is worktree or for one coder.
func TestB870InitIsolationHint(t *testing.T) {
	laravel := map[string]string{"composer.json": `{"name":"acme/shop","require":{"laravel/framework":"^13.0"}}`, "artisan": ""}
	hint, shown := b870InitHint(t, laravel)
	if !shown || !strings.Contains(hint, "proof.isolation: worktree") || !strings.Contains(hint, "--changed") {
		t.Fatalf("laravel: hint %q shown=%v, want the worktree and --changed hint under the isolation line", hint, shown)
	}
	workers := map[string]string{"package.json": `{"name":"app"}`, ".code-forge.yml": "version: 1\nreview:\n  workers: 4\n"}
	if hint, shown := b870InitHint(t, workers); !shown || !strings.Contains(hint, "proof.isolation: worktree") {
		t.Fatalf("review.workers 4: hint %q shown=%v, want the worktree hint", hint, shown)
	}
	worktree := map[string]string{"composer.json": `{"name":"acme/shop"}`, "artisan": "", ".code-forge.yml": "version: 1\nreview:\n  workers: 4\nproof:\n  isolation: worktree\n"}
	if hint, _ := b870InitHint(t, worktree); hint != "" {
		t.Fatalf("isolation worktree: hint %q, want none", hint)
	}
	single := map[string]string{"package.json": `{"name":"app"}`, ".code-forge.yml": "version: 1\nreview:\n  workers: 1\n"}
	if hint, _ := b870InitHint(t, single); hint != "" {
		t.Fatalf("one worker: hint %q, want none", hint)
	}
}
