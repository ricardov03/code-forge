package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// preflightFixture is a run with one open worktree block B1 that changed its
// owned file sample.txt. Its gates.test command appends one line to argvLog,
// so an empty log proves no gate command ran.
type preflightFixture struct {
	t                         *testing.T
	home, repo, tree, argvLog string
	run, base                 string
}

func newPreflightFixture(t *testing.T) *preflightFixture {
	t.Helper()
	root := t.TempDir()
	f := &preflightFixture{t: t, home: filepath.Join(root, "home"), repo: filepath.Join(root, "repo"), tree: filepath.Join(root, "tree"), argvLog: filepath.Join(root, "argv.log"), run: "r-preflight"}
	for _, dir := range []string{f.home, f.repo} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	config := `{"gates":{"test":["/bin/sh","-c","printf '%s\n' \"$0\" >> ` + f.argvLog + `","ran"],"lint":null,"types":null,"format":null,"rerun_failed":0}}`
	f.write(f.repo, ".code-forge.yml", config)
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
	record := ledger.Object{{Key: "run_id", Value: f.run}, {Key: "project", Value: "preflight"}, {Key: "workspace", Value: f.repo},
		{Key: "status", Value: "active"}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}}
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = 37
	}
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", f.run+".key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	f.sign(ledger.Object{{Key: "event", Value: "dispatch"}, {Key: "block", Value: "B1"}, {Key: "level", Value: "L2"}})
	return f
}

func (f *preflightFixture) write(dir, name, text string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *preflightFixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitx.ChildEnv(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *preflightFixture) commit(dir, message string) {
	f.git(dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", message)
}

func (f *preflightFixture) sign(row ledger.Object) {
	f.t.Helper()
	if _, err := state.WriteSigned(f.home, f.run, row); err != nil {
		f.t.Fatal(err)
	}
}

// moveMain commits on main after the block opened and returns main's new tip.
func (f *preflightFixture) moveMain() string {
	f.write(f.repo, "other.txt", "landed\n")
	f.git(f.repo, "add", "other.txt")
	f.commit(f.repo, "landed elsewhere")
	return f.git(f.repo, "rev-parse", "HEAD")
}

// openFinding records a review of sample.txt's current content that leaves F1 open.
func (f *preflightFixture) openFinding() {
	hash, err := gitx.ContentHash(f.tree, "sample.txt")
	if err != nil {
		f.t.Fatal(err)
	}
	f.sign(ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.txt"}, {Key: "content_hash", Value: hash},
		{Key: "status", Value: "reviewed"}, {Key: "approved", Value: false}, {Key: "open", Value: []any{ledger.Object{{Key: "id", Value: "F1"}, {Key: "severity", Value: "warning"}}}}})
}

// call runs the public router in a fresh process (TestGatesProcess) with Node unavailable.
func (f *preflightFixture) call(args ...string) (code int, stdout, stderr string) {
	f.t.Helper()
	exe, err := os.Executable()
	if err != nil {
		f.t.Fatal(err)
	}
	encoded, _ := json.Marshal(args)
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(), "HOME="+f.home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_HANDOVER_LOG=0", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded))
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		e, ok := err.(*exec.ExitError)
		if !ok {
			f.t.Fatal(err)
		}
		code = e.ExitCode()
	}
	return code, out.String(), errOut.String()
}

// gateRuns is how many times the configured test command ran.
func (f *preflightFixture) gateRuns() int {
	data, err := os.ReadFile(f.argvLog)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return len(strings.Split(strings.TrimSpace(string(data)), "\n"))
}

type preflightReport struct {
	Block     string   `json:"block"`
	Preflight bool     `json:"preflight"`
	Ready     bool     `json:"ready"`
	Skipped   []string `json:"skipped"`
	Blockers  []struct {
		Code, Detail, Fix string
		File              *string
		Finding           string
	} `json:"blockers"`
}

func (f *preflightFixture) preflightJSON() (int, preflightReport) {
	f.t.Helper()
	code, stdout, stderr := f.call("block", "ready", "B1", "--run", f.run, "--preflight", "--json")
	var report preflightReport
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		f.t.Fatalf("preflight --json: exit %d, want one JSON line: %v\nstdout=%q\nstderr=%q", code, err, stdout, stderr)
	}
	return code, report
}

// B392: a block whose base is behind main is refused by the preflight in
// seconds with stale_base, and gates run never starts its test command.
func TestPreflightStaleBaseStopsBeforeAnyGate(t *testing.T) {
	f := newPreflightFixture(t)
	tip := f.moveMain()
	code, report := f.preflightJSON()
	if code != 1 || report.Block != "B1" || !report.Preflight || report.Ready || len(report.Blockers) != 1 {
		t.Fatalf("stale base: want exit 1 and exactly one blocker, got exit %d %+v", code, report)
	}
	b := report.Blockers[0]
	wantFix := "code-forge block rebase B1 --run " + f.run + " --accept-landed"
	if b.Code != "stale_base" || !strings.Contains(b.Detail, tip) || b.Fix != wantFix {
		t.Fatalf("stale base blocker: want code stale_base, detail with main tip %s and fix %q; got %+v", tip, wantFix, b)
	}
	gcode, stdout, stderr := f.call("gates", "run", "--run", f.run, "--block", "B1")
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if gcode != 1 || stdout != "" || len(lines) != 1 || !strings.Contains(lines[0], "no gate was run; fix: "+wantFix) {
		t.Fatalf("gates run on a stale base: want exit 1, no stdout, one stderr line with the fix; got exit %d stdout=%q stderr=%q", gcode, stdout, stderr)
	}
	if n := f.gateRuns(); n != 0 {
		t.Fatalf("gates run on a stale base ran the test command %d times, want 0", n)
	}
}

// B392: a clean block passes the preflight without any gate result, and an
// owned file that has no review yet is not a preflight blocker.
func TestPreflightCleanBlockExitsZero(t *testing.T) {
	f := newPreflightFixture(t)
	code, stdout, stderr := f.call("block", "ready", "B1", "--run", f.run, "--preflight")
	if code != 0 || stdout != "block B1 preflight clean\n" || stderr != "" {
		t.Fatalf("clean preflight: want exit 0 and exactly one clean line; got exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	gcode, _, gerr := f.call("gates", "run", "--run", f.run, "--block", "B1")
	if gcode != 0 || f.gateRuns() != 1 {
		t.Fatalf("gates run on a clean block: want exit 0 and the test command run once; got exit %d, %d runs, stderr=%q", gcode, f.gateRuns(), gerr)
	}
}

// B392: an open review finding on the current content is a preflight blocker
// for block ready and stops gates run before its test command; --no-preflight
// skips the check.
func TestPreflightOpenFindingIsListed(t *testing.T) {
	f := newPreflightFixture(t)
	f.openFinding()
	code, report := f.preflightJSON()
	if code != 1 || report.Ready || len(report.Blockers) != 1 {
		t.Fatalf("open finding: want exit 1 and exactly one blocker, got exit %d %+v", code, report)
	}
	b := report.Blockers[0]
	if b.Code != "not_approved" || b.File == nil || *b.File != "sample.txt" || b.Finding != "F1" {
		t.Fatalf("open finding blocker: want not_approved on sample.txt finding F1, got %+v", b)
	}
	gcode, stdout, stderr := f.call("gates", "run", "--run", f.run, "--block", "B1")
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if gcode != 1 || stdout != "" || len(lines) != 1 || !strings.Contains(lines[0], "not_approved") || !strings.Contains(lines[0], "no gate was run; fix: ") {
		t.Fatalf("gates run with an open finding: want exit 1 and one not_approved line; got exit %d stdout=%q stderr=%q", gcode, stdout, stderr)
	}
	if n := f.gateRuns(); n != 0 {
		t.Fatalf("gates run with an open finding ran the test command %d times, want 0", n)
	}
	gcode, _, stderr = f.call("gates", "run", "--run", f.run, "--block", "B1", "--no-preflight")
	if gcode != 0 || f.gateRuns() != 1 {
		t.Fatalf("gates run --no-preflight: want exit 0 and one test run; got exit %d, %d runs, stderr=%q", gcode, f.gateRuns(), stderr)
	}
}
