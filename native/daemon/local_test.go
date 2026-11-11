//go:build unix

package daemon

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// joinFixture is a git repo with one owned changed file and the run record at
// the path the CLI hands Join as Scope.
func joinFixture(t *testing.T) JoinOptions {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base"}} {
		if _, err := git(repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	base, err := git(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	scope := filepath.Join(repo, ".code-forge", "runs", "r-test.json")
	writeObject(t, scope, object{
		"run_id": "r-test", "project": "test",
		"blocks": object{"B1": object{"owned_files": []any{"*.go"}, "base_sha": strings.TrimSpace(base)}},
	})
	if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return JoinOptions{
		Home: t.TempDir(), Repo: repo, Scope: scope, Run: "r-test", Block: "B1",
		Deadline: time.Now().Add(-time.Second),
		Live:     func() (bool, bool) { return true, true },
		Sleep:    func(time.Duration) bool { return true },
	}
}

// queueFiles lists every file under the queue with its content.
func queueFiles(t *testing.T, repo string) map[string]string {
	t.Helper()
	files := map[string]string{}
	root := filepath.Join(repo, ".code-forge", "queue")
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			data, _ := os.ReadFile(path)
			files[strings.TrimPrefix(path, root)] = string(data)
		}
		return nil
	})
	return files
}

func ticketNames(files map[string]string) []string {
	names := []string{}
	for name := range files {
		if strings.HasSuffix(name, ".json") && !strings.HasSuffix(name, ".done") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

func TestJoinEnqueuesMissingTicketAndTimesOut(t *testing.T) {
	o := joinFixture(t)
	answer, code := Join(o)
	if code != "" {
		t.Fatalf("join failed: %s", code)
	}
	files, _ := answer["files"].([]any)
	if answer["reason"] != "wait_timeout" || answer["done"] != false || len(files) != 1 {
		t.Fatalf("answer = %v, want one pending file and wait_timeout", answer)
	}
	state, _ := files[0].(map[string]any)
	if state["file"] != "a.go" || state["status"] != "pending" || state["ticket"] == nil {
		t.Fatalf("file state = %v, want a.go pending with a ticket", state)
	}
	if names := ticketNames(queueFiles(t, o.Repo)); len(names) != 1 || !strings.Contains(names[0], state["ticket"].(string)) {
		t.Fatalf("queue tickets = %v, want the one answered ticket %v", names, state["ticket"])
	}
}

func TestJoinWorkerDownAfterTwoPolls(t *testing.T) {
	o := joinFixture(t)
	o.Live = func() (bool, bool) { return false, true }
	sleeps := 0
	o.Sleep = func(time.Duration) bool { sleeps++; return true }
	answer, code := Join(o)
	if code != "" || answer["status"] != "worker_down" || answer["done"] != false {
		t.Fatalf("answer = %v code %q, want worker_down", answer, code)
	}
	if sleeps != 1 {
		t.Fatalf("polls slept %d times, want 1 (worker_down on the second poll)", sleeps)
	}
	if names := ticketNames(queueFiles(t, o.Repo)); len(names) != 0 {
		t.Fatalf("a down worker must not enqueue: %v", names)
	}
}

func TestJoinScopeUnreadable(t *testing.T) {
	o := joinFixture(t)
	if err := os.Remove(o.Scope); err != nil {
		t.Fatal(err)
	}
	if answer, code := Join(o); code != "block-scope-unreadable" || answer != nil {
		t.Fatalf("Join = %v, %q; want block-scope-unreadable", answer, code)
	}
	o = joinFixture(t)
	writeObject(t, o.Scope, object{"run_id": "r-other", "project": "test", "blocks": object{}})
	if _, code := Join(o); code != "block-scope-unreadable" {
		t.Fatalf("mismatched run: code %q, want block-scope-unreadable", code)
	}
}

func TestJoinInterruptedSleep(t *testing.T) {
	o := joinFixture(t)
	o.Deadline = time.Now().Add(time.Hour)
	if err := os.WriteFile(filepath.Join(o.Repo, "a.go"), []byte("package fixture // pending\n"), 0600); err != nil {
		t.Fatal(err)
	}
	o.Sleep = func(time.Duration) bool { return false }
	if _, code := Join(o); code != "interrupted" {
		t.Fatalf("code %q, want interrupted", code)
	}
}

// stoppedFixture enqueues a.go, marks its ticket stopped with the given
// result findings, then changes a.go so the join would restart it.
func stoppedFixture(t *testing.T, findings []any) JoinOptions {
	t.Helper()
	o := joinFixture(t)
	answer, code := Join(o)
	if code != "" {
		t.Fatalf("seed join: %s", code)
	}
	id := answer["files"].([]any)[0].(map[string]any)["ticket"].(string)
	rel := filepath.Join(".code-forge", "reviews", "stopped.json")
	writeObject(t, filepath.Join(o.Repo, rel), object{"status": "stopped", "findings": findings})
	writeObject(t, filepath.Join(o.Repo, ".code-forge", "queue", id+".done"), object{"ticket": id, "status": "stopped", "result": rel})
	if err := os.WriteFile(filepath.Join(o.Repo, "a.go"), []byte("package fixture // changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestJoinStoppedReviewWithFindingsHandsOverBeforeWrites(t *testing.T) {
	o := stoppedFixture(t, []any{object{"id": "F1"}})
	before := queueFiles(t, o.Repo)
	answer, code := Join(o)
	if code != "stopped" || answer != nil {
		t.Fatalf("Join = %v, %q; want the stopped handover", answer, code)
	}
	after := queueFiles(t, o.Repo)
	if len(after) != len(before) {
		t.Fatalf("queue changed before the handover: %d files, was %d", len(after), len(before))
	}
	for name, content := range before {
		if after[name] != content {
			t.Fatalf("queue file %s changed before the handover", name)
		}
	}
}

func TestJoinStoppedReviewWithoutFindingsStaysLocal(t *testing.T) {
	o := stoppedFixture(t, []any{})
	answer, code := Join(o)
	if code != "" {
		t.Fatalf("a stopped review without findings is no requeue candidate: code %q", code)
	}
	state := answer["files"].([]any)[0].(map[string]any)
	if answer["done"] != true || state["status"] != "done" || state["outcome"] != "stopped" {
		t.Fatalf("answer = %v, want done with outcome stopped", answer)
	}
}

func TestJoinStoppedReviewWorkerDownStaysLocal(t *testing.T) {
	o := stoppedFixture(t, []any{object{"id": "F1"}})
	o.Live = func() (bool, bool) { return false, true }
	if answer, code := Join(o); code != "" || answer["status"] != "worker_down" {
		t.Fatalf("Join = %v, %q; Node requeues stopped files only for a live worker", answer, code)
	}
}

func TestJoinWorkerUnsupported(t *testing.T) {
	o := joinFixture(t)
	o.Live = func() (bool, bool) { return false, false }
	if _, code := Join(o); code != "worker-unsupported" {
		t.Fatalf("code %q, want worker-unsupported", code)
	}
}

func TestLocalJoinCountsAutomaticReopen(t *testing.T) {
	row := object{"event": "review.reopened", "run": "r-test", "block": "B1", "file": "a.go", "by": "auto", "reason": "auto-requeue: content changed after stopped review", "from": "stopped", "from_hash": "h1", "to_hash": "h2", "ts": "2100-01-01T00:00:00Z"}
	local := &server{run: "r-test", local: true, rows: []any{row}}
	if got := local.reopened("B1", "a.go"); got != "2100-01-01T00:00:00Z" {
		t.Fatalf("local join reopened = %q, want the automatic reopen", got)
	}
	daemon := &server{run: "r-test", rows: []any{row}}
	if got := daemon.reopened("B1", "a.go"); got != "" {
		t.Fatalf("daemon reopened = %q; the daemon keeps its row filter", got)
	}
	same := object{}
	for k, v := range row {
		same[k] = v
	}
	same["to_hash"] = "h1"
	local.rows = []any{same}
	if got := local.reopened("B1", "a.go"); got != "" {
		t.Fatalf("equal hashes are no automatic reopen: %q", got)
	}
}
