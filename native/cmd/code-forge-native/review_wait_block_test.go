package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
)

func TestReviewWaitBlockProcess(t *testing.T) {
	if os.Getenv("REVIEW_WAIT_BLOCK_CHILD") == "1" {
		os.Exit(runReviewFileWait([]string{"--wait-block", "B1", "--run", "r-test", "--max=0ms"}))
	}
}

func TestReviewWaitBlockDaemonRoute(t *testing.T) {
	repo := t.TempDir()
	initRepo := exec.Command("git", "init", "-q", repo)
	initRepo.Dir, initRepo.Env = repo, gitx.ChildEnv()
	if out, err := initRepo.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	dir := filepath.Join(repo, ".code-forge", "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "r-test.json"), []byte(`{"run_id":"r-test","blocks":{"B1":{"owned_files":["sample.mjs"]}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	// macOS limits Unix socket names; use a short temporary socket directory.
	home, err := os.MkdirTemp("/tmp", "wb-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	socketDir := filepath.Join(home, ".code-forge", "runs", "r-test")
	if err := os.MkdirAll(socketDir, 0700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(socketDir, "native.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	finished := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			finished <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		scan := bufio.NewScanner(conn)
		for scan.Scan() {
			var req map[string]any
			if err := json.Unmarshal(scan.Bytes(), &req); err != nil {
				finished <- err
				return
			}
			if req["method"] == "ping" {
				json.NewEncoder(conn).Encode(map[string]any{"ok": true, "version": version, "run": "r-test"})
			} else {
				json.NewEncoder(conn).Encode(map[string]any{"block": "B1", "files": []any{}, "done": true})
			}
		}
		finished <- scan.Err()
	}()
	cmd := exec.Command(os.Args[0], "-test.run=^TestReviewWaitBlockProcess$")
	cmd.Dir = repo
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "HOME=") && !strings.HasPrefix(e, "GIT_") && !strings.HasPrefix(e, "CODE_FORGE_NATIVE=") && !strings.HasPrefix(e, "CODE_FORGE_NODE=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+home, "CODE_FORGE_NATIVE=1", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CODE_FORGE_NO_ERROR_LOG=1", "REVIEW_WAIT_BLOCK_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("live daemon must stay in Go: %v: %s", err, out)
	}
	if string(out) != "{\"block\":\"B1\",\"files\":[],\"done\":true}\n" {
		t.Fatalf("want one ordered empty completion, got %q", out)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
