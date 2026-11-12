package gatecheck

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B498: every date form in testdata/b498_dates.json (made by b498_dates.js
// under TZ=UTC, America/New_York and Asia/Kathmandu, the same bytes in each)
// reads in Go as V8's Date.parse reads it. Only the forms listed in
// b498DateUnknown may stay unknown (they keep the hand-over).
func TestB498JSDateParseNodeTable(t *testing.T) {
	data, err := os.ReadFile("testdata/b498_dates.json")
	if err != nil {
		t.Fatal(err)
	}
	var table [][2]any
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	for _, zone := range []string{"UTC", "America/New_York", "Asia/Kathmandu"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skip("no zone data:", err)
		}
		saved := dateLocal
		dateLocal = loc
		unknown := 0
		for _, row := range table {
			in := row[0].(string)
			ms, ok, known := jsDateParse(in, true)
			if !known {
				unknown++
				if !b498DateUnknown(in) {
					t.Errorf("%s: jsDateParse(%q) is unknown; Node says %v", zone, in, row[1])
				}
				continue
			}
			want, isNum := row[1].(float64)
			if ok != isNum || ok && ms != want {
				t.Errorf("%s: jsDateParse(%q) = %v %v; Node says %v", zone, in, ms, ok, row[1])
			}
		}
		dateLocal = saved
		if unknown == len(table) {
			t.Fatalf("%s: nothing read", zone)
		}
	}
}

// b498DateUnknown names the legacy forms Go still does not read: a minute or
// second of 60 or more (V8 drops or moves it), or a day outside 1-31 (V8 may
// then read the numbers in another order). They keep the hand-over.
func b498DateUnknown(in string) bool {
	m := regexp.MustCompile(`^(?:\w+,? )?(?:(\d+) \w+|\w+ (\d+)) \d+ (\d+):(\d+)(?::(\d+))? `).FindStringSubmatch(in)
	if m == nil {
		return false
	}
	n := func(s string) int { v, _ := strconv.Atoi(s); return v }
	day := n(m[1] + m[2])
	return day < 1 || day > 31 || n(m[4]) > 59 || n(m[5]) > 59
}

// B498: localeCompare in Go gives Node's sign on every pair in
// testdata/b498_collate.json (made by testdata/b498_collate_pairs.js; the same bytes
// under LANG=C, en_US and es_CO), Latin letters with marks included.
func TestB498LocaleCompareNodeTable(t *testing.T) {
	data, err := os.ReadFile("testdata/b498_collate.json")
	if err != nil {
		t.Fatal(err)
	}
	var table [][3]any
	if err := json.Unmarshal(data, &table); err != nil {
		t.Fatal(err)
	}
	bad := 0
	for _, row := range table {
		a, b, want := row[0].(string), row[1].(string), int(row[2].(float64))
		got, ok := localeCompare(a, b)
		if !ok || got != want {
			if bad++; bad <= 20 {
				t.Errorf("localeCompare(%q, %q) = %d %v; Node says %d", a, b, got, ok, want)
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d pairs differ from Node", bad, len(table))
	}
}

// b498DecisionRows are the rows of an autopilot waiver of F1 whose decision
// row decided raw (a JSON text), each with JSON.parse's property order.
func b498DecisionRows(t *testing.T, key []byte, raw string) ([]Row, map[uintptr]ledger.Object, []string) {
	t.Helper()
	var decision any
	if err := json.Unmarshal([]byte(raw), &decision); err != nil {
		t.Fatal(err)
	}
	rows := b380Rows(t, key,
		Row{"event": "review.round", "file": "a.mjs", "content_hash": "h2", "open_after": 1},
		Row{"event": "review.result", "file": "a.mjs", "content_hash": "h2", "status": "reviewed", "approved": false, "open": []any{map[string]any{"id": "F1", "severity": "warning"}}},
		Row{"event": "autopilot.grant", "grant_id": "ag-1", "scopes": []any{"waive:warning"}, "deny": []any{}, "delegate": "L2", "until": "2026-11-11T12:00:00.000Z", "ts": "2026-11-11T09:00:00.000Z"},
		Row{"event": "autopilot.decision", "decision_id": "d-1", "grant_id": "ag-1", "scope": "waive:warning", "acted": true, "decision": decision, "subject": map[string]any{"block": "B1", "file": "a.mjs", "finding": "F1"}, "ts": "2026-11-11T09:50:00.000Z"},
		Row{"event": "review.waived", "file": "a.mjs", "finding": "F1", "severity": "warning", "by": "autopilot", "grant_id": "ag-1", "decision_id": "d-1", "reason": "style", "ts": "2026-11-11T10:00:00.000Z"},
	)
	ordered := map[uintptr]ledger.Object{}
	lines := []string{}
	for _, r := range rows {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		o, err := ledger.DecodeObject(data)
		if err != nil {
			t.Fatal(err)
		}
		if r["event"] == "autopilot.decision" {
			wrapped, err := ledger.DecodeObject([]byte(`{"v":` + raw + `}`))
			if err != nil {
				t.Fatal(err)
			}
			o.Set("decision", wrapped.Get("v"))
		}
		ordered[OrderedKey(r)] = o
		line, err := ledger.Stringify(o, false)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(line))
	}
	return rows, ordered, lines
}

// B498: an object or array decision prints with JSON.stringify in the order
// JSON.parse read it. Details measured with Node's checkBlockReviews on the
// same signed lines (testdata/b498_decision.mjs over the B498_DECISION_DUMP lines);
// Node also reports the unreviewed file first.
func TestB498DecisionPrintsInParseOrder(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	for _, c := range []struct{ raw, detail string }{
		{`{"z":1,"10":"t","2":true,"a":[{"y":2,"b":3}]}`, `the autopilot waiver of F1 in a.mjs does not count: decision d-1 decided {"2":true,"10":"t","z":1,"a":[{"y":2,"b":3}]}, not "waive"`},
		{`[{"verdict":"waive","by":"L2"},"x",null]`, `the autopilot waiver of F1 in a.mjs does not count: decision d-1 decided [{"verdict":"waive","by":"L2"},"x",null], not "waive"`},
		{`["waive","b"]`, `the autopilot waiver of F1 in a.mjs does not count: decision d-1 decided ["waive","b"], not "waive"`},
		{`{}`, `the autopilot waiver of F1 in a.mjs does not count: decision d-1 decided {}, not "waive"`},
	} {
		rows, ordered, lines := b498DecisionRows(t, key, c.raw)
		if dump := os.Getenv("B498_DECISION_DUMP"); dump != "" {
			f, err := os.OpenFile(dump, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(lines)
			f.Write(append(data, '\n'))
			f.Close()
		}
		res := Evaluate(Input{Run: "r1", Block: "B1", Files: []File{{File: "a.mjs", Hash: "h2"}}, Rows: rows, Key: key, HighPaths: []any{}, Unowned: []string{}, Ordered: ordered})
		if res.Unknown || len(res.Blockers) != 2 || res.Blockers[0].Code != "unreviewed" || res.Blockers[1].Code != "autopilot_waiver_invalid" || res.Blockers[1].Detail != c.detail {
			t.Errorf("%s: unknown %v blockers %+v; Node: %s", c.raw, res.Unknown, res.Blockers, c.detail)
		}
	}
	// Without the parse order a decision holding an object stays unknown (hand-over).
	rows, _, _ := b498DecisionRows(t, key, `{"z":1,"a":2}`)
	if res := Evaluate(Input{Run: "r1", Block: "B1", Files: []File{{File: "a.mjs", Hash: "h2"}}, Rows: rows, Key: key, HighPaths: []any{}, Unowned: []string{}}); !res.Unknown {
		t.Fatalf("an object decision without its parse order must stay unknown: %+v", res.Blockers)
	}
}
