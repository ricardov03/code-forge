package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
)

// B626: block settle clears every orchestrator blocker of a block in one call
// and names the owner's commands when only owner blockers are left.

// b626Fixture is a run with one open worktree block B1 that owns pkg/a.txt
// (changed and approved). extra, when not "", is one more file the block's
// commit adds without owning it. The test gate appends one line to argvLog
// and exits gateExit.
func b626Fixture(t *testing.T, gateExit int, extra string) *preflightFixture {
	t.Helper()
	root := t.TempDir()
	f := &preflightFixture{t: t, home: filepath.Join(root, "home"), repo: filepath.Join(root, "repo"), tree: filepath.Join(root, "tree"), argvLog: filepath.Join(root, "argv.log"), run: "r-settle"}
	for _, dir := range []string{f.home, filepath.Join(f.repo, "pkg"), filepath.Join(f.repo, "other")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	gate := "echo ran >> " + f.argvLog + "; exit " + strconv.Itoa(gateExit)
	config := `{"version":1,"gates":{"test":["/bin/sh","-c",` + strconv.Quote(gate) + `]}}`
	f.write(f.repo, ".code-forge.yml", "version: 1\nprovider: anthropic\n"+b474Levels+"gates:\n  test: [/bin/sh, -c, "+strconv.Quote(gate)+"]\n")
	f.write(f.repo, ".gitignore", ".code-forge/\n")
	f.write(f.repo, "pkg/a.txt", "old\n")
	f.write(f.repo, "other/keep.txt", "keep\n")
	f.git(f.repo, "init", "-qb", "main")
	f.git(f.repo, "add", ".")
	f.commit(f.repo, "base")
	f.base = f.git(f.repo, "rev-parse", "HEAD")
	f.git(f.repo, "worktree", "add", "-qb", "forge-b1", f.tree, f.base)
	f.write(f.tree, "pkg/a.txt", "new\n")
	f.git(f.tree, "add", "pkg/a.txt")
	if extra != "" {
		f.write(f.tree, extra, "split\n")
		f.git(f.tree, "add", extra)
	}
	f.commit(f.tree, "work")
	entry := ledger.Object{{Key: "status", Value: "open"}, {Key: "kind", Value: "code"}, {Key: "owned_files", Value: []any{"pkg/a.txt"}},
		{Key: "test_files", Value: []any{}}, {Key: "base_sha", Value: f.base}, {Key: "base_branch", Value: "main"},
		{Key: "worktree", Value: ledger.Object{{Key: "path", Value: f.tree}, {Key: "branch", Value: "forge-b1"}, {Key: "base_sha", Value: f.base}}}}
	pid := os.Getpid()
	record := ledger.Object{{Key: "run_id", Value: f.run}, {Key: "project", Value: "settle"}, {Key: "workspace", Value: f.repo},
		{Key: "status", Value: "active"}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}},
		{Key: "worker", Value: ledger.Object{{Key: "pid", Value: pid}, {Key: "started_at", Value: statusStart(float64(pid))}}}}
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	for i := range key {
		key[i] = 43
	}
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", f.run+".key"), key, 0o600); err != nil {
		t.Fatal(err)
	}
	f.setSnapshot(config, nil)
	f.sign(ledger.Object{{Key: "event", Value: "dispatch"}, {Key: "block", Value: "B1"}, {Key: "level", Value: "L2"}})
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	closeFixtureWrite(t, filepath.Join(f.repo, ".code-forge", "workers", strconv.Itoa(pid)+"-unknown.json"),
		[]byte(`{"pid":`+strconv.Itoa(pid)+`,"start_time":"unknown","run":"`+f.run+`","started_at":"`+stamp+`","heartbeat_at":"`+stamp+`"}`))
	return f
}

// b626Review records a review of file's current content: approved, or with
// the open finding F1.
func (f *preflightFixture) b626Review(file string, approved bool) {
	f.t.Helper()
	hash, err := gitx.ContentHash(f.tree, file)
	if err != nil {
		f.t.Fatal(err)
	}
	id := ticket.ReviewID(ticket.Parts{Run: f.run, Block: "B1", File: file, ContentHash: hash})
	open := []any{}
	if !approved {
		open = []any{ledger.Object{{Key: "id", Value: "F1"}, {Key: "severity", Value: "warning"}}}
	}
	row := ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: "B1"}, {Key: "file", Value: file}, {Key: "ticket", Value: id},
		{Key: "content_hash", Value: hash}, {Key: "status", Value: "reviewed"}, {Key: "approved", Value: approved}, {Key: "open", Value: open}}
	f.sign(row)
	if approved {
		row.Set("event", "review.approved")
		f.sign(row)
	}
}

// b626Reviewer is a fake reviewer on the real queue, like the run worker: it
// approves every review ticket queued in the main checkout and writes its
// done marker.
func (f *preflightFixture) b626Reviewer() {
	f.t.Helper()
	stop := make(chan struct{})
	f.t.Cleanup(func() { close(stop) })
	dir := filepath.Join(f.repo, ".code-forge", "queue")
	go func() {
		for {
			select {
			case <-stop:
				return
			case <-time.After(10 * time.Millisecond):
			}
			entries, _ := os.ReadDir(dir)
			for _, e := range entries {
				if !strings.HasSuffix(e.Name(), ".json") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(dir, e.Name()))
				var t map[string]any
				if err != nil || json.Unmarshal(data, &t) != nil || t["run"] != f.run {
					continue
				}
				id, _ := t["ticket"].(string)
				name := filepath.Join(dir, id+".done")
				if _, err := os.Stat(name); id == "" || err == nil {
					continue
				}
				row := ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: t["block"]}, {Key: "file", Value: t["file"]}, {Key: "ticket", Value: id},
					{Key: "content_hash", Value: t["content_hash"]}, {Key: "status", Value: "reviewed"}, {Key: "approved", Value: true}, {Key: "open", Value: []any{}}}
				if _, err := state.WriteSigned(f.home, f.run, row); err != nil {
					continue
				}
				row.Set("event", "review.approved")
				if _, err := state.WriteSigned(f.home, f.run, row); err != nil {
					continue
				}
				done, _ := json.Marshal(map[string]any{"ticket": id, "status": "reviewed", "approved": true})
				if os.WriteFile(name+".tmp", done, 0o600) == nil {
					_ = os.Rename(name+".tmp", name)
				}
			}
		}
	}()
}

// b626Rows are the run's ledger rows.
func (f *preflightFixture) b626Rows() []map[string]any {
	f.t.Helper()
	all, err := ledger.ReadAllRows(filepath.Join(f.home, ".code-forge", "ledger"), "settle")
	if err != nil {
		f.t.Fatal(err)
	}
	out := []map[string]any{}
	for _, v := range all {
		if row, ok := v.(map[string]any); ok && row["run"] == f.run {
			out = append(out, row)
		}
	}
	return out
}

// b626SettleRows are the block.settle rows.
func (f *preflightFixture) b626SettleRows() []map[string]any {
	out := []map[string]any{}
	for _, row := range f.b626Rows() {
		if row["event"] == "block.settle" {
			out = append(out, row)
		}
	}
	return out
}

// b626Settle runs block settle --json and decodes its one JSON line.
func (f *preflightFixture) b626Settle() (int, map[string]any, string) {
	f.t.Helper()
	code, stdout, stderr := f.call("block", "settle", "B1", "--run", f.run, "--json")
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || strings.Count(stdout, "\n") != 1 {
		f.t.Fatalf("settle --json: want one JSON line, exit %d err %v\nstdout=%q\nstderr=%q", code, err, stdout, stderr)
	}
	return code, out, stderr
}

// b626HumanEvents are the rows only a human-only verb (close, waive, unwaive,
// reopen, human-pass, ack-waivers, accept rule break, amend acceptance)
// writes.
var b626HumanEvents = map[string]bool{"block.close": true, "review.waived": true, "waived": true, "review.unwaived": true, "review.reopened": true,
	"block.human_pass": true, "agent_waivers.acked": true, "rule_break.accepted": true, "block.acceptance_amended": true, "autopilot.close": true,
	"gate.reviews_waived": true, "gate.review_waived_all": true}

// b626NoHumanVerb proves settle ran no human-only verb: no such row, and the
// block is still open.
func (f *preflightFixture) b626NoHumanVerb() {
	f.t.Helper()
	for _, row := range f.b626Rows() {
		if event, _ := row["event"].(string); b626HumanEvents[event] {
			f.t.Fatalf("settle wrote the human-only row %v", row)
		}
	}
	record, err := state.ReadRun(f.home, f.run)
	if err != nil {
		f.t.Fatal(err)
	}
	if status := blockObject(blockObject(record.Get("blocks")).Get("B1")).Get("status"); status != "open" {
		f.t.Fatalf("settle changed the block status to %v", status)
	}
}

func b626Fixes(row map[string]any) []string {
	out := []string{}
	actions, _ := row["actions"].([]any)
	for _, v := range actions {
		a, _ := v.(map[string]any)
		out = append(out, a["fix"].(string)+":"+a["result"].(string))
	}
	return out
}

// Case 1: an unowned split file under an owned folder and no green gates.
// settle claims the file, runs the gates, waits for its review, and exits 0
// with one block.settle row; the close dry run then passes.
func TestB626SettleClearsSplitFileAndGates(t *testing.T) {
	f := b626Fixture(t, 0, "pkg/b.txt")
	f.b626Review("pkg/a.txt", true)
	code, stdout, _ := f.call("block", "close", "B1", "--run", f.run, "--dry-run", "--json")
	before := b622Parse(t, code, stdout, "")
	if before.find("unowned_change") == nil || before.find("no_green_gates") == nil {
		t.Fatalf("fixture: want unowned_change and no_green_gates before settle, got %+v", before.Blockers)
	}
	f.b626Reviewer()
	code, out, stderr := f.b626Settle()
	if code != 0 || out["outcome"] != "settled" || len(out["remaining"].([]any)) != 0 {
		t.Fatalf("settle: want exit 0 settled with nothing left, got %d %v\nstderr=%q", code, out, stderr)
	}
	rows := f.b626SettleRows()
	if len(rows) != 1 {
		t.Fatalf("want one block.settle row, got %d", len(rows))
	}
	if got := strings.Join(b626Fixes(rows[0]), ","); got != "claim:ok,gates:ok,reviews:ok" {
		t.Fatalf("settle row actions %s, want claim:ok,gates:ok,reviews:ok (%v)", got, rows[0])
	}
	if rows[0]["outcome"] != "settled" || rows[0]["exit"] != float64(0) || rows[0]["block"] != "B1" {
		t.Fatalf("settle row: %v", rows[0])
	}
	if f.gateRuns() != 1 {
		t.Fatalf("the test gate ran %d times, want 1", f.gateRuns())
	}
	record, err := state.ReadRun(f.home, f.run)
	if err != nil {
		t.Fatal(err)
	}
	ownedNow, _ := blockObject(blockObject(record.Get("blocks")).Get("B1")).Get("owned_files").([]any)
	if len(ownedNow) != 2 || ownedNow[1] != "pkg/b.txt" {
		t.Fatalf("owned files after settle: %v, want pkg/a.txt and pkg/b.txt", ownedNow)
	}
	code, stdout, stderr = f.call("block", "close", "B1", "--run", f.run, "--dry-run", "--json")
	after := b622Parse(t, code, stdout, stderr)
	if code != 0 || !after.Ready {
		t.Fatalf("close dry run after settle: want exit 0 ready, got %d %+v", code, after)
	}
	f.b626NoHumanVerb()
	// No owner line: nothing is left for the owner.
	if _, text, _ := f.call("status", "--run", f.run); strings.Contains(text, "owner: block B1") {
		t.Fatalf("status shows an owner line for a settled block:\n%s", text)
	}
}

// Case 2: only owner blockers left (an open finding: not_approved). settle
// runs the gates, exits 3, names the owner's waive, and status, autopilot
// status and the binnacle show the one owner line.
func TestB626SettleOwnerOnlyExit3(t *testing.T) {
	f := b626Fixture(t, 0, "")
	f.b626Review("pkg/a.txt", false)
	code, out, stderr := f.b626Settle()
	if code != 3 || out["outcome"] != "owner" {
		t.Fatalf("settle: want exit 3 owner, got %d %v\nstderr=%q", code, out, stderr)
	}
	// Left: the owner's not_approved, and at most the missing approval of
	// the same file, which only the owner's waive (or reopen) can clear.
	remaining := out["remaining"].([]any)
	ownerFiles := map[any]bool{}
	for _, v := range remaining {
		if b := v.(map[string]any); b["clears_by"] == "owner" {
			ownerFiles[b["file"]] = true
		}
	}
	if !ownerFiles["pkg/a.txt"] {
		t.Fatalf("want the owner blocker on pkg/a.txt, got %v", remaining)
	}
	for _, v := range remaining {
		if b := v.(map[string]any); b["clears_by"] != "owner" && (b["code"] != "unreviewed" || !ownerFiles[b["file"]]) {
			t.Fatalf("an orchestrator blocker is left: %v", b)
		}
	}
	line, _ := out["owner_line"].(string)
	if !strings.HasPrefix(line, "owner: block B1 needs you: code-forge block waive B1 F1 ") {
		t.Fatalf("owner line %q", line)
	}
	rows := f.b626SettleRows()
	if len(rows) != 1 || rows[0]["outcome"] != "owner" || rows[0]["exit"] != float64(3) || strings.Join(b626Fixes(rows[0]), ",") != "gates:ok" {
		t.Fatalf("settle rows: %v", rows)
	}
	f.b626NoHumanVerb()
	// block status (the run status) shows the line, in text and JSON.
	if code, text, errs := f.call("status", "--run", f.run); code != 0 || !strings.Contains(text, "\n"+line+"\n") {
		t.Fatalf("status: exit %d, want the owner line %q in\n%s\nstderr=%q", code, line, text, errs)
	}
	_, text, _ := f.call("status", "--run", f.run, "--json")
	if !strings.Contains(text, `"owner_actions":[`+strconv.Quote(line)+`]`) {
		t.Fatalf("status --json: want owner_actions [%q] in %s", line, text)
	}
	f.setSnapshot(`{"version":1,"gates":{"test":["/bin/sh","-c",`+strconv.Quote("echo ran >> "+f.argvLog+"; exit 0")+`]}}`, b622Grant())
	if code, text, errs := f.call("autopilot", "status", "--run", f.run); code != 0 || !strings.Contains(text, "  "+line+"\n") {
		t.Fatalf("autopilot status: exit %d, want the owner line in\n%s\nstderr=%q", code, text, errs)
	}
	_, text, _ = f.call("autopilot", "binnacle", "--run", f.run, "--json")
	var binnacle map[string]any
	if err := json.Unmarshal([]byte(text), &binnacle); err != nil {
		t.Fatalf("binnacle --json: %v\n%s", err, text)
	}
	found := 0
	for _, v := range binnacle["owner_actions"].([]any) {
		if v.(map[string]any)["text"] == line {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("binnacle owner_actions: want the owner line once, got %v", binnacle["owner_actions"])
	}
}

// Case 3: a fix fails (the test gate exits 1). settle stops with exit 1, the
// row names the failed fix, and no owner line is shown while an orchestrator
// blocker is left.
func TestB626SettleFailedFixExit1(t *testing.T) {
	f := b626Fixture(t, 1, "")
	f.b626Review("pkg/a.txt", true)
	code, out, stderr := f.b626Settle()
	if code != 1 || out["outcome"] != "failed" || out["failed_fix"] != "gates" {
		t.Fatalf("settle: want exit 1 failed gates, got %d %v\nstderr=%q", code, out, stderr)
	}
	rows := f.b626SettleRows()
	if len(rows) != 1 || rows[0]["failed_fix"] != "gates" || rows[0]["exit"] != float64(1) || strings.Join(b626Fixes(rows[0]), ",") != "gates:failed" {
		t.Fatalf("settle rows: %v", rows)
	}
	f.b626NoHumanVerb()
	if _, text, _ := f.call("status", "--run", f.run); strings.Contains(text, "owner: block B1") {
		t.Fatalf("status shows an owner line while an orchestrator blocker is left:\n%s", text)
	}
	// The text form names the failed fix too.
	code, text, _ := f.call("block", "settle", "B1", "--run", f.run)
	if code != 1 || !strings.Contains(text, "block B1 not settled: the gates fix failed: ") || !strings.Contains(text, "no_green_gates") {
		t.Fatalf("text settle: exit %d\n%s", code, text)
	}
}

// Case 4: no progress. Green gates are recorded; the one blocker left, an
// unowned change outside every owned folder, has no settle fix: settle stops
// at once (no round, no loop, nothing run), exits 1 and says so.
func TestB626SettleNoProgressStops(t *testing.T) {
	f := b626Fixture(t, 0, "other/x.txt")
	f.b626Review("pkg/a.txt", true)
	if code, _, stderr := f.call("gates", "run", "--run", f.run, "--block", "B1", "--cwd", f.tree); code != 0 || f.gateRuns() != 1 {
		t.Fatalf("fixture gates: exit %d runs %d stderr=%q", code, f.gateRuns(), stderr)
	}
	code, out, stderr := f.b626Settle()
	if code != 1 || out["outcome"] != "no_progress" || out["rounds"] != float64(0) {
		t.Fatalf("settle: want exit 1 no_progress after 0 rounds, got %d %v\nstderr=%q", code, out, stderr)
	}
	if detail, _ := out["detail"].(string); !strings.Contains(detail, "unowned_change") {
		t.Fatalf("detail %q should name the blocker settle cannot fix", detail)
	}
	rows := f.b626SettleRows()
	if len(rows) != 1 || rows[0]["outcome"] != "no_progress" || len(b626Fixes(rows[0])) != 0 {
		t.Fatalf("settle rows: %v", rows)
	}
	if f.gateRuns() != 1 {
		t.Fatalf("the gate ran %d times; a no-progress settle runs nothing", f.gateRuns()-1)
	}
	f.b626NoHumanVerb()
}

// Usage: a missing --run and an unknown flag exit 2 and write nothing;
// --help prints the usage.
func TestB626SettleUsage(t *testing.T) {
	f := b626Fixture(t, 0, "")
	for _, args := range [][]string{{"block", "settle", "B1"}, {"block", "settle", "B1", "--run", f.run, "--max", "3"}} {
		code, stdout, stderr := f.call(args...)
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "block settle: ") {
			t.Fatalf("%v: exit %d stdout=%q stderr=%q", args, code, stdout, stderr)
		}
	}
	code, stdout, _ := f.call("block", "settle", "--help")
	if code != 0 || !strings.HasPrefix(stdout, "usage: code-forge block settle <id> --run <r>") {
		t.Fatalf("help: exit %d stdout=%q", code, stdout)
	}
	if rows := f.b626SettleRows(); len(rows) != 0 {
		t.Fatalf("usage wrote %d settle rows", len(rows))
	}
}
