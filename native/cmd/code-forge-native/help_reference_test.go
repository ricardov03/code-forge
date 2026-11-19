package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// B801: every command in the help table has a reference (when to use it,
// flags with meanings, examples, exit codes, the --json shape, related
// commands), and every example runs against the real binary in a sandbox
// repo with fake provider CLIs on PATH.

// Red before B801: the help table had only a summary and a usage. Removing
// one command's examples makes the reference incomplete again.
func TestB801EveryCommandHasAReference(t *testing.T) {
	b613Env(t)
	if problems := helpRefProblems(); len(problems) > 0 {
		t.Fatalf("the command reference is incomplete:\n%s", strings.Join(problems, "\n"))
	}
	saved := helpRefs["version"]
	t.Cleanup(func() { helpRefs["version"] = saved })
	stripped := saved
	stripped.examples = nil
	helpRefs["version"] = stripped
	if problems := strings.Join(helpRefProblems(), "\n"); !strings.Contains(problems, "version: no example") {
		t.Fatalf("a command with no example is not reported:\n%s", problems)
	}
}

// commands.json lists every command of the help table, each with a purpose,
// a when-to-use text, examples with their exit codes, exit code meanings,
// flags with meanings and related commands that exist.
func TestB801CommandsJSONCoversTheTable(t *testing.T) {
	b613Env(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "reference", "commands.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version  int              `json:"version"`
		Commands []helpRefCommand `json:"commands"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	byName := map[string]helpRefCommand{}
	for _, cmd := range doc.Commands {
		byName[cmd.Name] = cmd
	}
	if doc.Version != 1 || len(doc.Commands) != len(helpCommands) {
		t.Fatalf("version %d, %d commands; want 1 and %d", doc.Version, len(doc.Commands), len(helpCommands))
	}
	for _, entry := range helpCommands {
		cmd, ok := byName[entry.name]
		switch {
		case !ok:
			t.Errorf("%s: missing", entry.name)
			continue
		case cmd.Purpose != entry.summary+"." || cmd.When == "" || cmd.Usage == "":
			t.Errorf("%s: purpose %q, when %q, usage %q", entry.name, cmd.Purpose, cmd.When, cmd.Usage)
		case len(cmd.Examples) == 0 || len(cmd.ExitCodes) < 2 || len(cmd.Related) == 0:
			t.Errorf("%s: %d examples, %d exit codes, %d related", entry.name, len(cmd.Examples), len(cmd.ExitCodes), len(cmd.Related))
		}
		codes := map[int]bool{}
		for _, e := range cmd.ExitCodes {
			codes[e.Code] = e.Meaning != ""
		}
		for _, ex := range cmd.Examples {
			if !strings.HasPrefix(ex.Command, "code-forge "+entry.name) || !codes[ex.Exit] {
				t.Errorf("%s: example %q (exit %d) has no exit meaning or runs another command", entry.name, ex.Command, ex.Exit)
			}
		}
		for _, f := range cmd.Flags {
			if f.Meaning == "" || f.Type == "" || f.Default == "" {
				t.Errorf("%s: flag %+v is incomplete", entry.name, f)
			}
		}
		for _, name := range cmd.Related {
			if _, ok := byName[name]; !ok {
				t.Errorf("%s: related %q is not a command", entry.name, name)
			}
		}
		if takes := helpRefTakesJSON(entry.name, cmd.Usage); takes != (cmd.JSON != nil) {
			t.Errorf("%s: takes --json %v but json shape %v", entry.name, takes, cmd.JSON)
		}
	}
}

// helpRefSandbox is the scratch package, repo and environment the examples
// run in. Nothing in it reads the real HOME, keychain or provider CLIs.
type helpRefSandbox struct {
	tmp, pkg, bin, home, repo, outside, version string
	env                                         []string
	head, run                                   string
}

func helpRefNewSandbox(t *testing.T) *helpRefSandbox {
	t.Helper()
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repoRoot, _ := filepath.Abs("../../..")
	s := &helpRefSandbox{tmp: tmp, pkg: filepath.Join(tmp, "pkg"), home: filepath.Join(tmp, "home"),
		repo: filepath.Join(tmp, "repo"), outside: filepath.Join(tmp, "outside")}
	// The binary finds its package root at native/bin/<target>/../../..;
	// the package files are links to this checkout.
	s.bin = filepath.Join(s.pkg, "native", "bin", "test", "code-forge-native")
	build := exec.Command("go", "build", "-ldflags", "-X github.com/ricardov03/code-forge/native/procguard.builtForTest=1", "-o", s.bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if text, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, text)
	}
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() == "native" || e.Name() == ".git" || e.Name() == "node_modules" {
			continue
		}
		if err := os.Symlink(filepath.Join(repoRoot, e.Name()), filepath.Join(s.pkg, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
	var manifest struct {
		Version string `json:"version"`
	}
	if data, err := os.ReadFile(filepath.Join(repoRoot, "package.json")); err == nil {
		_ = json.Unmarshal(data, &manifest)
	}
	s.version = manifest.Version
	stubs := filepath.Join(tmp, "stubs")
	for _, name := range []string{"claude", "codex", "gemini", "grok", "gh", "op", "curl", "security", "solo", "node"} {
		helpRefWrite(t, filepath.Join(stubs, name), "#!/bin/sh\nexit 1\n", 0o755)
	}
	helpRefWrite(t, filepath.Join(s.home, ".gitconfig"), "[user]\n\tname = T\n\temail = t@example.invalid\n[init]\n\tdefaultBranch = main\n", 0o644)
	s.env = []string{
		"PATH=" + stubs + ":/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + s.home, "TMPDIR=" + filepath.Join(tmp, "t"), "LANG=C", "TERM=dumb", "NO_COLOR=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(s.home, ".gitconfig"), "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"XDG_CONFIG_HOME=" + filepath.Join(s.home, ".config"), "XDG_CACHE_HOME=" + filepath.Join(s.home, ".cache"),
		"CODE_FORGE_KEY_BACKEND=file", "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_NODE=/nonexistent-b801-node",
		"CODE_FORGE_HANDOVER_LOG=0", "CODE_FORGE_TMP_ROOT=" + filepath.Join(tmp, "cf-tmp"),
		"HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "http_proxy=http://127.0.0.1:9", "https_proxy=http://127.0.0.1:9",
	}
	for _, dir := range []string{filepath.Join(tmp, "t"), filepath.Join(tmp, "cf-tmp"), s.outside} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	config := "version: 1\nproject:\n  name: demo\n  slug: demo\nprovider: anthropic\nlevels:\n" +
		"  L0:\n    model: claude-haiku-4-5-20251001\n  L1:\n    model: claude-sonnet-5\n" +
		"  L2:\n    model: claude-opus-5-5\n  L3:\n    model: claude-fable-5-2\n" +
		"caps:\n  coders: 1\nreview:\n  multimodel: false\nengine: auto\nharnesses: [claude]\nsystem1:\n  fallback: rules\n" +
		"gates:\n  test: [sh, test/a.test.sh]\n  lint: null\n  types: null\n  format: null\n" +
		"proof:\n  tiers:\n    high:\n      paths: []\n  isolation: export\n  export:\n    link_dirs: []\n    copy_untracked: []\n" +
		"autonomy:\n  coder_may_push: false\n  coder_may_open_draft_pr: false\n"
	for path, text := range map[string]string{
		".code-forge.yml":   config,
		"src/a.txt":         "a\n",
		"test/a.test.sh":    "grep -q b src/a.txt\n",
		"plans/acc.yml":     "- clause: a.txt says b\n  tests: [test/a.test.sh]\n",
		"plans/brief.md":    "# brief: change a.txt\n",
		"plans/plan.md":     "plan\n",
		"plans/t.txt":       "ran tests\n",
		"plans/report.json": "{}\n",
		"plans/p.json":      "{\"question\":\"q\"}\n",
		"plans/st.json":     "{}\n",
		"plans/c.json":      "[{\"id\":\"C1\",\"clause\":\"x\",\"tests\":[\"t1\"]}]\n",
		"plans/t.tap":       "TAP version 13\nok 1 - t1\n1..1\n",
		"plans/tr.txt":      "git push --force\n",
		"plans/outcome.json": "{\"skill\":\"demo\",\"status\":\"approved\",\"files\":[{\"path\":\"src/a.txt\",\"sha256\":\"\"}]," +
			"\"checks\":[{\"name\":\"lint\",\"result\":\"pass\"}]}\n",
	} {
		helpRefWrite(t, filepath.Join(s.repo, path), text, 0o644)
	}
	s.git(t, s.repo, "init", "-q")
	s.git(t, s.repo, "add", "-A")
	s.git(t, s.repo, "commit", "-qm", "start")
	s.head = strings.TrimSpace(s.git(t, s.repo, "rev-parse", "HEAD"))
	s.git(t, s.outside, "init", "-q")
	return s
}

// stopWorkers stops every worker started from this sandbox's binary.
func (s *helpRefSandbox) stopWorkers(t *testing.T) {
	t.Helper()
	for _, field := range strings.Fields(b511Pgrep(regexp.QuoteMeta(s.bin) + " worker --run")) {
		if pid, err := strconv.Atoi(field); err == nil {
			b511Stop(t, pid)
		}
	}
}

func helpRefWrite(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
}

func (s *helpRefSandbox) git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = dir, s.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// helpRefFill puts the live values into an example's args.
func (s *helpRefSandbox) fill(args string) string {
	now := time.Now().UTC()
	return strings.NewReplacer("{run}", s.run, "{head}", s.head,
		"{until}", now.Add(time.Hour).Format(time.RFC3339), "{until2}", now.Add(2*time.Hour).Format(time.RFC3339)).Replace(args)
}

// exec runs one example: stdout, then stderr.
func (s *helpRefSandbox) exec(t *testing.T, state, args string) (int, string, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", strconv.Quote(s.bin)+" "+s.fill(args))
	cmd.Dir, cmd.Env = s.repo, s.env
	if state == "outside" {
		cmd.Dir = s.outside
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("%s: %v", args, err)
	}
	if ctx.Err() != nil {
		t.Fatalf("%s: timed out", args)
	}
	return code, stdout.String(), stderr.String()
}

var (
	helpRefRunRe   = regexp.MustCompile(`r-\d{8}-\d{6}-[0-9a-f]+`)
	helpRefTimeRe  = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
	helpRefShaRe   = regexp.MustCompile(`\b[0-9a-f]{40}\b`)
	helpRefHexRe   = regexp.MustCompile(`\b(ap|ad|d|dg|row|h)-[0-9a-f]{8,}\b`)
	helpRefHashRe  = regexp.MustCompile(`\b[0-9a-f]{64}\b`)
	helpRefRevRe   = regexp.MustCompile(`\bR-\d{8}-\d{6}-[0-9a-f]+`)
	helpRefTickRe  = regexp.MustCompile(`"ticket":"[0-9a-f]+"`)
	helpRefPidRe   = regexp.MustCompile(`(pid|pid=|"pid":)\s?\d+`)
	helpRefMsRe    = regexp.MustCompile(`"(\w*_ms|\w*Ms|ms)":( ?)\d+`)
	helpRefLeftRe  = regexp.MustCompile(`"time_left_s":( ?)\d+`)
	helpRefDigitRe = regexp.MustCompile(`\d+`)
)

// normalize makes an output the same on every machine and every run.
func (s *helpRefSandbox) normalize(out string) string {
	out = strings.NewReplacer(s.repo, "<repo>", s.outside, "<repo>", s.home, "~", s.pkg, "<package>", s.tmp, "<tmp>",
		strings.TrimPrefix(s.tmp, "/private"), "<tmp>").Replace(out)
	if s.run != "" {
		out = strings.ReplaceAll(out, s.run, "<run>")
	}
	out = helpRefRunRe.ReplaceAllString(out, "<run>")
	out = helpRefHashRe.ReplaceAllString(out, "<sha256>")
	out = helpRefShaRe.ReplaceAllString(out, "<sha>")
	out = helpRefRevRe.ReplaceAllString(out, "R-<id>")
	out = helpRefTimeRe.ReplaceAllString(out, "2026-01-01T00:00:00Z")
	out = helpRefHexRe.ReplaceAllString(out, "$1-<id>")
	out = helpRefTickRe.ReplaceAllString(out, `"ticket":"<ticket>"`)
	out = helpRefPidRe.ReplaceAllString(out, "$1 <pid>")
	out = helpRefMsRe.ReplaceAllString(out, `"$1":${2}0`)
	out = helpRefLeftRe.ReplaceAllString(out, `"time_left_s":${1}3599`)
	out = strings.ReplaceAll(out, runtime.GOOS+"-"+runtime.GOARCH, "<platform>")
	if s.version != "" {
		out = strings.ReplaceAll(out, s.version, "<version>")
	}
	out = strings.ReplaceAll(out, "\x1b", "")
	return strings.TrimRight(out, "\n")
}

// helpRefTrim keeps an example output short: 15 lines of 300 characters.
func helpRefTrim(out string) string {
	lines := strings.Split(out, "\n")
	cut := len(lines) > 15
	if cut {
		lines = lines[:15]
	}
	for i, line := range lines {
		if r := []rune(line); len(r) > 300 {
			lines[i] = string(r[:300]) + "…"
		}
	}
	if cut {
		lines = append(lines, "…")
	}
	return strings.Join(lines, "\n")
}

// helpRefFirstWord is the first word of the first non-empty line, at most
// 24 characters, with digits masked: the part of a text output the check
// compares.
func helpRefFirstWord(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 {
			word := []rune(fields[0])
			if len(word) > 24 {
				word = word[:24]
			}
			return helpRefDigitRe.ReplaceAllString(string(word), "N")
		}
	}
	return ""
}

func helpRefKeyNames(shape *helpRefJSONShape) string {
	names := []string{shape.Kind}
	for _, k := range shape.Keys {
		names = append(names, k.Key)
	}
	return strings.Join(names, " ")
}

// Every example runs in state order against the real binary. The exit code
// must match the recorded one; a --json example must print the recorded
// shape; a text example must start like the recorded output.
// CODE_FORGE_B801_RECORD=1 rewrites help_reference_examples.json instead.
func TestB801DocExamplesRun(t *testing.T) {
	if testing.Short() {
		t.Skip("runs every reference example")
	}
	b613Env(t)
	record := os.Getenv("CODE_FORGE_B801_RECORD") == "1"
	rec := helpRefLoad()
	s := helpRefNewSandbox(t)
	t.Cleanup(func() { s.stopWorkers(t) })
	out := helpRefRecorded{Version: 1, Commands: map[string]helpRefRecordedCommand{}}
	ran := 0
	for _, state := range helpRefStates {
		for _, entry := range helpCommands {
			for _, ex := range helpRefs[entry.name].examples {
				if ex.state != state {
					continue
				}
				ran++
				code, stdout, stderr := s.exec(t, state, ex.args)
				if state == "start" && s.run == "" {
					s.run = helpRefRunRe.FindString(stdout + stderr)
				}
				text := s.normalize(strings.TrimRight(stdout, "\n") + "\n" + stderr)
				text = strings.Trim(text, "\n")
				shape, isJSON := helpRefShape(stdout)
				got := out.Commands[entry.name]
				got.Examples = append(got.Examples, helpRefRecord{Args: ex.args, Exit: code, Out: helpRefTrim(text)})
				if isJSON && got.JSON == nil && strings.Contains(" "+ex.args+" ", " --json ") {
					got.JSON = shape
				}
				out.Commands[entry.name] = got
				if record {
					continue
				}
				want, ok := helpRefFind(rec, entry.name, ex.args)
				if !ok {
					t.Errorf("%s: no recorded run; run with CODE_FORGE_B801_RECORD=1", ex.args)
					continue
				}
				if code != want.Exit {
					t.Errorf("%s: exit %d, recorded %d\n%s", ex.args, code, want.Exit, text)
					continue
				}
				recShape := rec.Commands[entry.name].JSON
				if _, wasJSON := helpRefShape(want.Out); wasJSON || (recShape != nil && isJSON && strings.Contains(" "+ex.args+" ", " --json ")) {
					if !isJSON {
						t.Errorf("%s: prints no JSON; recorded:\n%s\ngot:\n%s", ex.args, want.Out, text)
					} else if recShape != nil && strings.Contains(" "+ex.args+" ", " --json ") && helpRefKeyNames(shape) != helpRefKeyNames(recShape) {
						t.Errorf("%s: JSON keys %q, recorded %q", ex.args, helpRefKeyNames(shape), helpRefKeyNames(recShape))
					}
					continue
				}
				if helpRefFirstWord(text) != helpRefFirstWord(want.Out) {
					t.Errorf("%s: output starts %q, recorded %q\n%s", ex.args, helpRefFirstWord(text), helpRefFirstWord(want.Out), text)
				}
			}
		}
		switch state {
		case "none", "stop":
			// review starts its own run, and run end cannot signal a worker
			// that left the test's process tree (procguard): the test stops
			// the sandbox workers itself.
			s.stopWorkers(t)
		case "start":
			if s.run == "" {
				t.Fatal("no example of state start printed a run id")
			}
			// No worker picks up queued reviews, so the outputs that
			// follow do not depend on its timing.
			s.stopWorkers(t)
		case "open":
			helpRefWrite(t, filepath.Join(s.repo, "src", "a.txt"), "b\n", 0o644)
		}
	}
	if ran == 0 {
		t.Fatal("no example ran")
	}
	if record {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile("help_reference_examples.json", b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %d examples", ran)
	}
}
