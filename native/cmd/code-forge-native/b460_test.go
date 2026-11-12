package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// b460Call runs one native invocation with stdout and stderr captured.
func b460Call(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	stdout, _ := os.Create(filepath.Join(dir, "stdout"))
	stderr, _ := os.Create(filepath.Join(dir, "stderr"))
	beforeOut, beforeErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	code, runErr := run(args)
	os.Stdout, os.Stderr = beforeOut, beforeErr
	_ = stdout.Close()
	_ = stderr.Close()
	out, _ := os.ReadFile(stdout.Name())
	errText, _ := os.ReadFile(stderr.Name())
	if runErr != nil {
		errText = append(errText, []byte("handover: "+runErr.Error())...)
	}
	return code, string(out), string(errText)
}

func b460Chdir(t *testing.T, dir string) {
	t.Helper()
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(here) })
}

// b460Handovers lists the (verb, reason) of every hand-over row.
func b460Handovers(t *testing.T, home string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
	if err != nil {
		return nil
	}
	out := []string{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatalf("bad hand-over row %s", line)
		}
		out = append(out, row["verb"].(string)+" "+row["reason"].(string))
	}
	return out
}

func b460Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			env = append(env, v)
		}
	}
	cmd.Env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func b460Write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- worker

func TestB460WorkerAnswersInGo(t *testing.T) {
	f := newRunFixture(t)
	f.record(t, "r-live", "active", "")
	f.record(t, "r-done", "ended", "")
	b460Chdir(t, f.workspace)
	cases := []struct {
		args     []string
		code     int
		out, err string
	}{
		{[]string{"worker", "--help"}, 0, workerUsage, ""},
		{[]string{"worker", "-h", "--bogus"}, 0, workerUsage, ""},
		{[]string{"worker"}, 2, "", workerUsage},
		{[]string{"worker", "extra", "--run", "r-live"}, 2, "", workerUsage},
		{[]string{"worker", "--bogus"}, 2, "", "worker: unknown flag \"--bogus\"\n" + workerUsage},
		{[]string{"worker", "--run"}, 2, "", "worker: --run needs a value\n" + workerUsage},
		{[]string{"worker", "--run", "a", "--run", "b"}, 2, "", "worker: --run given more than once\n" + workerUsage},
		{[]string{"worker", "--once=yes", "--run", "r-live"}, 2, "", "worker: --once takes no value\n" + workerUsage},
		{[]string{"worker", "--run", "r-live", "--poll-ms", "0"}, 2, "", "worker: --poll-ms must be a positive integer\n"},
		{[]string{"worker", "--run", "r-live", "--poll-ms", "1.5"}, 2, "", "worker: --poll-ms must be a positive integer\n"},
		{[]string{"worker", "--run", "Bad_Id"}, 1, "", "worker: run id must match /^[a-z0-9][a-z0-9-]{0,63}$/\n"},
		{[]string{"worker", "--run", "r-none"}, 1, "", "worker: no run record for r-none\n"},
		{[]string{"worker", "--run", "r-done"}, 1, "", "worker: run r-done has ended\n"},
	}
	for _, tc := range cases {
		code, out, errText := b460Call(t, tc.args...)
		if code != tc.code || out != tc.out || errText != tc.err {
			t.Errorf("%v = %d %q %q, want %d %q %q", tc.args, code, out, errText, tc.code, tc.out, tc.err)
		}
	}
	if rows := b460Handovers(t, f.home); len(rows) != 0 {
		t.Fatalf("Go-served worker calls handed over: %v", rows)
	}
}

func TestB460WorkerServingHandsOver(t *testing.T) {
	f := newRunFixture(t)
	f.record(t, "r-live", "active", "")
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "r-junk.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	b460Chdir(t, f.workspace)
	// B489: Go serves the worker now; a workspace outside git is Node's
	// own refusal, answered in Go.
	if code, out, errText := b460Call(t, "worker", "--run", "r-live", "--once"); code != 1 || out != "" || errText != "worker: not inside a git repository\n" {
		t.Fatalf("r-live = %d %q %q", code, out, errText)
	}
	if code, out, _ := b460Call(t, "worker", "--run", "r-junk"); code != 127 || out != "" {
		t.Fatalf("r-junk = %d %q, want the Node hand-over (127)", code, out)
	}
	got := strings.Join(b460Handovers(t, f.home), ",")
	if got != "worker worker-record" {
		t.Fatalf("hand-overs = %s", got)
	}
	if record := f.read(t, "r-live"); record["status"] != "active" {
		t.Fatalf("the hand-over changed the run: %#v", record)
	}
}

// ---- spawn

func TestB460SpawnAnswersInGo(t *testing.T) {
	f := newRunFixture(t)
	b460Write(t, filepath.Join(f.workspace, ".code-forge.yml"), "version: 1\n")
	b460Write(t, filepath.Join(f.workspace, "brief.md"), "do it\n")
	b460Write(t, filepath.Join(f.workspace, "bad.json"), "{nope")
	b460Write(t, filepath.Join(f.workspace, "ok.json"), "{\"type\":\"object\"}")
	b460Chdir(t, f.workspace)
	base := []string{"spawn", "--level", "L1", "--role", "coder", "--brief", "brief.md"}
	with := func(extra ...string) []string { return append(append([]string{}, base...), extra...) }
	cases := []struct {
		args []string
		code int
		out  string
		err  string
	}{
		{[]string{"spawn", "--help"}, 0, spawnUsage, ""},
		{[]string{"spawn"}, 2, "", spawnUsage},
		{[]string{"spawn", "--level", "L1", "--role", "coder"}, 2, "", spawnUsage},
		{with("extra"), 2, "", spawnUsage},
		{with("--print-argv", "--background"), 2, "", spawnUsage},
		{with("--nope"), 2, "", "spawn: unknown flag \"--nope\"\n"},
		{with("--timeout", "0"), 2, "", "spawn: --timeout must be a positive integer\n"},
		{with("--schema", "missing.json"), 2, "", "spawn: --schema cannot be read\n"},
		{with("--schema", "bad.json"), 2, "", "spawn: --schema is not a valid JSON file\n"},
		{with("--run", "../x"), 2, "", "spawn: a run id is one path segment of letters, digits, \".\", \"_\" or \"-\"\n"},
		{[]string{"spawn", "--level", "L9", "--role", "coder", "--brief", "brief.md"}, 2, "", "spawn: level must be one of L0, L1, L2, L3\n"},
		{[]string{"spawn", "--level", "L1", "--role", "boss", "--brief", "brief.md", "--schema", "ok.json"}, 2, "", "spawn: role must be one of coder, reviewer, judge, s2, author, facts, delegate\n"},
	}
	for _, tc := range cases {
		code, out, errText := b460Call(t, tc.args...)
		if code != tc.code || out != tc.out || errText != tc.err {
			t.Errorf("%v = %d %q %q, want %d %q %q", tc.args, code, out, errText, tc.code, tc.out, tc.err)
		}
	}
	if rows := b460Handovers(t, f.home); len(rows) != 0 {
		t.Fatalf("Go-served spawn calls handed over: %v", rows)
	}
}

func TestB460SpawnConfigRefusal(t *testing.T) {
	f := newRunFixture(t)
	b460Write(t, filepath.Join(f.workspace, ".code-forge.yml"), "- a\n- b\n")
	b460Chdir(t, f.workspace)
	code, out, errText := b460Call(t, "spawn", "--level", "L1", "--role", "coder", "--brief", "b.md")
	if code != 2 || out != "" || errText != "spawn: config root must be a mapping, got a list\n" {
		t.Fatalf("bad config = %d %q %q", code, out, errText)
	}
}

func TestB460SpawnSessionHandsOver(t *testing.T) {
	f := newRunFixture(t)
	b460Write(t, filepath.Join(f.workspace, ".code-forge.yml"), "version: 1\n")
	b460Chdir(t, f.workspace)
	for _, args := range [][]string{
		{"spawn", "--level", "L1", "--role", "coder", "--brief", "b.md"},
		{"spawn", "--level", "L9", "--role", "coder", "--brief", "b.md", "--run", "r-x"},
		{"spawn", "--level", "L1", "--role", "reviewer", "--brief", "b.md", "--print-argv"},
	} {
		if code, out, _ := b460Call(t, args...); code != 127 || out != "" {
			t.Fatalf("%v = %d %q, want the Node hand-over (127)", args, code, out)
		}
	}
	got := strings.Join(b460Handovers(t, f.home), ",")
	if got != "spawn spawn-session,spawn spawn-session,spawn spawn-session" {
		t.Fatalf("hand-overs = %s", got)
	}
}

// ---- review

func newB460Repo(t *testing.T) (runFixture, string) {
	f := newRunFixture(t)
	repo := filepath.Join(f.workspace, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	b460Git(t, repo, "init", "-q")
	b460Write(t, filepath.Join(repo, "README.md"), "hi\n")
	b460Git(t, repo, "add", ".")
	b460Git(t, repo, "commit", "-q", "-m", "one")
	real, _ := filepath.EvalSymlinks(repo)
	b460Chdir(t, real)
	return f, real
}

func TestB460ReviewUsageInGo(t *testing.T) {
	f, _ := newB460Repo(t)
	cases := []struct {
		args []string
		code int
		out  string
		err  string
	}{
		{[]string{"review", "--help"}, 0, reviewUsage, ""},
		{[]string{"review", "--json", "--help"}, 0, reviewUsage, ""},
		{[]string{"review", "x"}, 2, "", "review: unexpected argument \"x\"\n" + reviewUsage},
		{[]string{"review", "--nope"}, 2, "", "review: unknown flag \"--nope\"\n" + reviewUsage},
		{[]string{"review", "--files"}, 2, "", "review: --files needs at least one value\n" + reviewUsage},
		{[]string{"review", "--acceptance", "a", "--intent", "b"}, 2, "", "review: give --acceptance or --intent, not both\n" + reviewUsage},
		{[]string{"review", "--intent", "  "}, 2, "", "review: --intent needs text\n" + reviewUsage},
		{[]string{"review", "--max", "0"}, 2, "", "review: --max must be whole seconds, like 900 or 900s\n" + reviewUsage},
		{[]string{"review", "--run", "Bad"}, 2, "", "review: --run must match ^[a-z0-9][a-z0-9-]{0,63}$\n" + reviewUsage},
		{[]string{"review", "--base", "nope"}, 2, "", "review: --base \"nope\" is not a commit of this repository\n"},
		{[]string{"review", "--base", "-x"}, 2, "", "review: --base \"-x\" is not a commit of this repository\n"},
		{[]string{"review", "--files", "../out.txt"}, 2, "", "review: --files \"../out.txt\": the path may not contain a \"..\" segment\n"},
	}
	for _, tc := range cases {
		code, out, errText := b460Call(t, tc.args...)
		if code != tc.code || out != tc.out || errText != tc.err {
			t.Errorf("%v = %d %q %q, want %d %q %q", tc.args, code, out, errText, tc.code, tc.out, tc.err)
		}
	}
	if rows := b460Handovers(t, f.home); len(rows) != 0 {
		t.Fatalf("Go-served review calls handed over: %v", rows)
	}
}

func TestB460ReviewOutsideRepoAndNoCommit(t *testing.T) {
	f := newRunFixture(t)
	plain := filepath.Join(f.workspace, "plain")
	empty := filepath.Join(f.workspace, "empty")
	_ = os.MkdirAll(plain, 0o755)
	_ = os.MkdirAll(empty, 0o755)
	b460Git(t, empty, "init", "-q")
	b460Chdir(t, plain)
	if code, out, errText := b460Call(t, "review"); code != 2 || out != "" || errText != "review: not inside a git repository\n" {
		t.Fatalf("outside a repo = %d %q %q", code, out, errText)
	}
	b460Chdir(t, empty)
	if code, out, errText := b460Call(t, "review"); code != 2 || out != "" || errText != "review: the repository has no commit yet — commit once, or pass --base\n" {
		t.Fatalf("no commit = %d %q %q", code, out, errText)
	}
}

func TestB460ReviewNothingToReview(t *testing.T) {
	f, repo := newB460Repo(t)
	head := b460Git(t, repo, "rev-parse", "HEAD")
	code, out, errText := b460Call(t, "review")
	if code != 0 || out != "nothing to review\n" || errText != "" {
		t.Fatalf("clean tree = %d %q %q", code, out, errText)
	}
	// only skipped files: a secret, a binary, the discovered config, a symlink
	b460Write(t, filepath.Join(repo, ".env.local"), "K=1\n")
	b460Write(t, filepath.Join(repo, "blob.bin"), "a\x00b")
	b460Write(t, filepath.Join(repo, ".code-forge.yml"), "version: 1\n")
	if err := os.Symlink("README.md", filepath.Join(repo, "link.md")); err != nil {
		t.Fatal(err)
	}
	code, out, errText = b460Call(t, "review", "--json")
	wantErr := "review: skipped .code-forge.yml (code-forge config)\n" +
		"review: skipped .env.local (secret-like name, never sent to a reviewer)\n" +
		"review: skipped blob.bin (binary)\n" +
		"review: skipped link.md (symlink)\n"
	wantOut := `{"ok":true,"base":"` + head + `","base_from":"HEAD (uncommitted changes)","run":null,"block":null,"files":[],` +
		`"skipped":[{"file":".code-forge.yml","reason":"code-forge config"},{"file":".env.local","reason":"secret-like name, never sent to a reviewer"},{"file":"blob.bin","reason":"binary"},{"file":"link.md","reason":"symlink"}],` +
		`"totals":{"files":0,"approved":0,"unchanged":0,"with_findings":0,"findings":0,"stopped":0,"unavailable":0},"nothing_to_review":true}` + "\n"
	if code != 0 || out != wantOut || errText != wantErr {
		t.Fatalf("skipped only = %d\n%s\n%s", code, out, errText)
	}
	// a deleted tracked file, through --files with a duplicate
	if err := os.Remove(filepath.Join(repo, "README.md")); err != nil {
		t.Fatal(err)
	}
	code, out, errText = b460Call(t, "review", "--files", "README.md", "README.md", "--base", "HEAD")
	if code != 0 || out != "nothing to review\n" || errText != "review: skipped README.md (deleted)\n" {
		t.Fatalf("deleted = %d %q %q", code, out, errText)
	}
	if rows := b460Handovers(t, f.home); len(rows) != 0 {
		t.Fatalf("nothing-to-review handed over: %v", rows)
	}
}

func TestB460ReviewBaseFromMergeBase(t *testing.T) {
	_, repo := newB460Repo(t)
	fork := b460Git(t, repo, "rev-parse", "HEAD")
	b460Git(t, repo, "checkout", "-q", "-b", "topic")
	b460Write(t, filepath.Join(repo, "key.pem"), "x")
	b460Git(t, repo, "add", ".")
	b460Git(t, repo, "commit", "-q", "-m", "two")
	code, out, errText := b460Call(t, "review", "--json")
	if code != 0 || !strings.Contains(out, `"base":"`+fork+`","base_from":"merge-base HEAD main"`) || errText != "review: skipped key.pem (secret-like name, never sent to a reviewer)\n" {
		t.Fatalf("topic branch = %d %q %q", code, out, errText)
	}
}

func TestB460ReviewWithFilesHandsOver(t *testing.T) {
	f, repo := newB460Repo(t)
	b460Write(t, filepath.Join(repo, "app.go"), "package app\n")
	if code, out, errText := b460Call(t, "review", "--json"); code != 127 || out != "" || strings.Contains(errText, "skipped") {
		t.Fatalf("a file to review = %d %q %q, want the Node hand-over (127) with nothing written first", code, out, errText)
	}
	if got := strings.Join(b460Handovers(t, f.home), ","); got != "review review-flow" {
		t.Fatalf("hand-overs = %s", got)
	}
}
