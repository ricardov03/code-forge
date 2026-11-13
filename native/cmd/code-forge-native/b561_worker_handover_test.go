package main

import (
	"fmt"
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

// B561: a Go worker that hands its run to Node replaces itself (same pid) with
// the Node worker. Node needs time to start and boot; a `review-file --wait`
// already polling must not read that gap as worker_down. The fake `node` here
// is this test binary (same pid as the Go worker, through exec): it waits as a
// slow Node start would, then boots like loop.mjs (prune the registry, refuse
// on a live entry, announce) and answers the ticket.

// b561FakeNodeStartMs stands in for Node's start-up before its boot lock.
const b561FakeNodeStartMs = 1000

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

// TestB561FakeNode is the Node worker the Go worker execs into.
func TestB561FakeNode(t *testing.T) {
	if os.Getenv("B561_FAKE_NODE") != "1" {
		return
	}
	root, ticket, run := os.Getenv("B561_ROOT"), os.Getenv("B561_TICKET"), os.Getenv("B561_RUN")
	time.Sleep(b561FakeNodeStartMs * time.Millisecond)
	// loop.mjs boot: workerRegistry(prune) then liveWorker for the legacy file.
	entries, err := worker.Registry(root, true, worker.Probes{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
	count := 0
	for _, e := range entries {
		if e.Live {
			count++
		}
	}
	if legacy, _ := worker.Live(root, worker.LiveOpts{}, worker.Probes{}); legacy != nil && count == 0 {
		count++
	}
	if count >= 1 {
		fmt.Fprintln(os.Stderr, "worker: review.workers limit 1: a live or unreadable worker entry already serves this queue")
		os.Exit(1)
	}
	self, err := worker.Announce(root, os.Getpid(), run, worker.Probes{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "worker:", err)
		os.Exit(1)
	}
	marker := filepath.Join(worker.QueueDir(root), ticket+".done")
	if err := os.WriteFile(marker, []byte(`{"ticket":"`+ticket+`"}`+"\n"), 0o644); err != nil {
		os.Exit(1)
	}
	// Serve until the waiter has read the marker, then stop cleanly.
	time.Sleep(500 * time.Millisecond)
	start, _ := self.Get("start_time").(string)
	_ = worker.Retract(root, os.Getpid(), &start, worker.Probes{})
	os.Exit(0)
}

func TestB561WaitSurvivesWorkerHandOverToNode(t *testing.T) {
	f := newB489Fix(t)
	schema, _ := session.FindingSchemaPath()
	lenses, _ := review.LensDir()
	ticket := strings.Repeat("c", 24)

	bin := t.TempDir()
	fakeNode := filepath.Join(bin, "fake-node")
	script := "#!/bin/sh\nexec \"" + os.Args[0] + "\" -test.run='^TestB561FakeNode$'\n"
	if err := os.WriteFile(fakeNode, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestB561WorkerChild$", "--", "--run", "r1", "--poll-ms", "20")
	cmd.Dir = f.root
	cmd.Env = append(os.Environ(), "B561_WORKER_CHILD=1", "B561_FAKE_NODE=1", "B561_SCHEMA="+schema, "B561_LENSES="+lenses,
		"B561_ROOT="+f.root, "B561_TICKET="+ticket, "B561_RUN=r1", "CODE_FORGE_NODE="+fakeNode)
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
	for deadline := time.Now().Add(20 * time.Second); !announced(); {
		if time.Now().After(deadline) {
			t.Fatalf("the Go worker never announced itself; stderr %s", workerErr.String())
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A waiter polls the ticket, then the unported ticket arrives and the Go
	// worker hands the run to Node.
	waited := make(chan reviewFileRun, 1)
	go func() { waited <- reviewFileExec(t, f.root, f.home, false, "--wait", ticket, "--max", "30s") }()
	time.Sleep(300 * time.Millisecond)
	f.unported(t)

	var got reviewFileRun
	select {
	case got = <-waited:
	case <-time.After(40 * time.Second):
		t.Fatal("the waiter did not return")
	}
	if got.code != 0 || got.stdout != `{"ticket":"`+ticket+`","status":"done","result":null}`+"\n" {
		t.Fatalf("waiter = %d %q %q; worker stderr %s", got.code, got.stdout, got.stderr, workerErr.String())
	}
	select {
	case <-exited:
		if waitErr != nil {
			t.Fatalf("the Node worker refused the hand-over: %v; stderr %s", waitErr, workerErr.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the Node worker did not stop")
	}
	if rows := strings.Join(b460Handovers(t, f.home), ","); rows != "worker worker-ticket-unported" {
		t.Fatalf("hand-overs %s", rows)
	}
	// Node's boot pruned the hand-over announcement; Node retracted its own.
	if names, _ := os.ReadDir(worker.WorkersDir(f.root)); len(names) != 0 {
		t.Fatalf("announcements left: %v", names)
	}
	if b489Exists(filepath.Join(worker.QueueDir(f.root), "worker.json")) {
		t.Fatal("worker.json left")
	}
}
