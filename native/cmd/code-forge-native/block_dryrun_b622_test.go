package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
)

// B622: blocker JSON names who clears each blocker, and close/human-pass
// --dry-run preview the verb's checks without writing anything.

type b622Blocker struct {
	Code     string  `json:"code"`
	File     *string `json:"file"`
	Detail   string  `json:"detail"`
	Finding  string  `json:"finding"`
	Fix      string  `json:"fix"`
	ClearsBy *string `json:"clears_by"`
}

type b622Report struct {
	Block    string        `json:"block"`
	DryRun   bool          `json:"dry_run"`
	Verb     string        `json:"verb"`
	Ready    bool          `json:"ready"`
	Blockers []b622Blocker `json:"blockers"`
}

func b622Parse(t *testing.T, code int, stdout, stderr string) b622Report {
	t.Helper()
	var r b622Report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil || strings.Count(stdout, "\n") != 1 {
		t.Fatalf("want one JSON line: exit %d err %v\nstdout=%q\nstderr=%q", code, err, stdout, stderr)
	}
	return r
}

func (r b622Report) find(code string) *b622Blocker {
	for i := range r.Blockers {
		if r.Blockers[i].Code == code {
			return &r.Blockers[i]
		}
	}
	return nil
}

func b622Clears(b *b622Blocker) string {
	if b == nil || b.ClearsBy == nil {
		return "<none>"
	}
	return *b.ClearsBy
}

// b622Snapshot is every file and directory under the roots, with its bytes.
func b622Snapshot(t *testing.T, roots ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				out[path] = "<dir>"
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			out[path] = string(data)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func b622SameTree(t *testing.T, before, after map[string]string) {
	t.Helper()
	for path, data := range before {
		if got, ok := after[path]; !ok {
			t.Errorf("dry run removed %s", path)
		} else if got != data {
			t.Errorf("dry run changed %s", path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("dry run created %s", path)
		}
	}
}

// b622Fixture is the ready fixture (no workspace .code-forge.yml) with a
// config snapshot and no gates configured.
func b622Fixture(t *testing.T) *preflightFixture {
	t.Helper()
	f := newReadyFixture(t)
	f.setSnapshot(readyBaseConfig+"}", nil)
	return f
}

func (f *preflightFixture) b622Roots() []string { return []string{f.home, f.repo, f.tree} }

// b622Update rewrites block B1's run entry through fn.
func (f *preflightFixture) b622Update(fn func(entry *ledger.Object)) {
	f.t.Helper()
	record, err := state.ReadRun(f.home, f.run)
	if err != nil {
		f.t.Fatal(err)
	}
	blocks := blockObject(record.Get("blocks"))
	entry := blockObject(blocks.Get("B1"))
	fn(&entry)
	blocks.Set("B1", entry)
	record.Set("blocks", blocks)
	if err := state.SaveRun(f.home, record); err != nil {
		f.t.Fatal(err)
	}
}

// b622Closable pins a live worker and records an approved review of
// sample.txt's current content, so the real close passes.
func (f *preflightFixture) b622Closable() {
	f.t.Helper()
	hash, err := gitx.ContentHash(f.tree, "sample.txt")
	if err != nil {
		f.t.Fatal(err)
	}
	err = state.UpdateRun(f.home, f.run, func(record *ledger.Object) error {
		record.Set("worker", ledger.Object{{Key: "pid", Value: os.Getpid()}, {Key: "started_at", Value: statusStart(float64(os.Getpid()))}})
		return nil
	})
	if err != nil {
		f.t.Fatal(err)
	}
	id := ticket.ReviewID(ticket.Parts{Run: f.run, Block: "B1", File: "sample.txt", ContentHash: hash})
	row := ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.txt"}, {Key: "ticket", Value: id},
		{Key: "content_hash", Value: hash}, {Key: "status", Value: "reviewed"}, {Key: "approved", Value: true}, {Key: "open", Value: []any{}}}
	f.sign(row)
	row.Set("event", "review.approved")
	f.sign(row)
}

func b622Grant() ledger.Object {
	return ledger.Object{{Key: "grant_id", Value: "g1"}, {Key: "status", Value: "active"}, {Key: "until", Value: "2099-01-01T00:00:00.000Z"}, {Key: "started_at", Value: "2026-01-01T00:00:00.000Z"},
		{Key: "scopes", Value: []any{}}, {Key: "deny", Value: []any{}}, {Key: "delegate", Value: "L2"}, {Key: "caps", Value: ledger.Object{}}, {Key: "stop_at", Value: 0.8}}
}

// ready --json and the preflight name who clears each blocker: an open
// finding needs the owner's waive, a missing approval the orchestrator,
// a stale base the orchestrator's rebase --accept-landed.
func TestB622ReadyAndPreflightClearsBy(t *testing.T) {
	f := b622Fixture(t)
	f.openFinding()
	code, stdout, stderr := f.call("block", "ready", "B1", "--run", f.run, "--json")
	r := b622Parse(t, code, stdout, stderr)
	if code != 1 || r.Ready {
		t.Fatalf("ready: want exit 1 not ready, got %d %+v", code, r)
	}
	waive := r.find("not_approved")
	if b622Clears(waive) != "owner" || !strings.HasPrefix(waive.Fix, "code-forge block waive B1 F1 ") {
		t.Fatalf("not_approved: want an owner waive, got %+v (clears_by %s)", waive, b622Clears(waive))
	}
	if review := r.find("unreviewed"); b622Clears(review) != "orchestrator" || review.Fix != "code-forge review-file --wait-block B1 --run "+f.run {
		t.Fatalf("unreviewed: want orchestrator review-file, got %+v", review)
	}
	for _, b := range r.Blockers {
		if b.ClearsBy == nil || (*b.ClearsBy != "owner" && *b.ClearsBy != "orchestrator") {
			t.Fatalf("every blocker needs clears_by owner|orchestrator: %+v", b)
		}
	}
	f.moveMain()
	code, stdout, stderr = f.call("block", "ready", "B1", "--run", f.run, "--preflight", "--json")
	p := b622Parse(t, code, stdout, stderr)
	stale := p.find("stale_base")
	if code != 1 || b622Clears(stale) != "orchestrator" || stale.Fix != "code-forge block rebase B1 --run "+f.run+" --accept-landed" {
		t.Fatalf("preflight stale_base: want orchestrator rebase --accept-landed, got exit %d %+v", code, p)
	}
	if b622Clears(p.find("not_approved")) != "owner" {
		t.Fatalf("preflight not_approved: want owner, got %+v", p)
	}
}

// A close that would fail: the dry run lists every blocker with clears_by,
// exits 1, and the ledger, run record, home and both trees stay byte-equal.
func TestB622CloseDryRunBlockedWritesNothing(t *testing.T) {
	f := b622Fixture(t)
	f.openFinding()
	before := b622Snapshot(t, f.b622Roots()...)
	code, stdout, stderr := f.call("block", "close", "B1", "--run", f.run, "--dry-run", "--json")
	r := b622Parse(t, code, stdout, stderr)
	if code != 1 || r.Block != "B1" || !r.DryRun || r.Verb != "close" || r.Ready || stderr != "" {
		t.Fatalf("blocked dry run: want exit 1, dry_run close not ready, no stderr; got %d %+v stderr=%q", code, r, stderr)
	}
	if b622Clears(r.find("not_approved")) != "owner" || b622Clears(r.find("unreviewed")) != "orchestrator" || b622Clears(r.find("worker.down")) != "orchestrator" {
		t.Fatalf("blocked dry run blockers: %+v", r.Blockers)
	}
	b622SameTree(t, before, b622Snapshot(t, f.b622Roots()...))
	// The text form lists the same blockers.
	code, stdout, _ = f.call("block", "close", "B1", "--run", f.run, "--dry-run")
	if code != 1 || !strings.Contains(stdout, "  clears by: owner\n") || !strings.HasSuffix(stdout, "block B1 close dry run: would not pass, 3 blockers (nothing was recorded)\n") {
		t.Fatalf("text dry run: exit %d stdout=%q", code, stdout)
	}
	b622SameTree(t, before, b622Snapshot(t, f.b622Roots()...))
}

// A close that would pass: the dry run exits 0 with no blockers and writes
// nothing; the real close then passes too.
func TestB622CloseDryRunPassesThenRealClose(t *testing.T) {
	f := b622Fixture(t)
	f.b622Closable()
	before := b622Snapshot(t, f.b622Roots()...)
	code, stdout, stderr := f.call("block", "close", "B1", "--run", f.run, "--dry-run", "--json")
	r := b622Parse(t, code, stdout, stderr)
	if code != 0 || !r.Ready || len(r.Blockers) != 0 || stderr != "" {
		t.Fatalf("passing dry run: want exit 0 ready, got %d %+v stderr=%q", code, r, stderr)
	}
	b622SameTree(t, before, b622Snapshot(t, f.b622Roots()...))
	code, stdout, stderr = f.call("block", "close", "B1", "--run", f.run, "--json")
	if code != 0 || !strings.Contains(stdout, `"status":"closed"`) {
		t.Fatalf("real close after a passing dry run: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
}

// close.require_human with no grant: the dry run never asks; the owner's
// confirmation is one owner blocker.
func TestB622CloseDryRunRequireHumanIsOwnerBlocker(t *testing.T) {
	f := b622Fixture(t)
	f.b622Closable()
	f.setSnapshot(readyBaseConfig+`,"close":{"require_human":true}}`, nil)
	before := b622Snapshot(t, f.b622Roots()...)
	code, stdout, stderr := f.call("block", "close", "B1", "--run", f.run, "--dry-run", "--json")
	r := b622Parse(t, code, stdout, stderr)
	owner := r.find("owner_confirm")
	if code != 1 || len(r.Blockers) != 1 || b622Clears(owner) != "owner" || owner.Fix != "code-forge block close B1 --run "+f.run {
		t.Fatalf("require_human dry run: want exit 1 and one owner_confirm owner blocker, got %d %+v stderr=%q", code, r, stderr)
	}
	b622SameTree(t, before, b622Snapshot(t, f.b622Roots()...))
}

// A refusal of the real close (the block is not open) is a blocker too.
func TestB622CloseDryRunRefusalIsBlocker(t *testing.T) {
	f := b622Fixture(t)
	f.b622Update(func(entry *ledger.Object) { entry.Set("status", "closed") })
	before := b622Snapshot(t, f.b622Roots()...)
	code, stdout, stderr := f.call("block", "close", "B1", "--run", f.run, "--dry-run", "--json")
	r := b622Parse(t, code, stdout, stderr)
	if code != 1 || len(r.Blockers) != 1 || r.Blockers[0].Code != "no-block" || b622Clears(&r.Blockers[0]) != "orchestrator" {
		t.Fatalf("closed block dry run: got %d %+v stderr=%q", code, r, stderr)
	}
	b622SameTree(t, before, b622Snapshot(t, f.b622Roots()...))
}

func TestB622DryRunUsage(t *testing.T) {
	f := b622Fixture(t)
	before := b622Snapshot(t, f.b622Roots()...)
	for _, c := range []struct {
		args   []string
		stderr string
	}{
		{[]string{"block", "close", "B1", "--run", f.run, "--dry-run", "--decide", "--reason", "x"}, "block close: --dry-run cannot be combined with --decide\n"},
		{[]string{"block", "close", "B1", "--run", f.run, "--dry-run", "--auto-claim"}, "block close: --dry-run cannot be combined with --auto-claim\n"},
		{[]string{"block", "close", "B1", "--run", f.run, "--dry-run", "--no-require-reviews"}, "block close: --dry-run cannot be combined with --no-require-reviews\n"},
		{[]string{"block", "close", "B1", "--run", f.run, "--dry-run=yes"}, "block close: --dry-run takes no value\n"},
		{[]string{"block", "close", "B1", "--run", f.run, "--dry-run", "--dry-run"}, "block close: --dry-run given more than once\n"},
		{[]string{"block", "close", "B1", "--dry-run"}, "block close: a block id and --run are required\n"},
		{[]string{"block", "human-pass", "B1", "--run", f.run, "--dry-run", "--note", " "}, "block human-pass: block human-pass needs one block id and --note <text>\n"},
	} {
		code, stdout, stderr := f.call(c.args...)
		if code != 2 || stdout != "" || stderr != c.stderr {
			t.Errorf("%v: want exit 2 and %q, got %d stdout=%q stderr=%q", c.args, c.stderr, code, stdout, stderr)
		}
	}
	code, stdout, _ := f.call("block", "close", "--help")
	if code != 0 || !strings.Contains(stdout, "[--decide] [--dry-run] [--json]") {
		t.Fatalf("close help: %d %q", code, stdout)
	}
	code, stdout, _ = f.call("block", "human-pass", "--help")
	if code != 0 || !strings.Contains(stdout, "[--proof <dir-or-file>] [--dry-run] [--json]") {
		t.Fatalf("human-pass help: %d %q", code, stdout)
	}
	b622SameTree(t, before, b622Snapshot(t, f.b622Roots()...))
}

// human-pass --dry-run: a block awaiting its check would pass (no prompt, no
// terminal); an open block is not awaiting; an active grant is the owner's.
func TestB622HumanPassDryRun(t *testing.T) {
	f := b622Fixture(t)
	code, stdout, stderr := f.call("block", "human-pass", "B1", "--run", f.run, "--dry-run", "--json")
	r := b622Parse(t, code, stdout, stderr)
	open := r.find("not-awaiting")
	if code != 1 || r.Verb != "human-pass" || b622Clears(open) != "orchestrator" || open.Fix != "code-forge block close B1 --run "+f.run {
		t.Fatalf("open block: want not-awaiting fixed by close, got %d %+v", code, r)
	}
	f.b622Update(func(entry *ledger.Object) {
		entry.Set("status", "awaiting_human_check")
		entry.Set("human_checks", []any{"look at the screen"})
	})
	before := b622Snapshot(t, f.b622Roots()...)
	code, stdout, stderr = f.call("block", "human-pass", "B1", "--run", f.run, "--dry-run", "--json", "--note", "seen")
	r = b622Parse(t, code, stdout, stderr)
	if code != 0 || !r.Ready || !r.DryRun || len(r.Blockers) != 0 || stderr != "" {
		t.Fatalf("awaiting block: want exit 0 ready, got %d %+v stderr=%q", code, r, stderr)
	}
	b622SameTree(t, before, b622Snapshot(t, f.b622Roots()...))
	f.setSnapshot(readyBaseConfig+"}", b622Grant())
	before = b622Snapshot(t, f.b622Roots()...)
	code, stdout, stderr = f.call("block", "human-pass", "B1", "--run", f.run, "--dry-run", "--json")
	r = b622Parse(t, code, stdout, stderr)
	grant := r.find("autopilot-active")
	if code != 1 || len(r.Blockers) != 1 || b622Clears(grant) != "owner" || grant.Fix != "code-forge autopilot stop --run "+f.run {
		t.Fatalf("active grant: want one owner autopilot-active blocker, got %d %+v stderr=%q", code, r, stderr)
	}
	b622SameTree(t, before, b622Snapshot(t, f.b622Roots()...))
}

// The blocker table: owner codes are exactly the human-only fixes.
func TestB622BlockerTableClearsBy(t *testing.T) {
	owner := []string{"not_approved", "late_unruled", "review_cap", "autopilot_waiver_invalid", "rule_break", "close-not-routine", "agent-waivers-not-acked", "no_changes", "owner_confirm", "autopilot-active"}
	orchestrator := []string{"unowned_change", "unreviewed", "unsigned", "no_green_gates", "light_limits", "unproven", "lockfile", "git-failed", "stale_base", "ledger.tamper", "coder_report_failed", "worker.down", "worker.replaced", "something-new"}
	for _, code := range owner {
		if _, by := blockerFix(blockerFixInput{p: gatecheck.Blocker{Code: code}, id: "B1", run: "r"}); by != "owner" {
			t.Errorf("%s: clears_by %s, want owner", code, by)
		}
	}
	for _, code := range orchestrator {
		if _, by := blockerFix(blockerFixInput{p: gatecheck.Blocker{Code: code}, id: "B1", run: "r"}); by != "orchestrator" {
			t.Errorf("%s: clears_by %s, want orchestrator", code, by)
		}
	}
	if fix, _ := blockerFix(blockerFixInput{p: gatecheck.Blocker{Code: "worker.down"}, id: "B1", run: "r"}); fix != "code-forge run start --reattach --run r" {
		t.Errorf("worker fix %q", fix)
	}
}
