package gatecheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
)

// b365Gate builds the B365 fixture: block B1 owns src/a.mjs and src/b.mjs,
// src/b.mjs is changed and approved, and prepare sets up src/a.mjs (given the
// repo and src/a.mjs's base hash) and returns its review rows. It returns the
// close file set and the gate's blockers as "code file finding".
func b365Gate(t *testing.T, prepare func(repo, baseHash string) []Row) ([]string, []string) {
	t.Helper()
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := gitx.Run(repo, args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return out
	}
	write := func(file, text string) {
		t.Helper()
		full := filepath.Join(repo, file)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitInit := exec.Command("git", "init", "-q", repo)
	gitInit.Env = gitx.ChildEnv()
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	write("src/a.mjs", "export const a = 1;\n")
	write("src/b.mjs", "export const b = 1;\n")
	git("add", "-A")
	git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")
	base := strings.TrimSpace(git("rev-parse", "HEAD"))
	baseHash, err := gitx.ContentHash(repo, "src/a.mjs")
	if err != nil {
		t.Fatal(err)
	}
	write("src/b.mjs", "export const b = 2;\n")
	bHash, err := gitx.ContentHash(repo, "src/b.mjs")
	if err != nil {
		t.Fatal(err)
	}
	key := []byte(strings.Repeat("k", 32))
	raw := append([]Row{{"event": "review.approved", "file": "src/b.mjs", "content_hash": bHash}}, prepare(repo, baseHash)...)
	rows := []Row{}
	for _, r := range raw {
		r["run"], r["block"] = "r1", "B1"
		signed, err := sign.SignRow(r, key)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, signed)
	}
	block := ledger.Object{{Key: "owned_files", Value: []any{"src/a.mjs", "src/b.mjs"}}}
	changed, err := gitx.ChangedFiles(repo, base)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := FileSet(repo, base, "", "B1", block, rows, changed)
	if err != nil {
		t.Fatal(err)
	}
	set := []string{}
	for _, f := range files {
		set = append(set, f.File)
	}
	blockers := []string{}
	for _, b := range Check("r1", "B1", files, rows, key, []any{}, []string{}, false).Blockers {
		blockers = append(blockers, strings.TrimSpace(b.Code+" "+b.File+" "+b.Finding))
	}
	return set, blockers
}

func noChangeRow(hash string) Row {
	return Row{"event": "review.result", "ticket": "no-change-ticket", "file": "src/a.mjs", "content_hash": hash, "status": "no_change", "approved": false, "open": []any{}}
}

// B365: an owned file unchanged since base whose only current-hash review row
// is a no_change result leaves the close file set; the changed file stays.
func TestB365NoChangeOnlyFileLeavesTheCloseSet(t *testing.T) {
	set, blockers := b365Gate(t, func(_, baseHash string) []Row { return []Row{noChangeRow(baseHash)} })
	if !slices.Equal(set, []string{"src/b.mjs"}) {
		t.Fatalf("file set = %v, want [src/b.mjs]", set)
	}
	if len(blockers) != 0 {
		t.Fatalf("blockers = %v, want none", blockers)
	}
}

// B365 guard: a file changed since base with only a no_change row of its old
// hash is still in the set and still unreviewed.
func TestB365ChangedFileWithStaleNoChangeIsUnreviewed(t *testing.T) {
	set, blockers := b365Gate(t, func(repo, baseHash string) []Row {
		if err := os.WriteFile(filepath.Join(repo, "src/a.mjs"), []byte("export const a = 2;\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return []Row{noChangeRow(baseHash)}
	})
	if !slices.Equal(set, []string{"src/a.mjs", "src/b.mjs"}) {
		t.Fatalf("file set = %v, want [src/a.mjs src/b.mjs]", set)
	}
	if !slices.Equal(blockers, []string{"unreviewed src/a.mjs"}) {
		t.Fatalf("blockers = %v, want [unreviewed src/a.mjs]", blockers)
	}
}

// B365 guard: a file unchanged since base whose current hash also has a
// refusal with a finding stays in the set and is still refused.
func TestB365UnchangedFileWithFindingIsRefused(t *testing.T) {
	set, blockers := b365Gate(t, func(_, baseHash string) []Row {
		refusal := Row{"event": "review.result", "ticket": "finding-ticket", "file": "src/a.mjs", "content_hash": baseHash, "status": "reviewed", "approved": false, "open": []any{map[string]any{"id": "F1", "severity": "warning"}}}
		return []Row{noChangeRow(baseHash), refusal}
	})
	if !slices.Equal(set, []string{"src/a.mjs", "src/b.mjs"}) {
		t.Fatalf("file set = %v, want [src/a.mjs src/b.mjs]", set)
	}
	if !slices.Equal(blockers, []string{"unreviewed src/a.mjs", "not_approved src/a.mjs F1"}) {
		t.Fatalf("blockers = %v, want [unreviewed src/a.mjs, not_approved src/a.mjs F1]", blockers)
	}
}

// B365: a block with an empty diff since base whose only review of its owned
// file is a no_change result has no reviewed work, so the close refuses
// no_changes; a refusal row of the same bytes keeps the file as work.
func TestB365NoChangeOnlyIsNoWork(t *testing.T) {
	repo := t.TempDir()
	gitInit := exec.Command("git", "init", "-q", repo)
	gitInit.Env = gitx.ChildEnv()
	if out, err := gitInit.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src/a.mjs"), []byte("export const a = 1;\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base"}} {
		if _, err := gitx.Run(repo, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	base, err := gitx.Run(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	base = strings.TrimSpace(base)
	hash, err := gitx.ContentHash(repo, "src/a.mjs")
	if err != nil {
		t.Fatal(err)
	}
	key := []byte(strings.Repeat("k", 32))
	signAll := func(raw ...Row) []Row {
		rows := []Row{}
		for _, r := range raw {
			r["run"], r["block"] = "r1", "B1"
			signed, err := sign.SignRow(r, key)
			if err != nil {
				t.Fatal(err)
			}
			rows = append(rows, signed)
		}
		return rows
	}
	block := ledger.Object{{Key: "owned_files", Value: []any{"src/a.mjs"}}}
	gate := func(rows []Row) ([]string, []string) {
		changed, err := gitx.ChangedFiles(repo, base)
		if err != nil {
			t.Fatal(err)
		}
		files, _, err := FileSet(repo, base, "", "B1", block, rows, changed)
		if err != nil {
			t.Fatal(err)
		}
		work := ReviewedWork(repo, rows, "B1")
		// the close's own rule (block_ready.go, block_close.go)
		noChanges := len(files) == 0 && !slices.ContainsFunc(work, func(f string) bool { return Owns("", block, f) })
		blockers := []string{}
		for _, b := range Check("r1", "B1", files, rows, key, []any{}, []string{}, noChanges).Blockers {
			blockers = append(blockers, strings.TrimSpace(b.Code+" "+b.File+" "+b.Finding))
		}
		return work, blockers
	}
	work, blockers := gate(signAll(noChangeRow(hash)))
	if len(work) != 0 || !slices.Equal(blockers, []string{"no_changes"}) {
		t.Fatalf("no_change only: work = %v, blockers = %v; want [], [no_changes]", work, blockers)
	}
	refusal := Row{"event": "review.result", "ticket": "finding-ticket", "file": "src/a.mjs", "content_hash": hash, "status": "reviewed", "approved": false, "open": []any{map[string]any{"id": "F1", "severity": "warning"}}}
	work, blockers = gate(signAll(noChangeRow(hash), refusal))
	if !slices.Equal(work, []string{"src/a.mjs"}) || !slices.Equal(blockers, []string{"unreviewed src/a.mjs", "not_approved src/a.mjs F1"}) {
		t.Fatalf("with a refusal: work = %v, blockers = %v; want [src/a.mjs], [unreviewed src/a.mjs, not_approved src/a.mjs F1]", work, blockers)
	}
}
