package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B498: close reads an object decision with the ledger line's property order
// (JSON.parse's), over rotations, and skips a malformed last line as Node does.
func TestB498CloseOrderedDecisionRows(t *testing.T) {
	dir := t.TempDir()
	rotated := `{"event":"autopilot.decision","decision_id":"d-0","decision":{"z":1,"a":{"y":2,"b":3}}}` + "\n"
	current := `{"event":"block.claim","run":"r1"}` + "\n" +
		`{"event":"autopilot.decision","decision_id":"d-1","decision":["x",{"q":1,"10":2,"c":null}]}` + "\n" +
		`{"event":"autopilot.decision","decision_id":"d-2","decision":"fix"}` + "\n" +
		`{"event":"autopilot.decision","decision_id":"d-3","decision":["a","b"]}` + "\n" +
		`{"event":"broken"`
	if err := os.WriteFile(filepath.Join(dir, "proj.1.jsonl"), []byte(rotated), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proj.jsonl"), []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}
	all, err := ledger.ReadAllRows(dir, "proj")
	if err != nil {
		t.Fatal(err)
	}
	ordered := closeOrderedRows(dir, "proj", all)
	want := map[string]string{"d-0": `{"z":1,"a":{"y":2,"b":3}}`, "d-1": `["x",{"10":2,"q":1,"c":null}]`}
	if len(ordered) != len(want) {
		t.Fatalf("ordered rows %d, want %d (only decisions holding an object)", len(ordered), len(want))
	}
	for _, v := range all {
		r := v.(map[string]any)
		id, _ := r["decision_id"].(string)
		o, has := ordered[gatecheck.OrderedKey(r)]
		if want[id] == "" {
			if has {
				t.Errorf("%s: read again though its decision holds no object", id)
			}
			continue
		}
		data, _ := ledger.Stringify(o.Get("decision"), false)
		if !has || string(data) != want[id] {
			t.Errorf("%s: decision %s, want JSON.stringify(JSON.parse(line).decision) = %s", id, data, want[id])
		}
	}
	// No decision holding an object: the ledger is not read again.
	if got := closeOrderedRows(filepath.Join(dir, "missing"), "proj", all[1:2]); got != nil {
		t.Fatalf("no object decision must read nothing: %v", got)
	}
}

// B498: an ack digest over agent waivers on Latin file names with marks is
// decided in Go: Node sorts the [file, finding, mac] items with localeCompare
// (ICU: accents count only when the whole strings tie on base letters, so the
// MACs order é.txt and e.txt). The fixed-ts fixture has the same MACs each
// run; every expected byte was captured from CODE_FORGE_NATIVE=0 on it.
func TestB498CloseAckDigestLocaleOrder(t *testing.T) {
	const refusedText = "agent-waivers-not-acked - agent-waivers-not-acked\n  fix: code-forge block ack-waivers B1 --run r-close-fixture\n"
	const refusedJSON = `{"block":"B1","ok":false,"reason":"agent-waivers-not-acked","blockers":[{"code":"agent-waivers-not-acked","file":null,"detail":"agent-waivers-not-acked","fix":"code-forge block ack-waivers B1 --run r-close-fixture"}]}` + "\n"
	const closedText = "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1: agent waiver é.txt F1 reason: minor; verdict: agree\nblock B1: agent waiver e.txt F1 reason: minor; verdict: agree\nblock B1: agent waiver ä.txt F1 reason: minor; verdict: agree\nblock B1 closed\n"
	const closedJSON = `{"block":"B1","event":"closed","status":"closed","reason":null,"close_event":null,"reviewed":[{"file":"file0.txt","ticket":"b82733d3089a96c3991c870e","via":"approved"}],"agent_waivers":[{"file":"é.txt","finding":"F1","severity":"warning","reason":"minor","verdict":"agree"},{"file":"e.txt","finding":"F1","severity":"warning","reason":"minor","verdict":"agree"},{"file":"ä.txt","finding":"F1","severity":"warning","reason":"minor","verdict":"agree"}]}` + "\n"
	for _, c := range []struct {
		order []int
		acked bool
	}{{[]int{0, 1, 2}, false}, {[]int{1, 0, 2}, false}, {[]int{2, 1, 0}, false}, {[]int{2, 0, 1}, true}} {
		for _, asJSON := range []bool{false, true} {
			extra := []string{}
			wantCode, wantOut, wantErr := 1, refusedText, "block B1 open: agent-waivers-not-acked\n"
			if asJSON {
				extra, wantOut = []string{"--json"}, refusedJSON
			}
			if c.acked {
				wantCode, wantOut, wantErr = 0, closedText, b493OpenedWarn
				if asJSON {
					wantOut = closedJSON
				}
			}
			code, out, errs, reason := b493Close(b493AckFiles(t, c.order, "é.txt", "e.txt", "ä.txt"), extra...)
			if reason != 0 || code != wantCode || out != wantOut || errs != wantErr {
				t.Errorf("digest order %v json=%v: exit=%d reason=%v\nout=%q\nerr=%q\nNode: exit=%d out=%q err=%q", c.order, asJSON, code, reason, out, errs, wantCode, wantOut, wantErr)
			}
		}
	}
}
