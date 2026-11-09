package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise the public router in a fresh process; a handover must never replace
// the test process itself. This helper also works against the pre-port router.
func TestGatesProcess(t *testing.T) {
	if os.Getenv("CF_GATES_PROCESS") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("CF_GATES_ARGS")), &args); err != nil {
		os.Exit(99)
	}
	code, _ := run(args)
	os.Exit(code)
}

func TestGatesNativeRoute(t *testing.T) {
	root := t.TempDir()
	repo, home := filepath.Join(root, "repo"), filepath.Join(root, "home")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(repo, 0755))
	must(os.MkdirAll(filepath.Join(home, ".code-forge", "runs"), 0700))
	config := `{"gates":{"test":["/bin/sh","-c","printf green"],"lint":null,"types":null,"format":null,"rerun_failed":0}}`
	must(os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte(config), 0600))
	record := map[string]any{"run_id": "fixture", "workspace": repo, "status": "active", "project": "fixture", "blocks": map[string]any{"B1": map[string]any{"status": "open", "kind": "code", "owned_files": []string{}}}}
	data, _ := json.Marshal(record)
	must(os.WriteFile(filepath.Join(home, ".code-forge", "runs", "fixture.json"), data, 0600))
	exe, err := os.Executable()
	must(err)
	call := func(args ...string) (int, string) {
		t.Helper()
		encoded, _ := json.Marshal(args)
		cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "HOME="+home, "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded))
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			if e, ok := err.(*exec.ExitError); ok {
				code = e.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		return code, string(out)
	}
	code, out := call("gates", "run", "--run", "fixture", "--block", "B1")
	if code != 0 {
		t.Fatalf("native green exit = %d, want 0: %s", code, out)
	}
	var result struct {
		AllOK   bool `json:"allOk"`
		Results []struct {
			Gate    string
			Skipped bool
			Stdout  string
		}
	}
	must(json.Unmarshal([]byte(out), &result))
	if !result.AllOK || len(result.Results) != 4 || result.Results[0].Stdout != "green" {
		t.Fatalf("green: want allOk, four results, and exact green output; got %s", out)
	}
	for _, flags := range [][]string{{"--timeout-ms"}, {"--timeout-ms", "NaN"}, {"--slug"}, {"--cwd"}} {
		args := append([]string{"gates", "run", "--run", "fixture", "--block", "B1"}, flags...)
		code, out := call(args...)
		if code != 2 || !strings.HasPrefix(out, "gates run: ") {
			t.Fatalf("%v: exit=%d output=%q, want native usage exit 2", flags, code, out)
		}
	}
	code, _ = call("gates", "detect")
	if code != 127 {
		t.Fatalf("detect handover exit=%d, want 127", code)
	}
}
