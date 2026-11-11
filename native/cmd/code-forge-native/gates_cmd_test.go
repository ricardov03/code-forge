package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B363: gatesStaleWarning is the one WARN line that gates run --allow-stale-base
// and block land print for a stale base, and nothing for a current base.
func TestGatesStaleWarningNamesTheFixOnlyForAStaleBase(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(gitx.ChildEnv(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	commit := func(text string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", ".")
		git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", text)
		return git("rev-parse", "HEAD")
	}
	git("init", "-qb", "main")
	base := commit("base")
	run := "r-stale-warn"
	entry := ledger.Object{{Key: "status", Value: "open"}, {Key: "base_sha", Value: base}, {Key: "base_branch", Value: "main"},
		{Key: "worktree", Value: ledger.Object{{Key: "path", Value: repo}, {Key: "branch", Value: "main"}, {Key: "base_sha", Value: base}}}}
	record := ledger.Object{{Key: "run_id", Value: run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: repo},
		{Key: "status", Value: "active"}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}}
	if err := state.SaveRun(home, record); err != nil {
		t.Fatal(err)
	}
	if got := gatesStaleWarning(home, run, "B1"); got != "" {
		t.Fatalf("equal base: want no line, got %q", got)
	}
	tip := commit("moved")
	got := gatesStaleWarning(home, run, "B1")
	want := "gates run: WARN block B1 of run r-stale-warn gates on a stale base (main moved " + base[:12] + " → " + tip[:12] +
		") because of --allow-stale-base; fix: code-forge block rebase B1 --run r-stale-warn --accept-landed\n"
	if got != want {
		t.Fatalf("stale base:\nwant %q\ngot  %q", want, got)
	}
	if got := gatesStaleWarning(home, "r-missing", "B1"); got != "" {
		t.Fatalf("missing run: want no line, got %q", got)
	}
}

// B335: gates classes prints the block's test-class filter from the Android
// reporter sample, and nothing on stdout (full suite) for a block with no test.
func TestGatesClassesPrintsTheBlockFilter(t *testing.T) {
	sample := filepath.Join("..", "..", "..", "test", "fixtures", "android-classes")
	home, workspace := t.TempDir(), t.TempDir()
	yml, err := os.ReadFile(filepath.Join(sample, "code-forge.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".code-forge.yml"), yml, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(sample, "block.json"))
	if err != nil {
		t.Fatal(err)
	}
	android, err := ledger.DecodeObject(raw)
	if err != nil {
		t.Fatal(err)
	}
	plain := ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{"app/src/main/java/a/b/Foo.kt"}}}
	run := "r-classes"
	record := ledger.Object{{Key: "run_id", Value: run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: workspace},
		{Key: "status", Value: "active"}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: android}, {Key: "B2", Value: plain}}}, {Key: "orphans", Value: []any{}}}
	if err := state.SaveRun(home, record); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ block, stdout, stderr string }{
		{"B1", "a.b.FooTest,a.b.BarTest\n", ""},
		{"B2", "", "gates classes: block B2 has no test class in gates.test_class_map; {test_classes} is dropped and the full suite runs\n"},
	} {
		var out, errOut bytes.Buffer
		code := gatesClasses([]string{"--run", run, "--block", tc.block}, home, &out, &errOut)
		if code != 0 || out.String() != tc.stdout || errOut.String() != tc.stderr {
			t.Fatalf("%s: code=%d stdout=%q stderr=%q", tc.block, code, out.String(), errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := gatesClasses([]string{"--run", run}, home, &out, &errOut); code != 2 || errOut.String() != gatesClassesUsage+"\n" {
		t.Fatalf("missing --block: code=%d stderr=%q", code, errOut.String())
	}
}
