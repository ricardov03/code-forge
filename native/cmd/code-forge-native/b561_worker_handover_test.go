package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/review"
	"github.com/ricardov03/code-forge/native/session"
	"github.com/ricardov03/code-forge/native/worker"
)

// B561: a Go worker that met a ticket it does not serve used to replace
// itself (same pid) with the Node worker; B690a made that an exit 1, and a
// `review-file --wait` already polling answered worker_down. B884: the
// worker closes that ticket as unsupported and keeps serving, so the waiter
// gets the ticket's done answer; a forced stop writes worker.exit.

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

func TestB884WaitSurvivesUnsupportedTicket(t *testing.T) {
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

	// A waiter polls the ticket, then the unported ticket arrives. B884: the
	// Go worker closes it as unsupported and keeps serving; the waiter gets
	// the done answer at once (never worker_down).
	waited := make(chan reviewFileRun, 1)
	go func() { waited <- reviewFileExec(t, f.root, f.home, false, "--wait", ticket, "--max", "120s") }()
	time.Sleep(300 * time.Millisecond)
	f.unported(t)

	var got reviewFileRun
	select {
	case got = <-waited:
	case <-time.After(150 * time.Second):
		t.Fatal("the waiter did not return")
	}
	if got.code != 0 || !strings.HasPrefix(got.stdout, `{"ticket":"`+ticket+`","status":"done"`) || !strings.Contains(got.stdout, `"status":"unsupported"`) {
		t.Fatalf("waiter = %d %q %q; worker stderr %s", got.code, got.stdout, got.stderr, workerErr.String())
	}
	select {
	case <-exited:
		t.Fatalf("the Go worker stopped on one unsupported ticket: %s", workerErr.String())
	case <-time.After(500 * time.Millisecond):
	}
	if rows := b460Handovers(t, f.home); len(rows) != 0 {
		t.Fatalf("hand-overs %v", rows)
	}
	// A forced stop: the worker writes its signed worker.exit row.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
	case <-time.After(60 * time.Second):
		t.Fatal("the Go worker did not stop on SIGTERM")
	}
	exits := 0
	for _, r := range f.rows(t) {
		if r["event"] == "worker.exit" && r["reason"] == "signal" && r["detail"] == "stopped by signal terminated" {
			exits++
		}
	}
	if exits != 1 {
		t.Fatalf("%d worker.exit rows (exit %v); stderr %s", exits, waitErr, workerErr.String())
	}
	// The stopped worker withdrew its announcement.
	if names, _ := os.ReadDir(worker.WorkersDir(f.root)); len(names) != 0 {
		t.Fatalf("announcements left: %v", names)
	}
	if b489Exists(filepath.Join(worker.QueueDir(f.root), "worker.json")) {
		t.Fatal("worker.json left")
	}
}
