package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestLedgerNativeArguments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b248-node")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	for _, tc := range []struct {
		args []string
		code int
		text string
	}{
		{[]string{"tail", "--slug", "s", "--n"}, 2, "ledger tail: --n requires a value\n"},
		{[]string{"tail", "--slug", "s", "-n", "-1"}, 2, "ledger tail: --n/-n must be a non-negative integer, got \"-1\"\n"},
		{[]string{"tail", "--slug", "s", "--n", "0", "-n", "1"}, 2, "ledger tail: give the count once, as --n <count> or -n <count>\n"},
	} {
		out, err := os.CreateTemp(t.TempDir(), "stderr")
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stderr
		os.Stderr = out
		code, runErr := run(append([]string{"ledger"}, tc.args...))
		os.Stderr = old
		if _, err := out.Seek(0, 0); err != nil {
			t.Fatal(err)
		}
		text, err := io.ReadAll(out)
		out.Close()
		if err != nil {
			t.Fatal(err)
		}
		if code != tc.code || runErr != nil || string(text) != tc.text {
			t.Errorf("ledger %q: got (%d, %v, %q), want (%d, nil, %q)", tc.args, code, runErr, text, tc.code, tc.text)
		}
	}
}

func TestLedgerNativeEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b248-node")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	dir := filepath.Join(home, ".code-forge", "ledger")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.jsonl"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	code, err := run([]string{"ledger", "tail", "--slug", "empty", "-n", "0"})
	if code != 0 || err != nil {
		t.Fatalf("empty tail: got (%d, %v), want (0, nil)", code, err)
	}
}
