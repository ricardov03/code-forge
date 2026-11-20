package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// b898CountGit puts a git on PATH that logs its verb and runs the real git;
// the returned func reads the logged verbs.
func b898CountGit(t *testing.T) func() []string {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "calls.log")
	script := "#!/bin/sh\necho \"$1\" >> '" + log + "'\nexec '" + real + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() []string {
		data, _ := os.ReadFile(log)
		_ = os.Remove(log)
		return strings.Fields(string(data))
	}
}

// B898: the doctor state-ignored row asks git twice (check-ignore, then
// ls-files from the top) instead of three times, with the same row text,
// also from a sub-directory of the repository.
func TestB898StateRowGitCalls(t *testing.T) {
	repo, _ := filepath.EvalSymlinks(t.TempDir())
	b828Laravel(t, repo, b828LaravelIgnore+".code-forge/\n")
	sub := filepath.Join(repo, "app", "Models")
	calls := b898CountGit(t)
	for _, dir := range []string{repo, sub} {
		row, shown := doctorStateIgnoreRow(dir)
		if got := calls(); !shown || row.status != "OK" || row.detail != "yes" || len(got) != 2 {
			t.Fatalf("%s: row = %+v shown=%v, git calls %q (want 2)", dir, row, shown, got)
		}
	}
	b462Write(t, filepath.Join(repo, ".code-forge", "runs", "r-acme-1.json"), []byte("{}\n"), 0o600)
	b828MustGit(t, repo, "add", "-f", ".code-forge/runs/r-acme-1.json")
	calls()
	const want = ".code-forge/: git tracks 1 file under it (.code-forge/runs/r-acme-1.json); remove them from git with: git rm -r --cached .code-forge && git commit"
	for _, dir := range []string{repo, sub} {
		row, _ := doctorStateIgnoreRow(dir)
		if got := calls(); row.status != "FAIL" || row.detail != want || len(got) != 2 {
			t.Fatalf("%s: row = %+v, git calls %q (want 2)", dir, row, got)
		}
	}
}
