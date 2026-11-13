package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B585: the test binary never asks the machine's node unless a test opts in
// (nodeRunVersionUse); every other test keeps the build-time fallback value.
func init() {
	nodeRunVersionAsk = func() string { return "" }
}

// nodeRunVersionUse resets the once-per-process cache and installs ask.
func nodeRunVersionUse(t *testing.T, ask func() string) {
	t.Helper()
	oldAsk, oldNode, oldTimeout := nodeRunVersionAsk, reviewWaitNodeVersion, nodeRunVersionTimeout
	nodeRunVersionOnce, nodeRunVersionCached, nodeRunVersionAsk = sync.Once{}, "", ask
	t.Cleanup(func() {
		nodeRunVersionAsk, reviewWaitNodeVersion, nodeRunVersionTimeout = oldAsk, oldNode, oldTimeout
		nodeRunVersionOnce, nodeRunVersionCached = sync.Once{}, ""
	})
}

// nodeRunFake writes a fake node that logs each call to log and runs body.
func nodeRunFake(t *testing.T, dir, log, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "node")
	script := "#!/bin/sh\necho \"$@\" >> '" + log + "'\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func nodeRunCalls(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

// nodeRunNoNode leaves no node anywhere: CODE_FORGE_NODE names nothing and
// PATH holds an empty dir plus the system dirs (checked to hold no node).
func nodeRunNoNode(t *testing.T, keep ...string) {
	t.Helper()
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("PATH", strings.Join(append(keep, t.TempDir(), "/usr/bin", "/bin"), ":"))
	if p, err := exec.LookPath("node"); err == nil {
		t.Skipf("a node is in the system dirs: %s", p)
	}
}

func TestB585NodeVersionAskedAtRunTimeOnce(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	nodeRunFake(t, filepath.Join(dir, "bin"), log, "echo v22.11.0")
	t.Setenv("CODE_FORGE_NODE", "")
	t.Setenv("PATH", filepath.Join(dir, "bin")+":/usr/bin:/bin")
	nodeRunVersionUse(t, nodeRunVersionAskNode)
	reviewWaitNodeVersion = "unknown"
	for i := 0; i < 3; i++ {
		if got := nodeRunVersion(); got != "v22.11.0" {
			t.Fatalf("call %d: got %q, want the running node v22.11.0", i, got)
		}
	}
	if calls := nodeRunCalls(t, log); len(calls) != 1 || calls[0] != "--version" {
		t.Errorf("node asked %q, want once with --version", calls)
	}
}

func TestB585NodeVersionPrefersCodeForgeNode(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	nodeRunFake(t, filepath.Join(dir, "path"), log, "echo v22.11.0")
	override := nodeRunFake(t, filepath.Join(dir, "override"), log, "echo v20.1.0")
	t.Setenv("CODE_FORGE_NODE", override)
	t.Setenv("PATH", filepath.Join(dir, "path")+":/usr/bin:/bin")
	nodeRunVersionUse(t, nodeRunVersionAskNode)
	if got := nodeRunVersion(); got != "v20.1.0" {
		t.Errorf("got %q, want CODE_FORGE_NODE's v20.1.0", got)
	}
}

func TestB585NodeVersionNoNodeFallsBack(t *testing.T) {
	nodeRunNoNode(t)
	nodeRunVersionUse(t, nodeRunVersionAskNode)
	reviewWaitNodeVersion = "unknown"
	if got := nodeRunVersion(); got != "unknown" {
		t.Errorf("no node, no build value: got %q, want unknown", got)
	}
	nodeRunVersionUse(t, nodeRunVersionAskNode)
	reviewWaitNodeVersion = "v18.20.4"
	if got := nodeRunVersion(); got != "v18.20.4" {
		t.Errorf("no node, build value v18.20.4: got %q", got)
	}
	nodeRunVersionUse(t, nodeRunVersionAskNode)
	reviewWaitNodeVersion = ""
	if got := nodeRunVersion(); got != "unknown" {
		t.Errorf("no node, empty build value: got %q, want unknown", got)
	}
}

func TestB585NodeVersionBadAnswerFallsBack(t *testing.T) {
	for name, body := range map[string]string{
		"not a version":  "echo hello",
		"pre-release":    "echo v23.0.0-pre",
		"two lines":      "echo warning; echo v22.11.0",
		"no v":           "echo 22.11.0",
		"non-zero exit":  "echo v22.11.0; exit 3",
		"empty":          "true",
		"times out":      "exec sleep 5",
		"version on err": "echo v22.11.0 >&2",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls.log")
			node := nodeRunFake(t, dir, log, body)
			t.Setenv("CODE_FORGE_NODE", node)
			asks := 0
			nodeRunVersionUse(t, func() string { asks++; return nodeRunVersionAskNode() })
			if name == "times out" {
				nodeRunVersionTimeout = 500 * time.Millisecond
			}
			reviewWaitNodeVersion = "unknown"
			started := time.Now()
			if got := nodeRunVersion(); got != "unknown" {
				t.Errorf("got %q, want unknown", got)
			}
			if got := nodeRunVersion(); got != "unknown" || asks != 1 {
				t.Errorf("second read: got %q after %d asks, want unknown after 1", got, asks)
			}
			calls := nodeRunCalls(t, log)
			if name == "times out" {
				// B599: the hanging fake may be killed before it logs its
				// call on a busy machine; the ask must still have waited
				// out the timeout and run node at most once.
				if elapsed := time.Since(started); elapsed < nodeRunVersionTimeout || len(calls) > 1 {
					t.Errorf("timed out after %v with node asked %d times, want >= %v and at most 1", elapsed, len(calls), nodeRunVersionTimeout)
				}
			} else if len(calls) != 1 {
				t.Errorf("node asked %d times, want 1", len(calls))
			}
		})
	}
}

// The gate-tree stamp input carries the running node: the same stamp as a
// build that baked that version in, and no stamp when no node is known.
func TestB585GateStampUsesRunningNode(t *testing.T) {
	plan := gates.Plan{Stack: "go"}
	tree := "4b825dc642cb6eb9a060e54bf8d69288fbe2c4b8"

	// The environment is stamp input too: name the fake node first.
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	node := nodeRunFake(t, dir, log, "echo v22.11.0")
	t.Setenv("CODE_FORGE_NODE", node)

	nodeRunVersionUse(t, func() string { return "" })
	reviewWaitNodeVersion = "v22.11.0"
	baked := gatesFullStamp(tree, plan, nil, ledger.Object{})
	if baked == "" {
		t.Fatal("no stamp with a baked-in version")
	}

	nodeRunVersionUse(t, nodeRunVersionAskNode)
	reviewWaitNodeVersion = "unknown"
	if got := gatesFullStamp(tree, plan, nil, ledger.Object{}); got != baked {
		t.Errorf("run-time v22.11.0 stamp %q, want the baked v22.11.0 stamp %q", got, baked)
	}
	if got := gatesFullStamp(tree, plan, nil, ledger.Object{}); got != baked {
		t.Errorf("second stamp %q", got)
	}
	if calls := nodeRunCalls(t, log); len(calls) != 1 {
		t.Errorf("node asked %d times, want 1", len(calls))
	}

	// Another running node is another stamp input.
	nodeRunFake(t, dir, log, "echo v24.22.1")
	nodeRunVersionUse(t, nodeRunVersionAskNode)
	if got := gatesFullStamp(tree, plan, nil, ledger.Object{}); got == "" || got == baked {
		t.Errorf("v24.22.1 stamp %q must differ from the v22.11.0 stamp", got)
	}

	nodeRunNoNode(t)
	nodeRunVersionUse(t, nodeRunVersionAskNode)
	reviewWaitNodeVersion = "unknown"
	if got := gatesFullStamp(tree, plan, nil, ledger.Object{}); got != "" {
		t.Errorf("no node: stamp %q, want none", got)
	}
}

// logs report prints the running node on its "- Node:" line (Node prints its
// own process.version) and never hands over for a missing build value.
func TestB585LogsReportUsesRunningNode(t *testing.T) {
	c := lrTestEnv(t, "", false)
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	node := nodeRunFake(t, dir, log, "echo v24.22.1")
	t.Setenv("CODE_FORGE_NODE", node)
	nodeRunVersionUse(t, nodeRunVersionAskNode)
	reviewWaitNodeVersion = "unknown"
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--dry-run")
	lrTestCheck(t, "running node", code, out, errText, 0, c.golden(t, "dry.stdout"), "")
	if got := b500HandedOver(t, c.home); got != "" {
		t.Errorf("handed over %q", got)
	}
	if calls := nodeRunCalls(t, log); len(calls) != 1 {
		t.Errorf("node asked %d times, want 1", len(calls))
	}
}

func TestB585LogsReportNoNodeRunsInGo(t *testing.T) {
	c := lrTestEnv(t, "", false)
	// Keep lrTestEnv's fake npm and gh (its first PATH entry).
	nodeRunNoNode(t, strings.SplitN(os.Getenv("PATH"), ":", 2)[0])
	nodeRunVersionUse(t, nodeRunVersionAskNode)
	reviewWaitNodeVersion = "unknown"
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--dry-run")
	want := strings.Replace(c.golden(t, "dry.stdout"), "- Node: v24.22.1\n", "- Node: unknown\n", 1)
	lrTestCheck(t, "no node", code, out, errText, 0, want, "")
	if got := b500HandedOver(t, c.home); got != "" {
		t.Errorf("handed over %q", got)
	}
}

// The error-log `node` field is the running node.
func TestB585ErrorLogRowNodeField(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	node := nodeRunFake(t, dir, log, "echo v22.11.0")
	t.Setenv("CODE_FORGE_NODE", node)
	nodeRunVersionUse(t, nodeRunVersionAskNode)
	reviewWaitNodeVersion = "unknown"
	spawnWarnMu.Lock()
	spawnWarned = map[string]bool{}
	spawnWarnMu.Unlock()
	t.Cleanup(func() {
		spawnWarnMu.Lock()
		spawnWarned = map[string]bool{}
		spawnWarnMu.Unlock()
	})
	spawnLogWarning("b585-test", "node field check")
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &row); err != nil {
		t.Fatal(err)
	}
	if row["node"] != "v22.11.0" {
		t.Errorf("row node %v, want v22.11.0", row["node"])
	}
}
