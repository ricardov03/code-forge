package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gosafe"
)

// b509GoroutinePanic is the test seam's panic, raised inside the goroutine
// named by B509_PANIC_GOROUTINE; its value must never reach the user.
func b509GoroutinePanic(name string) {
	if name == os.Getenv("B509_PANIC_GOROUTINE") {
		panic("b509 goroutine panic: token=" + b508Secret)
	}
}

// b509PanicChild runs in the child test process: the command goes through
// runGuarded (the product entry) with the seam set. A goroutine panic must
// end the process itself; returning here is a failure.
func b509PanicChild(t *testing.T, shape, repo, tree, run, report, transcript string) {
	t.Helper()
	args := []string{"block", "land", "B1", "--run", run, "--report", report, "--transcript", transcript, "--max", "60s", "--merge", "--json"}
	if shape == "gates-run-panic" {
		args = []string{"gates", "run", "--run", run, "--block", "B1", "--changed", "--cwd", tree}
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	gosafe.Seam = b509GoroutinePanic
	code, err := runGuarded(args)
	fmt.Fprintf(os.Stderr, "B509 CHILD RETURNED code=%d err=%v\n", code, err)
	os.Exit(99)
}

// The child half of the subprocess tests below; it does nothing in a normal run.
func TestB509PanicChild(t *testing.T) {
	shape := os.Getenv("B509_CHILD")
	if shape == "" {
		t.Skip("child process of TestB509GoroutinePanicStopsWritingVerb")
	}
	blockLandChain(t, shape)
	t.Fatal("the child must not return from blockLandChain")
}

// A panic in a goroutine a writing command started (block land's gate
// goroutine, gates run's gate wait) follows the B508 rules: one error line
// naming the command and the site, a go-panic-stopped row, exit 1 (not the
// usage code 2), no stack trace, no secret, and Node never runs.
func TestB509GoroutinePanicStopsWritingVerb(t *testing.T) {
	cases := []struct {
		shape, goroutine, name, verb, sub string
	}{
		{"land-gate-panic", "land-gates", "block land", "block", "land"},
		{"gates-run-panic", "gate-wait", "gates run", "gates", "run"},
		{"gates-run-panic", "gate-stdout", "gates run", "gates", "run"},
	}
	for _, c := range cases {
		t.Run(c.goroutine, func(t *testing.T) {
			home, err := os.MkdirTemp("/tmp", "cf-b509-")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				// The gate process the child started outlives it (its own
				// process group); let it write its one line before cleanup.
				if c.shape == "gates-run-panic" {
					for i := 0; i < 100; i++ {
						if _, err := os.Stat(home + "/gate-cwds"); err == nil {
							break
						}
						time.Sleep(50 * time.Millisecond)
					}
				}
				os.RemoveAll(home)
			}()
			fakeBin := t.TempDir()
			record := home + "/node-ran"
			if err := os.WriteFile(fakeBin+"/node", []byte("#!/bin/sh\n: > "+record+"\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestB509PanicChild$", "-test.count=1")
			cmd.Env = append(os.Environ(), "B509_CHILD="+c.shape, "B509_HOME="+home, "B509_PANIC_GOROUTINE="+c.goroutine, "PATH="+fakeBin+":"+os.Getenv("PATH"))
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			code := 0
			if err := cmd.Run(); err != nil {
				exit, ok := err.(*exec.ExitError)
				if !ok {
					t.Fatal(err)
				}
				code = exit.ExitCode()
			}
			all := stdout.String() + stderr.String()
			if code != 1 {
				t.Fatalf("exit=%d, want 1 (B508 stop), not 2 (usage) or 99 (returned)\n%s", code, all)
			}
			b508NoRawPanic(t, []string{c.goroutine}, all)
			want := "code-forge: internal error in the Go engine (" + c.name + ", at b509GoroutinePanic panic_guard_b509_test.go:"
			lines := 0
			for _, line := range strings.Split(stderr.String(), "\n") {
				if strings.HasPrefix(line, "code-forge: internal error") {
					lines++
					if !strings.HasPrefix(line, want) || !strings.Contains(line, "Node did not re-run it") {
						t.Errorf("error line %q; want prefix %q", line, want)
					}
					for _, private := range []string{"r-land-unit", "B1", home} {
						if strings.Contains(line, private) {
							t.Errorf("error line names argument value %q: %q", private, line)
						}
					}
				}
			}
			if lines != 1 {
				t.Errorf("%d internal-error lines, want 1:\n%s", lines, all)
			}
			rows := b508Rows(t, home)
			if len(rows) != 1 || rows[0].Reason != "go-panic-stopped" || rows[0].Verb != c.verb || rows[0].Sub != c.sub {
				t.Errorf("hand-over rows %+v; want one go-panic-stopped row for %s %s", rows, c.verb, c.sub)
			}
			if _, err := os.Stat(record); err == nil {
				t.Error("Node ran after a writing command's goroutine panicked")
			}
		})
	}
}

// The goroutine handler in-process: a read-only command goes to Node with a
// go-panic row (exit 127 here, no Node), a writing command stops with exit 1;
// the first panic wins and the process exit is called once.
func TestB509GoroutinePanicHandler(t *testing.T) {
	cases := []struct {
		args          []string
		code          int
		reason, start string
	}{
		{[]string{"status", "--json"}, 127, "go-panic", "code-forge-native: node executable"},
		{[]string{"logs", "summary"}, 127, "go-panic", "code-forge-native: node executable"},
		{[]string{"block", "land", "B1", "--run", "r-1"}, 1, "go-panic-stopped", "code-forge: internal error in the Go engine (block land, at b509UnitPanic panic_guard_b509_test.go:"},
		{[]string{"internal", "serve", "--home", "h", "--run", "r-1"}, 1, "go-panic-stopped", "code-forge: internal error in the Go engine (unknown, at b509UnitPanic panic_guard_b509_test.go:"},
	}
	oldExit, oldStderr := goroutineExit, guardStderr
	defer func() { goroutineExit, guardStderr = oldExit, oldStderr; gosafe.SetHandler(nil) }()
	for _, c := range cases {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CODE_FORGE_NODE", "/nonexistent")
		t.Setenv("CODE_FORGE_NATIVE", "1")
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		guardStderr = w
		panicClaimed.Store(false)
		exits := make(chan int, 2)
		goroutineExit = func(code int) { exits <- code }
		args := c.args
		gosafe.SetHandler(func(_, site string) { goroutinePanic(args, site) })
		gosafe.Go("b509-unit", b509UnitPanic)
		code := <-exits
		_ = w.Close()
		data, _ := io.ReadAll(r)
		text := string(data)
		if code != c.code {
			t.Errorf("%v: exit %d, want %d", c.args, code, c.code)
		}
		if !strings.HasPrefix(text, c.start) || strings.Count(text, "\n") != 1 {
			t.Errorf("%v: stderr %q; want one line starting %q", c.args, text, c.start)
		}
		b508NoRawPanic(t, c.args, text)
		if strings.Contains(text, "r-1") {
			t.Errorf("%v: stderr names an argument value: %q", c.args, text)
		}
		rows := b508Rows(t, home)
		if len(rows) != 1 || rows[0].Reason != c.reason {
			t.Errorf("%v: rows %+v; want one %s row", c.args, rows, c.reason)
		}
		// A second panic while the first is handled never answers again.
		if !panicClaimed.Load() {
			t.Errorf("%v: the first panic did not claim the handling", c.args)
		}
		select {
		case extra := <-exits:
			t.Errorf("%v: exit called twice (%d)", c.args, extra)
		default:
		}
	}
}

func b509UnitPanic() { panic("b509 unit panic: token=" + b508Secret) }

// A main-goroutine panic inside a command whose stderr is copied through the
// heal pipe (healWrap: gates, block land, block rebase) still prints its one
// error line on the command's real stderr.
func TestB509PanicInsideHealWrapReachesStderr(t *testing.T) {
	old := routes["gates"]
	routes["gates"] = healWrap([]string{"gates"}, func([]string) int { panic("b509 heal panic: token=" + b508Secret) })
	defer func() { routes["gates"] = old }()
	res := b508Run(t, nil, "gates", "run", "--run", "r-1")
	if res.code != 1 || res.err != nil {
		t.Fatalf("code=%d err=%v; want exit 1", res.code, res.err)
	}
	want := "code-forge: internal error in the Go engine (gates run, at TestB509PanicInsideHealWrapReachesStderr.func1 panic_guard_b509_test.go:"
	if !strings.HasPrefix(res.stderr, want) || strings.Count(res.stderr, "\n") != 1 || strings.Contains(res.stderr, "r-1") {
		t.Errorf("stderr %q; want one line starting %q", res.stderr, want)
	}
	b508NoRawPanic(t, []string{"gates", "run"}, res.stdout+res.stderr)
}
