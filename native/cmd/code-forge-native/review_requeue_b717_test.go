package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// B717: a block join restarts a changed stopped review in Go
// (requeueStoppedFiles: the signed automatic review.reopened row, the
// cleared done marker, the new ticket and the auto-requeue line), and block
// close's own requeue ends with "automatic requeue did not finish" when the
// join does not finish. Every case runs on the B612 run fixture (src/a.go)
// with a stopped ticket for the old content; the report is the B612 one
// (exit, stdout, stderr, new ledger rows without ts and mac, and every file
// under the workspace's .code-forge, HOME's runs folder and TMPDIR).
//
// Record Node's goldens with B717_RECORD_NODE=<repo root>.

var b717Cases = []struct{ name, setup, args string }{
	// review-file --wait-block, worker live: the requeue, then the join
	{"wait-requeue", "stopped", "review-file|--wait-block|B1|--run|r-test|--max=0ms"},
	{"wait-same-content", "same", "review-file|--wait-block|B1|--run|r-test|--max=0ms"},
	{"wait-reopen-other", "earlier-other", "review-file|--wait-block|B1|--run|r-test|--max=0ms"},
	{"wait-reopen-same", "earlier", "review-file|--wait-block|B1|--run|r-test|--max=0ms"},
	{"wait-tampered", "tampered", "review-file|--wait-block|B1|--run|r-test|--max=0ms"},
	{"wait-no-worker", "stopped-down", "review-file|--wait-block|B1|--run|r-test|--max=0ms"},
	// block close: the requeue runs without a live worker; the join then
	// sees the worker down and the close says so
	{"close-requeue-down", "stopped-down", "block|close|B1|--run|r-test"},
	{"close-reopen-other", "earlier-other", "block|close|B1|--run|r-test"},
	// --auto-claim joins with the live worker first: its requeue refusal
	// ends the close
	{"close-autoclaim-reopen-other", "earlier-other", "block|close|B1|--run|r-test|--auto-claim"},
}

// b717Setup: a committed base, the stopped ticket of "old\n" (seeded by a
// Go join with a live worker) with findings, the current content and the
// signed review state of setup.
func (f *b612Fixture) b717Setup(test, setup string) {
	f.t.Helper()
	git := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", f.workspace, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		// a fixed date keeps the base sha the same in every run
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			f.t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(f.workspace, ".gitignore"), []byte(".code-forge/\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	git("add", ".gitignore")
	git("commit", "-q", "-m", "base")
	head, err := exec.Command("git", "-C", f.workspace, "rev-parse", "HEAD").Output()
	if err != nil {
		f.t.Fatal(err)
	}
	record := f.record()
	blocks := record["blocks"].(map[string]any)
	blocks["B1"].(map[string]any)["base_sha"] = strings.TrimSpace(string(head))
	// block close checks the pinned worker: this test process
	record["worker"] = map[string]any{"pid": float64(os.Getpid()), "started_at": statusStart(float64(os.Getpid()))}
	record["orphans"] = []any{}
	f.writeRecord(record)
	code := filepath.Join(f.workspace, ".code-forge")
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	// waitBlockScope reads the run's workspace mirror
	mirror, _ := json.Marshal(record)
	if err := os.MkdirAll(filepath.Join(code, "runs"), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(code, "runs", "r-test.json"), mirror, 0o600); err != nil {
		f.t.Fatal(err)
	}
	pid := strconv.Itoa(os.Getpid())
	worker := filepath.Join(code, "workers", pid+"-unknown.json")
	if err := os.MkdirAll(filepath.Dir(worker), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(worker, []byte(`{"pid":`+pid+`,"start_time":"unknown","run":"r-test","started_at":"`+stamp+`","heartbeat_at":"`+stamp+`"}`), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.workspace, "src", "a.go"), []byte("old\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	seed := f.b717Go(test, "review-file|--wait-block|B1|--run|r-test|--max=0ms")
	var answer struct {
		Files []struct{ Ticket string } `json:"files"`
	}
	if seed.code != 0 || json.Unmarshal([]byte(seed.stdout), &answer) != nil || len(answer.Files) != 1 || answer.Files[0].Ticket == "" {
		f.t.Fatalf("seed join = %+v, want one pending ticket", seed)
	}
	id := answer.Files[0].Ticket
	if err := os.MkdirAll(filepath.Join(code, "reviews"), 0o700); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(code, "reviews", "stopped.json"), []byte(`{"status":"stopped","findings":[{"id":"F2"}]}`), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(code, "queue", id+".done"), []byte(`{"ticket":"`+id+`","status":"stopped","outcome":"stopped","result":".code-forge/reviews/stopped.json"}`), 0o600); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.workspace, "src", "a.go"), []byte(b612Content), 0o644); err != nil {
		f.t.Fatal(err)
	}
	switch setup {
	case "stopped-down":
		if err := os.Remove(worker); err != nil {
			f.t.Fatal(err)
		}
		setup = "stopped"
	}
	// the stop's cap row makes src/a.go a reviewed file of the block
	f.rows(map[string]any{"event": "dispatch", "block": "B1", "level": "L2"},
		map[string]any{"event": "review.cap", "block": "B1", "file": "src/a.go", "content_hash": b680Hash("old\n"), "open": []any{"F2"}})
	f.b680Setup(setup)
}

var b717Pin = regexp.MustCompile(`"worker":\{"pid":\d+,"started_at":"[^"]*"\}`)

// b717Report is the B612 report with the test process's pin and worker file
// name dropped.
func (f *b612Fixture) b717Report(r hvResult, before int) string {
	text := strings.ReplaceAll(f.report(r, before), "workers/"+strconv.Itoa(os.Getpid())+"-", "workers/<PID>-")
	text = b717Pin.ReplaceAllString(text, `"worker":<PIN>`)
	// queue file names are ticket ids (one holds a time): sort the queue
	// files by their normalized text, not their id
	lines := strings.SplitAfter(text, "\n")
	var out, queued []string
	var current *strings.Builder
	flush := func() {
		if current != nil {
			queued = append(queued, current.String())
			current = nil
		}
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "--- ") {
			flush()
			if strings.HasPrefix(line, "--- ws/queue/") {
				current = &strings.Builder{}
			} else if len(queued) > 0 {
				sort.Strings(queued)
				out = append(out, queued...)
				queued = nil
			}
		}
		if current != nil {
			current.WriteString(line)
		} else {
			out = append(out, line)
		}
	}
	flush()
	sort.Strings(queued)
	return strings.Join(append(out, queued...), "")
}

// b717Go runs the Go router in the workspace (review-file reads its cwd);
// a hand-over reaches the missing Node (127).
func (f *b612Fixture) b717Go(test, args string) hvResult {
	f.t.Helper()
	asked := filepath.Join(f.t.TempDir(), "asked")
	cmd := exec.Command(os.Args[0], "-test.run=^"+test+"$")
	cmd.Dir = f.workspace
	cmd.Env = append(os.Environ(), "HOME="+f.home, "BLOCK_HV_CHILD=1", "BLOCK_HV_ASK=yes", "BLOCK_HV_ASKED="+asked, "BLOCK_HV_ARGS="+args, "CODE_FORGE_NODE=/nonexistent", "CODE_FORGE_NO_ERROR_LOG=1",
		"PATH="+f.bin+":/usr/bin:/bin", "TMPDIR="+f.tmp, "CODE_FORGE_TMP_ROOT=")
	return b680Exec(f.t, cmd, asked)
}

// b717Node runs review-file through bin/code-forge with Node only, and
// block close through runBlock with the terminal answer injected.
func (f *b612Fixture) b717Node(root, args string) hvResult {
	f.t.Helper()
	if strings.HasPrefix(args, "block|") {
		return f.b680Node(root, "yes", args)
	}
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		f.t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(root, "bin", "code-forge"), strings.Split(args, "|")...)
	cmd.Dir = f.workspace
	cmd.Env = []string{"HOME=" + f.home, "CODE_FORGE_NATIVE=0", "CODE_FORGE_NO_ERROR_LOG=1", "PATH=" + f.bin + ":" + filepath.Dir(nodeBin) + ":/usr/bin:/bin", "TMPDIR=" + f.tmp, "CODE_FORGE_TMP_ROOT="}
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

func TestB717RequeueMatchesNode(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	b760NoNodeRecorder(t, "B717_RECORD_NODE")
	record := os.Getenv("B717_RECORD_NODE")
	for _, c := range b717Cases {
		t.Run(c.name, func(t *testing.T) {
			f := newB612Fixture(t, true)
			f.b717Setup("TestB717RequeueMatchesNode", c.setup)
			before := len(f.rawLedger())
			golden := filepath.Join("testdata", "b717", c.name+".txt")
			var r hvResult
			if record != "" {
				r = f.b717Node(record, c.args)
			} else {
				r = f.b717Go("TestB717RequeueMatchesNode", c.args)
			}
			got := f.b717Report(r, before)
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

// The automatic row verifies with the run key and the new ticket waits
// after it: the golden drops ts and mac, so check them here.
func TestB717RequeueSignsAndQueues(t *testing.T) {
	if os.Getenv("BLOCK_HV_CHILD") == "1" {
		blockHVChild()
	}
	f := newB612Fixture(t, true)
	f.b717Setup("TestB717RequeueSignsAndQueues", "stopped")
	r := f.b717Go("TestB717RequeueSignsAndQueues", "review-file|--wait-block|B1|--run|r-test|--max=0ms")
	if r.code != 0 || !strings.HasPrefix(r.stderr, "auto-requeue src/a.go: content changed after stopped review (ticket ") {
		t.Fatalf("exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	rows := f.ledger()
	row := rows[len(rows)-1]
	f.verified(row)
	if row["event"] != "review.reopened" || row["by"] != "auto" || row["from"] != "stopped" || row["to_hash"] != b680Hash(b612Content) {
		t.Fatalf("row %v", row)
	}
	var answer struct {
		Files []struct{ Ticket string } `json:"files"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &answer); err != nil || len(answer.Files) != 1 {
		t.Fatalf("stdout %q %v", r.stdout, err)
	}
	top, _ := filepath.EvalSymlinks(f.workspace)
	data, err := os.ReadFile(filepath.Join(top, ".code-forge", "queue", answer.Files[0].Ticket+".json"))
	var queued map[string]any
	if err != nil || json.Unmarshal(data, &queued) != nil || queued["reopened_at"] != row["ts"] {
		t.Fatalf("ticket %s %v", data, err)
	}
	if !strings.Contains(r.stderr, answer.Files[0].Ticket) {
		t.Fatalf("stderr %q does not name ticket %s", r.stderr, answer.Files[0].Ticket)
	}
}
