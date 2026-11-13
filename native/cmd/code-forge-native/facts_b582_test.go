package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
)

// B582: a facts call whose levels.L0 ladder has a Codex (openai) step runs in
// Go (it was the facts-codex-step hand-over). Each testdata/b582/<case>.txt
// is Node's report for the same workspace (the B556 report plus what the
// fake codex saw: argv, cwd, stdin packet, snapshot listing, the strict
// schema file and its mode), recorded with B582_RECORD=1, which runs
// CODE_FORGE_NATIVE=0 ./bin/code-forge facts … (Node 24) in the workspace.
// Extra masks: the session dir sessions/facts-<ms>-<hex> and the rate-limit
// retry wait (jittered).

const b582FakeCodex = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > "$FAKE_LOG/codex.argv"
pwd -P > "$FAKE_LOG/codex.cwd"
cat > "$FAKE_LOG/codex.stdin"
find . | sort | while read -r f; do printf '%s %s\n' "$(ls -ld "$f" | cut -c1-10)" "$f"; done > "$FAKE_LOG/codex.snapshot"
out=""; schema=""; prev=""
for a in "$@"; do
  case "$prev" in -o) out=$a;; --output-schema) schema=$a;; esac
  prev=$a
done
if [ -n "$schema" ]; then { ls -l "$schema" | cut -c1-10; cat "$schema"; printf '\n'; } > "$FAKE_LOG/codex.schema"; fi
printf '%s\n' "$FAKE_CODEX" >> "$FAKE_LOG/codex.calls"
case "$FAKE_CODEX" in
limit) printf '{"type":"error","message":"unexpected status 429 Too Many Requests: You have hit your usage limit"}\n'; exit 1;;
auth) printf '{"type":"error","message":"unexpected status 401 Unauthorized"}\n'; exit 1;;
busy) printf '{"type":"turn.failed","error":{"message":"unexpected status 503 Service Unavailable"}}\n'; echo "Error: 503 Service Unavailable" >&2; exit 1;;
fail) echo "boom" >&2; exit 5;;
hang) exec sleep 30;;
noout) printf '{"type":"turn.completed","usage":{"input_tokens":300,"cached_input_tokens":0,"output_tokens":40}}\n'; exit 0;;
esac
printf '%s' "$FAKE_ANSWER" > "$out"
printf '{"type":"thread.started","thread_id":"t1"}\n{"type":"turn.completed","usage":{"input_tokens":300,"cached_input_tokens":0,"output_tokens":40}}\n'
`

const b582FakeClaude = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > "$FAKE_LOG/claude.argv"
pwd -P > "$FAKE_LOG/claude.cwd"
cat > /dev/null
case "$FAKE_CLAUDE" in
cap) echo "API Error: 503 overloaded" >&2; exit 1;;
esac
printf '{"type":"result","is_error":false,"result":"","structured_output":%s,"usage":{"input_tokens":100,"output_tokens":20}}' "$FAKE_ANSWER"
`

var (
	b582SessionDir = regexp.MustCompile(`sessions/facts-[0-9]+-[0-9a-f]{8}`)
	b582RetryWait  = regexp.MustCompile(`retry in [0-9]+\.[0-9] s`)
)

// b582Mask is the extra mask: the session dir and the jittered retry wait.
func b582Mask(text string) string {
	return b582RetryWait.ReplaceAllString(b582SessionDir.ReplaceAllString(text, "sessions/facts-T-H"), "retry in W s")
}

type b582Case struct {
	name, config, codex, claude, answer string
	args                                []string
}

const (
	b582Head        = "version: 1\nproject:\n  name: unit\n  slug: unit\n"
	b582CodexFirst  = b582Head + "levels:\n  L0:\n    provider: openai\n    model: gpt-x\n    effort: medium\n    fallback:\n      - provider: anthropic\n        model: m-x\n"
	b582CodexOnly   = b582Head + "levels:\n  L0:\n    provider: openai\n    model: gpt-x\n"
	b582ClaudeFirst = b582Head + "levels:\n  L0:\n    provider: anthropic\n    model: m-x\n    fallback:\n      - provider: openai\n        model: gpt-y\n        effort: low\n"
	b582OpenBook    = b582CodexOnly + "review:\n  allow_open_book_codex: true\n"
	b582TopLevel    = b582Head + "provider: openai\nlevels:\n  L0:\n    model: gpt-z\n    effort: high\n"
)

var b582Cases = []b582Case{
	{name: "codex-first", config: b582CodexFirst, codex: "ok"},
	{name: "codex-only", config: b582CodexOnly, codex: "ok"},
	{name: "open-book", config: b582OpenBook, codex: "ok"},
	{name: "top-level-provider", config: b582TopLevel, codex: "ok"},
	{name: "claude-cap-codex", config: b582ClaudeFirst, codex: "ok", claude: "cap"},
	{name: "codex-limit", config: b582CodexFirst, codex: "limit"},
	{name: "codex-401", config: b582CodexFirst, codex: "auth"},
	{name: "codex-401-last", config: b582CodexOnly, codex: "auth"},
	{name: "codex-503", config: b582CodexFirst, codex: "busy"},
	{name: "codex-exit", config: b582CodexFirst, codex: "fail"},
	{name: "codex-timeout", config: b582CodexFirst, codex: "hang", args: []string{"--timeout", "1"}},
	{name: "codex-schema", config: b582CodexOnly, codex: "ok", answer: `{"nope":1}`},
	{name: "codex-strict-key", config: b582CodexOnly, codex: "ok", answer: `{"facts":[{"fact_id":"F1","claim":"src/app.mjs","kind":"path","command":"ls src/app.mjs","output_excerpt":"src/app.mjs","tag":"VERIFIED"}]}`},
	{name: "codex-noout", config: b582CodexOnly, codex: "noout"},
	{name: "codex-run", config: b582CodexOnly, codex: "ok", args: []string{"--run", "r9"}},
}

// b582Workspace is the B556 workspace with the fake codex and claude and the case's config.
func b582Workspace(t *testing.T, c b582Case) string {
	t.Helper()
	base := fcWorkspace(t, "openai", fcBrief)
	for name, text := range map[string]string{"bin/codex": b582FakeCodex, "bin/claude": b582FakeClaude} {
		if err := os.WriteFile(filepath.Join(base, name), []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "repo", ".code-forge.yml"), []byte(c.config), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_CODEX", c.codex)
	t.Setenv("FAKE_CLAUDE", c.claude)
	if c.answer != "" {
		t.Setenv("FAKE_ANSWER", c.answer)
	}
	return base
}

// b582Node runs Node's facts verb (record mode only).
func b582Node(t *testing.T, launcher, base string, args []string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(launcher, args...)
	cmd.Dir = filepath.Join(base, "repo")
	cmd.Env = append(os.Environ(), "CODE_FORGE_NATIVE=0")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code := 0
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatal(err)
		}
		code = exitErr.ExitCode()
	}
	return code, out.String(), errOut.String()
}

func TestB582FactsCodexStepMatchesNode(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	record := os.Getenv("B582_RECORD") == "1"
	for _, c := range b582Cases {
		t.Run(c.name, func(t *testing.T) {
			golden, err := filepath.Abs(filepath.Join("testdata", "b582", c.name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			launcher, err := filepath.Abs(filepath.Join("..", "..", "..", "bin", "code-forge"))
			if err != nil {
				t.Fatal(err)
			}
			base := b582Workspace(t, c)
			args := append([]string{"facts", "--brief", "brief.md"}, c.args...)
			if record {
				code, stdout, stderr := b582Node(t, launcher, base, args)
				report := b582Mask(fcReport(base, code, stdout, stderr))
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(report), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			code, stdout, stderr := runCaptured(t, args...)
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			got := b582Mask(fcReport(base, code, stdout, stderr))
			if got != b582Mask(fcMask(string(want))) {
				t.Fatalf("report differs from Node's\n--- got\n%s\n--- want (Node)\n%s", got, want)
			}
			// served in Go: no hand-over row
			data, _ := os.ReadFile(filepath.Join(base, "home", ".code-forge", "native", "handovers.jsonl"))
			for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
				var row handoverRow
				if json.Unmarshal(line, &row) == nil && row.Verb == "facts" {
					t.Fatalf("handed over: %s", line)
				}
			}
		})
	}
}
