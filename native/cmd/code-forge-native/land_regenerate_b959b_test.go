package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B959b: land.regenerate runs once on a batch land's combined tip, in the
// main checkout, before the push; what it changes is one commit on main.

// snapshotWithLand records the fixture's config plus this land section as
// the run-start config snapshot (hash-checked like a real run).
func (f *landBatchFixture) snapshotWithLand(land map[string]any) {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.repo, ".code-forge.yml"))
	if err != nil {
		f.t.Fatal(err)
	}
	cfg := map[string]any{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		f.t.Fatal(err)
	}
	cfg["land"] = land
	data, _ = json.Marshal(cfg)
	snapshot, err := ledger.DecodeObject(data)
	if err != nil {
		f.t.Fatal(err)
	}
	canonical, err := canon.Marshal(snapshot)
	if err != nil {
		f.t.Fatal(err)
	}
	sum := sha256.Sum256(canonical)
	hash := hex.EncodeToString(sum[:])
	record, err := state.ReadRun(f.home, "r-batch-unit")
	if err != nil {
		f.t.Fatal(err)
	}
	record.Set("config", ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: snapshot}}}})
	if err := state.SaveRun(f.home, record); err != nil {
		f.t.Fatal(err)
	}
}

func TestB959bRegenerateCommitsOnce(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	bare := f.bareRemote()
	f.snapshotWithLand(map[string]any{"push": true, "regenerate": []any{
		"printf 'gen\\n' > generated.txt",
		"printf 'two\\n' >> generated.txt",
	}})
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	if !strings.Contains(output, "regenerate: committed 1 files as ") {
		t.Errorf("no regenerate line: %s", output)
	}
	data, err := os.ReadFile(filepath.Join(f.repo, "generated.txt"))
	if err != nil || string(data) != "gen\ntwo\n" {
		t.Fatalf("generated.txt %q %v, want both commands once, in order", data, err)
	}
	subject := f.git(f.repo, "log", "-1", "--format=%s", "main")
	if subject != "regenerate after landing B1 B2" {
		t.Fatalf("main tip subject %q", subject)
	}
	if files := f.git(f.repo, "show", "--name-only", "--format=", "main"); files != "generated.txt" {
		t.Errorf("regenerate commit files %q", files)
	}
	if n := f.git(f.repo, "rev-list", "--count", "--grep=^regenerate after landing", "main"); n != "1" {
		t.Errorf("%s regenerate commits, want 1", n)
	}
	for _, id := range []string{"B1", "B2"} {
		if f.entry(id).Get("landed") == nil {
			t.Errorf("%s not landed", id)
		}
	}
	if status := f.git(f.repo, "status", "--porcelain"); status != "" {
		t.Errorf("main checkout not clean: %q", status)
	}
	if got, want := f.git(bare, "rev-parse", "refs/heads/main"), f.git(f.repo, "rev-parse", "main"); got != want {
		t.Errorf("remote main=%s, want %s (the regenerate commit is pushed)", got, want)
	}
}

func TestB959bRegenerateNoChangeNoCommit(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	f.snapshotWithLand(map[string]any{"regenerate": []any{"true"}})
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	if !strings.Contains(output, "regenerate: nothing changed\n") {
		t.Errorf("no nothing-changed line: %s", output)
	}
	if n := f.git(f.repo, "rev-list", "--count", "--grep=^regenerate after landing", "main"); n != "0" {
		t.Errorf("%s regenerate commits, want 0", n)
	}
}

func TestB959bRegenerateFailureStopsBeforePush(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	bare := f.bareRemote()
	before := f.git(bare, "rev-parse", "refs/heads/main")
	f.snapshotWithLand(map[string]any{"push": true, "regenerate": []any{"echo boom >&2; exit 3", "touch never-ran"}})
	code, output := f.land("B1", "B2")
	if code != 1 {
		t.Fatalf("exit=%d, want 1: %s", code, output)
	}
	if !strings.Contains(output, "land stopped at regenerate: land.regenerate: `echo boom >&2; exit 3` failed") || !strings.Contains(output, "boom") {
		t.Errorf("stop line: %s", output)
	}
	if _, err := os.Stat(filepath.Join(f.repo, "never-ran")); err == nil {
		t.Errorf("a command after the failing one ran")
	}
	if got := f.git(bare, "rev-parse", "refs/heads/main"); got != before {
		t.Errorf("remote main moved to %s; the push must not run", got)
	}
	if f.entry("B1").Get("landed") == nil || f.entry("B2").Get("landed") == nil {
		t.Errorf("the landed merge must stay on main")
	}
}

func TestB959bNoKeyNoRun(t *testing.T) {
	for name, land := range map[string]map[string]any{"no key": {"push": false}, "empty list": {"regenerate": []any{}}} {
		f := newLandBatchFixture(t, 0)
		f.snapshotWithLand(land)
		code, output := f.land("B1", "B2")
		if code != 0 || strings.Contains(output, "regenerate") {
			t.Fatalf("%s: exit=%d, output %s", name, code, output)
		}
		if n := f.git(f.repo, "rev-list", "--count", "--grep=^regenerate after landing", "main"); n != "0" {
			t.Errorf("%s: %s regenerate commits, want 0", name, n)
		}
	}
}

func TestB959bInitWritesRegenerateDefault(t *testing.T) {
	got := initNewKeys(ledger.Object{})
	land, _ := got.Get("land").(ledger.Object)
	list, ok := land.Get("regenerate").([]any)
	if !ok || len(list) != 0 {
		t.Fatalf("new config land.regenerate = %#v, want []", land.Get("regenerate"))
	}
	kept := initNewKeys(ledger.Object{{Key: "land", Value: ledger.Object{{Key: "regenerate", Value: []any{"sh scripts/cf-release.sh regen-docs"}}}}})
	land, _ = kept.Get("land").(ledger.Object)
	if list, _ := land.Get("regenerate").([]any); len(list) != 1 || list[0] != "sh scripts/cf-release.sh regen-docs" {
		t.Fatalf("kept land.regenerate = %#v", land.Get("regenerate"))
	}
}

// config fix (B791) keeps land.regenerate as written: it is a current key.
func TestB959bConfigFixKeepsRegenerate(t *testing.T) {
	keys := "land:\n  regenerate:\n    - sh scripts/cf-release.sh regen-docs # once per batch\n"
	old := b791Old + keys
	_, repo := smallProject(t, old)
	file := filepath.Join(repo, ".code-forge.yml")
	checkSmallCases(t, []smallCase{
		{[]string{"config", "fix"}, 0, b791Fix + file + ": fixed (1 rule); run code-forge validate to check it\n", ""},
		{[]string{"config", "fix"}, 0, file + ": config ok\n", ""},
	})
	b791Unchanged(t, file, strings.Replace(old, "    invariants: scripts/inv.sh # old\n", "", 1))
}
