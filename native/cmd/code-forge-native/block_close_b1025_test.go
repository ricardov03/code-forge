package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

func b1025Close(t *testing.T, f *closeReviewFixture, sha string, approve bool) (int, string) {
	t.Helper()
	if approve {
		for _, p := range f.parts {
			if _, err := state.WriteSigned(f.home, p.Run, ledger.Object{{Key: "event", Value: "review.approved"}, {Key: "block", Value: "B1"}, {Key: "file", Value: p.File}, {Key: "content_hash", Value: p.ContentHash}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(f.top); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(before) })
	code, out, errs := runCapturedRoute(t, routes["block"], "close", "B1", "--run", "r-close-fixture", "--landed", sha, "--report", f.report, "--transcript", f.transcript, "--json")
	return code, out + errs
}

func b1025Entry(t *testing.T, f *closeReviewFixture, mutate func(*ledger.Object, *ledger.Object)) {
	t.Helper()
	record, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil {
		t.Fatal(err)
	}
	blocks := blockObject(record.Get("blocks"))
	entry := blockObject(blocks.Get("B1"))
	mutate(&record, &entry)
	blocks.Set("B1", entry)
	record.Set("blocks", blocks)
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
}

func b1025AssertState(t *testing.T, f *closeReviewFixture, status, sha string) {
	t.Helper()
	record, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil {
		t.Fatal(err)
	}
	entry := blockObject(blockObject(record.Get("blocks")).Get("B1"))
	if entry.Get("status") != status || blockString(entry.Get("landed_sha")) != sha {
		t.Fatalf("unexpected block state: %v", entry)
	}
}

func TestB1025LandedCloseRejectsLaneTarget(t *testing.T) {
	for _, mode := range []string{"own-branch", "arbitrary-branch", "missing-main", "own-merge", "block-worktree-merge", "other-worktree-tip"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CODE_FORGE_RUN_ID", "")
			f := newCloseReviewFixture(t, 1)
			git := func(args ...string) string { return strings.TrimSpace(b990Git(t, f.top, args...)) }
			git("branch", "-M", "main")
			git("config", "user.name", "fixture")
			git("config", "user.email", "fixture@example.invalid")
			git("checkout", "-qb", "lane")
			git("add", "file0.txt")
			git("commit", "-qm", "B1: implemented")
			sha := git("rev-parse", "HEAD")
			branch := "lane"
			if mode == "arbitrary-branch" {
				git("branch", "combine-looking", sha)
				branch = "combine-looking"
			}
			if mode == "missing-main" {
				git("branch", "-m", "main", "not-main")
			}
			if mode == "own-merge" || mode == "block-worktree-merge" {
				git("checkout", "-qb", "lane-combine", "main")
				git("merge", "--no-ff", "--no-edit", "lane")
				branch = "lane-combine"
			}
			b1025Entry(t, f, func(record, entry *ledger.Object) {
				entry.Set("base_branch", branch)
				if mode == "block-worktree-merge" {
					root := filepath.Join(t.TempDir(), "root")
					git("worktree", "add", "-q", root, "main")
					record.Set("workspace", root)
					entry.Set("worktree", ledger.Object{{Key: "path", Value: f.top}, {Key: "branch", Value: branch}, {Key: "base_sha", Value: entry.Get("base_sha")}})
				}
				if mode == "other-worktree-tip" {
					lane := filepath.Join(t.TempDir(), "lane")
					git("worktree", "add", "-q", "--detach", lane, sha)
					record.Set("workspace", lane)
				}
			})
			code, output := b1025Close(t, f, sha, true)
			if code == 0 || !strings.Contains(output, "commit is not an ancestor") {
				t.Fatalf("lane self-certified landing: exit=%d output=%s", code, output)
			}
			b1025AssertState(t, f, "open", "")
			if _, err := os.Stat(filepath.Join(f.home, ".code-forge/runs/r-close-fixture/blocks/B1.done.json")); !os.IsNotExist(err) {
				t.Fatalf("refusal wrote done record: %v", err)
			}
		})
	}
}

func TestB1025LandedCloseMergeCommit(t *testing.T) {
	for _, mode := range []string{"main", "combine", "unapproved", "unowned", "base-only-on-second-parent"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CODE_FORGE_RUN_ID", "")
			f := newCloseReviewFixture(t, 1)
			git := func(args ...string) string { return strings.TrimSpace(b990Git(t, f.top, args...)) }
			git("branch", "-M", "main")
			git("config", "user.name", "fixture")
			git("config", "user.email", "fixture@example.invalid")
			git("checkout", "-qb", "implementation")
			git("add", "file0.txt")
			git("commit", "-qm", "implementation")
			implementation := git("rev-parse", "HEAD")
			git("checkout", "-qb", "lane", "main")
			git("merge", "--no-ff", "-m", "B1: implemented by merge", "implementation")
			sha := git("rev-parse", "HEAD")
			if mode == "unowned" {
				closeFixtureWrite(t, filepath.Join(f.top, "unowned.txt"), []byte("unowned\n"))
				git("add", "unowned.txt")
				git("commit", "--amend", "--no-edit", "-q")
				sha = git("rev-parse", "HEAD")
			}
			if mode == "base-only-on-second-parent" {
				b1025Entry(t, f, func(_, entry *ledger.Object) { entry.Set("base_sha", implementation) })
			}
			git("checkout", "-q", "main")
			if mode == "combine" {
				git("checkout", "-qb", "combine")
				git("merge", "--no-ff", "--no-edit", "lane")
				lane := filepath.Join(t.TempDir(), "lane")
				git("worktree", "add", "-q", lane, "lane")
				b1025Entry(t, f, func(record, _ *ledger.Object) { record.Set("workspace", lane) })
			} else {
				git("merge", "--ff-only", "lane")
			}
			// The gate must still measure the merge's historical content.
			closeFixtureWrite(t, filepath.Join(f.top, "file0.txt"), []byte("later\n"))
			git("commit", "-qam", "B2: later content")
			code, output := b1025Close(t, f, sha, mode != "unapproved")
			want := map[string]string{"unowned": "commit changes unowned file", "unapproved": "unreviewed", "base-only-on-second-parent": "block base is not an ancestor"}[mode]
			if want != "" {
				if code == 0 || !strings.Contains(output, want) {
					t.Fatalf("wanted %q: exit=%d output=%s", want, code, output)
				}
				b1025AssertState(t, f, "open", "")
				return
			}
			if code != 0 {
				t.Fatalf("merge close: exit=%d output=%s", code, output)
			}
			b1025AssertState(t, f, "closed", sha)
		})
	}
}
