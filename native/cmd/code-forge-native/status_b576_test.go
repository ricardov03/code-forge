package main

import (
	"os"
	"path/filepath"
	"testing"
)

// B576: a block id outside printable ASCII needs Node's ICU order only when
// two or more blocks are listed (not closed, not stopped). With one listed
// block, or with such ids only on closed and stopped blocks, `status` and
// `native-status` answer in Go. Each want is Node's output over the same
// record (status.mjs's default export and native-status.mjs in a scratch
// script); no masks.
func TestB576StatusOneListedNonASCIIBlockRunsInGo(t *testing.T) {
	home, ws := b452StatusFixture(t)
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	b452Record(t, home, "r-uni", map[string]any{"run_id": "r-uni", "workspace": ws, "project": "proj", "status": "active", "blocks": map[string]any{
		"Bé": map[string]any{"level": "L1"}, "Bü": map[string]any{"status": "closed"}, "B1": map[string]any{"status": "stopped"}}})
	code, out, errText := runCaptured(t, "status", "--run", "r-uni")
	wantText := "run r-uni · active · plan · spent $0.00 of $5.00\nBé open L1 · - · gate none · findings 0\nclosed 1 · stopped 1\n"
	if code != 0 || out != wantText || errText != "" {
		t.Fatalf("code=%d\nstdout=%q\nwant  =%q\nstderr=%q", code, out, wantText, errText)
	}
	code, out, errText = runCaptured(t, "status", "--run", "r-uni", "--json")
	wantJSON := `{"runs":[{"run_id":"r-uni","status":"active","mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[{"id":"Bé","state":"open","level":"L1","model":null,"gate":{"result":"none","name":null},"findings":0}],"more":0,"closed":1,"stopped":1}]}` + "\n"
	if code != 0 || out != wantJSON || errText != "" {
		t.Fatalf("json: code=%d\n got %s\nwant %s\nstderr=%q", code, out, wantJSON, errText)
	}
}

func TestB576NativeStatusOneListedNonASCIIBlockRunsInGo(t *testing.T) {
	home := b501NativeStatusFixture(t, false)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "")
	b501Stdin(t, `{"record":{"run_id":"r1","blocks":{"Bé":{"status":"open"},"Bü":{"status":"closed"},"Bø":{"status":"stopped"}}},"rows":[]}`+"\n"+
		`{"record":{"run_id":"r1","blocks":{"B10":{},"Bé":{"status":"closed"},"B2":{}}},"rows":[]}`+"\n")
	want := `{"runs":[{"run_id":"r1","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[{"id":"Bé","state":"open","level":null,"model":null,"gate":{"result":"none","name":null},"findings":0}],"more":0,"closed":1,"stopped":1}]}` + "\n" +
		`{"runs":[{"run_id":"r1","status":null,"mode":"plan","spent_usd":0,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[{"id":"B2","state":"open","level":null,"model":null,"gate":{"result":"none","name":null},"findings":0},{"id":"B10","state":"open","level":null,"model":null,"gate":{"result":"none","name":null},"findings":0}],"more":0,"closed":1,"stopped":0}]}` + "\n"
	code, out, errText := runCaptured(t, "native-status")
	if code != 1 || out != want || errText != b501RouterLine {
		t.Fatalf("code=%d\nstdout=%s\nwant  =%s\nstderr=%q", code, out, want, errText)
	}
	if _, err := os.Stat(filepath.Join(home, ".code-forge", "native", "handovers.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("a hand-over was recorded: %v", err)
	}
}
