package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B550: `code-forge author` and `code-forge s2` run their L3 session in Go
// for the anthropic and xai providers. Every expected line below is what Node
// printed and wrote for the same fixture (CODE_FORGE_NATIVE=0
// ./bin/code-forge author|s2 ..., the same fake claude and grok on PATH, a
// temp HOME and TMPDIR); masks: duration_ms, ts, and the workspace path.

const b550Claude = `#!/bin/sh
cat > /dev/null
case "$FAKE_MODE" in
author) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"draft":"# Plan\n\nbody line\n","questions":[{"id":"q1","question":"Which one?","options":["a","b"],"why":"it matters","blocking":true}]},"usage":{"input_tokens":1200,"output_tokens":340,"cache_read_input_tokens":5}}' ;;
s2) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"decision":"go","confidence":0.8,"reason":"fine é","overrule":false,"ask_human":false,"human_question":null},"usage":{"input_tokens":100,"output_tokens":20}}' ;;
s2long) printf '{"type":"result","is_error":false,"result":"done","structured_output":{"decision":"go","confidence":0.8,"reason":"%0300d","overrule":false,"ask_human":false,"human_question":null},"usage":{"input_tokens":100,"output_tokens":20}}' 0 ;;
invalid) printf '%s' '{"is_error":false,"result":"{\"x\":true}"}' ;;
fail) echo "boom" >&2; exit 5 ;;
esac
`

const b550Grok = `#!/bin/sh
cat > /dev/null
case "$FAKE_MODE" in
author) printf '%s' '{"text":"{\"draft\":\"# G\\n\",\"questions\":[]}","usage":{"prompt_tokens":70,"completion_tokens":30}}' ;;
s2) printf '%s' '{"text":"{\"decision\":\"stop\",\"confidence\":0.5,\"reason\":\"r\",\"overrule\":true,\"ask_human\":true,\"human_question\":\"why?\"}","usage":{"prompt_tokens":7,"completion_tokens":3}}' ;;
fail) echo "gboom" >&2; exit 4 ;;
esac
`

var b550Configs = map[string]string{
	"anthropic": "version: 1\nlevels:\n  L3:\n    provider: anthropic\n    model: claude-x\n    effort: high\n",
	"xai":       "version: 1\nlevels:\n  L3:\n    provider: xai\n    model: grok-x\n",
	"openai":    "version: 1\nlevels:\n  L3:\n    provider: openai\n    model: gpt-x\n",
}

// newB550Fixture: a temp HOME, a git repo named "repo" with the config for
// provider, a brief, a fresh facts sheet, a stale one, a draft, answers and
// s2 packets, fake claude/grok first on PATH, TMPDIR in the workspace.
func newB550Fixture(t *testing.T, provider string) (runFixture, string) {
	t.Helper()
	f := newRunFixture(t)
	bin := filepath.Join(f.workspace, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"claude": b550Claude, "grok": b550Grok} {
		b460Write(t, filepath.Join(bin, name), body)
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(f.workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	b460Git(t, repo, "init", "-q")
	b460Write(t, filepath.Join(repo, ".code-forge.yml"), b550Configs[provider])
	brief := "Build the thing.\n\nIt has two parts.\n"
	b460Write(t, filepath.Join(repo, "brief.md"), brief)
	sum := sha256.Sum256([]byte(brief))
	b460Write(t, filepath.Join(repo, "facts.md"), "# Facts\n\n- brief: `brief.md`\n- brief_sha256: `"+hex.EncodeToString(sum[:])+"`\n\n<!-- facts-json\n[]\n-->\n")
	b460Write(t, filepath.Join(repo, "stale.md"), "# Facts\n\n- brief: `brief.md`\n- brief_sha256: `"+strings.Repeat("0", 64)+"`\n\n<!-- facts-json\n[]\n-->\n")
	b460Write(t, filepath.Join(repo, "draft.md"), "# Old draft\n")
	b460Write(t, filepath.Join(repo, "answers.md"), "q1: a\n")
	b460Write(t, filepath.Join(repo, "packet.json"), `{"question":"Ship it?","options":["yes","no",3],"s1":{"lane":"L1"},"context":"some context"}`)
	b460Write(t, filepath.Join(repo, "noq.json"), `{"question":"  "}`)
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

// b550Tree lists the files under dir (relative, sorted by Walk).
func b550Tree(t *testing.T, dir string) []string {
	t.Helper()
	out := []string{}
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && path != dir {
			rel, _ := filepath.Rel(dir, path)
			if !strings.HasPrefix(rel, ".git") {
				out = append(out, rel)
			}
		}
		return nil
	})
	return out
}

type b550Case struct {
	name, provider, mode string
	args                 []string
	code                 int
	out, err             string
	rows                 []string
	files                map[string]string // repo file → content
}

func b550Check(t *testing.T, c b550Case) {
	t.Helper()
	f, repo := newB550Fixture(t, c.provider)
	t.Setenv("FAKE_MODE", c.mode)
	before := b550Tree(t, repo)
	code, out, errText := b460Call(t, c.args...)
	out = strings.ReplaceAll(out, repo, "<repo>")
	if code != c.code || out != c.out || errText != c.err {
		t.Fatalf("%s = %d\n%s\n%q", c.name, code, out, errText)
	}
	rows := b477Ledger(t, f.home)
	if strings.Join(rows, "\n") != strings.Join(c.rows, "\n") {
		t.Fatalf("%s: ledger rows = %v", c.name, rows)
	}
	after := b550Tree(t, repo)
	if len(after) != len(before)+len(c.files) {
		t.Fatalf("%s: repo files %v -> %v", c.name, before, after)
	}
	for file, want := range c.files {
		if want == "" {
			if info, err := os.Stat(filepath.Join(repo, file)); err != nil || !info.IsDir() {
				t.Fatalf("%s: %s is not a directory", c.name, file)
			}
			continue
		}
		data, err := os.ReadFile(filepath.Join(repo, file))
		if err != nil || string(data) != want {
			t.Fatalf("%s: %s = %q (%v)", c.name, file, data, err)
		}
	}
	if got := b460Handovers(t, f.home); len(got) != 0 {
		t.Fatalf("%s: handed over: %v", c.name, got)
	}
	// the packet directory and the lazy proc root are gone
	if left, _ := filepath.Glob(filepath.Join(f.workspace, "tmp", "code-forge", "proc-*")); len(left) != 0 {
		t.Fatalf("%s: proc root left behind: %v", c.name, left)
	}
}

const (
	b550A     = "level=L3 provider=anthropic model=claude-x effort=high fallback_step=0\n"
	b550X     = "level=L3 provider=xai model=grok-x effort=none fallback_step=0\n"
	b550Draft = "# Plan\n\nbody line\n"
	b550Qs    = `"questions":[{"id":"q1","question":"Which one?","options":["a","b"],"why":"it matters","blocking":true}]`
)

func TestB550AuthorRunsInGo(t *testing.T) {
	plan := []string{"author", "--job", "plan", "--brief", "brief.md", "--facts", "facts.md"}
	for _, c := range []b550Case{
		{name: "anthropic ok", provider: "anthropic", mode: "author", args: plan, code: 0,
			out:   `{"draft":"<repo>/plans/brief.plan.md",` + b550Qs + `,"cost":{"tokens_in":1205,"tokens_out":340,"tokens_source":"reported","usd_estimated":0.0463}}` + "\n",
			err:   b550A,
			rows:  []string{`{"event":"session","role":"author","level":"L3","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"run":null,"block":null,"status":"ok","tokens_in":1205,"tokens_out":340,"tokens_source":"reported","usd":0.0463,"cost_source":"estimated","duration_ms":N}`},
			files: map[string]string{"plans": "", "plans/brief.plan.md": b550Draft}},
		{name: "harden with --out and --run", provider: "anthropic", mode: "author",
			args: []string{"author", "--job", "harden", "--brief", "brief.md", "--draft", "draft.md", "--answers", "answers.md", "--out", "out/x.md", "--run", "r1"}, code: 0,
			out:   `{"draft":"<repo>/out/x.md",` + b550Qs + `,"cost":{"tokens_in":1205,"tokens_out":340,"tokens_source":"reported","usd_estimated":0.0463}}` + "\n",
			err:   b550A,
			rows:  []string{`{"event":"session","role":"author","level":"L3","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"run":"r1","block":null,"status":"ok","tokens_in":1205,"tokens_out":340,"tokens_source":"reported","usd":0.0463,"cost_source":"estimated","duration_ms":N}`},
			files: map[string]string{"out": "", "out/x.md": b550Draft}},
		{name: "xai ok", provider: "xai", mode: "author", args: plan, code: 0,
			out:   `{"draft":"<repo>/plans/brief.plan.md","questions":[],"cost":{"tokens_in":70,"tokens_out":30,"tokens_source":"reported","usd_estimated":0.002}}` + "\n",
			err:   b550X,
			rows:  []string{`{"event":"session","role":"author","level":"L3","provider":"xai","model":"grok-x","effort":null,"fallback_step":0,"run":null,"block":null,"status":"ok","tokens_in":70,"tokens_out":30,"tokens_source":"reported","usd":0.002,"cost_source":"estimated","duration_ms":N}`},
			files: map[string]string{"plans": "", "plans/brief.plan.md": "# G\n"}},
		{name: "anthropic provider fails", provider: "anthropic", mode: "fail", args: plan, code: 1,
			err:  b550A + "author: failed (exit 5)\n",
			rows: []string{`{"event":"session","role":"author","level":"L3","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"run":null,"block":null,"status":"failed","reason":"exit 5","tokens_in":334,"tokens_out":0,"tokens_source":"estimated","usd":0.01,"cost_source":"estimated","duration_ms":N}`}},
		{name: "xai provider fails", provider: "xai", mode: "fail", args: plan, code: 1,
			err:  b550X + "author: failed (exit 4)\n",
			rows: []string{`{"event":"session","role":"author","level":"L3","provider":"xai","model":"grok-x","effort":null,"fallback_step":0,"run":null,"block":null,"status":"failed","reason":"exit 4","tokens_in":334,"tokens_out":0,"tokens_source":"estimated","usd":0.0067,"cost_source":"estimated","duration_ms":N}`}},
		{name: "invalid answer", provider: "anthropic", mode: "invalid", args: plan, code: 1,
			err:  b550A + "author: invalid-output (answer does not match the schema)\n",
			rows: []string{`{"event":"session","role":"author","level":"L3","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"run":null,"block":null,"status":"invalid-output","reason":"answer does not match the schema","tokens_in":334,"tokens_out":11,"tokens_source":"estimated","usd":0.0103,"cost_source":"estimated","duration_ms":N}`}},
		{name: "usage", provider: "anthropic", args: []string{"author", "--job", "plan"}, code: 2,
			err: "usage: code-forge author --job plan|harden --brief <file> [--facts <file>] [--draft <file>] [--answers <file>] [--out <file>] [--run <id>] [--timeout <seconds>]\n"},
		{name: "no facts sheet", provider: "anthropic", args: []string{"author", "--job", "plan", "--brief", "brief.md"}, code: 2,
			err: "author: facts sheet required: run forge facts first\n"},
		{name: "stale facts sheet", provider: "anthropic", args: []string{"author", "--job", "plan", "--brief", "brief.md", "--facts", "stale.md"}, code: 2,
			err: "author: facts sheet is stale: brief changed after it was built\n"},
		{name: "bad job", provider: "anthropic", args: []string{"author", "--job", "bogus", "--brief", "brief.md"}, code: 2,
			err: "author: --job must be one of plan, harden\n"},
		{name: "bad run id", provider: "anthropic", args: []string{"author", "--job", "plan", "--brief", "brief.md", "--run", "a/b"}, code: 2,
			err: "author: a run id is one path segment of letters, digits, \".\", \"_\" or \"-\"\n"},
	} {
		t.Run(c.name, func(t *testing.T) { b550Check(t, c) })
	}
}

func TestB550S2RunsInGo(t *testing.T) {
	ask := []string{"s2", "--packet", "packet.json"}
	okRow := func(run, block string) string {
		return `{"event":"session","role":"s2","level":"L3","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"run":` + run + `,"block":` + block + `,"source":"s2","status":"ok","tokens_in":100,"tokens_out":20,"tokens_source":"reported","usd":0.0036,"cost_source":"estimated","duration_ms":N}`
	}
	answer := `{"decision":"go","confidence":0.8,"reason":"fine é","overrule":false,"ask_human":false,"human_question":null}` + "\n"
	for _, c := range []b550Case{
		{name: "anthropic ok", provider: "anthropic", mode: "s2", args: ask, code: 0, out: answer, err: b550A, rows: []string{okRow("null", "null")}},
		{name: "run and block", provider: "anthropic", mode: "s2", args: []string{"s2", "--packet", "packet.json", "--run", "r2", "--block", "B1", "--timeout", "30"}, code: 0,
			out: answer, err: b550A, rows: []string{okRow(`"r2"`, `"B1"`)}},
		{name: "xai ok", provider: "xai", mode: "s2", args: ask, code: 0,
			out:  `{"decision":"stop","confidence":0.5,"reason":"r","overrule":true,"ask_human":true,"human_question":"why?"}` + "\n",
			err:  b550X,
			rows: []string{`{"event":"session","role":"s2","level":"L3","provider":"xai","model":"grok-x","effort":null,"fallback_step":0,"run":null,"block":null,"source":"s2","status":"ok","tokens_in":7,"tokens_out":3,"tokens_source":"reported","usd":0.0002,"cost_source":"estimated","duration_ms":N}`}},
		{name: "provider fails", provider: "anthropic", mode: "fail", args: ask, code: 1,
			err:  b550A + "s2: failed (exit 5)\n",
			rows: []string{`{"event":"session","role":"s2","level":"L3","provider":"anthropic","model":"claude-x","effort":"high","fallback_step":0,"run":null,"block":null,"source":"s2","status":"failed","reason":"exit 5","tokens_in":119,"tokens_out":0,"tokens_source":"estimated","usd":0.0036,"cost_source":"estimated","duration_ms":N}`}},
		{name: "reason too long", provider: "anthropic", mode: "s2long", args: ask, code: 1,
			err: b550A + "s2: invalid-output (reason is longer than 280 characters)\n", rows: []string{okRow("null", "null")}},
		{name: "usage", provider: "anthropic", args: []string{"s2"}, code: 2,
			err: "usage: code-forge s2 --packet <file.json> [--run <id>] [--block <id>] [--timeout <seconds>]\n"},
		{name: "packet missing", provider: "anthropic", args: []string{"s2", "--packet", "nope.json"}, code: 2, err: "s2: --packet cannot be read\n"},
		{name: "no question", provider: "anthropic", args: []string{"s2", "--packet", "noq.json"}, code: 2, err: "s2: s2: the packet needs a non-empty question\n"},
	} {
		t.Run(c.name, func(t *testing.T) { b550Check(t, c) })
	}
}

// An openai (Codex) L3 stays with Node: the call hands over before Go writes
// anything (no ledger row, no draft, no run root, no packet).
func TestB550OpenAILevelHandsOverAndWritesNothing(t *testing.T) {
	for _, c := range []struct {
		args   []string
		reason string
	}{
		{[]string{"author", "--job", "plan", "--brief", "brief.md", "--facts", "facts.md", "--run", "r9"}, "author-provider"},
		{[]string{"s2", "--packet", "packet.json", "--run", "r9"}, "system-two-provider"},
	} {
		f, repo := newB550Fixture(t, "openai")
		t.Setenv("FAKE_MODE", "author")
		before := b550Tree(t, repo)
		code, out, _ := b460Call(t, c.args...)
		if code != 127 || out != "" {
			t.Fatalf("%v = %d %q; want the Node hand-over", c.args, code, out)
		}
		if got := b460Handovers(t, f.home); len(got) != 1 || got[0] != c.args[0]+" "+c.reason {
			t.Fatalf("%v: handovers %v", c.args, got)
		}
		if after := b550Tree(t, repo); strings.Join(after, ",") != strings.Join(before, ",") {
			t.Fatalf("%v: repo changed %v -> %v", c.args, before, after)
		}
		if rows := b477Ledger(t, f.home); len(rows) != 0 {
			t.Fatalf("%v: ledger rows %v", c.args, rows)
		}
		if left := b550Tree(t, filepath.Join(f.workspace, "tmp")); len(left) != 0 {
			t.Fatalf("%v: tmp written %v", c.args, left)
		}
	}
}
