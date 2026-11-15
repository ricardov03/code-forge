package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// B748: run start's sweep reads the process table once per dead temp root and
// one `ps -E` per candidate process; both made a start take seconds.

func b748DeadRoot(t *testing.T, base, name string, pids ...int) {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(filepath.Join(dir, "pids"), 0o700); err != nil {
		t.Fatal(err)
	}
	// a pid above MaxInt32 is never alive: the owner is dead
	if err := os.WriteFile(filepath.Join(dir, "owner.json"), []byte(`{"pid":99999999999,"start_time":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, pid := range pids {
		entry := `{"pid":` + strconv.Itoa(pid) + `,"start_time":"Sun Nov 15 10:00:00 2026"}`
		if err := os.WriteFile(filepath.Join(dir, "pids", strconv.Itoa(pid)+".json"), []byte(entry), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func b748CountTable(t *testing.T) *int {
	t.Helper()
	calls := 0
	before := runReadProcesses
	runReadProcesses = func() []runProc { calls++; return before() }
	t.Cleanup(func() { runReadProcesses = before })
	return &calls
}

// TestB748SweepRootsSkipsEmptyRegistry: dead roots with nothing registered
// are still removed, without reading the process table for each of them.
func TestB748SweepRootsSkipsEmptyRegistry(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		b748DeadRoot(t, base, "proc-"+strconv.Itoa(i))
	}
	calls := b748CountTable(t)
	runSweepRoots(base)
	left, _ := os.ReadDir(base)
	if len(left) != 0 || *calls != 0 {
		t.Fatalf("left=%d roots, process table read %d times; want 0 and 0", len(left), *calls)
	}
}

// TestB748SweepRootsRegistryStillChecked: a dead root with a registered pid
// that has exited still reads the table and drops the entry with the root.
func TestB748SweepRootsRegistryStillChecked(t *testing.T) {
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	b748DeadRoot(t, base, "proc-a", gone.Process.Pid)
	b748DeadRoot(t, base, "proc-b")
	calls := b748CountTable(t)
	runSweepRoots(base)
	left, _ := os.ReadDir(base)
	if len(left) != 0 || *calls != 1 {
		t.Fatalf("left=%d roots, process table read %d times; want 0 and 1", len(left), *calls)
	}
}

// TestB748Helper is the child process the tests below start.
func TestB748Helper(t *testing.T) {
	if os.Getenv("B748_HELPER") != "1" {
		t.Skip("child process only")
	}
	time.Sleep(30 * time.Second)
}

type b748Child struct {
	cmd *exec.Cmd
	row runProc
}

// b748Spawn starts `sleep 30` with env (markers first) and returns its row
// from the real process table.
func b748Spawn(t *testing.T, env ...string) b748Child {
	t.Helper()
	// the test binary itself: ps -E shows no environment of a platform binary
	cmd := exec.Command(os.Args[0], "-test.run=^TestB748Helper$")
	cmd.Env = append(env, "B748_HELPER=1", "PATH=/usr/bin:/bin")
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

func b748CountEnvCalls(t *testing.T) *int {
	t.Helper()
	calls := 0
	before := runPsEnvCall
	runPsEnvCall = func(pids []string) (string, bool) { calls++; return before(pids) }
	t.Cleanup(func() { runPsEnvCall = before })
	return &calls
}

func b748SameEnv(t *testing.T, label string, got, want map[string]string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) || (got == nil) != (want == nil) {
		t.Fatalf("%s: batched %#v, per process %#v", label, got, want)
	}
}

// TestB748EnvsOneCallMatchesPerProcess: the markers of many processes come
// from one ps call on darwin and equal what a call per process reads.
func TestB748EnvsOneCallMatchesPerProcess(t *testing.T) {
	children := []b748Child{}
	for i := 0; i < 4; i++ {
		children = append(children, b748Spawn(t,
			"HOME=/h/"+strconv.Itoa(i),
			"CODE_FORGE_OWNER_PID="+strconv.Itoa(4000+i),
			"CODE_FORGE_RUN_ID=r-b748-"+strconv.Itoa(i),
			"CODE_FORGE_OWNER_START_TIME=Sun Nov 15 10:00:0"+strconv.Itoa(i)+" 2026",
			"B748_NOTE=line one\nline two"))
	}
	gone := exec.Command("/bin/sleep", "30")
	if err := gone.Start(); err != nil {
		t.Fatal(err)
	}
	goneRow := runProc{uid: os.Getuid(), pid: gone.Process.Pid, ppid: os.Getpid(), state: "S", start: "Sun Nov 15 10:00:00 2026", command: "/bin/sleep 30"}
	_ = gone.Process.Kill()
	_ = gone.Wait()
	cands := []runProc{goneRow}
	for _, c := range children {
		cands = append(cands, c.row)
	}
	calls := b748CountEnvCalls(t)
	envs := runProcessEnvs(cands)
	wantCalls := 0
	if runtime.GOOS == "darwin" {
		wantCalls = 1
	}
	if *calls != wantCalls {
		t.Fatalf("ps -E calls = %d, want %d", *calls, wantCalls)
	}
	if env, ok := envs[goneRow]; !ok || env != nil {
		t.Fatalf("exited process: %#v (listed %v), want nil", env, ok)
	}
	for i, c := range children {
		env := envs[c.row]
		b748SameEnv(t, "child "+strconv.Itoa(i), env, runProcessEnv(c.row))
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			continue
		}
		if env["HOME"] != "/h/"+strconv.Itoa(i) || env["CODE_FORGE_OWNER_PID"] != strconv.Itoa(4000+i) || env["CODE_FORGE_RUN_ID"] != "r-b748-"+strconv.Itoa(i) {
			t.Fatalf("child %d markers %#v", i, env)
		}
	}
}

// TestB748EnvsSpoofedHeaderReadsEachProcess: an environment value that holds
// another candidate's ps header cannot lend that candidate its markers.
func TestB748EnvsSpoofedHeaderReadsEachProcess(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("ps -E batching is darwin only")
	}
	victim := b748Spawn(t, "HOME=/h/victim", "CODE_FORGE_OWNER_PID=4100")
	spoof := "\n" + strconv.Itoa(victim.row.pid) + " " + strconv.Itoa(victim.row.ppid) + " " + victim.row.start + " " + victim.row.command + " HOME=/h/evil CODE_FORGE_OWNER_PID=4999 X=1"
	liar := b748Spawn(t, "HOME=/h/liar", "B748_NOTE="+spoof)
	calls := b748CountEnvCalls(t)
	envs := runProcessEnvs([]runProc{liar.row, victim.row})
	if *calls != 1 {
		t.Fatalf("ps -E calls = %d, want 1", *calls)
	}
	b748SameEnv(t, "victim", envs[victim.row], runProcessEnv(victim.row))
	b748SameEnv(t, "liar", envs[liar.row], runProcessEnv(liar.row))
	if envs[victim.row]["HOME"] != "/h/victim" || envs[victim.row]["CODE_FORGE_OWNER_PID"] != "4100" {
		t.Fatalf("victim markers %#v", envs[victim.row])
	}
}

// TestB748SweepItemsOneEnvCall: the worker and orphan search reads every
// candidate's markers with one ps call and still finds a dead owner's orphan.
func TestB748SweepItemsOneEnvCall(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("ps -E batching is darwin only")
	}
	home := t.TempDir()
	orphan := b748Spawn(t, "HOME="+home, "CODE_FORGE_OWNER_PID=99999999999")
	other := b748Spawn(t, "HOME="+home)
	rows := []runProc{}
	for _, c := range []b748Child{orphan, other} {
		row := c.row
		row.ppid = 1 // the search reads children of pid 1
		rows = append(rows, row)
	}
	beforeRows := runReadProcesses
	runReadProcesses = func() []runProc { return append([]runProc{}, rows...) }
	t.Cleanup(func() { runReadProcesses = beforeRows })
	beforeCall := runPsEnvCall
	calls := 0
	// ps prints the real ppid; show the rows as children of pid 1, as the table does
	runPsEnvCall = func(pids []string) (string, bool) {
		calls++
		out, ok := beforeCall(pids)
		for _, c := range []b748Child{orphan, other} {
			out = b748Reparent(out, c.row)
		}
		return out, ok
	}
	t.Cleanup(func() { runPsEnvCall = beforeCall })
	items := runSweepItems(home)
	if calls != 1 || len(items) != 1 || items[0].pid != orphan.row.pid {
		t.Fatalf("ps -E calls=%d items=%#v, want 1 call and the orphan %d", calls, items, orphan.row.pid)
	}
}

// b748Reparent rewrites the ppid column of row's ps line to 1.
func b748Reparent(out string, row runProc) string {
	at := 0
	for at < len(out) {
		next := len(out)
		for i := at; i < len(out); i++ {
			if out[i] == '\n' {
				next = i + 1
				break
			}
		}
		if m := runPsEnvHead.FindStringSubmatchIndex(out[at:next]); m != nil && out[at+m[2]:at+m[3]] == strconv.Itoa(row.pid) {
			return out[:at+m[4]] + "1" + out[at+m[5]:]
		}
		at = next
	}
	return out
}
