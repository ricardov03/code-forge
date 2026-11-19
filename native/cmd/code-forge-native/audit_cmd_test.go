package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/releaseaudit"
)

// b802Fake is a binary under audit that answers every command well, except
// `status --help` (a panic) and `worker <bad flag>` (exit 0). It also checks
// the sandbox it runs in: HOME, PATH stubs, blocked network, no Node.
const b802Fake = `#!/bin/sh
case "$HOME" in */code-forge-audit-*/homes/*) ;; *) echo "panic: HOME not in the sandbox: $HOME" >&2; exit 2 ;; esac
[ "$HTTPS_PROXY" = http://127.0.0.1:9 ] || { echo "panic: network not blocked" >&2; exit 2; }
[ "$CODE_FORGE_KEY_BACKEND" = file ] || { echo "panic: real key store" >&2; exit 2; }
case "$(command -v claude)" in */code-forge-audit-*/bin/claude) ;; *) echo "panic: claude is not the stub" >&2; exit 2 ;; esac
last=""
for a in "$@"; do last=$a; done
case "$*" in
"status --help") echo "panic: boom" >&2; echo "goroutine 1 [running]:" >&2; exit 2 ;;
"worker --code-forge-audit-no-such-flag") exit 0 ;;
esac
if [ "$last" = --help ]; then echo "usage: code-forge $1 [options]"; exit 0; fi
echo "code-forge: unknown option $last" >&2
exit 2
`

func b802WithSelf(t *testing.T, path string) {
	t.Helper()
	saved := auditSelf
	auditSelf = func() (string, error) { return path, nil }
	t.Cleanup(func() { auditSelf = saved })
}

// The audit runs every help-table command with each check in the sandbox,
// writes the Markdown report and one JSON line, and exits 1 on a high problem.
func TestB802AuditCommandsReportsEachCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// the audit's own exit 1 is not logged, so any file in HOME came from an audited command
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-code-forge")
	if err := os.WriteFile(fake, []byte(b802Fake), 0o755); err != nil {
		t.Fatal(err)
	}
	b802WithSelf(t, fake)
	out := filepath.Join(dir, "docs", "audits", "audit-9.0.0.md")
	code, stdout, stderr := runCaptured(t, "audit", "commands", "--json", "--out", out)
	if code != 1 || !strings.Contains(stderr, "1 high problem(s)") {
		t.Fatalf("exit %d stderr %q", code, stderr)
	}
	if strings.Count(stdout, "\n") != 1 {
		t.Fatalf("--json printed %q, want one line", stdout)
	}
	var got auditJSON
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatal(err)
	}
	commands := auditCommands()
	if got.Commands != len(commands) || got.Checks != len(commands)*len(auditChecks) || got.Out != out ||
		got.Problems[releaseaudit.High] != 1 || got.Problems[releaseaudit.Medium] != 1 {
		t.Fatalf("summary %+v (want %d commands)", got, len(commands))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	rows, ok := releaseaudit.Parse(string(data))
	if !ok || len(rows) != got.Checks {
		t.Fatalf("report rows %d ok=%v, want %d", len(rows), ok, got.Checks)
	}
	seen := map[string]int{}
	for _, row := range rows {
		seen[row.Command]++
		switch {
		case row.Command == "status" && row.Check == "help":
			if row.Result != releaseaudit.Problem || row.Severity != releaseaudit.High || !strings.HasPrefix(row.Detail, "panic: boom") {
				t.Errorf("status help row %+v", row)
			}
		case row.Command == "worker" && row.Check == "bad-flag":
			if row.Result != releaseaudit.Problem || row.Severity != releaseaudit.Medium {
				t.Errorf("worker bad-flag row %+v", row)
			}
		case row.Result != releaseaudit.OK:
			t.Errorf("unexpected problem %+v", row)
		}
	}
	for _, entry := range helpCommands {
		if !entry.hidden && seen[entry.name] != len(auditChecks) {
			t.Errorf("%q has %d rows, want %d", entry.name, seen[entry.name], len(auditChecks))
		}
	}
	// the gate reads this report: the open high problem stops a major release
	if fail := releaseaudit.Check(b802Repo{text: string(data)}, "9.0.0"); !strings.Contains(fail, "status help (no block)") {
		t.Fatalf("gate: %q", fail)
	}
	// nothing was written outside the sandbox: the caller's HOME stays empty
	filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			t.Errorf("audit wrote into the caller's HOME: %s", path)
		}
		return nil
	})
}

type b802Repo struct{ text string }

func (r b802Repo) Committed(string) (string, bool) { return r.text, true }
func (r b802Repo) OnDisk(string) bool              { return true }
func (r b802Repo) Landed(string) bool              { return false }

func TestB802AuditCommandsUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{{"audit"}, {"audit", "verbs"}, {"audit", "commands", "--out"}, {"audit", "commands", "--bogus"}} {
		code, stdout, stderr := runCaptured(t, args...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, auditUsage) {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, code, stdout, stderr)
		}
	}
	code, stdout, _ := runCaptured(t, "audit", "commands", "--help")
	if code != 0 || !strings.Contains(stdout, "code-forge audit commands [--json] [--out <file>]") || !strings.Contains(stdout, "--out <file>") {
		t.Fatalf("help: exit %d %q", code, stdout)
	}
}

// The real binary, built for the test, audits itself: no command panics,
// hangs or hands over on --help or an unknown flag.
func TestB802AuditCommandsRealBinary(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	t.Setenv("HOME", t.TempDir())
	b802WithSelf(t, b553Binary(t))
	out := filepath.Join(t.TempDir(), "audit.md")
	code, stdout, stderr := runCaptured(t, "audit", "commands", "--out", out)
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("exit %d stdout %q stderr %q: %v", code, stdout, stderr, err)
	}
	rows, _ := releaseaudit.Parse(string(data))
	if len(rows) != len(auditCommands())*len(auditChecks) {
		t.Fatalf("%d rows", len(rows))
	}
	for _, row := range rows {
		if strings.HasPrefix(row.Detail, "panic") || strings.HasPrefix(row.Detail, "did not start") || strings.Contains(row.Detail, "did not end") {
			t.Errorf("%+v", row)
		}
	}
	if !strings.Contains(stdout, "wrote "+out) {
		t.Fatalf("stdout %q", stdout)
	}
}
