package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
)

// b452StatusFixture is a HOME with a run ledger and a git workspace. The
// expected outputs below are what Node's status printed for the same files.
func b452StatusFixture(t *testing.T) (home, ws string) {
	t.Helper()
	root := t.TempDir()
	home, ws = filepath.Join(root, "home"), filepath.Join(root, "ws")
	for _, dir := range []string{filepath.Join(home, ".code-forge", "runs"), filepath.Join(home, ".code-forge", "ledger"), ws} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	git := exec.Command("git", "init", "-q", ws)
	git.Env = gitx.ChildEnv()
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	b452Write(t, filepath.Join(ws, ".code-forge.yml"), "version: 1\nbudget:\n  usd: 5\n")
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b452-node")
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(ws); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(before) })
	return home, ws
}

func b452Write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}

func b452Record(t *testing.T, home, id string, record any) {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	b452Write(t, filepath.Join(home, ".code-forge", "runs", id+".json"), string(data))
}

func b452Ledger(t *testing.T, home, slug string, rows ...any) {
	t.Helper()
	text := ""
	for _, r := range rows {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		text += string(data) + "\n"
	}
	b452Write(t, filepath.Join(home, ".code-forge", "ledger", slug+".jsonl"), text)
}

// Former status-unsupported causes: a non-B block id, a block entry that is
// not a mapping, a non-object ledger row, review.answered, an agent waiver,
// block.acceptance_amended and a late finding whose file is not text.
func TestB452StatusFormerHandoverShapesRunInGo(t *testing.T) {
	home, ws := b452StatusFixture(t)
	b452Record(t, home, "r-a", map[string]any{"run_id": "r-a", "workspace": ws, "project": "proj", "status": "active", "mode": "brief", "blocks": map[string]any{
		"B10": map[string]any{"status": "open", "level": "L2"}, "B2": map[string]any{"status": "open", "attempt": 3},
		"a-1": map[string]any{"status": "awaiting_human_check"}, "B1": map[string]any{"status": "closed"}, "B3": "x"}})
	b452Ledger(t, home, "proj",
		5,
		map[string]any{"run": "r-a", "block": "B2", "event": "session", "role": "coder", "provider": "openai", "model": "gpt", "tokens_in": 1000, "tokens_out": 1000, "level": "L1"},
		map[string]any{"run": "r-a", "block": "B2", "event": "review.result", "file": "src/a.js", "content_hash": "h1", "approved": false, "open": []any{map[string]any{"id": "F1", "severity": "warning"}, map[string]any{"id": "F2", "severity": "nit"}}},
		map[string]any{"run": "r-a", "block": "B2", "event": "review.answered", "file": "src/a.js", "finding": "F2"},
		map[string]any{"run": "r-a", "block": "B10", "event": "review.late_finding", "file": 7, "finding": "L1", "severity": "critical"},
		map[string]any{"run": "r-a", "block": "B10", "event": "review.waived", "by": "agent", "file": "src/b.js", "finding": "F9"},
		map[string]any{"run": "r-a", "block": "B10", "event": "gate.red", "gate": "tests", "reason": "timeout"},
		map[string]any{"run": "r-a", "block": "B10", "event": "block.acceptance_amended"},
	)
	code, out, errText := runCaptured(t, "status", "--run", "r-a")
	want := "run r-a · active · brief · spent $0.01 of $5.00\n" +
		"a-1 awaiting_human_check - · - · gate none · findings 0\n" +
		"B2 attempt-3 - · openai/gpt · gate none · findings 2\n" +
		"B3 open - · - · gate none · findings 0\n" +
		"B10 open L2 · - · gate timeout (tests) · findings 1\n" +
		"awaiting_human_check: [\"a-1\"]\n" +
		"closed 1 · stopped 0\n"
	if code != 0 || out != want || errText != "" {
		t.Fatalf("code=%d\nstdout=%q\nwant  =%q\nstderr=%q", code, out, want, errText)
	}
	code, out, _ = runCaptured(t, "status", "--run", "r-a", "--json")
	wantJSON := `{"runs":[{"run_id":"r-a","status":"active","mode":"brief","spent_usd":0.005,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[{"id":"a-1","state":"awaiting_human_check","level":null,"model":null,"gate":{"result":"none","name":null},"findings":0},{"id":"B2","state":"attempt-3","level":null,"model":"openai/gpt","gate":{"result":"none","name":null},"findings":2},{"id":"B3","state":"open","level":null,"model":null,"gate":{"result":"none","name":null},"findings":0},{"id":"B10","state":"open","level":"L2","model":null,"gate":{"result":"timeout","name":"tests"},"findings":1}],"more":0,"closed":1,"stopped":0,"awaiting_human_check":["a-1"]}]}` + "\n"
	if code != 0 || out != wantJSON {
		t.Fatalf("json code=%d\n got %s\nwant %s", code, out, wantJSON)
	}
}

// A run id, workspace and project that are not text, and blocks as a list.
func TestB452StatusNonTextRecordFieldsRunInGo(t *testing.T) {
	home, _ := b452StatusFixture(t)
	b452Record(t, home, "r-b", map[string]any{"run_id": 42, "workspace": 17, "project": []any{"x"}, "status": "active", "blocks": []any{"a", map[string]any{"status": "stopped"}}})
	code, out, errText := runCaptured(t, "status", "--run", "r-b")
	want := "run 42 · active · plan · spent $?\n0 open - · - · gate none · findings 0\nclosed 0 · stopped 1\n"
	if code != 0 || out != want || errText != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errText)
	}
	code, out, _ = runCaptured(t, "status", "--run", "r-b", "--json")
	wantJSON := `{"runs":[{"run_id":"42","status":"active","mode":"plan","spent_usd":null,"unknown_usd_sessions":null,"budget_usd":null,"blocks":[{"id":"0","state":"open","level":null,"model":null,"gate":{"result":"none","name":null},"findings":0}],"more":0,"closed":0,"stopped":1}]}` + "\n"
	if code != 0 || out != wantJSON {
		t.Fatalf("json code=%d got %s", code, out)
	}
}

// The records Node's status fails on exit 1 with one stderr line and no stdout.
func TestB452StatusFailuresAreGoErrors(t *testing.T) {
	home, ws := b452StatusFixture(t)
	b452Record(t, home, "r-null", map[string]any{"run_id": "r-null", "workspace": ws, "project": "proj", "blocks": map[string]any{"B1": nil}})
	b452Record(t, home, "r-prim", 5)
	b452Record(t, home, "r-n", map[string]any{"run_id": "r-n", "workspace": ws, "project": "nullp", "status": "active", "blocks": map[string]any{}})
	b452Write(t, filepath.Join(home, ".code-forge", "ledger", "nullp.jsonl"), "null\n{\"run\":\"r-n\"}\n")
	b452Write(t, filepath.Join(home, ".code-forge", "runs", "r-bad.json"), "{nope")
	cases := []struct{ run, prefix string }{
		{"r-null", "status: Cannot read properties of null (reading 'status')\n"},
		{"r-prim", "status: no run record for r-prim\n"},
		{"r-n", "status: Cannot read properties of null (reading 'run')\n"},
		{"r-bad", "status: the run record of r-bad is not valid JSON ("},
	}
	for _, c := range cases {
		for _, extra := range [][]string{nil, {"--json"}} {
			code, out, errText := runCaptured(t, append([]string{"status", "--run", c.run}, extra...)...)
			if code != 1 || out != "" || !strings.HasPrefix(errText, c.prefix) || strings.Count(errText, "\n") != 1 {
				t.Fatalf("%s %v: code=%d stdout=%q stderr=%q", c.run, extra, code, out, errText)
			}
		}
	}
}

// Records Node's status completes: an array record (every field undefined, the
// budget from the current folder), a token price that overflows (unknown) and a
// total that overflows ($Infinity, JSON null). Outputs are Node's for the same
// files.
func TestB452StatusNodeSuccessShapesRunInGo(t *testing.T) {
	home, ws := b452StatusFixture(t)
	b452Write(t, filepath.Join(home, ".code-forge", "runs", "r-arr.json"), "[1,2]")
	b452Record(t, home, "r-inf", map[string]any{"run_id": "r-inf", "workspace": ws, "project": "infp", "status": "active", "blocks": map[string]any{}})
	b452Write(t, filepath.Join(home, ".code-forge", "ledger", "infp.jsonl"), `{"run":"r-inf","block":"B1","event":"session","role":"coder","provider":"openai","model":"gpt","tokens_in":1e308,"tokens_out":1e308,"level":"L1"}`+"\n")
	b452Record(t, home, "r-inf2", map[string]any{"run_id": "r-inf2", "workspace": ws, "project": "inf2", "status": "active", "blocks": map[string]any{}})
	b452Write(t, filepath.Join(home, ".code-forge", "ledger", "inf2.jsonl"), `{"run":"r-inf2","event":"session","usd":1e308}`+"\n"+`{"run":"r-inf2","event":"session","usd":1e308}`+"\n")
	cases := []struct{ run, text, json string }{
		{"r-arr", "run undefined · - · plan · spent $? of $5.00\n",
			`{"runs":[{"run_id":"undefined","status":null,"mode":"plan","spent_usd":null,"unknown_usd_sessions":null,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}` + "\n"},
		{"r-inf", "run r-inf · active · plan · spent $0.00+? of $5.00\n",
			`{"runs":[{"run_id":"r-inf","status":"active","mode":"plan","spent_usd":0,"unknown_usd_sessions":1,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}` + "\n"},
		{"r-inf2", "run r-inf2 · active · plan · spent $Infinity of $5.00\n",
			`{"runs":[{"run_id":"r-inf2","status":"active","mode":"plan","spent_usd":null,"unknown_usd_sessions":0,"budget_usd":5,"blocks":[],"more":0,"closed":0,"stopped":0}]}` + "\n"},
	}
	for _, c := range cases {
		code, out, errText := runCaptured(t, "status", "--run", c.run)
		if code != 0 || out != c.text || errText != "" {
			t.Fatalf("%s: code=%d\nstdout=%q\nwant  =%q\nstderr=%q", c.run, code, out, c.text, errText)
		}
		code, out, errText = runCaptured(t, "status", "--run", c.run, "--json")
		if code != 0 || out != c.json || errText != "" {
			t.Fatalf("%s json: code=%d\n got %s\nwant %s\nstderr=%q", c.run, code, out, c.json, errText)
		}
	}
}

// A block id outside printable ASCII needs ICU collation, which Go lacks: it
// stays with Node (here no Node exists, so the handover reports that).
func TestB452StatusNonASCIIBlockIDStaysWithNode(t *testing.T) {
	home, ws := b452StatusFixture(t)
	b452Record(t, home, "r-uni", map[string]any{"run_id": "r-uni", "workspace": ws, "project": "proj", "blocks": map[string]any{"Bé": map[string]any{}, "B1": map[string]any{}}})
	code, out, errText := runCaptured(t, "status", "--run", "r-uni")
	if code != 127 || out != "" || !strings.Contains(errText, "node executable") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errText)
	}
}

// Worker pins Node reads by throwing (a null pin, a non-text start time) give no
// warning; the plain unpinned and replaced cases warn like Node.
func TestB452StatusWorkerWarningsRunInGo(t *testing.T) {
	home, ws := b452StatusFixture(t)
	sleeper := exec.Command("sleep", "60")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() }()
	pid := sleeper.Process.Pid
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	base := func(id string, extra map[string]any) map[string]any {
		r := map[string]any{"run_id": id, "workspace": ws, "project": "empty", "status": "active", "blocks": map[string]any{}}
		for k, v := range extra {
			r[k] = v
		}
		return r
	}
	records := map[string]map[string]any{
		"r-w1": {"workers": []any{nil}},
		"r-w2": {"workers": "x"},
		"r-w3": {},
		"r-w4": {"worker": map[string]any{"pid": pid, "started_at": 5}},
		"r-w5": {"worker": map[string]any{"pid": 1, "started_at": "x"}},
		"r-w6": {},
	}
	for id, extra := range records {
		start := "unknown" + id[len(id)-1:]
		beat := now
		if id == "r-w6" {
			beat = "yesterday" // not a date: not a worker announcement
		}
		data, _ := json.Marshal(map[string]any{"pid": pid, "start_time": start, "run": id, "started_at": now, "heartbeat_at": beat})
		b452Write(t, filepath.Join(ws, ".code-forge", "workers", fmt.Sprintf("%d-%s.json", pid, start)), string(data))
		b452Record(t, home, id, base(id, extra))
	}
	want := map[string]string{
		"r-w1": "", "r-w2": "", "r-w4": "", "r-w6": "",
		"r-w3": fmt.Sprintf("warning: live worker %d is not pinned: run start --reattach --run r-w3\n", pid),
		"r-w5": fmt.Sprintf("warning: live worker %d is not pinned: run start --reattach --run r-w5 (pinned pid 1 is replaced)\n", pid),
	}
	for id, w := range want {
		code, _, errText := runCaptured(t, "status", "--run", id, "--json")
		if code != 0 || errText != w {
			t.Fatalf("%s: code=%d stderr=%q want %q", id, code, errText, w)
		}
	}
}
