package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/landmerge"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B814: single land with a main that conflicts with the block refuses before
// close: the block stays open, no done record or close row is written, main is
// unchanged and the output names the conflicting paths.
func TestB814LandRefusesConflictBeforeClose(t *testing.T) {
	for _, gate := range []bool{true, false} {
		t.Run(fmt.Sprintf("merge_gate=%v", gate), func(t *testing.T) {
			f := newB789Fixture(t, true, "open")
			f.approve()
			f.must(os.WriteFile(filepath.Join(f.repo, "sample.mjs"), []byte("main\n"), 0o600))
			f.git(f.repo, "commit", "-qam", "main changes sample")
			mainBefore := f.git(f.repo, "rev-parse", "HEAD")
			flags := map[string]any{"merge": true}
			if !gate {
				flags["no-merge-gate"] = true
			}
			code, output, handed := f.land(flags)
			if handed != "" || code != 1 {
				t.Fatalf("land = %d handed=%q, want 1:\n%s", code, handed, output)
			}
			if !strings.Contains(output, "block B1 land stopped at merge: merge conflict with main found before close; the block stays open and main is unchanged: sample.mjs (code)") {
				t.Fatalf("output lacks the pre-close refusal:\n%s", output)
			}
			if !strings.Contains(output, "next: code-forge block rebase B1 --run "+f.run) {
				t.Fatalf("output lacks the rebase next step:\n%s", output)
			}
			record, err := state.ReadRun(f.home, f.run)
			f.must(err)
			if got := blockObject(blockObject(record.Get("blocks")).Get("B1")).Get("status"); got != "open" {
				t.Fatalf("block status = %v, want open", got)
			}
			if _, err := os.Stat(filepath.Join(f.home, ".code-forge/runs", f.run, "blocks", "B1.done.json")); !os.IsNotExist(err) {
				t.Fatalf("close wrote a done record: %v", err)
			}
			if rows := (b795Single{f}).rows("closed"); len(rows) != 0 {
				t.Fatalf("close rows written: %v", rows)
			}
			if rows := (b795Single{f}).rows("land.conflict"); len(rows) != 1 || fmt.Sprint(rows[0]["files"]) != "[map[file:sample.mjs kind:code]]" {
				t.Fatalf("land.conflict rows=%v", rows)
			}
			if got := f.git(f.repo, "rev-parse", "HEAD"); got != mainBefore || f.git(f.repo, "status", "--porcelain") != "" {
				t.Fatal("main changed")
			}
		})
	}
}

// An append-only conflict (CHANGELOG.md) is one land resolves: the pre-close
// check lets it through and the block lands.
func TestB814LandAppendOnlyConflictStillLands(t *testing.T) {
	f := newB789Fixture(t, true, "open")
	base := "# Changelog\n\n## [Unreleased]\n\n## [0.1.0]\n"
	f.must(os.WriteFile(filepath.Join(f.repo, "CHANGELOG.md"), []byte(base), 0o600))
	f.git(f.repo, "add", "CHANGELOG.md")
	f.git(f.repo, "commit", "-qm", "changelog")
	f.git(f.tree, "rebase", "-q", "main")
	f.must(os.WriteFile(filepath.Join(f.tree, "CHANGELOG.md"), []byte(strings.Replace(base, "## [Unreleased]\n\n", "## [Unreleased]\n\n### Fixed\n\n- fix B1\n\n", 1)), 0o600))
	f.git(f.tree, "commit", "-qam", "B1 changelog")
	f.must(os.WriteFile(filepath.Join(f.repo, "CHANGELOG.md"), []byte(strings.Replace(base, "## [Unreleased]\n\n", "## [Unreleased]\n\n### Fixed\n\n- fix B0\n\n", 1)), 0o600))
	f.git(f.repo, "commit", "-qam", "B0 changelog")
	b814ApproveAll(f)
	code, output, handed := f.land(map[string]any{"merge": true, "no-merge-gate": true})
	if handed != "" || code != 0 {
		t.Fatalf("land = %d handed=%q, want 0:\n%s", code, handed, output)
	}
	if got := f.git(f.repo, "show", "HEAD:CHANGELOG.md"); !strings.Contains(got, "- fix B0\n- fix B1") {
		t.Fatalf("CHANGELOG.md = %q", got)
	}
}

// b814ApproveAll approves every file the review join queues (the block's
// files and its auto-claimed CHANGELOG.md).
func b814ApproveAll(f *b789Fixture) {
	f.t.Helper()
	f.entry.Set("owned_files", []any{"sample.mjs", "CHANGELOG.md"})
	blocks := blockObject(f.record.Get("blocks"))
	blocks.Set("B1", f.entry)
	f.record.Set("blocks", blocks)
	f.must(state.SaveRun(f.home, f.record))
	hash, err := gitx.ContentHash(f.tree, "CHANGELOG.md")
	f.must(err)
	_, err = state.WriteSigned(f.home, f.run, ledger.Object{{Key: "event", Value: "review.approved"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "CHANGELOG.md"}, {Key: "content_hash", Value: hash}})
	f.must(err)
	seed := reviewWaitBlockJoin(f.tree, f.repo, f.run, "B1", 0, io.Discard)
	files, _ := seed.answer.Get("files").([]any)
	if seed.handed || seed.refusal != nil || len(files) == 0 {
		f.t.Fatalf("seed join = %+v, want queued tickets", seed)
	}
	for _, file := range files {
		ticket := blockString(blockObject(file).Get("ticket"))
		if ticket == "" {
			continue
		}
		f.must(os.WriteFile(filepath.Join(f.repo, ".code-forge", "queue", ticket+".done"), []byte(`{"ticket":"`+ticket+`","status":"reviewed","approved":true,"open_findings":false}`), 0o600))
	}
}

// With land.auto_resolve off, single land resolves no conflict (lcfMerge), so
// the pre-close check must stop on an append-only CHANGELOG.md conflict too;
// with it on, that conflict is one land resolves and the check lets it pass.
func TestB814PrecheckAutoResolveOffStopsChangelogConflict(t *testing.T) {
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := gitx.Run(dir, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(out)
	}
	write := func(dir, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "CHANGELOG.md"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	base := "# Changelog\n\n## [Unreleased]\n\n## [0.1.0]\n"
	git(repo, "init", "-qb", "main")
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "f@example.invalid")
	git(repo, "config", "commit.gpgsign", "false")
	write(repo, base)
	git(repo, "add", "CHANGELOG.md")
	git(repo, "commit", "-qm", "base")
	tree := filepath.Join(repo, "wt")
	git(repo, "worktree", "add", "-qb", "block-B1", tree)
	write(tree, strings.Replace(base, "## [Unreleased]\n\n", "## [Unreleased]\n\n### Fixed\n\n- fix B1\n\n", 1))
	git(tree, "commit", "-qam", "B1 changelog")
	write(repo, strings.Replace(base, "## [Unreleased]\n\n", "## [Unreleased]\n\n### Fixed\n\n- fix B0\n\n", 1))
	git(repo, "commit", "-qam", "B0 changelog")

	off, err := lcfPrecheck(repo, "main", tree, landmerge.Legacy())
	if err != nil || len(off) != 1 || off[0].Path != "CHANGELOG.md" || off[0].Kind != landmerge.KindCode {
		t.Fatalf("auto_resolve off: conflicts=%v err=%v, want CHANGELOG.md (code)", off, err)
	}
	on, err := lcfPrecheck(repo, "main", tree, landmerge.Defaults())
	if err != nil || len(on) != 0 {
		t.Fatalf("auto_resolve on: conflicts=%v err=%v, want none (land resolves it)", on, err)
	}
}
