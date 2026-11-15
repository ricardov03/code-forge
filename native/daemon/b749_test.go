//go:build unix

package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ticket"
)

// b749Tickets writes one review ticket for every old run of the fixture
// (runs 0..runs-1) and returns the queue directory.
func b749Tickets(t *testing.T, f b749Fixture, runs int) {
	t.Helper()
	dir := filepath.Join(f.repo, ".code-forge", "queue")
	for i := 0; i < runs; i++ {
		p := ticket.Parts{Run: "r-old-" + strconv.Itoa(i), Block: "B1", File: "a.go", ContentHash: "hash"}
		id := ticket.ReviewID(p)
		writeObject(t, filepath.Join(dir, id+".json"), object{"ticket": id, "run": p.Run, "block": p.Block, "file": p.File, "content_hash": p.ContentHash})
	}
}

// B749: the sweep prunes exactly the old runs with a valid signed run.end row
// and a usable key, also when a run has many run.end rows (keyed, unkeyed and
// forged runs mixed).
func TestB749SweepPrunesSignedEndedRunsOnly(t *testing.T) {
	t.Setenv("CODE_FORGE_PRUNE_GRACE", "10m")
	const n, perRun = 3000, 3
	runs := n / perRun
	f := b749Write(t, n, perRun, func(i int) bool { return i%3 != 0 }, func(i int) bool { return i%5 == 0 })
	b749Tickets(t, f, runs)
	if len(f.ended) == 0 || len(f.ended) == runs {
		t.Fatalf("fixture must mix pruned and kept runs: %d of %d", len(f.ended), runs)
	}
	s := f.server()
	if got := s.handle(context.Background(), request{Method: "ping"}); got["ok"] != true {
		t.Fatal(got)
	}
	for i := 0; i < runs; i++ {
		run := "r-old-" + strconv.Itoa(i)
		want := 1
		if f.ended[run] {
			want = 0
		}
		if got := len(s.queue.Block(run, "B1", nil, nil)); got != want {
			t.Errorf("%s tickets = %d, want %d", run, got, want)
		}
	}
}

// B749: a sweep over run.end rows does a bounded amount of work per row.
// One regexp compile per row (the old sweep) costs dozens of allocations per
// row; a once-compiled pattern and one key read per run cost about one.
func TestB749SweepAllocationsPerRunEndRow(t *testing.T) {
	t.Setenv("CODE_FORGE_PRUNE_GRACE", "10m")
	const n = 2000
	// Every row is the run.end row of a run without a key, the worst case:
	// no run is ever marked ended, so every row is checked.
	f := b749Write(t, n, 1, func(int) bool { return false }, func(int) bool { return false })
	s := f.server()
	if err := s.refresh(); err != nil {
		t.Fatal(err)
	}
	allocs := testing.AllocsPerRun(3, func() {
		s.lastSweep = time.Time{}
		s.sweep(time.Now())
	})
	if perRow := allocs / n; perRow > 4 {
		t.Fatalf("sweep made %.0f allocations for %d run.end rows (%.1f per row), want at most 4 per row", allocs, n, perRow)
	}
}

// B750: the daemon listens before its first full ledger read, and a request
// sent while that read runs waits for it and then answers from the whole
// ledger. The run's scope record is a FIFO here, so the first read blocks
// until the test writes it: no timing guess decides the order.
func TestB750ListensBeforeFirstReadAndAnswersAfterIt(t *testing.T) {
	t.Setenv("CODE_FORGE_PRUNE_GRACE", "10m")
	const n = 1000
	f := b749Write(t, n, 10, func(int) bool { return true }, func(int) bool { return false })
	wantRows := 0
	for i := 0; i < n; i += 50 {
		wantRows++ // every 50th fixture row is a current-run block.open row
	}
	record := filepath.Join(f.repo, ".code-forge", "runs", f.run+".json")
	content, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(record, 0600); err != nil {
		t.Fatal(err)
	}
	writer := record + ".writer"
	if err := os.Link(record, writer); err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if released {
			return
		}
		released = true
		// The scope file becomes a regular file again for later refreshes;
		// the read already waiting on the FIFO gets the same content.
		regular := record + ".tmp"
		if err := os.WriteFile(regular, content, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(regular, record); err != nil {
			t.Fatal(err)
		}
		fifo, err := os.OpenFile(writer, os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		fifo.Write(content)
		fifo.Close()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeContext(ctx, f.home, f.run, "v-b750", time.Minute) }()
	defer func() {
		release()
		cancel()
		if err := <-done; err != nil {
			t.Errorf("ServeContext: %v", err)
		}
	}()
	socket := filepath.Join(f.home, ".code-forge", "runs", f.run, "native.sock")
	deadline := time.Now().Add(5 * time.Second)
	var conn net.Conn
	for conn == nil {
		if c, err := net.Dial("unix", socket); err == nil {
			conn = c
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the daemon did not listen while its first ledger read was still running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(`{"method":"rows"}` + "\n")); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if line, err := reader.ReadString('\n'); err == nil {
		t.Fatalf("answered before the first ledger read ended: %s", line)
	}
	release()
	conn.SetReadDeadline(time.Now().Add(30 * time.Second))
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var answer struct {
		Rows  []object `json:"rows"`
		Error string   `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.Error != "" || len(answer.Rows) != wantRows {
		t.Fatalf("rows answer has %d rows (error %q), want all %d current-run rows of the ledger", len(answer.Rows), answer.Error, wantRows)
	}
	for _, row := range answer.Rows {
		if row["run"] != f.run {
			t.Fatalf("rows answer carries another run's row: %v", row)
		}
	}
	ping := func() object {
		t.Helper()
		if _, err := conn.Write([]byte(`{"method":"ping"}` + "\n")); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		var got object
		json.Unmarshal([]byte(line), &got)
		return got
	}
	if got := ping(); got["ok"] != true || got["run"] != f.run || got["version"] != "v-b750" {
		t.Fatalf("ping = %v", got)
	}
}

// B750: a first ledger read that fails still ends the daemon with that
// error and leaves no socket, as when it read before listening.
func TestB750FirstReadFailureEndsDaemon(t *testing.T) {
	f := b749Write(t, 10, 5, func(int) bool { return true }, func(int) bool { return false })
	writeObject(t, filepath.Join(f.repo, ".code-forge", "runs", f.run+".json"), object{"run_id": f.run, "project": "other-project"})
	done := make(chan error, 1)
	go func() { done <- ServeContext(context.Background(), f.home, f.run, "v", time.Minute) }()
	select {
	case err := <-done:
		if err == nil || err.Error() != "block-scope-unreadable" {
			t.Fatalf("ServeContext = %v, want block-scope-unreadable", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("daemon kept serving after its first ledger read failed")
	}
	if _, err := os.Lstat(filepath.Join(f.home, ".code-forge", "runs", f.run, "native.sock")); !os.IsNotExist(err) {
		t.Fatalf("socket left behind: %v", err)
	}
}
