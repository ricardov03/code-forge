package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// b572PathWithoutNode is a PATH folder holding only git, so no node is found.
func b572PathWithoutNode(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(git, filepath.Join(dir, "git")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// b572Pack builds B1's pack and returns its head (up to the built-in proof
// rule, which other blocks own) with the fixture's base folder written as
// <BASE>, so it compares across temp dirs.
func b572Pack(t *testing.T, f *contextFixture) string {
	t.Helper()
	code, out, errText := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "B1")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q, want a pack", code, out, errText)
	}
	head, _, found := strings.Cut(out, "## proof rule (code-forge)\n")
	if !found {
		t.Fatalf("pack has no proof rule:\n%s", out)
	}
	return strings.ReplaceAll(head, filepath.Dir(f.home), "<BASE>")
}

const b572Config = "version: 1\ncontext:\n  commands:\n    test: [go, test]\n"

var b572Blocks = map[string]any{"B1": map[string]any{"status": "open", "level": "L1", "owned_files": []any{"a.go"}}}

// B572: with no node on PATH and CODE_FORGE_NODE naming no file, the pack is
// still written; its code-forge line names the install's bin/code-forge
// launcher (it runs this Go binary), and no node.
func TestB572ContextPackWithoutNodeNamesLauncher(t *testing.T) {
	f := newContextFixture(t, b572Blocks, b572Config)
	launcher := filepath.Join(filepath.Dir(f.home), "pkg", "bin", "code-forge")
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b572-node")
	t.Setenv("PATH", b572PathWithoutNode(t))
	got := b572Pack(t, f)
	want, err := os.ReadFile(filepath.Join("testdata", "b572", "pack-no-node.golden.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("pack =\n%s\nwant\n%s", got, want)
	}
	if !strings.Contains(got, "run code-forge verbs with exactly: <BASE>/pkg/bin/code-forge\n") || strings.Contains(got, "nodebin") {
		t.Fatalf("pack does not name the launcher alone:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(f.tmp, "r-ctx", "context", "B1.md")); err != nil {
		t.Fatalf("pack file: %v", err)
	}
}

// B572: an install with no node and no bin/code-forge launcher has no
// command to name; the pack refuses and writes nothing.
func TestB572ContextPackWithoutNodeOrLauncherRefuses(t *testing.T) {
	f := newContextFixture(t, b572Blocks, b572Config)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b572-node")
	t.Setenv("PATH", b572PathWithoutNode(t))
	code, out, errText := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "B1")
	if code != 1 || out != "" || !strings.Contains(errText, "this install's bin/code-forge cannot be found (ENOENT); reinstall code-forge; nothing was written") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errText)
	}
	if _, err := os.Stat(filepath.Join(f.tmp, "r-ctx", "context", "B1.md")); !os.IsNotExist(err) {
		t.Fatalf("a refusal wrote the pack: %v", err)
	}
}

// B572: with node found, the pack bytes are the ones main wrote (golden made
// on main before B572): node and bin/code-forge.mjs, as Node's own pack.
func TestB572ContextPackWithNodeUnchanged(t *testing.T) {
	f := newContextFixture(t, b572Blocks, b572Config)
	got := b572Pack(t, f)
	want, err := os.ReadFile(filepath.Join("testdata", "b572", "pack-node.golden.txt"))
	if os.Getenv("B572_WRITE_GOLDEN") == "1" {
		_ = os.MkdirAll(filepath.Join("testdata", "b572"), 0o755)
		if err := os.WriteFile(filepath.Join("testdata", "b572", "pack-node.golden.txt"), []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("pack =\n%s\nwant\n%s", got, want)
	}
}
