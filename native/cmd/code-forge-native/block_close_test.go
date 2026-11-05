package main

import (
	"testing"
)

func TestBlockCloseNativeUsage(t *testing.T) {
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	for _, args := range [][]string{{"close"}, {"close", "B1"}, {"close", "B1", "--run"}, {"close", "B1", "--run", "r-test", "--json=true"}} {
		if code := runBlock(args); code != 2 {
			t.Errorf("block %v: usage exit = %d, want 2 without Node", args, code)
		}
	}
}
