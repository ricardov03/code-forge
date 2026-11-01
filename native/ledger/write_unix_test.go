//go:build unix

package ledger_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

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
