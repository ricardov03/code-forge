package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewWaitProcess(t *testing.T) {
	if os.Getenv("REVIEW_WAIT_TEST_CHILD") != "1" {
		return
	}
	os.Exit(runReviewFileWait([]string{"--wait", "abcdef0123456789abcdef01", "--max=0ms"}))
}

func TestReviewWaitWorkerDownLogging(t *testing.T) {
	for _, mode := range []string{"default", "0", "1", "unwritable"} {
		t.Run(mode, func(t *testing.T) {
			repo, home := t.TempDir(), t.TempDir()
			if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
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
			for _, entry := range os.Environ() {
				if !strings.HasPrefix(entry, "HOME=") && !strings.HasPrefix(entry, "CODE_FORGE_NO_ERROR_LOG=") && !strings.HasPrefix(entry, "CODE_FORGE_NODE=") {
					cmd.Env = append(cmd.Env, entry)
				}
			}
			cmd.Env = append(cmd.Env, "HOME="+home, "CODE_FORGE_NODE=/nonexistent-code-forge-node", "REVIEW_WAIT_TEST_CHILD=1")
			if mode == "0" || mode == "1" {
				cmd.Env = append(cmd.Env, "CODE_FORGE_NO_ERROR_LOG="+mode)
			}
			var stderr strings.Builder
			cmd.Stderr = &stderr
			start := time.Now()
			out, err := cmd.Output()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 3 {
				t.Fatalf("want worker_down exit 3, got %v; stderr %s", err, stderr.String())
			}
			if string(out) != "{\"ticket\":\"abcdef0123456789abcdef01\",\"status\":\"worker_down\"}\n" || stderr.Len() != 0 {
				t.Fatalf("unexpected streams: stdout %q, stderr %q", out, stderr.String())
			}
			if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
				t.Fatalf("two down polls took %s", elapsed)
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
