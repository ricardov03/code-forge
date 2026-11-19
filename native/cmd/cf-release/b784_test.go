package main

// B784: site-data proof carries every number and sample the /proof page shows, picked from the
// code-forge ledger by fixed rules; a sample with no matching rows is null, never a made-up value.

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func b784Proof(t *testing.T, rows ...string) (map[string]any, string) {
	t.Helper()
	s := b690dSiteFixture(t)
	b690dWrite(t, filepath.Join(s.ledger, "code-forge.jsonl"), strings.Join(rows, "\n")+"\n")
	data, text := b690dDocument(t, s)
	return data["proof"].(map[string]any), text
}

// b784Run is a run with closed and stopped blocks between start and end (ended = "" leaves it open).
func b784Run(run, started, ended string, closed []string, usd string) []string {
	rows := []string{`{"event":"run.start","run":"` + run + `","ts":"` + started + `"}`}
	for _, b := range closed {
		rows = append(rows,
			`{"event":"dispatch","run":"`+run+`","block":"`+b+`","ts":"`+started+`"}`,
			`{"event":"block.close","run":"`+run+`","block":"`+b+`","status":"complete","ts":"`+started+`"}`)
	}
	rows = append(rows, `{"event":"session","run":"`+run+`","usd":`+usd+`,"ts":"`+started+`"}`)
	if ended != "" {
		rows = append(rows, `{"event":"run.end","run":"`+run+`","ts":"`+ended+`"}`)
	}
	return rows
}

func b784Want(t *testing.T, text string) any { return b690dJSON(t, text) }

func TestB784RunSamplesPickLatestEndedRun(t *testing.T) {
	var rows []string
	// An older three-block run, a newer three-block run, a newer one still open (not ended),
	// a two-block run (neither sample), and two single-block runs (the later wins).
	rows = append(rows, b784Run("r-old3", "2026-10-01T01:00:00Z", "2026-10-01T02:00:00Z", []string{"B1", "B2", "B3"}, "4")...)
	rows = append(rows, b784Run("r-new3", "2026-10-20T05:56:23Z", "2026-10-20T07:15:09Z", []string{"B4", "B5", "B6"}, "11.92054")...)
	rows = append(rows, `{"event":"dispatch","run":"r-new3","block":"B7","ts":"2026-10-20T06:00:00Z"}`,
		`{"event":"block.stop","run":"r-new3","block":"B7","ts":"2026-10-20T06:39:11Z"}`)
	// a later three-block run that stayed open for 13 hours is not an overnight batch
	rows = append(rows, b784Run("r-long3", "2026-10-25T00:00:00Z", "2026-10-25T13:00:00Z", []string{"B15", "B16", "B17"}, "9")...)
	rows = append(rows, b784Run("r-open3", "2026-11-01T00:00:00Z", "", []string{"B8", "B9", "B10"}, "1")...)
	rows = append(rows, b784Run("r-two", "2026-11-02T00:00:00Z", "2026-11-02T01:00:00Z", []string{"B11", "B12"}, "1")...)
	rows = append(rows, b784Run("r-one-a", "2026-11-03T00:00:00Z", "2026-11-03T00:10:00Z", []string{"B13"}, "1")...)
	rows = append(rows, b784Run("r-one-b", "2026-11-04T10:00:00Z", "2026-11-04T10:13:19.900Z", []string{"B14"}, "1.72")...)
	proof, _ := b784Proof(t, rows...)
	want := b784Want(t, `{"run":"r-new3","date":"2026-10-20","wall_ms":4726000,"wall":"1h 18m 46s","usd":11.9205,"closed":3,"stopped":1}`)
	if !reflect.DeepEqual(proof["overnight"], want) {
		t.Errorf("overnight %v\nwant %v", proof["overnight"], want)
	}
	want = b784Want(t, `{"run":"r-one-b","date":"2026-11-04","wall_ms":799900,"wall":"13m 19s","usd":1.72,"closed":1,"stopped":0}`)
	if !reflect.DeepEqual(proof["single"], want) {
		t.Errorf("single %v\nwant %v", proof["single"], want)
	}
	if proof["runs"] != 7.0 || proof["blocks"] != 16.0 {
		t.Errorf("totals changed: runs %v blocks %v", proof["runs"], proof["blocks"])
	}
}

func TestB784StoppedBlockIsLatestStillStopped(t *testing.T) {
	proof, _ := b784Proof(t,
		`{"event":"dispatch","run":"r-a","block":"B147","ts":"2026-10-20T06:00:00Z"}`,
		`{"event":"session","run":"r-a","block":"B147","usd":3.29284,"ts":"2026-10-20T06:10:00Z"}`,
		`{"event":"session","run":"r-a","block":"B147","usd":5,"source":"shadow","ts":"2026-10-20T06:10:00Z"}`,
		`{"event":"block.stop","run":"r-a","block":"B147","ts":"2026-10-20T06:39:11Z"}`,
		// later stop, but the block was dispatched again and closed: not stopped any more
		`{"event":"dispatch","run":"r-b","block":"B200","ts":"2026-11-01T00:00:00Z"}`,
		`{"event":"block.stop","run":"r-b","block":"B200","ts":"2026-11-01T00:05:00Z"}`,
		`{"event":"dispatch","run":"r-b","block":"B200","ts":"2026-11-01T01:00:00Z"}`,
		`{"event":"block.close","run":"r-b","block":"B200","status":"complete","ts":"2026-11-01T02:00:00Z"}`,
		// later stop with no spend recorded against it: not picked
		`{"event":"dispatch","run":"r-z","block":"B250","ts":"2026-11-01T03:00:00Z"}`,
		`{"event":"session","run":"r-z","block":"B250","usd":0,"ts":"2026-11-01T03:01:00Z"}`,
		`{"event":"block.stop","run":"r-z","block":"B250","ts":"2026-11-01T03:05:00Z"}`,
		// later stop with no dispatch: no time, not picked
		`{"event":"session","run":"r-c","block":"B300","usd":1,"ts":"2026-11-02T00:01:00Z"}`,
		`{"event":"block.stop","run":"r-c","block":"B300","ts":"2026-11-02T00:05:00Z"}`,
		// a path-like block id is never exported
		`{"event":"dispatch","run":"r-d","block":"/Users/x/B9","ts":"2026-11-03T00:00:00Z"}`,
		`{"event":"session","run":"r-d","block":"/Users/x/B9","usd":1,"ts":"2026-11-03T00:01:00Z"}`,
		`{"event":"block.stop","run":"r-d","block":"/Users/x/B9","ts":"2026-11-03T00:05:00Z"}`,
	)
	want := b784Want(t, `{"run":"r-a","block":"B147","date":"2026-10-20","wall_ms":2351000,"wall":"39m 11s","usd":3.2928}`)
	if !reflect.DeepEqual(proof["stopped"], want) {
		t.Errorf("stopped %v\nwant %v", proof["stopped"], want)
	}
}

func TestB784FindingIsLatestWarningFixedInClosedBlock(t *testing.T) {
	proof, text := b784Proof(t,
		// warning F1 open twice, then approved; block closed: the pick
		`{"event":"review.result","run":"r-a","block":"B10","file":"/secret/customer/a.go","approved":false,"open":[{"id":"F1","severity":"warning"}],"ts":"2026-10-10T00:00:00Z"}`,
		`{"event":"review.result","run":"r-a","block":"B10","file":"/secret/customer/a.go","approved":false,"open":[{"id":"F1","severity":"warning"}],"ts":"2026-10-10T01:00:00Z"}`,
		`{"event":"review.result","run":"r-a","block":"B10","file":"/secret/customer/a.go","approved":true,"open":[],"ts":"2026-10-10T02:00:00Z"}`,
		`{"event":"block.close","run":"r-a","block":"B10","status":"complete","ts":"2026-10-10T03:00:00Z"}`,
		// later: a nit only, then approved: not a warning
		`{"event":"review.result","run":"r-b","block":"B11","file":"b.go","approved":false,"open":[{"id":"F1","severity":"nit"}],"ts":"2026-10-11T00:00:00Z"}`,
		`{"event":"review.result","run":"r-b","block":"B11","file":"b.go","approved":true,"open":[],"ts":"2026-10-11T01:00:00Z"}`,
		`{"event":"block.close","run":"r-b","block":"B11","status":"complete"}`,
		// later: warning waived, then approved: not fixed
		`{"event":"review.result","run":"r-c","block":"B12","file":"c.go","approved":false,"open":[{"id":"F2","severity":"warning"}],"ts":"2026-10-12T00:00:00Z"}`,
		`{"event":"review.waived","run":"r-c","block":"B12","file":"c.go","finding":"F2","ts":"2026-10-12T00:30:00Z"}`,
		`{"event":"review.result","run":"r-c","block":"B12","file":"c.go","approved":true,"open":[],"ts":"2026-10-12T01:00:00Z"}`,
		`{"event":"block.close","run":"r-c","block":"B12","status":"complete"}`,
		// later: warning fixed, but the block never closed
		`{"event":"review.result","run":"r-d","block":"B13","file":"d.go","approved":false,"open":[{"id":"F3","severity":"warning"}],"ts":"2026-10-13T00:00:00Z"}`,
		`{"event":"review.result","run":"r-d","block":"B13","file":"d.go","approved":true,"open":[],"ts":"2026-10-13T01:00:00Z"}`,
	)
	want := b784Want(t, `{"run":"r-a","block":"B10","finding":"F1","severity":"warning","rounds":3,"date":"2026-10-10"}`)
	if !reflect.DeepEqual(proof["finding"], want) {
		t.Errorf("finding %v\nwant %v", proof["finding"], want)
	}
	for _, private := range []string{"/secret", "customer", "a.go", "b.go"} {
		if strings.Contains(text, private) {
			t.Errorf("site data holds %q", private)
		}
	}
}

func TestB784GateIsLatestGreenGateOfClosedBlock(t *testing.T) {
	proof, text := b784Proof(t,
		`{"event":"gate.done","run":"r-a","block":"B10","all_ok":true,"scope":"full","worktree":"/secret/wt","results":[{"gate":"test","ok":true,"skipped":false,"duration_ms":125000},{"gate":"lint","ok":true,"skipped":true,"duration_ms":0},{"gate":"types","ok":true,"skipped":false,"duration_ms":4200}],"ts":"2026-10-10T00:00:00Z"}`,
		`{"event":"block.close","run":"r-a","block":"B10","status":"complete"}`,
		// later: red gate, cancelled gate, gate of an unclosed block: none is picked
		`{"event":"gate.done","run":"r-b","block":"B11","all_ok":false,"results":[{"gate":"test","ok":false,"skipped":false,"duration_ms":1}],"ts":"2026-10-11T00:00:00Z"}`,
		`{"event":"gate.done","run":"r-b","block":"B11","all_ok":true,"cancelled":true,"results":[{"gate":"test","ok":true,"skipped":false,"duration_ms":1}],"ts":"2026-10-11T01:00:00Z"}`,
		`{"event":"block.close","run":"r-b","block":"B11","status":"complete"}`,
		`{"event":"gate.done","run":"r-c","block":"B12","all_ok":true,"results":[{"gate":"test","ok":true,"skipped":false,"duration_ms":1}],"ts":"2026-10-12T00:00:00Z"}`,
	)
	want := b784Want(t, `{"run":"r-a","block":"B10","date":"2026-10-10","scope":"full","results":[
		{"gate":"test","result":"ok","wall_ms":125000,"wall":"2m 5s"},
		{"gate":"lint","result":"skipped","wall_ms":null,"wall":null},
		{"gate":"types","result":"ok","wall_ms":4200,"wall":"4s"}]}`)
	if !reflect.DeepEqual(proof["gate"], want) {
		t.Errorf("gate %v\nwant %v", proof["gate"], want)
	}
	if strings.Contains(text, "/secret") {
		t.Error("site data holds the worktree path")
	}
}

// With no matching rows every sample is null, so the page hides the card; the totals stay.
func TestB784MissingSamplesAreNull(t *testing.T) {
	proof, text := b784Proof(t,
		`{"event":"run.start","run":"r-a","ts":"2026-10-10T00:00:00Z"}`,
		`{"event":"session","run":"r-a","usd":2}`,
	)
	want := map[string]any{"runs": 1.0, "blocks": 0.0, "spend": 2.0, "date": "2026-11-06",
		"overnight": nil, "single": nil, "stopped": nil, "finding": nil, "gate": nil}
	if !reflect.DeepEqual(proof, want) {
		t.Errorf("proof %v\nwant %v", proof, want)
	}
	wantEnd := "  \"proof\": {\n    \"runs\": 1,\n    \"blocks\": 0,\n    \"spend\": 2,\n    \"date\": \"2026-11-06\",\n" +
		"    \"overnight\": null,\n    \"single\": null,\n    \"stopped\": null,\n    \"finding\": null,\n    \"gate\": null\n  }\n}\n"
	if !strings.HasSuffix(text, wantEnd) {
		t.Errorf("proof keys or order:\n%s", text[strings.Index(text, "\"proof\""):])
	}
}
