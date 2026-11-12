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

func b498SameBlocker(a, b Blocker) bool {
	return a.Code == b.Code && a.File == b.File && a.Finding == b.Finding && a.Detail == b.Detail
}

// b498Repr is JSON.stringify(value) ?? "undefined" for a blocker field.
func b498Repr(v any, has bool) string {
	if !has {
		return "undefined"
	}
	data, _ := ledger.Stringify(v, false)
	return string(data)
}

func b498Refusals(res Result) []string {
	out := []string{}
	for _, b := range res.Blockers {
		file, finding := b498Repr(b.File, b.File != ""), b498Repr(b.Finding, b.Finding != "")
		if b.Raw != nil {
			file, finding = b498Repr(b.Raw.File, b.Raw.HasFile), b498Repr(b.Raw.Finding, b.Raw.HasFinding)
		}
		out = append(out, b.Code+"|"+file+"|"+finding+"|"+b.Detail)
	}
	return out
}

// B498: a late or cap row whose file is "", null, a number or false, or
// whose finding is missing or not text, is read as Node reads it. Expected
// refusals measured with Node's checkBlockReviews on the same signed lines
// (testdata/b498_latecap.mjs over the B498_LATECAP_DUMP lines).
func TestB498LateAndCapRowValues(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	approved := func() Row { return Row{"event": "review.approved", "file": "a.mjs", "content_hash": "h2"} }
	late := func(extra Row) Row {
		r := Row{"event": "review.late_finding", "severity": "warning"}
		for k, v := range extra {
			r[k] = v
		}
		return r
	}
	cases := []struct {
		name string
		rows []Row
		want []string
	}{
		{"late file empty", []Row{approved(), late(Row{"file": "", "finding": "L1"})}, []string{"late_unruled|\"\"|\"L1\"|late finding L1 in  has no ruling"}},
		{"late file null", []Row{approved(), late(Row{"file": nil, "finding": "L1"})}, []string{"late_unruled|null|\"L1\"|late finding L1 in null has no ruling"}},
		{"late file number", []Row{approved(), late(Row{"file": 7.0, "finding": "L1"})}, []string{"late_unruled|7|\"L1\"|late finding L1 in 7 has no ruling"}},
		{"late file false finding null", []Row{approved(), late(Row{"file": false, "finding": nil})}, []string{"late_unruled|false|null|late finding null in false has no ruling"}},
		{"late finding missing", []Row{approved(), late(Row{"file": "a.mjs"})}, []string{"late_unruled|\"a.mjs\"|undefined|late finding undefined in a.mjs has no ruling"}},
		{"late finding number", []Row{approved(), late(Row{"file": "a.mjs", "finding": 5.0})}, []string{"late_unruled|\"a.mjs\"|5|late finding 5 in a.mjs has no ruling"}},
		{"late finding empty", []Row{approved(), late(Row{"file": "a.mjs", "finding": ""})}, []string{"late_unruled|\"a.mjs\"|\"\"|late finding  in a.mjs has no ruling"}},
		{"late number finding waived by human", []Row{approved(), late(Row{"file": "a.mjs", "finding": 5.0}),
			{"event": "review.waived", "file": "a.mjs", "finding": 5.0, "by": "human", "reason": "ok"}}, []string{}},
		{"late text waiver does not clear number finding", []Row{approved(), late(Row{"file": "a.mjs", "finding": 5.0}),
			{"event": "review.waived", "file": "a.mjs", "finding": "5", "by": "human", "reason": "ok"}}, []string{"late_unruled|\"a.mjs\"|5|late finding 5 in a.mjs has no ruling"}},
		{"late missing finding waived by row with no finding", []Row{approved(), late(Row{"file": "a.mjs"}),
			{"event": "review.waived", "file": "a.mjs", "by": "human", "reason": "ok"}}, []string{}},
		{"late nit ruling on number file", []Row{approved(), late(Row{"file": 7.0, "finding": "L1", "severity": "nit"}),
			{"event": "review.late_ruling", "file": 7.0, "finding": "L1", "ruling": "nit"}}, []string{}},
		{"late autopilot waiver on number file", []Row{approved(), late(Row{"file": 7.0, "finding": "L1"}),
			{"event": "review.waived", "file": 7.0, "finding": "L1", "severity": "warning", "by": "autopilot", "grant_id": "ag-1", "decision_id": "d-1", "reason": "style", "ts": "2026-11-11T10:00:00.000Z"}}, []string{"autopilot_waiver_invalid|7|\"L1\"|the autopilot waiver of L1 in 7 does not count: no signed autopilot.grant row for grant ag-1 in this run"}},
		{"cap file empty", []Row{approved(), {"event": "review.cap", "file": "", "open": []any{"F1"}}}, []string{"review_cap|\"\"|\"F1\"| stopped at review_cap; finding F1 is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose"}},
		{"cap file number open values", []Row{approved(), {"event": "review.cap", "file": 3.0, "open": []any{1.0, nil, "F2", true, ""}}}, []string{"review_cap|3|1|3 stopped at review_cap; finding 1 is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose", "review_cap|3|null|3 stopped at review_cap; finding null is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose", "review_cap|3|\"F2\"|3 stopped at review_cap; finding F2 is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose", "review_cap|3|true|3 stopped at review_cap; finding true is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose", "review_cap|3|\"\"|3 stopped at review_cap; finding  is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose"}},
		{"cap current file open number", []Row{approved(), {"event": "review.cap", "file": "a.mjs", "open": []any{1.0, "F1"}}}, []string{"not_approved|\"a.mjs\"|\"F1\"|the latest review of a.mjs's current content left F1 open and it is not waived", "review_cap|\"a.mjs\"|1|a.mjs stopped at review_cap; finding 1 is open and not waived; exits: block reopen, block waive, review-file --answers, re-decompose"}},
		{"cap file null open not a list", []Row{approved(), {"event": "review.cap", "file": nil, "open": "F1"}}, []string{}},
	}
	dump := os.Getenv("B498_LATECAP_DUMP")
	for _, c := range cases {
		rows := b380Rows(t, key, c.rows...)
		if !Supported(rows) {
			t.Fatalf("%s: not supported", c.name)
		}
		if dump != "" {
			lines := []string{}
			for _, r := range rows {
				data, _ := ledger.Stringify(r, false)
				lines = append(lines, string(data))
			}
			f, err := os.OpenFile(dump, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(lines)
			f.Write(append(data, '\n'))
			f.Close()
		}
		res := b380Eval(rows, key, nil, "")
		got := b498Refusals(res)
		if res.Unknown || strings.Join(got, "\n") != strings.Join(c.want, "\n") {
			t.Errorf("%s: unknown %v\n got %q\nwant %q", c.name, res.Unknown, got, c.want)
		}
	}
	for _, bad := range []Row{
		late(Row{"file": map[string]any{}, "finding": "L1"}),
		late(Row{"file": "a.mjs", "finding": []any{"L1"}}),
		{"event": "review.cap", "file": "a.mjs", "open": []any{map[string]any{"id": "F1"}}},
		{"event": "review.cap", "file": 1e21, "open": []any{"F1"}},
	} {
		if Supported(b380Rows(t, key, bad)) {
			t.Errorf("%v stays a hand-over", bad)
		}
	}
}
