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
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B960 tests: land.after runs in main after the merge, before the push, and
// every landed block gets one signed block.landed row (the binnacle row).

// b960Snapshot records the fixture's config with this land section as the
// run-start config snapshot (hash-checked like a real run).
func (f *landBatchFixture) b960Snapshot(land map[string]any) {
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

func TestB960AfterRunsAndCommits(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	bare := f.bareRemote()
	f.b960Snapshot(map[string]any{"push": true, "after": []any{
		"printf 'stamp-a\\n' >> CHANGELOG.md",
		"printf 'stamp-b\\n' >> CHANGELOG.md",
		"printf 'scratch\\n' > untracked-after.txt",
	}})
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	head := f.git(f.repo, "rev-parse", "main")
	subject := f.git(f.repo, "show", "-s", "--format=%s", head)
	if !strings.HasPrefix(subject, "land.after for B1 B2: ") {
		t.Errorf("main tip subject=%q, want the land.after commit", subject)
	}
	// The commit sits on the merges, which keep their shape.
	merged := f.git(f.repo, "rev-parse", head+"^")
	if got := f.git(f.repo, "rev-list", "--first-parent", "--count", f.base+".."+merged); got != "2" {
		t.Errorf("merges under the land.after commit=%s, want 2", got)
	}
	changelog := f.git(f.repo, "show", head+":CHANGELOG.md")
	if !strings.HasSuffix(changelog, "stamp-a\nstamp-b") {
		t.Errorf("CHANGELOG.md on main must end with the two stamps in order: %q", changelog)
	}
	if files := f.git(f.repo, "show", "--name-only", "--format=", head); files != "CHANGELOG.md" {
		t.Errorf("land.after commit files=%q, want only the tracked CHANGELOG.md", files)
	}
	if status := f.git(f.repo, "status", "--porcelain"); !strings.Contains(status, "?? untracked-after.txt") {
		t.Errorf("an untracked file the commands write stays untracked: %q", status)
	}
	// Run before the push: the remote has the land.after commit.
	if got := f.git(bare, "rev-parse", "refs/heads/main"); got != head {
		t.Errorf("remote main=%s, want %s (land.after runs before the push)", got, head)
	}
	if !strings.Contains(output, "land.after: ran printf 'stamp-a\\n' >> CHANGELOG.md") || !strings.Contains(output, "land.after: committed "+head[:12]) {
		t.Errorf("output must name each command and the commit: %s", output)
	}
	rows := f.rows("block.landed")
	if len(rows) != 2 || rows[0]["block"] != "B1" || rows[1]["block"] != "B2" {
		t.Fatalf("block.landed rows=%v, want one for B1 and one for B2", rows)
	}
	for _, row := range rows {
		after, _ := row["after"].([]any)
		if row["main"] != head || row["after_commit"] != head || len(after) != 3 {
			t.Errorf("block.landed row=%v, want main and after_commit %s and the 3 commands", row, head)
		}
	}
}

func TestB960NoAfterNoCommit(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	f.checkMain([]string{"B1", "B2"})
	if strings.Contains(output, "land.after") {
		t.Errorf("no land.after: nothing runs: %s", output)
	}
	head := f.git(f.repo, "rev-parse", "main")
	rows := f.rows("block.landed")
	if len(rows) != 2 {
		t.Fatalf("block.landed rows=%v, want 2", rows)
	}
	for _, row := range rows {
		if row["main"] != head || row["after_commit"] != nil {
			t.Errorf("block.landed row=%v, want main %s and no after_commit", row, head)
		}
	}
}

func TestB960AfterFailureStops(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	bare := f.bareRemote()
	f.b960Snapshot(map[string]any{"push": true, "after": []any{
		"echo boom-out; exit 3",
		"printf 'never\\n' >> CHANGELOG.md",
	}})
	code, output := f.land("B1", "B2")
	if code != 1 {
		t.Fatalf("exit=%d, want 1: %s", code, output)
	}
	for _, want := range []string{"block B1 land stopped at after:", `land.after command "echo boom-out; exit 3" failed`, "boom-out", "nothing was pushed"} {
		if !strings.Contains(output, want) {
			t.Errorf("output must contain %q: %s", want, output)
		}
	}
	// The merge stays on local main; the later command did not run.
	f.checkMain([]string{"B1", "B2"})
	// No commit is made on a failure, so look at the checkout itself.
	if changelog, err := os.ReadFile(filepath.Join(f.repo, "CHANGELOG.md")); err != nil || strings.Contains(string(changelog), "never") {
		t.Errorf("a command after the failed one ran (or CHANGELOG.md unreadable: %v): %q", err, changelog)
	}
	if got := f.git(bare, "rev-parse", "refs/heads/main"); got != f.base {
		t.Errorf("remote main=%s, want it unchanged at %s", got, f.base)
	}
	if rows := f.rows("block.landed"); len(rows) != 0 {
		t.Errorf("block.landed rows=%v, want none while land.after failed", rows)
	}
}

func TestB960LandedRowInBinnacle(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	if code, output := f.land("B1", "B2"); code != 0 {
		t.Fatalf("land exit=%d: %s", code, output)
	}
	head := f.git(f.repo, "rev-parse", "main")
	code, out, errText := autopilotCall(t, "log", "--run", "r-batch-unit", "--json")
	if code != 0 {
		t.Fatalf("autopilot log exit=%d: %s %s", code, out, errText)
	}
	var log []map[string]any
	if err := json.Unmarshal([]byte(out), &log); err != nil {
		t.Fatalf("autopilot log json: %v: %s", err, out)
	}
	found := []string{}
	for _, entry := range log {
		if entry["event"] == "Block landed" {
			found = append(found, entry["block"].(string))
			if entry["detail"] != "main "+head[:7] {
				t.Errorf("binnacle row=%v, want detail main %s", entry, head[:7])
			}
		}
	}
	if strings.Join(found, " ") != "B2 B1" && strings.Join(found, " ") != "B1 B2" {
		t.Errorf("Block landed rows=%v, want one for B1 and one for B2: %s", found, out)
	}
}

func TestB960AfterConfig(t *testing.T) {
	cfg := map[string]any{"land": map[string]any{"after": []any{"make stamp", "", "  ", "npm run x"}}}
	if got := landAfterCommands(cfg); strings.Join(got, "|") != "make stamp|npm run x" {
		t.Errorf("landAfterCommands=%q, want the non-empty commands in order", got)
	}
	if got := landAfterCommands(nil); len(got) != 0 {
		t.Errorf("landAfterCommands(nil)=%q, want none", got)
	}
	landIssues := func(after any) []config.SchemaIssue {
		out := []config.SchemaIssue{}
		for _, issue := range config.SchemaIssues(map[string]any{"land": map[string]any{"after": after}}, "1") {
			if strings.HasPrefix(issue.Path, "/land") {
				out = append(out, issue)
			}
		}
		return out
	}
	if issues := landIssues([]any{"make stamp"}); len(issues) != 0 {
		t.Errorf("land.after [make stamp]: schema issues %v, want none", issues)
	}
	if issues := landIssues([]any{""}); len(issues) == 0 {
		t.Errorf("land.after [\"\"]: no schema issue, want one (commands are non-empty)")
	}
	if issues := landIssues([]any{float64(1)}); len(issues) == 0 {
		t.Errorf("land.after [1]: no schema issue, want one")
	}
	if issues := landIssues("make stamp"); len(issues) == 0 {
		t.Errorf("land.after as a string: no schema issue, want one (an array)")
	}
}
