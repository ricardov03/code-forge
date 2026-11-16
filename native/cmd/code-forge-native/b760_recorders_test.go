package main

import (
	"os"
	"testing"
)

// b760NoNodeRecorder skips a test when one of its Node golden recorder
// switches is set. Node was removed (B690b): bin/code-forge.mjs is gone, so a
// recorder has no Node CLI to run, and the goldens in testdata are final.
// They are never re-recorded; the test checks Go against them when the switch
// is unset.
func b760NoNodeRecorder(t *testing.T, envs ...string) {
	t.Helper()
	for _, env := range envs {
		if os.Getenv(env) != "" {
			t.Skipf("%s is set, but Node was removed (B690b): the Node recorder cannot run and the goldens in testdata are never re-recorded (B760); unset %s to check Go against them", env, env)
		}
	}
}

// TestB760NodeRecordersSkip: a set recorder switch skips the test before any
// Node run, and no fake `node` on PATH is started.
func TestB760NodeRecordersSkip(t *testing.T) {
	bin := t.TempDir()
	marker := bin + "/node-ran"
	if err := os.WriteFile(bin+"/node", []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	for _, c := range []struct {
		env, value string
		test       func(*testing.T)
	}{
		{"B697_RECORD_NODE", "/old/bin/code-forge.mjs", TestB697AutoClaimCloseMatchesNode},
		{"B703_RECORD", "1", TestB703RecordNode},
		{"B726_RECORD", "1", TestB726RecordNode},
	} {
		t.Setenv(c.env, c.value)
		var skipped bool
		t.Run(c.env, func(t *testing.T) {
			defer func() { skipped = t.Skipped() }()
			c.test(t)
		})
		if !skipped {
			t.Errorf("%s=%s did not skip", c.env, c.value)
		}
		t.Setenv(c.env, "")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a recorder started node")
	}
}
