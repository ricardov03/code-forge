package ledger_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// Drive the hidden command so these same assertions run against the base binary
// before the write packages exist, without introducing a compile-error red.
func nativeState(t *testing.T, home, op, input string) (string, error) {
	t.Helper()
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	binary, err := filepath.Abs(filepath.Join("..", "bin", runtime.GOOS+"-"+arch, "code-forge-native"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(binary); os.IsNotExist(err) {
		t.Skip("native state tables require a host binary: node scripts/build-native.mjs --host")
	}
	cmd := exec.Command(binary, "state-golden", "--home", home, "--run", "fixture", "--op", op)
	cmd.Stdin = strings.NewReader(input)
	data, err := cmd.CombinedOutput()
	return string(data), err
}

func TestAppendBoundaries(t *testing.T) {
	line := "{\"event\":\"old\",\"text\":\"é😀\"}\n"
	want := "{\"event\":\"new\",\"ts\":\"fixed\",\"tokens_source\":null,\"cost_source\":null}\n"
	for _, c := range []struct {
		name, prefix, rotated, current string
		limit                          int
	}{
		{"below", line, "", line + want, len(line) + 1},
		{"equal", line, line, want, len(line)},
		{"above", line, line, want, len(line) - 1},
		{"torn unicode", line + "{\"é\":\"😀", "", line + want, 10000},
		{"all torn", "{bad", "", want, 10000},
		{"rotate before repair", line + "{bad", line + "{bad", want, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".code-forge", "ledger")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "fixture.jsonl")
			if err := os.WriteFile(file, []byte(c.prefix), 0600); err != nil {
				t.Fatal(err)
			}
			// The first occupied name must never be overwritten.
			if err := os.WriteFile(filepath.Join(dir, "fixture.1.jsonl"), []byte("occupied\n"), 0600); err != nil {
				t.Fatal(err)
			}
			input, _ := json.Marshal(map[string]any{"slug": "fixture", "rotateAtBytes": c.limit, "rows": []any{map[string]any{"event": "new", "ts": "fixed"}}})
			out, err := nativeState(t, home, "append", string(input))
			if err != nil {
				t.Fatalf("append must succeed: %v: %s", err, out)
			}
			got, err := os.ReadFile(file)
			if err != nil || string(got) != c.current {
				t.Fatalf("current bytes: got %q (%v), want %q", got, err, c.current)
			}
			got, err = os.ReadFile(filepath.Join(dir, "fixture.2.jsonl"))
			if c.rotated == "" && !os.IsNotExist(err) || c.rotated != "" && (err != nil || string(got) != c.rotated) {
				t.Fatalf("rotation bytes: got %q (%v), want %q", got, err, c.rotated)
			}
			got, err = os.ReadFile(filepath.Join(dir, "fixture.1.jsonl"))
			if err != nil || string(got) != "occupied\n" {
				t.Fatalf("occupied rotation changed: %q (%v)", got, err)
			}
		})
	}
}

func TestRunStateBoundaries(t *testing.T) {
	for _, c := range []string{"private record and worker-free mirror", "lock timeout", "lock releases on completion"} {
		t.Run(c, func(t *testing.T) {
			home := t.TempDir()
			runs := filepath.Join(home, ".code-forge", "runs")
			if err := os.MkdirAll(runs, 0700); err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(runs, "fixture.lock")
			if c == "lock timeout" {
				if err := os.Mkdir(lock, 0700); err != nil {
					t.Fatal(err)
				}
				start := time.Now()
				out, err := nativeState(t, home, "lock", "")
				want := "run fixture is locked (" + lock + "); remove it if no forge verb is running"
				if err == nil || !strings.Contains(out, want) || time.Since(start) < 5*time.Second {
					t.Fatalf("lock must wait 5000ms and refuse with %q: elapsed=%s err=%v output=%q", want, time.Since(start), err, out)
				}
				if _, err := os.Stat(lock); err != nil {
					t.Fatalf("timeout removed holder lock: %v", err)
				}
				return
			}
			if c == "lock releases on completion" {
				out, err := nativeState(t, home, "lock", "")
				if err != nil || out != "locked\n" {
					t.Fatalf("lock callback: got %q (%v), want locked newline", out, err)
				}
				if _, err := os.Stat(lock); !os.IsNotExist(err) {
					t.Fatalf("completed lock still exists: %v", err)
				}
				return
			}
			workspace := filepath.Join(home, "workspace")
			record := map[string]any{"run_id": "fixture", "workspace": workspace, "worker": map[string]any{"pid": 123}, "z": 1, "a": 2}
			data, _ := json.MarshalIndent(record, "", "  ")
			file := filepath.Join(runs, "fixture.json")
			if err := os.WriteFile(file, append(data, '\n'), 0644); err != nil {
				t.Fatal(err)
			}
			out, err := nativeState(t, home, "save", `{"status":"ended"}`)
			if err != nil {
				t.Fatalf("save must succeed: %v: %s", err, out)
			}
			info, err := os.Stat(file)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("authoritative record must have mode 0600: %v (%v)", info, err)
			}
			actual, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			want := append(bytes.TrimSuffix(data, []byte("\n}")), []byte(",\n  \"status\": \"ended\"\n}\n")...)
			if !bytes.Equal(actual, want) {
				t.Fatalf("record bytes: got %q, want %q", actual, want)
			}
			mirror, err := os.ReadFile(filepath.Join(workspace, ".code-forge", "runs", "fixture.json"))
			want = bytes.Replace(want, []byte("  \"worker\": {\n    \"pid\": 123\n  },\n"), nil, 1)
			if err != nil || !bytes.Equal(mirror, want) {
				t.Fatalf("mirror must omit exactly worker: got %q (%v), want %q", mirror, err, want)
			}
		})
	}
}
