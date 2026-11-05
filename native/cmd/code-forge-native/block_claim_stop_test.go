package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/sign"
)

func TestBlockClaimStopUsage(t *testing.T) {
	if os.Getenv("BLOCK_USAGE_CHILD") == "1" {
		code, err := run(strings.Split(os.Getenv("BLOCK_USAGE_ARGS"), "|"))
		if err != nil {
			os.Stderr.WriteString(err.Error())
		}
		os.Exit(code)
	}
	for _, tc := range []struct{ args, message string }{
		{"block|claim", "block claim: a block id and --run are required\n"},
		{"block|claim|B1|--run|r-test", "block claim: block claim needs exactly one path\n"},
		{"block|stop|B1|--run|r-test", "block stop: block stop needs --reason\n"},
		{"block|stop|B1|--run|r-test|--json=true", "block stop: --json takes no value\n"},
	} {
		t.Run(tc.args, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestBlockClaimStopUsage$")
			cmd.Env = append(os.Environ(), "BLOCK_USAGE_CHILD=1", "BLOCK_USAGE_ARGS="+tc.args, "CODE_FORGE_NODE=/nonexistent", "CODE_FORGE_NO_ERROR_LOG=1")
			var out, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &stderr
			err := cmd.Run()
			if e, ok := err.(*exec.ExitError); !ok || e.ExitCode() != 2 {
				t.Fatalf("usage exit = %v, want 2; stderr %s", err, stderr.String())
			}
			if out.Len() != 0 || stderr.String() != tc.message {
				t.Fatalf("usage bytes stdout=%q stderr=%q, want empty stdout and %q", out.String(), stderr.String(), tc.message)
			}
		})
	}
	t.Run("signed mutations and done content", func(t *testing.T) {
		home, workspace := t.TempDir(), t.TempDir()
		dir := filepath.Join(home, ".code-forge", "runs")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		record := map[string]any{"run_id": "r-test", "project": "unit", "workspace": workspace, "status": "active", "orphans": []string{"new/child", "keep"}, "blocks": map[string]any{"B1": map[string]any{"status": "open", "owned_files": []string{}, "owned_kinds": map[string]any{}}}}
		data, _ := json.Marshal(record)
		if err := os.WriteFile(filepath.Join(dir, "r-test.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		key := bytes.Repeat([]byte{9}, 32)
		if err := os.WriteFile(filepath.Join(dir, "r-test.key"), key, 0600); err != nil {
			t.Fatal(err)
		}
		for _, args := range []string{"block|claim|B1|new|--run|r-test", "block|stop|B1|--run|r-test|--reason|done"} {
			cmd := exec.Command(os.Args[0], "-test.run=^TestBlockClaimStopUsage$")
			cmd.Env = append(os.Environ(), "HOME="+home, "BLOCK_USAGE_CHILD=1", "BLOCK_USAGE_ARGS="+args, "CODE_FORGE_NODE=/nonexistent", "CODE_FORGE_NO_ERROR_LOG=1")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("native mutation %s: %v, %s", args, err, out)
			}
		}
		data, err := os.ReadFile(filepath.Join(dir, "r-test.json"))
		if err != nil || json.Unmarshal(data, &record) != nil {
			t.Fatalf("read changed record: %v", err)
		}
		block := record["blocks"].(map[string]any)["B1"].(map[string]any)
		if block["status"] != "stopped" || block["stop_reason"] != "done" || len(block["owned_files"].([]any)) != 1 || block["owned_kinds"].(map[string]any)["new"] != "absent" {
			t.Fatalf("wrong stopped registry: %#v", block)
		}
		orphans := record["orphans"].([]any)
		if len(orphans) != 1 || orphans[0] != "keep" {
			t.Fatalf("covered orphans were not removed: %#v", orphans)
		}
		data, err = os.ReadFile(filepath.Join(home, ".code-forge", "ledger", "unit.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
		if len(lines) != 2 {
			t.Fatalf("signed rows = %d, want 2", len(lines))
		}
		for i, event := range []string{"block.claim", "block.stop"} {
			var row map[string]any
			if json.Unmarshal(lines[i], &row) != nil || row["event"] != event {
				t.Fatalf("row %d: want event %s, got %s", i, event, lines[i])
			}
			verified, err := sign.VerifyRow(row, key)
			if err != nil || !verified.OK {
				t.Fatalf("row %d signature: %#v, %v", i, verified, err)
			}
		}
		data, err = os.ReadFile(filepath.Join(dir, "r-test", "blocks", "B1.done.json"))
		var done map[string]any
		if err != nil || json.Unmarshal(data, &done) != nil || len(done) != 10 || done["reason"] != "done" || done["spent_usd"] != float64(0) || done["findings"] != float64(0) {
			t.Fatalf("done content: %s, %v", data, err)
		}
	})
}
