package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

func TestBlockLandNativeUsage(t *testing.T) {
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "a block id and --run are required"},
		{[]string{"B1", "--run", "r-unit"}, "block land takes one block id, --run, --report, --transcript and optional --max/--merge/--no-merge-gate/--json"},
		{[]string{"B1", "--run", "r-unit", "--report", "r", "--transcript", "t", "--max", "bad"}, "--max must look like 90s, 30m or 1500ms"},
		{[]string{"--merge=true"}, "--merge takes no value"},
		{[]string{"--merge", "--merge"}, "--merge given more than once"},
	} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		before := os.Stderr
		os.Stderr = w
		code := runBlock(append([]string{"land"}, tc.args...))
		os.Stderr = before
		_ = w.Close()
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			t.Fatal(err)
		}
		if code != 2 || string(data) != "block land: "+tc.want+"\n" {
			t.Errorf("%v: exit=%d stderr=%q; want exit=2 stderr=%q", tc.args, code, data, "block land: "+tc.want+"\n")
		}
	}
}

func TestBlockLandFullDaemonChain(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "cf-land-unit-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NATIVE", "1")
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = gitx.ChildEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	git(repo, "init", "-qb", "main")
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "f@example.invalid")
	git(repo, "config", "commit.gpgsign", "false")
	must(os.WriteFile(filepath.Join(repo, "sample.mjs"), []byte("old\n"), 0600))
	must(os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".code-forge/\n"), 0600))
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "base")
	base := git(repo, "rev-parse", "HEAD")
	tree := filepath.Join(repo, ".code-forge/worktrees/B1")
	git(repo, "worktree", "add", "-qb", "block-B1", tree, base)
	must(os.WriteFile(filepath.Join(tree, "sample.mjs"), []byte("new\n"), 0600))
	git(tree, "add", ".")
	git(tree, "commit", "-qm", "change")
	run := "r-land-unit"
	entry := ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{"sample.mjs"}}, {Key: "base_sha", Value: base}, {Key: "base_branch", Value: "main"}, {Key: "tree_at_open", Value: ledger.Object{}}, {Key: "kind", Value: "code"}, {Key: "test_files", Value: []any{}}, {Key: "worktree", Value: ledger.Object{{Key: "path", Value: tree}, {Key: "branch", Value: "block-B1"}, {Key: "base_sha", Value: base}}}}
	record := ledger.Object{{Key: "run_id", Value: run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: repo}, {Key: "status", Value: "active"}, {Key: "worker", Value: ledger.Object{{Key: "pid", Value: float64(os.Getpid())}, {Key: "started_at", Value: statusStart(float64(os.Getpid()))}}}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}}
	must(state.SaveRun(home, record))
	must(os.WriteFile(filepath.Join(home, ".code-forge/runs", run+".key"), make([]byte, 32), 0600))
	hash, err := gitx.ContentHash(tree, "sample.mjs")
	must(err)
	_, err = state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "dispatch"}, {Key: "block", Value: "B1"}, {Key: "level", Value: "L2"}})
	must(err)
	_, err = state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "review.approved"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.mjs"}, {Key: "content_hash", Value: hash}})
	must(err)
	report, transcript := filepath.Join(home, "report"), filepath.Join(home, "transcript")
	must(os.WriteFile(report, []byte("TESTS go test exit=0\n===BLOCK B1 COMPLETE===\n"), 0600))
	must(os.WriteFile(transcript, []byte("Completed implementation.\n"), 0600))
	socketDir := filepath.Join(home, ".code-forge/runs", run)
	must(os.MkdirAll(socketDir, 0700))
	listener, err := net.Listen("unix", filepath.Join(socketDir, "native.sock"))
	must(err)
	var connections sync.WaitGroup
	serving := make(chan struct{})
	requests := make(chan string, 16)
	go func() {
		defer close(serving)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			go func() {
				defer connections.Done()
				defer conn.Close()
				scan := bufio.NewScanner(conn)
				for scan.Scan() {
					var request map[string]any
					if json.Unmarshal(scan.Bytes(), &request) != nil {
						return
					}
					method, _ := request["method"].(string)
					requests <- method
					var reply any
					switch method {
					case "ping":
						reply = map[string]any{"ok": true, "run": run, "version": version}
					case "wait-block":
						reply = map[string]any{"block": "B1", "files": []any{map[string]any{"file": "sample.mjs", "status": "reviewed", "outcome": "approved"}}, "done": true}
					case "rows":
						rows, _ := ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), "fixture")
						reply = map[string]any{"rows": rows}
					}
					if json.NewEncoder(conn).Encode(reply) != nil {
						return
					}
				}
			}()
		}
	}()
	defer func() { listener.Close(); <-serving; connections.Wait() }()
	code := landExecute(nil, map[string]any{"report": report, "transcript": transcript, "max": "10s", "json": true}, home, run, "B1", record, entry, func() int { t.Error("the complete chain must not hand over"); return 127 })
	if code != 0 {
		t.Fatalf("land exit=%d", code)
	}
	fresh, err := state.ReadRun(home, run)
	must(err)
	if blockObject(blockObject(fresh.Get("blocks")).Get("B1")).Get("status") != "closed" {
		t.Fatal("block did not close")
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), "fixture")
	must(err)
	counts := map[string]int{}
	for _, v := range rows {
		counts[v.(map[string]any)["event"].(string)]++
	}
	for _, event := range []string{"gate.done", "block.close", "land.timing"} {
		if counts[event] != 1 {
			t.Errorf("%s count=%d, want 1", event, counts[event])
		}
	}
	seen := map[string]int{}
	for len(requests) > 0 {
		seen[<-requests]++
	}
	if seen["wait-block"] != 1 || seen["rows"] != 1 {
		t.Fatalf("unexpected daemon steps: %v", seen)
	}
}

func TestBlockLandDurationRefusals(t *testing.T) {
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	for _, raw := range []string{"9999999m", "1.5s", "-1"} {
		code := runBlock([]string{"land", "B1", "--run", "r-unit", "--report", "r", "--transcript", "t", "--max", raw})
		if code != 2 {
			t.Errorf("max %q: exit=%d, want 2 without Node", raw, code)
		}
	}
}
