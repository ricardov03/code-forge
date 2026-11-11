package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
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
	// The merge gate runs the tests selected from the merge (B404); the block names this one.
	landWrite(t, filepath.Join(f.repo, "sample.test.mjs"), "import './sample.mjs'\n")
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

// landDone writes the done file that block close leaves for a closed block.
func landDone(t *testing.T, home, run, commit string) {
	t.Helper()
	data, err := json.Marshal(map[string]any{"block": "B1", "run_id": run, "status": "closed", "event": "block.close", "commit": commit})
	if err != nil {
		t.Fatal(err)
	}
	landWrite(t, filepath.Join(home, ".code-forge/runs", run, "blocks", "B1.done.json"), string(data))
}

// landLinksEntry is the ledger entry of the fixture block: it owns sample.mjs
// and names sample.test.mjs as its test, so the scoped merge gate runs a suite.
func landLinksEntry() ledger.Object {
	return ledger.Object{{Key: "owned_files", Value: []any{"sample.mjs"}}, {Key: "test_files", Value: []any{"sample.test.mjs"}}}
}

func TestLandAndroidIgnoredFiles(t *testing.T) {
	f := androidLand(t)
	red := landMergeGate(f.home, "r-android-links", "B1", f.repo, f.tree, f.repo, "block-B1", "main", landLinksEntry())
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
	green := landMergeGate(f.home, "r-android-links", "B1", f.repo, f.tree, f.repo, "block-B1", "main", landLinksEntry())
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
	landDone(t, f.home, run, landGit(t, f.tree, "rev-parse", "HEAD"))
	before := landGit(t, f.repo, "rev-parse", "HEAD")
	code, output := land()
	if code != 1 || !strings.Contains(output, "Android SDK not found") || strings.Count(output, "gates.merge_gate_links") != 1 || landGit(t, f.repo, "rev-parse", "HEAD") != before {
		t.Errorf("first retry must fail only at Android gate and keep main unchanged: exit=%d output=%q", code, output)
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
}

// landClosedGo lands B1 as a closed block of run through the native path.
func landClosedGo(t *testing.T, f androidLandFixture, run string, flags map[string]any) (int, string) {
	t.Helper()
	entry := ledger.Object{{Key: "status", Value: "closed"}, {Key: "owned_files", Value: []any{"sample.mjs"}}, {Key: "base_branch", Value: "main"}, {Key: "worktree", Value: ledger.Object{{Key: "path", Value: f.tree}, {Key: "branch", Value: "block-B1"}, {Key: "base_sha", Value: f.base}}}}
	record := ledger.Object{{Key: "run_id", Value: run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: f.repo}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}}
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	landWrite(t, filepath.Join(f.home, ".code-forge/runs", run+".key"), strings.Repeat("0", 32))
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

func TestLandClosedRetryRefusesMovedBranch(t *testing.T) {
	f := androidLand(t)
	const run = "r-android-moved"
	flags := map[string]any{"report": "unused", "transcript": "unused", "merge": true, "no-merge-gate": true}
	closed := landGit(t, f.tree, "rev-parse", "HEAD")
	before := landGit(t, f.repo, "rev-parse", "HEAD")
	// A close that recorded no commit: Go must refuse, not merge blind.
	if code, output := landClosedGo(t, f, run, flags); code != 1 || strings.Count(output, "no recorded close commit") != 1 || landGit(t, f.repo, "rev-parse", "HEAD") != before {
		t.Errorf("go retry without close commit: exit=%d head moved=%v output=%q", code, landGit(t, f.repo, "rev-parse", "HEAD") != before, output)
	}
	landDone(t, f.home, run, closed)
	landWrite(t, filepath.Join(f.tree, "sample.mjs"), "after close\n")
	landGit(t, f.tree, "commit", "-qam", "after close")
	moved := landGit(t, f.tree, "rev-parse", "HEAD")
	want := "branch block-B1 moved after close (closed at " + closed[:12] + ", now " + moved[:12] + ")"
	if code, output := landClosedGo(t, f, run, flags); code != 1 || strings.Count(output, want) != 1 || landGit(t, f.repo, "rev-parse", "HEAD") != before {
		t.Errorf("go retry after a post-close commit: exit=%d head moved=%v output=%q", code, landGit(t, f.repo, "rev-parse", "HEAD") != before, output)
	}
}

// landCacheFixture links an ignored directory "cache" whose files are given
// by slash path, and gates on a suite that counts the copied regular files.
func landCacheFixture(t *testing.T, f androidLandFixture, files map[string]string) {
	t.Helper()
	for name, data := range files {
		landWrite(t, filepath.Join(f.repo, filepath.FromSlash(name)), data)
	}
	bin := t.TempDir()
	landWrite(t, filepath.Join(bin, "checkcopy"), "#!/bin/sh\n[ -L cache ] && { echo 'cache is a link'; exit 1; }\nn=$(find cache -type f | wc -l | tr -d ' ')\n[ \"$n\" = "+strconv.Itoa(len(files))+" ] || { echo \"copied $n files\"; exit 1; }\necho \"$PWD\" >> \"$HOME/copy-calls\"\n")
	if err := os.Chmod(filepath.Join(bin, "checkcopy"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODE_FORGE_LAND_COPY_LINKS", "1")
	landWrite(t, filepath.Join(f.repo, ".gitignore"), ".code-forge/\nlocal.properties\napp/google-services.json\nprepared\ncache/\n")
	landGit(t, f.repo, "add", ".gitignore")
	gates := f.cfg["gates"].(map[string]any)
	gates["test"] = []string{"checkcopy"}
	gates["merge_gate_links"] = []string{"cache"}
	f.commitConfig(t)
}

func landCopyCalls(t *testing.T, f androidLandFixture) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, "copy-calls"))
	if os.IsNotExist(err) {
		return 0
	} else if err != nil {
		t.Fatal(err)
	}
	return len(strings.Split(strings.TrimSpace(string(data)), "\n"))
}

func TestLandCopyLinksChildNames(t *testing.T) {
	f := androidLand(t)
	landCacheFixture(t, f, map[string]string{"cache/a:b": "colon\n", "cache/back\\slash": "back\n", "cache/node_modules/pkg/.git/HEAD": "ref\n", "cache/plain.txt": "plain\n"})
	if got := landMergeGate(f.home, "r-android-links", "B1", f.repo, f.tree, f.repo, "block-B1", "main", landLinksEntry(), true); got.reason != "" {
		t.Errorf("go copy of children named a:b, back\\slash and a nested .git must pass the gate: %q", got.reason)
	}
	if got := landCopyCalls(t, f); got != 1 {
		t.Errorf("want 1 green copied suite, got %d", got)
	}
	// Containment still holds below the configured name: a child that resolves
	// outside the repository is refused and runs no suite.
	outside := t.TempDir()
	landWrite(t, filepath.Join(outside, "secret"), "outside\n")
	if err := os.Symlink(outside, filepath.Join(f.repo, "cache", "escape")); err != nil {
		t.Fatal(err)
	}
	if got := landMergeGate(f.home, "r-android-links", "B1", f.repo, f.tree, f.repo, "block-B1", "main", landLinksEntry(), true); !strings.Contains(got.reason, "gates.merge_gate_links cannot copy: cache") {
		t.Errorf("go copy must refuse a child outside the repository: %q", got.reason)
	}
	if got := landCopyCalls(t, f); got != 1 {
		t.Errorf("refused copies must run no suite, got %d suites", got)
	}
}

func TestLandCopyLinksSymlinkedRepo(t *testing.T) {
	f := androidLand(t)
	landCacheFixture(t, f, map[string]string{"cache/plain.txt": "plain\n"})
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(f.repo, alias); err != nil {
		t.Fatal(err)
	}
	if got := landMergeGate(f.home, "r-android-links", "B1", alias, f.tree, alias, "block-B1", "main", landLinksEntry(), true); got.reason != "" {
		t.Errorf("go copy from a repository reached through a symlink must pass the gate: %q", got.reason)
	}
	if got := landCopyCalls(t, f); got != 1 {
		t.Errorf("want 1 green copied suite, got %d", got)
	}
}
