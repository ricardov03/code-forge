package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ticket"
)

// B730: the TypeError Node's reviewIdentity throws for a queued attempt of
// the current content (its enqueued_at a number, null, missing or an object
// whose toString is not callable) no longer hands review-file enqueue, the
// --wait-block join, block close (its requeue and --auto-claim) or block
// reopen (the owner's and the agent's) to Node. Go prints what Node prints:
// the refused JSON of review-file, the "block close:" or "block reopen:"
// failure. Every case runs on the B717 fixture (src/a.go, a live worker, a
// stopped review of the old content where the setup says so) with two
// attempts of the current content in the queue; testdata/b730/<case>.txt is
// Node's run of the same fixture (B730_RECORD_NODE=<repo root>).

type b730Case struct {
	name, setup, ask, args string
	first, second          string // the raw enqueued_at of the two attempts
}

const (
	b730Enqueue = "review-file|src/a.go|--block|B1|--run|r-test"
	b730Wait    = "review-file|--wait-block|B1|--run|r-test|--max=0ms"
	b730Close   = "block|close|B1|--run|r-test"
	b730Judge   = "block|reopen|B1|--run|r-test|--file|src/a.go|--reason|src/a.go:1 changed|--yes"
	b730Owner   = "block|reopen|B1|--run|r-test|--file|src/a.go|--reason| the owner split the fix "
	b730Stamp   = `"2026-11-01T00:00:01.000Z"`
)

var b730Cases = []b730Case{
	// review-file enqueue (and --fresh): the refused JSON, exit 1. V8's
	// insertion sort calls compare(second, first), so the second attempt's
	// enqueued_at is the localeCompare receiver; Date.parse reads both.
	{"enqueue-number", "none", "notty", b730Enqueue, b730Stamp, `5`},
	{"enqueue-null", "none", "notty", b730Enqueue, b730Stamp, `null`},
	{"enqueue-missing", "none", "notty", b730Enqueue, b730Stamp, ""},
	{"enqueue-array", "none", "notty", b730Enqueue, b730Stamp, `["a"]`},
	{"enqueue-object", "none", "notty", b730Enqueue, `{"toString":1}`, b730Stamp},
	{"enqueue-fresh", "none", "notty", b730Enqueue + "|--fresh", b730Stamp, `5`},
	// the join: its own resolve and the requeue of a changed stopped review
	{"wait-number", "none", "notty", b730Wait, b730Stamp, `5`},
	{"wait-requeue-number", "stopped", "notty", b730Wait, b730Stamp, `5`},
	{"wait-requeue-object", "stopped", "notty", b730Wait, `"a"`, `{"toString":1}`},
	// block close: its requeue, and --auto-claim's enqueue and join
	{"close-requeue-number", "stopped-down", "yes", b730Close, b730Stamp, `5`},
	{"close-autoclaim-number", "none", "yes", b730Close + "|--auto-claim", b730Stamp, `5`},
	{"close-autoclaim-requeue", "stopped", "yes", b730Close + "|--auto-claim", b730Stamp, `null`},
	// --auto-claim claims the unowned src/b.go, so its own join runs
	// (claim-<setup> also writes src/b.go)
	{"close-autoclaim-join-number", "claim-none", "yes", b730Close + "|--auto-claim", b730Stamp, `5`},
	{"close-autoclaim-join-requeue", "claim-stopped", "yes", b730Close + "|--auto-claim", `{"toString":1}`, b730Stamp},
	// block reopen: the owner's (after the question) and the agent's (after the judge)
	{"reopen-owner-number", "stopped", "yes", b730Owner, b730Stamp, `5`},
	{"reopen-owner-object-json", "stopped", "yes", b730Owner + "|--json", `{"toString":1}`, b730Stamp},
	{"reopen-judge-number", "stopped", "notty", b730Judge, b730Stamp, `5`},
	{"reopen-judge-null-json", "stopped", "notty", b730Judge + "|--json", b730Stamp, `null`},
}

// b730Attempts writes two reopened attempts of src/a.go's current content:
// the ticket whose id sorts first (the queue is read in name order) gets
// first as its raw enqueued_at ("" leaves the field out), the other second.
func (f *b612Fixture) b730Attempts(first, second string) {
	f.t.Helper()
	top, _ := filepath.EvalSymlinks(f.workspace)
	dir := filepath.Join(top, ".code-forge", "queue")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		f.t.Fatal(err)
	}
	a, b := "2026-10-01T00:00:00.000Z", "2026-10-02T00:00:00.000Z"
	parts := func(at string) ticket.Parts {
		return ticket.Parts{Run: "r-test", Block: "B1", File: "src/a.go", ContentHash: b680Hash(b612Content), ReopenedAt: &at}
	}
	if ticket.ReviewID(parts(b)) < ticket.ReviewID(parts(a)) {
		a, b = b, a
	}
	for _, at := range []struct{ at, enqueued string }{{a, first}, {b, second}} {
		id := ticket.ReviewID(parts(at.at))
		body := `{"ticket":"` + id + `","run":"r-test","block":"B1","file":"src/a.go","content_hash":"` + b680Hash(b612Content) + `"`
		if at.enqueued != "" {
			body += `,"enqueued_at":` + at.enqueued
		}
		body += `,"reopened_at":"` + at.at + `"}`
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *b612Fixture) b730Env() []string {
	return []string{"CODE_FORGE_NO_ERROR_LOG=1", "TMPDIR=" + f.tmp, "CODE_FORGE_TMP_ROOT=", "FAKE_DIR=" + f.fake, "FAKE_VERDICT=agree"}
}

// b730Go runs the Go router in the workspace; a hand-over reaches the
// missing Node (127).
func (f *b612Fixture) b730Go(test, ask, args string) hvResult {
	f.t.Helper()
	asked := filepath.Join(f.t.TempDir(), "asked")
	cmd := exec.Command(os.Args[0], "-test.run=^"+test+"$")
	cmd.Dir = f.workspace
	cmd.Env = append(append(os.Environ(), "HOME="+f.home, "BLOCK_HV_CHILD=1", "BLOCK_HV_ASK="+ask, "BLOCK_HV_ASKED="+asked, "BLOCK_HV_ARGS="+args, "CODE_FORGE_NODE=/nonexistent",
		"PATH="+f.bin+":/usr/bin:/bin"), f.b730Env()...)
	return b680Exec(f.t, cmd, asked)
}

// b730Node runs a block verb at a terminal through runBlock with the answer
// injected, anything else through bin/code-forge with Node only.
func (f *b612Fixture) b730Node(root, ask, args string) hvResult {
	f.t.Helper()
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		f.t.Fatal(err)
	}
	asked := filepath.Join(f.t.TempDir(), "asked")
	var cmd *exec.Cmd
	if strings.HasPrefix(args, "block|") && ask != "notty" {
		driver := filepath.Join(f.t.TempDir(), "b730-driver.mjs")
		if err := os.WriteFile(driver, []byte(b680NodeDriver), 0o600); err != nil {
			f.t.Fatal(err)
		}
		cmd = exec.Command(nodeBin, append([]string{driver, root, ask, asked}, strings.Split(args, "|")[1:]...)...)
	} else {
		cmd = exec.Command(filepath.Join(root, "bin", "code-forge"), strings.Split(args, "|")...)
	}
	cmd.Dir = f.workspace
	cmd.Env = append([]string{"HOME=" + f.home, "CODE_FORGE_NATIVE=0", "PATH=" + f.bin + ":" + filepath.Dir(nodeBin) + ":/usr/bin:/bin"}, f.b730Env()...)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err = cmd.Run()
	code := 0
	if e, ok := err.(*exec.ExitError); ok {
		code = e.ExitCode()
	} else if err != nil {
		f.t.Fatal(err)
	}
	question, _ := os.ReadFile(asked)
	return hvResult{code: code, stdout: out.String(), stderr: stderr.String(), asked: string(question)}
}

var b730Worker = regexp.MustCompile(`"pid": [0-9]+,\s*"started_at": "[^"]*"`)

func TestB730IdentityTypeErrorMatchesNode(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	const test = "TestB730IdentityTypeErrorMatchesNode"
	b760NoNodeRecorder(t, "B730_RECORD_NODE")
	record := os.Getenv("B730_RECORD_NODE")
	for _, c := range b730Cases {
		t.Run(c.name, func(t *testing.T) {
			f := newB612Fixture(t, true)
			setup, claim := strings.CutPrefix(c.setup, "claim-")
			f.b717Setup(test, setup)
			if claim {
				// an empty tree at open: every unowned change is the block's
				record := f.record()
				record["blocks"].(map[string]any)["B1"].(map[string]any)["tree_at_open"] = map[string]any{}
				f.writeRecord(record)
				mirror, _ := json.Marshal(record)
				if err := os.WriteFile(filepath.Join(f.workspace, ".code-forge", "runs", "r-test.json"), mirror, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(f.workspace, "src", "b.go"), []byte("package b\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			f.b730Attempts(c.first, c.second)
			before := len(f.rawLedger())
			golden := filepath.Join("testdata", "b730", c.name+".txt")
			var r hvResult
			if record != "" {
				r = f.b730Node(record, c.ask, c.args)
			} else {
				r = f.b730Go(test, c.ask, c.args)
				// every case runs in Go: no hand-over is logged
				if log, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl")); err == nil {
					t.Errorf("Go handed the case over: %s", log)
				}
			}
			// a claim rewrites the run record indented: its pinned worker too
			got := b730Worker.ReplaceAllString("asked="+r.asked+"\n"+f.b717Report(r, before), `"pid": <PID>, "started_at": "<START>"`)
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
				t.Fatalf("report differs from Node's (exit %s)\n--- got\n%s\n--- want (Node)\n%s", strconv.Itoa(r.code), got, want)
			}
		})
	}
}
