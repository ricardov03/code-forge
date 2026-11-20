package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

func TestB1017LandedClose(t *testing.T) {
	for _, mode := range []string{"main", "combine", "unlanded", "wrong-block", "unapproved", "dirty", "human", "unowned", "missing-worktree", "unlanded-other-worktree"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CODE_FORGE_RUN_ID", "")
			f := newCloseReviewFixture(t, 1)
			git := func(args ...string) string { return strings.TrimSpace(b990Git(t, f.top, args...)) }
			git("branch", "-M", "main")
			git("checkout", "-qb", "lane")
			git("config", "user.name", "fixture")
			git("config", "user.email", "fixture@example.invalid")
			git("add", "file0.txt")
			if mode == "unowned" {
				closeFixtureWrite(t, filepath.Join(f.top, "unowned.txt"), []byte("unowned\n"))
				git("add", "unowned.txt")
			}
			title := "B1: implemented"
			if mode == "wrong-block" {
				title = "B2: unrelated"
			}
			git("commit", "-qm", title)
			sha := git("rev-parse", "HEAD")
			if mode != "unapproved" {
				for _, p := range f.parts {
					if _, err := state.WriteSigned(f.home, p.Run, ledger.Object{{Key: "event", Value: "review.approved"}, {Key: "block", Value: "B1"}, {Key: "file", Value: p.File}, {Key: "content_hash", Value: p.ContentHash}}); err != nil {
						t.Fatal(err)
					}
				}
			}
			record, err := state.ReadRun(f.home, "r-close-fixture")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "human" || mode == "missing-worktree" {
				blocks := blockObject(record.Get("blocks"))
				entry := blockObject(blocks.Get("B1"))
				if mode == "human" {
					entry.Set("human_checks", []any{"device"})
				} else {
					entry.Set("worktree", ledger.Object{{Key: "path", Value: filepath.Join(t.TempDir(), "gone")}, {Key: "branch", Value: "lane"}, {Key: "base_sha", Value: entry.Get("base_sha")}})
				}
				blocks.Set("B1", entry)
				record.Set("blocks", blocks)
				if err := state.SaveRun(f.home, record); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "unlanded" && mode != "unlanded-other-worktree" {
				git("checkout", "-q", "main")
				if mode == "combine" {
					closeFixtureWrite(t, filepath.Join(f.top, "integration.txt"), []byte("integration\n"))
					git("add", "integration.txt")
					git("commit", "-qm", "integration base")
					git("merge", "--no-ff", "--no-edit", "lane")
				} else {
					git("merge", "--ff-only", "lane")
				}
				closeFixtureWrite(t, filepath.Join(f.top, "other.txt"), []byte("another block\n"))
				git("add", "other.txt")
				git("commit", "-qm", "B2: later work")
				// Later shared-file edits must not invalidate the historic approval.
				closeFixtureWrite(t, filepath.Join(f.top, "file0.txt"), []byte("later content\n"))
				git("commit", "-qam", "B3: later owned content")
			}
			if mode == "combine" || mode == "unlanded-other-worktree" {
				git("branch", "combine", "HEAD")
				git("checkout", "-q", "combine")
				// main is still at the block's base, so only the caller's combine proves landing.
				base := blockString(blockObject(blockObject(record.Get("blocks")).Get("B1")).Get("base_sha"))
				git("branch", "-f", "main", base)
				lane := filepath.Join(t.TempDir(), "lane")
				git("worktree", "add", "-q", lane, "lane")
				record.Set("workspace", lane)
				if err := state.SaveRun(f.home, record); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "dirty" {
				closeFixtureWrite(t, filepath.Join(f.top, "file0.txt"), []byte("uncommitted\n"))
			}
			before, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(f.top); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chdir(before) })
			args := []string{"B1", "--run", "r-close-fixture", "--landed", sha, "--report", f.report, "--transcript", f.transcript, "--json"}
			if mode == "main" {
				code, out, errs := runCapturedRoute(t, routes["block"], append(append([]string{"close"}, args...), "--dry-run")...)
				if code != 0 {
					t.Fatalf("dry run: %d %s %s", code, out, errs)
				}
				rec, _ := state.ReadRun(f.home, "r-close-fixture")
				if blockObject(blockObject(rec.Get("blocks")).Get("B1")).Get("status") != "open" {
					t.Fatal("dry run closed block")
				}
			}
			code, out, errs := runCapturedRoute(t, routes["block"], append([]string{"close"}, args...)...)
			refused := mode == "unlanded" || mode == "wrong-block" || mode == "unapproved" || mode == "unowned" || mode == "unlanded-other-worktree"
			if refused {
				want := map[string]string{
					"unlanded":                "commit is not an ancestor",
					"unlanded-other-worktree": "commit is not an ancestor",
					"wrong-block":             "commit subject must start with the block id",
					"unowned":                 "commit changes unowned file unowned.txt",
					"unapproved":              "unreviewed",
				}[mode]
				if !strings.Contains(out+errs, want) {
					t.Fatalf("%s: wanted %q, got exit=%d out=%s err=%s", mode, want, code, out, errs)
				}
				if code == 0 {
					t.Fatalf("close accepted %s: %s %s", mode, out, errs)
				}
				rec, _ := state.ReadRun(f.home, "r-close-fixture")
				if blockObject(blockObject(rec.Get("blocks")).Get("B1")).Get("status") != "open" {
					t.Fatal("refusal mutated block")
				}
				return
			}
			if code != 0 {
				t.Fatalf("close exit=%d out=%s err=%s", code, out, errs)
			}
			rec, err := state.ReadRun(f.home, "r-close-fixture")
			if err != nil {
				t.Fatal(err)
			}
			entry := blockObject(blockObject(rec.Get("blocks")).Get("B1"))
			want := "closed"
			if mode == "human" {
				want = "awaiting_human_check"
			}
			if entry.Get("status") != want || entry.Get("landed_sha") != sha {
				t.Fatalf("entry=%v", entry)
			}
			rows, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge/ledger"), "fixture")
			if err != nil {
				t.Fatal(err)
			}
			audited := false
			for _, v := range rows {
				if row, ok := v.(map[string]any); ok && row["event"] == "block.close_landed" && row["landed_sha"] == sha {
					audited = true
				}
			}
			if !audited {
				t.Fatal("no landed close audit row")
			}
			if mode != "human" {
				data, err := os.ReadFile(filepath.Join(f.home, ".code-forge/runs/r-close-fixture/blocks/B1.done.json"))
				if err != nil {
					t.Fatal(err)
				}
				var done map[string]any
				if json.Unmarshal(data, &done) != nil || done["commit"] != sha {
					t.Fatalf("done=%s", data)
				}
			}
		})
	}
}
