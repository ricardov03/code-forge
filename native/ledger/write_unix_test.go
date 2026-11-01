//go:build unix

package ledger_test

import (
	"bufio"
	"bytes"
	"context"
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
import { watch, existsSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
const { appendRow } = await import(pathToFileURL(process.argv[1]));
const lock = process.argv[2] + '/fixture.append.lock';
let started = false;
const watcher = watch(process.argv[2], async () => {
  if (started || !existsSync(lock)) return;
  started = true;
  watcher.close();
  console.log('contending');
  for (let i = 0; i < 100; i++) {
    await appendRow({ event: 'node', id: i }, { slug: 'fixture', rotateAtBytes: 128 * 1024 * 1024 });
  }
});
console.log('ready');
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
		t.Fatalf("Node watcher did not start: %s (%v)", &stderr, scanner.Err())
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
