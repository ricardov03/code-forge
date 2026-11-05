package main

import (
	"io"
	"os"
	"testing"
)

func TestBlockOpenRegistered(t *testing.T) {
	if blockSubs["open"] == nil {
		t.Fatal("block open must have one native handler")
	}
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{[]string{"--level", "L0"}, "a block id and --run are required"},
		{[]string{"B1", "--level", "L0", "--run", "unit"}, "block open needs --level, --owned and --acceptance"},
		{[]string{"--level", "L0", "--tests", "*.go"}, `--tests takes exact paths (a file or a directory); "*" and "?" are refused: "*.go"`},
		{[]string{"--level", "L0", "--tests", "-x"}, `--tests: a path may not start with "-": "-x"`},
		{[]string{"--level", "L0", "--level", "L1"}, "--level given more than once"},
		{[]string{"--level"}, "--level needs a value"},
	} {
		read, write, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		before := os.Stderr
		os.Stderr = write
		code := runBlock(append([]string{"open"}, tc.args...))
		os.Stderr = before
		_ = write.Close()
		data, err := io.ReadAll(read)
		_ = read.Close()
		if err != nil {
			t.Fatal(err)
		}
		if code != 2 || string(data) != "block open: "+tc.message+"\n" {
			t.Errorf("%v: exit=%d stderr=%q; want exit=2 stderr=%q", tc.args, code, data, "block open: "+tc.message+"\n")
		}
	}
}
