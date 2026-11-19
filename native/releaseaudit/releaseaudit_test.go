package releaseaudit

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIsMajor(t *testing.T) {
	for _, c := range []struct {
		current, next string
		want          bool
	}{
		{"0.25.1", "1.0.0", true}, {"1.4.2", "2.0.0", true}, {"0.25.1", "v3.0.0", true},
		{"0.25.1", "0.26.0", false}, {"0.25.1", "0.25.2", false}, {"1.0.0", "1.9.9", false}, {"x", "1.0.0", false},
	} {
		if got := IsMajor(c.current, c.next); got != c.want {
			t.Errorf("IsMajor(%s, %s) = %v", c.current, c.next, got)
		}
	}
}

func TestRenderParseRoundTrip(t *testing.T) {
	r := Report{Version: "0.25.1", Commit: "abc", Date: "2026-11-19T00:00:00Z", Rows: []Row{
		{Command: "status", Check: "help", Result: OK},
		{Command: "block land", Check: "bad-flag", Result: Problem, Severity: High, Detail: "panic: a | b", Block: "B900"},
		{Command: "worker", Check: "bad-flag", Result: Problem, Severity: Medium, Detail: "timed out"},
	}}
	text := Render(r)
	rows, ok := Parse(text)
	if !ok || !reflect.DeepEqual(rows, r.Rows) {
		t.Fatalf("round trip lost rows: ok=%v\n%#v\n%s", ok, rows, text)
	}
	if !strings.Contains(text, "- Problems: high 1, medium 1, low 0\n") || !strings.Contains(text, "- Commands: 3\n") {
		t.Fatalf("summary lines:\n%s", text)
	}
	if _, ok := Parse(strings.Replace(text, Marker, "", 1)); ok {
		t.Fatal("a file with no marker parsed as a report")
	}
}

type fakeRepo struct {
	committed map[string]string
	disk      map[string]bool
	landed    map[string]bool
}

func (f fakeRepo) Committed(p string) (string, bool) { s, ok := f.committed[p]; return s, ok }
func (f fakeRepo) OnDisk(p string) bool              { return f.disk[p] }
func (f fakeRepo) Landed(b string) bool              { return f.landed[b] }

func TestCheck(t *testing.T) {
	path := "docs/audits/audit-1.0.0.md"
	report := func(rows ...Row) string { return Render(Report{Version: "0.25.1", Rows: rows}) }
	cases := []struct {
		name string
		repo fakeRepo
		want string // "" passes; else a substring of the failure
	}{
		{"missing", fakeRepo{}, "run `code-forge audit commands --out docs/audits/audit-1.0.0.md`"},
		{"not committed", fakeRepo{disk: map[string]bool{path: true}}, "committed: commit it"},
		{"no marker", fakeRepo{committed: map[string]string{path: "# notes\n"}}, "was not written by the audit tool"},
		{"clean", fakeRepo{committed: map[string]string{path: report(Row{Command: "status", Check: "help", Result: OK})}}, ""},
		{"medium only", fakeRepo{committed: map[string]string{path: report(Row{Command: "worker", Check: "bad-flag", Result: Problem, Severity: Medium})}}, ""},
		{"high no block", fakeRepo{committed: map[string]string{path: report(Row{Command: "run status", Check: "help", Result: Problem, Severity: High})}},
			"has 1 open high problem(s): run status help (no block)"},
		{"high block not landed", fakeRepo{committed: map[string]string{path: report(Row{Command: "run status", Check: "help", Result: Problem, Severity: High, Block: "B900"})}},
			"run status help (B900 not landed)"},
		{"high block landed", fakeRepo{committed: map[string]string{path: report(Row{Command: "run status", Check: "help", Result: Problem, Severity: High, Block: "B900"})},
			landed: map[string]bool{"B900": true}}, ""},
	}
	for _, c := range cases {
		got := Check(c.repo, "1.0.0")
		if (c.want == "") != (got == "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestGitRepoLanded(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	g := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	g("init", "-q", "-b", "main")
	path := filepath.Join(root, "docs", "audits", "audit-1.0.0.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	text := Render(Report{Rows: []Row{{Command: "status", Check: "help", Result: Problem, Severity: High, Block: "B900"}}})
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := CheckRoot(root, "1.0.0"); !strings.Contains(got, "committed: commit it") {
		t.Fatalf("uncommitted file: %q", got)
	}
	g("add", "-A")
	g("commit", "-q", "-m", "docs: audit")
	if got := CheckRoot(root, "1.0.0"); !strings.Contains(got, "B900 not landed") {
		t.Fatalf("block not landed: %q", got)
	}
	g("commit", "-q", "--allow-empty", "-m", "B900: fix status help")
	if got := CheckRoot(root, "1.0.0"); got != "" {
		t.Fatalf("landed block still open: %q", got)
	}
}
