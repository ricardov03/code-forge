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
	blockLandChain(t, "plain")
}

func TestBlockLandMergeGateReuse(t *testing.T) {
	for _, shape := range []string{"same-tree", "prior-green", "prior-docs", "prior-red", "prior-skipped", "prior-timeout", "prior-partial", "main-empty-commit", "main-moved", "docs-moved", "partial", "commands-changed"} {
		t.Run(shape, func(t *testing.T) { blockLandChain(t, shape) })
	}
}

func blockLandChain(t *testing.T, shape string) {
	t.Helper()
	// The daemon's Unix socket needs a short private HOME on macOS.
	home, err := os.MkdirTemp("/tmp", "cf-land-unit-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NATIVE", "1")
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	nodeVersion := reviewWaitNodeVersion
	reviewWaitNodeVersion = "v24.22.1"
	t.Cleanup(func() { reviewWaitNodeVersion = nodeVersion })
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
	if shape == "prior-docs" {
		must(os.WriteFile(filepath.Join(repo, "README.md"), []byte("base docs\n"), 0600))
	}
	if shape == "prior-partial" {
		must(os.WriteFile(filepath.Join(repo, "sample.test.mjs"), []byte("// fixture\n"), 0600))
	}
	if shape != "plain" {
		command := `printf '%s\n' "$PWD" >> "$HOME/gate-cwds"`
		if shape == "prior-red" || shape == "prior-timeout" {
			command += `; if [ ! -f "$HOME/first-gate" ]; then : > "$HOME/first-gate"; `
			if shape == "prior-red" {
				command += "exit 1; fi"
			} else {
				command += "exec sleep 60; fi"
			}
		}
		gate := []string{"/bin/sh", "-c", command, "gate", "{test_files}"}
		cfg, err := json.Marshal(landTestConfig(gate))
		must(err)
		must(os.WriteFile(filepath.Join(repo, ".code-forge.yml"), cfg, 0600))
	}
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "base")
	base := git(repo, "rev-parse", "HEAD")
	tree := filepath.Join(repo, ".code-forge/worktrees/B1")
	git(repo, "worktree", "add", "-qb", "block-B1", tree, base)
	must(os.WriteFile(filepath.Join(tree, "sample.mjs"), []byte("new\n"), 0600))
	if shape == "partial" {
		must(os.WriteFile(filepath.Join(tree, "sample.test.mjs"), []byte("// fixture\n"), 0600))
	}
	if shape == "commands-changed" {
		cfg, err := json.Marshal(landTestConfig([]string{"/bin/sh", "-c", `printf '%s\n' "$PWD" >> "$HOME/gate-cwds"`, "changed", "{test_files}"}))
		must(err)
		must(os.WriteFile(filepath.Join(tree, ".code-forge.yml"), cfg, 0600))
	}
	git(tree, "add", ".")
	git(tree, "commit", "-qm", "change")
	blockTree := git(tree, "rev-parse", "HEAD^{tree}")
	if shape == "main-empty-commit" {
		git(repo, "commit", "--allow-empty", "-qm", "no content change")
	}
	if shape == "main-moved" || shape == "docs-moved" {
		file := "other.mjs"
		if shape == "docs-moved" {
			file = "README.md"
		}
		must(os.WriteFile(filepath.Join(repo, file), []byte("main moved\n"), 0600))
		git(repo, "add", file)
		git(repo, "commit", "-qm", "main change")
	}
	run := "r-land-unit"
	entry := ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{"sample.mjs"}}, {Key: "base_sha", Value: base}, {Key: "base_branch", Value: "main"}, {Key: "tree_at_open", Value: ledger.Object{}}, {Key: "kind", Value: "code"}, {Key: "test_files", Value: []any{}}, {Key: "worktree", Value: ledger.Object{{Key: "path", Value: tree}, {Key: "branch", Value: "block-B1"}, {Key: "base_sha", Value: base}}}}
	if shape == "partial" || shape == "prior-partial" {
		if shape == "partial" {
			entry.Set("owned_files", []any{"sample.mjs", "sample.test.mjs"})
		}
		entry.Set("test_files", []any{"sample.test.mjs"})
	}
	if shape == "commands-changed" {
		entry.Set("owned_files", []any{"sample.mjs", ".code-forge.yml"})
	}
	if shape == "prior-docs" {
		entry.Set("owned_files", []any{"sample.mjs", "README.md"})
	}
	record := ledger.Object{{Key: "run_id", Value: run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: repo}, {Key: "status", Value: "active"}, {Key: "worker", Value: ledger.Object{{Key: "pid", Value: float64(os.Getpid())}, {Key: "started_at", Value: statusStart(float64(os.Getpid()))}}}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}}
	must(state.SaveRun(home, record))
	must(os.WriteFile(filepath.Join(home, ".code-forge/runs", run+".key"), make([]byte, 32), 0600))
	if strings.HasPrefix(shape, "prior-") {
		args := []string{"run", "--run", run, "--block", "B1", "--changed", "--cwd", tree}
		if shape == "prior-timeout" {
			args = append(args, "--timeout-ms", "100")
		}
		options, _ := gatesParse(args)
		prior := landCapture(func(s *landStep) int {
			code, safe := gatesNativeStep(args, options, s)
			if !safe {
				t.Error("prior gates must run natively")
			}
			return code
		}, nil)
		badPrior := shape == "prior-red" || shape == "prior-timeout"
		if (prior.code != 0) != badPrior || (prior.data.Get("allOk") == true) == badPrior {
			t.Fatalf("prior full run failed: %+v", prior)
		}
		if shape == "prior-skipped" {
			if !gatesReusableFull(tree) {
				t.Fatal("real prior-green row must be reusable before changing skipped")
			}
			file := filepath.Join(home, ".code-forge/ledger/fixture.jsonl")
			data, err := os.ReadFile(file)
			must(err)
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			changed := 0
			for i, line := range lines {
				row, err := ledger.DecodeObject([]byte(line))
				must(err)
				if row.Get("event") != "gate.done" || row.Get("run") != run || row.Get("block") != "B1" {
					continue
				}
				results, _ := row.Get("results").([]any)
				for _, value := range results {
					result := blockObject(value)
					if result.Get("gate") == "test" {
						if result.Get("skipped") != false || result.Get("ok") != true || blockString(result.Get("full_stamp")) == "" {
							t.Fatalf("expected a stamped green test result: %v", result)
						}
						// Preserve the real tree/command stamp and every other field.
						result.Set("skipped", true)
						changed++
					}
				}
				copy, err := ledger.Stringify(row, false)
				must(err)
				lines[i] = string(copy)
			}
			if changed != 1 {
				t.Fatalf("changed %d prior test results, want exactly 1", changed)
			}
			// Replace the original green row so it cannot authorize reuse itself.
			must(os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0600))
		}
		if shape == "prior-timeout" {
			results, _ := prior.data.Get("results").([]any)
			if len(results) != 4 || blockObject(results[0]).Get("reason") != "timeout" {
				t.Fatalf("prior run must contain exactly 4 gate results and a timed-out test: %v", results)
			}
		}
		if shape == "prior-partial" {
			entry.Set("test_files", []any{})
			blocks := blockObject(record.Get("blocks"))
			blocks.Set("B1", entry)
			record.Set("blocks", blocks)
			must(state.SaveRun(home, record))
		}
		if shape == "prior-docs" {
			must(os.WriteFile(filepath.Join(tree, "README.md"), []byte("changed docs\n"), 0600))
			git(tree, "commit", "-qam", "docs after full run")
			blockTree = git(tree, "rev-parse", "HEAD^{tree}")
		}
	}
	hash, err := gitx.ContentHash(tree, "sample.mjs")
	must(err)
	owned, _ := entry.Get("owned_files").([]any)
	for _, file := range owned[1:] {
		hash, err := gitx.ContentHash(tree, file.(string))
		must(err)
		_, err = state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "review.approved"}, {Key: "block", Value: "B1"}, {Key: "file", Value: file}, {Key: "content_hash", Value: hash}})
		must(err)
	}
	_, err = state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "dispatch"}, {Key: "block", Value: "B1"}, {Key: "level", Value: "L2"}})
	must(err)
	_, err = state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "review.approved"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "sample.mjs"}, {Key: "content_hash", Value: hash}})
	must(err)
	report, transcript := filepath.Join(home, "report"), filepath.Join(home, "transcript")
	must(os.WriteFile(report, []byte("TESTS go test exit=0\n===BLOCK B1 COMPLETE===\n"), 0600))
	must(os.WriteFile(transcript, []byte("Completed implementation.\n"), 0600))
	if shape != "plain" && shape != "same-tree" {
		if !strings.HasPrefix(shape, "prior-") {
			args := []string{"run", "--run", run, "--block", "B1", "--changed", "--cwd", tree}
			options, _ := gatesParse(args)
			gate := landCapture(func(s *landStep) int {
				code, safe := gatesNativeStep(args, options, s)
				if !safe {
					t.Error("block gates must run natively")
				}
				return code
			}, nil)
			if gate.code != 0 || gate.data.Get("allOk") != true {
				t.Fatalf("block gates failed: %+v", gate)
			}
		}
		result := landMergeGate(home, run, "B1", repo, tree, repo, "block-B1", "main")
		if result.reason != "" {
			t.Fatalf("merge gate failed: %s", result.reason)
		}
		cwds, err := os.ReadFile(filepath.Join(home, "gate-cwds"))
		must(err)
		calls := strings.Fields(string(cwds))
		reused := shape == "prior-green" || shape == "main-empty-commit"
		wantCalls := 2
		if reused {
			wantCalls = 1
		}
		if len(calls) != wantCalls {
			t.Errorf("suite executions=%d (%v), want %d", len(calls), calls, wantCalls)
		}
		for _, cwd := range calls {
			if reused && cwd != tree {
				t.Errorf("reused merge gate must execute 0 trial suites; got cwd %s", cwd)
			}
		}
		if !reused && len(calls) > 0 {
			last := calls[len(calls)-1]
			if filepath.Dir(last) != filepath.Dir(tree) || !strings.HasPrefix(filepath.Base(last), ".merge-gate-") {
				t.Errorf("last suite cwd=%s, want exactly one sibling trial worktree", last)
			}
		}
		if strings.Contains(git(repo, "worktree", "list", "--porcelain"), ".merge-gate-") {
			t.Error("trial worktree count must be 0 after merge gate")
		}
		return
	}
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
	flags := map[string]any{"report": report, "transcript": transcript, "max": "60s", "json": true}
	if shape != "plain" {
		flags["merge"] = true
	}
	out, stderr := os.Stdout, os.Stderr
	capture, err := os.CreateTemp(home, "land-output-")
	must(err)
	os.Stdout, os.Stderr = capture, capture
	code := landExecuteWithReason(nil, flags, home, run, "B1", record, entry, func(reason handoverReason) int {
		t.Errorf("the complete chain must not hand over: %v", reason)
		return 127
	})
	os.Stdout, os.Stderr = out, stderr
	must(capture.Close())
	output, err := os.ReadFile(capture.Name())
	must(err)
	if code != 0 {
		t.Fatalf("land exit=%d, want 0: %s", code, output)
	}
	fresh, err := state.ReadRun(home, run)
	must(err)
	if got := blockObject(blockObject(fresh.Get("blocks")).Get("B1")).Get("status"); got != "closed" {
		t.Fatalf("block status=%v, want closed", got)
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), "fixture")
	must(err)
	counts := map[string]int{}
	var timing map[string]any
	for _, v := range rows {
		row := v.(map[string]any)
		counts[row["event"].(string)]++
		if row["event"] == "land.timing" {
			timing = row
		}
	}
	for _, event := range []string{"gate.done", "block.close", "land.timing"} {
		if counts[event] != 1 {
			t.Errorf("%s count=%d, want 1", event, counts[event])
		}
	}
	if shape != "plain" {
		line := "merge gate: reused green full suite on tree " + blockTree[:12] + "\n"
		if got := strings.Count(string(output), "merge gate: reused green full suite on tree "); got != 1 || strings.Count(string(output), line) != 1 {
			t.Errorf("reuse line count=%d, want 1 with exact tree %s: %s", got, blockTree[:12], output)
		}
		if timing["merge_gate_reused"] != true || timing["merge_gate_tree"] != blockTree {
			t.Errorf("land.timing reuse=%v tree=%v, want reuse=true tree=%s", timing["merge_gate_reused"], timing["merge_gate_tree"], blockTree)
		}
		cwds, err := os.ReadFile(filepath.Join(home, "gate-cwds"))
		must(err)
		calls := strings.Fields(string(cwds))
		if len(calls) != 1 || calls[0] != tree {
			t.Errorf("suite cwds=%v, want exactly one run in %s", calls, tree)
		}
		parents := strings.Fields(git(repo, "rev-list", "--parents", "-n", "1", "HEAD"))
		if len(parents) != 3 || parents[2] != git(tree, "rev-parse", "HEAD") {
			t.Errorf("merge parents=%v, want exactly main and block tips", parents)
		}
		if strings.Contains(git(repo, "worktree", "list", "--porcelain"), ".merge-gate-") {
			t.Error("trial worktree count must be 0 after land")
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

func landTestConfig(test any) map[string]any {
	return map[string]any{"version": 1, "project": map[string]any{"slug": "fixture"}, "provider": "anthropic", "engine": "harness",
		"levels": map[string]any{"L0": map[string]any{"model": "claude-haiku-4-5-20251001"}, "L1": map[string]any{"model": "claude-sonnet-5"}, "L2": map[string]any{"model": "claude-opus-5-5"}, "L3": map[string]any{"model": "claude-fable-5-1"}},
		"gates":  map[string]any{"test": test, "lint": nil, "types": nil, "format": nil}}
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

func TestLandClaimFilesRequiresCheckedScope(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = repo, gitx.ChildEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-qb", "main")
	git("-c", "user.name=Fixture", "-c", "user.email=f@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base")
	entry := ledger.Object{{Key: "base_sha", Value: git("rev-parse", "HEAD")}, {Key: "owned_files", Value: []any{}}}
	record := ledger.Object{{Key: "workspace", Value: repo}, {Key: "project", Value: "fixture"}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}}
	const run = "r-scope-unit"
	dir := filepath.Join(home, ".code-forge/runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, run+".key"), make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "outsider.mjs"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	files, err := landClaimFiles(home, run, "B1", repo, repo, record, entry)
	if err == nil || files != nil {
		t.Fatalf("unchecked scope must fail without claim candidates: files=%v err=%v", files, err)
	}
	entry.Set("tree_at_open", ledger.Object{})
	blocks := blockObject(record.Get("blocks"))
	blocks.Set("B1", entry)
	record.Set("blocks", blocks)
	files, err = landClaimFiles(home, run, "B1", repo, repo, record, entry)
	if err != nil || len(files) != 1 || files[0] != "outsider.mjs" {
		t.Fatalf("checked scope must return the unowned change: files=%v err=%v", files, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".code-forge/ledger")); !os.IsNotExist(err) {
		t.Fatalf("scope check wrote ledger: %v", err)
	}
}
