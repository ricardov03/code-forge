package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/review"
	"github.com/ricardov03/code-forge/native/session"
	"github.com/ricardov03/code-forge/native/worker"
)

// B561: a Go worker that met a ticket it does not serve used to replace
// itself (same pid) with the Node worker. B690a: Go never starts Node; the
// worker prints the refusal, writes the hand-over row, withdraws its
// announcement and exits 1, and a `review-file --wait` already polling
// answers worker_down (no worker serves the queue any more).

// TestB561WorkerChild runs `worker` with the arguments after "--".
func TestB561WorkerChild(t *testing.T) {
	if os.Getenv("B561_WORKER_CHILD") != "1" {
		return
	}
	schema, lenses := os.Getenv("B561_SCHEMA"), os.Getenv("B561_LENSES")
	session.FindingSchemaPath = func() (string, error) { return schema, nil }
	review.LensDir = func() (string, error) { return lenses, nil }
	args := []string{}
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	os.Exit(runWorkerVerb(args))
}

func TestB561WaitSurvivesWorkerHandOverToNode(t *testing.T) {
	f := newB489Fix(t)
	schema, _ := session.FindingSchemaPath()
	lenses, _ := review.LensDir()
	ticket := strings.Repeat("c", 24)

	cmd := exec.Command(os.Args[0], "-test.run=^TestB561WorkerChild$", "--", "--run", "r1", "--poll-ms", "20")
	cmd.Dir = f.root
	cmd.Env = append(os.Environ(), "B561_WORKER_CHILD=1", "B561_SCHEMA="+schema, "B561_LENSES="+lenses, "CODE_FORGE_NODE=/nonexistent-b561-node")
	var workerErr strings.Builder
	cmd.Stderr = &workerErr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		select {
		case <-exited:
		default:
			_ = cmd.Process.Kill()
			<-exited
		}
	})
	// The Go worker serves: its registry entry is announced.
	prefix := strconv.Itoa(cmd.Process.Pid) + "-"
	announced := func() bool {
		names, _ := os.ReadDir(worker.WorkersDir(f.root))
		for _, n := range names {
			if strings.HasPrefix(n.Name(), prefix) && strings.HasSuffix(n.Name(), ".json") {
				return true
			}
		}
		return false
	}
	for deadline := time.Now().Add(60 * time.Second); !announced(); {
		if time.Now().After(deadline) {
			t.Fatalf("the Go worker never announced itself; stderr %s", workerErr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A waiter polls the ticket, then the unported ticket arrives and the Go
	// worker refuses it (B690a) and stops.
	waited := make(chan reviewFileRun, 1)
	go func() { waited <- reviewFileExec(t, f.root, f.home, false, "--wait", ticket, "--max", "120s") }()
	time.Sleep(300 * time.Millisecond)
	f.unported(t)

	select {
	case <-exited:
	case <-time.After(60 * time.Second):
		t.Fatal("the Go worker did not stop")
	}
	want := "code-forge: worker is not supported in this case yet (worker-ticket-unported). Please report it: code-forge logs report\n"
	if code := cmd.ProcessState.ExitCode(); code != 1 || !strings.HasSuffix(workerErr.String(), want) {
		t.Fatalf("worker exit %d (%v) stderr %q; want exit 1 ending in %q", code, waitErr, workerErr.String(), want)
	}
	var got reviewFileRun
	select {
	case got = <-waited:
	case <-time.After(150 * time.Second):
		t.Fatal("the waiter did not return")
	}
	if got.code != 3 || got.stdout != `{"ticket":"`+ticket+`","status":"worker_down"}`+"\n" {
		t.Fatalf("waiter = %d %q %q; worker stderr %s", got.code, got.stdout, got.stderr, workerErr.String())
	}
	if rows := strings.Join(b460Handovers(t, f.home), ","); rows != "worker worker-ticket-unported" {
		t.Fatalf("hand-overs %s", rows)
	}
	// The refused worker withdrew its announcement.
	if names, _ := os.ReadDir(worker.WorkersDir(f.root)); len(names) != 0 {
		t.Fatalf("announcements left: %v", names)
	}
	if b489Exists(filepath.Join(worker.QueueDir(f.root), "worker.json")) {
		t.Fatal("worker.json left")
	}
}
