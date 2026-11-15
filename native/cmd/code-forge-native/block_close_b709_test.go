package main

import (
	"os"
	"path/filepath"
	"testing"
)

// B709: a grant until Go's strict reader refused (a date-only until such as
// "2020-01-01", a legacy form V8's Date.parse reads, such as "Jan 2 2020" or
// "2099-06-01 10:00") no longer sends block close to Node: Go dates it as
// Node's grantState does (expires it with Node's autopilot.expire row, or
// keeps it active) and closes, refuses or asks as Node does. Every case runs
// on the B684 fixture; testdata/b709/<case>.txt is Node's run of the same
// fixture (B709_RECORD_NODE=<repo root>).
var b709Cases = []struct {
	name, ask, args string
	setup           b684Setup
	after           func(f *b684Fixture)
}{
	{"grant-date-only-expired", "notty", "", b684Setup{grant: "active", review: b684Approved}, b697Until("2020-01-01")},
	{"grant-date-only-active", "notty", "", b684Setup{grant: "active", review: b684Approved}, b697Until("2099-01-01")},
	{"grant-legacy-expired", "notty", "", b684Setup{grant: "active", review: b684Approved}, b697Until("Jan 2 2020")},
	{"grant-space-active", "notty", "", b684Setup{grant: "active", review: b684Approved}, b697Until("2099-06-01 10:00")},
	{"grant-slash-expired-json", "notty", "|--json", b684Setup{grant: "active", review: b684Approved}, b697Until("2020/01/02 10:20:30")},
	{"grant-date-only-routine", "notty", "", b684Setup{grant: "active", config: b684Config(map[string]any{"close": map[string]any{"require_human": true}}), review: b684Approved}, b697Until("2099-01-01")},
	{"grant-date-only-expired-human", "yes", "", b684Setup{grant: "active", config: b684Config(map[string]any{"close": map[string]any{"require_human": true}}), review: b684Approved}, b697Until("2020-01-01")},
	{"nrr-grant-date-only-expired", "yes", "|--no-require-reviews", b684Setup{grant: "active", review: b684Finding("major")}, b697Until("2020-01-01")},
	{"nrr-grant-legacy-active", "yes", "|--no-require-reviews", b684Setup{grant: "active", review: b684Finding("major")}, b697Until("January 2, 2099")},
	{"decide-grant-legacy-active", "yes", "|--decide|--reason|naming only", b684Setup{grant: "active", review: b684Finding("warning")}, b697Until("Jan 2 2099")},
	{"decide-grant-legacy-expired", "yes", "|--decide|--reason|naming only", b684Setup{grant: "active", review: b684Finding("warning")}, b697Until("1/2/2020 10:20")},
}

func TestB709GrantUntilCloseMatchesNode(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		b684Child()
	}
	record := os.Getenv("B709_RECORD_NODE")
	for _, c := range b709Cases {
		t.Run(c.name, func(t *testing.T) {
			f := newB684Fixture(t, c.setup)
			if c.after != nil {
				c.after(f)
			}
			args := f.args(c.args)
			before := len(f.ledgerLines())
			golden := filepath.Join("testdata", "b709", c.name+".txt")
			var r hvResult
			if record != "" {
				r = f.nodeRun(record, c.ask, args)
			} else {
				r = f.goRun("TestB709GrantUntilCloseMatchesNode", c.ask, args)
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
