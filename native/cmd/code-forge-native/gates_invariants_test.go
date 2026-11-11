package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// B402: gates.extra.invariants was accepted but no engine ran it. Go gates
// refuse it before any gate command runs and never hand it to Node.
func TestGatesRefuseExtraInvariants(t *testing.T) {
	const config = `{"gates":{"test":["/usr/bin/touch","gate-ran"],"lint":null,"types":null,"format":null,"rerun_failed":0,"extra":%s}}`
	setup := func(t *testing.T, extra string) (repo, home string) {
		t.Helper()
		root := t.TempDir()
		repo, home = filepath.Join(root, "repo"), filepath.Join(root, "home")
		if err := os.MkdirAll(repo, 0755); err != nil {
			t.Fatal(err)
		}
		body := strings.Replace(config, "%s", extra, 1)
		if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n"), 0600); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "."}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base"}} {
			cmd := exec.Command("git", args...)
			cmd.Dir = repo
			cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v: %s", args, err, out)
			}
		}
		dir := filepath.Join(home, ".code-forge", "runs")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		block := map[string]any{"status": "open", "kind": "code", "base_branch": "main", "owned_files": []string{"a.go"}, "test_files": []string{}}
		data, _ := json.Marshal(map[string]any{"run_id": "fixture", "workspace": repo, "status": "active", "project": "fixture", "blocks": map[string]any{"B1": block}})
		if err := os.WriteFile(filepath.Join(dir, "fixture.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		return repo, home
	}
	gatesRun := func(t *testing.T, repo, home string, args ...string) (int, string, string) {
		t.Helper()
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(append([]string{"gates", "run"}, args...))
		cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "HOME="+home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_TEST_QUEUE=off", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_NODE_VERSION=v24.22.1", "CF_GATES_ARGS="+string(encoded))
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err = cmd.Run()
		code := 0
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return code, stdout.String(), stderr.String()
	}
	ran := func(repo string) bool {
		_, err := os.Stat(filepath.Join(repo, "gate-ran"))
		return err == nil
	}
	const want = "gates run: gates.extra.invariants is not supported: no engine runs it, so its script would be skipped; remove it from .code-forge.yml and run the script as a gates.custom entry ({name, argv}); no gate was run"

	for _, c := range []struct {
		name string
		args []string
	}{{"block", []string{"--run", "fixture", "--block", "B1"}}, {"unscoped", nil}} {
		t.Run(c.name, func(t *testing.T) {
			repo, home := setup(t, `{"secret_scan":true,"invariants":"scripts/invariants.sh"}`)
			code, stdout, stderr := gatesRun(t, repo, home, c.args...)
			if code != 2 {
				t.Fatalf("want exit 2, got %d; stdout %q stderr %q", code, stdout, stderr)
			}
			if lines := strings.Split(strings.TrimRight(stderr, "\n"), "\n"); len(lines) != 1 || lines[0] != want {
				t.Fatalf("want exactly one stderr line %q, got %q", want, stderr)
			}
			if stdout != "" {
				t.Fatalf("want no stdout, got %q", stdout)
			}
			if ran(repo) {
				t.Fatal("the test gate command ran; want no gate command before the refusal")
			}
		})
	}

	t.Run("without invariants", func(t *testing.T) {
		repo, home := setup(t, `{"secret_scan":true,"safe_edit":false}`)
		code, stdout, stderr := gatesRun(t, repo, home, "--run", "fixture", "--block", "B1")
		if code != 0 {
			t.Fatalf("want exit 0, got %d; stdout %q stderr %q", code, stdout, stderr)
		}
		if !ran(repo) {
			t.Fatalf("want the test gate to run once, marker missing; stdout %q", stdout)
		}
	})
}
