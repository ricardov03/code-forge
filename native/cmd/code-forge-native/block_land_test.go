package main

import (
	"io"
	"os"
	"testing"
)

func TestBlockLandNativeUsage(t *testing.T) {
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "a block id and --run are required"},
		{[]string{"B1", "--run", "r-unit"}, "block land takes one block id, --run, --report, --transcript and optional --max/--merge/--no-merge-gate/--json"},
		{[]string{"B1", "--run", "r-unit", "--report", "r", "--transcript", "t", "--max", "bad"}, "--max must look like 90s, 30m or 1500ms"},
		{[]string{"--merge=true"}, "--merge takes no value"},
		{[]string{"--merge", "--merge"}, "--merge given more than once"},
	} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		before := os.Stderr
		os.Stderr = w
		code := runBlock(append([]string{"land"}, tc.args...))
		os.Stderr = before
		_ = w.Close()
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if code != 2 || string(data) != "block land: "+tc.want+"\n" {
			t.Errorf("%v: exit=%d stderr=%q; want exit=2 stderr=%q", tc.args, code, data, "block land: "+tc.want+"\n")
		}
	}
}

func TestBlockLandDurationRefusals(t *testing.T) {
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	for _, raw := range []string{"9999999m", "1.5s", "-1"} {
		code := runBlock([]string{"land", "B1", "--run", "r-unit", "--report", "r", "--transcript", "t", "--max", raw})
		if code != 2 {
			t.Errorf("max %q: exit=%d, want 2 without Node", raw, code)
		}
	}
}
