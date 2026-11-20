package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockopen"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B800: work found during a run becomes a block of the same run.

func b800Fixture(t *testing.T, extra map[string]any) *hvFixture {
	t.Helper()
	f := newHVFixture(t, map[string]any{"status": "closed"}, extra)
	t.Setenv("HOME", f.home)
	before := autopilotNow
	autopilotNow = func() time.Time { return time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { autopilotNow = before })
	return f
}

// b800WithCap is a run whose config snapshot sets autopilot.max_found_blocks.
func b800WithCap(n int) map[string]any {
	cfg := map[string]any{"version": float64(1), "autopilot": map[string]any{"max_found_blocks": float64(n)}}
	h := config.Hash(cfg)
	return map[string]any{"config": map[string]any{"hash": h, "snapshots": map[string]any{h: cfg}}}
}

func b800Found(t *testing.T, f *hvFixture) []map[string]any {
	t.Helper()
	list, _ := f.record()["found"].([]any)
	out := []map[string]any{}
	for _, item := range list {
		out = append(out, item.(map[string]any))
	}
	return out
}

func b800Rows(t *testing.T, f *hvFixture) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, row := range f.ledger() {
		if row["event"] == "autopilot.found" {
			out = append(out, row)
		}
	}
	return out
}

func b800Planned(t *testing.T, f *hvFixture) []string {
	t.Helper()
	record, err := state.ReadRun(f.home, "r-test")
	if err != nil {
		t.Fatal(err)
	}
	grant, _ := record.Get("autopilot").(ledger.Object)
	return autopilot.Planned(record, grant)
}

func b800Report(t *testing.T, text string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "coder-report.txt")
	if err := os.WriteFile(file, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

const b800CoderReport = "Done with the block.\n" +
	"TESTS go test ./x exit=0\n" +
	"- FOUND flaky [customer-breaking] TestLandRetry fails one run in five :: --- FAIL: TestLandRetry (0.31s) land_test.go:88: lock busy\n" +
	"FOUND [docs,customer-breaking] commands.md misses the extend row :: docs/reference/commands.md has no autopilot extend line\n" +
	"FOUND a line without evidence\n" +
	"===BLOCK B1 COMPLETE===\n"

// A coder report with two leftovers queues two blocks with the next free
// ids, briefs and acceptance files, and both join the planned blocks.
func TestB800CoderLeftoversQueueBlocks(t *testing.T) {
	f := b800Fixture(t, map[string]any{"autopilot": map[string]any{"grant_id": "ap-1", "status": "active", "until_done": true, "planned_blocks": []any{"B1"}}})
	code, out, errText := autopilotCall(t, "found", "--run", "r-test", "--report", b800Report(t, b800CoderReport), "--from", "B1")
	if code != 0 {
		t.Fatalf("found = %d %q %q", code, out, errText)
	}
	if !strings.Contains(errText, "WARN found: report line 5 skipped") {
		t.Fatalf("no WARN for the line without evidence: %q", errText)
	}
	found := b800Found(t, f)
	if len(found) != 2 {
		t.Fatalf("found = %v", found)
	}
	want := []struct{ block, source, title string }{
		{"B2", "flaky", "TestLandRetry fails one run in five"},
		{"B3", "coder", "commands.md misses the extend row"},
	}
	for i, w := range want {
		e := found[i]
		if e["block"] != w.block || e["state"] != "queued" || e["source"] != w.source || e["title"] != w.title || e["from_block"] != "B1" {
			t.Fatalf("found[%d] = %v", i, e)
		}
		brief, _ := os.ReadFile(e["brief"].(string))
		if !strings.Contains(string(brief), "lock busy") && w.block == "B2" || !strings.Contains(string(brief), "red") {
			t.Fatalf("brief %s = %q", w.block, brief)
		}
		accept, _ := os.ReadFile(e["acceptance"].(string))
		parsed, err := blockopen.ParseAcceptance(accept)
		if err == nil {
			_, err = blockopen.Acceptance(parsed)
		}
		if clauses, _ := parsed.([]any); err != nil || len(clauses) != 1 {
			t.Fatalf("acceptance %s = %q (%v)", w.block, accept, err)
		}
		if !strings.Contains(out, w.block+" queued") || !strings.Contains(out, "code-forge block open "+w.block+" --run r-test") {
			t.Fatalf("out = %q", out)
		}
	}
	if labels := found[1]["labels"].([]any); len(labels) != 1 || labels[0] != "docs" {
		t.Fatalf("labels = %v", labels)
	}
	if got := b800Planned(t, f); !slices.Equal(got, []string{"B1", "B2", "B3"}) {
		t.Fatalf("planned = %v", got)
	}
	rows := b800Rows(t, f)
	if len(rows) != 2 || rows[0]["block"] != "B2" || rows[1]["block"] != "B3" || rows[0]["mac"] == nil {
		t.Fatalf("rows = %v", rows)
	}
	// the until-done grant is not done while B2 and B3 are open
	record, _ := state.ReadRun(f.home, "r-test")
	if autopilot.RunDone(record, record.Get("autopilot").(ledger.Object)) {
		t.Fatal("RunDone with queued found blocks")
	}
}

// The same finding twice (other times and numbers in the evidence) is
// queued once; the second call counts it as seen again.
func TestB800DuplicateAddedOnce(t *testing.T) {
	f := b800Fixture(t, nil)
	args := func(evidence string) []string {
		return []string{"found", "--run", "r-test", "--source", "gate", "--title", "go vet fails on autopilot_found.go", "--severity", "customer-breaking", "--evidence", evidence}
	}
	if code, out, errText := autopilotCall(t, args("vet: autopilot_found.go:41:2: unreachable code (0.42s)")...); code != 0 {
		t.Fatalf("first = %d %q %q", code, out, errText)
	}
	code, out, errText := autopilotCall(t, args("vet: autopilot_found.go:57:2: unreachable code (1.07s)")...)
	if code != 0 || !strings.Contains(out, "again (seen 2 times) → B2") {
		t.Fatalf("second = %d %q %q", code, out, errText)
	}
	found := b800Found(t, f)
	if len(found) != 1 || found[0]["block"] != "B2" || found[0]["seen"] != float64(2) {
		t.Fatalf("found = %v", found)
	}
	rows := b800Rows(t, f)
	if len(rows) != 2 || rows[1]["duplicate"] != true {
		t.Fatalf("rows = %v", rows)
	}
	if got := b800Planned(t, f); !slices.Equal(got, []string{"B1", "B2"}) {
		t.Fatalf("planned = %v", got)
	}
}

// Past autopilot.max_found_blocks a finding waits for the owner, and the
// final report lists found → block → state with it as an owner action.
func TestB800CapRespected(t *testing.T) {
	f := b800Fixture(t, b800WithCap(1))
	for _, title := range []string{"land drops the PR url", "gate log has no block id"} {
		if code, out, errText := autopilotCall(t, "found", "--run", "r-test", "--source", "land", "--title", title, "--severity", "customer-breaking", "--evidence", "seen in "+title); code != 0 {
			t.Fatalf("found = %d %q %q", code, out, errText)
		}
	}
	found := b800Found(t, f)
	if len(found) != 2 || found[0]["state"] != "queued" || found[1]["state"] != "waits_for_owner" || found[1]["block"] != nil ||
		!strings.HasPrefix(found[1]["reason"].(string), "cap: 1 ") {
		t.Fatalf("found = %v", found)
	}
	if got := b800Planned(t, f); !slices.Equal(got, []string{"B1", "B2"}) {
		t.Fatalf("planned = %v", got)
	}
	code, out, _ := autopilotCall(t, "found", "--run", "r-test", "--list")
	if code != 0 || !strings.Contains(out, "F1 land → B2 → not opened: land drops the PR url") ||
		!strings.Contains(out, "F2 land → - → waits for the owner (cap: 1 ") {
		t.Fatalf("list = %d %q", code, out)
	}
	code, out, errText := autopilotCall(t, "binnacle", "--run", "r-test", "--markdown")
	if data, err := os.ReadFile(strings.TrimSpace(out)); err == nil {
		out = string(data)
	}
	if code != 0 || !strings.Contains(out, "## Found work") || !strings.Contains(out, "| F1 | land | land drops the PR url | B2 | not opened |") ||
		!strings.Contains(out, "- [ ] Decide on found F2 (cap: 1 ") {
		t.Fatalf("binnacle = %d %q %q", code, out, errText)
	}
}

// A finding labelled security, or one that names a secret or a publish,
// is never queued: it waits for the owner and joins no plan.
func TestB800SecurityNotAutoRun(t *testing.T) {
	f := b800Fixture(t, nil)
	cases := [][]string{
		{"--source", "review", "--title", "token check is weak", "--label", "security", "--evidence", "review of auth.go"},
		{"--source", "coder", "--title", "test leaks a value", "--evidence", "output had ghp_abcdefghijklmnopqrstuvwxyz0123456789"},
		{"--source", "land", "--title", "npm publish step is missing", "--evidence", "release ran without it"},
	}
	for _, c := range cases {
		if code, out, errText := autopilotCall(t, append([]string{"found", "--run", "r-test"}, c...)...); code != 0 || !strings.Contains(out, "waits for the owner") {
			t.Fatalf("%v = %d %q %q", c, code, out, errText)
		}
	}
	found := b800Found(t, f)
	for i, e := range found {
		if e["state"] != "waits_for_owner" || e["block"] != nil || e["brief"] != nil {
			t.Fatalf("found[%d] = %v", i, e)
		}
	}
	if !strings.HasPrefix(found[0]["reason"].(string), "security") || !strings.HasPrefix(found[1]["reason"].(string), "security") ||
		!strings.HasPrefix(found[2]["reason"].(string), "publish") {
		t.Fatalf("reasons = %v", found)
	}
	if strings.Contains(found[1]["evidence"].(string), "ghp_abc") {
		t.Fatalf("secret kept: %v", found[1]["evidence"])
	}
	if got := b800Planned(t, f); !slices.Equal(got, []string{"B1"}) {
		t.Fatalf("planned = %v", got)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "runs", "r-test", "found")); !os.IsNotExist(err) {
		t.Fatalf("brief dir written: %v", err)
	}
}

// An ended run records nothing; a bad source is a usage error.
func TestB800Refusals(t *testing.T) {
	f := newHVFixture(t, nil, map[string]any{"status": "ended"})
	t.Setenv("HOME", f.home)
	code, _, errText := autopilotCall(t, "found", "--run", "r-test", "--source", "gate", "--title", "x", "--evidence", "y")
	if code != 1 || !strings.Contains(errText, "has ended") || len(b800Rows(t, f)) != 0 {
		t.Fatalf("ended = %d %q", code, errText)
	}
	code, _, errText = autopilotCall(t, "found", "--run", "r-test", "--source", "nope", "--title", "x", "--evidence", "y")
	if code != 2 || !strings.Contains(errText, "--source must be one of gate, land, coder, flaky, handover, review") {
		t.Fatalf("bad source = %d %q", code, errText)
	}
}
