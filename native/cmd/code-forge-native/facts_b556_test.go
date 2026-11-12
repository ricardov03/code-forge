package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// B556: the facts session runs in Go. Each testdata/b556/<case>.txt is Node's
// report for the same workspace, recorded with CODE_FORGE_NATIVE=0
// ./bin/code-forge facts … (Node 24) and fake claude/grok CLIs: exit code,
// stdout, stderr, the fake CLI's argv, cwd, stdin packet and snapshot listing
// with modes, every file the call left under HOME, the tmp root and the
// repo, and the text of the ledger, breadcrumbs, error log and sheet.
// Masks (Node and Go alike): the workspace path (<WS>), proc-<pid>-<ms>,
// facts/<ms>-<hex>, "ts", "duration_ms", built_at, brief_mtime and the error
// log's version, node, platform and arch fields.

const fcFakeClaude = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > "$FAKE_LOG/claude.argv"
pwd -P > "$FAKE_LOG/claude.cwd"
cat > "$FAKE_LOG/stdin.txt"
find . | sort | while read -r f; do printf '%s %s\n' "$(ls -ld "$f" | cut -c1-10)" "$f"; done > "$FAKE_LOG/snapshot.txt"
case "$FAKE_MODE" in
fail) echo "boom" >&2; exit 5;;
esac
printf '{"type":"result","is_error":false,"result":"","structured_output":%s,"usage":{"input_tokens":100,"output_tokens":20}}' "$FAKE_ANSWER"
`

const fcFakeGrok = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > "$FAKE_LOG/grok.argv"
pwd -P > "$FAKE_LOG/grok.cwd"
find . | sort | while read -r f; do printf '%s %s\n' "$(ls -ld "$f" | cut -c1-10)" "$f"; done > "$FAKE_LOG/snapshot.txt"
case "$FAKE_MODE" in
fail) echo "boom" >&2; exit 5;;
esac
ESC=$(printf '%s' "$FAKE_ANSWER" | sed 's/\\/\\\\/g; s/"/\\"/g')
printf '{"text":"%s","usage":{"prompt_tokens":7,"completion_tokens":3}}' "$ESC"
`

const fcAnswer = `{"facts":[{"fact_id":"F1","claim":"src/app.mjs","kind":"path","command":"ls src/app.mjs","output_excerpt":"src/app.mjs","tag":"VERIFIED","why":null},{"fact_id":"F2","claim":"--alpha","kind":"flag","command":"grep -rn -- --alpha src","output_excerpt":"src/app.mjs:1:const a = '--alpha';","tag":"VERIFIED","why":null},{"fact_id":"F3","claim":"docs/missing.md","kind":"path","command":"cat docs/missing.md | head","output_excerpt":"No such file","tag":"NOT-FOUND","why":null}]}`

const fcBrief = "# Brief\n\nThe file `src/app.mjs` reads the flag --alpha.\nIt also needs docs/missing.md and src/new.mjs.\nThe ignored file ignored.txt stays out.\nSet `NODE_ENV` first; see /usr/bin/env.\n"

// fcWorkspace builds golden.sh's workspace: fake CLIs on PATH, a one-commit
// git repo with an ignored file, a .env and an untracked src/new.mjs.
func fcWorkspace(t *testing.T, provider, brief string) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"bin", "repo/src", "repo/docs", "tmp", "home", "log"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(base, "repo")
	files := map[string]string{
		"bin/claude":           fcFakeClaude,
		"bin/grok":             fcFakeGrok,
		"repo/.code-forge.yml": "version: 1\nproject:\n  name: unit\n  slug: unit\nlevels:\n  L0:\n    provider: " + provider + "\n    model: m-x\n",
		"repo/src/app.mjs":     "const a = '--alpha';\nexport function fooBar() {}\n",
		"repo/docs/readme.md":  "notes\n",
		"repo/.env":            "secret=1\n",
		"repo/.gitignore":      "ignored.txt\n",
		"repo/ignored.txt":     "x\n",
		"repo/brief.md":        brief,
	}
	for name, text := range files {
		mode := os.FileMode(0o644)
		if strings.HasPrefix(name, "bin/") {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(base, name), []byte(text), mode); err != nil {
			t.Fatal(err)
		}
	}
	for _, argv := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.email=a@b", "-c", "user.name=n", "commit", "-qm", "init"}} {
		cmd := exec.Command("git", argv...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", argv, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "new.mjs"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); strings.HasPrefix(name, "CODE_FORGE_") || strings.HasPrefix(name, "GIT_") {
			value := os.Getenv(name)
			t.Setenv(name, value)
			_ = os.Unsetenv(name)
		}
	}
	t.Setenv("PATH", filepath.Join(base, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("TMPDIR", filepath.Join(base, "tmp")+"/")
	t.Setenv("CODE_FORGE_TMP_ROOT", filepath.Join(base, "tmp", "cf"))
	t.Setenv("CODE_FORGE_NATIVE", "1")
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b556-node")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("FAKE_LOG", filepath.Join(base, "log"))
	t.Setenv("FAKE_MODE", "ok")
	t.Setenv("FAKE_ANSWER", fcAnswer)
	schema, err := filepath.Abs(filepath.Join("..", "..", "..", "schema", "facts.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	saved := fcSchemaPath
	fcSchemaPath = func() (string, error) { return schema, nil }
	t.Cleanup(func() { fcSchemaPath = saved })
	fcChdir(t, repo)
	return base
}

// fcFind is `find <roots> [-path repo/.git -prune] -print | sort` (files only with filesOnly).
func fcFind(base string, roots []string, filesOnly bool) []string {
	out := []string{}
	for _, root := range roots {
		_ = filepath.Walk(filepath.Join(base, root), func(p string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(base, p)
			if rel == "repo/.git" {
				return filepath.SkipDir
			}
			if !filesOnly || info.Mode().IsRegular() {
				out = append(out, rel)
			}
			return nil
		})
	}
	sort.Strings(out)
	return out
}

var fcMasks = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`proc-[0-9]+-[0-9]+`), "proc-P-T"},
	{regexp.MustCompile(`facts/[0-9]+-[0-9a-f]{8}`), "facts/T-H"},
	{regexp.MustCompile(`"ts":"[^"]*"`), `"ts":"T"`},
	{regexp.MustCompile(`"duration_ms":[0-9]+`), `"duration_ms":D`},
	{regexp.MustCompile("(built_at|brief_mtime): `[^`]*`"), "$1: `T`"},
	// the error log's host fields: the package version and Node version a
	// test build does not have, and the machine the golden was recorded on
	{regexp.MustCompile(`"version":"[^"]*","node":"[^"]*","platform":"[^"]*","arch":"[^"]*"`), `"version":"V","node":"N","platform":"P","arch":"A"`},
}

func fcMask(text string) string {
	for _, m := range fcMasks {
		text = m.re.ReplaceAllString(text, m.with)
	}
	return text
}

// fcReport is golden.sh's report of one call.
func fcReport(base string, code int, stdout, stderr string) string {
	var b strings.Builder
	b.WriteString("exit=" + strconv.Itoa(code) + "\n--- stdout\n" + stdout + "--- stderr\n" + stderr)
	logs, _ := filepath.Glob(filepath.Join(base, "log", "*"))
	sort.Strings(logs)
	for _, f := range logs {
		data, _ := os.ReadFile(f)
		b.WriteString("--- log/" + filepath.Base(f) + "\n" + string(data))
	}
	b.WriteString("--- files\n")
	for _, p := range fcFind(base, []string{"home", "tmp", "repo"}, false) {
		b.WriteString(p + "\n")
	}
	for _, f := range fcFind(base, []string{"home", "repo/plans", "repo/out"}, true) {
		data, _ := os.ReadFile(filepath.Join(base, f))
		b.WriteString("--- " + f + "\n" + string(data))
	}
	return fcMask(strings.ReplaceAll(b.String(), base, "<WS>"))
}

func TestB556FactsSessionMatchesNode(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	cases := []struct {
		name, provider, mode, answer string
		args                         []string
	}{
		{"ok", "anthropic", "ok", "", nil},
		{"fail", "anthropic", "fail", "", nil},
		{"schema", "anthropic", "ok", `{"facts":[{"fact_id":"X1","claim":"","kind":"path","command":"ls","output_excerpt":"","tag":"VERIFIED","why":null}]}`, nil},
		{"miss", "anthropic", "ok", `{"nope":1}`, nil},
		{"run", "anthropic", "ok", "", []string{"--run", "r9"}},
		{"sources", "anthropic", "ok", "", []string{"--sources", "src/new.mjs", "--out", "out/sheet.md"}},
		{"xai", "xai", "ok", "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			golden, err := filepath.Abs(filepath.Join("testdata", "b556", c.name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			base := fcWorkspace(t, c.provider, fcBrief)
			t.Setenv("FAKE_MODE", c.mode)
			if c.answer != "" {
				t.Setenv("FAKE_ANSWER", c.answer)
			}
			code, stdout, stderr := runCaptured(t, append([]string{"facts", "--brief", "brief.md"}, c.args...)...)
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got := fcReport(base, code, stdout, stderr); got != fcMask(string(want)) {
				t.Fatalf("report differs from Node's\n--- got\n%s\n--- want (Node)\n%s", got, want)
			}
		})
	}
}

// The cases Go does not serve byte-equal hand over to Node before any write:
// no breadcrumb, nothing under the tmp root, no sheet, no ledger row.
func TestB556FactsNarrowHandovers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	cases := []struct {
		name, provider, brief string
		args                  []string
		setup                 func(t *testing.T, base string) []string
		reason                string
	}{
		{"openai L0 runs the Codex CLI", "openai", fcBrief, nil, nil, "facts-codex-step"},
		{"a glob path claim", "anthropic", "# Brief\n\nSee `src/*.mjs`.\n", nil, nil, "facts-glob-claim"},
		{"a brief that is not UTF-8", "anthropic", "# Brief\n\nThe file `src/app.mjs` \xff\xfe.\n", nil, nil, "facts-brief-encoding"},
		{"a source outside the project", "anthropic", fcBrief, nil, func(t *testing.T, base string) []string {
			out := filepath.Join(base, "outside.md")
			if err := os.WriteFile(out, []byte("x\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			return []string{"--sources", out}
		}, "facts-outside-source"},
		{"a run root inside a git work tree", "anthropic", fcBrief, nil, func(t *testing.T, base string) []string {
			t.Setenv("CODE_FORGE_TMP_ROOT", filepath.Join(base, "repo", "tmpcf"))
			return []string{"--run", "r9"}
		}, "facts-root-in-git"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			base := fcWorkspace(t, c.provider, c.brief)
			args := append([]string{"facts", "--brief", "brief.md"}, c.args...)
			if c.setup != nil {
				args = append(args, c.setup(t, base)...)
			}
			if code, _ := run(args); code != 127 {
				t.Fatalf("code=%d; want the Node hand-over", code)
			}
			home := filepath.Join(base, "home")
			data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
			var row handoverRow
			if json.Unmarshal(bytes.TrimSpace(data), &row) != nil || row.Verb != "facts" || row.Reason != c.reason {
				t.Fatalf("handover row %q; want reason %q", data, c.reason)
			}
			for _, p := range []string{".code-forge/logs/breadcrumbs.json", ".code-forge/ledger", ".code-forge/logs/errors.jsonl"} {
				if _, err := os.Stat(filepath.Join(home, p)); err == nil {
					t.Fatalf("Go wrote %s before the hand-over", p)
				}
			}
			for _, p := range []string{"tmp", "log", "repo/plans", "repo/tmpcf"} {
				if left := fcTree(t, filepath.Join(base, p)); len(left) != 0 {
					t.Fatalf("Go wrote under %s before the hand-over: %v", p, left)
				}
			}
		})
	}
}

// spawn --role facts --cwd runs in Go since B556 (it was the spawn-facts-cwd
// hand-over): a cwd outside a facts snapshot area is refused with Node's
// stderr and exit 2 (CODE_FORGE_NATIVE=0 on the same b549 layout printed
// the same line, exit 2, and no CLI ran), and no hand-over row is written.
func TestB556SpawnFactsCwdRefusedLikeNode(t *testing.T) {
	w := b549Fixture(t, "ok", "")
	code, out, errText := b460Call(t, "spawn", "--level", "L1", "--role", "facts", "--brief", "brief.md", "--cwd", ".")
	if want := "spawn: facts cwd refused: the run root has no facts snapshot area\n"; code != 2 || out != "" || errText != want {
		t.Fatalf("= %d %q %q; want 2, \"\", %q", code, out, errText, want)
	}
	if rows := b460Handovers(t, filepath.Join(w, "home")); len(rows) != 0 {
		t.Fatalf("handed over: %v", rows)
	}
	if entries, _ := os.ReadDir(filepath.Join(w, "log")); len(entries) != 0 {
		t.Fatalf("a CLI ran: %v", entries)
	}
	for _, file := range b549Files(t, w) {
		if strings.Contains(file, "ledger") || strings.Contains(file, "sessions") {
			t.Fatalf("wrote %s", file)
		}
	}
}
