package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B470: the close, open, stop and land cases B466 left on the Node hand-over.

func b470Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s %v", args, data, err)
	}
	return strings.TrimSpace(string(data))
}

// b470SetRun edits the close fixture's run record in place.
func b470SetRun(t *testing.T, f *closeReviewFixture, edit func(record, entry *ledger.Object)) {
	t.Helper()
	record, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil {
		t.Fatal(err)
	}
	blocks := record.Get("blocks").(ledger.Object)
	entry := blocks.Get("B1").(ledger.Object)
	edit(&record, &entry)
	blocks.Set("B1", entry)
	record.Set("blocks", blocks)
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
}

func b470Tree(path, branch, sha string) ledger.Object {
	return ledger.Object{{Key: "path", Value: path}, {Key: "branch", Value: branch}, {Key: "base_sha", Value: sha}}
}

// Node's blockTree refusals: one Go line each, exit 1, nothing recorded.
func TestB470CloseWorktreeRefusalsAreNative(t *testing.T) {
	refusal := func(message string) string {
		return "block close: block B1: " + message + ", so its work cannot be measured; nothing was recorded\n"
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *closeReviewFixture, base string) (tree ledger.Object, want string)
	}{
		{"malformed", func(t *testing.T, f *closeReviewFixture, base string) (ledger.Object, string) {
			return b470Tree("relative/tree", "b", base), refusal("malformed worktree record (it needs an absolute path, a branch and a base_sha)")
		}},
		{"malformed-control", func(t *testing.T, f *closeReviewFixture, base string) (ledger.Object, string) {
			return b470Tree(filepath.Join(f.home, "t\x1bx"), "b", base), refusal("malformed worktree record (it needs an absolute path, a branch and a base_sha)")
		}},
		{"not-dir", func(t *testing.T, f *closeReviewFixture, base string) (ledger.Object, string) {
			file := filepath.Join(f.home, "tree-file")
			closeFixtureWrite(t, file, []byte("x"))
			return b470Tree(file, "b", base), refusal("the worktree " + file + " is not a directory")
		}},
		{"other-branch", func(t *testing.T, f *closeReviewFixture, base string) (ledger.Object, string) {
			tree := filepath.Join(f.home, "wt")
			b470Git(t, f.top, "worktree", "add", "-q", "-b", "wt-branch", tree, base)
			return b470Tree(tree, "b470-want", base), refusal("the worktree " + tree + " is not on its branch b470-want (it is on wt-branch)")
		}},
		{"not-a-worktree", func(t *testing.T, f *closeReviewFixture, base string) (ledger.Object, string) {
			tree := filepath.Join(f.home, "plain")
			if err := os.MkdirAll(tree, 0700); err != nil {
				t.Fatal(err)
			}
			return b470Tree(tree, "b", base), refusal("the worktree " + tree + " is not on its branch b (its HEAD is detached, or it is not a git worktree)")
		}},
		{"foreign", func(t *testing.T, f *closeReviewFixture, base string) (ledger.Object, string) {
			tree := filepath.Join(f.home, "other")
			if err := os.MkdirAll(tree, 0700); err != nil {
				t.Fatal(err)
			}
			b470Git(t, tree, "init", "-q", "-b", "b")
			return b470Tree(tree, "b", base), refusal("the worktree " + tree + " belongs to another repository")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCloseReviewFixture(t, 1)
			base := b470Git(t, f.top, "rev-parse", "HEAD")
			tree, want := tc.setup(t, f, base)
			b470SetRun(t, f, func(_, entry *ledger.Object) { entry.Set("worktree", tree) })
			ledgerFile := filepath.Join(f.home, ".code-forge/ledger/fixture.jsonl")
			before, _ := os.ReadFile(ledgerFile)
			code, out, errs, reason := b466Close(f, f.transcript)
			if reason != 0 {
				t.Fatalf("handed over to Node (reason %d); want Go", reason)
			}
			if code != 1 || out != "" || errs != want {
				t.Fatalf("exit=%d stdout=%q stderr=%q; want exit 1 and %q", code, out, errs, want)
			}
			if after, _ := os.ReadFile(ledgerFile); !bytes.Equal(before, after) {
				t.Fatalf("ledger changed:\n%s", after)
			}
		})
	}
}

// A workspace below the repository top must exist inside the tree too.
func TestB470CloseWorktreeWithoutWorkspaceDirIsNative(t *testing.T) {
	f := newCloseReviewFixture(t, 1)
	base := b470Git(t, f.top, "rev-parse", "HEAD")
	sub := filepath.Join(f.top, "pkg")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	tree := filepath.Join(f.home, "wt")
	b470Git(t, f.top, "worktree", "add", "-q", "-b", "wt-branch", tree, base)
	b470SetRun(t, f, func(record, entry *ledger.Object) {
		record.Set("workspace", sub)
		entry.Set("worktree", b470Tree(tree, "wt-branch", base))
	})
	code, out, errs, reason := b466Close(f, f.transcript)
	want := "block close: block B1: the worktree " + tree + " has no pkg directory (the run's workspace), so its work cannot be measured; nothing was recorded\n"
	if reason != 0 || code != 1 || out != "" || errs != want {
		t.Fatalf("reason=%d exit=%d stdout=%q stderr=%q; want exit 1 and %q", reason, code, out, errs, want)
	}
}

// Node filters the ledger with row.run: a null row throws, other non-object
// rows are dropped, and the read comes before the run-ended refusal.
func TestB470CloseLedgerShapeIsNative(t *testing.T) {
	appendLine := func(t *testing.T, f *closeReviewFixture, line string) {
		file := filepath.Join(f.home, ".code-forge/ledger/fixture.jsonl")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		// a middle line, so the torn-last-line rule does not apply
		closeFixtureWrite(t, file, append([]byte(line+"\n"), data...))
	}
	t.Run("null", func(t *testing.T) {
		f := newCloseReviewFixture(t, 1)
		appendLine(t, f, "null")
		b470SetRun(t, f, func(record, _ *ledger.Object) { record.Set("status", "ended") })
		code, out, errs, reason := b466Close(f, f.transcript)
		want := "block close: Cannot read properties of null (reading 'run')\n"
		if reason != 0 || code != 1 || out != "" || errs != want {
			t.Fatalf("reason=%d exit=%d stdout=%q stderr=%q; want exit 1 and %q", reason, code, out, errs, want)
		}
	})
	t.Run("scalar-rows-dropped", func(t *testing.T) {
		f := newCloseReviewFixture(t, 1)
		appendLine(t, f, `5`)
		appendLine(t, f, `"text"`)
		appendLine(t, f, `[1]`)
		// the close goes on in Go: the review wait and its blockers, as without them
		code, out, errs, reason := b466Close(f, f.transcript)
		if reason != 0 || code != 1 || !strings.Contains(out, "review_pending file0.txt") || !strings.HasSuffix(errs, "block B1 open: review_pending file0.txt\n") {
			t.Fatalf("reason=%d exit=%d stdout=%q stderr=%q; want Go's review_pending close", reason, code, out, errs)
		}
	})
}

// readAllRows' errno text for a rotated ledger file (the live file and the
// directory are storage checks that stay on the hand-over): a directory is
// EISDIR on read (no path); an unreadable file is EACCES on open.
func TestB470CloseLedgerReadErrorIsNative(t *testing.T) {
	t.Run("rotated-is-dir", func(t *testing.T) {
		f := newCloseReviewFixture(t, 1)
		if err := os.MkdirAll(filepath.Join(f.home, ".code-forge/ledger/fixture.1.jsonl"), 0700); err != nil {
			t.Fatal(err)
		}
		code, out, errs, reason := b466Close(f, f.transcript)
		want := "block close: EISDIR: illegal operation on a directory, read\n"
		if reason != 0 || code != 1 || out != "" || errs != want {
			t.Fatalf("reason=%d exit=%d stdout=%q stderr=%q; want exit 1 and %q", reason, code, out, errs, want)
		}
	})
	t.Run("rotated-unreadable", func(t *testing.T) {
		f := newCloseReviewFixture(t, 1)
		file := filepath.Join(f.home, ".code-forge/ledger/fixture.1.jsonl")
		closeFixtureWrite(t, file, []byte("{}\n"))
		if err := os.Chmod(file, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(file, 0600) })
		code, out, errs, reason := b466Close(f, f.transcript)
		want := "block close: EACCES: permission denied, open '" + file + "'\n"
		if reason != 0 || code != 1 || out != "" || errs != want {
			t.Fatalf("reason=%d exit=%d stdout=%q stderr=%q; want exit 1 and %q", reason, code, out, errs, want)
		}
	})
}

// open --brief: Node's realpath error names the path as given; its readFile
// error on a directory has no path. Exit 1, no block opened.
func TestB470OpenBriefErrorsAreNative(t *testing.T) {
	for _, tc := range []struct {
		name  string
		brief func(f *closeReviewFixture) string
		want  func(f *closeReviewFixture) string
	}{
		{"missing", func(f *closeReviewFixture) string { return "no-such-brief.md" },
			func(*closeReviewFixture) string {
				return "block open: ENOENT: no such file or directory, realpath 'no-such-brief.md'\n"
			}},
		{"under-a-file", func(f *closeReviewFixture) string { return filepath.Join(f.top, "file0.txt", "brief.md") },
			func(f *closeReviewFixture) string {
				return "block open: ENOTDIR: not a directory, realpath '" + filepath.Join(f.top, "file0.txt", "brief.md") + "'\n"
			}},
		{"directory", func(f *closeReviewFixture) string {
			dir := filepath.Join(f.top, "plans")
			_ = os.MkdirAll(dir, 0700)
			return dir
		}, func(*closeReviewFixture) string {
			return "block open: EISDIR: illegal operation on a directory, read\n"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCloseReviewFixture(t, 1)
			// a run records its workspace resolved, as run start does
			real, _ := filepath.EvalSymlinks(f.top)
			b470SetRun(t, f, func(record, _ *ledger.Object) { record.Set("workspace", real) })
			acceptance := filepath.Join(f.home, "acceptance.yml")
			closeFixtureWrite(t, acceptance, []byte("- clause: the card shows\n  tests: [card_test.go]\n"))
			runFile := filepath.Join(f.home, ".code-forge/runs/r-close-fixture.json")
			before, _ := os.ReadFile(runFile)
			wd, _ := os.Getwd()
			if err := os.Chdir(f.top); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chdir(wd) })
			res := b451Run(t, f.home, "open", "B2", "--run", "r-close-fixture", "--level", "L1", "--owned", "card.go", "--acceptance", acceptance, "--brief", tc.brief(f))
			if res.code != 1 || res.stdout != "" || res.stderr != tc.want(f) {
				t.Fatalf("exit=%d stdout=%q stderr=%q; want exit 1 and %q", res.code, res.stdout, res.stderr, tc.want(f))
			}
			if after, _ := os.ReadFile(runFile); !bytes.Equal(before, after) {
				t.Fatal("the run record changed")
			}
		})
	}
}
