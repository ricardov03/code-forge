package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// B549: `spawn --role coder` (--run, --block, --background), `--role facts`
// and --background for any role run in Go. testdata/b549/<case>.txt is what
// Node printed and wrote for the same setup (CODE_FORGE_NATIVE=0
// ./bin/code-forge spawn ..., fake claude and grok on PATH, temp HOME and
// TMPDIR), as one report: exit code, stdout, stderr, ledger, the fake CLI's
// argv/cwd/prompt/stdin, every file under tmp/ and home/, pid.json, the pid
// registry entry and the session logs. Masks: the workspace path (<ws>),
// duration_ms, pid, proc-<pid>-<ms>, the session dir's -<ms>-<hex>, ts, mac,
// start_time and pids/<pid>.json.

const b549Claude = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > "$FAKE_LOG/claude.argv"
pwd -P > "$FAKE_LOG/claude.cwd"
last=""; for a in "$@"; do last="$a"; done
if [ -f "$last" ]; then cp "$last" "$FAKE_LOG/prompt.txt"; fi
cat > "$FAKE_LOG/stdin.txt"
case "$FAKE_MODE" in
fail) echo "boom" >&2; exit 5;;
esac
printf '%s' '{"type":"result","is_error":false,"result":"built it\nTESTS go test ./... exit=0\n===BLOCK B1 COMPLETE===","usage":{"input_tokens":100,"output_tokens":20}}'
`

const b549Grok = `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > "$FAKE_LOG/grok.argv"
pwd -P > "$FAKE_LOG/grok.cwd"
case "$FAKE_MODE" in
fail) echo "boom" >&2; exit 5;;
esac
printf '%s' '{"text":"grok did it\n===BLOCK B1 FAILED: no tests===","usage":{"prompt_tokens":7,"completion_tokens":3}}'
`

const b549Config = `version: 1
levels:
  L0:
    provider: anthropic
    model: claude-x
    fallback:
      - provider: openai
        model: gpt-x
  L1:
    provider: anthropic
    model: claude-x
    effort: high
    fallback:
      - provider: anthropic
        model: claude-y
      - provider: xai
        model: grok-x
  L2:
    provider: xai
    model: grok-x
    effort: high
  L3:
    provider: openai
    model: gpt-x
`

// b549Hash is configHash({version: 1, coder: {sandbox: {profile: "x"}}}).
const b549Hash = "a14758136ebc0f37ccc2d1eebf7c18d83310f03cb0dccf5313af83488ad0afed"

const (
	b549RecordHead = `{"run_id":"r1","project":"unit","workspace":"@REPO@","status":"active","engine":"claude",`
	b549Record     = b549RecordHead + `"blocks":{"B1":{"brief":{"path":"docs/b1.md"},"acceptance":[{"clause":"it builds","tests":["TestBuild"]},{"clause":"owner looks","kind":"human_check"}]}}}`
	b549RecordNone = b549RecordHead + `"blocks":{}}`
	b549RecordSand = b549RecordHead + `"config":{"hash":"` + b549Hash + `","snapshots":{"` + b549Hash + `":{"version":1,"coder":{"sandbox":{"profile":"x"}}}}}}`
	b549RecordBad  = b549RecordHead + `"config":{"hash":"` + b549Hash + `","snapshots":{"` + b549Hash + `":{"version":2}}}}`
)

// b549Fixture is golden.sh's layout: <ws>/bin (fake CLIs), <ws>/repo (a git
// repo with the config, brief.md and docs/b1.md), <ws>/tmp (TMPDIR),
// <ws>/home (HOME), <ws>/log (what the fake CLIs saw). Returns <ws>.
func b549Fixture(t *testing.T, mode, record string) string {
	t.Helper()
	w, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"bin", "repo/docs", "tmp", "home/.code-forge/runs", "log"} {
		if err := os.MkdirAll(filepath.Join(w, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b460Write(t, filepath.Join(w, "bin", "claude"), b549Claude)
	b460Write(t, filepath.Join(w, "bin", "grok"), b549Grok)
	for _, n := range []string{"claude", "grok"} {
		if err := os.Chmod(filepath.Join(w, "bin", n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	repo := filepath.Join(w, "repo")
	b460Git(t, repo, "init", "-q")
	b460Write(t, filepath.Join(repo, ".code-forge.yml"), b549Config)
	b460Write(t, filepath.Join(repo, "brief.md"), "Build block one.\nKeep it small.\n\n")
	b460Write(t, filepath.Join(repo, "docs", "b1.md"), "The block brief.\nLine two.\n")
	home := filepath.Join(w, "home")
	if record != "" {
		runs := filepath.Join(home, ".code-forge", "runs")
		if err := os.WriteFile(filepath.Join(runs, "r1.json"), []byte(strings.ReplaceAll(record, "@REPO@", repo)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runs, "r1.key"), bytes.Repeat([]byte{7}, 32), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", filepath.Join(home, "missing-node"))
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	t.Setenv("PATH", filepath.Join(w, "bin")+":/usr/bin:/bin")
	t.Setenv("TMPDIR", filepath.Join(w, "tmp"))
	t.Setenv("CODE_FORGE_TMP_ROOT", "")
	t.Setenv("CODE_FORGE_RUN_ID", "")
	t.Setenv("FAKE_MODE", mode)
	t.Setenv("FAKE_LOG", filepath.Join(w, "log"))
	b460Chdir(t, repo)
	return w
}

var b549Masks = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`"duration_ms":[0-9]*`), `"duration_ms":N`},
	{regexp.MustCompile(`"pid":[0-9]*`), `"pid":N`},
	{regexp.MustCompile(`proc-[0-9]*-[0-9]*`), `proc-N`},
	{regexp.MustCompile(`-[0-9]{13}-[0-9a-f]{8}`), `-T-R`},
	{regexp.MustCompile(`,"ts":"[^"]*"`), ``},
	{regexp.MustCompile(`,"mac":"[^"]*"`), ``},
	{regexp.MustCompile(`"start_time":"[^"]*"`), `"start_time":"S"`},
	{regexp.MustCompile(`pids/[0-9]*\.json`), `pids/N.json`},
}

func b549Mask(w, s string) string {
	s = strings.ReplaceAll(s, w, "<ws>")
	for _, m := range b549Masks {
		s = m.re.ReplaceAllString(s, m.with)
	}
	return s
}

func b549Read(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

func b549Files(t *testing.T, w string) []string {
	t.Helper()
	var files []string
	for _, top := range []string{"tmp", "home"} {
		_ = filepath.Walk(filepath.Join(w, top), func(path string, info os.FileInfo, err error) error {
			if err == nil && info.Mode().IsRegular() {
				rel, _ := filepath.Rel(w, path)
				files = append(files, rel)
			}
			return nil
		})
	}
	sort.Strings(files)
	return files
}

// b549Report is golden.sh's report for this run.
func b549Report(t *testing.T, w string, code int, out, errText string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("== code " + strconv.Itoa(code) + "\n== out\n" + b549Mask(w, out) + "== err\n" + b549Mask(w, errText) + "== ledger\n")
	ledgers, _ := filepath.Glob(filepath.Join(w, "home", ".code-forge", "ledger", "*.jsonl"))
	for _, file := range ledgers {
		b.WriteString(b549Mask(w, b549Read(file)))
	}
	for _, name := range []string{"claude.argv", "grok.argv", "claude.cwd", "grok.cwd", "prompt.txt", "stdin.txt"} {
		if data, err := os.ReadFile(filepath.Join(w, "log", name)); err == nil {
			b.WriteString("== " + name + "\n" + b549Mask(w, string(data)))
		}
	}
	b.WriteString("== files\n")
	files := b549Files(t, w)
	for _, file := range files {
		b.WriteString(b549Mask(w, file) + "\n")
	}
	for _, file := range files {
		if strings.HasPrefix(file, "tmp/") && filepath.Base(file) == "pid.json" {
			b.WriteString("== pid.json\n" + b549Mask(w, b549Read(filepath.Join(w, file))))
		}
	}
	for _, file := range files {
		if strings.HasPrefix(file, "tmp/") && filepath.Base(filepath.Dir(file)) == "pids" {
			b.WriteString("== pids entry\n" + b549Mask(w, b549Read(filepath.Join(w, file))))
		}
	}
	for _, file := range files {
		if base := filepath.Base(file); strings.HasPrefix(file, "tmp/") && (base == "session.log" || base == "session.err.log") {
			b.WriteString("== " + base + "\n" + b549Mask(w, b549Read(filepath.Join(w, file))))
		}
	}
	return b.String()
}

// b549Reap waits for the background child this test started (pid.json in
// the session the result names), so its log is complete and no process is
// left behind; it never signals anything.
func b549Reap(t *testing.T, out string) {
	t.Helper()
	var res struct {
		Status string `json:"status"`
		Pid    int    `json:"pid"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.Status != "started" || res.Pid <= 1 {
		t.Fatalf("background result %q", out)
	}
	var status syscall.WaitStatus
	if _, err := syscall.Wait4(res.Pid, &status, 0, nil); err != nil {
		t.Fatalf("wait4 %d: %v", res.Pid, err)
	}
}

func TestB549SpawnCoderFactsBackgroundMatchNode(t *testing.T) {
	cases := []struct {
		name, mode, record string
		args               []string
	}{
		{"coder-fg", "ok", "", []string{"--level", "L1", "--role", "coder", "--brief", "brief.md"}},
		{"coder-block", "ok", "", []string{"--level", "L1", "--role", "coder", "--brief", "brief.md", "--block", "B1"}},
		{"coder-run-norecord", "ok", "", []string{"--level", "L1", "--role", "coder", "--brief", "brief.md", "--run", "r1", "--block", "B1"}},
		{"coder-run-record", "ok", b549Record, []string{"--level", "L1", "--role", "coder", "--brief", "brief.md", "--run", "r1", "--block", "B1"}},
		{"coder-run-noblock", "ok", b549RecordNone, []string{"--level", "L1", "--role", "coder", "--brief", "brief.md", "--run", "r1", "--block", "B1"}},
		{"coder-badblock", "ok", "", []string{"--level", "L1", "--role", "coder", "--brief", "brief.md", "--block", "B 1"}},
		{"coder-xai", "ok", "", []string{"--level", "L2", "--role", "coder", "--brief", "brief.md", "--block", "B1"}},
		{"coder-sandbox", "ok", b549RecordSand, []string{"--level", "L1", "--role", "coder", "--brief", "brief.md", "--run", "r1"}},
		{"coder-badsnap", "ok", b549RecordBad, []string{"--level", "L1", "--role", "coder", "--brief", "brief.md", "--run", "r1"}},
		{"coder-fail", "fail", "", []string{"--level", "L1", "--role", "coder", "--brief", "brief.md"}},
		{"coder-argv", "ok", "", []string{"--level", "L1", "--role", "coder", "--brief", "brief.md", "--block", "B1", "--print-argv"}},
		{"bg-reviewer", "ok", "", []string{"--level", "L1", "--role", "reviewer", "--brief", "brief.md", "--background"}},
		{"bg-coder", "ok", "", []string{"--level", "L1", "--role", "coder", "--brief", "brief.md", "--block", "B1", "--background"}},
		{"bg-coder-l0", "ok", "", []string{"--level", "L0", "--role", "coder", "--brief", "brief.md", "--background", "--run", "r1"}},
		{"facts-claude", "ok", "", []string{"--level", "L1", "--role", "facts", "--brief", "brief.md"}},
		{"facts-grok", "ok", "", []string{"--level", "L2", "--role", "facts", "--brief", "brief.md"}},
		{"facts-fail", "fail", "", []string{"--level", "L1", "--role", "facts", "--brief", "brief.md"}},
	}
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := b549Fixture(t, tc.mode, tc.record)
			code, out, errText := b460Call(t, append([]string{"spawn"}, tc.args...)...)
			if strings.Contains(strings.Join(tc.args, " "), "--background") && code == 0 {
				b549Reap(t, out)
			}
			want, err := os.ReadFile(filepath.Join(here, "testdata", "b549", tc.name+".txt"))
			if err != nil {
				t.Fatal(err)
			}
			if got := b549Report(t, w, code, out, errText); got != string(want) {
				t.Fatalf("%v differs from Node\n--- go\n%s\n--- node\n%s", tc.args, got, want)
			}
			if rows := b460Handovers(t, filepath.Join(w, "home")); len(rows) != 0 {
				t.Fatalf("handed over: %v", rows)
			}
		})
	}
}

// The cases Go leaves to Node hand over by name before anything is written:
// no run root, no session, no ledger row.
func TestB549SpawnNarrowHandovers(t *testing.T) {
	cases := []struct {
		name, reason, record string
		args                 []string
	}{
		// B551r: a Codex coder step runs in Go unless coder.sandbox is set
		{"codex coder sandbox --run", "spawn-codex-sandbox", "", []string{"--level", "L3", "--role", "coder", "--brief", "brief.md", "--run", "r1"}},
		{"codex coder sandbox fallback", "spawn-codex-sandbox", "", []string{"--level", "L0", "--role", "coder", "--brief", "brief.md"}},
		{"facts --cwd", "spawn-facts-cwd", "", []string{"--level", "L1", "--role", "facts", "--brief", "brief.md", "--cwd", "."}},
		{"unreadable run record", "spawn-coder-run", "{not json", []string{"--level", "L1", "--role", "coder", "--brief", "brief.md", "--run", "r1"}},
		{"non-object block", "spawn-coder-run", b549RecordHead + `"blocks":{"B1":"x"}}`, []string{"--level", "L1", "--role", "coder", "--brief", "brief.md", "--run", "r1", "--block", "B1"}},
		{"background cli missing", "spawn-background-start", "", []string{"--level", "L2", "--role", "reviewer", "--brief", "brief.md", "--background"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := b549Fixture(t, "ok", tc.record)
			if tc.reason == "spawn-codex-sandbox" {
				b460Write(t, filepath.Join(w, "repo", ".code-forge.yml"), b549Config+"coder:\n  sandbox:\n    profile: workspace\n")
			}
			if tc.reason == "spawn-background-start" {
				if err := os.Remove(filepath.Join(w, "bin", "grok")); err != nil {
					t.Fatal(err)
				}
			}
			code, out, _ := b460Call(t, append([]string{"spawn"}, tc.args...)...)
			if code != 127 || out != "" {
				t.Fatalf("%v = %d %q, want the Node hand-over (127)", tc.args, code, out)
			}
			if got := strings.Join(b460Handovers(t, filepath.Join(w, "home")), ","); got != "spawn "+tc.reason {
				t.Fatalf("hand-overs = %s", got)
			}
			for _, file := range b549Files(t, w) {
				if strings.HasPrefix(file, "tmp/") || strings.Contains(file, "ledger") {
					t.Fatalf("wrote %s before handing over", file)
				}
			}
			if entries, _ := os.ReadDir(filepath.Join(w, "log")); len(entries) != 0 {
				t.Fatalf("a CLI ran: %v", entries)
			}
		})
	}
}
