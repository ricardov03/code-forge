package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
)

func b1013Fixture(t *testing.T) *hvFixture {
	t.Helper()
	f := b633Fixture(t)
	f.setStatus("B1", "closed")
	proof := filepath.Join(f.home, "proof.json")
	if err := os.WriteFile(proof, []byte("proof"), 0600); err != nil {
		t.Fatal(err)
	}
	f.rows(map[string]any{"event": "human_check.cleared", "block": "B1", "clause": "C2", "check": "screen looks right", "waits_for": "device test on iPhone", "note": "evidence green", "ts": "2026-11-20T00:01:00.000Z", "by": "autopilot", "passed_by": "autopilot", "grant_id": "g1", "decision_id": "d1", "proof": proof, "proof_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte("proof"))), "evidence": []any{}})
	f.rows(map[string]any{"event": "block.human_pass", "block": "B1", "proof": proof, "proof_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte("proof"))), "note": "evidence green", "ts": "2026-11-20T00:01:00.000Z", "checks": []any{"screen looks right"}, "by": "autopilot", "grant_id": "g1", "decision_id": "d1"})
	return f
}

func TestB1013Reopen(t *testing.T) {
	for _, landed := range []bool{false, true} {
		t.Run(fmt.Sprint(landed), func(t *testing.T) {
			f := b1013Fixture(t)
			if landed {
				r := f.record()
				r["blocks"].(map[string]any)["B1"].(map[string]any)["landed"] = map[string]any{"merge": "abc"}
				f.writeRecord(r)
			}
			b493HumanSeam(t, true, humanask.Confirmed)
			args := []string{"reopen", "--run", "r-test", "--block", "B1", "--note", "proof is wrong"}
			if landed {
				args = append(args, "--clause", "C2")
			}
			got := b631Run(t, f, args...)
			if got.code != 0 {
				t.Fatalf("%+v", got)
			}
			rows := f.ledger()
			last := rows[len(rows)-1]
			f.verified(last)
			if last["event"] != "human_check.reopened" || last["revokes"] != "d1" || last["by"] != "human" || last["clause"] != "C2" {
				t.Fatal(last)
			}
			list := b631Run(t, f, "list", "--run", "r-test")
			if !strings.Contains(list.stdout, "B1 C2") {
				t.Fatal(list)
			}
			want := "awaiting_human_check"
			if landed {
				want = "closed"
				if !strings.Contains(got.stderr, "stop or fix block") {
					t.Fatal(got)
				}
			}
			if f.record()["blocks"].(map[string]any)["B1"].(map[string]any)["status"] != want {
				t.Fatal(f.record())
			}
			code, _, errText := runVerbCall(t, "end", "--run", "r-test")
			if code != 1 || !strings.Contains(errText, "B1 C2") {
				t.Fatal(code, errText)
			}
			f.clearRow("B1", "C2", "screen looks right")
			if b1013IsOpen(t, f, "B1", "C2") {
				t.Fatal("later clear did not win")
			}
		})
	}
}

func TestB1013ReopenGuards(t *testing.T) {
	for _, mode := range []string{"tty", "cancel", "grant", "race", "yes", "note"} {
		t.Run(mode, func(t *testing.T) {
			f := b1013Fixture(t)
			asked := b493HumanSeam(t, mode != "tty", humanask.Confirmed)
			args := []string{"reopen", "--run", "r-test", "--block", "B1", "--clause", "C2", "--note", "bad proof"}
			if mode == "cancel" {
				b493HumanSeam(t, true, humanask.Cancelled)
			}
			grant := func() {
				r := f.record()
				r["autopilot"] = map[string]any{"grant_id": "g1", "status": "active", "until": "2099-01-01T00:00:00.000Z", "started_at": "2026-01-01T00:00:00.000Z", "scopes": []any{}, "deny": []any{}, "delegate": "L2", "caps": map[string]any{}, "stop_at": 0.8}
				f.writeRecord(r)
			}
			if mode == "grant" {
				grant()
			}
			if mode == "race" {
				humanAsk = func(string) (humanask.Result, error) { grant(); return humanask.Confirmed, nil }
			}
			if mode == "yes" {
				args = append(args, "--yes")
			}
			if mode == "note" {
				args[len(args)-1] = "bad\nnote"
			}
			before := len(f.ledger())
			blockBefore, _ := json.Marshal(f.record()["blocks"].(map[string]any)["B1"])
			got := b631Run(t, f, args...)
			if got.code == 0 || len(f.ledger()) != before {
				t.Fatal(got, "refusal wrote")
			}
			want := map[string]string{"tty": "needs the human at a terminal", "cancel": "cancelled", "grant": "autopilot-active", "race": "autopilot-active", "yes": "unknown flag --yes", "note": "--note must be nonblank"}[mode]
			if !strings.Contains(got.stderr, want) {
				t.Fatalf("%s: want refusal %q, got %+v", mode, want, got)
			}
			blockAfter, _ := json.Marshal(f.record()["blocks"].(map[string]any)["B1"])
			if string(blockBefore) != string(blockAfter) {
				t.Fatal("refusal changed block record")
			}
			if mode == "grant" && len(*asked) != 0 {
				t.Fatal("asked under grant")
			}
		})
	}
}

func TestB1013Audit(t *testing.T) {
	f := b1013Fixture(t)
	for _, want := range []string{"ok", "changed", "missing"} {
		p := filepath.Join(f.home, "proof.json")
		if want == "changed" {
			if err := os.WriteFile(p, []byte("edited"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if want == "missing" {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
		}
		before := len(f.ledger())
		got := b631Run(t, f, "audit", "--run", "r-test", "--json")
		if got.code != 0 || !strings.Contains(got.stdout, `"status":"`+want+`"`) || len(f.ledger()) != before {
			t.Fatal(got)
		}
	}
}

func TestB1013Surfaces(t *testing.T) {
	f := b1013Fixture(t)
	got := b631Run(t, f, "list", "--run", "r-test", "--json")
	if !strings.Contains(got.stdout, `"auto_passed"`) || !strings.Contains(got.stdout, `"grant_id":"g1"`) {
		t.Fatal(got)
	}
	code, out, errs := runVerbCall(t, "status", "--run", "r-test")
	if code != 0 || !strings.Contains(out, `"auto_passed"`) {
		t.Fatal(code, out, errs)
	}
	rows := []ledger.Row{}
	for _, r := range f.ledger() {
		rows = append(rows, r)
	}
	rec, err, ok := runReadRecord(f.home, "r-test")
	if err != nil || !ok {
		t.Fatal(err, ok)
	}
	b := &bnBuild{runID: "r-test", home: f.home}
	scrub := func(s string) string { return s }
	bin := bnRenderBinnacle(b.binnacle(rows, rec, time.Now(), scrub, "owner"), scrub)
	log := bnRenderLog("r-test", b.fullLog(rows, scrub), scrub)
	for _, s := range []string{bin, log} {
		if !strings.Contains(s, "Passed by autopilot — please audit") || !strings.Contains(s, "proof.json") || !strings.Contains(s, "reopen --run r-test") {
			t.Fatal(s)
		}
	}
}

func TestB1013Report(t *testing.T) {
	for _, event := range []string{"block.human_pass", "block.human_checks_cleared"} {
		data := `{"event":"dispatch","run":"r","block":"B1","ts":"2026-11-20T00:00:00Z"}` + "\n" + `{"event":"block.awaiting_human_check","run":"r","block":"B1","ts":"2026-11-20T00:01:00Z"}` + "\n" + fmt.Sprintf(`{"event":%q,"run":"r","block":"B1","by":"autopilot","ts":"2026-11-20T00:02:00Z"}`, event) + "\n"
		b456ReportHome(t, map[string]string{"t.jsonl": data})
		code, out, errText := b456Report(t, "--slug", "t", "--json")
		if code != 0 || !strings.Contains(out, `"autoPassed":true`) || !strings.Contains(out, `"humanWaitMs":null`) {
			t.Fatal(code, out, errText)
		}
		_, text, _ := b456Report(t, "--slug", "t")
		if !strings.Contains(text, "passed by autopilot") {
			t.Fatal(text)
		}
	}
}

func b1013IsOpen(t *testing.T, f *hvFixture, block, clause string) bool {
	t.Helper()
	got := b631Run(t, f, "list", "--run", "r-test", "--json")
	var data struct {
		Open []struct{ Block, Clause string }
	}
	if got.code != 0 || json.Unmarshal([]byte(got.stdout), &data) != nil {
		t.Fatal(got)
	}
	for _, e := range data.Open {
		if e.Block == block && e.Clause == clause {
			return true
		}
	}
	return false
}

func TestB1013SignedOrdering(t *testing.T) {
	f := b1013Fixture(t)
	// Unsigned and tampered reopen rows cannot undo a signed pass.
	f.rows(map[string]any{"event": "human_check.reopened", "block": "B1", "clause": "C2", "revokes": "d1", "by": "human"})
	rows := f.ledger()
	rows[len(rows)-1]["mac"] = b741Tamper(t, rows[len(rows)-1]["mac"].(string))
	var content strings.Builder
	for _, r := range rows {
		data, _ := json.Marshal(r)
		content.Write(data)
		content.WriteByte('\n')
	}
	content.WriteString(`{"event":"human_check.reopened","run":"r-test","block":"B1","clause":"C2","revokes":"d1","by":"human"}` + "\n")
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"), []byte(content.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if b1013IsOpen(t, f, "B1", "C2") {
		t.Fatal("forged reopen took effect")
	}
	f.rows(map[string]any{"event": "human_check.reopened", "block": "B1", "clause": "C2", "revokes": "d1", "by": "human"})
	if !b1013IsOpen(t, f, "B1", "C2") {
		t.Fatal("signed reopen did not win over clear plus whole-block pass")
	}
	f.clearRow("B1", "C2", "screen looks right")
	if b1013IsOpen(t, f, "B1", "C2") {
		t.Fatal("later signed clear did not win")
	}
}

func TestB1013EvidenceAudit(t *testing.T) {
	f := b1013Fixture(t)
	evidence := filepath.Join(f.home, "shot.png")
	if err := os.WriteFile(evidence, []byte("image"), 0600); err != nil {
		t.Fatal(err)
	}
	f.rows(map[string]any{"event": "human_check.cleared", "block": "B1", "clause": "C2", "check": "screen looks right", "by": "autopilot", "grant_id": "g1", "decision_id": "d2", "proof": filepath.Join(f.home, "proof.json"), "proof_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte("proof"))), "evidence": []any{map[string]any{"path": "shot.png", "sha256": fmt.Sprintf("%x", sha256.Sum256([]byte("image")))}}})
	for _, want := range []string{"ok", "changed", "missing"} {
		if want == "changed" {
			if err := os.WriteFile(evidence, []byte("edited"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if want == "missing" {
			if err := os.Remove(evidence); err != nil {
				t.Fatal(err)
			}
		}
		got := b631Run(t, f, "audit", "--run", "r-test", "--json")
		var data struct {
			Passes []struct {
				Decision string `json:"decision_id"`
				Status   string
				Evidence []struct{ Status string } `json:"evidence_audit"`
			}
		}
		if got.code != 0 || json.Unmarshal([]byte(got.stdout), &data) != nil {
			t.Fatal(got)
		}
		if len(data.Passes) != 2 || data.Passes[1].Status != want || len(data.Passes[1].Evidence) != 1 || data.Passes[1].Evidence[0].Status != want {
			t.Fatal(got)
		}
	}
}

func TestB1013ReportLifecycle(t *testing.T) {
	prefix := `{"event":"dispatch","run":"r","block":"B1","ts":"2026-11-20T00:00:00Z"}` + "\n" + `{"event":"block.awaiting_human_check","run":"r","block":"B1","ts":"2026-11-20T00:01:00Z"}` + "\n" + `{"event":"block.human_pass","run":"r","block":"B1","by":"autopilot","ts":"2026-11-20T00:02:00Z"}` + "\n"
	for _, mode := range []string{"redispatch", "owner-after-reopen"} {
		t.Run(mode, func(t *testing.T) {
			tail := `{"event":"dispatch","run":"r","block":"B1","ts":"2026-11-20T00:03:00Z"}` + "\n" + `{"event":"block.close","run":"r","block":"B1","ts":"2026-11-20T00:04:00Z"}` + "\n"
			if mode == "owner-after-reopen" {
				tail = `{"event":"human_check.reopened","run":"r","block":"B1","ts":"2026-11-20T00:03:00Z"}` + "\n" + `{"event":"block.human_pass","run":"r","block":"B1","by":"human","ts":"2026-11-20T00:04:00Z"}` + "\n"
			}
			b456ReportHome(t, map[string]string{"t.jsonl": prefix + tail})
			code, out, errs := b456Report(t, "--slug", "t", "--json")
			if code != 0 || strings.Contains(out, `"autoPassed":true`) {
				t.Fatal(code, out, errs)
			}
			want := `"humanWaitMs":null`
			if mode == "owner-after-reopen" {
				want = `"humanWaitMs":60000`
			}
			if !strings.Contains(out, want) {
				t.Fatal(out)
			}
		})
	}
}
