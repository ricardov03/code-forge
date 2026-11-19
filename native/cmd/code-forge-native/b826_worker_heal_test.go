package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/ricardov03/code-forge/native/state"
)

// B826, the customer's case (Laravel 13 + Vue + Pest, macOS, 4 coders,
// 0.25.1): the worker died, `.code-forge/queue/worker.json` and the
// `.code-forge/workers/` entry were gone, `review-file` answered worker_down
// with no ticket and `run start --reattach` failed with "no live worker serves
// this queue". Now review-file (enqueue, --wait, --wait-block) and --reattach
// start the run's worker themselves, as `run start --add-worker` does, and
// the run keeps one pin per worker (B905).

func b826JSON(t *testing.T, out string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("not one JSON line: %q", out)
	}
	return doc
}

func TestB826CustomerDeadWorkerReviewFileStartsIt(t *testing.T) {
	f, run := b826Fixture(t, b884Config)
	startedLine := regexp.MustCompile(`(?m)^review-file: no live worker for run ` + regexp.QuoteMeta(run) + `; started worker pid (\d+)$`)

	// Enqueue: a queued ticket and a live worker.
	b826KillWorker(t, f, run)
	code, out, errText := f.sh(t, "code-forge review-file a.txt --block B1 --run "+run)
	doc := b826JSON(t, out)
	if code != 0 || doc["status"] != "queued" || doc["ticket"] == nil || doc["file"] != "a.txt" {
		t.Fatalf("review-file = %d %q %q", code, out, errText)
	}
	m := startedLine.FindStringSubmatch(errText)
	if m == nil {
		t.Fatalf("stderr %q", errText)
	}
	if pid := b826OneLiveWorker(t, f, run, "after review-file"); fmt.Sprint(pid) != m[1] {
		t.Fatalf("started pid %s, live %d", m[1], pid)
	}
	code, out, errText = f.sh(t, "code-forge review-file --wait "+doc["ticket"].(string)+" --max 60s")
	if waited := b826JSON(t, out); code != 0 || waited["status"] != "done" {
		t.Fatalf("--wait = %d %q %q", code, out, errText)
	}

	// --wait-block: the join starts the worker and serves the block.
	f.must(t, "code-forge review-file b.txt --block B1 --run "+run)
	b826KillWorker(t, f, run)
	code, out, errText = f.sh(t, "code-forge review-file --wait-block B1 --run "+run+" --max 60s")
	if joined := b826JSON(t, out); code != 0 || joined["status"] == "worker_down" || !startedLine.MatchString(errText) {
		t.Fatalf("--wait-block = %d %q %q", code, out, errText)
	}
	b826OneLiveWorker(t, f, run, "after --wait-block")

	// --wait on a queued ticket whose worker died: the worker is stopped
	// (alive, serving nothing) while c.txt is queued, then killed.
	for _, pid := range b884Workers(run) {
		_ = syscall.Kill(pid, syscall.SIGSTOP)
	}
	f.must(t, "echo three >> c.txt")
	queued := b826JSON(t, f.must(t, "code-forge review-file c.txt --block B1 --run "+run))
	if queued["status"] != "queued" {
		t.Fatalf("c.txt %v", queued)
	}
	b826KillWorker(t, f, run)
	code, out, errText = f.sh(t, "code-forge review-file --wait "+queued["ticket"].(string)+" --max 60s")
	if waited := b826JSON(t, out); code != 0 || waited["status"] != "done" || !startedLine.MatchString(errText) {
		t.Fatalf("--wait after a dead worker = %d %q %q", code, out, errText)
	}
	b826OneLiveWorker(t, f, run, "after --wait")

	// An ended run answers worker_down and starts nothing.
	// (run end refuses open blocks; the record is ended as run end leaves it)
	rec, err := state.ReadRun(f.home, run)
	if err != nil {
		t.Fatal(err)
	}
	rec.Set("status", "ended")
	if err := state.SaveRun(f.home, rec); err != nil {
		t.Fatal(err)
	}
	b826KillWorker(t, f, run)
	code, out, errText = f.sh(t, "code-forge review-file d.txt --block B1 --run "+run)
	doc = b826JSON(t, out)
	if code != 3 || doc["status"] != "worker_down" || doc["fix"] != nil || strings.Contains(errText, "started worker") {
		t.Fatalf("ended run review-file = %d %q %q", code, out, errText)
	}
	if live := b884Workers(run); len(live) != 0 {
		t.Fatalf("a worker started for an ended run: %v", live)
	}
}

// A start that fails (the queue directory cannot be written) keeps
// worker_down, exit 3, with the fix on stdout and stderr and no worker left.
func TestB826LaunchFailureKeepsWorkerDownWithFix(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only directory")
	}
	f, run := b826Fixture(t, b884Config)
	b826KillWorker(t, f, run)
	queue := filepath.Join(f.repo, ".code-forge", "queue")
	if err := os.Chmod(queue, 0o500); err != nil {
		t.Fatal(err)
	}
	code, out, errText := f.sh(t, "code-forge review-file a.txt --block B1 --run "+run)
	doc := b826JSON(t, out)
	fix := "code-forge run start --add-worker --run " + run
	if code != 3 || doc["status"] != "worker_down" || doc["fix"] != fix || doc["start_error"] == nil || doc["ticket"] != nil ||
		!strings.Contains(errText, "review-file: no live worker for run "+run+"; could not start one (") || !strings.Contains(errText, "; fix: "+fix+"\n") {
		t.Fatalf("review-file = %d %q %q", code, out, errText)
	}
	if live := b884Workers(run); len(live) != 0 {
		t.Fatalf("workers left: %v", live)
	}
}

// Four coders call review-file at once after the worker died: one worker
// starts (never one per coder, even with review.workers: 4), and every coder
// gets a queued ticket.
func TestB826FourCodersStartOneWorker(t *testing.T) {
	cfg := strings.Replace(b884Config, "review:\n  multimodel: false\n", "review:\n  multimodel: false\n  workers: 4\n", 1)
	if cfg == b884Config {
		t.Fatal("config not changed")
	}
	f, run := b826Fixture(t, cfg)
	b826KillWorker(t, f, run)
	var wg sync.WaitGroup
	results := make([]string, 4)
	for i, name := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			code, out, errText := f.sh(t, "code-forge review-file "+name+".txt --block B1 --run "+run)
			results[i] = fmt.Sprintf("%d %s %s", code, strings.TrimSpace(out), errText)
		}(i, name)
	}
	wg.Wait()
	started := 0
	for _, r := range results {
		if !strings.HasPrefix(r, "0 {") || !strings.Contains(r, `"status":"queued"`) {
			t.Fatalf("coder results:\n%s", strings.Join(results, "\n"))
		}
		started += strings.Count(r, "started worker pid")
	}
	if started != 1 {
		t.Fatalf("started %d workers:\n%s", started, strings.Join(results, "\n"))
	}
	b826OneLiveWorker(t, f, run, "after four coders")
}

// --reattach in the customer's state (worker killed, worker.json and the
// registry entry gone) starts the run's worker and pins it as the one worker.
func TestB826ReattachWithNoLiveWorkerStartsOne(t *testing.T) {
	f, run := b826Fixture(t, b884Config)
	b826KillWorker(t, f, run)
	b826KillWorker(t, f, run)
	code, out, errText := f.sh(t, "code-forge run start --reattach --run "+run)
	m := regexp.MustCompile(`^run ` + regexp.QuoteMeta(run) + `: worker re-pinned to pid (\d+)\n$`).FindStringSubmatch(out)
	if code != 0 || m == nil || !strings.Contains(errText, "no live worker served run "+run+"; started worker pid "+m[1]) {
		t.Fatalf("--reattach = %d %q %q", code, out, errText)
	}
	pid := b826OneLiveWorker(t, f, run, "after --reattach")
	if pins := b826Pins(t, f, run); len(pins) != 1 || pins[0] != float64(pid) {
		t.Fatalf("pins after --reattach %v, live %d", pins, pid)
	}

}
