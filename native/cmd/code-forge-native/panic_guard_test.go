package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const b508Secret = "sk-test-SECRET-do-not-print"

type b508Result struct {
	code   int
	err    error
	stdout string
	stderr string
	home   string
}

// b508Run runs main's guarded entry in-process with a panic seam, a temp
// HOME and no Node (CODE_FORGE_NODE points nowhere, so a hand-over is 127).
func b508Run(t *testing.T, seam func([]string), args ...string) b508Result {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	old := panicSeam
	panicSeam = seam
	defer func() { panicSeam = old }()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	outC, errC := make(chan string), make(chan string)
	go func() { data, _ := io.ReadAll(outR); outC <- string(data) }()
	go func() { data, _ := io.ReadAll(errR); errC <- string(data) }()
	res := b508Result{home: home}
	func() {
		defer func() {
			if v := recover(); v != nil {
				res.code, res.err = -1, fmt.Errorf("panic escaped runGuarded: %v", v)
			}
		}()
		res.code, res.err = runGuarded(args)
	}()
	os.Stdout, os.Stderr = stdout, stderr
	_ = outW.Close()
	_ = errW.Close()
	res.stdout, res.stderr = <-outC, <-errC
	return res
}

func b508Rows(t *testing.T, home string) []handoverRow {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
	if err != nil {
		return nil
	}
	var rows []handoverRow
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row handoverRow
		if json.Unmarshal([]byte(line), &row) == nil {
			rows = append(rows, row)
		}
	}
	return rows
}

func b508Panic([]string) { panic("b508 seam panic: token=" + b508Secret) }

func b508NoRawPanic(t *testing.T, args []string, text string) {
	t.Helper()
	for _, bad := range []string{"goroutine ", "panic:", b508Secret, "[running]"} {
		if strings.Contains(text, bad) {
			t.Errorf("%v: output shows %q: %q", args, bad, text)
		}
	}
}

// A panic in a verb that only reads (Go wrote no state) goes to Node with
// the original arguments and one go-panic hand-over row; never exit 2.
func TestB508PanicInReadOnlyVerbHandsOver(t *testing.T) {
	cases := []struct {
		args      []string
		verb, sub string
	}{
		{[]string{"status", "--json"}, "status", "unknown"},
		{[]string{"setup"}, "setup", ""},
		{[]string{"run", "status", "--run", "r-1"}, "run", "status"},
		{[]string{"queue"}, "queue", ""},
		{[]string{"queue", "status", "--json"}, "queue", "status"},
		{[]string{"heal", "list"}, "heal", "list"},
		{[]string{"heal", "show", "h-1"}, "heal", "show"},
		{[]string{"decisions", "list"}, "decisions", "list"},
		{[]string{"docs", "path"}, "docs", "path"},
		{[]string{"logs"}, "logs", ""},
		{[]string{"logs", "summary"}, "logs", "summary"},
		{[]string{"logs", "path"}, "logs", "path"},
		{[]string{"logs", "handovers", "--json"}, "logs", "handovers"},
		{[]string{"ledger", "tail", "--slug", "unit"}, "ledger", "tail"},
	}
	for _, c := range cases {
		res := b508Run(t, b508Panic, c.args...)
		if res.code != 127 || res.err == nil || !strings.Contains(res.err.Error(), "node executable") {
			t.Errorf("%v: code=%d err=%v; want the Node hand-over (127 with no Node)", c.args, res.code, res.err)
		}
		b508NoRawPanic(t, c.args, res.stdout+res.stderr)
		rows := b508Rows(t, res.home)
		if len(rows) != 1 || rows[0].Reason != "go-panic" || rows[0].Verb != c.verb || rows[0].Sub != c.sub {
			t.Errorf("%v: hand-over rows %+v; want one go-panic row for %s %q", c.args, rows, c.verb, c.sub)
		}
	}
}

// A panic in a verb that may have written state is never re-run in Node:
// one error line naming the command and the panic site, exit 1.
func TestB508PanicInWritingVerbStops(t *testing.T) {
	cases := []struct {
		args []string
		name string
	}{
		{[]string{"block", "close", "B1", "--report", "private-report.md"}, "block close"},
		{[]string{"block", "land", "B1"}, "block land"},
		{[]string{"run", "end", "--run", "r-1"}, "run end"},
		{[]string{"run", "start", "--reattach"}, "run start"},
		{[]string{"gates", "run", "--run", "r-1"}, "gates run"},
		{[]string{"queue", "run", "--", "echo", "private-arg"}, "queue run"},
		{[]string{"logs", "clear"}, "logs clear"},
		{[]string{"ledger", "add"}, "ledger add"},
		{[]string{"docs", "sync"}, "docs sync"},
		{[]string{"heal", "run"}, "heal run"},
		{[]string{"review-file", "private-file.go"}, "review-file"},
		{[]string{"internal", "serve"}, "unknown"},
	}
	for _, c := range cases {
		res := b508Run(t, b508Panic, c.args...)
		if res.code != 1 || res.err != nil {
			t.Errorf("%v: code=%d err=%v; want exit 1 and no hand-over", c.args, res.code, res.err)
		}
		b508NoRawPanic(t, c.args, res.stdout+res.stderr)
		want := "code-forge: internal error in the Go engine (" + c.name + ", at "
		if !strings.HasPrefix(res.stderr, want) || strings.Count(res.stderr, "\n") != 1 || !strings.HasSuffix(res.stderr, "\n") {
			t.Errorf("%v: stderr %q; want one line starting %q", c.args, res.stderr, want)
		}
		if !strings.Contains(res.stderr, ", at b508Panic panic_guard_test.go:") || !strings.Contains(res.stderr, "Node did not re-run it") {
			t.Errorf("%v: stderr %q; want the panic site and the no-rerun note", c.args, res.stderr)
		}
		for _, private := range []string{"private-report.md", "private-arg", "private-file.go", "r-1", "B1"} {
			if strings.Contains(res.stderr, private) {
				t.Errorf("%v: stderr names argument value %q: %q", c.args, private, res.stderr)
			}
		}
		rows := b508Rows(t, res.home)
		if len(rows) != 1 || rows[0].Reason != "go-panic-stopped" {
			t.Errorf("%v: hand-over rows %+v; want one go-panic-stopped row", c.args, rows)
		}
	}
}

// Without a panic the guard changes nothing: usage errors keep exit 2 and
// no hand-over row is written.
func TestB508NoPanicKeepsResults(t *testing.T) {
	for _, args := range [][]string{{"setup", "extra"}, {"decisions"}, {"docs"}} {
		res := b508Run(t, nil, args...)
		if res.code != 2 || res.err != nil {
			t.Errorf("%v: code=%d err=%v; want the usage exit 2", args, res.code, res.err)
		}
		if strings.Contains(res.stderr, "internal error") {
			t.Errorf("%v: stderr %q", args, res.stderr)
		}
		if rows := b508Rows(t, res.home); len(rows) != 0 {
			t.Errorf("%v: rows %+v; want none", args, rows)
		}
	}
	res := b508Run(t, nil, "setup", "--help")
	if res.code != 0 || res.stdout != helpExpect(t, []string{"setup", "--help"}, 0, setupUsage) {
		t.Errorf("setup --help: code=%d stdout=%q", res.code, res.stdout)
	}
}

// The real launcher (bin/code-forge) with a Go binary built with the
// cfpanicseam tag, which panics in every command: Node gets a read-only
// command (setup included), a writing command stops with exit 1.
func TestB508LauncherWithPanickingBinary(t *testing.T) {
	host := map[string]string{"darwin": "darwin", "linux": "linux"}[runtime.GOOS]
	arch := map[string]string{"arm64": "arm64", "amd64": "x64"}[runtime.GOARCH]
	if host == "" || arch == "" {
		t.Skip("no launcher target for this host")
	}
	pkg, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	binDir := filepath.Join(pkg, "native", "bin", host+"-"+arch)
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(pkg, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-tags", "cfpanicseam", "-o", filepath.Join(binDir, "code-forge-native"), ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	launcher, err := os.ReadFile(filepath.Join("..", "..", "..", "bin", "code-forge"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "bin", "code-forge"), launcher, 0o755); err != nil {
		t.Fatal(err)
	}
	// A fake Node: records its arguments and answers like the command ran.
	fakeBin := t.TempDir()
	record := filepath.Join(t.TempDir(), "node-args")
	fake := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + record + "\necho 'node served it'\nexit 0\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "node"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	launch := func(args ...string) (int, string, string, string) {
		_ = os.Remove(record)
		cmd := exec.Command(filepath.Join(pkg, "bin", "code-forge"), args...)
		cmd.Dir = pkg
		cmd.Env = []string{"HOME=" + home, "PATH=" + fakeBin + ":/usr/bin:/bin", "CODE_FORGE_TEST_PANIC=1", "CODE_FORGE_NO_ERROR_LOG=1"}
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("%v: %v", args, err)
			}
			code = exit.ExitCode()
		}
		got, _ := os.ReadFile(record)
		return code, stdout.String(), stderr.String(), string(got)
	}
	for _, args := range [][]string{{"status"}, {"setup"}, {"logs", "summary"}} {
		code, stdout, stderr, nodeArgs := launch(args...)
		want := filepath.Join(pkg, "bin", "code-forge.mjs") + "\n" + strings.Join(args, "\n") + "\n"
		if code != 0 || stdout != "node served it\n" || nodeArgs != want {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q node args=%q; want Node to run the command", args, code, stdout, stderr, nodeArgs)
		}
		b508NoRawPanic(t, args, stdout+stderr)
	}
	code, stdout, stderr, nodeArgs := launch("block", "close", "B1", "--report", "private-report.md")
	if code != 1 || nodeArgs != "" || stdout != "" || !strings.HasPrefix(stderr, "code-forge: internal error in the Go engine (block close, at ") || strings.Count(stderr, "\n") != 1 {
		t.Errorf("block close: exit=%d stdout=%q stderr=%q node args=%q; want one error line, exit 1, no Node", code, stdout, stderr, nodeArgs)
	}
	b508NoRawPanic(t, []string{"block", "close"}, stdout+stderr)
	if strings.Contains(stderr, "private-report.md") {
		t.Errorf("block close: stderr names an argument value: %q", stderr)
	}
	reasons := []string{}
	for _, row := range b508Rows(t, home) {
		reasons = append(reasons, row.Verb+" "+row.Sub+" "+row.Reason)
	}
	if strings.Join(reasons, ",") != "status  go-panic,setup  go-panic,logs summary go-panic,block close go-panic-stopped" {
		t.Errorf("hand-over rows %q", reasons)
	}
}
