package main

// B802: a major release needs a committed command audit (docs/audits/audit-<version>.md, written
// by `code-forge audit commands`) with no open high problem. Patch and minor are not affected;
// --no-checks does not skip the gate, --allow-no-audit does (with a loud warning).

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/releaseaudit"
)

func b802Audit(rows ...releaseaudit.Row) string {
	return releaseaudit.Render(releaseaudit.Report{Version: "0.21.0", Commit: "abc", Date: "2026-11-12T00:00:00Z", Rows: rows})
}

func b802Commit(f *fixture, file, body, msg string) {
	f.write(filepath.Join(f.repo, file), body)
	f.git(f.repo, "add", "-A")
	f.git(f.repo, "commit", "-q", "-m", msg)
}

// b802CleanAudit runs prep (when given), then commits a clean audit for 1.0.0,
// so a major-release golden still shows the whole release.
func b802CleanAudit(prep func(f *fixture)) func(f *fixture) {
	return func(f *fixture) {
		if prep != nil {
			prep(f)
		}
		b802Commit(f, "docs/audits/audit-1.0.0.md", b802Audit(
			releaseaudit.Row{Command: "status", Check: "help", Result: releaseaudit.OK}), "docs: command audit 1.0.0")
	}
}

func TestB802MajorDryRunNeedsAudit(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	r := f.runGo(parityCase{args: []string{"release", "major", "--dry-run", "--date", date}})
	want := "a major release needs the command audit docs/audits/audit-1.0.0.md: run `code-forge audit commands --out docs/audits/audit-1.0.0.md`"
	if r.code != 1 || !strings.Contains(r.stdout, "FAIL  "+want) || !strings.Contains(r.stderr, "REFUSED: a real run would stop") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
	// an explicit x.y.z that starts a new major is a major release too
	r = f.runGo(parityCase{args: []string{"release", "2.0.0", "--dry-run", "--date", date}})
	if r.code != 1 || !strings.Contains(r.stdout, "audit-2.0.0.md") {
		t.Fatalf("explicit 2.0.0: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	// a real run refuses before it writes anything
	r = f.runGo(parityCase{args: []string{"release", "major", "--date", date}})
	if r.code != 1 || !strings.Contains(r.stderr, "REFUSED: "+want) || strings.Contains(f.git(f.repo, "tag", "--list"), "v1.0.0") {
		t.Fatalf("real run: exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
}

func TestB802MajorWithAuditPasses(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	b802Commit(f, "docs/audits/audit-1.0.0.md", b802Audit(
		releaseaudit.Row{Command: "status", Check: "help", Result: releaseaudit.OK},
		releaseaudit.Row{Command: "worker", Check: "bad-flag", Result: releaseaudit.Problem, Severity: releaseaudit.Medium, Detail: "timed out"},
		releaseaudit.Row{Command: "run status", Check: "help", Result: releaseaudit.Problem, Severity: releaseaudit.High, Block: "B900"},
	), "docs: audit")
	b802Commit(f, "docs/guide/start.md", "# Start\n\nfixed\n", "B900: fix run status help")
	r := f.runGo(parityCase{args: []string{"release", "major", "--dry-run", "--date", date}})
	if r.code != 0 || !strings.Contains(r.stdout, "ok    command audit docs/audits/audit-1.0.0.md has no open high problem") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
}

func TestB802MajorOpenHighProblemRefuses(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	b802Commit(f, "docs/audits/audit-1.0.0.md", b802Audit(
		releaseaudit.Row{Command: "run status", Check: "help", Result: releaseaudit.Problem, Severity: releaseaudit.High, Block: "B901"},
	), "docs: audit")
	r := f.runGo(parityCase{args: []string{"release", "major", "--dry-run", "--date", date}})
	if r.code != 1 || !strings.Contains(r.stdout, "FAIL  docs/audits/audit-1.0.0.md has 1 open high problem(s): run status help (B901 not landed)") {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", r.code, r.stdout, r.stderr)
	}
}

func TestB802PatchAndMinorNeedNoAudit(t *testing.T) {
	for _, bump := range []string{"patch", "minor", "0.22.0"} {
		f := newFixture(t, fixtureOpts{})
		r := f.runGo(parityCase{args: []string{"release", bump, "--dry-run", "--date", date}})
		if r.code != 0 || strings.Contains(r.stdout+r.stderr, "audit") {
			t.Fatalf("%s: exit %d\nstdout:\n%s\nstderr:\n%s", bump, r.code, r.stdout, r.stderr)
		}
	}
}

func TestB802NoChecksKeepsGateUnlessAllowNoAudit(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	r := f.runGo(parityCase{args: []string{"release", "major", "--no-checks", "--dry-run", "--date", date}})
	if r.code != 1 || !strings.Contains(r.stdout, "FAIL  a major release needs the command audit") {
		t.Fatalf("--no-checks skipped the audit gate: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	r = f.runGo(parityCase{args: []string{"release", "major", "--no-checks", "--allow-no-audit", "--dry-run", "--date", date}})
	if r.code != 0 || !strings.Contains(r.stdout, "WARN  !!! --allow-no-audit: the major release command audit docs/audits/audit-1.0.0.md was NOT checked !!!") {
		t.Fatalf("--allow-no-audit: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}
