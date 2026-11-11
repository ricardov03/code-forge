package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
)

// B370: `code-forge help` (served by Node) must list every public Go route,
// so a route added here without a src/cli file still shows up for customers.
func TestHelpListsEveryGoRoute(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required to run code-forge help:", err)
	}
	script, err := filepath.Abs("../../../bin/code-forge.mjs")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	cmd := exec.Command(node, script, "help")
	cmd.Dir = home
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + home,
		"CODE_FORGE_NATIVE=0", "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_HANDOVER_LOG=0", "NO_COLOR=1",
	}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("code-forge help failed: %v", err)
	}
	listed := map[string]int{}
	for _, m := range regexp.MustCompile(`(?m)^  ([a-z][a-z0-9-]*)$`).FindAllStringSubmatch(string(out), -1) {
		listed[m[1]]++
	}
	keys := make([]string, 0, len(routes))
	for verb := range routes {
		keys = append(keys, verb)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		t.Fatal("routes is empty; want the registered Go verbs")
	}
	for _, verb := range keys {
		if listed[verb] != 1 {
			t.Errorf("help lists Go route %q %d times; want exactly 1\n%s", verb, listed[verb], out)
		}
	}
}
