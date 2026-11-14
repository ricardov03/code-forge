package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// b408Fixture is a code-forge-shaped repo (native/go.mod and
// scripts/build-native.mjs) with one open worktree block B1. A fake go on PATH
// logs every build as "build <cwd> <argv> | <env>" (the -o temp name with its
// pid as PID) and writes the -o file; a fake Node answers --version, logs a CLI
// call as "node <verb> <sub>" and, for block rebase, rebases the worktree onto
// main (B575: the rebuild itself no longer runs Node).
type b408Fixture struct {
	t                          *testing.T
	repo, tree, home, log, bin string
	fakeNode, goDir            string
}

func newB408Fixture(t *testing.T) *b408Fixture {
	t.Helper()
	root := t.TempDir()
	f := &b408Fixture{t: t, repo: filepath.Join(root, "repo"), tree: filepath.Join(root, "wt"), home: filepath.Join(root, "home"), log: filepath.Join(root, "calls.log"), fakeNode: filepath.Join(root, "fake-node"), goDir: filepath.Join(root, "gobin")}
	config := `{"gates":{"test":["/bin/sh","-c","echo test >> '` + f.log + `'"],"lint":null,"types":null,"format":null,"rerun_failed":0}}`
	f.write(f.repo, ".code-forge.yml", config)
	f.write(f.repo, ".gitignore", "native/bin/\n")
	f.write(f.repo, "README.md", "readme\n")
	f.write(f.repo, "scripts/build-native.mjs", "// fixture\n")
	f.write(f.repo, "package.json", `{"name":"fixture","version":"9.8.7"}`+"\n")
	f.write(f.repo, "native/go.mod", "module fixture\n")
	f.write(f.repo, "native/x.go", "package fixture\n")
	f.write(f.repo, "native/x_test.go", "package fixture\n")
	f.git(f.repo, "init", "-q", "-b", "main")
	f.git(f.repo, "add", ".")
	f.git(f.repo, "commit", "-qm", "base")
	base := strings.TrimSpace(f.git(f.repo, "rev-parse", "HEAD"))
	f.git(f.repo, "worktree", "add", "-q", "-b", "forge/B1", f.tree, "main")
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	f.bin = filepath.Join(f.tree, "native", "bin", runtime.GOOS+"-"+arch, "code-forge-native")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"--version) echo v24.22.1; exit 0 ;;\n" +
		"*)\n" +
		"  echo \"node $2 $3\" >> \"$CF_B408_LOG\"\n" +
		"  if [ \"$2 $3\" = 'block rebase' ]; then git -C \"$CF_B408_TREE\" rebase -q main || exit 1; fi\n" +
		"  exit 0 ;;\n" +
		"esac\n"
	if err := os.WriteFile(f.fakeNode, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	b575WriteFakeGo(t, f.goDir)
	if err := os.MkdirAll(filepath.Join(f.home, ".code-forge", "runs"), 0700); err != nil {
		t.Fatal(err)
	}
	block := map[string]any{"status": "open", "kind": "code", "owned_files": []string{"native/x.go"}, "base_sha": base, "base_branch": "main",
		"worktree": map[string]any{"path": f.tree, "branch": "forge/B1", "base_sha": base}}
	data, _ := json.Marshal(map[string]any{"run_id": "fixture", "workspace": f.repo, "status": "active", "project": "fixture", "blocks": map[string]any{"B1": block}})
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "fixture.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	// B578: block rebase runs in Go; it signs its row with the run key.
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "fixture.key"), bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *b408Fixture) write(dir, file, body string) {
	f.t.Helper()
	p := filepath.Join(dir, file)
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *b408Fixture) env() []string {
	return append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
}

func (f *b408Fixture) git(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = f.env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}

func (f *b408Fixture) touch(file string, at time.Time) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		f.t.Fatal(err)
	}
	if _, err := os.Stat(file); os.IsNotExist(err) {
		if err := os.WriteFile(file, []byte("binary"), 0755); err != nil {
			f.t.Fatal(err)
		}
	}
	if err := os.Chtimes(file, at, at); err != nil {
		f.t.Fatal(err)
	}
}

// call runs the public router in a fresh test process, as TestGatesProcess does.
func (f *b408Fixture) call(extra []string, args ...string) (code int, stdout, stderr string) {
	f.t.Helper()
	exe, err := os.Executable()
	if err != nil {
		f.t.Fatal(err)
	}
	encoded, _ := json.Marshal(args)
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.repo
	cmd.Env = append(f.env(), "HOME="+f.home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_TEST_QUEUE=off", "CODE_FORGE_NODE="+f.fakeNode,
		"CF_B408_LOG="+f.log, "CF_B408_TREE="+f.tree, "PATH="+f.goDir+string(os.PathListSeparator)+os.Getenv("PATH"), "CF_GATES_PROCESS=1", "CF_GATES_NODE_VERSION=v24.22.1", "CF_GATES_ARGS="+string(encoded))
	cmd.Env = append(cmd.Env, extra...)
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			f.t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	return code, out.String(), errOut.String()
}

// buildCall is the fake go log line of one host build: cwd is the real
// worktree's native/, argv and env are the script's, and the temp name is
// <binary>.<pid>.tmp.
func (f *b408Fixture) buildCall() string {
	return b575BuildLine(filepath.Join(f.realTree(), "native"), f.bin, "9.8.7", "v24.22.1")
}

func (f *b408Fixture) calls() string {
	data, err := os.ReadFile(f.log)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return string(data)
}

func (f *b408Fixture) realTree() string {
	real, err := filepath.EvalSymlinks(f.tree)
	if err != nil {
		f.t.Fatal(err)
	}
	return real
}

func TestGatesNativeRebuildWhenSourcesAreNewer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(f *b408Fixture)
	}{
		{"older binary", func(f *b408Fixture) {
			now := time.Now()
			// Only x.go is newer than the binary, so a check that ignores
			// .go files cannot pass on go.mod or x_test.go mtimes.
			for _, file := range []string{"native/go.mod", "native/x_test.go"} {
				f.touch(filepath.Join(f.tree, file), now.Add(-2*time.Hour))
			}
			f.touch(f.bin, now.Add(-time.Hour))
			f.touch(filepath.Join(f.tree, "native", "x.go"), now)
		}},
		{"newer go.mod", func(f *b408Fixture) {
			now := time.Now()
			for _, file := range []string{"native/x.go", "native/x_test.go"} {
				f.touch(filepath.Join(f.tree, file), now.Add(-2*time.Hour))
			}
			f.touch(f.bin, now.Add(-time.Hour))
			f.touch(filepath.Join(f.tree, "native", "go.mod"), now)
		}},
		{"missing binary", func(f *b408Fixture) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newB408Fixture(t)
			tc.setup(f)
			code, stdout, stderr := f.call(nil, "gates", "run", "--run", "fixture", "--block", "B1")
			want := f.buildCall() + "test\n"
			if code != 0 || f.calls() != want {
				t.Fatalf("want exit 0 and exactly one build before the test command (%q), got exit %d calls %q stderr %s", want, code, f.calls(), stderr)
			}
			var result struct {
				AllOK         bool  `json:"allOk"`
				NativeRebuilt *bool `json:"native_rebuilt"`
			}
			if err := json.Unmarshal([]byte(stdout), &result); err != nil {
				t.Fatalf("gate JSON: %v: %s", err, stdout)
			}
			if !result.AllOK || result.NativeRebuilt == nil || !*result.NativeRebuilt {
				t.Fatalf("want allOk and native_rebuilt true, got %s", stdout)
			}
		})
	}
}

func TestGatesNativeRebuildSkippedWhenBinaryIsNewer(t *testing.T) {
	f := newB408Fixture(t)
	now := time.Now()
	for _, file := range []string{"native/x.go", "native/go.mod"} {
		f.touch(filepath.Join(f.tree, file), now.Add(-2*time.Hour))
	}
	f.touch(f.bin, now.Add(-time.Hour))
	// A test file is not built into the binary.
	f.touch(filepath.Join(f.tree, "native", "x_test.go"), now)
	code, stdout, stderr := f.call(nil, "gates", "run", "--run", "fixture", "--block", "B1")
	if code != 0 || f.calls() != "test\n" {
		t.Fatalf("want exit 0 and only the test command, got exit %d calls %q stderr %s", code, f.calls(), stderr)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("gate JSON: %v: %s", err, stdout)
	}
	if _, present := result["native_rebuilt"]; present || result["allOk"] != true {
		t.Fatalf("want allOk and no native_rebuilt field, got %s", stdout)
	}
}

func TestGatesNativeRebuildFailureRunsNoGate(t *testing.T) {
	f := newB408Fixture(t)
	code, stdout, stderr := f.call([]string{"CF_B408_BUILD_FAIL=1"}, "gates", "run", "--run", "fixture", "--block", "B1")
	if code != 1 || f.calls() != f.buildCall() || stdout != "" {
		t.Fatalf("want exit 1, one build call, no gate command and no JSON, got exit %d calls %q stdout %q", code, f.calls(), stdout)
	}
	if lines := strings.Split(strings.TrimSpace(stderr), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "native-rebuild-failed") || !strings.Contains(lines[0], "go: build broke") {
		t.Fatalf("want one stderr line naming native-rebuild-failed and the build error, got %q", stderr)
	}
}

func TestBlockRebaseRebuildsNative(t *testing.T) {
	for _, tc := range []struct {
		name, file, calls string
		fail              bool
		code              int
	}{
		// B578: the rebase runs in Go (no Node call); the rebuild follows it.
		{name: "native change", file: "native/x.go", calls: "%s"},
		{name: "build script change", file: "scripts/build-native.mjs", calls: "%s"},
		{name: "no native change", file: "README.md", calls: ""},
		{name: "build fails", file: "native/x.go", calls: "%s", fail: true, code: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newB408Fixture(t)
			f.write(f.repo, tc.file, "// upstream change\n")
			f.git(f.repo, "commit", "-qam", "upstream")
			var extra []string
			if tc.fail {
				extra = append(extra, "CF_B408_BUILD_FAIL=1")
			}
			code, _, stderr := f.call(extra, "block", "rebase", "B1", "--run", "fixture")
			want := strings.ReplaceAll(tc.calls, "%s", f.buildCall())
			if code != tc.code || f.calls() != want {
				t.Fatalf("want exit %d and calls %q, got exit %d calls %q stderr %s", tc.code, want, code, f.calls(), stderr)
			}
			if head, upstream := f.git(f.tree, "rev-parse", "HEAD~0^{tree}"), f.git(f.repo, "rev-parse", "main^{tree}"); head != upstream {
				t.Fatalf("the fake rebase did not move the worktree onto main")
			}
			lines := strings.Split(strings.TrimSpace(stderr), "\n")
			if tc.fail && (len(lines) != 1 || !strings.Contains(lines[0], "native-rebuild-failed")) {
				t.Fatalf("want one stderr line naming native-rebuild-failed, got %q", stderr)
			}
			if !tc.fail && strings.Contains(stderr, "native-rebuild-failed") {
				t.Fatalf("no failure expected, got %q", stderr)
			}
		})
	}
}

// B426: block rebase holds no run lock while its slow native build runs. A
// block write on another block of the same run succeeds meanwhile: the build
// holds until the test releases it, so a held run lock would refuse the write
// at its 5 s lock deadline (B654: no wall-clock bound).
func TestBlockRebaseSlowBuildLeavesRunUnlocked(t *testing.T) {
	f := newB408Fixture(t)
	record, err := state.ReadRun(f.home, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	blocks := record.Get("blocks").(ledger.Object)
	blocks.Set("B2", ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{"README.md"}}})
	record.Set("blocks", blocks)
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	f.write(f.repo, "native/x.go", "// upstream change\n")
	f.git(f.repo, "commit", "-qam", "upstream")
	type outcome struct {
		code   int
		stderr string
	}
	release := filepath.Join(f.home, "build-release")
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o644) })
	done := make(chan outcome, 1)
	go func() {
		code, _, stderr := f.call([]string{"CF_B408_BUILD_HOLD=" + release}, "block", "rebase", "B1", "--run", "fixture")
		done <- outcome{code, stderr}
	}()
	for deadline := time.Now().Add(60 * time.Second); !strings.Contains(f.calls(), "build "); {
		if time.Now().After(deadline) {
			t.Fatal("the slow build never started")
		}
		select {
		case o := <-done:
			t.Fatalf("block rebase ended before its build (exit %d): %s", o.code, o.stderr)
		case <-time.After(20 * time.Millisecond):
		}
	}
	err = state.UpdateBlock(f.home, "fixture", "B2", func(_, block *ledger.Object) ([]ledger.Object, error) {
		block.Set("note", "written during the build")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("write on block B2 during the build: err=%v, want success while the build holds", err)
	}
	select {
	case o := <-done:
		t.Fatalf("block rebase ended before the other write finished (exit %d), the test proves nothing: %s", o.code, o.stderr)
	default:
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if o := <-done; o.code != 0 || f.calls() != f.buildCall() {
		t.Fatalf("want exit 0 and one rebase then one build, got exit %d calls %q stderr %s", o.code, f.calls(), o.stderr)
	}
	after, err := state.ReadRun(f.home, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if note := after.Get("blocks").(ledger.Object).Get("B2").(ledger.Object).Get("note"); note != "written during the build" {
		t.Fatalf("B2 note = %v, want the write made during the build", note)
	}
}
