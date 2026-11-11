package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type reviewWaitTrace struct {
	Polls  int
	Pauses []time.Duration
}

func TestReviewWaitProcess(t *testing.T) {
	if os.Getenv("REVIEW_WAIT_TEST_CHILD") != "1" {
		return
	}
	trace := reviewWaitTrace{}
	code := runReviewFileWaitWith([]string{"--wait", "abcdef0123456789abcdef01", "--max=0ms"},
		func(ctx context.Context, root string, deadline time.Time) (bool, bool) {
			trace.Polls++
			return reviewWaitLive(ctx, root, deadline)
		},
		func(ctx context.Context, pause time.Duration) bool {
			trace.Pauses = append(trace.Pauses, pause)
			return ctx.Err() == nil
		})
	data, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("REVIEW_WAIT_TEST_TRACE"), data, 0600); err != nil {
		t.Fatal(err)
	}
	os.Exit(code)
}

func reviewWaitTestEnv(home string) []string {
	env := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "HOME=") && !strings.HasPrefix(entry, "CODE_FORGE_") && !strings.HasPrefix(entry, "REVIEW_WAIT_TEST_") {
			env = append(env, entry)
		}
	}
	return append(env, "HOME="+home)
}

func TestReviewWaitWorkerDownLogging(t *testing.T) {
	for _, mode := range []string{"default", "0", "1", "unwritable"} {
		t.Run(mode, func(t *testing.T) {
			repo, home := t.TempDir(), t.TempDir()
			git := exec.Command("git", "init", "-q", repo)
			git.Env = reviewWaitTestEnv(home)
			if out, err := git.CombinedOutput(); err != nil {
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
			traceFile := filepath.Join(t.TempDir(), "trace.json")
			cmd.Env = append(reviewWaitTestEnv(home), "CODE_FORGE_NODE=/nonexistent-code-forge-node", "REVIEW_WAIT_TEST_CHILD=1", "REVIEW_WAIT_TEST_TRACE="+traceFile)
			if mode == "0" || mode == "1" {
				cmd.Env = append(cmd.Env, "CODE_FORGE_NO_ERROR_LOG="+mode)
			}
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 3 {
				t.Fatalf("want worker_down exit 3, got %v; stderr %s", err, stderr.String())
			}
			if string(out) != "{\"ticket\":\"abcdef0123456789abcdef01\",\"status\":\"worker_down\"}\n" || stderr.Len() != 0 {
				t.Fatalf("unexpected streams: stdout %q, stderr %q", out, stderr.String())
			}
			traceData, err := os.ReadFile(traceFile)
			if err != nil {
				t.Fatal(err)
			}
			var trace reviewWaitTrace
			if err := json.Unmarshal(traceData, &trace); err != nil {
				t.Fatal(err)
			}
			if trace.Polls != 2 {
				t.Fatalf("worker_down: want exactly 2 down polls, got %d", trace.Polls)
			}
			if len(trace.Pauses) != 1 || trace.Pauses[0] != 200*time.Millisecond {
				t.Fatalf("worker_down: want exactly one 200ms sleep between polls, got %v", trace.Pauses)
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

// TestReviewFileChild runs the review-file verb with the arguments after "--".
func TestReviewFileChild(t *testing.T) {
	if os.Getenv("REVIEW_FILE_TEST_CHILD") != "1" {
		return
	}
	args := []string{}
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	os.Exit(runReviewFileWait(args))
}

// reviewFileExtraEnv is added to every reviewFileExec child.
var reviewFileExtraEnv []string

type reviewFileRun struct {
	stdout, stderr string
	code           int
}

// reviewFileExec runs review-file in dir. A handover would exit 127: Node is
// never reachable here.
func reviewFileExec(t *testing.T, dir, home string, logging bool, args ...string) reviewFileRun {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestReviewFileChild$", "--"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(reviewWaitTestEnv(home), "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CODE_FORGE_NATIVE=1", "REVIEW_FILE_TEST_CHILD=1", "GIT_CEILING_DIRECTORIES="+filepath.Dir(dir))
	if !logging {
		cmd.Env = append(cmd.Env, "CODE_FORGE_NO_ERROR_LOG=1")
	}
	cmd.Env = append(cmd.Env, reviewFileExtraEnv...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return reviewFileRun{stdout.String(), stderr.String(), code}
}

func reviewFileRepo(t *testing.T, home string) string {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base"}} {
		git := exec.Command("git", args...)
		git.Dir, git.Env = repo, reviewWaitTestEnv(home)
		if out, err := git.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return repo
}

func TestReviewFileHelpIsNative(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"--help", "--wait-block", "B1"}, {"-h", "--bogus"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := t.TempDir()
			got := reviewFileExec(t, t.TempDir(), home, true, args...)
			if got != (reviewFileRun{reviewWaitUsage, "", 0}) {
				t.Fatalf("got %+v, want usage on stdout with exit 0", got)
			}
			if _, err := os.Stat(filepath.Join(home, ".code-forge")); !os.IsNotExist(err) {
				t.Fatalf("help wrote under HOME: %v", err)
			}
		})
	}
	// Boundary: help after another argument is a flag like any other.
	home := t.TempDir()
	got := reviewFileExec(t, t.TempDir(), home, false, "--wait", "--help")
	if got.code != 2 || got.stderr != "review-file: --wait needs a value\n"+reviewWaitUsage {
		t.Fatalf("--wait --help = %+v, want the needs-a-value usage error", got)
	}
}

func TestReviewFileNoRepoRefusalIsNative(t *testing.T) {
	const want = `{"status":"refused","reason":"no-repo","message":"not inside a git repository"}` + "\n"
	for _, args := range [][]string{
		{"a.go", "--block", "B1"},
		{"--wait", "abcdef0123456789abcdef01"},
		{"--wait-block", "B1", "--run", "r-test"},
		{"--rebase-check", "--block", "B1", "--run", "r-test"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			got := reviewFileExec(t, t.TempDir(), t.TempDir(), false, args...)
			if got != (reviewFileRun{want, "", 1}) {
				t.Fatalf("got %+v, want the no-repo refusal with exit 1", got)
			}
		})
	}
}

func TestReviewFileWaitUsageIsNative(t *testing.T) {
	home := t.TempDir()
	repo := reviewFileRepo(t, home)
	for _, tc := range []struct {
		args    []string
		message string
	}{
		{[]string{"--max", "5s", "--wait", "abc"}, "a ticket id is 24 lowercase hex characters"},
		{[]string{"--wait=abcdef0123456789abcdef01", "--max", "5m"}, "--max must look like 90s or 1500ms"},
		{[]string{"--wait", "abcdef0123456789abcdef01", "--block", "B1"}, "--wait takes only a ticket and --max"},
		{[]string{"--wait", "abcdef0123456789abcdef01", "--wait", "abcdef0123456789abcdef02"}, "--wait given more than once"},
		{[]string{"--fresh", "--wait-block", "B1", "--run", "r-test"}, "--wait-block takes a block, --run and --max only"},
		{[]string{"--wait-block", "B1", "--run", "r-test", "--max", "1.5s"}, "--max must look like 90s or 1500ms"},
		{[]string{"--cwd", filepath.Join(repo, "missing"), "--wait-block", "B1", "--run", "r-test"}, "--cwd must name an existing directory"},
		{[]string{"--wait", "abcdef0123456789abcdef01", "--cwd", filepath.Join(repo, "missing")}, "--cwd must name an existing directory"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			got := reviewFileExec(t, repo, home, false, tc.args...)
			if got != (reviewFileRun{"", "review-file: " + tc.message + "\n" + reviewWaitUsage, 2}) {
				t.Fatalf("got %+v, want usage %q with exit 2", got, tc.message)
			}
		})
	}
}

func TestReviewFileArgsDecodeLikeNode(t *testing.T) {
	for _, tc := range []struct{ arg, want string }{
		{"--x\xe2\x82y", "--x�y"},       // one maximal subpart
		{"--x\xff\xfey", "--x��y"},      // two bad bytes
		{"--x\xed\xa0\x80y", "--x���y"}, // a surrogate is three subparts
		{"--x\xf0\x9f\x98y", "--x�y"},   // truncated four-byte form
	} {
		t.Run(tc.want, func(t *testing.T) {
			got := reviewFileExec(t, t.TempDir(), t.TempDir(), false, tc.arg)
			want := "review-file: unknown flag " + statusQuote(tc.want) + "\n" + reviewWaitUsage
			if got.code != 2 || got.stderr != want || got.stdout != "" {
				t.Fatalf("got %+v, want stderr %q", got, want)
			}
		})
	}
}

func TestReviewFileBlockScopeUnreadableIsNative(t *testing.T) {
	const want = `{"status":"refused","reason":"block-scope-unreadable","message":"the block scope cannot be read"}` + "\n"
	for _, record := range []string{"", `{"run_id":"r-other","blocks":{"B1":{"owned_files":["a.go"]}}}`, `{"run_id":"r-test","blocks":{}}`} {
		t.Run(record, func(t *testing.T) {
			home := t.TempDir()
			repo := reviewFileRepo(t, home)
			if record != "" {
				dir := filepath.Join(repo, ".code-forge", "runs")
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "r-test.json"), []byte(record), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got := reviewFileExec(t, repo, home, false, "--wait-block", "B1", "--run", "r-test", "--max=0ms")
			if got != (reviewFileRun{want, "", 1}) {
				t.Fatalf("got %+v, want block-scope-unreadable with exit 1", got)
			}
		})
	}
}

func TestReviewFileWaitBlockLocalJoinIsNative(t *testing.T) {
	home := t.TempDir()
	repo := reviewFileRepo(t, home)
	dir := filepath.Join(repo, ".code-forge", "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	head := exec.Command("git", "rev-parse", "HEAD")
	head.Dir = repo
	base, err := head.Output()
	if err != nil {
		t.Fatal(err)
	}
	record := `{"run_id":"r-test","project":"p","blocks":{"B1":{"owned_files":["*.go"],"base_sha":"` + strings.TrimSpace(string(base)) + `"}}}`
	if err := os.WriteFile(filepath.Join(dir, "r-test.json"), []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// No worker and no daemon: Node's join reports worker_down on the second poll.
	got := reviewFileExec(t, repo, home, false, "--wait-block", "B1", "--run", "r-test", "--max=0ms")
	const want = `{"block":"B1","files":[{"file":"a.go","ticket":null,"status":"not_queued","outcome":null}],"done":false,"status":"worker_down"}` + "\n"
	if got != (reviewFileRun{want, "", 3}) {
		t.Fatalf("got %+v, want worker_down with exit 3", got)
	}
	if _, err := os.Stat(filepath.Join(repo, ".code-forge", "queue")); !os.IsNotExist(err) {
		t.Fatalf("a down worker must not create queue files: %v", err)
	}
}

func reviewFileQueue(t *testing.T, repo string) map[string]string {
	t.Helper()
	files := map[string]string{}
	dir := filepath.Join(repo, ".code-forge", "queue")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[entry.Name()] = string(data)
	}
	return files
}

func TestReviewFileWaitBlockStoppedReviewHandsOver(t *testing.T) {
	for _, findings := range []string{`[{"id":"F1"}]`, `[]`} {
		t.Run(findings, func(t *testing.T) {
			home := t.TempDir()
			repo := reviewFileRepo(t, home)
			head := exec.Command("git", "rev-parse", "HEAD")
			head.Dir = repo
			base, err := head.Output()
			if err != nil {
				t.Fatal(err)
			}
			code := filepath.Join(repo, ".code-forge")
			stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
			for name, body := range map[string]string{
				"runs/r-test.json": `{"run_id":"r-test","project":"p","blocks":{"B1":{"owned_files":["*.go"],"base_sha":"` + strings.TrimSpace(string(base)) + `"}}}`,
				"workers/" + strconv.Itoa(os.Getpid()) + "-unknown.json": `{"pid":` + strconv.Itoa(os.Getpid()) + `,"start_time":"unknown","run":"r-test","started_at":"` + stamp + `","heartbeat_at":"` + stamp + `"}`,
			} {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(code, name)), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(code, name), []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n"), 0600); err != nil {
				t.Fatal(err)
			}
			args := []string{"--wait-block", "B1", "--run", "r-test", "--max=0ms"}
			seed := reviewFileExec(t, repo, home, false, args...)
			var answer struct {
				Files []struct{ Ticket string } `json:"files"`
			}
			if seed.code != 0 || json.Unmarshal([]byte(seed.stdout), &answer) != nil || len(answer.Files) != 1 || answer.Files[0].Ticket == "" {
				t.Fatalf("seed join = %+v, want one pending ticket", seed)
			}
			id := answer.Files[0].Ticket
			if err := os.MkdirAll(filepath.Join(code, "reviews"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(code, "reviews", "stopped.json"), []byte(`{"status":"stopped","findings":`+findings+`}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(code, "queue", id+".done"), []byte(`{"ticket":"`+id+`","status":"stopped","result":".code-forge/reviews/stopped.json"}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a // changed\n"), 0600); err != nil {
				t.Fatal(err)
			}
			before := reviewFileQueue(t, repo)
			reviewFileExtraEnv = []string{"CODE_FORGE_HANDOVER_LOG=1"}
			defer func() { reviewFileExtraEnv = nil }()
			got := reviewFileExec(t, repo, home, false, args...)
			rows, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
			if findings == `[]` {
				// No findings: no requeue candidate, the join answers in Go.
				if got.code != 0 || !strings.Contains(got.stdout, `"outcome":"stopped"`) || len(rows) != 0 {
					t.Fatalf("got %+v rows %q, want a native done answer", got, rows)
				}
				return
			}
			if got.code != 127 || got.stdout != "" {
				t.Fatalf("got %+v, want the hand-over to the missing Node (127)", got)
			}
			var row map[string]any
			if err := json.Unmarshal(rows, &row); err != nil || row["reason"] != "wait-block-stopped" {
				t.Fatalf("hand-over rows %q, want one wait-block-stopped row", rows)
			}
			if after := reviewFileQueue(t, repo); !reflect.DeepEqual(after, before) {
				t.Fatalf("Go changed the queue before the hand-over:\nbefore %v\nafter  %v", before, after)
			}
		})
	}
}
