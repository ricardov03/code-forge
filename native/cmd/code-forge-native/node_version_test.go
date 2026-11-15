package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B690d: no verb starts node. B585 asked `node --version` once per process
// for the error-log `node` field, the gate stamp and the logs report; the
// field and the report line now say "none". Each test puts a fake node on
// CODE_FORGE_NODE and PATH that logs every run; its log must stay empty.

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

// b690dFakeNode puts a fake node first on PATH and on CODE_FORGE_NODE; it
// returns the log every run of it writes.
func b690dFakeNode(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "node-marker")
	node := nodeRunFake(t, filepath.Join(dir, "bin"), log, "echo v24.22.1")
	t.Setenv("CODE_FORGE_NODE", node)
	t.Setenv("PATH", filepath.Dir(node)+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func b690dNoNodeRun(t *testing.T, log string) {
	t.Helper()
	if calls := nodeRunCalls(t, log); len(calls) != 0 {
		t.Errorf("node ran %d times: %q", len(calls), calls)
	}
}

// b690dRows reads the error-log rows under home.
func b690dRows(t *testing.T, home string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	return rows
}

func b690dLogHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	return home
}

// spawn warning: the row keeps its `node` field, valued "none".
func TestB690dSpawnWarningRowNodeNone(t *testing.T) {
	home := b690dLogHome(t)
	log := b690dFakeNode(t)
	spawnWarnMu.Lock()
	spawnWarned = map[string]bool{}
	spawnWarnMu.Unlock()
	t.Cleanup(func() {
		spawnWarnMu.Lock()
		spawnWarned = map[string]bool{}
		spawnWarnMu.Unlock()
	})
	spawnLogWarning("b690d-test", "node field check")
	rows := b690dRows(t, home)
	if len(rows) != 1 || rows[0]["node"] != "none" {
		t.Errorf("rows %v, want one with node \"none\"", rows)
	}
	b690dNoNodeRun(t, log)
}

// doctor keys' 1Password retry warning (doctor_keys.go logNativeWarning).
func TestB690dDoctorKeysWarningRowNodeNone(t *testing.T) {
	home := b690dLogHome(t)
	log := b690dFakeNode(t)
	logNativeWarning("doctor", "op_retry", "1Password did not answer in time; the call was retried once")
	rows := b690dRows(t, home)
	if len(rows) != 1 || rows[0]["node"] != "none" || rows[0]["verb"] != "doctor" {
		t.Errorf("rows %v, want one doctor row with node \"none\"", rows)
	}
	b690dNoNodeRun(t, log)
}

// The real binary: a failing `gates run` and a failing `review-file --wait`
// each log one row with node "none", and start no node.
func TestB690dBinaryFailuresStartNoNode(t *testing.T) {
	binary := b511BuildNative(t)
	for _, tc := range []struct {
		name string
		args []string
		code int
		verb string
	}{
		{"gates run", []string{"gates", "run", "--run", "nope", "--block", "B1"}, 1, "gates"},
		{"review-file wait", []string{"review-file", "--wait", "T1", "--bogus"}, 2, "review-file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := b690dLogHome(t)
			log := b690dFakeNode(t)
			work := t.TempDir()
			git := exec.Command("git", "init", "-q", work)
			git.Env = envWithoutGit()
			if out, err := git.CombinedOutput(); err != nil {
				t.Fatalf("git init: %s: %v", out, err)
			}
			cmd := exec.Command(binary, tc.args...)
			cmd.Dir = work
			cmd.Env = append(envWithoutGit(), "HOME="+home, "CODE_FORGE_NO_ERROR_LOG=", "CODE_FORGE_TEST_QUEUE=off")
			out, err := cmd.CombinedOutput()
			code := 0
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if code != tc.code {
				t.Fatalf("exit %d, want %d: %s", code, tc.code, out)
			}
			rows := b690dRows(t, home)
			if len(rows) != 1 || rows[0]["node"] != "none" || rows[0]["verb"] != tc.verb {
				t.Errorf("rows %v, want one %s row with node \"none\"", rows, tc.verb)
			}
			b690dNoNodeRun(t, log)
		})
	}
}

// The gate-tree stamp no longer needs a node: the same stamp with no node
// anywhere and with a node on PATH, and that node never runs (B585 gave no
// stamp without one).
func TestB690dGateStampNeedsNoNode(t *testing.T) {
	plan := gates.Plan{Stack: "go"}
	tree := "4b825dc642cb6eb9a060e54bf8d69288fbe2c4b8"
	log := b690dFakeNode(t)
	withNode := gatesFullStamp(tree, plan, nil, ledger.Object{})
	if withNode == "" {
		t.Fatal("no stamp with a node on PATH")
	}
	b690dNoNodeRun(t, log)
	// The environment is stamp input: keep PATH and CODE_FORGE_NODE, make
	// the node fail if it were run.
	if err := os.WriteFile(os.Getenv("CODE_FORGE_NODE"), []byte("#!/bin/sh\necho ran >> '"+log+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := gatesFullStamp(tree, plan, nil, ledger.Object{}); got != withNode {
		t.Errorf("stamp %q with a broken node, want %q", got, withNode)
	}
	b690dNoNodeRun(t, log)
}

// logs report's system line names Node "none" and runs no node.
func TestB690dLogsReportNodeNone(t *testing.T) {
	c := lrTestEnv(t, "", false)
	log := b690dFakeNode(t)
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--dry-run")
	lrTestCheck(t, "node none", code, out, errText, 0, c.golden(t, "dry.stdout"), "")
	if !strings.Contains(out, "- Node: none\n") {
		t.Errorf("report lacks \"- Node: none\":\n%s", out)
	}
	b690dNoNodeRun(t, log)
}

// A release build stamps main.version; the pack header names that version,
// not the install's package.json.
func TestB690dContextPackNamesGoVersion(t *testing.T) {
	f := newContextFixture(t, b572Blocks, b572Config)
	old := version
	version = "1.2.3"
	t.Cleanup(func() { version = old })
	got := b572Pack(t, f)
	if !strings.Contains(got, "\nversion: 1.2.3\n") || strings.Contains(got, "9.8.7") {
		t.Fatalf("pack does not name the Go version:\n%s", got)
	}
	if _, err := os.Stat(f.nodeMarker); !os.IsNotExist(err) {
		t.Fatalf("node ran: %v", err)
	}
}
