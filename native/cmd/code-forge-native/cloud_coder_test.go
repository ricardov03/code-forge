package main

import (
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
	fetch := script("fetch.sh", `n=$(cat "$HOME/fetches" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$HOME/fetches"; `+fetchBody)
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

	// Node-only verbs (review-file, ledger add) go to this fake node.
	node := script("node.sh", `shift; printf '%s\n' "$*" >> "$HOME/node-calls"; if [ "$1" = review-file ]; then echo '{"ticket":"t-'"$2"'","status":"queued"}'; fi`)
	t.Setenv("CODE_FORGE_NODE", node)

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
	t.Logf("submit to commit: %vms", out.Get("apply_ms"))
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
	if got := readLines(t, filepath.Join(f.home, "node-calls")); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("node calls =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
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
	if got := readLines(t, filepath.Join(f.home, "node-calls")); len(got) != 0 {
		t.Errorf("node calls = %q; want none", got)
	}
}

func TestCloudCoderFetchTimeoutExitsOne(t *testing.T) {
	f := newCloudFixture(t, map[string]string{"src/owned.txt": "new\n"}, `exit 0`,
		map[string]any{"timeout_s": 2, "poll_s": 0.05})
	started := time.Now()
	code, out := runCloud(t)
	if code != 1 || out.Get("status") != "timeout" || out.Get("reason") != "fetch-timeout" {
		t.Fatalf("exit=%d out=%v; want exit 1 timeout fetch-timeout", code, out)
	}
	if message, _ := out.Get("message").(string); !strings.Contains(message, "task-42") || !strings.Contains(message, "within 2s") {
		t.Errorf("message = %q; want the task id and the limit", message)
	}
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Errorf("timeout took %v; want about 2s", elapsed)
	}
	got := readLines(t, filepath.Join(f.home, "fetches"))
	if n, _ := strconv.Atoi(strings.Join(got, "")); len(got) != 1 || n < 2 {
		t.Errorf("fetch calls = %q; want at least 2 polls", got)
	}
	if got := f.git(f.tree, "rev-parse", "HEAD"); got != f.base {
		t.Errorf("HEAD moved to %s; want base %s", got, f.base)
	}
	if got := readLines(t, filepath.Join(f.home, "node-calls")); len(got) != 0 {
		t.Errorf("node calls = %q; want none", got)
	}
}
