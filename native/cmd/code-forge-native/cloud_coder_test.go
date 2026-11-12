package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

type cloudFixture struct {
	home, repo, tree, base, patch string
	git                           func(dir string, args ...string) string
}

// newCloudFixture builds a repo with a block worktree, a run record that owns
// src/owned.txt and src/new.txt, a patch with the given edits, and fake
// submit/fetch/node scripts. Nothing here reaches a network or a model.
func newCloudFixture(t *testing.T, edits map[string]string, fetchBody string, cloud map[string]any) cloudFixture {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
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
	script := func(name, body string) string {
		file := filepath.Join(home, name)
		must(os.WriteFile(file, []byte("#!/bin/sh\n"+body+"\n"), 0700))
		return file
	}
	git(repo, "init", "-qb", "main")
	git(repo, "config", "user.name", "Fixture")
	git(repo, "config", "user.email", "f@example.invalid")
	git(repo, "config", "commit.gpgsign", "false")
	must(os.MkdirAll(filepath.Join(repo, "src"), 0700))
	must(os.WriteFile(filepath.Join(repo, "src/owned.txt"), []byte("old\n"), 0600))
	must(os.WriteFile(filepath.Join(repo, "other.txt"), []byte("other\n"), 0600))
	must(os.WriteFile(filepath.Join(repo, ".gitignore"), []byte(".code-forge/\n"), 0600))
	submit := script("submit.sh", `printf '%s|%s|%s\n' "$1" "$2" "$3" > "$HOME/submit-args"; echo task-42`)
	fetch := script("fetch.sh", `n=$(cat "$HOME/fetches" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$HOME/fetches.tmp"; mv "$HOME/fetches.tmp" "$HOME/fetches"; `+fetchBody)
	cloud["submit"] = []any{submit, "{block}", "{base}", "{brief}"}
	cloud["fetch"] = []any{fetch, "{task}", "{patch_out}"}
	cfg, err := json.Marshal(map[string]any{"version": 1, "coder": map[string]any{"cloud": cloud}})
	must(err)
	must(os.WriteFile(filepath.Join(repo, ".code-forge.yml"), cfg, 0600))
	git(repo, "add", ".")
	git(repo, "commit", "-qm", "base")
	base := git(repo, "rev-parse", "HEAD")
	tree := filepath.Join(repo, ".code-forge/worktrees/B1")
	git(repo, "worktree", "add", "-qb", "block-B1", tree, base)

	// The cloud patch: made in the worktree, saved, then undone.
	for file, text := range edits {
		must(os.WriteFile(filepath.Join(tree, file), []byte(text), 0600))
	}
	git(tree, "add", "-A")
	patch := filepath.Join(home, "cloud.patch")
	must(os.WriteFile(patch, []byte(git(tree, "diff", "--cached")+"\n"), 0600))
	git(tree, "reset", "-q", "--hard", "HEAD")
	must(os.WriteFile(filepath.Join(home, "patch-src"), []byte(patch), 0600))

	// review-file and ledger add coder run as children of this Go binary
	// (B572); here a fake binary records them. No node is ever started.
	self := script("self.sh", `printf '%s\n' "$*" >> "$HOME/self-calls"; if [ "$1" = review-file ]; then echo '{"ticket":"t-'"$2"'","status":"queued"}'; fi`)
	previous := cloudSelf
	cloudSelf = func() (string, error) { return self, nil }
	t.Cleanup(func() { cloudSelf = previous })
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b572-node")

	entry := ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{"src/owned.txt", "src/new.txt"}}, {Key: "base_sha", Value: base}, {Key: "base_branch", Value: "main"}, {Key: "brief", Value: ledger.Object{{Key: "path", Value: "plans/b1.md"}}},
		{Key: "worktree", Value: ledger.Object{{Key: "path", Value: tree}, {Key: "branch", Value: "block-B1"}, {Key: "base_sha", Value: base}}}}
	record := ledger.Object{{Key: "run_id", Value: "r-cloud-unit"}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: repo}, {Key: "status", Value: "active"}, {Key: "blocks", Value: ledger.Object{{Key: "B1", Value: entry}}}, {Key: "orphans", Value: []any{}}}
	must(state.SaveRun(home, record))
	return cloudFixture{home, repo, tree, base, patch, git}
}

// runCloud calls the public route and returns its exit code and parsed stdout.
func runCloud(t *testing.T) (int, ledger.Object) {
	t.Helper()
	route := routes["cloud-coder"]
	if route == nil {
		t.Error("cloud-coder is not a native route")
		return -99, nil
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	before := os.Stdout
	os.Stdout = w
	code := func() int {
		defer func() { os.Stdout = before; _ = w.Close() }()
		return route([]string{"--run", "r-cloud-unit", "--block", "B1"})
	}()
	data, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatal(err)
	}
	out, err := ledger.DecodeObject([]byte(strings.TrimSpace(string(data))))
	if err != nil {
		t.Errorf("stdout is not one JSON object: %q", data)
	}
	return code, out
}

func readLines(t *testing.T, file string) []string {
	t.Helper()
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestCloudCoderOwnedPatchLandsAsOneCommit(t *testing.T) {
	// The first fetch is still pending; the second writes the patch.
	f := newCloudFixture(t, map[string]string{"src/owned.txt": "new\n", "src/new.txt": "added\n"},
		`if [ $n -ge 2 ]; then cp "$(cat "$HOME/patch-src")" "$2"; echo usd=0.42; fi`,
		map[string]any{"timeout_s": 20, "poll_s": 0.05})
	code, out := runCloud(t)
	if code != 0 || out.Get("status") != "landed" {
		t.Fatalf("exit=%d out=%v; want exit 0 status landed", code, out)
	}
	t.Logf("submit to commit: %vms", out.Get("elapsed_ms"))
	if got := f.git(f.tree, "rev-list", "--count", f.base+"..HEAD"); got != "1" {
		t.Errorf("commits since base = %s; want 1", got)
	}
	if got := f.git(f.tree, "log", "-1", "--format=%s"); got != "B1: cloud coder patch" {
		t.Errorf("commit subject = %q", got)
	}
	if got := f.git(f.tree, "show", "--name-only", "--format=", "HEAD"); got != "src/new.txt\nsrc/owned.txt" {
		t.Errorf("commit files = %q; want src/new.txt and src/owned.txt", got)
	}
	if got := f.git(f.tree, "status", "--porcelain"); got != "" {
		t.Errorf("worktree not clean after landing: %q", got)
	}
	if data, _ := os.ReadFile(filepath.Join(f.tree, "src/owned.txt")); string(data) != "new\n" {
		t.Errorf("src/owned.txt = %q; want the patched text", data)
	}
	if got := readLines(t, filepath.Join(f.home, "submit-args")); len(got) != 1 || got[0] != "B1|"+f.base+"|"+filepath.Join(f.repo, "plans/b1.md") {
		t.Errorf("submit placeholders = %q", got)
	}
	if got := readLines(t, filepath.Join(f.home, "fetches")); len(got) != 1 || got[0] != "2" {
		t.Errorf("fetch calls = %q; want 2 (one pending poll, then the patch)", got)
	}
	want := []string{
		"review-file src/new.txt --block B1 --run r-cloud-unit",
		"review-file src/owned.txt --block B1 --run r-cloud-unit",
		"ledger add coder --run r-cloud-unit --block B1 --usd 0.42 --note cloud coder task task-42",
	}
	if got := readLines(t, filepath.Join(f.home, "self-calls")); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("self calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	reviews, _ := out.Get("reviews").([]any)
	if len(reviews) != 2 {
		t.Fatalf("reviews in output = %d; want 2", len(reviews))
	}
	if first, _ := reviews[0].(ledger.Object); first.Get("ticket") != "t-src/new.txt" {
		t.Errorf("first review = %v; want ticket t-src/new.txt", first)
	}
}

func TestCloudCoderRefusesUnownedFile(t *testing.T) {
	f := newCloudFixture(t, map[string]string{"src/owned.txt": "new\n", "other.txt": "changed\n"},
		`cp "$(cat "$HOME/patch-src")" "$2"`,
		map[string]any{"timeout_s": 20, "poll_s": 0.05})
	code, out := runCloud(t)
	if code != 1 || out.Get("status") != "refused" || out.Get("reason") != "unowned-file" {
		t.Fatalf("exit=%d out=%v; want exit 1 refused unowned-file", code, out)
	}
	if unowned, _ := out.Get("unowned").([]any); len(unowned) != 1 || unowned[0] != "other.txt" {
		t.Errorf("unowned = %v; want [other.txt]", out.Get("unowned"))
	}
	if got := f.git(f.tree, "rev-parse", "HEAD"); got != f.base {
		t.Errorf("HEAD moved to %s; want base %s", got, f.base)
	}
	if got := f.git(f.tree, "status", "--porcelain"); got != "" {
		t.Errorf("worktree changed: %q", got)
	}
	if data, _ := os.ReadFile(filepath.Join(f.tree, "src/owned.txt")); string(data) != "old\n" {
		t.Errorf("src/owned.txt = %q; want it untouched", data)
	}
	if got := readLines(t, filepath.Join(f.home, "self-calls")); len(got) != 0 {
		t.Errorf("self calls = %q; want none", got)
	}
}

// The deadline here expires once the fetch script has run twice, not after 2 s
// of wall time, so a loaded machine cannot starve the polls (B412). A 2-minute
// ceiling keeps a broken loop from hanging the test.
func TestCloudCoderFetchTimeoutExitsOne(t *testing.T) {
	f := newCloudFixture(t, map[string]string{"src/owned.txt": "new\n"}, `exit 0`,
		map[string]any{"timeout_s": 2, "poll_s": 0.05})
	fetches := filepath.Join(f.home, "fetches")
	asked := []time.Duration{}
	before := cloudDeadline
	t.Cleanup(func() { cloudDeadline = before })
	cloudDeadline = func(timeout time.Duration) (context.Context, context.CancelFunc) {
		asked = append(asked, timeout)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		go func() {
			for ctx.Err() == nil {
				data, _ := os.ReadFile(fetches)
				if n, _ := strconv.Atoi(strings.TrimSpace(string(data))); n >= 2 {
					cancel()
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		return ctx, cancel
	}
	code, out := runCloud(t)
	if len(asked) != 1 || asked[0] != 2*time.Second {
		t.Errorf("deadlines asked = %v; want exactly one of 2s (timeout_s)", asked)
	}
	if code != 1 || out.Get("status") != "timeout" || out.Get("reason") != "fetch-timeout" {
		t.Fatalf("exit=%d out=%v; want exit 1 timeout fetch-timeout", code, out)
	}
	if message, _ := out.Get("message").(string); !strings.Contains(message, "task-42") || !strings.Contains(message, "within 2s") {
		t.Errorf("message = %q; want the task id and the limit", message)
	}
	got := readLines(t, filepath.Join(f.home, "fetches"))
	if n, _ := strconv.Atoi(strings.Join(got, "")); len(got) != 1 || n < 2 {
		t.Errorf("fetch calls = %q; want at least 2 polls", got)
	}
	if got := f.git(f.tree, "rev-parse", "HEAD"); got != f.base {
		t.Errorf("HEAD moved to %s; want base %s", got, f.base)
	}
	if got := readLines(t, filepath.Join(f.home, "self-calls")); len(got) != 0 {
		t.Errorf("self calls = %q; want none", got)
	}
}

// b572NodelessPath is a PATH of the system tool dirs only (git and the fake
// provider scripts' sh tools); the test fails when node is found on it.
func b572NodelessPath(t *testing.T) string {
	t.Helper()
	path := "/usr/bin:/bin"
	t.Setenv("PATH", path)
	if p, err := exec.LookPath("node"); err == nil {
		t.Fatalf("node %s is on the test PATH %s", p, path)
	}
	return path
}

// B572: with no node anywhere (PATH without node, CODE_FORGE_NODE naming no
// file), the cloud coder's review-file and ledger add coder run in the real Go
// binary: each file gets a queued ticket and the coder spend is a signed row.
func TestB572CloudCoderReviewAndSpendRunInGoWithoutNode(t *testing.T) {
	binary := b511BuildNative(t)
	f := newCloudFixture(t, map[string]string{"src/owned.txt": "new\n", "src/new.txt": "added\n"},
		`cp "$(cat "$HOME/patch-src")" "$2"; echo usd=0.42`,
		map[string]any{"timeout_s": 20, "poll_s": 0.05})
	cloudSelf = func() (string, error) { return binary, nil }
	if err := os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "r-cloud-unit.key"), []byte(strings.Repeat("k", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	// A live worker for this run (this test process, a fresh heartbeat), so
	// review-file queues a ticket.
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	pid := strconv.Itoa(os.Getpid())
	workers := filepath.Join(f.repo, ".code-forge", "workers")
	if err := os.MkdirAll(workers, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workers, pid+"-unknown.json"), []byte(`{"pid":`+pid+`,"start_time":"unknown","run":"r-cloud-unit","started_at":"`+stamp+`","heartbeat_at":"`+stamp+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", b572NodelessPath(t))
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b572-node")
	code, out := runCloud(t)
	if code != 0 || out.Get("status") != "landed" || out.Get("spend_recorded") != true {
		t.Fatalf("exit=%d out=%v; want exit 0, landed, spend recorded", code, out)
	}
	reviews, _ := out.Get("reviews").([]any)
	if len(reviews) != 2 {
		t.Fatalf("reviews = %v; want 2", reviews)
	}
	for _, r := range reviews {
		row, _ := r.(ledger.Object)
		ticket, _ := row.Get("ticket").(string)
		if row.Get("exit") != float64(0) || !reviewWaitTicket.MatchString(ticket) {
			t.Errorf("review row %v; want exit 0 and a queued ticket", row)
		}
	}
	var data []byte
	_ = filepath.WalkDir(filepath.Join(f.home, ".code-forge"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			text, _ := os.ReadFile(p)
			data = append(data, text...)
		}
		return nil
	})
	for _, want := range []string{`"role":"coder"`, `"block":"B1"`, `"usd":0.42`, `"note":"cloud coder task task-42"`, `"mac":"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("ledger rows hold no signed coder spend row with %s:\n%s", want, data)
		}
	}
}
