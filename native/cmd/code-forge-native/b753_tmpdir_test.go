package main

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// b753Leakers are the tests that ran a verb in-process which makes the CLI's
// lazy run root (proc-<pid>-<ms>) and left it behind under the TMPDIR the test
// binary started with: one full package run left exactly these four roots.
var b753Leakers = []string{
	"TestAutopilotWaiveInGo",        // autopilot waive: lrProcRoot before gh
	"TestB610JevAskKeychainKey",     // jev ask: keychain read via lrRegisterChild
	"TestB505DoctorProbeCli",        // doctor probes via lrRegisterChild
	"TestB678KeysSetPromptKeychain", // keys set: keychain write via lrRegisterChild
}

// The test binary leaves nothing in the TMPDIR it was started with (B753):
// before, each run of these tests left a proc-<pid>-<ms> root with a dead
// owner in $TMPDIR/code-forge, 4 per full package run.
func TestB753TestBinaryLeavesNoRunRootInTmpdir(t *testing.T) {
	if os.Getenv("CODE_FORGE_B753_CHILD") != "" {
		t.Skip("child run")
	}
	tmp := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run", "^("+strings.Join(b753Leakers, "|")+")$", "-test.count=1")
	// a top-level `go test` run: no parent test's TMPDIR marker
	env := []string{"TMPDIR=" + tmp + "/", "CODE_FORGE_B753_CHILD=1"}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMPDIR=") && !strings.HasPrefix(kv, b753TmpEnv+"=") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child run failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "PASS") || strings.Contains(string(out), "no tests to run") {
		t.Fatalf("child did not run the tests:\n%s", out)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	left := []string{}
	for _, e := range entries {
		left = append(left, e.Name())
		if sub, err := os.ReadDir(tmp + "/" + e.Name()); err == nil {
			for _, s := range sub {
				left = append(left, e.Name()+"/"+s.Name())
			}
		}
	}
	if len(left) != 0 {
		t.Fatalf("the test binary left %v in its TMPDIR", left)
	}
}
