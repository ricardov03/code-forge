package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B727: reviewWaitJSON reads a lone surrogate escape as JSON.parse does
// (WTF-8). Every expected output below was recorded from Node on the same
// fixture (CODE_FORGE_NATIVE=0 ./bin/code-forge review-file ..., and
// src/worker/queue.mjs workerRegistry/liveWorker), never written by hand.

const b727Ticket = "abcdef0123456789abcdef01"

// b727WaitResultNode is Node's review-file --wait for b727WaitFixture.
const b727WaitResultNode = `{"ticket":"abcdef0123456789abcdef01","status":"done","result":{"status":"reviewed","approved":false,"queued_at":"q\udc00","note":"😀","findings":[{"id":"F\ud800","severity":"major","line_start":"1\udbff","line_end":2,"claim":"c\udc00","fix":"x\ud83d"}]},"queued_at":"q\udc00","fix_list":[{"id":"F\ud800","severity":"major","lines":"1\udbff-2","claim":"c\udc00","fix":"x\ud83d"}]}` + "\n"

// b727WaitFixture writes a done marker whose result path holds a lone
// surrogate (Node's fs opens the U+FFFD form of it) and a result with lone
// surrogates and an escaped pair.
func b727WaitFixture(t *testing.T, repo string) {
	t.Helper()
	b727Write(t, filepath.Join(repo, ".code-forge", "queue", b727Ticket+".done"), `{"status":"done","result":".code-forge/reviews/r\ud800.json"}`)
	b727Write(t, filepath.Join(repo, ".code-forge", "reviews", "r�.json"), `{"status":"reviewed","approved":false,"queued_at":"q\udc00","note":"😀","findings":[{"id":"F\ud800","severity":"major","line_start":"1\udbff","line_end":2,"claim":"c\udc00","fix":"x\ud83d"}]}`)
}

func b727Write(t *testing.T, file, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// b727LiveWorker registers this test process as a live worker (fresh
// heartbeat) with extra fields; run and the extras may hold escapes.
func b727LiveWorker(t *testing.T, repo, run, extra string) (int, string) {
	t.Helper()
	pid := os.Getpid()
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	b727Write(t, filepath.Join(repo, ".code-forge", "workers", strconv.Itoa(pid)+"-unknown.json"),
		`{"pid":`+strconv.Itoa(pid)+`,"start_time":"unknown","run":"`+run+`","started_at":"`+now+`","heartbeat_at":"`+now+`"`+extra+`}`)
	return pid, now
}

func TestB727WaitResultLoneSurrogateLikeNode(t *testing.T) {
	home := t.TempDir()
	repo := reviewFileRepo(t, home)
	b727WaitFixture(t, repo)
	got := reviewFileExec(t, repo, home, false, "--wait", b727Ticket)
	if want := (reviewFileRun{b727WaitResultNode, "", 0}); got != want {
		t.Fatalf("review-file --wait:\n got %+v\nwant %+v (Node)", got, want)
	}
}

func TestB727EnqueueCacheReadsLoneSurrogateResult(t *testing.T) {
	home := t.TempDir()
	repo := reviewFileRepo(t, home)
	b727WaitFixture(t, repo)
	// Node's doneResult reads this marker's result as status "reviewed"
	// (b727WaitResultNode): a cached review, not a rerun or a split.
	if !reviewEnqueueCached(repo, b727Ticket) {
		t.Fatal("reviewEnqueueCached: want the reviewed result cached")
	}
	if !reviewEnqueueCacheSupported(repo, b727Ticket, nil) {
		t.Fatal("reviewEnqueueCacheSupported: want Go to judge the cached result")
	}
}

func TestB727WorkerExtraLoneSurrogateLikeNode(t *testing.T) {
	home := t.TempDir()
	repo := reviewFileRepo(t, home)
	b727LiveWorker(t, repo, "r1", `,"note":"\ud800"`)
	got := reviewFileExec(t, repo, home, false, "--wait", b727Ticket, "--max=0ms")
	if want := (reviewFileRun{`{"ticket":"abcdef0123456789abcdef01","status":"pending","reason":"wait_timeout"}` + "\n", "", 0}); got != want {
		t.Fatalf("--wait:\n got %+v\nwant %+v (Node)", got, want)
	}
	got = reviewFileExec(t, repo, home, false, "--rebase-check", "--block", "B1", "--run", "r2")
	if want := (reviewFileRun{`{"status":"worker_down","block":"B1"}` + "\n", "", 3}); got != want {
		t.Fatalf("--rebase-check:\n got %+v\nwant %+v (Node)", got, want)
	}
}

func TestB727UnshapedLoneSurrogateEntryLikeNode(t *testing.T) {
	home := t.TempDir()
	repo := reviewFileRepo(t, home)
	b727Write(t, filepath.Join(repo, ".code-forge", "workers", "bad.json"), `{"note":"\ud800"}`)
	got := reviewFileExec(t, repo, home, false, "--wait", b727Ticket, "--max=0ms")
	if want := (reviewFileRun{`{"ticket":"abcdef0123456789abcdef01","status":"worker_down"}` + "\n", "", 3}); got != want {
		t.Fatalf("--wait:\n got %+v\nwant %+v (Node)", got, want)
	}
	got = reviewFileExec(t, repo, home, false, "--rebase-check", "--block", "B1", "--run", "r1")
	if want := (reviewFileRun{`{"status":"worker_down","block":"B1"}` + "\n", "", 3}); got != want {
		t.Fatalf("--rebase-check:\n got %+v\nwant %+v (Node)", got, want)
	}
	// Node: workerRegistry -> [{"file":"bad.json","worker":null,"live":true}], liveWorker -> null.
	entries, ok := runWorkerRegistry(repo, false)
	if !ok || len(entries) != 1 || filepath.Base(entries[0].file) != "bad.json" || entries[0].worker != nil || !entries[0].live {
		t.Fatalf("runWorkerRegistry = %+v, %v; want bad.json, no worker, live (Node)", entries, ok)
	}
	if w, ok := runLiveWorker(repo, 0, false); !ok || w != nil {
		t.Fatalf("runLiveWorker = %v, %v; want nil, judged (Node: null)", w, ok)
	}
}

func TestB727RunStartRegistryLoneSurrogateLikeNode(t *testing.T) {
	home := t.TempDir()
	repo := reviewFileRepo(t, home)
	pid, now := b727LiveWorker(t, repo, "r1", `,"note":"\ud800","k\udc00":["x\udbff"]`)
	// Node liveWorker(repo) and liveWorker(repo, {pid, registeredOnly: true}),
	// JSON.stringify, with the pid and time written as PID and NOW.
	const node = `{"pid":PID,"start_time":"unknown","run":"r1","started_at":"NOW","heartbeat_at":"NOW","note":"\ud800","k\udc00":["x\udbff"]}`
	want := strings.NewReplacer("PID", strconv.Itoa(pid), "NOW", now).Replace(node)
	for _, c := range []struct {
		pid        float64
		registered bool
	}{{0, false}, {float64(pid), true}} {
		w, ok := runLiveWorker(repo, c.pid, c.registered)
		if !ok || w == nil {
			t.Fatalf("runLiveWorker(%v, %v) = %v, %v; want the worker (Node)", c.pid, c.registered, w, ok)
		}
		data, err := ledger.Stringify(w, false)
		if err != nil || string(data) != want {
			t.Fatalf("runLiveWorker(%v, %v):\n got %s (%v)\nwant %s (Node)", c.pid, c.registered, data, err, want)
		}
	}
}

func TestB727UntilDoneGuardReadsWorkerWithLoneSurrogate(t *testing.T) {
	home := t.TempDir()
	repo := reviewFileRepo(t, home)
	b727LiveWorker(t, repo, "r1", `,"note":"\ud800"`)
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(here) })
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	// Node's review-file with no --run uses this worker's run (liveWorker: "run":"r1").
	if got := hudRunIDs([]string{"review-file", "a.go", "--block", "B1"}); !reflect.DeepEqual(got, []string{"r1"}) {
		t.Fatalf("hudRunIDs = %q, want [r1]", got)
	}
}

// Not served in Go: a worker run with a lone surrogate reaches stderr text,
// and a run record's values reach paths, git argv and ledger names. These
// cases handed over to Node before B690a; now Go refuses them (B756).
func TestB727KeptCasesStillHandOver(t *testing.T) {
	t.Run("worker run", func(t *testing.T) {
		home := t.TempDir()
		repo := reviewFileRepo(t, home)
		b727LiveWorker(t, repo, `r\ud800`, "")
		if got := reviewFileExec(t, repo, home, false, "--rebase-check", "--block", "B1", "--run", "r2"); got.code != 1 || got.stdout != "" || !b690aIsRefusal(got.stderr) {
			t.Fatalf("got %+v, want the Go refusal", got)
		}
	})
	t.Run("run record", func(t *testing.T) {
		home := t.TempDir()
		repo := reviewFileRepo(t, home)
		b727Write(t, filepath.Join(repo, ".code-forge", "runs", "r1.json"), `{"run_id":"r1","project":"p\ud800","blocks":{"B1":{"owned_files":["a.go"]}}}`)
		if got := reviewFileExec(t, repo, home, false, "--wait-block", "B1", "--run", "r1", "--max=0ms"); got.code != 1 || got.stdout != "" || !b690aIsRefusal(got.stderr) {
			t.Fatalf("got %+v, want the Go refusal", got)
		}
	})
}
