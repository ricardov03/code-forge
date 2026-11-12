package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// B551: `code-forge spawn` runs an openai/Codex session in Go when the config
// opts in to open-book Codex (review.allow_open_book_codex). The expected
// lines are what Node printed for the same fixture (CODE_FORGE_NATIVE=0
// ./bin/code-forge spawn ..., this fake codex the only model CLI on PATH),
// duration_ms masked.

const b551Codex = `#!/bin/sh
out=""; schema=""; prev=""
for a in "$@"; do
  [ "$prev" = "-o" ] && out="$a"
  [ "$prev" = "--output-schema" ] && schema="$a"
  prev="$a"
done
stdin=$(cat)
{ printf 'argv:'; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'
  printf 'stdin:%s\n' "$stdin"; printf 'codex_home:%s\n' "${CODEX_HOME:-unset}"
  [ -n "$schema" ] && { printf 'schema:'; cat "$schema"; printf '\n'; ls -l "$schema" | cut -c1-10; }; } >> "$FAKE_LOG"
case "$FAKE_MODE" in
ok)
  printf '%s' '{"passed":true,"summary":"fine","reviewed_hunks":["h1"],"findings":[{"id":"f1","file":"a.go","line_start":1,"line_end":2,"severity":"nit","category":"style","claim":"c","evidence":"e","fix":"x","agreement":null}],"resolved":null,"needs_file":null}' > "$out"
  echo '{"type":"thread.started","thread_id":"t1"}'
  echo '{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":5,"output_tokens":20}}' ;;
coerce)
  printf '%s' '{"passed":null,"summary":"bad","reviewed_hunks":[],"findings":[{"id":"f1","file":"a.go","line_start":1,"line_end":2,"severity":"critical","category":"bug","claim":"c","evidence":"e","fix":"x","agreement":null}],"resolved":null,"needs_file":null}' > "$out"
  echo '{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}' ;;
miss)
  printf '%s' '{"passed":true,"summary":"fine","reviewed_hunks":[],"findings":[]}' > "$out"
  echo '{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}' ;;
noout)
  echo '{"type":"thread.started"}' ;;
fail)
  echo '{"type":"thread.started"}'; echo "codex broke" >&2; exit 4 ;;
cap)
  echo '{"type":"turn.failed","error":{"message":"unexpected status 503 Service Unavailable: busy"}}'; exit 1 ;;
login)
  echo '{"type":"error","message":"unexpected status 401 Unauthorized"}'; exit 1 ;;
hang)
  echo '{"type":"thread.started"}'; echo "thinking" >&2; exec sleep 30 ;;
esac
`

const b551Config = "version: 1\nreview:\n  allow_open_book_codex: true\nlevels:\n  L3:\n    provider: openai\n    model: gpt-x\n    effort: high\n    fallback:\n      - provider: xai\n        model: grok-x\n  L2:\n    provider: openai\n    model: gpt-y\n"

// newB551Fixture: the B477 fixture with the fake codex as the only model CLI
// on PATH, a temp CODEX_HOME and the open-book config.
func newB551Fixture(t *testing.T) (runFixture, string, string) {
	t.Helper()
	f, repo := newB477Fixture(t)
	bin := filepath.Join(f.workspace, "bin")
	for _, n := range []string{"claude", "grok"} {
		if err := os.Remove(filepath.Join(bin, n)); err != nil {
			t.Fatal(err)
		}
	}
	b460Write(t, filepath.Join(bin, "codex"), b551Codex)
	if err := os.Chmod(filepath.Join(bin, "codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	b460Write(t, filepath.Join(repo, ".code-forge.yml"), b551Config)
	codexHome := filepath.Join(f.workspace, "codexhome")
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	log := filepath.Join(f.workspace, "fake.log")
	t.Setenv("FAKE_LOG", log)
	return f, repo, log
}

var b551Session = regexp.MustCompile(`[^ \[\]]*/sessions/reviewer-[0-9]+-[0-9a-f]+`)

func TestB551SpawnCodexReviewerRunsInGo(t *testing.T) {
	f, _, log := newB551Fixture(t)
	t.Setenv("FAKE_MODE", "ok")
	code, out, errText := b460Call(t, "spawn", "--level", "L3", "--role", "reviewer", "--brief", "brief.md", "--schema", "schema.json")
	want := `{"status":"ok","answer":{"passed":true,"summary":"fine","reviewed_hunks":["h1"],"findings":[{"id":"f1","file":"a.go","line_start":1,"line_end":2,"severity":"nit","category":"style","claim":"c","evidence":"e","fix":"x"}]},"usage":{"tokens_in":100,"tokens_out":20,"tokens_source":"reported"},"duration_ms":N,"exit_code":0,"stderr":"","provider":"openai","model":"gpt-x","effort":"high","fallback_step":0,"row":{"event":"session","role":"reviewer","level":"L3","provider":"openai","model":"gpt-x","effort":"high","fallback_step":0,"run":null,"block":null,"status":"ok","tokens_in":100,"tokens_out":20,"tokens_source":"reported","usd":0.003,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"openai","model":"gpt-x","status":"ok","reason":null}]}` + "\n"
	if code != 0 || b477Norm(out) != want || errText != "level=L3 provider=openai model=gpt-x effort=high fallback_step=0\n" {
		t.Fatalf("spawn = %d\n%s\n%q", code, out, errText)
	}
	rows := b477Ledger(t, f.home)
	if len(rows) != 1 || rows[0] != `{"event":"session","role":"reviewer","level":"L3","provider":"openai","model":"gpt-x","effort":"high","fallback_step":0,"run":null,"block":null,"status":"ok","tokens_in":100,"tokens_out":20,"tokens_source":"reported","usd":0.003,"cost_source":"estimated","duration_ms":N}` {
		t.Fatalf("ledger rows = %v", rows)
	}
	if got := b460Handovers(t, f.home); len(got) != 0 {
		t.Fatalf("handed over: %v", got)
	}
	// the argv, stdin, CODEX_HOME and the strict schema file (0600) Node gave codex
	data, _ := os.ReadFile(log)
	got := b551Session.ReplaceAllString(string(data), "<session>")
	wantLog := "argv: [exec] [-m] [gpt-x] [-c] [model_reasoning_effort=high] [-s] [read-only] [--ephemeral] [--ignore-rules] [--ignore-user-config] [-C] [<session>/cwd] [--skip-git-repo-check] [--output-schema] [<session>/schema.json] [-o] [<session>/out.json] [--json] [-]\n" +
		"stdin:review this please, the packet line here\n" +
		"codex_home:" + filepath.Join(f.workspace, "codexhome") + "\n" +
		`schema:{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"https://code-forge.local/schema/review/finding.schema.json","title":"code-forge review answer (plan §4.2 finding schema)","type":"object","additionalProperties":false,"required":["passed","summary","reviewed_hunks","findings","resolved","needs_file"],"properties":{"passed":{"type":"boolean"},"summary":{"type":"string","maxLength":300},"reviewed_hunks":{"type":"array","items":{"type":"string"}},"findings":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","file","line_start","line_end","severity","category","claim","evidence","fix","agreement"],"properties":{"id":{"type":"string","minLength":1,"maxLength":64},"file":{"type":"string"},"line_start":{"type":"integer","minimum":0},"line_end":{"type":"integer","minimum":0},"severity":{"type":"string","enum":["critical","warning","nit"]},"category":{"type":"string","maxLength":64},"claim":{"type":"string","maxLength":300},"evidence":{"type":"string","maxLength":300},"fix":{"type":"string","maxLength":300},"agreement":{"anyOf":[{"type":"string","enum":["both","A-only","B-only"]},{"type":"null"}]}}}},"resolved":{"anyOf":[{"type":"array","items":{"type":"object","additionalProperties":false,"required":["id","resolved","why","current_lines"],"properties":{"id":{"type":"string"},"resolved":{"type":"boolean"},"why":{"type":"string","maxLength":200},"current_lines":{"anyOf":[{"type":"string","maxLength":300},{"type":"null"}]}}}},{"type":"null"}]},"needs_file":{"anyOf":[{"type":"array","items":{"type":"string"}},{"type":"null"}]}}}` + "\n" +
		"-rw-------\n"
	if got != wantLog {
		t.Fatalf("codex saw\n%s\nwant\n%s", got, wantLog)
	}
	// the session dir (out.json, schema.json, cwd) is gone with the session
	if left, _ := filepath.Glob(filepath.Join(f.workspace, "tmp", "code-forge", "proc-*")); len(left) != 0 {
		t.Fatalf("proc root left behind: %v", left)
	}
}

func TestB551SpawnCodexOutcomesMatchNode(t *testing.T) {
	cases := []struct {
		name, mode string
		args       []string
		code       int
		out, err   string
	}{
		{"null passed is coerced", "coerce", []string{"--level", "L3", "--role", "reviewer", "--schema", "schema.json"}, 0,
			`{"status":"ok","answer":{"passed":false,"summary":"bad","reviewed_hunks":[],"findings":[{"id":"f1","file":"a.go","line_start":1,"line_end":2,"severity":"critical","category":"bug","claim":"c","evidence":"e","fix":"x"}]},"usage":{"tokens_in":10,"tokens_out":2,"tokens_source":"reported"},"duration_ms":N,"exit_code":0,"stderr":"","passed_coerced":true,"provider":"openai","model":"gpt-x","effort":"high","fallback_step":0,"row":{"event":"session","role":"reviewer","level":"L3","provider":"openai","model":"gpt-x","effort":"high","fallback_step":0,"run":null,"block":null,"status":"ok","tokens_in":10,"tokens_out":2,"tokens_source":"reported","usd":0.0003,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"openai","model":"gpt-x","status":"ok","reason":null}]}` + "\n",
			"level=L3 provider=openai model=gpt-x effort=high fallback_step=0\n"},
		{"schema miss", "miss", []string{"--level", "L3", "--role", "reviewer", "--schema", "schema.json"}, 1,
			`{"status":"invalid-output","reason":"answer does not match the schema","answer":null,"usage":{"tokens_in":10,"tokens_out":2,"tokens_source":"reported"},"duration_ms":N,"exit_code":0,"stderr":"","provider":"openai","model":"gpt-x","effort":"high","fallback_step":0,"row":{"event":"session","role":"reviewer","level":"L3","provider":"openai","model":"gpt-x","effort":"high","fallback_step":0,"run":null,"block":null,"status":"invalid-output","reason":"answer does not match the schema","tokens_in":10,"tokens_out":2,"tokens_source":"reported","usd":0.0003,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"openai","model":"gpt-x","status":"invalid-output","reason":"answer does not match the schema"}]}` + "\n",
			"level=L3 provider=openai model=gpt-x effort=high fallback_step=0\n"},
		{"no answer file, estimated usage", "noout", []string{"--level", "L3", "--role", "reviewer", "--schema", "schema.json"}, 1,
			`{"status":"invalid-output","reason":"answer does not match the schema","answer":null,"usage":{"tokens_in":11,"tokens_out":7,"tokens_source":"estimated"},"duration_ms":N,"exit_code":0,"stderr":"","provider":"openai","model":"gpt-x","effort":"high","fallback_step":0,"row":{"event":"session","role":"reviewer","level":"L3","provider":"openai","model":"gpt-x","effort":"high","fallback_step":0,"run":null,"block":null,"status":"invalid-output","reason":"answer does not match the schema","tokens_in":11,"tokens_out":7,"tokens_source":"estimated","usd":0.0005,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"openai","model":"gpt-x","status":"invalid-output","reason":"answer does not match the schema"}]}` + "\n",
			"level=L3 provider=openai model=gpt-x effort=high fallback_step=0\n"},
		{"non-zero exit", "fail", []string{"--level", "L3", "--role", "reviewer", "--schema", "schema.json"}, 1,
			`{"status":"failed","reason":"exit 4","answer":null,"usage":{"tokens_in":11,"tokens_out":7,"tokens_source":"estimated"},"duration_ms":N,"exit_code":4,"stderr":"codex broke\n","provider":"openai","model":"gpt-x","effort":"high","fallback_step":0,"row":{"event":"session","role":"reviewer","level":"L3","provider":"openai","model":"gpt-x","effort":"high","fallback_step":0,"run":null,"block":null,"status":"failed","reason":"exit 4","tokens_in":11,"tokens_out":7,"tokens_source":"estimated","usd":0.0005,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"openai","model":"gpt-x","status":"failed","reason":"exit 4"}]}` + "\n",
			"level=L3 provider=openai model=gpt-x effort=high fallback_step=0\n"},
		{"503 at capacity falls to the xai step", "cap", []string{"--level", "L3", "--role", "reviewer", "--schema", "schema.json"}, 3,
			`{"status":"unavailable","reason":"cli-missing","answer":null,"usage":{"tokens_in":11,"tokens_out":0,"tokens_source":"estimated"},"duration_ms":N,"exit_code":null,"stderr":"","provider":"xai","model":"grok-x","effort":null,"fallback_step":1,"row":{"event":"session","role":"reviewer","level":"L3","provider":"xai","model":"grok-x","effort":null,"fallback_step":1,"run":null,"block":null,"status":"unavailable","reason":"cli-missing","tokens_in":11,"tokens_out":0,"tokens_source":"estimated","usd":0.0002,"cost_source":"estimated","duration_ms":N,"l3_fallback":true},"attempts":[{"fallback_step":0,"provider":"openai","model":"gpt-x","status":"unavailable","reason":"at-capacity"},{"fallback_step":1,"provider":"xai","model":"grok-x","status":"unavailable","reason":"cli-missing"}]}` + "\n",
			"level=L3 provider=openai model=gpt-x effort=high fallback_step=0\nlevel=L3 provider=xai model=grok-x effort=none fallback_step=1\n"},
		{"401 login expired", "login", []string{"--level", "L2", "--role", "judge"}, 3,
			`{"status":"unavailable","reason":"login-expired","answer":null,"usage":{"tokens_in":11,"tokens_out":16,"tokens_source":"estimated"},"duration_ms":N,"exit_code":1,"stderr":"","provider":"openai","model":"gpt-y","effort":null,"fallback_step":0,"row":{"event":"session","role":"judge","level":"L2","provider":"openai","model":"gpt-y","effort":null,"fallback_step":0,"run":null,"block":null,"status":"unavailable","reason":"login-expired","tokens_in":11,"tokens_out":16,"tokens_source":"estimated","usd":0.0003,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"openai","model":"gpt-y","status":"unavailable","reason":"login-expired"}]}` + "\n",
			"level=L2 provider=openai model=gpt-y effort=none fallback_step=0\n"},
		{"timeout", "hang", []string{"--level", "L3", "--role", "reviewer", "--schema", "schema.json", "--timeout", "1"}, 1,
			`{"status":"timeout","reason":"killed after 1000 ms","answer":null,"usage":{"tokens_in":11,"tokens_out":7,"tokens_source":"estimated"},"duration_ms":N,"exit_code":null,"stderr":"thinking\n","stderr_tail":"thinking\n","stdout_bytes":26,"stdout_events":["thread.started"],"provider":"openai","model":"gpt-x","effort":"high","fallback_step":0,"row":{"event":"session","role":"reviewer","level":"L3","provider":"openai","model":"gpt-x","effort":"high","fallback_step":0,"run":null,"block":null,"status":"timeout","reason":"killed after 1000 ms","tokens_in":11,"tokens_out":7,"tokens_source":"estimated","usd":0.0005,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"openai","model":"gpt-x","status":"timeout","reason":"killed after 1000 ms"}]}` + "\n",
			"level=L3 provider=openai model=gpt-x effort=high fallback_step=0\n"},
		{"no schema: the parsed answer", "ok", []string{"--level", "L2", "--role", "s2"}, 0,
			`{"status":"ok","answer":{"passed":true,"summary":"fine","reviewed_hunks":["h1"],"findings":[{"id":"f1","file":"a.go","line_start":1,"line_end":2,"severity":"nit","category":"style","claim":"c","evidence":"e","fix":"x","agreement":null}],"resolved":null,"needs_file":null},"usage":{"tokens_in":100,"tokens_out":20,"tokens_source":"reported"},"duration_ms":N,"exit_code":0,"stderr":"","provider":"openai","model":"gpt-y","effort":null,"fallback_step":0,"row":{"event":"session","role":"s2","level":"L2","provider":"openai","model":"gpt-y","effort":null,"fallback_step":0,"run":null,"block":null,"source":"s2","status":"ok","tokens_in":100,"tokens_out":20,"tokens_source":"reported","usd":0.0014,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"openai","model":"gpt-y","status":"ok","reason":null}]}` + "\n",
			"level=L2 provider=openai model=gpt-y effort=none fallback_step=0\n"},
		{"print-argv", "ok", []string{"--level", "L2", "--role", "reviewer", "--print-argv"}, 0,
			`{"argv":["codex","exec","-m","gpt-y","-s","read-only","--ephemeral","--ignore-rules","--ignore-user-config","-C","<session>/cwd","--skip-git-repo-check","-o","<session>/out.json","--json","-"],"cwd":"<session>/cwd","provider":"openai","model":"gpt-y","effort":null,"fallback_step":0}` + "\n", ""},
		{"delegate stays closed-book", "ok", []string{"--level", "L2", "--role", "delegate"}, 2, "",
			"spawn: codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, repo, _ := newB551Fixture(t)
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
