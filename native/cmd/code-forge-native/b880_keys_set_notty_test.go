package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// B880 (B801 audit P15): `keys set <name>` with no terminal on stdin refuses
// before the hidden prompt is drawn: exit 2, the usage, why, and the --op
// form to store a 1Password reference instead. The sample is the audit's
// exact command, `keys set jev`.

// b880StdinTTY makes stdin a terminal (or not) for the test.
func b880StdinTTY(t *testing.T, tty bool) {
	t.Helper()
	saved := keysStdinTTY
	keysStdinTTY = func() bool { return tty }
	t.Cleanup(func() { keysStdinTTY = saved })
}

// b880Refusal is the refusal of `keys set <name>` with no terminal.
func b880Refusal(name string) string {
	return keysSubUsage["set"] +
		"keys set: stdin is not a terminal, so the value of " + name + " cannot be asked (input hidden); run it in a terminal, or store a 1Password reference with --op\n" +
		"next: code-forge keys set " + name + " --op <item-id|link|op://ref>\n"
}

func TestB880KeysSetNoTTYRefuses(t *testing.T) {
	dir, project := b456KeysHome(t, "", nil)
	b880StdinTTY(t, false)
	saved := keysPromptOpen
	t.Cleanup(func() { keysPromptOpen = saved })
	keysPromptOpen = func() (keysTTY, bool) {
		t.Fatal("the prompt was opened with no terminal on stdin")
		return keysTTY{}, false
	}
	code, stdout, stderr := b456Keys(t, project, "set", "jev")
	if code != 2 || stdout != "" || stderr != b880Refusal("jev") {
		t.Fatalf("exit %d stdout %q\nstderr %q\nwant   %q", code, stdout, stderr, b880Refusal("jev"))
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the store was touched: %v", err)
	}
	// --op never needs the terminal: it goes on to check the reference
	_, _, stderr = b456Keys(t, project, "set", "jev", "--op", "junk")
	if stderr != "jev: --op must be a 1Password item ID, item link or op://vault/item/field reference\n" {
		t.Fatalf("--op: stderr %q", stderr)
	}
}

// The audit's run: the binary with stdin not a terminal (/dev/null).
func TestB880KeysSetNoTTYBinary(t *testing.T) {
	bin := b553Binary(t)
	home := t.TempDir()
	cmd := exec.Command(bin, "keys", "set", "jev")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "CODE_FORGE_KEY_BACKEND=file", "CODE_FORGE_NO_ERROR_LOG=1", "TERM=xterm-256color"}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	if code != 2 || stdout.String() != "" || stderr.String() != b880Refusal("jev") {
		t.Fatalf("exit %d stdout %q\nstderr %q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".code-forge", "store")); !os.IsNotExist(err) {
		t.Fatalf("the store was touched: %v", err)
	}
}
