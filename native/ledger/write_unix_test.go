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
	// Give the foreign writer time to observe the lock during the recovery read.
	// Neither runtime rotates this fixture, so every complete row must remain.
	prefix := "{\"event\":\"before\",\"note\":\"información 🚀\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "fixture.jsonl"), []byte(prefix+strings.Repeat("x", 64*1024*1024)), 0600); err != nil {
		t.Fatal(err)
	}
	module, err := filepath.Abs("../../src/ledger/write.mjs")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", `
import fs from 'node:fs';
import { syncBuiltinESMExports } from 'node:module';
// Suppress filesystem notifications: this test must work when FSEvents is stalled.
fs.watch = () => ({ close() {} });
fs.watchFile = () => {};
syncBuiltinESMExports();
import { existsSync, statSync } from 'node:fs';
import { setTimeout as delay } from 'node:timers/promises';
import { pathToFileURL } from 'node:url';
const { appendRow } = await import(pathToFileURL(process.argv[1]));
const lock = process.argv[2] + '/fixture.append.lock';
console.log('ready');
while (!existsSync(lock)) await delay(1);
// The torn tail still exists while Go holds its first recovery lock. Observing
// a later append lock would not prove contention with recovery.
console.log(statSync(process.argv[2] + '/fixture.jsonl').size > 64 * 1024 * 1024 ? 'contending' : 'missed recovery');
for (let i = 0; i < 100; i++) {
  await appendRow({ event: 'node', id: i }, { slug: 'fixture', rotateAtBytes: 128 * 1024 * 1024 });
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
	for i := 0; i < 100; i++ {
		if _, err := ledger.AppendRow(dir, "fixture", map[string]any{"event": "go", "id": i}, 128*1024*1024); err != nil {
			t.Fatal(err)
		}
	}
	if !scanner.Scan() || scanner.Text() != "contending" {
		t.Fatalf("Node must contend with Go recovery: %s (%v)", &stderr, scanner.Err())
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
	start := time.Now()
	_, err = ledger.AppendRow(dir, "fixture", map[string]any{"event": "after-kill"}, ledger.DefaultRotateAtBytes)
	if err != nil {
		t.Fatalf("append after SIGKILL must succeed within the 5s wait limit: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 5*time.Second {
		t.Fatalf("append after SIGKILL took %s, want less than 5s", elapsed)
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
