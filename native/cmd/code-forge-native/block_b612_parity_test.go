package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/blockstate"
	"github.com/ricardov03/code-forge/native/sign"
)

// B612 parity: every waive --yes and reopen --yes case runs on the same
// fixture and fake claude as Node did when testdata/b612/<case>.txt was
// recorded. The report holds the exit code, stdout, stderr, the new ledger
// rows (ts and mac dropped, key order kept), every file under the
// workspace's .code-forge (queue ticket, verify file), what the judge left
// in TMPDIR and under HOME's runs folder (the key aside), and each judge
// call's argv and stdin packet.
//
// Record Node's goldens with B612_RECORD_NODE=<repo root>: the test then runs
// <repo root>/bin/code-forge with CODE_FORGE_NATIVE=0 and writes the files.

var b612Cases = []struct{ name, kind, verdict, json string }{
	{"waive-agree", "waive", "agree", ""},
	{"waive-agree-json", "waive", "agree", "|--json"},
	{"waive-disagree", "waive", "disagree", ""},
	{"waive-disagree-json", "waive", "disagree", "|--json"},
	{"waive-broken", "waive", "broken", ""},
	{"waive-broken-json", "waive", "broken", "|--json"},
	{"reopen-agree", "reopen", "agree", ""},
	{"reopen-agree-json", "reopen", "agree", "|--json"},
	{"reopen-disagree", "reopen", "disagree", ""},
	{"reopen-disagree-json", "reopen", "disagree", "|--json"},
	{"reopen-broken", "reopen", "broken", ""},
	{"reopen-broken-json", "reopen", "broken", "|--json"},
	{"reopen-stopped", "stopped", "agree", ""},
	{"reopen-stopped-json", "stopped", "agree", "|--json"},
	{"reopen-open", "open", "agree", ""},
	{"reopen-open-json", "open", "agree", "|--json"},
	{"reopen-open-disagree", "open", "disagree", ""},
	{"waive-result", "result", "agree", ""},
	{"waive-result-forged", "forged", "agree", ""},
	{"reopen-body", "body", "agree", ""},
	{"reopen-body-json", "body", "agree", "|--json"},
}

// b612State writes a signed review state file for src/a.go in Node's own
// key order (not sorted), with open findings, and its review.state anchor.
func (f *b612Fixture) b612State(status string) {
	f.t.Helper()
	if status == "body" {
		f.b612Body()
		return
	}
	old := sha256.Sum256([]byte("old\n"))
	state := `{"file":"src/a.go","status":"` + status + `","round":2,"next":{"reason":"cap"},"stop_content_hash":"` + hex.EncodeToString(old[:]) + `","open":[{"id":"F2","severity":"major","claim":"c","line":2,"evidence":"e","fix":"f"},{"severity":"minor","id":"F3","claim":"d"}]}`
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
	name := blockstate.Hash([]byte("src/a.go"))[:24] + ".json"
	dir := filepath.Join(f.home, ".code-forge", "runs", "r-test", "review-state", "B1")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		f.t.Fatal(err)
	}
	f.rows(map[string]any{"event": "review.state", "block": "B1", "file": "src/a.go", "seq": 3, "state_sha": blockstate.Hash(data)})
}

var (
	b612TS   = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{3}Z`)
	b612ID   = regexp.MustCompile(`\b[0-9a-f]{24}\b`)
	b612Drop = regexp.MustCompile(`,"(ts|mac)":"[^"]*"`)
	b612Pid  = regexp.MustCompile(`"pid":\d+,"start_time":"[^"]*"`)
)

func (f *b612Fixture) report(r hvResult, before int) string {
	var b strings.Builder
	b.WriteString("exit=" + strconv.Itoa(r.code) + "\n--- stdout\n" + r.stdout + "--- stderr\n" + r.stderr + "--- new rows\n")
	lines := f.rawLedger()
	for _, line := range lines[before:] {
		b.WriteString(b612Drop.ReplaceAllString(line, "") + "\n")
	}
	top, _ := filepath.EvalSymlinks(f.workspace)
	for _, root := range []struct{ label, dir string }{{"ws", filepath.Join(top, ".code-forge")}, {"runs", filepath.Join(f.home, ".code-forge", "runs")}, {"tmp", f.tmp}} {
		var names []string
		_ = filepath.WalkDir(root.dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && !strings.HasSuffix(path, ".key") {
				names = append(names, path)
			}
			return nil
		})
		sort.Strings(names)
		for _, name := range names {
			rel, _ := filepath.Rel(root.dir, name)
			data, _ := os.ReadFile(name)
			b.WriteString("--- " + root.label + "/" + b612ID.ReplaceAllString(rel, "<ID>") + "\n" + string(data) + "\n")
		}
	}
	args, _ := os.ReadFile(filepath.Join(f.fake, "args"))
	b.WriteString("--- judge argv\n" + string(args))
	names, _ := filepath.Glob(filepath.Join(f.fake, "stdin.*"))
	var packets []string
	for _, name := range names {
		data, _ := os.ReadFile(name)
		packets = append(packets, string(data))
	}
	sort.Strings(packets)
	for _, p := range packets {
		b.WriteString("--- judge stdin\n" + p + "\n")
	}
	text := b.String()
	for _, p := range []struct{ from, to string }{{top, "<WS>"}, {f.workspace, "<WS>"}, {f.tmp, "<TMP>"}, {f.home, "<HOME>"}} {
		text = strings.ReplaceAll(text, p.from, p.to)
	}
	text = b612Pid.ReplaceAllString(text, `"pid":<PID>,"start_time":"<START>"`)
	return b612ID.ReplaceAllString(b612TS.ReplaceAllString(text, "<TS>"), "<ID>")
}

// node runs the same call through the Node router of the repo at root.
func (f *b612Fixture) node(root, verdict, args string) hvResult {
	f.t.Helper()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		f.t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(root, "bin", "code-forge"), strings.Split(args, "|")...)
	cmd.Dir = f.workspace
	cmd.Env = []string{"HOME=" + f.home, "CODE_FORGE_NATIVE=0", "CODE_FORGE_NO_ERROR_LOG=1", "PATH=" + f.bin + ":" + filepath.Dir(nodeBin) + ":/usr/bin:/bin",
		"TMPDIR=" + f.tmp, "CODE_FORGE_TMP_ROOT=", "FAKE_DIR=" + f.fake, "FAKE_VERDICT=" + verdict}
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err = cmd.Run()
	code := 0
	if e, ok := err.(*exec.ExitError); ok {
		code = e.ExitCode()
	} else if err != nil {
		f.t.Fatal(err)
	}
	return hvResult{code: code, stdout: out.String(), stderr: stderr.String()}
}

// b612Body leaves no state file: the anchor row carries the signed state as
// state_body, and the ledger line keeps Node's (unsorted) key order.
func (f *b612Fixture) b612Body() {
	f.t.Helper()
	signed := func(text string) string {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(text), &parsed); err != nil {
			f.t.Fatal(err)
		}
		mac, err := sign.MACFor(parsed, f.key)
		if err != nil {
			f.t.Fatal(err)
		}
		return strings.TrimSuffix(text, "}") + `,"mac":"` + mac + `"}`
	}
	body := signed(`{"run":"r-test","block":"B1","file":"src/a.go","seq":3,"state":{"file":"src/a.go","status":"open","round":1,"open":[{"id":"F2","severity":"major","claim":"c","line":2,"evidence":"e","fix":"f"},{"severity":"minor","id":"F3","claim":"d"}]}}`)
	row := signed(`{"event":"review.state","block":"B1","file":"src/a.go","seq":3,"state_sha":"` + strings.Repeat("1", 64) + `","state_body":` + body + `,"run":"r-test","ts":"2026-11-13T00:00:00.000Z"}`)
	if err := os.MkdirAll(filepath.Join(f.home, ".code-forge", "ledger"), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"), []byte(row+"\n"), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// b612Result records F1 in a review.result row and in the ticket's signed
// result file (the verify file), whose claim and fix differ from the row's.
// forged breaks the file's mac, so the row's text is used.
func (f *b612Fixture) b612Result(forged bool) {
	f.t.Helper()
	const ticketID = "0123456789abcdef01234567"
	f.rows(map[string]any{"event": "review.result", "block": "B1", "file": "src/a.go", "ticket": ticketID, "open": []any{map[string]any{"id": "F1", "severity": "warning", "claim": "row claim", "evidence": "row evidence"}}})
	result, err := sign.SignRow(map[string]any{"ticket": ticketID, "run": "r-test", "block": "B1", "file": "src/a.go", "findings": []any{map[string]any{"id": "F1", "severity": "warning", "claim": "file claim", "evidence": "file evidence", "fix": "see src/a.go:3"}}, "ts": "2026-11-13T00:00:00.000Z"}, f.key)
	if err != nil {
		f.t.Fatal(err)
	}
	if forged {
		result["mac"] = strings.Repeat("0", 64)
	}
	data, _ := json.Marshal(result)
	top, _ := filepath.EvalSymlinks(f.workspace)
	dir := filepath.Join(top, ".code-forge", "reviews", "r-test")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ticketID+".json"), data, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func TestB612JudgeMatchesNode(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	record := os.Getenv("B612_RECORD_NODE")
	for _, c := range b612Cases {
		t.Run(c.name, func(t *testing.T) {
			f := newB612Fixture(t, true)
			args := "block|waive|B1|F1|--run|r-test|--file|src/a.go|--reason| see src/a.go:2 |--yes" + c.json
			switch c.kind {
			case "waive":
				f.triage()
			case "result", "forged":
				f.b612Result(c.kind == "forged")
			case "reopen":
				f.lost()
			default:
				f.b612State(c.kind)
			}
			if c.kind != "waive" && c.kind != "result" && c.kind != "forged" {
				args = "block|reopen|B1|--run|r-test|--file|src/a.go|--reason|src/a.go:1 changed|--yes" + c.json
			}
			before := len(f.rawLedger())
			golden := filepath.Join("testdata", "b612", c.name+".txt")
			if record != "" {
				got := f.report(f.node(record, c.verdict, args), before)
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
			if got := f.report(f.run("TestB612JudgeMatchesNode", c.verdict, args), before); got != string(want) {
				t.Fatalf("report differs from Node's\n--- got\n%s\n--- want (Node)\n%s", got, want)
			}
		})
	}
}
