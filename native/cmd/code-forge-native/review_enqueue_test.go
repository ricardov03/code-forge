package main

import (
	"os"
	"testing"
)

func TestReviewEnqueueUsage(t *testing.T) {
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-code-forge-node")
	// These fail before root discovery, so no repository or state is needed.
	for _, args := range [][]string{{"--cwd", "/nonexistent-cf-enqueue-dir"}, {"--block"}, {"--block="}, {"--json"}, {"--"}, {"--fresh=yes"}, {"--run=a", "--run=b"}} {
		t.Run(args[0], func(t *testing.T) {
			old := os.Stderr
			f, err := os.CreateTemp(t.TempDir(), "stderr")
			if err != nil {
				t.Fatal(err)
			}
			os.Stderr = f
			defer func() { os.Stderr = old; f.Close() }()
			if code := runReviewFileWait(args); code != 2 {
				t.Fatalf("usage exit = %d, want 2 without starting Node", code)
			}
		})
	}
}
