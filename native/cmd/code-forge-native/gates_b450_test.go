package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
)

// B450: every gates run form runs in Go. Each former Node handover case runs
// the public router in a child process (TestGatesProcess) with no Node at all
// (CODE_FORGE_NODE points nowhere, so a handover would exit 127).

type b450Fixture struct {
	t                *testing.T
	root, repo, home string
}

func b450Setup(t *testing.T, config any, blocks map[string]any) b450Fixture {
	t.Helper()
	root := t.TempDir()
	f := b450Fixture{t: t, root: root, repo: filepath.Join(root, "repo"), home: filepath.Join(root, "home")}
	f.must(os.MkdirAll(f.repo, 0755))
	f.must(os.MkdirAll(filepath.Join(f.home, ".code-forge", "runs"), 0700))
	data, err := json.Marshal(config)
	f.must(err)
	f.must(os.WriteFile(filepath.Join(f.repo, ".code-forge.yml"), data, 0600))
	if blocks == nil {
		blocks = map[string]any{"B1": map[string]any{"status": "open", "kind": "code", "owned_files": []string{}}}
	}
	record, err := json.Marshal(map[string]any{"run_id": "fixture", "workspace": f.repo, "status": "active", "project": "fixture", "blocks": blocks})
	f.must(err)
	f.must(os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "fixture.json"), record, 0600))
	return f
}

func (f b450Fixture) must(err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
}

func (f b450Fixture) write(name, body string, mode os.FileMode) {
	f.t.Helper()
	p := filepath.Join(f.repo, filepath.FromSlash(name))
	f.must(os.MkdirAll(filepath.Dir(p), 0755))
	f.must(os.WriteFile(p, []byte(body), mode))
}

func (f b450Fixture) git(args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = f.repo
	cmd.Env = gitx.ChildEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// call runs code-forge with args. env entries replace the defaults; "HOME="
// alone (no value) removes HOME.
func (f b450Fixture) call(env []string, args ...string) (int, string, string) {
	f.t.Helper()
	encoded, _ := json.Marshal(args)
	exe, err := os.Executable()
	f.must(err)
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.repo
	base := []string{}
	for _, kv := range envWithoutGit() {
		if !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "CODE_FORGE_TMP_ROOT=") {
			base = append(base, kv)
		}
	}
	extra := []string{"HOME=" + f.home}
	for _, kv := range env {
		if kv == "HOME=" {
			extra = extra[1:]
			continue
		}
		extra = append(extra, kv)
	}
	cmd.Env = append(append(base, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_NODE_VERSION=v24.22.1", "CF_GATES_ARGS="+string(encoded)), extra...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	code := 0
	if e, ok := err.(*exec.ExitError); ok {
		code = e.ExitCode()
	} else if err != nil {
		f.t.Fatal(err)
	}
	return code, stdout.String(), stderr.String()
}

type b450Result struct {
	AllOK   bool `json:"allOk"`
	Results []struct {
		Gate    string
		OK      bool
		Skipped bool
		Stdout  string
		Stderr  string
		Reason  string
	}
}

func (f b450Fixture) result(out string) b450Result {
	f.t.Helper()
	var r b450Result
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		f.t.Fatalf("stdout is not one JSON result: %v\n%s", err, out)
	}
	return r
}

func b450Green() map[string]any {
	return map[string]any{"gates": map[string]any{"test": []string{"/bin/sh", "-c", "printf green"}, "lint": nil, "types": nil, "format": nil, "rerun_failed": 0}}
}

// oneLine checks a refusal: the exit code, no stdout, and exactly one stderr
// line that holds want.
func b450OneLine(t *testing.T, code int, stdout, stderr string, wantCode int, want string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if code != wantCode || stdout != "" || len(lines) != 1 || !strings.Contains(stderr, want) {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want exit %d and one stderr line with %q", code, stdout, stderr, wantCode, want)
	}
}

// Node's os.homedir() falls back to the user database; an unscoped run with no
// --slug reads and writes nothing under the home folder.
func TestB450GatesRunHomeUnset(t *testing.T) {
	f := b450Setup(t, b450Green(), nil)
	code, out, errOut := f.call([]string{"HOME="}, "gates", "run")
	if code != 0 || !f.result(out).AllOK || errOut != "" {
		t.Fatalf("HOME unset: exit=%d stdout=%s stderr=%q, want a green run", code, out, errOut)
	}
}

func TestB450GatesRunUnscoped(t *testing.T) {
	f := b450Setup(t, b450Green(), nil)
	code, out, errOut := f.call(nil, "gates", "run", "--full", "--slug", "proj")
	if code != 0 {
		t.Fatalf("unscoped exit=%d stderr=%q", code, errOut)
	}
	r := f.result(out)
	if !r.AllOK || len(r.Results) != 4 || r.Results[0].Stdout != "green" {
		t.Fatalf("unscoped: want allOk and test stdout green; got %s", out)
	}
	rows, _ := filepath.Glob(filepath.Join(f.home, ".code-forge", "ledger", "*"))
	var text strings.Builder
	for _, p := range rows {
		b, err := os.ReadFile(p)
		f.must(err)
		text.Write(b)
	}
	lines := strings.Split(strings.TrimSpace(text.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("ledger rows = %q, want one gate.done row", text.String())
	}
	var row map[string]any
	f.must(json.Unmarshal([]byte(lines[0]), &row))
	if _, has := row["run"]; has {
		t.Fatalf("unscoped ledger row names a run: %s", lines[0])
	}
	if _, has := row["block"]; has {
		t.Fatalf("unscoped ledger row names a block: %s", lines[0])
	}
	if row["event"] != "gate.done" || row["all_ok"] != true {
		t.Fatalf("ledger row = %s, want gate.done all_ok", lines[0])
	}
}

func TestB450GatesRunTimeoutRadix(t *testing.T) {
	f := b450Setup(t, b450Green(), nil)
	for _, v := range []string{"0x186A0", "0o303240", "0b11000011010100000", " 100000 ", "1e5"} {
		code, out, errOut := f.call(nil, "gates", "run", "--run", "fixture", "--block", "B1", "--timeout-ms", v)
		if code != 0 || !f.result(out).AllOK {
			t.Fatalf("--timeout-ms %q: exit=%d stdout=%s stderr=%q, want a green run", v, code, out, errOut)
		}
	}
}

// Node's setTimeout turns a delay over 2^31-1 ms into 1 ms and warns.
func TestB450GatesRunTimeoutOverflow(t *testing.T) {
	config := b450Green()
	config["gates"].(map[string]any)["test"] = []string{"/bin/sh", "-c", "sleep 5"}
	f := b450Setup(t, config, nil)
	code, out, errOut := f.call(nil, "gates", "run", "--run", "fixture", "--block", "B1", "--timeout-ms", "2147483648")
	r := f.result(out)
	if code != 1 || r.AllOK || r.Results[0].Reason != "timeout" {
		t.Fatalf("overflow: exit=%d stdout=%s, want a red test gate with reason timeout", code, out)
	}
	if !strings.Contains(errOut, ") TimeoutOverflowWarning: 2147483648 does not fit into a 32-bit signed integer.\nTimeout duration was set to 1.\n") || !strings.HasPrefix(errOut, "(node:") {
		t.Fatalf("overflow stderr = %q, want Node's TimeoutOverflowWarning", errOut)
	}
}

func TestB450GatesRunUnsupportedFlagIgnored(t *testing.T) {
	f := b450Setup(t, b450Green(), nil)
	code, out, errOut := f.call(nil, "gates", "run", "--run", "fixture", "--block", "B1", "--bogus", "extra")
	if code != 0 || !f.result(out).AllOK {
		t.Fatalf("unknown flag: exit=%d stdout=%s stderr=%q, want a green run (Node ignores it)", code, out, errOut)
	}
}

// Node refuses an empty --run (assertRunId) and an empty --block (no such
// open block) with exit 1.
func TestB450GatesRunEmptyScope(t *testing.T) {
	f := b450Setup(t, b450Green(), nil)
	code, out, errOut := f.call(nil, "gates", "run", "--run", "", "--block", "B1")
	b450OneLine(t, code, out, errOut, 1, "gates run: run id must match /^[a-z0-9][a-z0-9-]{0,63}$/")
	code, out, errOut = f.call(nil, "gates", "run", "--run", "fixture", "--block", "")
	b450OneLine(t, code, out, errOut, 1, "gates run: block  is not open in run fixture")
}

// A --cwd that is not there has no config and no detected gate: every gate
// is skipped and the run is green, as in Node.
func TestB450GatesRunCwdNotAFolder(t *testing.T) {
	f := b450Setup(t, b450Green(), nil)
	missing := filepath.Join(f.root, "missing")
	code, out, errOut := f.call(nil, "gates", "run", "--cwd", missing)
	r := f.result(out)
	if code != 0 || !r.AllOK || len(r.Results) != 4 || !r.Results[0].Skipped {
		t.Fatalf("missing --cwd: exit=%d stdout=%s stderr=%q, want four skipped gates", code, out, errOut)
	}
}

func TestB450GatesRunConfigRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  map[string]any
		want string
	}{
		{"custom name", map[string]any{"custom": []any{map[string]any{"name": "Bad", "argv": []string{"/bin/true"}}}}, "gates run: gates.custom[0] (Bad).name must match ^[a-z][a-z0-9_-]{0,31}$"},
		{"custom token", map[string]any{"custom": []any{map[string]any{"name": "docs", "argv": []string{"/bin/echo", "{test_files}"}}}}, "gates run: gates.custom[0] (docs).argv: {test_files} and {changed_files} are refused in custom gates"},
		{"custom shape", map[string]any{"custom": "x"}, "gates run: gates.custom must be an array of at most 20 named object gates"},
		{"kinds unknown", map[string]any{"kinds": map[string]any{"code": []string{"nope"}}}, "gates run: gates.kinds.code: nope is an unknown gate name"},
		{"kinds null fixed", map[string]any{"kinds": map[string]any{"docs": []string{"lint"}}}, "gates run: gates.kinds.docs: lint is a null fixed gate"},
		{"rerun_failed", map[string]any{"rerun_failed": 3}, "gates run: gates.rerun_failed must be 0 or 1"},
		{"fixed shape", map[string]any{"lint": 5}, "gates run: gates.lint must be a non-empty argv array of strings, an {argv, env} object, or null"},
		{"fixed object argv", map[string]any{"lint": map[string]any{"argv": []any{}}}, "gates run: gates.lint.argv must be a non-empty argv array of strings"},
		{"fixed env value", map[string]any{"lint": map[string]any{"argv": []string{"/bin/true"}, "env": map[string]any{"A": 1}}}, "gates run: gates.lint.env.A must be a string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := b450Green()
			gates := config["gates"].(map[string]any)
			for k, v := range tc.set {
				gates[k] = v
			}
			f := b450Setup(t, config, nil)
			code, out, errOut := f.call(nil, "gates", "run", "--run", "fixture", "--block", "B1")
			b450OneLine(t, code, out, errOut, 1, tc.want)
		})
	}
}

func TestB450GatesRunCustomGate(t *testing.T) {
	config := b450Green()
	config["gates"].(map[string]any)["custom"] = []any{map[string]any{"name": "docs-check", "argv": []string{"/bin/sh", "-c", "printf custom-ok"}}}
	f := b450Setup(t, config, nil)
	code, out, errOut := f.call(nil, "gates", "run", "--run", "fixture", "--block", "B1")
	if code != 0 {
		t.Fatalf("custom gate exit=%d stderr=%q", code, errOut)
	}
	r := f.result(out)
	found := false
	for _, g := range r.Results {
		if g.Gate == "docs-check" && g.OK && g.Stdout == "custom-ok" {
			found = true
		}
	}
	if !r.AllOK || !found {
		t.Fatalf("custom gate: want docs-check green with stdout custom-ok; got %s", out)
	}
}

func TestB450GatesRunTmpRoot(t *testing.T) {
	config := b450Green()
	config["tmp"] = map[string]any{"root": "scratch"}
	config["gates"].(map[string]any)["test"] = []string{"/bin/sh", "-c", `printf %s "$CODE_FORGE_TMP_ROOT"`}
	f := b450Setup(t, config, nil)
	code, out, errOut := f.call(nil, "gates", "run", "--run", "fixture", "--block", "B1")
	if code != 0 {
		t.Fatalf("tmp.root exit=%d stderr=%q", code, errOut)
	}
	got := f.result(out).Results[0].Stdout
	want := filepath.Join(f.repo, "scratch")
	if got == "" || filepath.Base(got) != "scratch" {
		t.Fatalf("CODE_FORGE_TMP_ROOT=%q, want %q", got, want)
	}
	gotReal, _ := filepath.EvalSymlinks(filepath.Dir(got))
	wantReal, _ := filepath.EvalSymlinks(f.repo)
	if gotReal != wantReal {
		t.Fatalf("CODE_FORGE_TMP_ROOT=%q, want %q", got, want)
	}
}

func TestB450GatesRunRepeatNoBase(t *testing.T) {
	config := b450Green()
	config["gates"].(map[string]any)["test"] = []string{"/bin/sh", "-c", `printf '[%s]' "$@"`, "sh", "{test_files}"}
	config["gates"].(map[string]any)["repeat_new_tests"] = 2
	f := b450Setup(t, config, map[string]any{"B1": map[string]any{"status": "open", "kind": "code", "owned_files": []string{"test/a.test.mjs"}}})
	f.write("test/a.test.mjs", "export {};\n", 0644)
	f.git("init", "-q")
	code, out, errOut := f.call(nil, "gates", "run", "--run", "fixture", "--block", "B1")
	b450OneLine(t, code, out, errOut, 1, "gates run: repeat_new_tests: the block has no recorded base commit (base_sha), so its new tests are unknown; drop gates.repeat_new_tests or reopen the block; no gate was run")
}

func TestB450GatesRunRepeatChangedSource(t *testing.T) {
	config := b450Green()
	config["gates"].(map[string]any)["test"] = []string{"/bin/sh", "-c", `printf '[%s]' "$@"`, "sh", "{test_files}"}
	config["gates"].(map[string]any)["repeat_new_tests"] = 2
	f := b450Setup(t, config, nil)
	f.write("src/a.mjs", "export const a = 1;\n", 0644)
	f.write("test/a.test.mjs", "import { a } from '../src/a.mjs';\n", 0644)
	f.git("init", "-q")
	f.git("add", ".")
	f.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	base := f.git("rev-parse", "HEAD")
	f.write("src/a.mjs", "export const a = 2;\n", 0644)
	record, _ := json.Marshal(map[string]any{"run_id": "fixture", "workspace": f.repo, "status": "active", "project": "fixture", "blocks": map[string]any{
		"B1": map[string]any{"status": "open", "kind": "code", "base_sha": base, "owned_files": []string{"src/a.mjs", "test/a.test.mjs"}}}})
	f.must(os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "fixture.json"), record, 0600))
	f.must(os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "fixture.key"), bytes.Repeat([]byte{7}, 32), 0600))
	code, out, errOut := f.call(nil, "gates", "run", "--run", "fixture", "--block", "B1")
	if code != 0 {
		t.Fatalf("repeat with a changed owned source: exit=%d stdout=%s stderr=%q, want a green run", code, out, errOut)
	}
	r := f.result(out)
	if !r.AllOK || !strings.Contains(r.Results[0].Stdout, "test/a.test.mjs") {
		t.Fatalf("repeat with a changed owned source: want test/a.test.mjs run; got %s", out)
	}
	ledgerRows, _ := filepath.Glob(filepath.Join(f.home, ".code-forge", "ledger", "*"))
	signed := false
	for _, p := range ledgerRows {
		b, err := os.ReadFile(p)
		f.must(err)
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var row map[string]any
			if json.Unmarshal([]byte(line), &row) == nil && row["event"] == "gate.done" && row["run"] == "fixture" && row["block"] == "B1" && row["mac"] != nil {
				signed = true
			}
		}
	}
	if !signed {
		t.Fatalf("repeat run: want a signed gate.done row for fixture/B1 in the ledger; files %v", ledgerRows)
	}
}

func TestB450GatesRunFormatter(t *testing.T) {
	config := map[string]any{"gates": map[string]any{"test": nil, "lint": nil, "types": nil, "rerun_failed": 0}}
	f := b450Setup(t, config, nil)
	f.write("package.json", `{"name":"x","scripts":{"test":"x"},"devDependencies":{"prettier":"3"}}`, 0644)
	f.write("a.js", "x\n", 0644)
	f.write("notes.txt", "x\n", 0644)
	f.write("ignored.js", "x\n", 0644)
	f.write(".gitignore", "ignored.js\n", 0644)
	f.git("init", "-q")
	f.git("add", "-f", ".")
	bin := filepath.Join(f.root, "bin")
	f.must(os.MkdirAll(bin, 0755))
	f.must(os.WriteFile(filepath.Join(bin, "npx"), []byte("#!/bin/sh\nprintf '%s|' \"$@\"\n"), 0755))
	code, out, errOut := f.call([]string{"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH")}, "gates", "run")
	if code != 0 {
		t.Fatalf("formatter exit=%d stdout=%s stderr=%q", code, out, errOut)
	}
	r := f.result(out)
	var format string
	for _, g := range r.Results {
		if g.Gate == "format" {
			format = g.Stdout
		}
	}
	if want := "prettier|--check|--ignore-unknown|./.code-forge.yml|./a.js|./package.json|"; format != want {
		t.Fatalf("formatter argv = %q, want %q; got %s", format, want, out)
	}
}

func TestB450GatesRunMissingCommandIsRed(t *testing.T) {
	config := b450Green()
	config["gates"].(map[string]any)["test"] = []string{"/nonexistent/b450-command"}
	f := b450Setup(t, config, nil)
	code, out, errOut := f.call(nil, "gates", "run", "--run", "fixture", "--block", "B1")
	if code != 1 {
		t.Fatalf("missing command exit=%d stdout=%s stderr=%q, want 1", code, out, errOut)
	}
	r := f.result(out)
	if r.AllOK || r.Results[0].Gate != "test" || r.Results[0].OK || r.Results[0].Reason != "spawn-error" {
		t.Fatalf("missing command: want a red test gate with reason spawn-error; got %s", out)
	}
}

// The land batch preflight runs the Go step: a custom gate passes, a refused
// config fails, and no handover is asked for.
func TestB450LandBatchGateScope(t *testing.T) {
	config := b450Green()
	config["gates"].(map[string]any)["custom"] = []any{map[string]any{"name": "docs-check", "argv": []string{"/bin/true"}}}
	f := b450Setup(t, config, nil)
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-code-forge-node")
	options := gatesOptions{cwd: f.repo, run: "fixture", block: "B1"}
	if !landBatchGateScope([]string{"gates", "run", "--run", "fixture", "--block", "B1"}, options) {
		t.Fatal("landBatchGateScope = false for a custom gate config, want true")
	}
	config["gates"].(map[string]any)["custom"] = []any{map[string]any{"name": "Bad", "argv": []string{"/bin/true"}}}
	data, _ := json.Marshal(config)
	f.must(os.WriteFile(filepath.Join(f.repo, ".code-forge.yml"), data, 0600))
	if landBatchGateScope([]string{"gates", "run", "--run", "fixture", "--block", "B1"}, options) {
		t.Fatal("landBatchGateScope = true for a refused custom gate, want false")
	}
}

// Node completes a run record whose run_id differs from --run or that has no
// workspace string; Go does not port these yet, so they keep the Node
// hand-over (exit 127 here: the fixture's Node does not exist).
func TestB450GatesRunKeepsRecordHandover(t *testing.T) {
	for name, record := range map[string]map[string]any{
		"run_id mismatch": {"run_id": "other", "workspace": "", "status": "active", "project": "fixture", "blocks": map[string]any{"B1": map[string]any{"status": "open"}}},
		"no workspace":    {"run_id": "fixture", "status": "active", "project": "fixture", "blocks": map[string]any{"B1": map[string]any{"status": "open"}}},
	} {
		t.Run(name, func(t *testing.T) {
			f := b450Setup(t, b450Green(), nil)
			if record["workspace"] == "" {
				record["workspace"] = f.repo
			}
			data, err := json.Marshal(record)
			f.must(err)
			f.must(os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "fixture.json"), data, 0600))
			code, out, errOut := f.call(nil, "gates", "run", "--run", "fixture", "--block", "B1")
			if code != 127 || out != "" || !strings.Contains(errOut, "/nonexistent-code-forge-node") {
				t.Fatalf("exit=%d stdout=%q stderr=%q, want the Node hand-over (exit 127 naming the missing Node)", code, out, errOut)
			}
		})
	}
}
