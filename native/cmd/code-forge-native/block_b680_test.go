package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/blockstate"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/ticket"
)

// B680: block reopen at a terminal runs in Go. Every case runs on the same
// temp-HOME fixture (the B612 run, src/a.go) as Node did when
// testdata/b680/<case>.txt was recorded; the report holds the exit code,
// stdout, stderr, the question asked, the new ledger rows (ts and mac
// dropped, key order kept) and every file under the workspace's .code-forge
// (queue tickets, done markers), HOME's runs folder and TMPDIR.
//
// Record Node's goldens with B680_RECORD_NODE=<repo root>: the test then
// runs Node's runBlock in process with the terminal and the answer injected
// (the B80 test seam; no real terminal) and writes the files.

const b680NodeDriver = `import { writeFileSync } from 'node:fs';
import { pathToFileURL } from 'node:url';
const [root, ask, asked, ...args] = process.argv.slice(2);
const { runBlock } = await import(pathToFileURL(root + '/src/cli/block.mjs').href);
const ui = { confirm: async ({ message }) => { writeFileSync(asked, message); return ask === 'yes'; }, isCancel: () => false };
process.exitCode = await runBlock(args, { isTTY: ask !== 'notty', ui });
`

var b680Cases = []struct{ name, setup, ask, args string }{
	{"stopped", "stopped", "yes", ""},
	{"stopped-json", "stopped", "yes", "|--json"},
	{"stopped-done", "stopped-done", "yes", "|--json"},
	{"lost", "lost", "yes", ""},
	{"lost-json", "lost", "yes", "|--json"},
	{"same-content", "same", "yes", ""},
	{"open", "open", "yes", ""},
	{"no-state", "none", "yes", ""},
	{"tampered", "tampered", "yes", ""},
	{"earlier-same", "earlier", "yes", "|--json"},
	{"earlier-other", "earlier-other", "yes", ""},
	{"blank-reason", "stopped", "yes", "|--reason|   "},
	{"cancelled", "stopped", "no", ""},
	{"no-terminal", "stopped", "notty", ""},
	{"not-owned", "stopped", "yes", "|--file|src/b.go"},
	{"bad-path", "stopped", "yes", "|--file|src//a.go"},
	{"unknown-block", "stopped", "yes", "|B9"},
}

// b680State writes a signed review state for src/a.go (status, stop content
// hash) with its review.state anchor at seq 3; a wrong anchor sha tampers it.
func (f *b612Fixture) b680State(status, stopHash string, tamper bool) {
	f.t.Helper()
	state := `{"file":"src/a.go","status":"` + status + `","round":2,"next":{"reason":"review_cap"},"stop_content_hash":"` + stopHash + `","open":[{"id":"F2","severity":"major","claim":"c","line":2}]}`
	body := `{"run":"r-test","block":"B1","file":"src/a.go","seq":3,"state":` + state + `}`
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		f.t.Fatal(err)
	}
	mac, err := sign.MACFor(parsed, f.key)
	if err != nil {
		f.t.Fatal(err)
	}
	data := []byte(strings.TrimSuffix(body, "}") + `,"mac":"` + mac + `"}`)
	dir := filepath.Join(f.home, ".code-forge", "runs", "r-test", "review-state", "B1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, blockstate.Hash([]byte("src/a.go"))[:24]+".json"), data, 0o600); err != nil {
		f.t.Fatal(err)
	}
	sha := blockstate.Hash(data)
	if tamper {
		sha = strings.Repeat("ab", 32)
	}
	f.rows(map[string]any{"event": "review.state", "block": "B1", "file": "src/a.go", "seq": 3, "state_sha": sha})
}

func b680Hash(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }

func (f *b612Fixture) b680Setup(setup string) {
	f.t.Helper()
	current := b680Hash(b612Content)
	switch setup {
	case "stopped", "stopped-done", "earlier", "earlier-other":
		f.b680State("stopped", b680Hash("old\n"), false)
	case "same":
		f.b680State("stopped", current, false)
	case "open":
		f.b680State("open", "", false)
	case "tampered":
		f.b680State("stopped", b680Hash("old\n"), true)
	case "lost":
		f.lost()
	}
	switch setup {
	case "stopped-done":
		// the stop of the current content is cached: what a reopen clears
		top, _ := filepath.EvalSymlinks(f.workspace)
		dir := filepath.Join(top, ".code-forge", "queue")
		id, err := queue.Identity(dir, ticket.Parts{Run: "r-test", Block: "B1", File: "src/a.go", ContentHash: current})
		if err != nil {
			f.t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			f.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, id+".done"), []byte(`{"status":"stopped"}`), 0o644); err != nil {
			f.t.Fatal(err)
		}
	case "earlier", "earlier-other":
		to := current
		if setup == "earlier-other" {
			to = b680Hash("other\n")
		}
		f.rows(map[string]any{"event": "review.reopened", "block": "B1", "file": "src/a.go", "from": "stopped", "stop_reason": "review_cap", "at_seq": 3, "from_hash": b680Hash("old\n"), "to_hash": to, "reason": "first", "by": "human", "ts": "2026-11-14T00:00:00.000Z"})
	}
}

func (f *b612Fixture) b680Args(extra string) string {
	args := []string{"block", "reopen", "B1", "--run", "r-test", "--file", "src/a.go", "--reason", " the owner split the fix "}
	// an extra flag replaces its value; an extra positional replaces the id
	parts := strings.Split(strings.TrimPrefix(extra, "|"), "|")
	for i := 0; i < len(parts) && extra != ""; i++ {
		switch parts[i] {
		case "--json":
			args = append(args, "--json")
		case "--reason", "--file":
			for j := range args {
				if args[j] == parts[i] {
					args[j+1] = parts[i+1]
				}
			}
			i++
		default:
			args[2] = parts[i]
		}
	}
	return strings.Join(args, "|")
}

func (f *b612Fixture) b680Go(test, ask, args string) hvResult {
	f.t.Helper()
	asked := filepath.Join(f.t.TempDir(), "asked")
	cmd := exec.Command(os.Args[0], "-test.run=^"+test+"$")
	cmd.Env = append(os.Environ(), "HOME="+f.home, "BLOCK_HV_CHILD=1", "BLOCK_HV_ASK="+ask, "BLOCK_HV_ASKED="+asked, "BLOCK_HV_ARGS="+args, "CODE_FORGE_NODE=/nonexistent", "CODE_FORGE_NO_ERROR_LOG=1",
		"PATH="+f.bin+":/usr/bin:/bin", "TMPDIR="+f.tmp, "CODE_FORGE_TMP_ROOT=")
	return b680Exec(f.t, cmd, asked)
}

func (f *b612Fixture) b680Node(root, ask, args string) hvResult {
	f.t.Helper()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		f.t.Fatal(err)
	}
	driver := filepath.Join(f.t.TempDir(), "b680-driver.mjs")
	if err := os.WriteFile(driver, []byte(b680NodeDriver), 0o600); err != nil {
		f.t.Fatal(err)
	}
	asked := filepath.Join(f.t.TempDir(), "asked")
	cmd := exec.Command(nodeBin, append([]string{driver, root, ask, asked}, strings.Split(args, "|")[1:]...)...)
	cmd.Dir = f.workspace
	cmd.Env = []string{"HOME=" + f.home, "CODE_FORGE_NATIVE=0", "CODE_FORGE_NO_ERROR_LOG=1", "PATH=" + f.bin + ":" + filepath.Dir(nodeBin) + ":/usr/bin:/bin", "TMPDIR=" + f.tmp, "CODE_FORGE_TMP_ROOT="}
	return b680Exec(f.t, cmd, asked)
}

func b680Exec(t *testing.T, cmd *exec.Cmd, asked string) hvResult {
	t.Helper()
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	code := 0
	if e, ok := err.(*exec.ExitError); ok {
		code = e.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	question, _ := os.ReadFile(asked)
	return hvResult{code: code, stdout: out.String(), stderr: stderr.String(), asked: string(question)}
}

func TestB680HumanReopenMatchesNode(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	record := os.Getenv("B680_RECORD_NODE")
	for _, c := range b680Cases {
		t.Run(c.name, func(t *testing.T) {
			f := newB612Fixture(t, true)
			f.b680Setup(c.setup)
			args := f.b680Args(c.args)
			before := len(f.rawLedger())
			golden := filepath.Join("testdata", "b680", c.name+".txt")
			var r hvResult
			if record != "" {
				r = f.b680Node(record, c.ask, args)
			} else {
				r = f.b680Go("TestB680HumanReopenMatchesNode", c.ask, args)
			}
			got := "asked=" + r.asked + "\n" + f.report(r, before)
			if record != "" {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("report differs from Node's\n--- got\n%s\n--- want (Node)\n%s", got, want)
			}
		})
	}
}

// The signed row verifies with the run key and the ticket names it: the
// golden drops ts and mac, so check them here.
func TestB680HumanReopenSignsAndQueues(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	f := newB612Fixture(t, true)
	f.b680Setup("stopped-done")
	r := f.b680Go("TestB680HumanReopenSignsAndQueues", "yes", f.b680Args("|--json"))
	if r.code != 0 {
		t.Fatalf("exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	rows := f.ledger()
	row := rows[len(rows)-1]
	f.verified(row)
	if row["event"] != "review.reopened" || row["by"] != "human" || row["from"] != "stopped" || row["reason"] != "the owner split the fix" {
		t.Fatalf("row %v", row)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || out["ticket_cleared"] != true {
		t.Fatalf("stdout %q %v", r.stdout, err)
	}
	top, _ := filepath.EvalSymlinks(f.workspace)
	data, err := os.ReadFile(filepath.Join(top, ".code-forge", "queue", out["ticket"].(string)+".json"))
	var queued map[string]any
	if err != nil || json.Unmarshal(data, &queued) != nil || queued["reopened_at"] != row["ts"] {
		t.Fatalf("ticket %s %v", data, err)
	}
}
