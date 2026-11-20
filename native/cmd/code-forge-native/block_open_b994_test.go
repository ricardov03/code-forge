package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B994: append-only shared files (CHANGELOG.md, a docs table) may be owned by
// two open blocks of one run when both blocks name them with --shared. A file
// only one side shares still overlaps, and --shared names exact owned files.

func b994Setup(t *testing.T) (string, b553Fix, []string, string, string) {
	t.Helper()
	bin := b553Binary(t)
	f := b553Fixture(t, "approve")
	t.Cleanup(func() { b553Reap(t, f) })
	env := b553Env(f)
	run := "r-b994"
	if code, out, errText := b969Exec(t, f.repo, env, bin, "run", "start", "--cwd", ".", "--run", run); code != 0 {
		t.Fatalf("run start = %d\n%s\n%s", code, out, errText)
	}
	acc := filepath.Join(f.dir, "acc.json")
	if err := os.WriteFile(acc, []byte(`[{"clause":"c","tests":["TestX"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	return bin, f, env, run, acc
}

func b994Open(t *testing.T, bin string, f b553Fix, env []string, run, acc, id string, rest ...string) (int, string) {
	t.Helper()
	argv := append([]string{bin, "block", "open", id, "--run", run, "--level", "L1", "--acceptance", acc}, rest...)
	code, out, errText := b969Exec(t, f.repo, env, argv...)
	return code, out + errText
}

func TestB994SharedOwnersOpen(t *testing.T) {
	bin, f, env, run, acc := b994Setup(t)
	if code, text := b994Open(t, bin, f, env, run, acc, "B1", "--owned", "src/a.txt", "CHANGELOG.md", "docs/table.md", "--shared", "CHANGELOG.md", "docs/table.md"); code != 0 {
		t.Fatalf("B1 open = %d\n%s", code, text)
	}
	code, text := b994Open(t, bin, f, env, run, acc, "B2", "--owned", "src/b.txt", "CHANGELOG.md", "docs/table.md", "--shared", "CHANGELOG.md", "docs/table.md")
	if code != 0 {
		t.Fatalf("B2 open with both files shared = %d\n%s", code, text)
	}
	// each block keeps its own owned files: a third block that does not share
	// CHANGELOG.md is refused, naming it
	code, text = b994Open(t, bin, f, env, run, acc, "B3", "--owned", "CHANGELOG.md")
	if code != 1 || !strings.Contains(text, "overlaps CHANGELOG.md owned by open block B1") {
		t.Fatalf("B3 without --shared = %d\n%s", code, text)
	}
	// a shared file does not share the rest: src/a.txt stays B1's alone
	code, text = b994Open(t, bin, f, env, run, acc, "B4", "--owned", "src/a.txt", "CHANGELOG.md", "--shared", "CHANGELOG.md")
	if code != 1 || !strings.Contains(text, "overlaps src/a.txt owned by open block B1") {
		t.Fatalf("B4 owning B1's src/a.txt = %d\n%s", code, text)
	}
}

func TestB994SharedNeedsBoth(t *testing.T) {
	bin, f, env, run, acc := b994Setup(t)
	if code, text := b994Open(t, bin, f, env, run, acc, "B1", "--owned", "CHANGELOG.md"); code != 0 {
		t.Fatalf("B1 open = %d\n%s", code, text)
	}
	// B1 did not share it: B2's --shared alone does not open
	code, text := b994Open(t, bin, f, env, run, acc, "B2", "--owned", "CHANGELOG.md", "--shared", "CHANGELOG.md")
	if code != 1 || !strings.Contains(text, "overlaps CHANGELOG.md owned by open block B1") {
		t.Fatalf("B2 shared against an unshared owner = %d\n%s", code, text)
	}
}

func TestB994SharedMustBeOwnedFile(t *testing.T) {
	bin, f, env, run, acc := b994Setup(t)
	for _, tc := range []struct{ name, want string }{
		{"README.md", `shared files: "README.md" must also be named in --owned`},
		{"docs/*.md", `shared files: "docs/*.md" must be an exact file path`},
		{"src", `shared files: "src" must be a file, not a directory`},
	} {
		code, text := b994Open(t, bin, f, env, run, acc, "B1", "--owned", "CHANGELOG.md", "docs/*.md", "src", "--shared", tc.name)
		if code != 1 || !strings.Contains(text, tc.want) {
			t.Fatalf("--shared %s = %d\n%s", tc.name, code, text)
		}
	}
}
