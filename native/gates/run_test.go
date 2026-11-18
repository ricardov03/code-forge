package gates

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecuteSignalNames(t *testing.T) {
	for _, signal := range []string{"HUP", "USR1", "USR2", "PIPE", "ALRM", "TERM", "INT", "KILL"} {
		t.Run(signal, func(t *testing.T) {
			r := execute(context.Background(), []string{"/bin/sh", "-c", "kill -" + signal + " $$"}, nil, t.TempDir(), "", 30000, false)
			want := "gate terminated: SIG" + signal
			if r.Get("reason") != "SIG"+signal || r.Get("message") != want || r.Get("stderr") != want+"\n" || r.Get("code") != nil {
				t.Fatalf("signal %s: %v", signal, r)
			}
		})
	}
}

// TestExecuteGateGetsDefaultForIgnoredSignals: a gates run whose own process
// ignores SIGHUP (started under nohup) or SIGINT (a script's background job)
// must still give its gate the default action, as Node's spawn does. Before
// B719 the gate shell inherited SIG_IGN, survived `kill -HUP $$` and the gate
// passed with code 0.
func TestExecuteGateGetsDefaultForIgnoredSignals(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
	}{{"HUP", syscall.SIGHUP}, {"INT", syscall.SIGINT}} {
		t.Run(tc.name, func(t *testing.T) {
			signal.Ignore(tc.sig)
			defer signal.Reset(tc.sig)
			r := execute(context.Background(), []string{"/bin/sh", "-c", "kill -" + tc.name + " $$; echo survived"}, nil, t.TempDir(), "", 5000, false)
			want := "gate terminated: SIG" + tc.name
			if r.Get("ok") != false || r.Get("reason") != "SIG"+tc.name || r.Get("stdout") != "" || r.Get("stderr") != want+"\n" || r.Get("code") != nil {
				t.Fatalf("parent ignores SIG%s: %v", tc.name, r)
			}
		})
	}
}

// gateSignalStateScript prints, for each signal, its action (DEFAULT, IGNORE)
// and whether it is blocked, as the gate program sees them at its start.
const gateSignalStateScript = `use POSIX; my $s = POSIX::SigSet->new; sigprocmask(SIG_BLOCK, undef, $s); for my $n (qw(HUP INT QUIT PIPE TERM USR1 USR2 TSTP TTIN TTOU)) { my $num = eval "SIG$n"; my $o = POSIX::SigAction->new; sigaction($num, undef, $o); print "$n ", $o->handler, " ", ($s->ismember($num) ? "blocked" : "open"), "\n" }`

// TestExecuteGateSignalStateHelper is run only by
// TestExecuteGateStartsWithDefaultSignalsAndEmptyMask, inside a process that
// inherited ignored and blocked signals. It prints the gate's report.
func TestExecuteGateSignalStateHelper(t *testing.T) {
	if os.Getenv("CODE_FORGE_B721_HELPER") != "1" {
		t.Skip("helper process only")
	}
	r := execute(context.Background(), []string{"perl", "-e", gateSignalStateScript}, nil, t.TempDir(), "", 30000, false)
	fmt.Printf("B721-STDOUT<<%v>>\nB721-CODE<<%v>>\n", r.Get("stdout"), r.Get("code"))
}

// runGateSignalStateHelper runs TestExecuteGateSignalStateHelper as the
// engine process under perl, which first ignores the signals in ignore and
// blocks the ones in block (both space-separated names), then execs the test
// binary. It returns the gate's report: one "NAME ACTION open|blocked" line
// per signal.
func runGateSignalStateHelper(t *testing.T, ignore, block string) string {
	t.Helper()
	if _, err := exec.LookPath("perl"); err != nil {
		t.Skip("perl not found")
	}
	setup := `use POSIX; $SIG{$_} = "IGNORE" for split / /, $ENV{B721_IGNORE}; sigprocmask(SIG_BLOCK, POSIX::SigSet->new(map { eval "SIG$_" } split / /, $ENV{B721_BLOCK})) or die "sigprocmask: $!"; exec @ARGV or die "exec: $!"`
	cmd := exec.Command("perl", "-e", setup, os.Args[0], "-test.count=1", "-test.run=^TestExecuteGateSignalStateHelper$")
	cmd.Env = append(os.Environ(), "CODE_FORGE_B721_HELPER=1", "B721_IGNORE="+ignore, "B721_BLOCK="+block)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper: %v\n%s", err, out)
	}
	text := string(out)
	from := strings.Index(text, "B721-STDOUT<<")
	to := strings.Index(text, ">>\nB721-CODE<<0>>")
	if from < 0 || to < from {
		t.Fatalf("helper gave no gate report with code 0:\n%s", out)
	}
	return text[from+len("B721-STDOUT<<") : to]
}

const gateStateAllDefaultOpen = "HUP DEFAULT open\nINT DEFAULT open\nQUIT DEFAULT open\nPIPE DEFAULT open\nTERM DEFAULT open\nUSR1 DEFAULT open\nUSR2 DEFAULT open\nTSTP DEFAULT open\nTTIN DEFAULT open\nTTOU DEFAULT open\n"

// TestExecuteGateGetsDefaultForInheritedIgnoredJobControlSignals: a gates run
// started as a job-control shell's background job, or by a parent that set
// SIG_IGN for SIGTSTP/SIGTTIN/SIGTTOU, must give its gate SIG_DFL for them, as
// Node's spawn does (libuv resets 1-31 to SIG_DFL). Before B721 the Go runtime
// left these three alone, signal.Ignored did not report them, and the gate
// started with them ignored.
func TestExecuteGateGetsDefaultForInheritedIgnoredJobControlSignals(t *testing.T) {
	got := runGateSignalStateHelper(t, "HUP INT QUIT PIPE TERM USR1 USR2 TSTP TTIN TTOU", "")
	if got != gateStateAllDefaultOpen {
		t.Fatalf("gate signal actions, want every signal DEFAULT:\n%s", got)
	}
}

// TestExecuteGateStartsWithEmptySignalMask: a gates run whose own process
// inherited a blocked-signal mask must start its gate with nothing blocked, as
// Node's spawn does (libuv clears the mask in the child). Before B721 the gate
// kept PIPE, USR1, USR2, TSTP, TTIN and TTOU blocked (the Go runtime unblocks
// only the signals it needs, on its own threads).
func TestExecuteGateStartsWithEmptySignalMask(t *testing.T) {
	got := runGateSignalStateHelper(t, "", "HUP INT QUIT PIPE TERM USR1 USR2 TSTP TTIN TTOU")
	if got != gateStateAllDefaultOpen {
		t.Fatalf("gate signal mask, want every signal open:\n%s", got)
	}
}

func TestExecuteSpawnErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "no-execute")
	if err := os.WriteFile(file, []byte("#!/bin/sh\nexit 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, code string }{{file, "EACCES"}, {filepath.Join(dir, "missing"), "ENOENT"}, {file + "/child", "ENOTDIR"}} {
		r := execute(context.Background(), []string{tc.name}, nil, dir, "", 1000, false)
		want := "gate terminated: spawn " + tc.name + " " + tc.code
		if r.Get("reason") != "spawn-error" || r.Get("message") != want || r.Get("stderr") != want+"\n" || r.Get("code") != nil {
			t.Errorf("%s: %v", tc.code, r)
		}
	}
}

func TestSpawnErrorCode(t *testing.T) {
	for code, errno := range map[string]syscall.Errno{"EFAULT": syscall.EFAULT, "EISDIR": syscall.EISDIR, "EINTR": syscall.EINTR, "ENOSYS": syscall.ENOSYS} {
		err := &os.PathError{Op: "fork/exec", Path: "gate", Err: errno}
		if got := spawnErrorCode(err); got != code {
			t.Errorf("%s: got %s", code, got)
		}
	}
}

func TestExecuteDoesNotSearchParentPATH(t *testing.T) {
	r := execute(context.Background(), []string{"sh", "-c", "printf parent-path-was-used"}, map[string]string{"PATH": t.TempDir()}, t.TempDir(), "", 1000, false)
	if r.Get("reason") != "spawn-error" || r.Get("stdout") != "" || r.Get("stderr") != "gate terminated: spawn sh ENOENT\n" {
		t.Fatalf("child PATH must be authoritative: %v", r)
	}
}

func niceOf(t *testing.T, pid int) int {
	t.Helper()
	out, err := exec.Command("/bin/ps", "-o", "nice=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestExecuteRunsTestChildAtNice10(t *testing.T) {
	// The child reads its own nice value once, as its first step, and reports
	// it in a file. The parent's post-start step is held until that report
	// exists, so a slow parent can never set the value before the child reads
	// it: only a child that started at nice 10 passes. A parent already above
	// 10 keeps its value.
	want := 10
	if parent := niceOf(t, os.Getpid()); parent > want {
		want = parent
	}
	dir := t.TempDir()
	report := filepath.Join(dir, "nice")
	calls, reported := 0, ""
	old := afterGateStart
	afterGateStart = func(pid int) {
		calls++
		// Wait for the report event; the cap only ends a child that never reports.
		for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if data, err := os.ReadFile(report); err == nil {
				reported = string(data)
				return
			}
		}
		reported = "no report"
	}
	defer func() { afterGateStart = old }()
	script := "n=$(/bin/ps -o nice= -p $$ | tr -d ' '); printf %s \"$n\" > \"$0.tmp\"; mv \"$0.tmp\" \"$0\"; printf %s \"$n\""
	p := Plan{Commands: map[string]*Command{"test": {Argv: []string{"/bin/sh", "-c", script, report}}}}
	results, _, err := Run(context.Background(), p, dir, "fixture", 30000, Scope{})
	if err != nil {
		t.Fatal(err)
	}
	r := results[0].(interface{ Get(string) any })
	if calls != 1 || reported != strconv.Itoa(want) {
		t.Fatalf("post-start step: calls=%d child reported nice %q before it, want 1 call and %d", calls, reported, want)
	}
	if r.Get("gate") != "test" || r.Get("ok") != true || r.Get("stdout") != strconv.Itoa(want) {
		t.Fatalf("test gate child nice: gate=%v ok=%v stdout=%q, want %d", r.Get("gate"), r.Get("ok"), r.Get("stdout"), want)
	}
}

func TestExecuteHelper(t *testing.T) {
	if os.Getenv("CF_PIPE_HELPER") != "1" {
		return
	}
	// A slow start (loaded CI runner) is simulated by CF_PIPE_START_DELAY_MS.
	if ms, _ := strconv.Atoi(os.Getenv("CF_PIPE_START_DELAY_MS")); ms > 0 {
		time.Sleep(time.Duration(ms) * time.Millisecond)
	}
	// Leave the gate's process group while retaining both inherited pipe ends.
	child := exec.Command("/bin/sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	child.Stdout, child.Stderr = os.Stdout, os.Stderr
	if err := child.Start(); err != nil {
		os.Exit(98)
	}
	// Write then rename, so the parent never reads a partial pid file.
	pidFile := os.Getenv("CF_PIPE_PID")
	if err := os.WriteFile(pidFile+".tmp", []byte(strconv.Itoa(child.Process.Pid)), 0600); err != nil {
		os.Exit(99)
	}
	if err := os.Rename(pidFile+".tmp", pidFile); err != nil {
		os.Exit(99)
	}
	// Stay alive until the gate deadline terminates us, leaving the detached
	// descendant holding the output pipes after Wait has returned.
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

func TestRunTimeoutWithLeakedDescendant(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "descendant.pid")
	defer func() {
		data, _ := os.ReadFile(pidFile)
		if pid, _ := strconv.Atoi(strings.TrimSpace(string(data))); pid > 1 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	}()
	// The gate's timeout clock starts after afterGateStart returns. Hold it
	// until the descendant holds both pipes (its pid file exists), so a slow
	// start on a loaded runner can never let the timeout fire first (B776).
	// The cap only ends a helper that never starts its descendant.
	ready := make(chan bool, 1)
	old := afterGateStart
	afterGateStart = func(pid int) {
		for limit := time.Now().Add(2 * time.Minute); time.Now().Before(limit); time.Sleep(5 * time.Millisecond) {
			if _, err := os.Stat(pidFile); err == nil {
				ready <- true
				return
			}
		}
		ready <- false
	}
	defer func() { afterGateStart = old }()
	const timeoutMs = 500.0
	// Includes TERM grace and bounded pipe drain. A run that waited on the
	// leaked descendant would take its full 30 s sleep, so 10 s from the
	// moment the descendant holds the pipes still tells the two apart while
	// leaving room for a loaded machine (B539).
	deadline := 10 * time.Second
	done := make(chan error, 1)
	// The helper starts its descendant 700 ms late, past the 500 ms timeout,
	// so every run proves the timeout clock waits for the ready point.
	go func() {
		p := Plan{Commands: map[string]*Command{"test": {Argv: []string{exe, "-test.run=^TestExecuteHelper$"}, Env: map[string]string{"CF_PIPE_HELPER": "1", "CF_PIPE_PID": pidFile, "CF_PIPE_START_DELAY_MS": "700"}}}}
		results, ok, err := Run(context.Background(), p, dir, "fixture", timeoutMs, Scope{})
		if err == nil && (ok || len(results) != 4) {
			err = fmt.Errorf("Run returned ok=%v, %d results; want failed and four results", ok, len(results))
		}
		if err == nil {
			// Run returns ledger.Objects, including the timed-out test gate.
			r := results[0].(interface{ Get(string) any })
			if r.Get("reason") != "timeout" || r.Get("timed_out") != true {
				err = fmt.Errorf("gate outcome: reason=%v timed_out=%v", r.Get("reason"), r.Get("timed_out"))
			}
		}
		done <- err
	}()
	select {
	case up := <-ready:
		if !up {
			t.Fatal("helper never started its descendant within 2 min")
		}
	case err := <-done:
		t.Fatal("gate run ended before its descendant held the pipes:", err)
	}
	started := time.Now()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatal("descendant never held the pipes:", err)
		}
		pid, err := strconv.Atoi(string(data))
		if err != nil || syscall.Kill(pid, 0) != nil {
			t.Fatal("descendant must still be alive and holding the pipes when the run returns")
		}
		t.Logf("gate deadline %g ms, leaked-pipe run ended in %s", timeoutMs, time.Since(started))
	case <-time.After(deadline):
		t.Fatal("gate run exceeded its bounded timeout with a leaked descendant holding both pipes")
	}
}
