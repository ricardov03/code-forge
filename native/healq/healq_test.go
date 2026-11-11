package healq

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/filelock"
)

// fixture gives a temp HOME, a fake verb script and a fake clock.
type fixture struct {
	home, dir, exe, calls string
	now                   time.Time
	sleeps                []time.Duration
}

func newFixture(t *testing.T, script string) *fixture {
	t.Helper()
	f := &fixture{home: t.TempDir(), dir: t.TempDir(), now: time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)}
	t.Setenv("HOME", f.home)
	f.calls = filepath.Join(f.dir, "calls")
	f.exe = filepath.Join(f.dir, "fake-verb")
	body := "#!/bin/sh\nCALLS='" + f.calls + "'\nprintf '%s|%s\\n' \"$" + RetryEnv + "\" \"$*\" >> \"$CALLS\"\nN=$(wc -l < \"$CALLS\" | tr -d ' ')\n" + script
	if err := os.WriteFile(f.exe, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) runner(max int) *Runner {
	return &Runner{
		Home: f.home, Run: "r1", Exe: f.exe, MaxTries: max,
		Now: func() time.Time { return f.now },
		Sleep: func(_ context.Context, d time.Duration) error {
			f.sleeps = append(f.sleeps, d)
			f.now = f.now.Add(d)
			return nil
		},
	}
}

func (f *fixture) callLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(f.calls)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func only(t *testing.T, home string) Item {
	t.Helper()
	items, err := Load(home, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("queue has %d items, want 1: %+v", len(items), items)
	}
	return items[0]
}

var gateArgv = []string{"gates", "run", "--run", "r1", "--block", "B1"}

func TestRunHealsAfterTwoLockedFailures(t *testing.T) {
	f := newFixture(t, "if [ \"$N\" -le 2 ]; then echo \"gates run: run r1 is locked (/nowhere/r1.lock); remove it if no forge verb is running\" >&2; exit 1; fi\necho green\nexit 0\n")
	item, err := Enqueue(f.home, "r1", "B1", KindLock, gateArgv, f.dir, "gates run: run r1 is locked (/nowhere/r1.lock)", f.now)
	if err != nil {
		t.Fatal(err)
	}
	r := f.runner(3)
	if err := r.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := only(t, f.home)
	if got.Status != StatusHealed || got.Tries != 3 {
		t.Fatalf("item: status %q tries %d, want healed after 3 tries", got.Status, got.Tries)
	}
	want := item.ID + "|" + strings.Join(gateArgv, " ")
	if lines := f.callLines(t); !reflect.DeepEqual(lines, []string{want, want, want}) {
		t.Fatalf("child calls = %q, want 3 × %q (stored argv, retry id in env)", lines, want)
	}
	if !reflect.DeepEqual(f.sleeps, []time.Duration{15 * time.Second, 60 * time.Second}) {
		t.Fatalf("backoff waits = %v, want [15s 1m0s]", f.sleeps)
	}
	if r.Healed != 1 || r.NeedsCoder != 0 {
		t.Fatalf("counts healed=%d needs_coder=%d, want 1 and 0", r.Healed, r.NeedsCoder)
	}
}

func TestRunMarksNeedsCoderAfterMaxTries(t *testing.T) {
	f := newFixture(t, "echo \"block land: run r1 is locked (/nowhere/r1.lock)\" >&2\nexit 1\n")
	if _, err := Enqueue(f.home, "r1", "B1", KindLock, []string{"block", "land", "B1", "--run", "r1"}, f.dir, "run r1 is locked (/nowhere/r1.lock)", f.now); err != nil {
		t.Fatal(err)
	}
	r := f.runner(3)
	if err := r.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := only(t, f.home)
	if got.Status != StatusNeedsCoder || got.Tries != 3 || len(f.callLines(t)) != 3 {
		t.Fatalf("item: status %q tries %d calls %d, want needs_coder after exactly 3 tries", got.Status, got.Tries, len(f.callLines(t)))
	}
	if !strings.Contains(got.Detail, "block land: run r1 is locked") {
		t.Fatalf("detail keeps the last failure text, got %q", got.Detail)
	}
	if r.NeedsCoder != 1 || r.Healed != 0 {
		t.Fatalf("counts healed=%d needs_coder=%d, want 0 and 1", r.Healed, r.NeedsCoder)
	}
}

// A retry that fails for a reason a retry does not fix stops at once.
func TestRunStopsOnNonRetryableFailure(t *testing.T) {
	f := newFixture(t, "echo \"gates run: test: 2 tests failed\" >&2\nexit 1\n")
	if _, err := Enqueue(f.home, "r1", "B1", KindLock, gateArgv, f.dir, "run r1 is locked (/nowhere/r1.lock)", f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.runner(3).Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := only(t, f.home)
	if got.Status != StatusNeedsCoder || got.Tries != 1 || len(f.callLines(t)) != 1 || len(f.sleeps) != 0 {
		t.Fatalf("item: status %q tries %d calls %d sleeps %v, want needs_coder after 1 try and no wait", got.Status, got.Tries, len(f.callLines(t)), f.sleeps)
	}
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.Process.Pid
}

func TestLockItemRemovesDeadOwnerLockDirBeforeRetry(t *testing.T) {
	f := newFixture(t, "if [ -d \"$LOCK\" ]; then echo \"run r1 is locked ($LOCK)\" >&2; exit 1; fi\nexit 0\n")
	lock := filepath.Join(f.home, ".code-forge", "runs", "r1.lock")
	t.Setenv("LOCK", lock)
	if err := os.MkdirAll(lock, 0755); err != nil {
		t.Fatal(err)
	}
	owner := `{"pid":` + strconv.Itoa(deadPID(t)) + `,"start":"Thu Jan  1 00:00:00 2026"}`
	if err := os.WriteFile(filepath.Join(lock, OwnerFile), []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Enqueue(f.home, "r1", "B1", KindLock, gateArgv, f.dir, "gates run: run r1 is locked ("+lock+"); remove it if no forge verb is running", f.now); err != nil {
		t.Fatal(err)
	}
	if err := f.runner(3).Pass(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lock); !os.IsNotExist(err) {
		t.Fatalf("lock dir of a dead owner must be removed before the retry, stat err = %v", err)
	}
	if got := only(t, f.home); got.Status != StatusHealed || got.Tries != 1 {
		t.Fatalf("item: status %q tries %d, want healed on try 1", got.Status, got.Tries)
	}
}

func TestReapKeepsLiveAndOwnerlessLocks(t *testing.T) {
	home := t.TempDir()
	start := filelock.ProcessStartTime(os.Getpid())
	if start == nil {
		t.Fatal("cannot read this process start time")
	}
	live := filepath.Join(home, ".code-forge", "runs", "live.lock")
	bare := filepath.Join(home, ".code-forge", "runs", "bare.lock")
	for _, dir := range []string{live, bare} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	owner := `{"pid":` + strconv.Itoa(os.Getpid()) + `,"start":"` + *start + `"}`
	if err := os.WriteFile(filepath.Join(live, OwnerFile), []byte(owner), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "x.lock")
	if err := os.Mkdir(outside, 0755); err != nil {
		t.Fatal(err)
	}
	for dir, want := range map[string]string{live: "kept: owner alive", bare: "kept: no owner pid", outside: "kept: outside "} {
		path, outcome := Reap(home, "run x is locked ("+dir+")")
		if path != dir || !strings.HasPrefix(outcome, want) {
			t.Errorf("Reap(%s) = %q %q, want prefix %q", dir, path, outcome, want)
		}
		if _, err := os.Stat(dir); err != nil {
			t.Errorf("%s must be kept: %v", dir, err)
		}
	}
}

func TestAcquireRefusesASecondHealRun(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	release, err := Acquire("r1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = Acquire("r1")
	busy, ok := err.(*ErrBusy)
	if !ok || busy.PID != os.Getpid() || !strings.HasPrefix(err.Error(), "busy: another heal run (pid ") {
		t.Fatalf("second Acquire = %v, want ErrBusy naming pid %d", err, os.Getpid())
	}
	other, err := Acquire("r2")
	if err != nil {
		t.Fatalf("another run is not busy: %v", err)
	}
	other()
}
