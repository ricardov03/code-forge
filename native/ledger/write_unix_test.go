//go:build unix

package ledger_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

func TestAppendRecoversOldUnidentifiedLocks(t *testing.T) {
	for _, contents := range []string{"", `{"pid":`, "null", "{}", `{"pid":1}`, `{"pid":"foreign","started_at":"unknown"}`} {
		t.Run(fmt.Sprintf("%q", contents), func(t *testing.T) {
			dir := t.TempDir()
			lock := filepath.Join(dir, "fixture.append.lock")
			if err := os.WriteFile(lock, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-time.Minute)
			if err := os.Chtimes(lock, old, old); err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.AppendRow(dir, "fixture", map[string]any{"event": "after-old-lock"}, ledger.DefaultRotateAtBytes); err != nil {
				t.Fatal(err)
			}
			rows, err := ledger.ReadRows(dir, "fixture")
			if err != nil || len(rows) != 1 || rows[0].(map[string]any)["event"] != "after-old-lock" {
				t.Fatalf("want one recovered append, got %v (%v)", rows, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != "fixture.jsonl" {
				t.Fatalf("lock artifacts remain: %v (%v)", entries, err)
			}
		})
	}
}

func TestAppendWaitsForFreshOrLiveLock(t *testing.T) {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", fmt.Sprint(os.Getpid()))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	start, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(start)) == "" {
		t.Fatalf("read own process identity: %q (%v)", start, err)
	}
	live, err := json.Marshal(map[string]any{"pid": os.Getpid(), "started_at": string(start)})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, contents string
		old            bool
	}{
		{"fresh empty", "", false},
		{"fresh partial", `{"pid":`, false},
		{"fresh unidentified", "{}", false},
		{"old live identity", string(live), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			lock := filepath.Join(dir, "fixture.append.lock")
			if err := os.WriteFile(lock, []byte(c.contents), 0600); err != nil {
				t.Fatal(err)
			}
			if c.old {
				old := time.Now().Add(-time.Minute)
				if err := os.Chtimes(lock, old, old); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			waited := false
			go func() {
				_, err := ledger.AppendRow(dir, "fixture", map[string]any{"event": "after-release"}, ledger.DefaultRotateAtBytes)
				done <- err
			}()
			t.Cleanup(func() {
				_ = os.Remove(lock)
				if !waited {
					if err := <-done; err != nil {
						t.Errorf("append after release: %v", err)
					}
				}
			})
			select {
			case err := <-done:
				waited = true
				t.Fatalf("writer did not wait for held lock: %v", err)
			case <-time.After(125 * time.Millisecond):
			}
			if contents, err := os.ReadFile(lock); err != nil || string(contents) != c.contents {
				t.Fatalf("holder lock changed: %q (%v)", contents, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "fixture.jsonl")); !os.IsNotExist(err) {
				t.Fatalf("writer touched ledger while lock held: %v", err)
			}
			if err := os.Remove(lock); err != nil {
				t.Fatal(err)
			}
			err := <-done
			waited = true
			if err != nil {
				t.Fatalf("append after release: %v", err)
			}
			rows, err := ledger.ReadRows(dir, "fixture")
			if err != nil || len(rows) != 1 || rows[0].(map[string]any)["event"] != "after-release" {
				t.Fatalf("want one append after release, got %v (%v)", rows, err)
			}
		})
	}
}

func TestNodeAppendDuringGoTornTailRecovery(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node is required for the cross-runtime append regression:", err)
	}
	home := t.TempDir()
	dir := filepath.Join(home, ".code-forge", "ledger")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// Neither runtime rotates this fixture, so every complete row must remain.
	// The large torn tail keeps Go's recovery read busy while Node waits on the lock.
	prefix := "{\"event\":\"before\",\"note\":\"información 🚀\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "fixture.jsonl"), []byte(prefix+strings.Repeat("x", 64*1024*1024)), 0600); err != nil {
		t.Fatal(err)
	}
	module, err := filepath.Abs("../../src/ledger/write.mjs")
	if err != nil {
		t.Fatal(err)
	}
	// No fixed stopwatch: a busy machine may take far longer than an idle one.
	// The only bound is go test's own -timeout; stop Node shortly before it so a
	// real hang still fails here with Node's stderr instead of a bare panic.
	ctx, cancel := context.Background(), context.CancelFunc(func() {})
	if deadline, ok := t.Deadline(); ok {
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-time.Until(deadline)/10))
	}
	defer cancel()
	// Every step waits on a signal, never on a clock (B624): Go holds its first
	// recovery lock until Node reports it saw that lock with the torn tail, and
	// after that the writers alternate, each Go append starting only after Node
	// reports its previous append. Both still race for the shared lock on every
	// round, but neither waits longer than one append of the other, so neither
	// runtime's 5 s lock deadline depends on host load.
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", `
import fs from 'node:fs';
import { existsSync, statSync } from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
import { setTimeout as delay } from 'node:timers/promises';
import { pathToFileURL } from 'node:url';
// Suppress filesystem notifications: this test must work when FSEvents is stalled.
fs.watch = () => ({ close() {} });
fs.watchFile = () => {};
syncBuiltinESMExports();
// Keep this import dynamic so the writer receives the patched builtins.
const { appendRow } = await import(pathToFileURL(process.argv[1]));
const lock = process.argv[2] + '/fixture.append.lock';
console.log('ready');
while (!existsSync(lock)) await delay(1);
// Go holds its first recovery lock until it reads the next line, so the torn
// tail is still present here. A smaller file means Go recovered without Node.
console.log(statSync(process.argv[2] + '/fixture.jsonl').size > 64 * 1024 * 1024 ? 'contending' : 'missed recovery');
for (let i = 0; i < 100; i++) {
  await appendRow({ event: 'node', id: i }, { slug: 'fixture', rotateAtBytes: 128 * 1024 * 1024 });
  console.log('node ' + i);
}
`, module, dir)
	cmd.Env = append(os.Environ(), "HOME="+home)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatalf("Node poller did not start: %s (%v)", &stderr, scanner.Err())
	}
	contention := "no recovery hook call"
	held := false
	defer ledger.SetBeforeTornTailRecovery(func() {
		if held {
			return
		}
		held = true
		if !scanner.Scan() {
			contention = fmt.Sprintf("Node exited before seeing the lock: %v", scanner.Err())
			return
		}
		contention = scanner.Text()
	})()
	for i := 0; i < 100; i++ {
		if i > 0 {
			if want := fmt.Sprintf("node %d", i-1); !scanner.Scan() || scanner.Text() != want {
				t.Fatalf("want %q from Node before Go append %d, got %q: %s (%v)", want, i, scanner.Text(), &stderr, scanner.Err())
			}
		}
		if _, err := ledger.AppendRow(dir, "fixture", map[string]any{"event": "go", "id": i}, 128*1024*1024); err != nil {
			t.Fatal(err)
		}
		if i == 0 && contention != "contending" {
			t.Fatalf("Node must contend with Go recovery, got %q: %s", contention, &stderr)
		}
	}
	if want := "node 99"; !scanner.Scan() || scanner.Text() != want {
		t.Fatalf("want %q from Node, got %q: %s (%v)", want, scanner.Text(), &stderr, scanner.Err())
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("Node append failed: %v; %s", err, &stderr)
	}
	data, err := os.ReadFile(filepath.Join(dir, "fixture.jsonl"))
	if err != nil || !bytes.HasPrefix(data, []byte(prefix)) {
		t.Fatalf("complete prefix changed: %v", err)
	}
	rows, err := ledger.ReadRows(dir, "fixture")
	if err != nil || len(rows) != 201 {
		t.Fatalf("want original + 100 Go + 100 Node complete rows; got %d (%v)", len(rows), err)
	}
	seen := make(map[string]bool)
	for _, value := range rows[1:] {
		row := value.(map[string]any)
		key := fmt.Sprintf("%s:%v", row["event"], row["id"])
		if seen[key] {
			t.Fatalf("duplicate row %s", key)
		}
		seen[key] = true
	}
}

func TestAppendLockSurvivesKilledHolder(t *testing.T) {
	const holderDir = "CF_TEST_LEDGER_LOCK_HOLDER_DIR"
	if dir := os.Getenv(holderDir); dir != "" {
		// A row larger than the FIFO buffer blocks in Write while holding the
		// real append lock, until the parent kills this process.
		_, err := ledger.AppendRow(dir, "fixture", map[string]any{
			"event": "holder", "payload": strings.Repeat("x", 1024*1024),
		}, ledger.DefaultRotateAtBytes)
		t.Fatalf("holder unexpectedly finished its blocked append: %v", err)
	}

	dir := t.TempDir()
	file := filepath.Join(dir, "fixture.jsonl")
	if err := syscall.Mkfifo(file, 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := syscall.Open(file, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(reader)
	// Re-exec the freshly built Go test binary, never a prebuilt release binary.
	cmd := exec.Command(os.Args[0], "-test.run=^TestAppendLockSurvivesKilledHolder$")
	cmd.Env = append(os.Environ(), holderDir+"="+dir)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		var first [1]byte
		n, err := syscall.Read(reader, first[:])
		if n == 1 {
			break // The child reached Write, so it owns the append lock.
		}
		if err != nil && err != syscall.EAGAIN && err != syscall.EWOULDBLOCK {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not reach the locked append within 5s")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	err = cmd.Wait()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("holder must exit from SIGKILL: %v; output=%s", err, &output)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	_, err = ledger.AppendRow(dir, "fixture", map[string]any{"event": "after-kill"}, ledger.DefaultRotateAtBytes)
	if err != nil {
		// AppendRow enforces its own lock-wait limit. Assert that result rather
		// than charging host scheduling delays against a second wall clock.
		t.Fatalf("append after SIGKILL must succeed without exhausting its lock wait: %v", err)
	}
	rows, err := ledger.ReadRows(dir, "fixture")
	if err != nil || len(rows) != 1 {
		t.Fatalf("recovered ledger must contain exactly the parent's row: rows=%v err=%v", rows, err)
	}
	row, ok := rows[0].(map[string]any)
	if !ok || row["event"] != "after-kill" {
		t.Fatalf("recovered row must be the parent's after-kill event: %v", rows[0])
	}
}

// fakeAppendPS installs a ps script for the lock identity and restores the
// real one (and drops the cached identity) when the test ends.
func fakeAppendPS(t *testing.T, script string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ps")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ledger.SetAppendPS(path))
}

func TestAppendRetriesLockIdentityAfterPSFailure(t *testing.T) {
	// Node's `selfStart ??= readStartTime(...)` retries after a failed ps read;
	// one failed read must not fail every later append in the process.
	marker := filepath.Join(t.TempDir(), "failed-once")
	fakeAppendPS(t, fmt.Sprintf("if [ ! -e %q ]; then : > %q; exit 1; fi\nexec /bin/ps \"$@\"\n", marker, marker))
	dir := t.TempDir()
	_, err := ledger.AppendRow(dir, "fixture", map[string]any{"event": "first"}, ledger.DefaultRotateAtBytes)
	if err == nil || !strings.Contains(err.Error(), "cannot determine ledger lock owner process start time") {
		t.Fatalf("first append with failing ps: want identity error, got %v", err)
	}
	if _, err := ledger.AppendRow(dir, "fixture", map[string]any{"event": "second"}, ledger.DefaultRotateAtBytes); err != nil {
		t.Fatalf("second append after ps recovered: %v", err)
	}
	rows, err := ledger.ReadRows(dir, "fixture")
	if err != nil || len(rows) != 1 || rows[0].(map[string]any)["event"] != "second" {
		t.Fatalf("want only the second row, got %v (%v)", rows, err)
	}
}

func TestAppendLockIdentityAllowsSlowPS(t *testing.T) {
	// Node gives ps 5000 ms (readStartTime); a ps slower than one second on a
	// loaded host must still produce the lock identity.
	fakeAppendPS(t, "sleep 1.5\nexec /bin/ps \"$@\"\n")
	dir := t.TempDir()
	if _, err := ledger.AppendRow(dir, "fixture", map[string]any{"event": "slow-ps"}, ledger.DefaultRotateAtBytes); err != nil {
		t.Fatalf("append with slow ps: %v", err)
	}
	rows, err := ledger.ReadRows(dir, "fixture")
	if err != nil || len(rows) != 1 {
		t.Fatalf("want one row, got %v (%v)", rows, err)
	}
}
