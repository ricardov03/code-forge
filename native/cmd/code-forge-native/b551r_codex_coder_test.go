package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// B551r: a Codex (openai) coder step runs in Go like Node: `codex exec -s
// workspace-write ... -o <session>/out.json <prompt>` with CODEX_HOME set to
// a fresh per-session home under <run root>/codex-homes/, the sentinel read
// from the -o answer, and --background writing outPath into pid.json.
// testdata/b551r/<case>.txt is what Node printed and wrote for the same
// setup (CODE_FORGE_NATIVE=0 ./bin/code-forge spawn ..., this fake codex
// plus the B549 fake claude and grok on PATH, temp HOME, TMPDIR and
// CODEX_HOME), as the B549 report plus what codex saw. Extra mask: the
// session home's random coder-<12 hex>.

const b551rCodex = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > "$FAKE_LOG/codex.argv"
pwd -P > "$FAKE_LOG/codex.cwd"
out=""; prev=""; last=""
for a in "$@"; do [ "$prev" = "-o" ] && out="$a"; prev="$a"; last="$a"; done
if [ "$last" = "-" ]; then cat > "$FAKE_LOG/codex.stdin"; elif [ -f "$last" ]; then cp "$last" "$FAKE_LOG/codex.prompt"; fi
{ printf 'CODEX_HOME=%s\n' "$CODEX_HOME"; (cd "$CODEX_HOME" && find . | LC_ALL=C sort)
  for f in "$CODEX_HOME"/rules/*; do [ -f "$f" ] && { printf 'rules:'; cksum < "$f"; }; done
  [ -e "$CODEX_HOME/auth.json" ] && { printf 'auth:'; cat "$CODEX_HOME/auth.json"; printf '\n'; }; } > "$FAKE_LOG/codex.home"
case "$FAKE_MODE" in
fail) echo '{"type":"thread.started","thread_id":"t1"}'; echo "boom" >&2; exit 5;;
esac
[ -n "$out" ] && printf 'built it\nTESTS go test ./... exit=0\n===BLOCK B1 COMPLETE===' > "$out"
echo '{"type":"thread.started","thread_id":"t1"}'
echo '{"type":"turn.completed","usage":{"input_tokens":100,"cached_input_tokens":0,"output_tokens":20}}'
: > "$FAKE_LOG/codex.done"
`

const b551rConfig = `version: 1
levels:
  L0:
    provider: anthropic
    model: claude-x
    fallback:
      - provider: openai
        model: gpt-x
  L2:
    provider: xai
    model: grok-x
  L3:
    provider: openai
    model: gpt-x
    effort: high
`

var b551rHome = regexp.MustCompile(`coder-[0-9a-f]{12}`)

// b551rFixture is the B549 layout plus the fake codex, a temp CODEX_HOME
// (<ws>/codexhome with auth.json) and this config.
func b551rFixture(t *testing.T, mode, record string) string {
	t.Helper()
	w := b549Fixture(t, mode, record)
	b460Write(t, filepath.Join(w, "bin", "codex"), b551rCodex)
	if err := os.Chmod(filepath.Join(w, "bin", "codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	b460Write(t, filepath.Join(w, "repo", ".code-forge.yml"), b551rConfig)
	b460Write(t, filepath.Join(w, "codexhome", "auth.json"), `{"token":"t"}`)
	t.Setenv("CODEX_HOME", filepath.Join(w, "codexhome"))
	return w
}

func b551rReport(t *testing.T, w string, code int, out, errText string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(b549Report(t, w, code, out, errText))
	for _, name := range []string{"codex.argv", "codex.cwd", "codex.prompt", "codex.stdin", "codex.home"} {
		if data, err := os.ReadFile(filepath.Join(w, "log", name)); err == nil {
			b.WriteString("== " + name + "\n" + b549Mask(w, string(data)))
		}
	}
	return b551rHome.ReplaceAllString(b.String(), "coder-H")
}

var b551rCases = []struct {
	name, mode, record string
	args               []string
}{
	{"coder-fg", "ok", "", []string{"--level", "L3", "--role", "coder", "--brief", "brief.md", "--block", "B1"}},
	{"coder-run", "ok", b549Record, []string{"--level", "L3", "--role", "coder", "--brief", "brief.md", "--run", "r1", "--block", "B1"}},
	{"coder-fail", "fail", "", []string{"--level", "L3", "--role", "coder", "--brief", "brief.md"}},
	{"coder-argv", "ok", "", []string{"--level", "L3", "--role", "coder", "--brief", "brief.md", "--block", "B1", "--print-argv"}},
	{"coder-fallback", "ok", "", []string{"--level", "L0", "--role", "coder", "--brief", "brief.md", "--block", "B1"}},
	{"bg-coder", "ok", "", []string{"--level", "L3", "--role", "coder", "--brief", "brief.md", "--block", "B1", "--background"}},
	{"bg-coder-run", "ok", "", []string{"--level", "L3", "--role", "coder", "--brief", "brief.md", "--background", "--run", "r1"}},
	{"facts-codex", "ok", "", []string{"--level", "L3", "--role", "facts", "--brief", "brief.md"}},
}

func TestB551rSpawnCodexCoderMatchesNode(t *testing.T) {
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	// B551R_NODE=<repo>/bin/code-forge B551R_NODE_BIN=<dir of node> rewrites
	// the goldens from Node instead of checking Go.
	node := os.Getenv("B551R_NODE")
	nodeBin := os.Getenv("B551R_NODE_BIN")
	for _, tc := range b551rCases {
		t.Run(tc.name, func(t *testing.T) {
			w := b551rFixture(t, tc.mode, tc.record)
			if tc.name == "coder-fallback" {
				// claude is missing: the L0 ladder falls to its openai step
				if err := os.Remove(filepath.Join(w, "bin", "claude")); err != nil {
					t.Fatal(err)
				}
			}
			bg := strings.Contains(strings.Join(tc.args, " "), "--background")
			golden := filepath.Join(here, "testdata", "b551r", tc.name+".txt")
			if node != "" {
				if err := os.Symlink(filepath.Join(nodeBin, "node"), filepath.Join(w, "bin", "node")); err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command(node, append([]string{"spawn"}, tc.args...)...)
				cmd.Env = append(os.Environ(), "CODE_FORGE_NATIVE=0")
				var so, se strings.Builder
				cmd.Stdout, cmd.Stderr = &so, &se
				code := 0
				if err := cmd.Run(); err != nil {
					ee, ok := err.(*exec.ExitError)
					if !ok {
						t.Fatal(err)
					}
					code = ee.ExitCode()
				}
				if bg && code == 0 {
					b551rWaitDone(t, w)
				}
				if err := os.WriteFile(golden, []byte(b551rReport(t, w, code, so.String(), se.String())), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			code, out, errText := b460Call(t, append([]string{"spawn"}, tc.args...)...)
			if bg && code == 0 {
				b549Reap(t, out)
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got := b551rReport(t, w, code, out, errText); got != string(want) {
				t.Fatalf("%v differs from Node\n--- go\n%s\n--- node\n%s", tc.args, got, want)
			}
			if rows := b460Handovers(t, filepath.Join(w, "home")); len(rows) != 0 {
				t.Fatalf("handed over: %v", rows)
			}
		})
	}
}

// b551rWaitDone (golden mode only) waits for Node's detached codex child to
// finish: the fake touches codex.done last, then the session log settles.
func b551rWaitDone(t *testing.T, w string) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", `i=0; while [ ! -e "$1/log/codex.done" ] && [ $i -lt 200 ]; do sleep 0.05; i=$((i+1)); done; sleep 0.5`, "sh", w)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}
