package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B714: block human-pass reads the grant as Node's grantState does (B709's
// reader): a date-only until or a legacy form V8's Date.parse reads no longer
// sends human-pass to Node. Every case runs on the B684 fixture with B1
// awaiting its human check; testdata/b714/<case>.txt is Node's run of the
// same fixture (B714_RECORD_NODE=<repo root>).
func b714Awaiting(until any) func(f *b684Fixture) {
	return func(f *b684Fixture) {
		record, err := state.ReadRun(f.home, f.run)
		f.must(err)
		blocks := record.Get("blocks").(ledger.Object)
		entry := blocks.Get("B1").(ledger.Object)
		entry.Set("status", "awaiting_human_check")
		entry.Set("human_checks", []any{"the screen shows the new name"})
		blocks.Set("B1", entry)
		record.Set("blocks", blocks)
		if until != nil {
			grant := record.Get("autopilot").(ledger.Object)
			grant.Set("until", until)
			record.Set("autopilot", grant)
		}
		f.must(state.SaveRun(f.home, record))
	}
}

var b714HumanPassCases = []struct {
	name, ask, args string
	until           any
}{
	{"hp-date-only-expired", "yes", "", "2020-01-01"},
	{"hp-date-only-active", "yes", "", "2099-01-01"},
	{"hp-legacy-expired", "yes", "|--json", "Jan 2 2020"},
	{"hp-space-active", "yes", "", "2099-06-01 10:00"},
	{"hp-slash-expired-no", "no", "", "2020/01/02 10:20:30"},
}

func TestB714HumanPassGrantMatchesNode(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		b684Child()
	}
	b760NoNodeRecorder(t, "B714_RECORD_NODE")
	record := os.Getenv("B714_RECORD_NODE")
	for _, c := range b714HumanPassCases {
		t.Run(c.name, func(t *testing.T) {
			f := newB684Fixture(t, b684Setup{grant: "active", review: b684Approved})
			b714Awaiting(c.until)(f)
			args := "block|human-pass|B1|--run|" + f.run + "|--note|checked on the phone" + c.args
			before := len(f.ledgerLines())
			golden := filepath.Join("testdata", "b714", c.name+".txt")
			var r hvResult
			if record != "" {
				r = f.nodeRun(record, c.ask, args)
			} else {
				r = f.goRun("TestB714HumanPassGrantMatchesNode", c.ask, args)
			}
			got := b697Mac.ReplaceAllString(f.report(r, before), `"mac":"<MAC>"`)
			if record != "" {
				f.must(os.MkdirAll(filepath.Dir(golden), 0o755))
				f.must(os.WriteFile(golden, []byte(got), 0o644))
				return
			}
			want, err := os.ReadFile(golden)
			f.must(err)
			if got != string(want) {
				t.Fatalf("report differs from Node's\n--- got\n%s\n--- want (Node)\n%s", got, want)
			}
		})
	}
}

// The dry run (Go only) judges the same grants without the question and
// writes nothing: an expired date-only grant leaves no blocker, an active
// legacy one is the owner's autopilot-active blocker.
func TestB714HumanPassDryRunGrant(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		b684Child()
	}
	for _, c := range []struct {
		name  string
		until any
		code  int
		want  string
	}{
		{"date-only-expired", "2020-01-01", 0, `"blockers":[]`},
		{"legacy-active", "January 2, 2099", 1, `"code":"autopilot-active"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newB684Fixture(t, b684Setup{grant: "active", review: b684Approved})
			b714Awaiting(c.until)(f)
			before := len(f.ledgerLines())
			r := f.goRun("TestB714HumanPassDryRunGrant", "notty", "block|human-pass|B1|--run|"+f.run+"|--dry-run|--json")
			if r.code != c.code || !strings.Contains(r.stdout, c.want) || strings.Contains(r.stdout, "dry_run_unsupported") {
				t.Fatalf("exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
			}
			if after := len(f.ledgerLines()); after != before {
				t.Fatalf("dry run wrote %d ledger rows", after-before)
			}
		})
	}
}
