package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/sign"
)

// B453 child: BLOCK_HV_ASK sets the terminal seam (notty, yes, no); the
// question text goes to BLOCK_HV_ASKED so the test can see it was asked.
func blockHVChild() {
	switch os.Getenv("BLOCK_HV_ASK") {
	case "yes", "no":
		answer := humanask.Confirmed
		if os.Getenv("BLOCK_HV_ASK") == "no" {
			answer = humanask.Cancelled
		}
		blockAskAvailable = func() bool { return true }
		blockAskConfirm = func(message string) (humanask.Result, error) {
			_ = os.WriteFile(os.Getenv("BLOCK_HV_ASKED"), []byte(message), 0600)
			return answer, nil
		}
	default:
		blockAskAvailable = func() bool { return false }
		blockAskConfirm = func(string) (humanask.Result, error) { return humanask.NoTTY, nil }
	}
	code, err := run(strings.Split(os.Getenv("BLOCK_HV_ARGS"), "|"))
	if err != nil {
		os.Stderr.WriteString(err.Error())
	}
	os.Exit(code)
}

type hvFixture struct {
	t                    *testing.T
	home, workspace, dir string
	key                  []byte
}

func newHVFixture(t *testing.T, block map[string]any, extra map[string]any) *hvFixture {
	t.Helper()
	f := &hvFixture{t: t, home: t.TempDir(), workspace: t.TempDir(), key: bytes.Repeat([]byte{7}, 32)}
	if out, err := exec.Command("git", "-C", f.workspace, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	f.dir = filepath.Join(f.home, ".code-forge", "runs")
	if err := os.MkdirAll(f.dir, 0700); err != nil {
		t.Fatal(err)
	}
	b := map[string]any{"status": "open", "owned_files": []any{"src/a.go"}, "owned_kinds": map[string]any{}, "attempt": 1, "acceptance": []any{map[string]any{"clause": "c1", "tests": []any{"t1"}}}}
	for k, v := range block {
		b[k] = v
	}
	record := map[string]any{"run_id": "r-test", "project": "unit", "workspace": f.workspace, "status": "active", "blocks": map[string]any{"B1": b}}
	for k, v := range extra {
		record[k] = v
	}
	data, _ := json.Marshal(record)
	if err := os.WriteFile(filepath.Join(f.dir, "r-test.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "r-test.key"), f.key, 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

// rows appends signed fixture rows to the project ledger.
func (f *hvFixture) rows(rows ...map[string]any) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Join(f.home, ".code-forge", "ledger"), 0700); err != nil {
		f.t.Fatal(err)
	}
	var buf bytes.Buffer
	for _, r := range rows {
		r["run"] = "r-test"
		signed, err := sign.SignRow(r, f.key)
		if err != nil {
			f.t.Fatal(err)
		}
		data, _ := json.Marshal(signed)
		buf.Write(append(data, '\n'))
	}
	file, err := os.OpenFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		f.t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.Write(buf.Bytes()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *hvFixture) ledger() []map[string]any {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	out := []map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			f.t.Fatalf("ledger line %s: %v", line, err)
		}
		out = append(out, row)
	}
	return out
}

func (f *hvFixture) record() map[string]any {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, "r-test.json"))
	var record map[string]any
	if err != nil || json.Unmarshal(data, &record) != nil {
		f.t.Fatalf("read record: %v", err)
	}
	return record
}

type hvResult struct {
	code           int
	stdout, stderr string
	asked          string
}

func (f *hvFixture) run(test, ask, args string) hvResult {
	f.t.Helper()
	asked := filepath.Join(f.t.TempDir(), "asked")
	cmd := exec.Command(os.Args[0], "-test.run=^"+test+"$")
	cmd.Env = append(os.Environ(), "HOME="+f.home, "BLOCK_HV_CHILD=1", "BLOCK_HV_ASK="+ask, "BLOCK_HV_ASKED="+asked, "BLOCK_HV_ARGS="+args, "CODE_FORGE_NODE=/nonexistent", "CODE_FORGE_NO_ERROR_LOG=1")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	code := 0
	if e, ok := err.(*exec.ExitError); ok {
		code = e.ExitCode()
	} else if err != nil {
		f.t.Fatal(err)
	}
	question, _ := os.ReadFile(asked)
	return hvResult{code, out.String(), stderr.String(), string(question)}
}

func (r hvResult) want(t *testing.T, code int, stdout, stderr string) {
	t.Helper()
	if r.code != code || r.stdout != stdout || r.stderr != stderr {
		t.Fatalf("got exit %d stdout %q stderr %q; want exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr, code, stdout, stderr)
	}
}

func (f *hvFixture) verified(row map[string]any) {
	f.t.Helper()
	got, err := sign.VerifyRow(row, f.key)
	if err != nil || !got.OK {
		f.t.Fatalf("row does not verify: %#v %v %v", row, got, err)
	}
}

func TestBlockAcceptHitGo(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	newWith := func(t *testing.T) (*hvFixture, string) {
		f := newHVFixture(t, nil, nil)
		transcript := filepath.Join(t.TempDir(), "t.txt")
		if err := os.WriteFile(transcript, []byte("hello\nrm -rf build\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return f, transcript
	}
	t.Run("usage and missing hit", func(t *testing.T) {
		f, transcript := newWith(t)
		f.run("TestBlockAcceptHitGo", "yes", "block|accept-hit|B1|--run|r-test|--line|x|--transcript|"+transcript+"|--reason|ok").want(t, 2, "", "block accept-hit: --line must be an integer\n")
		f.run("TestBlockAcceptHitGo", "yes", "block|accept-hit|B1|--run|r-test|--line|2|--transcript|"+transcript).want(t, 2, "", "block accept-hit: --reason is required\n")
		r := f.run("TestBlockAcceptHitGo", "yes", "block|accept-hit|B1|--run|r-test|--line|1|--transcript|"+transcript+"|--reason|ok")
		r.want(t, 1, "", "block accept-hit: accept-hit-no-such-hit: the named transcript hit was not found; nothing was recorded\n")
		if r.asked != "" || f.ledger() != nil {
			t.Fatalf("refusal asked %q or wrote rows", r.asked)
		}
	})
	t.Run("no terminal", func(t *testing.T) {
		f, transcript := newWith(t)
		f.run("TestBlockAcceptHitGo", "notty", "block|accept-hit|B1|--run|r-test|--line|2|--transcript|"+transcript+"|--reason|ok").want(t, 2, "", "block accept-hit needs the human at a terminal to confirm; nothing was recorded (there is no --yes)\n")
	})
	t.Run("cancelled", func(t *testing.T) {
		f, transcript := newWith(t)
		f.run("TestBlockAcceptHitGo", "no", "block|accept-hit|B1|--run|r-test|--line|2|--transcript|"+transcript+"|--reason|ok").want(t, 1, "", "block B1 accept-hit: cancelled — nothing was recorded\n")
		if f.ledger() != nil {
			t.Fatal("cancel wrote rows")
		}
	})
	t.Run("confirmed writes the signed row", func(t *testing.T) {
		f, transcript := newWith(t)
		r := f.run("TestBlockAcceptHitGo", "yes", "block|accept-hit|B1|--run|r-test|--line|2|--transcript|"+transcript+"|--reason|  false hit  ")
		r.want(t, 0, "block B1 accepted rm-rf transcript line 2 (by: human)\n", "")
		if want := "Accept false transcript hits rm-rf at line 2 in " + transcript + " of block B1 (run r-test)? Only the owner may say yes."; r.asked != want {
			t.Fatalf("question %q; want %q", r.asked, want)
		}
		rows := f.ledger()
		sum := sha256.Sum256([]byte("rm -rf build"))
		if len(rows) != 1 || rows[0]["event"] != "rule_break.accepted" || rows[0]["rule"] != "rm-rf" || rows[0]["line"] != float64(2) || rows[0]["line_sha256"] != hex.EncodeToString(sum[:]) || rows[0]["reason"] != "false hit" || rows[0]["by"] != "human" || rows[0]["block"] != "B1" {
			t.Fatalf("rows %#v", rows)
		}
		f.verified(rows[0])
	})
	t.Run("json with rule", func(t *testing.T) {
		f, transcript := newWith(t)
		f.run("TestBlockAcceptHitGo", "yes", "block|accept-hit|B1|--run|r-test|--rule|rm-rf|--line|2|--transcript|"+transcript+"|--reason|ok|--json").want(t, 0, `{"block":"B1","event":"rule_break.accepted","rule":"rm-rf","line":2,"reason":"ok","by":"human"}`+"\n", "")
	})
}

func TestBlockAmendAcceptanceGo(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	file := func(t *testing.T, text string) string {
		p := filepath.Join(t.TempDir(), "acc.yml")
		if err := os.WriteFile(p, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	t.Run("help", func(t *testing.T) {
		f := newHVFixture(t, nil, nil)
		f.run("TestBlockAmendAcceptanceGo", "notty", "block|amend-acceptance|--help").want(t, 0, "usage: code-forge block amend-acceptance <id> --run <r> --acceptance <file> --reason <text> [--json] [--help|-h]\n  --help, -h  Print this usage and exit without changing state.\n  --json      Print one JSON line on success instead of text.\n  --acceptance <file>  JSON/YAML list [{clause, tests}]; tests is a nonempty list of test ids.\n                       Human checks use {clause, kind: human_check} without tests.\n", "")
	})
	t.Run("usage, no terminal, unchanged", func(t *testing.T) {
		f := newHVFixture(t, nil, nil)
		same := file(t, "- clause: c1\n  tests: [t1]\n")
		f.run("TestBlockAmendAcceptanceGo", "yes", "block|amend-acceptance|B1|--run|r-test|--acceptance|"+same).want(t, 2, "", "block amend-acceptance: amend-acceptance needs one block id, --acceptance and a nonblank --reason without control characters\n")
		f.run("TestBlockAmendAcceptanceGo", "notty", "block|amend-acceptance|B1|--run|r-test|--acceptance|"+same+"|--reason|why").want(t, 2, "", "block amend-acceptance needs the human at a terminal to confirm; nothing was recorded (there is no --yes)\n")
		r := f.run("TestBlockAmendAcceptanceGo", "yes", "block|amend-acceptance|B1|--run|r-test|--acceptance|"+same+"|--reason|why")
		r.want(t, 1, "", "block amend-acceptance: acceptance-unchanged: the clause list is unchanged; nothing was recorded\n")
		if r.asked != "" {
			t.Fatal("unchanged acceptance asked the human")
		}
	})
	t.Run("active grant", func(t *testing.T) {
		grant := map[string]any{"autopilot": map[string]any{"grant_id": "g1", "status": "active", "until": "2099-01-01T00:00:00.000Z", "started_at": "2026-01-01T00:00:00.000Z", "scopes": []any{}, "deny": []any{}, "delegate": "L2", "caps": map[string]any{}, "stop_at": 0.8}}
		f := newHVFixture(t, nil, grant)
		human := file(t, "- clause: look\n  kind: human_check\n")
		r := f.run("TestBlockAmendAcceptanceGo", "notty", "block|amend-acceptance|B1|--run|r-test|--acceptance|"+human+"|--reason|why")
		r.want(t, 1, "", "block amend-acceptance: autopilot-human-check: an active autopilot grant cannot wait on human_check clauses: \"look\"; turn each into a clause with tests or ask the owner\n")
		tests := file(t, "- clause: c2\n  tests: [t2]\n")
		r = f.run("TestBlockAmendAcceptanceGo", "yes", "block|amend-acceptance|B1|--run|r-test|--acceptance|"+tests+"|--reason|why")
		r.want(t, 1, "", "block amend-acceptance: amend-acceptance is refused while an autopilot grant is active\n")
		if r.asked != "" || f.ledger() != nil {
			t.Fatalf("grant refusal asked %q or wrote rows", r.asked)
		}
	})
	t.Run("confirmed amends the registry", func(t *testing.T) {
		f := newHVFixture(t, nil, nil)
		watch := filepath.Join(f.workspace, ".code-forge", "queue", "watch")
		if err := os.MkdirAll(watch, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(watch, "B1.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		next := file(t, "- clause: c1\n  tests: [t1]\n- clause: look at it\n  kind: human_check\n")
		r := f.run("TestBlockAmendAcceptanceGo", "yes", "block|amend-acceptance|B1|--run|r-test|--acceptance|"+next+"|--reason|why|--json")
		if r.code != 0 || r.stderr != "" {
			t.Fatalf("exit %d stderr %q", r.code, r.stderr)
		}
		if r.asked != "Amend acceptance of block B1 (run r-test) from 1 clauses to 2 clauses? Only the owner may say yes." {
			t.Fatalf("question %q", r.asked)
		}
		rows := f.ledger()
		if len(rows) != 1 || rows[0]["event"] != "block.acceptance_amended" || rows[0]["clauses"] != float64(2) || rows[0]["reason"] != "why" || rows[0]["by"] != "human" {
			t.Fatalf("rows %#v", rows)
		}
		f.verified(rows[0])
		oldSum := sha256.Sum256([]byte(`[{"clause":"c1","tests":["t1"]}]`))
		if rows[0]["old_sha256"] != hex.EncodeToString(oldSum[:]) {
			t.Fatalf("old sha %v", rows[0]["old_sha256"])
		}
		var emitted map[string]any
		if json.Unmarshal([]byte(r.stdout), &emitted) != nil || emitted["event"] != "block.acceptance_amended" || emitted["new_sha256"] != rows[0]["new_sha256"] || emitted["mac"] != nil {
			t.Fatalf("json %q", r.stdout)
		}
		block := f.record()["blocks"].(map[string]any)["B1"].(map[string]any)
		checks, _ := block["human_checks"].([]any)
		if len(block["acceptance"].([]any)) != 2 || len(checks) != 1 || checks[0] != "look at it" || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(blockString(block["acceptance_revision"])) {
			t.Fatalf("block %#v", block)
		}
		if _, err := os.Stat(filepath.Join(watch, "B1.json")); !os.IsNotExist(err) {
			t.Fatalf("watch cursor kept: %v", err)
		}
	})
}

func TestBlockAgentWaiverVerbsGo(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	waived := func(f *hvFixture) {
		f.rows(
			map[string]any{"event": "review.triage", "block": "B1", "file": "src/a.go", "finding": "F1", "severity": "warning"},
			map[string]any{"event": "review.waived", "block": "B1", "file": "src/a.go", "finding": "F1", "severity": "warning", "reason": "fine", "verdict": "agree", "by": "agent"},
		)
	}
	t.Run("refusals", func(t *testing.T) {
		f := newHVFixture(t, nil, nil)
		f.run("TestBlockAgentWaiverVerbsGo", "yes", "block|unwaive|B1|--run|r-test|--file|src/a.go|--reason|x").want(t, 2, "", "block unwaive: block unwaive takes one block id and one finding id\n")
		f.run("TestBlockAgentWaiverVerbsGo", "yes", "block|unwaive|B1|F1|--run|r-test|--file|src/a.go|--reason| ").want(t, 2, "", "block unwaive: --reason must be nonblank and contain no control characters\n")
		f.run("TestBlockAgentWaiverVerbsGo", "yes", "block|unwaive|B1|F1|--run|r-test|--file|src/a.go|--reason|x").want(t, 1, "", "block unwaive: unwaive-no-agent-waiver: no verified agent waiver for this block, file and finding; nothing was recorded\n")
		f.run("TestBlockAgentWaiverVerbsGo", "yes", "block|ack-waivers|B1|--run|r-test").want(t, 1, "", "block ack-waivers: ack-waivers-none: the block has no counted agent waivers; nothing was recorded\n")
		if f.ledger() != nil {
			t.Fatal("refusals wrote rows")
		}
	})
	t.Run("no terminal", func(t *testing.T) {
		f := newHVFixture(t, nil, nil)
		waived(f)
		f.run("TestBlockAgentWaiverVerbsGo", "notty", "block|ack-waivers|B1|--run|r-test").want(t, 2, "", "block ack-waivers needs the human at a terminal to confirm; nothing was recorded (there is no --yes)\n")
	})
	t.Run("unwaive confirmed", func(t *testing.T) {
		f := newHVFixture(t, nil, nil)
		waived(f)
		r := f.run("TestBlockAgentWaiverVerbsGo", "yes", "block|unwaive|B1|F1|--run|r-test|--file|src/a.go|--reason|undo")
		r.want(t, 0, "block B1 unwaived F1 in src/a.go (by: human)\n", "")
		if r.asked != "Undo agent waivers for finding F1 in src/a.go of block B1 (run r-test)? Only the owner may say yes." {
			t.Fatalf("question %q", r.asked)
		}
		rows := f.ledger()
		last := rows[len(rows)-1]
		if len(rows) != 3 || last["event"] != "review.unwaived" || last["finding"] != "F1" || last["file"] != "src/a.go" || last["reason"] != "undo" || last["by"] != "human" {
			t.Fatalf("rows %#v", rows)
		}
		f.verified(last)
	})
	t.Run("ack-waivers confirmed lists and signs the digest", func(t *testing.T) {
		f := newHVFixture(t, nil, nil)
		waived(f)
		mac := f.ledger()[1]["mac"].(string)
		r := f.run("TestBlockAgentWaiverVerbsGo", "yes", "block|ack-waivers|B1|--run|r-test|--json")
		sum := sha256.Sum256([]byte(`[["src/a.go","F1","` + mac + `"]]`))
		digest := hex.EncodeToString(sum[:])
		r.want(t, 0, `{"event":"agent_waivers.acked","block":"B1","digest":"`+digest+`","count":1,"by":"human"}`+"\n", "block B1: agent waiver src/a.go F1 reason: fine; verdict: agree\n")
		rows := f.ledger()
		last := rows[len(rows)-1]
		if len(rows) != 3 || last["event"] != "agent_waivers.acked" || last["digest"] != digest {
			t.Fatalf("rows %#v", rows)
		}
		f.verified(last)
		r = newHVFixtureCopy(t, f).run("TestBlockAgentWaiverVerbsGo", "no", "block|ack-waivers|B1|--run|r-test")
		r.want(t, 1, "block B1: agent waiver src/a.go F1 reason: fine; verdict: agree\n", "block B1 ack-waivers: cancelled — nothing was recorded\n")
	})
}

// newHVFixtureCopy reuses f's home: the listing prints before the question.
func newHVFixtureCopy(t *testing.T, f *hvFixture) *hvFixture {
	return &hvFixture{t: t, home: f.home, workspace: f.workspace, dir: f.dir, key: f.key}
}

func TestBlockAttemptGo(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	t.Run("attempt counts and keeps claims", func(t *testing.T) {
		f := newHVFixture(t, nil, nil)
		f.rows(map[string]any{"event": "block.claim", "block": "B1", "path": "src/new.go"})
		f.run("TestBlockAttemptGo", "notty", "block|attempt|B1|--run|r-test").want(t, 0, "block B1 attempt 2\nkept claims: 1\n", "")
		block := f.record()["blocks"].(map[string]any)["B1"].(map[string]any)
		files := block["owned_files"].([]any)
		if block["attempt"] != float64(2) || len(files) != 2 || files[1] != "src/new.go" || block["owned_kinds"].(map[string]any)["src/new.go"] != "absent" {
			t.Fatalf("block %#v", block)
		}
		rows := f.ledger()
		last := rows[len(rows)-1]
		if last["event"] != "block.attempt" || last["attempt"] != float64(2) {
			t.Fatalf("rows %#v", rows)
		}
		f.verified(last)
		f.run("TestBlockAttemptGo", "notty", "block|attempt|B1|--run|r-test|--json").want(t, 0, `{"block":"B1","event":"attempt","attempt":3,"kept_claims":1}`+"\n", "")
	})
	t.Run("closed block", func(t *testing.T) {
		f := newHVFixture(t, map[string]any{"status": "closed"}, nil)
		f.run("TestBlockAttemptGo", "notty", "block|attempt|B1|--run|r-test").want(t, 1, "", "block attempt: block B1 is not open in run r-test\n")
	})
}

func TestBlockWaiveHumanGo(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	t.Run("unknown finding is refused before the question", func(t *testing.T) {
		f := newHVFixture(t, nil, nil)
		r := f.run("TestBlockWaiveHumanGo", "yes", "block|waive|B1|F1|--run|r-test|--file|src/a.go|--reason|ok")
		r.want(t, 1, "", "block waive: waive-unknown-finding: no review row of block B1 names finding F1 in src/a.go; nothing was recorded\n")
		if r.asked != "" {
			t.Fatal("asked about a refused waive")
		}
		f.run("TestBlockWaiveHumanGo", "yes", "block|waive|B1|F1|--run|r-test|--file|src/b.go|--reason|ok").want(t, 1, "", "block waive: waive-not-owned: block B1 does not own src/b.go; nothing was recorded\n")
	})
	t.Run("confirmed waive signs the human row", func(t *testing.T) {
		f := newHVFixture(t, nil, nil)
		f.rows(map[string]any{"event": "review.result", "block": "B1", "file": "src/a.go", "open": []any{map[string]any{"id": "F1", "severity": "critical"}}})
		r := f.run("TestBlockWaiveHumanGo", "yes", "block|waive|B1|F1|--run|r-test|--file|src/a.go|--reason| ok |--json")
		r.want(t, 0, `{"block":"B1","event":"waived","finding":"F1","file":"src/a.go","by":"human"}`+"\n", "")
		if r.asked != "Waive finding F1 in src/a.go of block B1 (run r-test)? Only the owner may say yes." {
			t.Fatalf("question %q", r.asked)
		}
		rows := f.ledger()
		last := rows[len(rows)-1]
		if len(rows) != 2 || last["event"] != "review.waived" || last["reason"] != "ok" || last["by"] != "human" || last["finding"] != "F1" {
			t.Fatalf("rows %#v", rows)
		}
		f.verified(last)
	})
	t.Run("cancelled", func(t *testing.T) {
		f := newHVFixture(t, nil, nil)
		f.rows(map[string]any{"event": "review.triage", "block": "B1", "file": "src/a.go", "finding": "F1", "severity": "warning"})
		f.run("TestBlockWaiveHumanGo", "no", "block|waive|B1|F1|--run|r-test|--file|src/a.go|--reason|ok").want(t, 1, "", "block B1 waive: cancelled — nothing was recorded\n")
		if len(f.ledger()) != 1 {
			t.Fatal("cancel wrote rows")
		}
	})
}
