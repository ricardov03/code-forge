package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
)

// b452EnqueueFixture is a repository with a live worker for run r-test and a
// private run record whose block B1 carries the given worktree JSON.
func b452EnqueueFixture(t *testing.T, worktree func(repo string) string) (repo, home string) {
	t.Helper()
	home = t.TempDir()
	repo = reviewFileRepo(t, home)
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	pid := strconv.Itoa(os.Getpid())
	files := map[string]string{
		filepath.Join(repo, ".code-forge", "workers", pid+"-unknown.json"): `{"pid":` + pid + `,"start_time":"unknown","run":"r-test","started_at":"` + stamp + `","heartbeat_at":"` + stamp + `"}`,
		filepath.Join(repo, "a.go"):                                        "package a\n",
	}
	if worktree != nil {
		files[filepath.Join(home, ".code-forge", "runs", "r-test.json")] = `{"run_id":"r-test","project":"p","workspace":` + strconv.Quote(repo) + `,"blocks":{"B1":{"owned_files":["*.go"],"worktree":` + worktree(repo) + `}}}`
	}
	for name, body := range files {
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return repo, home
}

func b452Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	git := exec.Command("git", append([]string{"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
	git.Dir, git.Env = dir, gitx.ChildEnv()
	out, err := git.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func b452Tree(path, branch, base string) string {
	return `{"path":` + strconv.Quote(path) + `,"branch":` + strconv.Quote(branch) + `,"base_sha":` + strconv.Quote(base) + `}`
}

// Former hand-over enqueue-worktree: Node's blockTree refusals now come from Go.
func TestB452ReviewFileEnqueueWorktreeRefusals(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	var other string
	for _, tc := range []struct {
		name, reason string
		tree         func(t *testing.T, repo string) string
		message      func(repo string) string
	}{
		{"malformed", "worktree-malformed",
			func(t *testing.T, repo string) string { return b452Tree("relative/path", "b1", sha) },
			func(string) string {
				return "malformed worktree record (it needs an absolute path, a branch and a base_sha)"
			}},
		{"missing", "worktree-missing",
			func(t *testing.T, repo string) string { return b452Tree(repo+"-gone", "b1", sha) },
			func(repo string) string { return "the worktree " + repo + "-gone is gone (removed or moved)" }},
		{"not-dir", "worktree-not-dir",
			func(t *testing.T, repo string) string { return b452Tree(filepath.Join(repo, "a.go"), "b1", sha) },
			func(repo string) string { return "the worktree " + filepath.Join(repo, "a.go") + " is not a directory" }},
		{"branch-mismatch", "worktree-branch-mismatch",
			func(t *testing.T, repo string) string {
				other = b452Git(t, repo, "symbolic-ref", "--short", "HEAD")
				return b452Tree(repo, "b1", sha)
			},
			func(repo string) string {
				return "the worktree " + repo + " is not on its branch b1 (it is on " + other + ")"
			}},
		{"foreign", "worktree-foreign",
			func(t *testing.T, repo string) string {
				foreign := reviewFileRepo(t, t.TempDir())
				b452Git(t, foreign, "checkout", "-q", "-b", "b1")
				other = foreign
				return b452Tree(foreign, "b1", sha)
			},
			func(string) string { return "the worktree " + other + " belongs to another repository" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var tree string
			repo, home := b452EnqueueFixture(t, func(repo string) string { tree = tc.tree(t, repo); return tree })
			got := reviewFileExec(t, repo, home, false, "a.go", "--block", "B1")
			want := `{"status":"refused","reason":"` + tc.reason + `","message":` + statusQuote("block B1: "+tc.message(repo)+"; nothing was queued") + "}\n"
			if got != (reviewFileRun{want, "", 1}) {
				t.Fatalf("got %+v\nwant stdout %s exit 1", got, want)
			}
			if _, err := os.Stat(filepath.Join(repo, ".code-forge", "queue")); !os.IsNotExist(err) {
				t.Fatalf("a refusal must queue nothing: %v", err)
			}
		})
	}
}

// A block worktree on its branch: the ticket names that worktree as its tree.
func TestB452ReviewFileEnqueueInWorktree(t *testing.T) {
	var wt string
	repo, home := b452EnqueueFixture(t, func(repo string) string {
		wt = repo + "-b1"
		b452Git(t, repo, "worktree", "add", "-q", "-b", "b1", wt)
		return b452Tree(wt, "b1", b452Git(t, repo, "rev-parse", "HEAD"))
	})
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a // in the worktree\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got := reviewFileExec(t, repo, home, false, "a.go", "--block", "B1")
	if got.code != 0 || got.stderr != "" || !strings.Contains(got.stdout, `"status":"queued"`) {
		t.Fatalf("got %+v, want a queued ticket", got)
	}
	queue := reviewFileQueue(t, repo)
	if len(queue) != 1 {
		t.Fatalf("queue = %v, want one ticket", queue)
	}
	for _, body := range queue {
		if !strings.Contains(body, `"tree":`+strconv.Quote(wt)+"}") {
			t.Fatalf("ticket %s does not name the worktree %s", body, wt)
		}
	}
}

// Former hand-over enqueue-run-read: a private run record that is not JSON is
// Node's run-record-unreadable refusal.
func TestB452ReviewFileEnqueueRunRecordUnreadable(t *testing.T) {
	repo, home := b452EnqueueFixture(t, nil)
	dir := filepath.Join(home, ".code-forge", "runs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "r-test.json"), []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	got := reviewFileExec(t, repo, home, false, "a.go", "--block", "B1")
	const want = `{"status":"refused","reason":"run-record-unreadable","message":"the run record of r-test cannot be read (corrupt or not a record); nothing was queued"}` + "\n"
	if got != (reviewFileRun{want, "", 1}) {
		t.Fatalf("got %+v, want run-record-unreadable exit 1", got)
	}
}
