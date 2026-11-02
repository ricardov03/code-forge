package ledger_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

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
			_, err := ledger.AppendRow(dir, "fixture", map[string]any{"event": "new", "ts": "fixed"}, int64(c.limit))
			if err != nil {
				t.Fatalf("append must succeed: %v", err)
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
				called := false
				err := state.WithRunLock(home, "fixture", func() error {
					called = true
					return nil
				})
				want := "run fixture is locked (" + lock + "); remove it if no forge verb is running"
				if err == nil || err.Error() != want || called || time.Since(start) < 5*time.Second {
					t.Fatalf("lock must wait 5000ms and refuse with %q: elapsed=%s err=%v called=%v", want, time.Since(start), err, called)
				}
				if _, err := os.Stat(lock); err != nil {
					t.Fatalf("timeout removed holder lock: %v", err)
				}
				return
			}
			if c == "lock releases on completion" {
				called := false
				err := state.WithRunLock(home, "fixture", func() error {
					called = true
					if info, err := os.Stat(lock); err != nil || !info.IsDir() {
						t.Fatalf("callback must hold the run lock: %v (%v)", info, err)
					}
					return nil
				})
				if err != nil || !called {
					t.Fatalf("lock callback: called=%v err=%v", called, err)
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
			err := state.WithRunLock(home, "fixture", func() error {
				record, err := state.ReadRun(home, "fixture")
				if err != nil {
					return err
				}
				record.Set("status", "ended")
				return state.SaveRun(home, record)
			})
			if err != nil {
				t.Fatalf("save must succeed: %v", err)
			}
			info, err := os.Stat(file)
			if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
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
