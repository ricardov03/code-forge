package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B795: land merges append-only conflicts by itself. Two blocks that each add
// a `### Fixed` bullet under [Unreleased], or a row to one docs table, land
// with no human and no repeated heading; a code conflict aborts the merge and
// leaves main as it was.

const b795Changelog = "# Changelog\n\n## [Unreleased]\n\n## [0.1.0]\n\n- first release\n"

const b795Table = "# Engine\n\n| Command | Notes |\n|---|---|\n| a | one |\n| b | two |\n\nText.\n"

func b795Fixed(id string) string {
	return strings.Replace(b795Changelog, "## [Unreleased]\n\n", "## [Unreleased]\n\n### Fixed\n\n- fix "+id+"\n\n", 1)
}

// b795Owned adds files to a batch block's owned files.
func (f *landBatchFixture) b795Owned(id string, files ...string) {
	f.t.Helper()
	if err := state.UpdateRun(f.home, "r-batch-unit", func(record *ledger.Object) error {
		blocks := blockObject(record.Get("blocks"))
		entry := blockObject(blocks.Get(id))
		owned := gatesStringsAny(entry.Get("owned_files"))
		for _, file := range files {
			owned = append(owned, file)
		}
		entry.Set("owned_files", owned)
		blocks.Set(id, entry)
		record.Set("blocks", blocks)
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
}

// Both blocks add a `### Fixed` heading with one bullet at the same place
// (B2 also adds an `### Added` group above its Fixed one): the batch lands
// both with one heading of each kind, B1's entry first.
func TestB795BatchFixedBulletsLandWithOneHeading(t *testing.T) {
	for name, b2 := range map[string]string{
		"same":  b795Fixed("B2"),
		"added": strings.Replace(b795Changelog, "## [Unreleased]\n\n", "## [Unreleased]\n\n### Added\n\n- add B2\n\n### Fixed\n\n- fix B2\n\n", 1),
	} {
		t.Run(name, func(t *testing.T) { b795BatchFixed(t, b2) })
	}
}

func b795BatchFixed(t *testing.T, b2 string) {
	f := newLandBatchFixture(t, 0)
	// The repository marks CHANGELOG.md merge=union, as code-forge's own does.
	f.write(f.repo, ".gitattributes", "CHANGELOG.md merge=union\n")
	f.write(f.repo, "CHANGELOG.md", b795Changelog)
	f.git(f.repo, "add", ".")
	f.git(f.repo, "commit", "-qm", "changelog base")
	moved := f.git(f.repo, "rev-parse", "HEAD")
	for _, id := range []string{"B1", "B2"} {
		tree := filepath.Join(f.repo, ".code-forge/worktrees", id)
		f.git(tree, "rebase", "-q", moved)
		body := b795Fixed(id)
		if id == "B2" {
			body = b2
		}
		f.write(tree, "CHANGELOG.md", body)
		f.git(tree, "commit", "-qam", id+" changelog")
		f.b795Owned(id, "CHANGELOG.md")
		if err := state.UpdateRun(f.home, "r-batch-unit", func(record *ledger.Object) error {
			blocks := blockObject(record.Get("blocks"))
			entry := blockObject(blocks.Get(id))
			entry.Set("base_sha", moved)
			wt := blockObject(entry.Get("worktree"))
			wt.Set("base_sha", moved)
			entry.Set("worktree", wt)
			blocks.Set(id, entry)
			record.Set("blocks", blocks)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.base = moved
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("batch exit=%d, want 0 with no human: %s", code, output)
	}
	f.checkMain([]string{"B1", "B2"})
	main := f.git(f.repo, "show", "main:CHANGELOG.md") + "\n"
	added := ""
	if strings.Contains(b2, "### Added") {
		added = "### Added\n\n- add B2\n\n"
	}
	want := strings.Replace(b795Changelog, "## [Unreleased]\n\n", "## [Unreleased]\n\n### Fixed\n\n- fix B1\n- fix B2\n\n"+added, 1)
	if main != want {
		t.Fatalf("main CHANGELOG.md:\n%q\nwant one heading of each kind, B1 first:\n%q", main, want)
	}
}

// A batch block whose conflict is code is parked with a land.conflict row
// naming the file and its kind.
func TestB795BatchCodeConflictWritesConflictRow(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	f.addShared(map[string]string{"B1": "one\n", "B2": "two\n"})
	code, output := f.land("B1", "B2")
	if code != 0 || !strings.Contains(output, "block B2 parked: block_rebase_conflict (notes/shared.txt)\nnext: git -C ") {
		t.Fatalf("exit=%d, want B2 parked with a next step: %s", code, output)
	}
	rows := f.rows("land.conflict")
	if len(rows) != 1 || rows[0]["block"] != "B2" || fmt.Sprint(rows[0]["files"]) != "[map[file:notes/shared.txt kind:code]]" {
		t.Fatalf("land.conflict rows=%v", rows)
	}
}

// b795Single is a closed block (a land retry: merge gate and merge only)
// whose branch and main both changed the files given.
type b795Single struct {
	*b789Fixture
}

func newB795Single(t *testing.T, mainFiles, blockFiles map[string]string, land map[string]any) b795Single {
	t.Helper()
	f := newB789Fixture(t, true, "closed")
	// Shared base for the docs; the block branch is rebased onto it.
	f.must(os.WriteFile(filepath.Join(f.repo, ".gitattributes"), []byte("CHANGELOG.md merge=union\n"), 0o600))
	f.must(os.MkdirAll(filepath.Join(f.repo, "docs"), 0o700))
	f.must(os.WriteFile(filepath.Join(f.repo, "CHANGELOG.md"), []byte(b795Changelog), 0o600))
	f.must(os.WriteFile(filepath.Join(f.repo, "docs/native-engine.md"), []byte(b795Table), 0o600))
	f.git(f.repo, "add", ".")
	f.git(f.repo, "commit", "-qm", "docs base")
	f.git(f.tree, "rebase", "-q", "main")
	write := func(dir string, files map[string]string, message string) {
		for name, body := range files {
			f.must(os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700))
			f.must(os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600))
		}
		f.git(dir, "add", ".")
		f.git(dir, "commit", "-qm", message)
	}
	write(f.tree, blockFiles, "block docs")
	write(f.repo, mainFiles, "main docs")
	done := filepath.Join(f.home, ".code-forge/runs", f.run, "blocks", "B1.done.json")
	f.must(os.MkdirAll(filepath.Dir(done), 0o700))
	f.must(os.WriteFile(done, []byte(`{"status":"closed","commit":"`+f.git(f.tree, "rev-parse", "HEAD")+`"}`), 0o600))
	if land != nil {
		snapshot := blockObject(blockObject(f.record.Get("config")).Get("snapshots"))
		var cfg map[string]any
		data, _ := json.Marshal(snapshot[0].Value)
		f.must(json.Unmarshal(data, &cfg))
		cfg["land"] = land
		data, _ = json.Marshal(cfg)
		object, err := ledger.DecodeObject(data)
		f.must(err)
		canonical, err := canon.Marshal(object)
		f.must(err)
		sum := sha256.Sum256(canonical)
		hash := hex.EncodeToString(sum[:])
		f.record.Set("config", ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: object}}}})
		f.must(state.SaveRun(f.home, f.record))
	}
	return b795Single{f}
}

func (f b795Single) rows(event string) []map[string]any {
	f.t.Helper()
	rows, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge/ledger"), "fixture")
	f.must(err)
	found := []map[string]any{}
	for _, value := range rows {
		if row := value.(map[string]any); row["event"] == event {
			found = append(found, row)
		}
	}
	return found
}

func b795Rows(rows ...string) string {
	return strings.Replace(b795Table, "| a | one |\n", "| a | one |\n"+strings.Join(rows, ""), 1)
}

func TestB795LandResolvesChangelogAndTable(t *testing.T) {
	for _, gate := range []bool{true, false} {
		t.Run(fmt.Sprintf("merge_gate=%v", gate), func(t *testing.T) {
			f := newB795Single(t,
				map[string]string{"CHANGELOG.md": b795Fixed("B0"), "docs/native-engine.md": b795Rows("| a2 | main row |\n")},
				map[string]string{"CHANGELOG.md": b795Fixed("B1"), "docs/native-engine.md": b795Rows("| a3 | block row |\n")}, nil)
			flags := map[string]any{"merge": true}
			if !gate {
				flags["no-merge-gate"] = true
			}
			mainBefore := f.git(f.repo, "rev-parse", "HEAD")
			code, output, handed := f.land(flags)
			if handed != "" || code != 0 {
				t.Fatalf("land = %d handed=%q, want 0 with no human:\n%s", code, handed, output)
			}
			changelog := f.git(f.repo, "show", "HEAD:CHANGELOG.md") + "\n"
			want := strings.Replace(b795Changelog, "## [Unreleased]\n\n", "## [Unreleased]\n\n### Fixed\n\n- fix B0\n- fix B1\n\n", 1)
			if changelog != want {
				t.Fatalf("CHANGELOG.md:\n%q\nwant:\n%q", changelog, want)
			}
			table := f.git(f.repo, "show", "HEAD:docs/native-engine.md") + "\n"
			if want := b795Rows("| a2 | main row |\n", "| a3 | block row |\n"); table != want {
				t.Fatalf("table:\n%q\nwant:\n%q", table, want)
			}
			if parents := strings.Fields(f.git(f.repo, "show", "-s", "--format=%P", "HEAD")); len(parents) != 2 || parents[0] != mainBefore || parents[1] != f.git(f.repo, "rev-parse", "block-B1") {
				t.Fatalf("merge parents=%v", parents)
			}
			if message := f.git(f.repo, "show", "-s", "--format=%B", "HEAD"); strings.Contains(message, "Conflicts") || strings.Contains(message, "#") {
				t.Fatalf("merge message keeps conflict notes: %q", message)
			}
			if status := f.git(f.repo, "status", "--porcelain"); status != "" {
				t.Fatalf("main checkout dirty: %s", status)
			}
			if rows := f.rows("land.resolved"); len(rows) != 1 || fmt.Sprint(rows[0]["files"]) != "[map[file:CHANGELOG.md kind:changelog] map[file:docs/native-engine.md kind:table]]" {
				t.Fatalf("land.resolved rows=%v", rows)
			}
		})
	}
}

func TestB795LandCodeConflictAbortsWithMainUnchanged(t *testing.T) {
	for _, gate := range []bool{true, false} {
		t.Run(fmt.Sprintf("merge_gate=%v", gate), func(t *testing.T) {
			f := newB795Single(t,
				map[string]string{"sample.mjs": "main\n", "docs/native-engine.md": b795Rows("| a2 | main row |\n")},
				map[string]string{"sample.mjs": "block\n", "docs/native-engine.md": b795Rows("| a3 | block row |\n")}, nil)
			flags := map[string]any{"merge": true}
			if !gate {
				flags["no-merge-gate"] = true
			}
			mainBefore := f.git(f.repo, "rev-parse", "HEAD")
			code, output, handed := f.land(flags)
			if handed != "" || code != 1 {
				t.Fatalf("land = %d handed=%q, want 1:\n%s", code, handed, output)
			}
			if !strings.Contains(output, "main is unchanged: docs/native-engine.md (table), sample.mjs (code)\nnext: git -C "+f.repo+" merge --no-ff --no-edit -- block-B1\n") {
				t.Fatalf("output lacks the files and one next step:\n%s", output)
			}
			if got := f.git(f.repo, "rev-parse", "HEAD"); got != mainBefore {
				t.Fatalf("main moved to %s", got)
			}
			if status := f.git(f.repo, "status", "--porcelain"); status != "" {
				t.Fatalf("main checkout left mid-merge: %s", status)
			}
			if _, err := os.Stat(filepath.Join(f.repo, ".git", "MERGE_HEAD")); !os.IsNotExist(err) {
				t.Fatalf("MERGE_HEAD left: %v", err)
			}
			rows := f.rows("land.conflict")
			if len(rows) != 1 || rows[0]["block"] != "B1" || fmt.Sprint(rows[0]["files"]) != "[map[file:docs/native-engine.md kind:table] map[file:sample.mjs kind:code]]" {
				t.Fatalf("land.conflict rows=%v", rows)
			}
		})
	}
}

// land.auto_resolve: false keeps the B431 rule only; a table conflict is then
// a real conflict and main stays as it was.
func TestB795LandAutoResolveOff(t *testing.T) {
	f := newB795Single(t,
		map[string]string{"docs/native-engine.md": b795Rows("| a2 | main row |\n")},
		map[string]string{"docs/native-engine.md": b795Rows("| a3 | block row |\n")}, map[string]any{"auto_resolve": false})
	mainBefore := f.git(f.repo, "rev-parse", "HEAD")
	code, output, _ := f.land(map[string]any{"merge": true})
	if code != 1 || !strings.Contains(output, "docs/native-engine.md (code)") {
		t.Fatalf("land = %d, want a refused code conflict:\n%s", code, output)
	}
	if got := f.git(f.repo, "rev-parse", "HEAD"); got != mainBefore || f.git(f.repo, "status", "--porcelain") != "" {
		t.Fatal("main changed")
	}
}
