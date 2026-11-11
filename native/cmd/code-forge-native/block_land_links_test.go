package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

type androidLandFixture struct {
	repo, tree, home, base string
	cfg                    map[string]any
}

func androidLand(t *testing.T) androidLandFixture {
	t.Helper()
	f := androidLandFixture{repo: t.TempDir(), home: t.TempDir()}
	f.repo, _ = filepath.EvalSymlinks(f.repo)
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_NATIVE", "1")
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	old := reviewWaitNodeVersion
	reviewWaitNodeVersion = "v24.22.1"
	t.Cleanup(func() { reviewWaitNodeVersion = old })
	bin := t.TempDir()
	landWrite(t, filepath.Join(bin, "gradle"), "#!/bin/sh\n[ -f local.properties ] || { echo 'Android SDK not found'; exit 1; }\n[ -f app/google-services.json ] || { echo 'google-services.json not found'; exit 1; }\n[ -f prepared ] || { echo 'setup did not run'; exit 1; }\n[ -L local.properties ] && kind=link || kind=copy\nprintf '%s %s\\n' \"$PWD\" \"$kind\" >> \"$HOME/suite-calls\"\n")
	if err := os.Chmod(filepath.Join(bin, "gradle"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	landGit(t, f.repo, "init", "-qb", "main")
	landGit(t, f.repo, "config", "user.name", "Fixture")
	landGit(t, f.repo, "config", "user.email", "f@example.invalid")
	landGit(t, f.repo, "config", "commit.gpgsign", "false")
	landWrite(t, filepath.Join(f.repo, ".gitignore"), ".code-forge/\nlocal.properties\napp/google-services.json\nprepared\n")
	landWrite(t, filepath.Join(f.repo, "sample.mjs"), "old\n")
	f.cfg = landTestConfig([]string{"gradle"})
	f.saveConfig(t)
	landGit(t, f.repo, "add", ".")
	landGit(t, f.repo, "commit", "-qm", "base")
	f.base = landGit(t, f.repo, "rev-parse", "HEAD")
	f.tree = filepath.Join(t.TempDir(), "block")
	landGit(t, f.repo, "worktree", "add", "-qb", "block-B1", f.tree, f.base)
	landWrite(t, filepath.Join(f.tree, "sample.mjs"), "new\n")
	landGit(t, f.tree, "commit", "-qam", "change")
	landWrite(t, filepath.Join(f.repo, "local.properties"), "sdk.dir=private-sdk\n")
	landWrite(t, filepath.Join(f.repo, "app/google-services.json"), "private-google-config\n")
	return f
}

func landWrite(t *testing.T, name, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func landGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitx.Run(dir, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(out)
}

func (f androidLandFixture) saveConfig(t *testing.T) {
	t.Helper()
	data, err := json.Marshal(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	landWrite(t, filepath.Join(f.repo, config.Filename), string(data))
}

func (f androidLandFixture) commitConfig(t *testing.T) {
	t.Helper()
	f.saveConfig(t)
	landGit(t, f.repo, "add", config.Filename)
	landGit(t, f.repo, "commit", "-qm", "gate config")
}

func TestLandAndroidIgnoredFiles(t *testing.T) {
	f := androidLand(t)
	red := landMergeGate(f.home, "r-android-links", "B1", f.repo, f.tree, f.repo, "block-B1", "main")
	if !strings.Contains(red.reason, "Android SDK not found") || strings.Count(red.reason, "gates.merge_gate_links") != 1 {
		t.Errorf("unconfigured gate must fail with SDK error and exactly one links hint: %q", red.reason)
	}
	gates := f.cfg["gates"].(map[string]any)
	gates["merge_gate_links"] = []string{"local.properties", "app/google-services.json", "missing", "missing"}
	gates["merge_gate_setup"] = []string{"/bin/sh", "-c", "test -f local.properties && test -f app/google-services.json && touch prepared"}
	f.commitConfig(t)
	capture, err := os.CreateTemp(f.home, "warnings-")
	if err != nil {
		t.Fatal(err)
	}
	stderr := os.Stderr
	os.Stderr = capture
	green := landMergeGate(f.home, "r-android-links", "B1", f.repo, f.tree, f.repo, "block-B1", "main")
	os.Stderr = stderr
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	warnings, err := os.ReadFile(capture.Name())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(warnings), "source missing: missing"); got != 1 {
		t.Errorf("missing duplicate source must warn exactly once, got %d: %s", got, warnings)
	}
	if strings.Contains(string(warnings), "private-sdk") || strings.Contains(string(warnings), "private-google-config") {
		t.Fatal("warnings printed ignored contents")
	}
	if green.reason != "" {
		t.Fatalf("linked Android gate must pass: %s", green.reason)
	}
	calls, err := os.ReadFile(filepath.Join(f.home, "suite-calls"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
	if len(lines) != 1 || !strings.HasSuffix(lines[0], " link") || !strings.Contains(lines[0], ".merge-gate-") {
		t.Fatalf("want exactly one linked suite in temporary tree, got %q", calls)
	}
	if strings.Contains(landGit(t, f.repo, "worktree", "list", "--porcelain"), ".merge-gate-") {
		t.Fatal("want zero temporary worktrees after gate")
	}
}

func TestLandLinksRefusedAtConfigLoad(t *testing.T) {
	f := androidLand(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(f.repo, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../x", "app/../x", outside, "C:/outside", "escape/missing", "sample.mjs", "."} {
		t.Run(name, func(t *testing.T) {
			f.cfg["gates"].(map[string]any)["merge_gate_links"] = []string{name}
			f.saveConfig(t)
			_, err := config.Load(f.repo)
			if err == nil || !strings.Contains(err.Error(), "gates.merge_gate_links") {
				t.Fatalf("config must refuse unsafe or tracked link %q with key named, got %v", name, err)
			}
		})
	}
}

func TestLandClosedUnmergedRetry(t *testing.T) {
	f := androidLand(t)
	const run = "r-android-retry"
	entry := ledger.Object{{Key: "status", Value: "closed"}, {Key: "owned_files", Value: []any{"sample.mjs"}}, {Key: "base_branch", Value: "main"}, {Key: "worktree", Value: ledger.Object{{Key: "path", Value: f.tree}, {Key: "branch", Value: "block-B1"}, {Key: "base_sha", Value: f.base}}}}
	record := ledger.Object{{Key: "run_id", Value: run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: f.repo}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}}
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	landWrite(t, filepath.Join(f.home, ".code-forge/runs", run+".key"), strings.Repeat("0", 32))
	flags := map[string]any{"report": "unused", "transcript": "unused", "merge": true, "copy-links": true}
	land := func() (int, string) {
		t.Helper()
		capture, err := os.CreateTemp(f.home, "output-")
		if err != nil {
			t.Fatal(err)
		}
		out, stderr := os.Stdout, os.Stderr
		os.Stdout, os.Stderr = capture, capture
		code := landExecuteWithReason(nil, flags, f.home, run, "B1", record, entry, func(reason handoverReason) int { t.Errorf("retry handed over: %v", reason); return 127 })
		os.Stdout, os.Stderr = out, stderr
		capture.Close()
		data, err := os.ReadFile(capture.Name())
		if err != nil {
			t.Fatal(err)
		}
		return code, string(data)
	}
	before := landGit(t, f.repo, "rev-parse", "HEAD")
	code, output := land()
	if code != 1 || !strings.Contains(output, "gates.merge_gate_links") || landGit(t, f.repo, "rev-parse", "HEAD") != before {
		t.Errorf("first retry must fail only at Android gate and keep main unchanged: exit=%d output=%q", code, output)
	}
	// The existing Node module is still reachable through native handover.
	// Exercise its closed retry with an in-process fake gate dispatcher that
	// executes the same reporter-shaped gradle fixture in the supplied tree.
	fallback := func() (int, string) {
		t.Helper()
		module, err := filepath.Abs("../../../src/cli/land.mjs")
		if err != nil {
			t.Fatal(err)
		}
		script := `
import {pathToFileURL} from 'node:url';
import {spawnSync} from 'node:child_process';
const {landBlock} = await import(pathToFileURL(process.argv[1]));
const [repo, tree] = process.argv.slice(2);
const unexpected = async () => { throw new Error('closed retry repeated join or close'); };
const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('GIT_')));
const code = await landBlock({id:'B1', runId:'r-fallback', record:{workspace:repo, project:'fixture', blocks:{B1:{status:'closed', base_branch:'main'}}}, report:'unused', transcript:'unused', maxMs:60000, merge:true, noMergeGate:false, json:false}, {
  autoClaimChanges:unexpected, runReviewFile:unexpected, runBlock:unexpected,
  blockTree:async () => ({path:tree, top:tree, workspace:tree, branch:'block-B1'}), repoTop:async () => repo,
  configInForce:() => null, gitChildEnv:() => env, parseTapPassed:() => ({failed:[]}),
  appendRow:async () => {}, signRow:(row) => row, loadKey:async () => Buffer.alloc(32),
  runGatesVerb:async (args, streams) => {
    const result = spawnSync('gradle', [], {cwd:args.at(-1), env, encoding:'utf8'});
    streams.stdout.write(JSON.stringify({allOk:result.status===0, results:[{gate:'test', skipped:false, ok:result.status===0, stdout:result.stdout, stderr:result.stderr}]}));
    return result.status ?? 1;
  }
});
process.exitCode = code;
`
		cmd := exec.Command("node", "--input-type=module", "-e", script, module, f.repo, f.tree)
		data, err := cmd.CombinedOutput()
		if err == nil {
			return 0, string(data)
		}
		if exit, ok := err.(*exec.ExitError); ok {
			return exit.ExitCode(), string(data)
		}
		t.Fatal(err)
		return -1, string(data)
	}
	if code, output := fallback(); code != 1 || !strings.Contains(output, "Android SDK not found") || strings.Count(output, "gates.merge_gate_links") != 1 {
		t.Errorf("fallback without links must fail with SDK error and one hint: exit=%d output=%q", code, output)
	}
	gates := f.cfg["gates"].(map[string]any)
	gates["merge_gate_links"] = []string{"local.properties", "app/google-services.json"}
	gates["merge_gate_setup"] = []string{"/bin/sh", "-c", "test -f local.properties && test -f app/google-services.json && touch prepared"}
	f.commitConfig(t)
	code, output = land()
	if code != 0 || !strings.Contains(output, "B1 landed") {
		t.Fatalf("closed-unmerged retry must land: exit=%d output=%q", code, output)
	}
	if got := landGit(t, f.repo, "show", "HEAD:sample.mjs"); got != "new" {
		t.Fatalf("merged sample=%q, want new", got)
	}
	calls, err := os.ReadFile(filepath.Join(f.home, "suite-calls"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(calls)), "\n"); len(lines) != 1 || !strings.HasSuffix(lines[0], " copy") {
		t.Fatalf("copy-links retry must run exactly one suite with copies: %q", calls)
	}
	if strings.Contains(output, "private-sdk") || strings.Contains(output, "private-google-config") {
		t.Fatal("land printed ignored file contents")
	}
	if code, output := fallback(); code != 0 || !strings.Contains(output, "B1 landed") {
		t.Fatalf("fallback with links must land: exit=%d output=%q", code, output)
	}
	calls, err = os.ReadFile(filepath.Join(f.home, "suite-calls"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(calls)), "\n"); len(lines) != 2 || !strings.HasSuffix(lines[1], " link") {
		t.Fatalf("want native copy and fallback symlink suite, got %q", calls)
	}
}
