package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ticket"
	"github.com/ricardov03/code-forge/native/worker"
)

// B534: the commit watch (review.file_loop: watch) runs in the Go worker.
// The B489 fixture commits a.txt after the block's base; the watch cursor
// already saw that HEAD long ago (seen_at 0), so the first tick queues it.

const b534Cfg = `{"levels":{"L2":{"model":"claude-x","provider":"anthropic"}},"native":{"daemon":false},"review":{"file_loop":"watch"},"version":1}`

func b534Fix(t *testing.T, kinds string) (*b489Fix, string, string) {
	t.Helper()
	f := newB489Fix(t)
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = f.root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	base := git("rev-parse", "HEAD")
	git("commit", "-q", "-m", "a", "a.txt")
	head := git("rev-parse", "HEAD")
	h := b489Sha(b534Cfg)
	f.record(t, "r1", "active", `,"blocks":{"B1":{"status":"open","base_sha":"`+base+`","owned_files":["a.txt"]`+kinds+`}},"config":{"hash":"`+h+`","snapshots":{"`+h+`":`+b534Cfg+`}}`)
	b460Write(t, filepath.Join(worker.QueueDir(f.root), "watch", "B1.json"),
		`{"run":"r1","base":"`+base+`","tree":null,"skipped":[],"pending":{"sha":"`+head+`","seen_at":0},"burst_started_at":0}`+"\n")
	return f, base, head
}

// The worker no longer hands the run to Node: the watch queues the commit's
// owned file and the same --once drain reviews it in Go.
func TestB534WorkerWatchRunsInGo(t *testing.T) {
	f, base, head := b534Fix(t, "")
	code, out, errText := b460Call(t, "worker", "--run", "r1", "--once")
	if code != 0 || out != "" || !strings.HasSuffix(errText, "worker: drained 1 ticket(s)\n") {
		t.Fatalf("worker --once = %d %q %q", code, out, errText)
	}
	if rows := b460Handovers(t, f.home); len(rows) != 0 {
		t.Fatalf("hand-overs %v", rows)
	}
	id := ticket.ReviewID(ticket.Parts{Run: "r1", Block: "B1", File: "a.txt", ContentHash: b489Sha("hello\nworld\n")})
	if !b489Exists(filepath.Join(worker.QueueDir(f.root), id+".done")) || f.results(t, id) != 1 {
		t.Fatalf("the watched commit's ticket %s was not queued and reviewed", id)
	}
	cursor, _ := os.ReadFile(filepath.Join(worker.QueueDir(f.root), "watch", "B1.json"))
	if want := `{"run":"r1","base":"` + base + `","tree":null,"skipped":[],"sha":"` + head + `"}` + "\n"; string(cursor) != want {
		t.Fatalf("cursor %s, want %s", cursor, want)
	}
}

// A watch shape Go does not reproduce (owned_kinds as an array) hands the
// worker to Node with its own reason before the tick queues anything.
func TestB534WorkerWatchShapeHandsOver(t *testing.T) {
	f, _, _ := b534Fix(t, `,"owned_kinds":["a.txt"]`)
	code, out, _ := b460Call(t, "worker", "--run", "r1", "--once")
	if code != 127 || out != "" {
		t.Fatalf("worker = %d %q, want the Node hand-over (127)", code, out)
	}
	if got := strings.Join(b460Handovers(t, f.home), ","); got != "worker worker-watch-shape" {
		t.Fatalf("hand-overs %s", got)
	}
	if p := worker.Pending(f.root); len(p) != 0 {
		t.Fatalf("the hand-over queued %v", p)
	}
}
