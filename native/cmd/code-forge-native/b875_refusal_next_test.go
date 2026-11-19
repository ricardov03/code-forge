package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/worker"
)

// B875 (B801 audit P08): each bare refusal of the audit ends with one
// `next:` line naming the command to run. The samples are the audit's
// exact commands (plans/tools/forge/p4/b801-audit.md).

func b875Env(t *testing.T) (home, dir string) {
	t.Helper()
	home = b613Env(t)
	dir = t.TempDir()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	b699Chdir(t, dir)
	return home, dir
}

func TestB875AuditRefusalsPrintNext(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		code   int
		stderr string
	}{
		{"run-status-missing", []string{"run", "status", "--run", "r-missing"}, 1, "run status: no run record for r-missing\nnext: code-forge status\n"},
		{"block-open-norun", []string{"block", "open", "B1", "--run", "r-missing", "--owned", "src/a.txt", "--acceptance", "plans/acc.yml", "--json"}, 1, "block open: no run record for r-missing\nnext: code-forge status\n"},
		{"report", []string{"report", "--slug", "repon"}, 1, "report: no ledger for slug \"repon\"; known slugs: none\nnext: code-forge run start\n"},
		{"report-json", []string{"report", "--slug", "repon", "--json"}, 1, "report: no ledger for slug \"repon\"; known slugs: none\nnext: code-forge run start\n"},
		{"ledger-tail", []string{"ledger", "tail", "--slug", "repon"}, 1, "ledger tail: no ledger for slug \"repon\"; known slugs: none\nnext: code-forge run start\n"},
		{"ledger-calibration", []string{"ledger", "calibration", "--slug", "repon"}, 1, "ledger calibration: no ledger for slug \"repon\"; known slugs: none\nnext: code-forge run start\n"},
		{"heal-show", []string{"heal", "show", "h-missing"}, 1, "heal: no item h-missing\nnext: code-forge heal list\n"},
		{"plan-check-missing", []string{"plan", "check", "nope.md"}, 2, "plan: the plan file cannot be read: nope.md\nnext: code-forge plan check <path of a readable plan file>\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b875Env(t)
			code, _, stderr := runCaptured(t, tc.args...)
			if code != tc.code || stderr != tc.stderr {
				t.Fatalf("%v: exit %d (want %d)\nstderr %q\nwant   %q", tc.args, code, tc.code, stderr, tc.stderr)
			}
		})
	}
}

// run end --json is the audit's exact sample; its stdout belongs to the
// --json work (B871-B874), so only the refusal on stderr is checked.
func TestB875RunEndMissingPrintsNext(t *testing.T) {
	b875Env(t)
	code, _, stderr := runCaptured(t, "run", "end", "--run", "r-missing", "--json")
	if code != 1 || stderr != "run end: no run record for r-missing\nnext: code-forge status\n" {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestB875LedgerKnownSlugNamesIt(t *testing.T) {
	home, _ := b875Env(t)
	dir := filepath.Join(home, ".code-forge", "ledger")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha.jsonl"), []byte(""), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCaptured(t, "report", "--slug", "repon")
	if code != 1 || stderr != "report: no ledger for slug \"repon\"; known slugs: alpha\nnext: code-forge report --slug alpha\n" {
		t.Fatalf("report: exit %d stderr %q", code, stderr)
	}
	code, _, stderr = runCaptured(t, "ledger", "tail", "--slug", "repon")
	if code != 1 || stderr != "ledger tail: no ledger for slug \"repon\"; known slugs: alpha\nnext: code-forge ledger tail --slug alpha\n" {
		t.Fatalf("ledger tail: exit %d stderr %q", code, stderr)
	}
}

func TestB875HealShowWithRunNamesTheRun(t *testing.T) {
	b875Env(t)
	code, _, stderr := runCaptured(t, "heal", "show", "h-missing", "--run", "r1")
	if code != 1 || stderr != "heal: no item h-missing\nnext: code-forge heal list --run r1\n" {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestB875ValidateMissingConfig(t *testing.T) {
	_, dir := b875Env(t)
	// o-validate: no config in the folder
	code, _, stderr := runCaptured(t, "validate")
	want := "[code-forge:error] not-found: no config file at " + filepath.Join(dir, ".code-forge.yml") + "\nnext: code-forge init\n"
	if code != 1 || stderr != want {
		t.Fatalf("validate: exit %d stderr %q want %q", code, stderr, want)
	}
	// validate-missing without a default config: init too
	code, _, stderr = runCaptured(t, "validate", "--file", "nope.yml")
	want = "[code-forge:error] not-found: no config file at " + filepath.Join(dir, "nope.yml") + "\nnext: code-forge init\n"
	if code != 1 || stderr != want {
		t.Fatalf("validate --file: exit %d stderr %q", code, stderr)
	}
	// validate-missing in a project with a config: validate that one
	if err := os.WriteFile(filepath.Join(dir, ".code-forge.yml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = runCaptured(t, "validate", "--file", "nope.yml")
	want = "[code-forge:error] not-found: no config file at " + filepath.Join(dir, "nope.yml") + "\nnext: code-forge validate\n"
	if code != 1 || stderr != want {
		t.Fatalf("validate --file with a config: exit %d stderr %q", code, stderr)
	}
}

func TestB875KeysTestMissingNamesSet(t *testing.T) {
	_, project := b456KeysHome(t, "", nil)
	code, _, stderr := b456Keys(t, project, "test", "jev")
	if code != 1 || !strings.HasSuffix(stderr, "\njev: not found\nnext: code-forge keys set jev\n") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

func TestB875PlanFactsMissingNamesFacts(t *testing.T) {
	_, dir := b875Env(t)
	if err := os.WriteFile(filepath.Join(dir, "p.md"), []byte("# plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCaptured(t, "plan", "check", "p.md", "--facts", "f.md")
	if code != 2 || stderr != "plan: --facts cannot be read: f.md\nnext: code-forge plan check p.md --facts <path of a readable facts sheet>\n" {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
}

// The error log keeps the refusal itself as the message (and fingerprint),
// not its next line.
func TestB875ErrorLogKeepsTheRefusal(t *testing.T) {
	home, _ := b875Env(t)
	if code, _, _ := runCaptured(t, "ledger", "tail", "--slug", "repon"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	message, _, _ := b699LastEntry(t, home)
	if message != "ledger tail: no ledger for slug \"repon\"; known slugs: none" {
		t.Fatalf("logged message %q", message)
	}
	if code, _, _ := runCaptured(t, "run", "status", "--run", "r-missing"); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if message, _, _ = b699LastEntry(t, home); message != "run status: no run record for r-missing" {
		t.Fatalf("logged message %q", message)
	}
}

// worker-once: the refusal names the holder and how to see it.
func TestB875WorkerRefusalNext(t *testing.T) {
	for _, tc := range []struct {
		err  worker.Error
		want string
	}{
		{worker.Error{HolderPid: "5151", HolderRun: "r0"}, "worker: held by pid 5151 (run r0)\nnext: code-forge run status --run r0\n"},
		{worker.Error{HolderPid: "5151"}, "worker: held by pid 5151\nnext: code-forge status\n"},
		{worker.Error{HolderFile: "/q/workers/x.json"}, "worker: held by an unreadable entry /q/workers/x.json\nnext: code-forge status\n"},
		{worker.Error{}, "next: code-forge status\n"},
	} {
		if got := workerRefusalNext(&tc.err); got != tc.want {
			t.Errorf("%+v: %q want %q", tc.err, got, tc.want)
		}
	}
}
