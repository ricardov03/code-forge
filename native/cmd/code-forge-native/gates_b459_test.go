package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B459: the gates subcommands other than run answer in Go. Every expected
// stdout/stderr below was taken from Node (CODE_FORGE_NATIVE=0) on the same
// input. A Node hand-over shows as exit 127 here (the fixture's Node does not
// exist) with its reason in the hand-over log.

func b459Setup(t *testing.T) b450Fixture {
	t.Helper()
	f := b450Setup(t, map[string]any{}, nil)
	f.must(os.Remove(filepath.Join(f.repo, ".code-forge.yml")))
	return f
}

// b459Native checks a Go answer: exit, stdout, stderr exact, and no hand-over logged.
func b459Native(t *testing.T, f b450Fixture, code int, stdout, stderr string, wantCode int, wantOut, wantErr string) {
	t.Helper()
	if code != wantCode || stdout != wantOut || stderr != wantErr {
		t.Fatalf("exit=%d stdout=%q stderr=%q\nwant exit=%d stdout=%q stderr=%q", code, stdout, stderr, wantCode, wantOut, wantErr)
	}
	if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl")); !os.IsNotExist(err) {
		data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
		t.Fatalf("a hand-over was logged: %s", data)
	}
}

// b459Handover checks a kept Node hand-over: exit 127 naming the missing Node, no stdout, and the reason logged.
func b459Handover(t *testing.T, f b450Fixture, code int, stdout, stderr, reason string) {
	t.Helper()
	if code != 127 || stdout != "" || !strings.Contains(stderr, "/nonexistent-code-forge-node") {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want the Node hand-over", code, stdout, stderr)
	}
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
	f.must(err)
	var row struct{ Verb, Reason string }
	f.must(json.Unmarshal([]byte(strings.Split(strings.TrimSpace(string(data)), "\n")[0]), &row))
	if row.Verb != "gates" || row.Reason != reason {
		t.Fatalf("hand-over row %s, want verb gates reason %s", data, reason)
	}
}

func TestB459GatesHelpAndUsage(t *testing.T) {
	for _, c := range []struct {
		args     []string
		code     int
		out, err string
	}{
		{[]string{"--help"}, 0, gatesUsage, ""},
		{[]string{"-h", "detect"}, 0, gatesUsage, ""},
		{[]string{"run", "--help"}, 0, "usage: code-forge gates run [--cwd <dir>] [--timeout-ms <n>] [--slug <slug>] [--run <id>] [--block <id>] [--changed | --full] [--auto-rebase | --allow-stale-base] [--preflight | --no-preflight]\n", ""},
		{[]string{"run", "-h", "--run", "x"}, 0, gatesSubUsage["run"], ""},
		{[]string{"detect", "--help"}, 0, "usage: code-forge gates detect [--cwd <dir>]\n", ""},
		{[]string{"secret-scan", "-h"}, 0, "usage: code-forge gates secret-scan [--cwd <dir>] --file <path> [--file <path>...]\n", ""},
		{[]string{"safe-edit", "--help"}, 0, "usage: code-forge gates safe-edit [--cwd <dir>] --base <sha>\n", ""},
		{[]string{"scope", "--help"}, 0, "usage: code-forge gates scope [--cwd <dir>] --base <sha>\n", ""},
		{[]string{"acceptance", "--help"}, 0, "usage: code-forge gates acceptance --clauses <file.json> --tap <file>\n", ""},
		{[]string{"transcript-grep", "--help"}, 0, "usage: code-forge gates transcript-grep --file <path> [--cwd <dir>]\n", ""},
		{[]string{}, 2, "", gatesUsage},
		{[]string{"help"}, 2, "", gatesUsage},
		{[]string{"bogus", "--help"}, 2, "", gatesUsage},
		{[]string{"--cwd", ".", "detect"}, 2, "", gatesUsage},
		{[]string{"detect", "--cwd"}, 2, "", "gates detect: --cwd requires a value\n"},
		{[]string{"scope", "--cwd", "--base"}, 2, "", "gates scope: --cwd requires a value\n"},
		{[]string{"secret-scan"}, 2, "", "gates secret-scan: at least one --file <path> is required\n"},
		{[]string{"secret-scan", "--file"}, 2, "", "gates secret-scan: at least one --file <path> is required\n"},
		{[]string{"safe-edit"}, 2, "", "gates safe-edit: --base <sha> is required\n"},
		{[]string{"scope", "--base", "--x"}, 2, "", "gates scope: --base <sha> is required\n"},
		{[]string{"acceptance", "--clauses", "x"}, 2, "", "gates acceptance: --clauses <file.json> and --tap <file> are required\n"},
		{[]string{"transcript-grep"}, 2, "", "gates transcript-grep: --file <path> is required\n"},
		{[]string{"transcript-grep", "--file", "x", "--cwd"}, 2, "", "gates transcript-grep: --cwd requires a value\n"},
	} {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			f := b459Setup(t)
			code, out, errOut := f.call(nil, append([]string{"gates"}, c.args...)...)
			b459Native(t, f, code, out, errOut, c.code, c.out, c.err)
		})
	}
}

// The error log row and breadcrumbs match Node's for a gates usage error.
func TestB459GatesErrorLogAndCrumbs(t *testing.T) {
	f := b459Setup(t)
	f.call([]string{"CODE_FORGE_NO_ERROR_LOG="}, "gates", "detect", "--cwd", ".")
	code, _, _ := f.call([]string{"CODE_FORGE_NO_ERROR_LOG="}, "gates", "scope")
	if code != 2 {
		t.Fatalf("scope exit=%d, want 2", code)
	}
	crumbs, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "logs", "breadcrumbs.json"))
	f.must(err)
	if string(crumbs) != `["gates detect","gates scope"]`+"\n" {
		t.Fatalf("breadcrumbs %s", crumbs)
	}
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "logs", "errors.jsonl"))
	f.must(err)
	var row map[string]any
	f.must(json.Unmarshal(data, &row))
	if row["verb"] != "gates" || row["sub"] != "scope" || row["exit"] != float64(2) || row["kind"] != "usage" ||
		row["message"] != "gates scope: --base <sha> is required" || row["fp"] != "7ae65bb323f3" {
		t.Fatalf("error row %s", data)
	}
	if before, _ := json.Marshal(row["before"]); string(before) != `["gates detect"]` {
		t.Fatalf("before %s", before)
	}
}

func TestB459GatesDetect(t *testing.T) {
	const prettier = `"format":{"argv":["npx","prettier","--check","--ignore-unknown"],"formatter":"prettier"}`
	for _, c := range []struct {
		name, config, pkg string
		code              int
		out, err          string
	}{
		{"env names and order", "gates:\n  lint: [eslint, .]\n  test:\n    argv: [npm, test]\n    env:\n      ZED: \"1\"\n      ALPHA: \"2\"\n", "",
			0, `{"stack":"node","test":{"argv":["npm","test"],"env_keys":["ALPHA","ZED"]},"lint":["eslint","."],"types":null,` + prettier + `,"rerun_failed":1}` + "\n", ""},
		{"custom env", "gates:\n  custom:\n    - name: lic\n      argv: [./lic.sh]\n      env: {B: x, A: y}\n", "",
			0, `{"stack":"node","test":["npm","test"],"lint":null,"types":null,` + prettier + `,"custom":[{"name":"lic","argv":["./lic.sh"],"env_keys":["A","B"]}],"rerun_failed":1}` + "\n", ""},
		{"rerun 0 and null", "gates:\n  rerun_failed: 0\n  format: null\n  test: [a]\n", "",
			0, `{"stack":"node","test":["a"],"lint":null,"types":null,"format":null,"rerun_failed":0}` + "\n", ""},
		{"gates list", "gates: [1, 2]\n", "",
			0, `{"stack":"node","test":["npm","test"],"lint":null,"types":null,` + prettier + `,"rerun_failed":1}` + "\n", ""},
		{"bad rerun", "gates:\n  rerun_failed: 2\n", "", 1, "", "gates detect: gates.rerun_failed must be 0 or 1\n"},
		{"bad custom", "gates:\n  custom: 5\n", "", 1, "", "gates detect: gates.custom must be an array of at most 20 named object gates\n"},
		{"no gates", "version: 1\n", "",
			0, `{"stack":"node","test":["npm","test"],"lint":null,"types":null,` + prettier + `}` + "\n", ""},
		{"prettier 0 is no dependency", "", `{"scripts":{"test":"x"},"devDependencies":{"prettier":0}}`,
			0, `{"stack":"node","test":["npm","test"],"lint":null,"types":null,"format":null}` + "\n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := b459Setup(t)
			pkg := c.pkg
			if pkg == "" {
				pkg = `{"name":"x","scripts":{"test":"node --test"},"devDependencies":{"prettier":"1"}}`
			}
			f.write("package.json", pkg, 0644)
			f.write("package-lock.json", "{}", 0644)
			if c.config != "" {
				f.write(".code-forge.yml", c.config, 0644)
			}
			code, out, errOut := f.call(nil, "gates", "detect")
			b459Native(t, f, code, out, errOut, c.code, c.out, c.err)
		})
	}
	t.Run("empty cwd", func(t *testing.T) {
		f := b459Setup(t)
		code, out, errOut := f.call(nil, "gates", "detect", "--cwd", "")
		b459Native(t, f, code, out, errOut, 1, "", "gates detect: detectGates: root must be a non-empty string\n")
	})
}

func TestB459GatesSecretScan(t *testing.T) {
	f := b459Setup(t)
	f.write("sec.txt", "key sk-abcdefghijklmnop here\nfake sk-fakefakefakefake\nxsk-abcdefghijklmnopq\nAKIAABCDEFGHIJKLMN AKIAABCDEFGHIJKLMN\n", 0644)
	f.write("clean.txt", "nothing here\n", 0644)
	f.must(os.Mkdir(filepath.Join(f.repo, "dir"), 0755))
	code, out, errOut := f.call(nil, "gates", "secret-scan", "--file", "sec.txt", "--file", "missing", "--file", "sec.txt")
	b459Native(t, f, code, out, errOut, 1,
		`{"hits":[{"path":"sec.txt","line":1,"count":1},{"path":"sec.txt","line":4,"count":2},{"path":"sec.txt","line":1,"count":1},{"path":"sec.txt","line":4,"count":2}],"count":4}`+"\n", "")
	code, out, errOut = f.call(nil, "gates", "secret-scan", "--file", "clean.txt")
	b459Native(t, f, code, out, errOut, 0, `{"hits":[],"count":0}`+"\n", "")
	code, out, errOut = f.call(nil, "gates", "secret-scan", "--file", "dir")
	b459Native(t, f, code, out, errOut, 1, "", "gates secret-scan: EISDIR: illegal operation on a directory, read\n")
}

func b459Repo(t *testing.T) b450Fixture {
	t.Helper()
	f := b459Setup(t)
	f.write("src/a.test.mjs", "test(\"a\", () => {})\ntest(\"b\", () => {})\nit (\"c\", ()=>{})\ntest(\"d\",()=>{})\ntest(\"e\",()=>{})\n", 0644)
	f.write("x_test.go", "package x\nfunc TestA(t *testing.T){}\nfunc TestB(t *testing.T){}\n", 0644)
	f.write("README", "x\n", 0644)
	f.git("init", "-q")
	f.git("add", "-A")
	f.git("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init")
	return f
}

func TestB459GatesSafeEdit(t *testing.T) {
	f := b459Repo(t)
	code, out, errOut := f.call(nil, "gates", "safe-edit", "--base", "HEAD")
	b459Native(t, f, code, out, errOut, 0, `{"ok":true}`+"\n", "")
	f.write("src/a.test.mjs", "test(\"a\", () => {})\n", 0644)
	code, out, errOut = f.call(nil, "gates", "safe-edit", "--base", "HEAD")
	b459Native(t, f, code, out, errOut, 1, `{"ok":false,"reason":"test count shrank from 7 to 3 (more than 20%)"}`+"\n", "")
	f.must(os.Remove(filepath.Join(f.repo, "x_test.go")))
	code, out, errOut = f.call(nil, "gates", "safe-edit", "--base", "HEAD")
	b459Native(t, f, code, out, errOut, 1, `{"ok":false,"reason":"deleted test file(s): x_test.go"}`+"\n", "")
	code, out, errOut = f.call(nil, "gates", "safe-edit", "--base", "nope")
	b459Native(t, f, code, out, errOut, 1, "", "gates safe-edit: safe-edit: git ls-tree -r -z --name-only nope failed (exit 128): fatal: Not a valid object name nope\n\n")
	code, out, errOut = f.call(nil, "gates", "safe-edit", "--base", "-x")
	b459Native(t, f, code, out, errOut, 1, "", "gates safe-edit: safe-edit: base must be a non-empty string not starting with \"-\" (got \"-x\")\n")
	code, out, errOut = f.call(nil, "gates", "safe-edit", "--base", "HEAD", "--cwd", filepath.Join(f.root, "nonexist"))
	b459Native(t, f, code, out, errOut, 1, "", "gates safe-edit: safe-edit: git ls-tree -r -z --name-only HEAD failed (exit null): \n")
}

func TestB459GatesScope(t *testing.T) {
	f := b459Repo(t)
	f.write("src/a.test.mjs", "changed\n", 0644)
	f.write("new file.txt", "n\n", 0644)
	f.write("d/ü.txt", "u\n", 0644)
	code, out, errOut := f.call(nil, "gates", "scope", "--base", "HEAD")
	b459Native(t, f, code, out, errOut, 0, `{"tracked":["src/a.test.mjs"],"untracked":["d/ü.txt","new file.txt"],"all":["d/ü.txt","new file.txt","src/a.test.mjs"]}`+"\n", "")
	code, out, errOut = f.call(nil, "gates", "scope", "--base", "-x")
	b459Native(t, f, code, out, errOut, 1, "", "gates scope: base must be a non-empty string not starting with \"-\" (got \"-x\") — option injection\n")
	code, out, errOut = f.call(nil, "gates", "scope", "--base", "HEAD", "--cwd", filepath.Join(f.root, "nonexist"))
	b459Native(t, f, code, out, errOut, 1, "", "gates scope: trackedChangedFiles: git diff --name-only -z HEAD failed (exit null): \n")
}

func TestB459GatesAcceptance(t *testing.T) {
	const tap = "TAP version 13\nok 1 - alpha\nok 2 - beta # SKIP x\nnot ok 3 - gamma\n  ok 4 - delta  \nok 5 - eps # todo\nok 6 -   spaced name   \n"
	for _, c := range []struct {
		clauses string
		code    int
		out     string
	}{
		{`[{"clause":"A","tests":["alpha","delta","spaced name"]}]`, 0, `{"ok":true}`},
		{`[{"clause":"A","tests":["beta"]}]`, 1, `{"ok":false,"reason":"clause \"A\" names test \"beta\", which did not exist or did not pass in this gate run","clause":"A","test":"beta"}`},
		{`[{"clause":"A","tests":["gamma"]}]`, 1, `{"ok":false,"reason":"clause \"A\" names test \"gamma\", which did not exist or did not pass in this gate run","clause":"A","test":"gamma"}`},
		{`{}`, 1, `{"ok":false,"reason":"no acceptance clauses given","clause":"","test":""}`},
		{`[{"clause":"H","kind":"human_check","tests":[]}]`, 1, `{"ok":false,"reason":"human_check clause must not name tests","clause":"H","test":""}`},
		{`[{"clause":[1,null,[2]],"tests":["alpha"]}]`, 1, `{"ok":false,"reason":"clause names no test id","clause":"1,,2","test":""}`},
		{`[{"clause":"A","tests":[{"a":1}]}]`, 1, `{"ok":false,"reason":"clause \"A\" names test \"[object Object]\", which did not exist or did not pass in this gate run","clause":"A","test":{"a":1}}`},
		{`[{"clause":"A","tests":[1.5e300]}]`, 1, `{"ok":false,"reason":"clause \"A\" names test \"1.5e+300\", which did not exist or did not pass in this gate run","clause":"A","test":1.5e+300}`},
	} {
		t.Run(c.clauses, func(t *testing.T) {
			f := b459Setup(t)
			f.write("c.json", c.clauses, 0644)
			f.write("t.tap", tap, 0644)
			code, out, errOut := f.call(nil, "gates", "acceptance", "--clauses", "c.json", "--tap", "t.tap")
			b459Native(t, f, code, out, errOut, c.code, c.out+"\n", "")
		})
	}
	f := b459Setup(t)
	code, out, errOut := f.call(nil, "gates", "acceptance", "--clauses", "missing.json", "--tap", "t.tap")
	b459Native(t, f, code, out, errOut, 1, "", "gates acceptance: ENOENT: no such file or directory, open 'missing.json'\n")
}

func TestB459GatesTranscriptGrep(t *testing.T) {
	f := b459Setup(t)
	f.write("tr.txt", "run git push --force origin main\nthen gh pr merge 3 && rm -rf /x\ncode-forge block waive B1\ngit stash\n", 0644)
	code, out, errOut := f.call(nil, "gates", "transcript-grep", "--file", "tr.txt")
	b459Native(t, f, code, out, errOut, 1, `{"ok":false,"hits":[{"id":"git-push-force","line":1},{"id":"gh-pr-merge","line":2},{"id":"rm-rf","line":2},{"id":"git-stash","line":4}]}`+"\n", "")
	f.write("ok.txt", "git status\n", 0644)
	code, out, errOut = f.call(nil, "gates", "transcript-grep", "--file", "ok.txt", "--cwd", ".")
	b459Native(t, f, code, out, errOut, 0, `{"ok":true,"hits":[]}`+"\n", "")
	code, out, errOut = f.call(nil, "gates", "transcript-grep", "--file", "missing")
	b459Native(t, f, code, out, errOut, 1, "", "gates transcript-grep: ENOENT: no such file or directory, open 'missing'\n")
}

// The cases Go does not port keep the Node hand-over, each with its own reason.
func TestB459GatesKeptHandovers(t *testing.T) {
	t.Run("acceptance JSON.parse error", func(t *testing.T) {
		f := b459Setup(t)
		f.write("c.json", "nope", 0644)
		f.write("t.tap", "", 0644)
		code, out, errOut := f.call(nil, "gates", "acceptance", "--clauses", "c.json", "--tap", "t.tap")
		b459Handover(t, f, code, out, errOut, "gates-acceptance-json")
	})
	t.Run("tap not UTF-8", func(t *testing.T) {
		f := b459Setup(t)
		f.write("c.json", "[]", 0644)
		f.write("t.tap", "ok 1 - \xff\n", 0644)
		code, out, errOut := f.call(nil, "gates", "acceptance", "--clauses", "c.json", "--tap", "t.tap")
		b459Handover(t, f, code, out, errOut, "gates-tap-encoding")
	})
	t.Run("transcript production markers", func(t *testing.T) {
		f := b459Setup(t)
		f.write(".code-forge.yml", "production:\n  markers: [PROD]\n", 0644)
		f.write("tr.txt", "echo PROD\n", 0644)
		code, out, errOut := f.call(nil, "gates", "transcript-grep", "--file", "tr.txt", "--cwd", ".")
		b459Handover(t, f, code, out, errOut, "gates-transcript-extras")
	})
	// A Codex exec --json log runs in Go since B466 (gatecheck reads it), with
	// Node's output: {"ok":true,"hits":[]} and exit 0 for this input.
	t.Run("transcript codex log", func(t *testing.T) {
		f := b459Setup(t)
		f.write("tr.txt", "{\"type\":\"thread.started\"}\n", 0644)
		code, out, errOut := f.call(nil, "gates", "transcript-grep", "--file", "tr.txt")
		if code != 0 || out != "{\"ok\":true,\"hits\":[]}\n" || errOut != "" {
			t.Fatalf("exit=%d stdout=%q stderr=%q, want Node's {\"ok\":true,\"hits\":[]} exit 0", code, out, errOut)
		}
	})
	for name, c := range map[string]struct {
		record map[string]any
		reason string
	}{
		"run_id mismatch": {map[string]any{"run_id": "other", "status": "active", "project": "fixture", "blocks": map[string]any{"B1": map[string]any{"status": "open"}}}, "gates-run-id-mismatch"},
		"no workspace":    {map[string]any{"run_id": "fixture", "status": "active", "project": "fixture", "blocks": map[string]any{"B1": map[string]any{"status": "open"}}}, "gates-run-no-workspace"},
	} {
		t.Run(name, func(t *testing.T) {
			f := b450Setup(t, b450Green(), nil)
			if c.reason == "gates-run-id-mismatch" {
				c.record["workspace"] = f.repo
			}
			data, err := json.Marshal(c.record)
			f.must(err)
			f.must(os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "fixture.json"), data, 0600))
			code, out, errOut := f.call(nil, "gates", "run", "--run", "fixture", "--block", "B1")
			b459Handover(t, f, code, out, errOut, c.reason)
		})
	}
}
