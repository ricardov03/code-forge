package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// captureStderr swaps os.Stderr for a pipe while body runs and returns what
// was written to it.
func captureStderr(t *testing.T, body func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() {
		data, _ := io.ReadAll(r)
		done <- string(data)
	}()
	func() {
		defer func() { os.Stderr = saved }()
		body()
	}()
	w.Close()
	out := <-done
	r.Close()
	return out
}

func lockDir(home, run string) string {
	return filepath.Join(home, ".code-forge", "runs", run+".lock")
}

// makeLock creates a held-looking lock dir, with an owner file when owner is
// not nil.
func makeLock(t *testing.T, home, run string, owner map[string]any) string {
	t.Helper()
	dir := lockDir(home, run)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if owner != nil {
		data, err := json.Marshal(owner)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "owner.json"), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// deadPid returns the pid of a child that has already exited and been reaped.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func brokeLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "broke stale run lock") {
			lines = append(lines, line)
		}
	}
	return lines
}

func isLocked(err error) bool {
	var se *Error
	return errors.As(err, &se) && se.Code == "locked"
}

func TestRunLockBreaksDeadOwner(t *testing.T) {
	home := t.TempDir()
	pid := deadPid(t)
	dir := makeLock(t, home, "r1", map[string]any{"pid": pid, "start": "darwin:1.000000", "verb": "worker --run"})
	runs := 0
	var err error
	out := captureStderr(t, func() {
		err = WithRunLock(home, "r1", func() error { runs++; return nil })
	})
	if err != nil {
		t.Fatalf("WithRunLock: %v", err)
	}
	if runs != 1 {
		t.Fatalf("fn ran %d times, want 1", runs)
	}
	lines := brokeLines(out)
	if len(lines) != 1 || !strings.Contains(lines[0], fmt.Sprintf("broke stale run lock (pid %d)", pid)) {
		t.Fatalf("stderr broke lines = %q (all stderr %q), want 1 naming pid %d", lines, out, pid)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("lock dir after fn: %v, want gone", err)
	}
}

func TestRunLockBreaksReusedPid(t *testing.T) {
	home := t.TempDir()
	pid := os.Getpid()
	makeLock(t, home, "r2", map[string]any{"pid": pid, "start": "other:1", "verb": "worker --run"})
	runs := 0
	var err error
	out := captureStderr(t, func() {
		err = WithRunLock(home, "r2", func() error { runs++; return nil })
	})
	if err != nil || runs != 1 {
		t.Fatalf("WithRunLock: err %v, fn ran %d times, want nil and 1", err, runs)
	}
	lines := brokeLines(out)
	if len(lines) != 1 || !strings.Contains(lines[0], fmt.Sprintf("(pid %d)", pid)) {
		t.Fatalf("stderr broke lines = %q, want 1 naming pid %d", lines, pid)
	}
}

func TestRunLockWritesAndRemovesOwnerFile(t *testing.T) {
	home := t.TempDir()
	dir := lockDir(home, "r3")
	var owner map[string]any
	var readErr error
	err := WithRunLock(home, "r3", func() error {
		data, err := os.ReadFile(filepath.Join(dir, "owner.json"))
		if err != nil {
			readErr = err
			return nil
		}
		readErr = json.Unmarshal(data, &owner)
		return nil
	})
	if err != nil {
		t.Fatalf("WithRunLock: %v", err)
	}
	if readErr != nil {
		t.Fatalf("owner file while fn runs: %v", readErr)
	}
	if pid, _ := owner["pid"].(float64); int(pid) != os.Getpid() {
		t.Fatalf("owner pid = %v, want %d", owner["pid"], os.Getpid())
	}
	if start, _ := owner["start"].(string); start == "" {
		t.Fatalf("owner start = %q, want a process start token", owner["start"])
	}
	if verb, _ := owner["verb"].(string); verb == "" {
		t.Fatalf("owner verb = %q, want the verb", owner["verb"])
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("lock dir after fn: %v, want gone", err)
	}
}

func TestRunLockKeepsLiveOwner(t *testing.T) {
	home := t.TempDir()
	dir := lockDir(home, "r4")
	held := make(chan struct{})
	release := make(chan struct{})
	holderErr := make(chan error, 1)
	go func() {
		holderErr <- WithRunLock(home, "r4", func() error { close(held); <-release; return nil })
	}()
	<-held
	before, err := os.ReadFile(filepath.Join(dir, "owner.json"))
	if err != nil {
		close(release)
		t.Fatalf("holder owner file: %v", err)
	}
	runs := 0
	var waitErr error
	out := captureStderr(t, func() {
		waitErr = WithRunLock(home, "r4", func() error { runs++; return nil })
	})
	after, readErr := os.ReadFile(filepath.Join(dir, "owner.json"))
	close(release)
	if err := <-holderErr; err != nil {
		t.Fatalf("holder: %v", err)
	}
	if !isLocked(waitErr) {
		t.Fatalf("waiter err = %v, want code locked", waitErr)
	}
	if runs != 0 {
		t.Fatalf("waiter fn ran %d times, want 0", runs)
	}
	if readErr != nil || string(after) != string(before) {
		t.Fatalf("owner file changed: before %q after %q (%v)", before, after, readErr)
	}
	if out != "" {
		t.Fatalf("stderr = %q, want empty", out)
	}
}

func TestRunLockBreaksOldOwnerless(t *testing.T) {
	home := t.TempDir()
	dir := makeLock(t, home, "r5", nil)
	old := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
	runs := 0
	var err error
	out := captureStderr(t, func() {
		err = WithRunLock(home, "r5", func() error { runs++; return nil })
	})
	if err != nil || runs != 1 {
		t.Fatalf("WithRunLock: err %v, fn ran %d times, want nil and 1", err, runs)
	}
	if lines := brokeLines(out); len(lines) != 1 {
		t.Fatalf("stderr broke lines = %q, want exactly 1", lines)
	}
}

func TestRunLockKeepsFreshOwnerless(t *testing.T) {
	home := t.TempDir()
	dir := makeLock(t, home, "r6", nil)
	recent := time.Now().Add(-9 * time.Minute)
	if err := os.Chtimes(dir, recent, recent); err != nil {
		t.Fatal(err)
	}
	runs := 0
	var err error
	out := captureStderr(t, func() {
		err = WithRunLock(home, "r6", func() error { runs++; return nil })
	})
	if !isLocked(err) || runs != 0 {
		t.Fatalf("WithRunLock: err %v, fn ran %d times, want locked and 0", err, runs)
	}
	if _, statErr := os.Stat(dir); statErr != nil {
		t.Fatalf("lock dir: %v, want kept", statErr)
	}
	if out != "" {
		t.Fatalf("stderr = %q, want empty", out)
	}
}

func TestRunLockStaleRaceOneWinnerAtATime(t *testing.T) {
	home := t.TempDir()
	pid := deadPid(t)
	makeLock(t, home, "r7", map[string]any{"pid": pid, "start": "darwin:1.000000", "verb": "worker --run"})
	var inside, maxInside, runs int32
	errs := make([]error, 2)
	out := captureStderr(t, func() {
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				errs[i] = WithRunLock(home, "r7", func() error {
					n := atomic.AddInt32(&inside, 1)
					for {
						m := atomic.LoadInt32(&maxInside)
						if n <= m || atomic.CompareAndSwapInt32(&maxInside, m, n) {
							break
						}
					}
					time.Sleep(50 * time.Millisecond)
					atomic.AddInt32(&inside, -1)
					atomic.AddInt32(&runs, 1)
					return nil
				})
			}(i)
		}
		close(start)
		wg.Wait()
	})
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("WithRunLock errors: %v, %v", errs[0], errs[1])
	}
	if runs != 2 || maxInside != 1 {
		t.Fatalf("fn runs %d (want 2), max inside at once %d (want 1)", runs, maxInside)
	}
	if lines := brokeLines(out); len(lines) != 1 {
		t.Fatalf("stderr broke lines = %q, want exactly 1", lines)
	}
}
