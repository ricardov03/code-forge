package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type reviewWaitTrace struct {
	Polls  int
	Pauses []time.Duration
}

func TestReviewWaitProcess(t *testing.T) {
	if os.Getenv("REVIEW_WAIT_TEST_CHILD") != "1" {
		return
	}
	trace := reviewWaitTrace{}
	code := runReviewFileWaitWith([]string{"--wait", "abcdef0123456789abcdef01", "--max=0ms"},
		func(ctx context.Context, root string, deadline time.Time) (bool, bool) {
			trace.Polls++
			return reviewWaitLive(ctx, root, deadline)
		},
		func(ctx context.Context, pause time.Duration) bool {
			trace.Pauses = append(trace.Pauses, pause)
			return ctx.Err() == nil
		})
	data, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("REVIEW_WAIT_TEST_TRACE"), data, 0600); err != nil {
		t.Fatal(err)
	}
	os.Exit(code)
}

func reviewWaitTestEnv(home string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "HOME=") && !strings.HasPrefix(entry, "CODE_FORGE_") && !strings.HasPrefix(entry, "REVIEW_WAIT_TEST_") {
			env = append(env, entry)
		}
	}
	return append(env, "HOME="+home)
}

func TestReviewWaitWorkerDownLogging(t *testing.T) {
	for _, mode := range []string{"default", "0", "1", "unwritable"} {
		t.Run(mode, func(t *testing.T) {
			repo, home := t.TempDir(), t.TempDir()
			git := exec.Command("git", "init", "-q", repo)
			git.Env = reviewWaitTestEnv(home)
			if out, err := git.CombinedOutput(); err != nil {
				t.Fatalf("git init: %v: %s", err, out)
			}
			logs := filepath.Join(home, ".code-forge", "logs")
			if err := os.MkdirAll(logs, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(logs, "breadcrumbs.json"), []byte(`["init","bad/value","run start"]`), 0600); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(logs, "errors.jsonl")
			if mode == "unwritable" {
				if err := os.Mkdir(file, 0700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestReviewWaitProcess$")
			cmd.Dir = repo
			traceFile := filepath.Join(t.TempDir(), "trace.json")
			cmd.Env = append(reviewWaitTestEnv(home), "CODE_FORGE_NODE=/nonexistent-code-forge-node", "REVIEW_WAIT_TEST_CHILD=1", "REVIEW_WAIT_TEST_TRACE="+traceFile)
			if mode == "0" || mode == "1" {
				cmd.Env = append(cmd.Env, "CODE_FORGE_NO_ERROR_LOG="+mode)
			}
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 3 {
				t.Fatalf("want worker_down exit 3, got %v; stderr %s", err, stderr.String())
			}
			if string(out) != "{\"ticket\":\"abcdef0123456789abcdef01\",\"status\":\"worker_down\"}\n" || stderr.Len() != 0 {
				t.Fatalf("unexpected streams: stdout %q, stderr %q", out, stderr.String())
			}
			traceData, err := os.ReadFile(traceFile)
			if err != nil {
				t.Fatal(err)
			}
			var trace reviewWaitTrace
			if err := json.Unmarshal(traceData, &trace); err != nil {
				t.Fatal(err)
			}
			if trace.Polls != 2 {
				t.Fatalf("worker_down: want exactly 2 down polls, got %d", trace.Polls)
			}
			if len(trace.Pauses) != 1 || trace.Pauses[0] != 200*time.Millisecond {
				t.Fatalf("worker_down: want exactly one 200ms sleep between polls, got %v", trace.Pauses)
			}
			data, err := os.ReadFile(file)
			if mode == "1" || mode == "unwritable" {
				if err == nil {
					t.Fatalf("unexpected log %s", data)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var entry map[string]any
			if json.Unmarshal(data, &entry) != nil {
				t.Fatalf("expected exactly one JSON log entry: %s", data)
			}
			if entry["exit"] != float64(3) || entry["kind"] != "error" || entry["message"] != nil || entry["stack"] != nil || entry["verb"] != "review-file" {
				t.Fatalf("wrong failure shape: %s", data)
			}
			if len(entry) != 15 {
				t.Fatalf("expected Node's 15 log fields: %s", data)
			}
			if _, err := time.Parse(time.RFC3339Nano, entry["ts"].(string)); err != nil {
				t.Fatal(err)
			}
			if got := entry["before"].([]any); len(got) != 2 || got[0] != "init" || got[1] != "run start" {
				t.Fatalf("wrong prior commands: %v", got)
			}
			if info, err := os.Stat(file); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("log permissions: %v, %v", info, err)
			}
		})
	}
}
