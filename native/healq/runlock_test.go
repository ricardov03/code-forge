package healq

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/state"
)

const holdEnv = "HEALQ_TEST_HOLD_RUN_LOCK"

// TestHelperHoldRunLock is not a test: run as a child with holdEnv set, it
// takes the real run lock (state.WithRunLock writes owner.json, B423/B426),
// says "held" and blocks until killed.
func TestHelperHoldRunLock(t *testing.T) {
	run := os.Getenv(holdEnv)
	if run == "" {
		t.Skip("helper process only")
	}
	err := state.WithRunLock(os.Getenv("HOME"), run, func() error {
		os.Stdout.WriteString("held\n")
		time.Sleep(time.Hour) // select{} would trip the deadlock detector
		return nil
	})
	t.Fatal(err)
}

// holdRunLock starts a child that holds the run lock of run r1 under home.
func holdRunLock(t *testing.T, home string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldRunLock$", "-test.count=1")
	cmd.Env = append(os.Environ(), "HOME="+home, holdEnv+"=r1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "held\n" {
		t.Fatalf("lock holder did not start: %q %v", line, err)
	}
	return cmd
}

func TestRealRunLockOwnerLiveKeptDeadReaped(t *testing.T) {
	f := newFixture(t, "if [ -d \"$LOCK\" ]; then echo \"run r1 is locked ($LOCK)\" >&2; exit 1; fi\nexit 0\n")
	lock := filepath.Join(f.home, ".code-forge", "runs", "r1.lock")
	t.Setenv("LOCK", lock)
	holder := holdRunLock(t, f.home)
	owner := filepath.Join(lock, OwnerFile)
	written, err := os.ReadFile(owner)
	if err != nil {
		t.Fatalf("B426 lock writer left no owner.json: %v", err)
	}

	// The failure text is the real one a second verb gets on this lock.
	lockErr := state.WithRunLock(f.home, "r1", func() error { return nil })
	if lockErr == nil || !strings.Contains(lockErr.Error(), "is locked ("+lock+")") {
		t.Fatalf("second WithRunLock: %v, want a locked error naming %s", lockErr, lock)
	}
	detail := "gates run: " + lockErr.Error()

	// Live holder: Reap and a heal pass keep the lock and its owner.json.
	if path, outcome := Reap(f.home, detail); path != lock || outcome != "kept: owner alive" {
		t.Fatalf("Reap with a live holder = %q %q, want kept: owner alive", path, outcome)
	}
	if _, err := Enqueue(f.home, "r1", "B1", KindLock, gateArgv, f.dir, detail, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.runner(1).Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if now, err := os.ReadFile(owner); err != nil || string(now) != string(written) {
		t.Fatalf("a live holder's lock must stay: owner.json %q %v", now, err)
	}
	if got := only(t, f.home); got.Status != StatusNeedsCoder || got.Tries != 1 {
		t.Fatalf("live: item status %q tries %d, want needs_coder after 1 try", got.Status, got.Tries)
	}

	// Dead holder: SIGKILL leaves the dir and owner.json; heal run reaps it.
	if err := holder.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = holder.Wait()
	if _, err := os.Stat(owner); err != nil {
		t.Fatalf("killed holder must leave owner.json behind: %v", err)
	}
	f.now = f.now.Add(time.Minute)
	if _, err := Enqueue(f.home, "r1", "B1", KindLock, gateArgv, f.dir, detail, f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.runner(3).Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("lock dir of a dead holder must be reaped, stat err = %v", err)
	}
	items, err := Load(f.home, "r1")
	if err != nil {
		t.Fatal(err)
	}
	healed := 0
	for _, it := range items {
		if it.Status == StatusHealed && it.Tries == 1 {
			healed++
		}
	}
	if healed != 1 {
		t.Fatalf("dead: want one item healed on try 1, got %+v", items)
	}
}
