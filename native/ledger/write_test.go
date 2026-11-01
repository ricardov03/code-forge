package ledger_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

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
