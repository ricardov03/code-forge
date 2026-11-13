package worker

import (
	"testing"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B623: testdata/b623_fixloop_golden.json is Node's fixLoopRound (worker/
// engine.mjs, with the review call faked as for b496_fixloop_golden.json) over
// tickets that carry a signed `block reopen` row (review.reopened: agent from
// open, human and auto from stopped, lost) or autopilot extra-round rows
// (grant, decision, autopilot.extra_round). Before B623 Go handed each such
// ticket to Node; now the outcome, rows, files and loadState are Node's.
func TestB623FixLoopReopenAndExtraRoundMatchNode(t *testing.T) {
	scenarios := fixLoopGoldenScenarios(t, "testdata/b623_fixloop_golden.json")
	if len(scenarios) != 12 {
		t.Fatalf("golden has %d scenarios", len(scenarios))
	}
	replayFixLoopGolden(t, scenarios)
}

func b623Cases(t *testing.T, kind string) []ledger.Object {
	t.Helper()
	out := []ledger.Object{}
	for _, c := range fixLoopGoldenScenarios(t, "testdata/b623_reopen_cases.json") {
		if c.Get("kind") == kind {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		t.Fatalf("no %s cases", kind)
	}
	return out
}

// testdata/b623_reopen_cases.json is Node's review/reopen.mjs reopenRowFor
// (want: the index of the row it returns, -1 for null) and
// review/fixloop.mjs extraRoundsFor (want: the count) over hand-made rows
// signed with the 32×7 key: hash and seq matches, by human/agent/auto,
// legacy review.cap stop hashes, lost state, grant windows, deny lists,
// stops, decision age and subject.
func TestB623ReopenRowForMatchesNode(t *testing.T) {
	for _, c := range b623Cases(t, "reopen") {
		t.Run(c.Get("name").(string), func(t *testing.T) {
			rows := objects(c.Get("rows"))
			at := c.Get("at").(ledger.Object)
			state, _ := at.Get("state").(ledger.Object)
			a := reopenAt{runID: "r1", block: "B1", file: "a.go", key: b496Key, contentHash: at.Get("contentHash").(string),
				loaded: Loaded{Status: at.Get("status").(string), State: state, Seq: at.Get("seq").(float64)}}
			got := -1.0
			if row := a.reopenRowFor(rows); row != nil {
				for i, r := range rows {
					if mustJSON(t, r) == mustJSON(t, row) {
						got = float64(i)
						break
					}
				}
			}
			if got != c.Get("want").(float64) {
				t.Fatalf("row %v, Node %v", got, c.Get("want"))
			}
		})
	}
}

func TestB623ExtraRoundsForMatchesNode(t *testing.T) {
	for _, c := range b623Cases(t, "extra") {
		t.Run(c.Get("name").(string), func(t *testing.T) {
			got, err := extraRoundsFor(objects(c.Get("rows")), "r1", "B1", "a.go", b496Key)
			if err != nil || float64(got) != c.Get("want").(float64) {
				t.Fatalf("%d %v, Node %v", got, err, c.Get("want"))
			}
		})
	}
}

// A grant, stop or extra-round date Go cannot read as V8's Date.parse does
// keeps the ticket Node's: FixLoopRound returns ErrUnsupported with nothing
// written.
func TestB623ExtraRoundUnknownDateHandsOver(t *testing.T) {
	const odd = "next tuesday at noon"
	if _, _, known := gatecheck.JSDateParse(odd, true); known {
		t.Fatalf("%q is a date Go reads; pick another", odd)
	}
	sign := func(row ledger.Object) ledger.Object {
		signed, err := signAt(append(ledger.Object{{Key: "run", Value: "r1"}}, row...), b496Key, b496Clock())
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	grant := func(until string) ledger.Object {
		return sign(ledger.Object{{Key: "event", Value: "autopilot.grant"}, {Key: "grant_id", Value: "g1"}, {Key: "scopes", Value: []any{"round:extra"}},
			{Key: "deny", Value: []any{}}, {Key: "until", Value: until}})
	}
	extra := func(ts string) ledger.Object {
		return sign(ledger.Object{{Key: "event", Value: "autopilot.extra_round"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "a.go"},
			{Key: "grant_id", Value: "g1"}, {Key: "decision_id", Value: "d1"}, {Key: "ts", Value: ts}})
	}
	cases := map[string][]ledger.Object{
		"extra round ts": {grant("2026-11-12T00:00:00.000Z"), extra(odd)},
		"grant until":    {grant(odd), extra("2026-11-11T09:30:00.000Z")},
	}
	for name, extraRows := range cases {
		t.Run(name, func(t *testing.T) {
			home, repo := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			b496Repo(t, repo)
			l := &b496Ledger{t: t, tk: FixLoopTicket{Block: "B1", File: "a.go", ContentHash: b496Hash(t, repo, "a.go")}}
			r := b496Request(t, home, repo, l, func(func(ledger.Object) error) (ledger.Object, error) { t.Fatal("review ran"); return nil, nil })
			l.rows = append(l.rows, extraRows...)
			mark, before := len(l.rows), mustJSON(t, b496Files(t, home))
			_, err := FixLoopRound(l.tk, r)
			if !IsUnsupported(err) || len(l.rows) != mark || mustJSON(t, b496Files(t, home)) != before {
				t.Fatalf("got %v, %d rows written", err, len(l.rows)-mark)
			}
		})
	}
}

// B623: readTicket takes any reopened_at Date.parse reads (an RFC 1123 stamp
// as well as ISO) and refuses one it reads as NaN with bad-ticket (Node:
// Date.parse("Wed, 11 Nov 2026 09:00:00 GMT") = 1794387600000,
// Date.parse("2026-13-01T00:00:00.000Z") = NaN). Before B623 Go handed both
// over. A stamp Go cannot decide still hands over.
func TestB623TicketReopenedAtAsNodeReadsIt(t *testing.T) {
	fx := newB488Fix(t)
	real := sha("hello\n")
	clock := fixedNow
	w := fx.runner(&clock)
	read := func(at string) (ledger.Object, ledger.Object, error) {
		t.Helper()
		return w.decideRead(fx.ticket(t, "r1", "B1", "a.txt", real, ledger.Field{Key: "reopened_at", Value: at}))
	}
	if refused, tk, err := read("Wed, 11 Nov 2026 09:00:00 GMT"); err != nil || refused != nil || tk.Get("reopened_at") != "Wed, 11 Nov 2026 09:00:00 GMT" {
		t.Fatalf("RFC 1123: %v %v %v", refused, tk, err)
	}
	if refused, _, err := read("2026-13-01T00:00:00.000Z"); err != nil || refused.Get("reason") != "bad-ticket" {
		t.Fatalf("month 13: %v %v", refused, err)
	}
	if _, _, err := read("next tuesday at noon"); !IsUnsupported(err) {
		t.Fatalf("undecided: %v", err)
	}
}
