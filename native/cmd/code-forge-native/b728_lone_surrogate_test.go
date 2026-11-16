package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// B728: readers that handed over a lone surrogate escape (\udXXX) now keep it
// as JSON.parse does (WTF-8 in Go, ledger.ParseOrdered / jsonx). Expected
// bytes are Node's: with B728_NODE_GOLDEN=1 each test runs
// CODE_FORGE_NATIVE=0 ./bin/code-forge on the same fixture first, logs what
// Node printed and wrote, and (for the s2 prompt) records Node's packet.md
// under testdata/b728.

// b728Dir is this package's source directory (fixtures chdir away from it).
func b728Dir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	return filepath.Dir(file)
}

// b728NodeGolden runs the Node CLI in the current directory with the test's
// environment (HOME, PATH fakes, TMPDIR) when B728_NODE_GOLDEN=1. It returns
// false when golden mode is off.
func b728NodeGolden(t *testing.T, extraEnv []string, args ...string) (code int, out, errText string, ran bool) {
	t.Helper()
	b760NoNodeRecorder(t, "B728_NODE_GOLDEN")
	if os.Getenv("B728_NODE_GOLDEN") != "1" {
		return 0, "", "", false
	}
	bin := filepath.Join(b728Dir(t), "..", "..", "..", "bin", "code-forge")
	// the fixture's PATH hides node: B728_NODE names it
	node := os.Getenv("B728_NODE")
	cmd := exec.Command(bin, args...)
	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CODE_FORGE_NODE=") || strings.HasPrefix(kv, "CODE_FORGE_NATIVE=") || strings.HasPrefix(kv, "PATH=") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(append(env, "PATH="+os.Getenv("PATH")+":"+filepath.Dir(node), "CODE_FORGE_NATIVE=0", "CODE_FORGE_HANDOVER_LOG=0"), extraEnv...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		e, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = e.ExitCode()
	}
	t.Logf("NODE %v exit=%d\nout=%q\nerr=%q", args, code, stdout.String(), stderr.String())
	return code, stdout.String(), stderr.String(), true
}

// b728Claude is b550Claude's s2 answer with the prompt it was given saved to
// $FAKE_CAPTURE.
const b728Claude = `#!/bin/sh
cat > "$FAKE_CAPTURE"
printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"decision":"go","confidence":0.8,"reason":"fine é","overrule":false,"ask_human":false,"human_question":null},"usage":{"input_tokens":100,"output_tokens":20}}'
`

// TestB728S2PacketLoneSurrogateInGo: an s2 packet whose question, options,
// System 1 answer and context hold lone surrogate escapes runs in Go: the
// prompt Go writes (packet.md, piped to the CLI) is byte-equal to Node's
// (U+FFFD for each lone surrogate in text, \udXXX inside the JSON of s1, the
// context cut at the same byte), and stdout, stderr and the session row are
// Node's. Before B728 the packet handed over (s2-input).
func TestB728S2PacketLoneSurrogateInGo(t *testing.T) {
	long := strings.Repeat(`ab\ud800`, 3400) + `\udc00tail`
	for _, c := range []struct{ name, packet string }{
		{"every field", `{"question":"  Ship \ud800 it?\udfff ","options":["yes \udc00","no",3],"s1":{"lane":"L1\ud83d","k\udead":"v"},"context":"ctx \ud800😀 end"}`},
		{"context cut", `{"question":"Q\ud800","context":"` + long + `"}`},
		{"context cut shifted", `{"question":"Q\ud800","context":"x` + long + `"}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, repo := newB550Fixture(t, "anthropic")
			b460Write(t, filepath.Join(f.workspace, "bin", "claude"), b728Claude)
			b460Write(t, filepath.Join(repo, "packet.json"), c.packet)
			capture := filepath.Join(f.workspace, "prompt.txt")
			t.Setenv("FAKE_CAPTURE", capture)
			golden := filepath.Join(b728Dir(t), "testdata", "b728", "s2-"+strings.ReplaceAll(c.name, " ", "-")+".prompt.txt")
			if _, _, _, ran := b728NodeGolden(t, nil, "s2", "--packet", "packet.json"); ran {
				prompt, err := os.ReadFile(capture)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("NODE ledger %v", b477Ledger(t, f.home))
				if err := os.WriteFile(golden, prompt, 0o644); err != nil {
					t.Fatal(err)
				}
				_ = os.RemoveAll(filepath.Join(f.home, ".code-forge", "ledger"))
				if err := os.Remove(capture); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			code, out, errText := b460Call(t, "s2", "--packet", "packet.json")
			if code != 0 || out != `{"decision":"go","confidence":0.8,"reason":"fine é","overrule":false,"ask_human":false,"human_question":null}`+"\n" || errText != b550A {
				t.Fatalf("= %d %q %q", code, out, errText)
			}
			got, err := os.ReadFile(capture)
			if err != nil || string(got) != string(want) {
				t.Fatalf("prompt (%v):\n%q\nNode:\n%q", err, got, want)
			}
			rows := b477Ledger(t, f.home)
			wantRow := `{"event":"session","role":"s2","level":"L3","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"run":null,"block":null,"source":"s2","status":"ok","tokens_in":100,"tokens_out":20,"tokens_source":"reported","usd":0.0036,"cost_source":"estimated","duration_ms":N}`
			if len(rows) != 1 || rows[0] != wantRow {
				t.Fatalf("ledger rows %v", rows)
			}
			if got := b460Handovers(t, f.home); len(got) != 0 {
				t.Fatalf("handed over: %v", got)
			}
		})
	}
}

// b728Golden compares got with testdata/b728/<name>, writing Node's value
// there first in golden mode (record non-empty).
func b728Golden(t *testing.T, name, record, got string) {
	t.Helper()
	path := filepath.Join(b728Dir(t), "testdata", "b728", name)
	if record != "" {
		if err := os.WriteFile(path, []byte(record), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s:\n%q\nNode:\n%q", name, got, want)
	}
}

var b728DecisionID = regexp.MustCompile(`"decision_id": ?"[0-9a-f-]{36}"`)

// TestB728JevAskStateLoneSurrogateInGo: a --state file whose keys and values
// hold lone surrogate escapes runs in Go: the Jev request body keeps each one
// as \udXXX (JSON.stringify), and stdout and the decision row are Node's; the
// --rules answer reads keywordsFound with them. Before B728 it handed over
// (jev-ask-state). Node golden: its fetch is replaced through
// NODE_OPTIONS=--import by B728_FETCH_STUB, which records the request.
func TestB728JevAskStateLoneSurrogateInGo(t *testing.T) {
	state := `{"filesChanged":2,"note\ud800":"a\udfffb","pair":"\ud83d\ude00","keywordsFound":["Auth\ud800","Money","\udc00"],"linesAdded":10,"touchesMigration":false,"touchesPolicyOrMiddleware":false,"pathFloorHit":false}`
	for _, c := range []struct {
		name string
		args []string
	}{
		{"jev", []string{"jev", "ask", "lane", "--state", "surrogate.json", "--block", "B1"}},
		{"rules", []string{"jev", "ask", "risk", "--state", "surrogate.json", "--rules"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, fake := newB550Jev(t, "version: 1\n", 200, b550JevOK, false)
			b460Write(t, "surrogate.json", state)
			var recOut, recRows, recReq string
			capture := filepath.Join(f.workspace, "b728-request.txt")
			env := []string{"NODE_OPTIONS=--import=" + os.Getenv("B728_FETCH_STUB"), "B728_FETCH_CAPTURE=" + capture, "B728_FETCH_BODY=" + b550JevOK}
			if code, out, _, ran := b728NodeGolden(t, env, c.args...); ran {
				if code != 0 {
					t.Fatalf("node exit %d", code)
				}
				recOut = b728DecisionID.ReplaceAllString(out, `"decision_id":"ID"`)
				recRows = b728DecisionID.ReplaceAllString(strings.Join(b477Ledger(t, f.home), "\n"), `"decision_id":"ID"`) + "\n"
				if data, err := os.ReadFile(capture); err == nil {
					recReq = string(data)
				} else {
					recReq = "(no request)"
				}
				_ = os.RemoveAll(filepath.Join(f.home, ".code-forge", "ledger"))
			}
			code, out, errText := b460Call(t, c.args...)
			if code != 0 || errText != "" {
				t.Fatalf("= %d %q %q", code, out, errText)
			}
			b728Golden(t, "jev-"+c.name+".out.txt", recOut, b728DecisionID.ReplaceAllString(out, `"decision_id":"ID"`))
			b728Golden(t, "jev-"+c.name+".rows.txt", recRows, b728DecisionID.ReplaceAllString(strings.Join(b477Ledger(t, f.home), "\n"), `"decision_id":"ID"`)+"\n")
			req := "(no request)"
			if len(fake.calls) == 1 {
				req = fake.calls[0]
			} else if len(fake.calls) > 1 {
				t.Fatalf("calls %q", fake.calls)
			}
			b728Golden(t, "jev-"+c.name+".request.txt", recReq, req)
			b550NoHandover(t, f)
		})
	}
}

// TestB728AcceptHitCodexLoneSurrogateIDs: Codex item ids with lone surrogate
// escapes pair in Go as JavaScript strings compare (\ud800 is not \ud801 nor
// U+FFFD; \ud83d\ude00 is 😀). Before B728 Go handed the log over. Node golden:
// prepareAcceptHit (src/review/accept-hit.mjs) on the same temp HOME, its
// rows' rule, line and line_sha256 recorded under testdata/b728.
func TestB728AcceptHitCodexLoneSurrogateIDs(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	codex := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t"}`,
		`{"type":"item.started","item":{"id":"\ud800","type":"command_execution","command":"git stash"}}`,
		`{"type":"item.completed","item":{"id":"\ud801","type":"command_execution","command":"git stash"}}`,
		`{"type":"item.completed","item":{"id":"\ud800","type":"command_execution","command":"git stash"}}`,
		`{"type":"item.started","item":{"id":"\ud83d\ude00","command":"git stash"}}`,
		`{"type":"item.completed","item":{"id":"😀","command":"git stash"}}`,
		`{"type":"item.completed","item":{"id":"\ufffd","command":"git stash \udc00"}}`,
	}, "\n") + "\n"
	for _, line := range []string{"2", "3", "5", "7"} {
		t.Run("line "+line, func(t *testing.T) {
			f := newHVFixture(t, nil, nil)
			transcript := filepath.Join(t.TempDir(), "codex.jsonl")
			if err := os.WriteFile(transcript, []byte(codex), 0o600); err != nil {
				t.Fatal(err)
			}
			record := ""
			b760NoNodeRecorder(t, "B728_NODE_GOLDEN")
			if os.Getenv("B728_NODE_GOLDEN") == "1" {
				src := filepath.Join(b728Dir(t), "..", "..", "..", "src", "review", "accept-hit.mjs")
				script := `const { prepareAcceptHit } = await import(process.argv[1]);
const { rows } = await prepareAcceptHit({ runId: 'r-test', id: 'B1', flags: { line: process.argv[2], transcript: process.argv[3], reason: 'ok' }, positionals: ['B1'] });
process.stdout.write(rows.map((r) => r.rule + ' ' + r.line + ' ' + r.line_sha256 + '\n').join(''));`
				cmd := exec.Command(os.Getenv("B728_NODE"), "--input-type=module", "-e", script, src, line, transcript)
				cmd.Env = append(os.Environ(), "HOME="+f.home)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("node: %v %s", err, out)
				}
				t.Logf("NODE line %s:\n%s", line, out)
				record = string(out)
			}
			r := f.run("TestB728AcceptHitCodexLoneSurrogateIDs", "yes", "block|accept-hit|B1|--run|r-test|--line|"+line+"|--transcript|"+transcript+"|--reason|ok")
			if r.code != 0 || r.stderr != "" {
				t.Fatalf("= %d %q %q", r.code, r.stdout, r.stderr)
			}
			var got strings.Builder
			for _, row := range f.ledger() {
				f.verified(row)
				got.WriteString(fmt.Sprintf("%v %v %v\n", row["rule"], row["line"], row["line_sha256"]))
			}
			b728Golden(t, "accept-hit-line-"+line+".txt", record, got.String())
		})
	}
}
