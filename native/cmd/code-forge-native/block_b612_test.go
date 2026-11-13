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

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/ticket"
)

// B612: block waive --yes and block reopen --yes run their fresh L3 judge in
// Go. The fake claude CLI on PATH answers per FAKE_VERDICT, keeps each stdin
// it was given and counts its runs; Node is unreachable (CODE_FORGE_NODE is
// /nonexistent), so a handover can never pass these tests.

const b612Claude = `#!/bin/sh
cat > "$FAKE_DIR/stdin.$$"
printf '%s\n' "$*" >> "$FAKE_DIR/args"
echo x >> "$FAKE_DIR/calls"
case "$FAKE_VERDICT" in
agree) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"verdict":"agree","reason":"  fine  "},"usage":{"input_tokens":10,"output_tokens":5}}' ;;
disagree) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"verdict":"disagree","reason":"not enough"},"usage":{"input_tokens":10,"output_tokens":5}}' ;;
broken) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"verdict":"maybe","reason":"x"},"usage":{"input_tokens":10,"output_tokens":5}}' ;;
esac
`

const b612Content = "line one\nline two\nline three\n"

// b612Level is the line every judge spawn prints on stderr (spawn.mjs).
const b612Level = "level=L3 provider=anthropic model=claude-x effort=high fallback_step=0\n"

type b612Fixture struct {
	*hvFixture
	bin, fake, tmp string
}

// newB612Fixture: the B453 run fixture with an L3 anthropic config snapshot
// (none for a legacy run), src/a.go in the workspace and the fake claude.
func newB612Fixture(t *testing.T, snapshot bool) *b612Fixture {
	t.Helper()
	extra := map[string]any{}
	if snapshot {
		cfg := map[string]any{"version": 1.0, "levels": map[string]any{"L3": map[string]any{"provider": "anthropic", "model": "claude-x", "effort": "high"}}}
		canonical, err := canon.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(canonical)
		hash := hex.EncodeToString(sum[:])
		extra["config"] = map[string]any{"hash": hash, "snapshots": map[string]any{hash: cfg}}
	}
	f := &b612Fixture{hvFixture: newHVFixture(t, nil, extra), bin: t.TempDir(), fake: t.TempDir(), tmp: t.TempDir()}
	if err := os.MkdirAll(filepath.Join(f.workspace, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.workspace, "src", "a.go"), []byte(b612Content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.bin, "claude"), []byte(b612Claude), 0o755); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *b612Fixture) run(test, verdict, args string) hvResult {
	f.t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^"+test+"$")
	cmd.Env = append(os.Environ(), "HOME="+f.home, "BLOCK_HV_CHILD=1", "BLOCK_HV_ASK=notty", "BLOCK_HV_ARGS="+args, "CODE_FORGE_NODE=/nonexistent", "CODE_FORGE_NO_ERROR_LOG=1",
		"PATH="+f.bin+":/usr/bin:/bin", "TMPDIR="+f.tmp, "CODE_FORGE_TMP_ROOT=", "FAKE_DIR="+f.fake, "FAKE_VERDICT="+verdict)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	code := 0
	if e, ok := err.(*exec.ExitError); ok {
		code = e.ExitCode()
	} else if err != nil {
		f.t.Fatal(err)
	}
	return hvResult{code: code, stdout: out.String(), stderr: stderr.String()}
}

// calls is how many judge sessions ran; packets is every stdin they got.
func (f *b612Fixture) calls() int {
	data, _ := os.ReadFile(filepath.Join(f.fake, "calls"))
	return strings.Count(string(data), "x")
}

func (f *b612Fixture) packets() string {
	names, _ := filepath.Glob(filepath.Join(f.fake, "stdin.*"))
	var all strings.Builder
	for _, name := range names {
		data, _ := os.ReadFile(name)
		all.Write(data)
	}
	return all.String()
}

// rawLedger is the project ledger's lines as written.
func (f *b612Fixture) rawLedger() []string {
	data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"))
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (f *b612Fixture) triage() {
	f.rows(map[string]any{"event": "review.triage", "block": "B1", "file": "src/a.go", "finding": "F1", "severity": "warning", "claim": "bad thing", "evidence": "line two", "fix": "do x"})
}

func TestB612WaiveYesJudge(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	const args = "block|waive|B1|F1|--run|r-test|--file|src/a.go|--reason| see src/a.go:2 |--yes"
	t.Run("agreed judge signs the agent waiver", func(t *testing.T) {
		f := newB612Fixture(t, true)
		f.triage()
		f.run("TestB612WaiveYesJudge", "agree", args).want(t, 0, "block B1 waived F1 in src/a.go (by: agent)\n", b612Level)
		if f.calls() != 1 {
			t.Fatalf("judge sessions %d", f.calls())
		}
		packet := f.packets()
		for _, want := range []string{
			`"finding":{"id":"F1","severity":"warning","claim":"bad thing","evidence":"line two"},"reason":" see src/a.go:2 ","file":{"path":"src/a.go","content":"line one\nline two\nline three\n"},"evidence":[{"path":"src/a.go","start":1,"end":3,"content":"1| line one\n2| line two\n3| line three"}]`,
		} {
			if !strings.Contains(packet, want) {
				t.Fatalf("packet %q lacks %q", packet, want)
			}
		}
		lines := f.rawLedger()
		if len(lines) != 2 || !strings.HasPrefix(lines[1], `{"run":"r-test","severity":"warning","verdict":"agree","judge_reason":"fine","event":"review.waived","block":"B1","file":"src/a.go","finding":"F1","reason":"see src/a.go:2","by":"agent","ts":"`) {
			t.Fatalf("ledger %q", lines)
		}
		f.verified(f.ledger()[1])
		if names, _ := filepath.Glob(filepath.Join(f.tmp, "*", "*", "agent-waiver", "judge-*")); len(names) != 0 {
			t.Fatalf("judge folder left: %v", names)
		}
	})
	t.Run("agreed judge with --json", func(t *testing.T) {
		f := newB612Fixture(t, true)
		f.triage()
		f.run("TestB612WaiveYesJudge", "agree", args+"|--json").want(t, 0, `{"block":"B1","event":"waived","finding":"F1","file":"src/a.go","by":"agent"}`+"\n", b612Level)
	})
	t.Run("disagreeing judge records nothing", func(t *testing.T) {
		f := newB612Fixture(t, true)
		f.triage()
		f.run("TestB612WaiveYesJudge", "disagree", args).want(t, 1, "", b612Level+"block waive: waive-yes-disagreed: not enough; nothing was recorded\n")
		if len(f.rawLedger()) != 1 {
			t.Fatal("a refused waive wrote a row")
		}
	})
	t.Run("broken answers retry once then refuse", func(t *testing.T) {
		f := newB612Fixture(t, true)
		f.triage()
		f.run("TestB612WaiveYesJudge", "broken", args).want(t, 1, "", b612Level+b612Level+"block waive: waive-yes-broken: Judge answer broken: no valid answer after retry and fallback.; nothing was recorded\n")
		if f.calls() != 2 || len(f.rawLedger()) != 1 {
			t.Fatalf("judge sessions %d, ledger %d lines", f.calls(), len(f.rawLedger()))
		}
	})
	t.Run("legacy run without a config snapshot", func(t *testing.T) {
		f := newB612Fixture(t, false)
		f.triage()
		f.run("TestB612WaiveYesJudge", "agree", args).want(t, 1, "", "block waive: waive-yes-broken: Judge answer broken: the judge session failed.; nothing was recorded\n")
		if f.calls() != 0 {
			t.Fatal("a legacy run spawned a judge")
		}
	})
}

// lost gives src/a.go a review.state anchor at seq 3 with no state file and
// no state body: Node's "lost" review state.
func (f *b612Fixture) lost() {
	f.rows(map[string]any{"event": "review.state", "block": "B1", "file": "src/a.go", "seq": 3, "state_sha": strings.Repeat("0", 64)})
}

func TestB612ReopenYesJudge(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	const args = "block|reopen|B1|--run|r-test|--file|src/a.go|--reason|src/a.go:1 changed|--yes"
	sum := sha256.Sum256([]byte(b612Content))
	toHash := hex.EncodeToString(sum[:])
	t.Run("agreed judge signs the row and queues the reopened attempt", func(t *testing.T) {
		f := newB612Fixture(t, true)
		f.lost()
		r := f.run("TestB612ReopenYesJudge", "agree", args)
		lines := f.rawLedger()
		if r.code != 0 || len(lines) != 2 {
			t.Fatalf("exit %d stderr %q ledger %q", r.code, r.stderr, lines)
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(lines[1]), &row); err != nil {
			t.Fatal(err)
		}
		f.verified(row)
		ts, _ := row["ts"].(string)
		if !strings.HasPrefix(lines[1], `{"run":"r-test","event":"review.reopened","block":"B1","file":"src/a.go","from":"lost","stop_reason":null,"at_seq":3,"from_hash":null,"to_hash":"`+toHash+`","reason":"src/a.go:1 changed","by":"agent","verdict":{"verdict":"agree","reason":"fine"},"ts":"`) {
			t.Fatalf("row %s", lines[1])
		}
		top, _ := filepath.EvalSymlinks(f.workspace)
		id := ticket.ReviewID(ticket.Parts{Run: "r-test", Block: "B1", File: "src/a.go", ContentHash: toHash, ReopenedAt: &ts})
		r.want(t, 0, "block B1 reopened src/a.go (from lost; by: agent)\nticket "+id+"\nnext: code-forge review-file --wait "+id+"\n", b612Level)
		data, err := os.ReadFile(filepath.Join(top, ".code-forge", "queue", id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var queued map[string]any
		if err := json.Unmarshal(data, &queued); err != nil || queued["reopened_at"] != ts || queued["content_hash"] != toHash || queued["file"] != "src/a.go" {
			t.Fatalf("ticket %s %v", data, err)
		}
		if packet := f.packets(); !strings.Contains(packet, `{"stop_reason":null,"findings":[],"reason":"src/a.go:1 changed","file":{"path":"src/a.go","content":"line one\nline two\nline three\n"},"evidence":[{"path":"src/a.go","start":1,"end":3,`) {
			t.Fatalf("packet %q", packet)
		}
	})
	t.Run("agreed judge with --json", func(t *testing.T) {
		f := newB612Fixture(t, true)
		f.lost()
		r := f.run("TestB612ReopenYesJudge", "agree", args+"|--json")
		if r.code != 0 || !regexp.MustCompile(`^\{"block":"B1","event":"reopened","file":"src/a.go","from":"lost","stop_reason":null,"at_seq":3,"from_hash":null,"to_hash":"`+toHash+`","ticket_cleared":false,"by":"agent","ticket":"[0-9a-f]{24}"\}\n$`).MatchString(r.stdout) || r.stderr != b612Level {
			t.Fatalf("exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
		}
	})
	t.Run("disagreeing judge records and queues nothing", func(t *testing.T) {
		f := newB612Fixture(t, true)
		f.lost()
		f.run("TestB612ReopenYesJudge", "disagree", args).want(t, 1, "", b612Level+"block reopen: reopen-yes-disagreed: not enough; nothing was recorded\n")
		if len(f.rawLedger()) != 1 {
			t.Fatal("a refused reopen wrote a row")
		}
		top, _ := filepath.EvalSymlinks(f.workspace)
		if names, _ := filepath.Glob(filepath.Join(top, ".code-forge", "queue", "*.json")); len(names) != 0 {
			t.Fatalf("queued %v", names)
		}
	})
	t.Run("broken judge refuses as disagreed", func(t *testing.T) {
		f := newB612Fixture(t, true)
		f.lost()
		f.run("TestB612ReopenYesJudge", "broken", args).want(t, 1, "", b612Level+b612Level+"block reopen: reopen-yes-disagreed: Judge answer broken: no valid answer after retry and fallback.; nothing was recorded\n")
	})
}
