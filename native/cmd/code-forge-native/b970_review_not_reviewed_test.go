package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// B970: `review` is ok only when every file was reviewed. A named file with
// no change against --base is refused before anything starts (exit 1, "not
// reviewed"), and a file the worker answers no_change makes ok false.

func TestB970NamedFileWithoutChangeIsNotReviewed(t *testing.T) {
	bin := b553Binary(t)
	f := b553Fixture(t, "approve")
	cmd := exec.Command("git", "checkout", "-q", "--", "src/b.txt")
	cmd.Dir = f.repo
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + f.home}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout: %v\n%s", err, out)
	}
	env := b553Env(f, "CODE_FORGE_HANDOVER_LOG=1", "CODE_FORGE_NODE="+filepath.Join(f.dir, "missing-node"))
	for _, args := range [][]string{
		{"--files", "src/a.txt", "src/b.txt", "--max", "60"},
		{"--files", "src/b.txt", "--json", "--max", "60"},
	} {
		s := b553Run(t, f, append([]string{bin, "review"}, args...), env)
		if s.code != 1 || !strings.Contains(s.err, "review: not reviewed: these files have no change against --base ") ||
			!strings.Contains(s.err, "in this tree: src/b.txt\n") || strings.Contains(s.err, "src/a.txt") {
			t.Fatalf("review %v = %d\nstdout %q\nstderr %q", args, s.code, s.out, s.err)
		}
		if strings.Contains(s.out, `"ok":true`) {
			t.Fatalf("ok:true for a file nobody reviewed: %s", s.out)
		}
		// nothing started: no run, no queue, no temp root
		if len(s.tmp) != 0 {
			t.Fatalf("temp root entries = %v", s.tmp)
		}
		if _, err := os.Stat(filepath.Join(f.repo, ".code-forge", "queue")); err == nil {
			t.Fatal("a queue was made for a refused review")
		}
		if len(s.handover) != 0 {
			t.Fatalf("hand-overs: %v", s.handover)
		}
	}
}

func TestB970UnchangedIsNotOK(t *testing.T) {
	approved := rvfOutcome{file: "src/a.txt", result: "approved"}
	unchanged := rvfOutcome{file: "src/b.txt", result: "unchanged"}
	cases := []struct {
		in   []rvfOutcome
		ok   bool
		none []string
	}{
		{[]rvfOutcome{approved}, true, []string{}},
		{[]rvfOutcome{approved, unchanged}, false, []string{"src/b.txt"}},
		{[]rvfOutcome{unchanged}, false, []string{"src/b.txt"}},
		{[]rvfOutcome{approved, {file: "src/c.txt", result: "unavailable"}}, false, []string{}},
	}
	for _, c := range cases {
		ok, none := rvfVerdict(c.in)
		if ok != c.ok || !reflect.DeepEqual(none, c.none) {
			t.Fatalf("rvfVerdict(%v) = %v %v, want %v %v", c.in, ok, none, c.ok, c.none)
		}
	}
}
