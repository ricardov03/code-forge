package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/session"
)

// B477: `code-forge spawn` runs a foreground closed-book session in Go. The
// expected lines are what Node printed for the same fixture
// (CODE_FORGE_NATIVE=0 ./bin/code-forge spawn ..., fake claude and grok on
// PATH), duration_ms aside.

const b477Claude = `#!/bin/sh
cat > /dev/null
case "$FAKE_MODE" in
ok) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"fine","reviewed_hunks":["h1"],"findings":[]},"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":5}}' ;;
invalid) printf '%s' '{"is_error":false,"result":"{\"passed\":true}"}' ;;
p402) printf '%s' '{"is_error":true,"api_error_status":402,"result":"payment"}'; exit 1 ;;
fail) echo "boom" >&2; exit 5 ;;
esac
`

const b477Grok = `#!/bin/sh
printf '%s' "{\"text\":\"{\\\"passed\\\":false,\\\"summary\\\":\\\"g\\\",\\\"reviewed_hunks\\\":[],\\\"findings\\\":[]}\",\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3}}"
`

const b477Config = "version: 1\nlevels:\n  L1:\n    provider: anthropic\n    model: claude-x\n    effort: high\n    fallback:\n      - provider: openai\n        model: gpt-x\n      - provider: xai\n        model: grok-x\n  L2:\n    provider: xai\n    model: grok-x\n  L3:\n    provider: openai\n    model: gpt-x\n"

const b477Skip = "fallback skipped: 1 openai step(s) — codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author\n"

var b477Duration = regexp.MustCompile(`"duration_ms":\d+`)

func b477Norm(s string) string { return b477Duration.ReplaceAllString(s, `"duration_ms":N`) }

// newB477Fixture: a temp HOME, a project with b477Config, a brief, the
// finding schema, fake claude/grok first on PATH; returns the workspace.
func newB477Fixture(t *testing.T) (runFixture, string) {
	t.Helper()
	f := newRunFixture(t)
	bin := filepath.Join(f.workspace, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	b460Write(t, filepath.Join(bin, "claude"), b477Claude)
	b460Write(t, filepath.Join(bin, "grok"), b477Grok)
	for _, n := range []string{"claude", "grok"} {
		if err := os.Chmod(filepath.Join(bin, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(f.workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	b460Git(t, repo, "init", "-q")
	b460Write(t, filepath.Join(repo, ".code-forge.yml"), b477Config)
	b460Write(t, filepath.Join(repo, "brief.md"), "review this please, the packet line here\n")
	here, _ := os.Getwd()
	schemaPath := filepath.Join(here, "..", "..", "..", "src", "review", "finding.schema.json")
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	b460Write(t, filepath.Join(repo, "schema.json"), string(data))
	before := session.FindingSchemaPath
	session.FindingSchemaPath = func() (string, error) { return schemaPath, nil }
	t.Cleanup(func() { session.FindingSchemaPath = before })
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	tmp := filepath.Join(f.workspace, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	t.Setenv("CODE_FORGE_TMP_ROOT", "")
	t.Setenv("CODE_FORGE_RUN_ID", "")
	real, _ := filepath.EvalSymlinks(repo)
	b460Chdir(t, real)
	return f, real
}

func b477Ledger(t *testing.T, home string) []string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(home, ".code-forge", "ledger", "*.jsonl"))
	out := []string{}
	for _, file := range files {
		data, _ := os.ReadFile(file)
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line != "" {
				out = append(out, regexp.MustCompile(`,"ts":"[^"]*"`).ReplaceAllString(b477Norm(line), ""))
			}
		}
	}
	return out
}

func TestB477SpawnReviewerRunsInGo(t *testing.T) {
	f, _ := newB477Fixture(t)
	t.Setenv("FAKE_MODE", "ok")
	code, out, errText := b460Call(t, "spawn", "--level", "L1", "--role", "reviewer", "--brief", "brief.md", "--schema", "schema.json")
	want := `{"status":"ok","answer":{"passed":true,"summary":"fine","reviewed_hunks":["h1"],"findings":[]},"usage":{"tokens_in":105,"tokens_out":20,"tokens_source":"reported"},"duration_ms":N,"exit_code":0,"stderr":"","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"row":{"event":"session","role":"reviewer","level":"L1","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"run":null,"block":null,"status":"ok","tokens_in":105,"tokens_out":20,"tokens_source":"reported","usd":0.0004,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"anthropic","model":"claude-x","status":"ok","reason":null}]}` + "\n"
	if code != 0 || b477Norm(out) != want || errText != b477Skip+"level=L1 provider=anthropic model=claude-x effort=high fallback_step=0\n" {
		t.Fatalf("spawn = %d\n%s\n%q", code, out, errText)
	}
	rows := b477Ledger(t, f.home)
	if len(rows) != 1 || rows[0] != `{"event":"session","role":"reviewer","level":"L1","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"run":null,"block":null,"status":"ok","tokens_in":105,"tokens_out":20,"tokens_source":"reported","usd":0.0004,"cost_source":"estimated","duration_ms":N}` {
		t.Fatalf("ledger rows = %v", rows)
	}
	if got := b460Handovers(t, f.home); len(got) != 0 {
		t.Fatalf("handed over: %v", got)
	}
	// the lazy proc root is gone once the session ended
	if left, _ := filepath.Glob(filepath.Join(f.workspace, "tmp", "code-forge", "proc-*")); len(left) != 0 {
		t.Fatalf("proc root left behind: %v", left)
	}
}

func TestB477SpawnOutcomesMatchNode(t *testing.T) {
	cases := []struct {
		name, mode string
		args       []string
		code       int
		out, err   string
	}{
		{"402 falls to the xai step", "p402", []string{"--level", "L1", "--role", "reviewer", "--schema", "schema.json"}, 0,
			`{"status":"ok","answer":{"passed":false,"summary":"g","reviewed_hunks":[],"findings":[]},"usage":{"tokens_in":7,"tokens_out":3,"tokens_source":"reported"},"duration_ms":N,"exit_code":0,"stderr":"","provider":"xai","model":"grok-x","effort":null,"fallback_step":2,"row":{"event":"session","role":"reviewer","level":"L1","provider":"xai","model":"grok-x","effort":null,"fallback_step":2,"run":null,"block":null,"status":"ok","tokens_in":7,"tokens_out":3,"tokens_source":"reported","usd":0,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"anthropic","model":"claude-x","status":"unavailable","reason":"http-402"},{"fallback_step":2,"provider":"xai","model":"grok-x","status":"ok","reason":null}]}` + "\n",
			b477Skip + "level=L1 provider=anthropic model=claude-x effort=high fallback_step=0\nlevel=L1 provider=xai model=grok-x effort=none fallback_step=2\n"},
		{"invalid output", "invalid", []string{"--level", "L1", "--role", "reviewer", "--schema", "schema.json"}, 1,
			`{"status":"invalid-output","reason":"answer does not match the schema","answer":null,"usage":{"tokens_in":11,"tokens_out":12,"tokens_source":"estimated"},"duration_ms":N,"exit_code":0,"stderr":"","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"row":{"event":"session","role":"reviewer","level":"L1","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"run":null,"block":null,"status":"invalid-output","reason":"answer does not match the schema","tokens_in":11,"tokens_out":12,"tokens_source":"estimated","usd":0.0001,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"anthropic","model":"claude-x","status":"invalid-output","reason":"answer does not match the schema"}]}` + "\n",
			b477Skip + "level=L1 provider=anthropic model=claude-x effort=high fallback_step=0\n"},
		{"failed exit", "fail", []string{"--level", "L1", "--role", "s2"}, 1,
			`{"status":"failed","reason":"exit 5","answer":null,"usage":{"tokens_in":11,"tokens_out":0,"tokens_source":"estimated"},"duration_ms":N,"exit_code":5,"stderr":"boom\n","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"row":{"event":"session","role":"s2","level":"L1","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"run":null,"block":null,"source":"s2","status":"failed","reason":"exit 5","tokens_in":11,"tokens_out":0,"tokens_source":"estimated","usd":0,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"anthropic","model":"claude-x","status":"failed","reason":"exit 5"}]}` + "\n",
			b477Skip + "level=L1 provider=anthropic model=claude-x effort=high fallback_step=0\n"},
		{"closed-book refusal", "ok", []string{"--level", "L3", "--role", "reviewer"}, 2, "",
			"spawn: codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author\n"},
		{"print-argv", "ok", []string{"--level", "L2", "--role", "reviewer", "--print-argv"}, 0,
			`{"argv":["grok","--prompt-file","<repo>/brief.md","-m","grok-x","--disallowed-tools","Bash,Read,Write,Edit,Glob,Grep,WebFetch,WebSearch","--deny","*","--no-plan","--no-subagents","--max-turns","1","--permission-mode","dontAsk","--cwd","<session>/cwd"],"cwd":"<session>/cwd","provider":"xai","model":"grok-x","effort":null,"fallback_step":0}` + "\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, repo := newB477Fixture(t)
			t.Setenv("FAKE_MODE", tc.mode)
			args := append([]string{"spawn", "--brief", "brief.md"}, tc.args...)
			code, out, errText := b460Call(t, args...)
			out = strings.ReplaceAll(b477Norm(out), repo, "<repo>")
			if code != tc.code || out != tc.out || errText != tc.err {
				t.Fatalf("%v = %d\n%s\n%q", args, code, out, errText)
			}
			if got := b460Handovers(t, f.home); len(got) != 0 {
				t.Fatalf("handed over: %v", got)
			}
		})
	}
}

func TestB477SpawnCliMissingIsUnavailable(t *testing.T) {
	f, _ := newB477Fixture(t)
	t.Setenv("PATH", filepath.Join(f.workspace, "nobin")+":/usr/bin:/bin")
	code, out, errText := b460Call(t, "spawn", "--level", "L1", "--role", "reviewer", "--brief", "brief.md", "--schema", "schema.json")
	want := `{"status":"unavailable","reason":"cli-missing","answer":null,"usage":{"tokens_in":11,"tokens_out":0,"tokens_source":"estimated"},"duration_ms":N,"exit_code":null,"stderr":"","provider":"xai","model":"grok-x","effort":null,"fallback_step":2,"row":{"event":"session","role":"reviewer","level":"L1","provider":"xai","model":"grok-x","effort":null,"fallback_step":2,"run":null,"block":null,"status":"unavailable","reason":"cli-missing","tokens_in":11,"tokens_out":0,"tokens_source":"estimated","usd":0,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"anthropic","model":"claude-x","status":"unavailable","reason":"cli-missing"},{"fallback_step":2,"provider":"xai","model":"grok-x","status":"unavailable","reason":"cli-missing"}]}` + "\n"
	if code != 3 || b477Norm(out) != want || !strings.HasSuffix(errText, "fallback_step=2\n") {
		t.Fatalf("spawn = %d\n%s\n%q", code, out, errText)
	}
	if rows := b477Ledger(t, f.home); len(rows) != 2 {
		t.Fatalf("ledger rows = %v", rows)
	}
}

// The cases Go does not serve keep the spawn-session hand-over.
func TestB477SpawnUnportedCasesHandOver(t *testing.T) {
	f, _ := newB477Fixture(t)
	b460Write(t, "other.json", `{"type":"object"}`)
	for _, args := range [][]string{
		{"spawn", "--level", "L1", "--role", "reviewer", "--brief", "brief.md", "--run", "r-x"},
		{"spawn", "--level", "L1", "--role", "reviewer", "--brief", "brief.md", "--background"},
		{"spawn", "--level", "L1", "--role", "coder", "--brief", "brief.md"},
		{"spawn", "--level", "L1", "--role", "facts", "--brief", "brief.md"},
		{"spawn", "--level", "L1", "--role", "reviewer", "--brief", "brief.md", "--schema", "other.json"},
		{"spawn", "--level", "L1", "--role", "reviewer", "--brief", "missing.md"},
		{"spawn", "--level", "L0", "--role", "reviewer", "--brief", "brief.md"},
	} {
		if code, out, _ := b460Call(t, args...); code != 127 || out != "" {
			t.Fatalf("%v = %d %q, want the Node hand-over (127)", args, code, out)
		}
	}
	got := b460Handovers(t, f.home)
	if len(got) != 7 || strings.Count(strings.Join(got, ","), "spawn spawn-session") != 7 {
		t.Fatalf("hand-overs = %v", got)
	}
}
