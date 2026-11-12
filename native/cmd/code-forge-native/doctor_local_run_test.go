package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B504: the full doctor run without a project config runs in Go. The rows
// are Node's (CODE_FORGE_NATIVE=0 ./bin/code-forge doctor in the same temp
// HOME); the sweep and memory rows read the live process table, so only
// their place and label are checked.

func b504NoConfigRows(home string) []string {
	return []string{
		"FAIL config: not-found: .code-forge.yml cannot be loaded (run code-forge init)",
		"WARN links: no harness link recorded (run code-forge init)",
		"WARN keys: jev key not resolved; System 1 falls back to rules",
		"OK   ledger: writable",
		"OK   locks: no unreadable or malformed locks",
		"OK   tmp: 0 stale roots, 0 stale pids",
		"sweep",
		"memory",
		"INFO review concurrency: 6 tickets at once; provider slots anthropic 4 · openai 2 · xai 2",
		"FAIL probes: skipped: no valid config",
		"INFO signer: same-user boundary only",
		"INFO native binary: not found, using Node",
		"INFO native daemon: off",
	}
}

func b504CheckRows(t *testing.T, got string, want []string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("rows:\n%s\nwant %d rows", got, len(want))
	}
	for i, w := range want {
		if w == "sweep" || w == "memory" {
			if !strings.Contains(lines[i], " "+w+": ") {
				t.Errorf("row %d = %q, want the %s row", i, lines[i], w)
			}
			continue
		}
		if lines[i] != w {
			t.Errorf("row %d = %q, want %q", i, lines[i], w)
		}
	}
}

func b504NoConfig(t *testing.T) (home, repo string) {
	t.Helper()
	home, repo = b474Project(t, "", 0)
	t.Setenv("CODE_FORGE_NATIVE", "0") // Node's own run says "not found" too
	t.Setenv("PATH", os.Getenv("PATH"))
	return home, repo
}

func TestB504DoctorNoConfigRunsInGo(t *testing.T) {
	home, _ := b504NoConfig(t)
	code, out, errText := runCaptured(t, "doctor")
	if code != 1 {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, out, errText)
	}
	b504CheckRows(t, out, b504NoConfigRows(home))
	// the sweep row is OK or WARN with the live process table
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		counts[strings.TrimSpace(line[:4])]++
	}
	summary := fmt.Sprintf("doctor: %d ok, %d warn, %d fail", counts["OK"], counts["WARN"], counts["FAIL"])
	if counts["FAIL"] != 2 || errText != summary+"\n" {
		t.Errorf("stderr = %q, want %q", errText, summary)
	}
	if _, err := os.Stat(filepath.Join(home, ".code-forge", "ledger")); err != nil {
		t.Errorf("checkLedger makes the ledger folder: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	if !strings.Contains(string(data), `"verb":"doctor"`) || !strings.Contains(string(data), `"message":"`+summary+`"`) {
		t.Errorf("error log = %s", data)
	}
}

func TestB504DoctorNoConfigJSON(t *testing.T) {
	b504NoConfig(t)
	code, out, _ := runCaptured(t, "doctor", "--json")
	if code != 1 {
		t.Fatalf("code=%d stdout=%s", code, out)
	}
	var doc struct {
		Rows []struct{ ID, Status, Label, Detail string } `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	var ids []string
	for _, r := range doc.Rows {
		ids = append(ids, r.ID)
	}
	want := "config links keys ledger locks tmp sweep memory review-concurrency probes signer native native_daemon"
	if strings.Join(ids, " ") != want {
		t.Errorf("ids = %s\nwant %s", strings.Join(ids, " "), want)
	}
}

// --cwd and an empty folder outside any repository run in Go too.
func TestB504DoctorNoConfigFollowsCwd(t *testing.T) {
	_, repo := b504NoConfig(t)
	empty, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"doctor"}, {"doctor", "--cwd", repo}} {
		code, out, errText := runCaptured(t, args...)
		if code != 1 || !strings.HasPrefix(out, "FAIL config: not-found: ") || !strings.Contains(errText, ", 2 fail") {
			t.Errorf("%q: code=%d stdout=%s stderr=%s", args, code, out, errText)
		}
	}
}

// Stale tmp roots, a bad lock and a bad worker-registry entry give Node's
// WARN rows.
func TestB504DoctorNoConfigStaleState(t *testing.T) {
	home, repo := b504NoConfig(t)
	tmp := os.Getenv("CODE_FORGE_TMP_ROOT")
	old := filepath.Join(tmp, "old1")
	for _, dir := range []string{filepath.Join(old, "pids"), filepath.Join(home, ".code-forge", "locks"), filepath.Join(repo, ".code-forge", "workers")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		filepath.Join(old, "owner.json"):                          `{"pid":999991,"start_time":"x"}`,
		filepath.Join(old, "pids", "999992.json"):                 `{"pid":999992,"start_time":"x"}`,
		filepath.Join(home, ".code-forge", "locks", "a.lock"):     "not json",
		filepath.Join(repo, ".code-forge", "workers", "1-x.json"): "{}",
	}
	for p, body := range files {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	code, out, _ := runCaptured(t, "doctor")
	for _, want := range []string{
		"WARN locks: " + filepath.Join(home, ".code-forge", "locks", "a.lock") + "\n",
		"WARN worker registry: " + filepath.Join(repo, ".code-forge", "workers", "1-x.json") + "\n",
	} {
		if code != 1 || !strings.Contains(out, want) {
			t.Errorf("code=%d stdout=%s\nwant %q", code, out, want)
		}
	}
	if !strings.Contains(out, "\nWARN tmp: 1 stale roots, 1 stale pids (the next code-forge run start removes them)\n") {
		t.Errorf("tmp row: %s", out)
	}
}
