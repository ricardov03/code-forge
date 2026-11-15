//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/sign"
)

// b749Fixture is a run whose home ledger holds n signed rows: the current
// run's block rows and older runs that each end with a signed run.end row.
// keyed says which old runs keep their key file (a run without a key can
// never be swept, so every one of its run.end rows is checked again).
type b749Fixture struct {
	home, repo, run, project string
	ended                    map[string]bool // old runs a sweep must prune
}

func b749Write(t *testing.T, n, perRun int, keyed func(i int) bool, forged func(i int) bool) b749Fixture {
	t.Helper()
	// A short home keeps native.sock under the Unix socket path limit.
	home, err := os.MkdirTemp("", "b749")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	f := b749Fixture{home: home, repo: t.TempDir(), run: "r-current", project: "b749", ended: map[string]bool{}}
	if f.repo, err = filepath.EvalSymlinks(f.repo); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base"}} {
		if _, err := git(f.repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	record := object{"run_id": f.run, "project": f.project, "workspace": f.repo}
	writeObject(t, filepath.Join(f.home, ".code-forge", "runs", f.run+".json"), record)
	writeObject(t, filepath.Join(f.repo, ".code-forge", "runs", f.run+".json"), record)
	keyDir := filepath.Join(f.home, ".code-forge", "runs")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	if err := os.WriteFile(filepath.Join(keyDir, f.run+".key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	var b strings.Builder
	for i := 0; i < n; i++ {
		runIndex := i / perRun
		run := "r-old-" + strconv.Itoa(runIndex)
		row := object{"run": run, "block": "B" + strconv.Itoa(i%7), "event": "review.done", "file": "a.go", "ts": old}
		if i%perRun == perRun-1 {
			row["event"] = "run.end"
		}
		if i%50 == 0 {
			row = object{"run": f.run, "block": "B" + strconv.Itoa(i%13), "event": "block.open", "ts": old}
		}
		signed, err := sign.SignRow(row, key)
		if err != nil {
			t.Fatal(err)
		}
		if row["event"] == "run.end" {
			if forged(runIndex) {
				signed["mac"] = strings.Repeat("0", 64)
			} else if keyed(runIndex) {
				f.ended[run] = true
			}
		}
		line, _ := json.Marshal(signed)
		b.Write(line)
		b.WriteByte('\n')
	}
	for i := 0; i <= n/perRun; i++ {
		if keyed(i) {
			if err := os.WriteFile(filepath.Join(keyDir, "r-old-"+strconv.Itoa(i)+".key"), key, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	file := filepath.Join(f.home, ".code-forge", "ledger", f.project+".jsonl")
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f b749Fixture) server() *server {
	s := &server{home: f.home, repo: f.repo, run: f.run, project: f.project, trees: make(map[string]*treeState)}
	s.queue.Dir = filepath.Join(f.repo, ".code-forge", "queue")
	return s
}

// TestB749Measure prints refresh, sweep and serve timings for the report.
// It runs only with B749_MEASURE=1.
func TestB749Measure(t *testing.T) {
	if os.Getenv("B749_MEASURE") != "1" {
		t.Skip("B749_MEASURE=1 prints the B749 timings")
	}
	t.Setenv("CODE_FORGE_PRUNE_GRACE", "10m")
	shapes := []struct {
		name   string
		perRun int
		keyed  func(int) bool
	}{
		{"every-row-run.end-nokey", 1, func(int) bool { return false }},
		{"every-row-run.end-keyed", 1, func(int) bool { return true }},
		{"100-rows-per-run-keyed", 100, func(int) bool { return true }},
		{"100-rows-per-run-nokey", 100, func(int) bool { return false }},
	}
	sizes := []int{10000, 100000}
	if os.Getenv("B749_N") != "" {
		one, _ := strconv.Atoi(os.Getenv("B749_N"))
		sizes = []int{one}
	}
	for _, n := range sizes {
		for _, shape := range shapes {
			begin := time.Now()
			f := b749Write(t, n, shape.perRun, shape.keyed, func(int) bool { return false })
			t.Logf("fixture rows=%d shape=%s built in %v", n, shape.name, time.Since(begin).Round(time.Millisecond))
			s := f.server()
			start := time.Now()
			if err := s.refresh(); err != nil {
				t.Fatal(err)
			}
			refresh := time.Since(start)
			start = time.Now()
			s.sweep(time.Now())
			sweep := time.Since(start)
			// Serve: time to socket and to the first ping answer.
			socket := filepath.Join(f.home, ".code-forge", "runs", f.run, "native.sock")
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			start = time.Now()
			go func() { done <- ServeContext(ctx, f.home, f.run, "v", time.Minute) }()
			var listen, answer time.Duration
			for answer == 0 {
				if listen == 0 {
					if _, err := os.Lstat(socket); err == nil {
						listen = time.Since(start)
					}
				}
				if conn, err := net.DialTimeout("unix", socket, time.Second); err == nil {
					conn.SetDeadline(time.Now().Add(30 * time.Second))
					fmt.Fprintln(conn, `{"method":"ping"}`)
					buf := make([]byte, 4096)
					if k, err := conn.Read(buf); err == nil && strings.Contains(string(buf[:k]), `"ok":true`) {
						answer = time.Since(start)
					}
					conn.Close()
				}
				if answer == 0 {
					select {
					case err := <-done:
						t.Fatalf("daemon ended before it answered: %v", err)
					case <-time.After(time.Millisecond):
					}
				}
			}
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			t.Logf("rows=%d shape=%s refresh=%v sweep=%v serve-listen=%v serve-first-answer=%v", n, shape.name, refresh.Round(time.Millisecond), sweep.Round(time.Millisecond), listen.Round(time.Millisecond), answer.Round(time.Millisecond))
		}
	}
}
