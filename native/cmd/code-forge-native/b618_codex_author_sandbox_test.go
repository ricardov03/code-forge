package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// B618: the last Codex (openai) hand-overs of author, s2 and spawn run in
// Go. author/s2 on an openai level print the session refusal Node prints
// (closed-book without review.allow_open_book_codex, a provider with no CLI
// mapping) or run the read-only Codex session; a Codex coder under
// coder.sandbox builds Node's profile argv (workspace, toolchain --add-dir,
// full -s danger-full-access), refuses a bad add_dirs entry with
// sandbox-dir-refused and writes the sandbox field on its session row.
// testdata/b618/<case>.txt is what Node printed and wrote for the same
// setup (CODE_FORGE_NATIVE=0 ./bin/code-forge ..., these fake CLIs on PATH,
// temp HOME, TMPDIR and CODEX_HOME): the B549 report plus what codex saw and
// the repo tree. Record with B618_NODE=<repo>/bin/code-forge
// B618_NODE_BIN=<dir of node>.

const b618Codex = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > "$FAKE_LOG/codex.argv"
pwd -P > "$FAKE_LOG/codex.cwd"
out=""; prev=""; last=""; schema=""
for a in "$@"; do [ "$prev" = "-o" ] && out="$a"; [ "$prev" = "--output-schema" ] && schema="$a"; prev="$a"; last="$a"; done
[ "$last" = "-" ] && cat > "$FAKE_LOG/codex.stdin"
[ -n "$schema" ] && cp "$schema" "$FAKE_LOG/codex.schema"
printf 'CODEX_HOME=%s\n' "$CODEX_HOME" > "$FAKE_LOG/codex.home"
case "$FAKE_MODE" in
author) printf '%s' '{"draft":"# C\n\nfrom codex\n","questions":[]}' > "$out";;
s2) printf '%s' '{"decision":"go","confidence":0.7,"reason":"codex ok","overrule":false,"ask_human":false,"human_question":null}' > "$out";;
fail) echo '{"type":"thread.started","thread_id":"t1"}'; echo "boom" >&2; exit 5;;
esac
echo '{"type":"thread.started","thread_id":"t1"}'
echo '{"type":"turn.completed","usage":{"input_tokens":50,"cached_input_tokens":0,"output_tokens":9}}'
`

const b618Brief = "Build the thing.\n\nIt has two parts.\n"

// b618Configs: the author/s2 configs. @WS@ is the workspace.
var b618Configs = map[string]string{
	"closed":   "version: 1\nlevels:\n  L3:\n    provider: openai\n    model: gpt-x\n",
	"open":     "version: 1\nreview:\n  allow_open_book_codex: true\nlevels:\n  L3:\n    provider: openai\n    model: gpt-x\n    effort: high\n",
	"unmapped": "version: 1\nreview:\n  allow_open_book_codex: true\nlevels:\n  L3:\n    provider: OpenAI\n    model: gpt-x\n",
	"fallback": "version: 1\nlevels:\n  L3:\n    provider: anthropic\n    model: claude-x\n    fallback:\n      - provider: Foo\n        model: foo-x\n",
	"xhigh":    "version: 1\nreview:\n  allow_open_book_codex: true\nlevels:\n  L3:\n    provider: openai\n    model: gpt-x\n    effort: xhigh\n",
}

// b618AuthorFixture is the B549 layout with the fake codex, the author and
// s2 inputs and config cfg in the repo.
func b618AuthorFixture(t *testing.T, mode, cfg string) string {
	t.Helper()
	w := b549Fixture(t, mode, "")
	b460Write(t, filepath.Join(w, "bin", "codex"), b618Codex)
	if err := os.Chmod(filepath.Join(w, "bin", "codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(w, "repo")
	b460Write(t, filepath.Join(repo, ".code-forge.yml"), b618Configs[cfg])
	b460Write(t, filepath.Join(repo, "brief.md"), b618Brief)
	sum := sha256.Sum256([]byte(b618Brief))
	b460Write(t, filepath.Join(repo, "facts.md"), "# Facts\n\n- brief: `brief.md`\n- brief_sha256: `"+hex.EncodeToString(sum[:])+"`\n\n<!-- facts-json\n[]\n-->\n")
	b460Write(t, filepath.Join(repo, "packet.json"), `{"question":"Ship it?","options":["yes","no"],"s1":{"lane":"L1"},"context":"some context"}`)
	t.Setenv("CODEX_HOME", filepath.Join(w, "codexhome"))
	return w
}

// b618Report is the B549 report, what codex saw, and the repo tree.
func b618Report(t *testing.T, w string, code int, out, errText string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString(b549Report(t, w, code, out, errText))
	for _, name := range []string{"codex.argv", "codex.cwd", "codex.stdin", "codex.schema", "codex.prompt", "codex.home"} {
		if data, err := os.ReadFile(filepath.Join(w, "log", name)); err == nil {
			b.WriteString("== " + name + "\n" + b549Mask(w, string(data)))
		}
	}
	b.WriteString("== repo\n")
	var files []string
	repo := filepath.Join(w, "repo")
	_ = filepath.Walk(repo, func(path string, info os.FileInfo, err error) error {
		rel, _ := filepath.Rel(repo, path)
		if err == nil && path != repo && rel != ".git" && !strings.HasPrefix(rel, ".git/") {
			files = append(files, rel)
		}
		return nil
	})
	sort.Strings(files)
	for _, file := range files {
		b.WriteString(file + "\n")
	}
	return b551rHome.ReplaceAllString(b.String(), "coder-H")
}

// b618Record runs Node for args in the fixture (golden mode).
func b618Record(t *testing.T, w, node, nodeBin string, args []string, bg bool) (int, string, string) {
	t.Helper()
	if err := os.Symlink(filepath.Join(nodeBin, "node"), filepath.Join(w, "bin", "node")); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, args...)
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
	if err := os.Remove(filepath.Join(w, "bin", "node")); err != nil {
		t.Fatal(err)
	}
	return code, so.String(), se.String()
}

// b618Compare checks Go against the golden (or records it from Node).
func b618Compare(t *testing.T, w, golden string, args []string, bg bool) {
	t.Helper()
	if node := os.Getenv("B618_NODE"); node != "" {
		code, out, errText := b618Record(t, w, node, os.Getenv("B618_NODE_BIN"), args, bg)
		if err := os.WriteFile(golden, []byte(b618Report(t, w, code, out, errText)), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	code, out, errText := b460Call(t, args...)
	if bg && code == 0 {
		b549Reap(t, out)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if got := b618Report(t, w, code, out, errText); got != string(want) {
		t.Fatalf("%v differs from Node\n--- go\n%s\n--- node\n%s", args, got, want)
	}
	if rows := b460Handovers(t, filepath.Join(w, "home")); len(rows) != 0 {
		t.Fatalf("handed over: %v", rows)
	}
}

func TestB618AuthorS2CodexMatchesNode(t *testing.T) {
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	plan := []string{"author", "--job", "plan", "--brief", "brief.md", "--facts", "facts.md"}
	cases := []struct {
		name, cfg, mode string
		args            []string
	}{
		// former author-provider / system-two-provider hand-overs
		{"author-closed-run", "closed", "author", append(append([]string{}, plan...), "--run", "r9")},
		{"author-closed", "closed", "author", plan},
		{"s2-closed-run", "closed", "s2", []string{"s2", "--packet", "packet.json", "--run", "r9"}},
		{"s2-closed", "closed", "s2", []string{"s2", "--packet", "packet.json"}},
		{"author-unmapped", "unmapped", "author", plan},
		{"s2-unmapped-run", "unmapped", "s2", []string{"s2", "--packet", "packet.json", "--run", "r9", "--block", "B1"}},
		{"s2-fallback-unmapped", "fallback", "fail", []string{"s2", "--packet", "packet.json"}},
		// the open-book Codex session (review.allow_open_book_codex)
		{"author-open", "open", "author", plan},
		{"s2-open-run", "open", "s2", []string{"s2", "--packet", "packet.json", "--run", "r9"}},
		{"s2-open-fail", "open", "fail", []string{"s2", "--packet", "packet.json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := b618AuthorFixture(t, tc.mode, tc.cfg)
			if tc.name == "s2-fallback-unmapped" {
				// claude is missing: the ladder falls to its unmapped step
				if err := os.Remove(filepath.Join(w, "bin", "claude")); err != nil {
					t.Fatal(err)
				}
			}
			b618Compare(t, w, filepath.Join(here, "testdata", "b618", tc.name+".txt"), tc.args, false)
		})
	}
}

// A Codex effort the codex builder refuses is a TypeError Node prints with
// its stack: author and s2 still hand that over, writing nothing.
func TestB618CodexBadEffortStillHandsOver(t *testing.T) {
	for _, c := range []struct {
		args   []string
		reason string
	}{
		{[]string{"author", "--job", "plan", "--brief", "brief.md", "--facts", "facts.md", "--run", "r9"}, "author-provider"},
		{[]string{"s2", "--packet", "packet.json", "--run", "r9"}, "system-two-provider"},
	} {
		w := b618AuthorFixture(t, "author", "xhigh")
		code, out, _ := b460Call(t, c.args...)
		if code != 127 || out != "" {
			t.Fatalf("%v = %d %q; want the Node hand-over", c.args, code, out)
		}
		if got := b460Handovers(t, filepath.Join(w, "home")); len(got) != 1 || got[0] != c.args[0]+" "+c.reason {
			t.Fatalf("%v: handovers %v", c.args, got)
		}
		for _, file := range b549Files(t, w) {
			if strings.HasPrefix(file, "tmp/") || strings.Contains(file, "ledger") {
				t.Fatalf("%v: wrote %s before handing over", c.args, file)
			}
		}
	}
}

// b618SandboxConfigs: coder.sandbox blocks after the B551r config. @WS@ is
// the workspace.
var b618SandboxConfigs = map[string]string{
	"workspace": "coder:\n  sandbox:\n    profile: workspace\n",
	"toolchain": "coder:\n  sandbox:\n    profile: toolchain\n    add_dirs:\n      - @WS@/cache\n      - @WS@/cache/\n      - ~/build\n      - @WS@/link\n",
	"unused":    "coder:\n  sandbox:\n    add_dirs:\n      - @WS@/cache\n",
	"full":      "coder:\n  sandbox:\n    profile: full\n    add_dirs:\n      - @WS@/cache\n",
	"ssh":       "coder:\n  sandbox:\n    profile: toolchain\n    add_dirs:\n      - @WS@/cache\n      - ~/.ssh/keys\n",
	"tmp":       "coder:\n  sandbox:\n    profile: toolchain\n    add_dirs:\n      - @WS@/tmp/x\n",
	"root":      "coder:\n  sandbox:\n    profile: workspace\n    add_dirs:\n      - /\n",
	"home":      "coder:\n  sandbox:\n    profile: toolchain\n    add_dirs:\n      - ~/\n",
	"holds":     "coder:\n  sandbox:\n    profile: toolchain\n    add_dirs:\n      - @WS@\n",
	"loop":      "coder:\n  sandbox:\n    profile: toolchain\n    add_dirs:\n      - @WS@/loop/x\n",
	"runroot":   "coder:\n  sandbox:\n    profile: toolchain\n    add_dirs:\n      - @WS@/tmp/code-forge/x\n",
}

func TestB618SpawnCodexSandboxMatchesNode(t *testing.T) {
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	coder := func(level string, extra ...string) []string {
		return append([]string{"spawn", "--level", level, "--role", "coder", "--brief", "brief.md"}, extra...)
	}
	cases := []struct {
		name, cfg, mode, record string
		args                    []string
	}{
		// former spawn-codex-sandbox hand-overs
		{"sb-workspace", "workspace", "ok", "", coder("L3", "--block", "B1")},
		{"sb-workspace-run", "workspace", "ok", b549Record, coder("L3", "--run", "r1", "--block", "B1")},
		{"sb-toolchain", "toolchain", "ok", "", coder("L3", "--block", "B1")},
		{"sb-toolchain-argv", "toolchain", "ok", "", coder("L3", "--print-argv")},
		{"sb-unused", "unused", "ok", "", coder("L3")},
		{"sb-full", "full", "ok", "", coder("L3", "--block", "B1")},
		{"sb-fail", "toolchain", "fail", "", coder("L3")},
		{"sb-bg", "toolchain", "ok", "", coder("L3", "--block", "B1", "--background")},
		{"sb-fallback", "toolchain", "ok", "", coder("L0", "--block", "B1")},
		{"sb-l0-claude", "full", "ok", "", coder("L0", "--block", "B1")},
		{"sb-ssh", "ssh", "ok", "", coder("L3")},
		{"sb-tmp", "tmp", "ok", "", coder("L3")},
		{"sb-root", "root", "ok", "", coder("L3")},
		{"sb-home", "home", "ok", "", coder("L3")},
		{"sb-holds", "holds", "ok", "", coder("L3")},
		{"sb-loop", "loop", "ok", "", coder("L3")},
		{"sb-ssh-argv", "ssh", "ok", "", coder("L3", "--print-argv")},
		{"sb-runroot", "runroot", "ok", "", coder("L3")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := b551rFixture(t, tc.mode, tc.record)
			b460Write(t, filepath.Join(w, "repo", ".code-forge.yml"), b551rConfig+strings.ReplaceAll(b618SandboxConfigs[tc.cfg], "@WS@", w))
			if err := os.MkdirAll(filepath.Join(w, "cache"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(w, "cache2"), filepath.Join(w, "link")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("loop", filepath.Join(w, "loop")); err != nil {
				t.Fatal(err)
			}
			if tc.name == "sb-fallback" {
				// claude is missing: the L0 ladder falls to its openai step
				if err := os.Remove(filepath.Join(w, "bin", "claude")); err != nil {
					t.Fatal(err)
				}
			}
			bg := strings.Contains(strings.Join(tc.args, " "), "--background")
			b618Compare(t, w, filepath.Join(here, "testdata", "b618", tc.name+".txt"), tc.args, bg)
		})
	}
}

// A coder.sandbox that is not a mapping is a TypeError Node prints with its
// stack (file paths and line numbers): spawn still hands that over before
// anything is written.
func TestB618SpawnSandboxNotMappingStillHandsOver(t *testing.T) {
	w := b551rFixture(t, "ok", "")
	b460Write(t, filepath.Join(w, "repo", ".code-forge.yml"), b551rConfig+"coder:\n  sandbox: workspace\n")
	code, out, _ := b460Call(t, "spawn", "--level", "L3", "--role", "coder", "--brief", "brief.md")
	if code != 127 || out != "" {
		t.Fatalf("= %d %q; want the Node hand-over", code, out)
	}
	if got := strings.Join(b460Handovers(t, filepath.Join(w, "home")), ","); got != "spawn spawn-codex-sandbox" {
		t.Fatalf("hand-overs = %s", got)
	}
	for _, file := range b549Files(t, w) {
		if strings.HasPrefix(file, "tmp/") || strings.Contains(file, "ledger") {
			t.Fatalf("wrote %s before handing over", file)
		}
	}
}
