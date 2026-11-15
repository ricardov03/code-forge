package main

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// B747: `ps -E` prints a process's environment after its command, one row
// ending in "\n". The marker reader stopped a value at a line end without
// taking the row's end as the value's end, so the LAST environment variable
// was never read: a run-id filter never matched when CODE_FORGE_RUN_ID came
// last (the Go spawners append it last).

// b747Spawn starts the B748 helper (the test binary: ps -E shows no
// environment of a platform binary) with env exactly LAST in its environment.
func b747Spawn(t *testing.T, env ...string) b748Child {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestB748Helper$")
	cmd.Env = append([]string{"B748_HELPER=1", "PATH=/usr/bin:/bin"}, env...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for i := 0; i < 200; i++ {
		for _, p := range runReadProcesses() {
			if p.pid == cmd.Process.Pid && runProcessEnv(p) != nil {
				return b748Child{cmd, p}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pid %d not in the process table", cmd.Process.Pid)
	return b748Child{}
}

func b747Supported(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("markers are read on darwin and linux only")
	}
}

type b747Case struct {
	env  []string
	want map[string]string
}

// b747Cases: each marker in turn is the process's last variable; a value
// with spaces (single and double) is read whole, last or not.
func b747Cases() []b747Case {
	start := "Sun Nov 15 10:00:00 2026"
	return []b747Case{
		{[]string{"HOME=/h/run", "B747_SPACED=a b  c", "CODE_FORGE_OWNER_PID=4747", "CODE_FORGE_OWNER_START_TIME=" + start, "CODE_FORGE_RUN_ID=r-b747-last"},
			map[string]string{"HOME": "/h/run", "CODE_FORGE_OWNER_PID": "4747", "CODE_FORGE_OWNER_START_TIME": start, "CODE_FORGE_RUN_ID": "r-b747-last"}},
		{[]string{"CODE_FORGE_RUN_ID=r-b747-start", "HOME=/h/start", "CODE_FORGE_OWNER_PID=4748", "CODE_FORGE_OWNER_START_TIME=" + start},
			map[string]string{"HOME": "/h/start", "CODE_FORGE_OWNER_PID": "4748", "CODE_FORGE_OWNER_START_TIME": start, "CODE_FORGE_RUN_ID": "r-b747-start"}},
		{[]string{"HOME=/h/pid", "CODE_FORGE_RUN_ID=r-b747-pid", "B747_SPACED=x  y z", "CODE_FORGE_OWNER_PID=4749"},
			map[string]string{"HOME": "/h/pid", "CODE_FORGE_OWNER_PID": "4749", "CODE_FORGE_RUN_ID": "r-b747-pid"}},
		{[]string{"CODE_FORGE_OWNER_PID=4750", "HOME=/h/with space"},
			map[string]string{"HOME": "/h/with space", "CODE_FORGE_OWNER_PID": "4750"}},
	}
}

func b747Check(t *testing.T, label string, got, want map[string]string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: markers %#v, want %#v", label, got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s: %s = %q, want %q (all %#v)", label, k, got[k], v, got)
		}
	}
}

// TestB747LastEnvVarPerProcess: one process read on its own.
func TestB747LastEnvVarPerProcess(t *testing.T) {
	b747Supported(t)
	for i, c := range b747Cases() {
		child := b747Spawn(t, c.env...)
		b747Check(t, "case "+strconv.Itoa(i), runProcessEnv(child.row), c.want)
	}
}

// TestB747LastEnvVarBatched: several processes read with one ps call, each
// with a different marker last.
func TestB747LastEnvVarBatched(t *testing.T) {
	b747Supported(t)
	cases := b747Cases()
	cands := []runProc{}
	for _, c := range cases {
		cands = append(cands, b747Spawn(t, c.env...).row)
	}
	calls := b748CountEnvCalls(t)
	envs := runProcessEnvs(cands)
	if runtime.GOOS == "darwin" && *calls != 1 {
		t.Fatalf("ps -E calls = %d, want 1", *calls)
	}
	for i, c := range cases {
		b747Check(t, "case "+strconv.Itoa(i), envs[cands[i]], c.want)
	}
}

// TestB747RunIDFilterMatchesLastVar: the run's reader (as the sweep and
// the worker search use it) finds the run id that a Go spawner appends last.
func TestB747RunIDFilterMatchesLastVar(t *testing.T) {
	b747Supported(t)
	child := b747Spawn(t, "HOME=/h/run", "CODE_FORGE_OWNER_PID=4751", "CODE_FORGE_RUN_ID=r-b747-filter")
	read := runEnvReader([]runProc{child.row})
	if got := read(child.row)["CODE_FORGE_RUN_ID"]; got != "r-b747-filter" {
		t.Fatalf("run id %q, want r-b747-filter", got)
	}
}
