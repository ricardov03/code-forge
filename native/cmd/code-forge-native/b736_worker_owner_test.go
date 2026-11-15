package main

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/ricardov03/code-forge/native/worker"
)

// B736: the worker `run start` launches outlives its caller, so its parent
// is init. It used to inherit the caller's CODE_FORGE_OWNER_PID (the review
// CLI's pid, set by childreg), so once that CLI exited every owner-based
// sweep read the live worker of an open run as a proven orphan: Node's
// sweepProcesses (src/util/reaper.mjs, run by Node `run start`,
// src/cli/run.mjs:177) knows only `node code-forge.mjs worker` as a worker
// and sent the Go worker SIGTERM, 2 s, SIGKILL. The SIGKILL skipped the
// worker's exit path and left its lazy root proc-<pid>-<ms> (B734 load runs).
// The worker is judged by its run record, never by an owner: it starts with
// no owner variables, so no orphan rule can name it.
func TestB736KeptRunWorkerIsNoOrphanOfItsDeadCaller(t *testing.T) {
	bin := b553Binary(t)
	f := b553Fixture(t, "approve")
	env := b553Env(f, "CODE_FORGE_HANDOVER_LOG=1", "CODE_FORGE_NODE="+filepath.Join(f.dir, "missing-node"))
	cmd := exec.Command(bin, "review", "--files", "src/a.txt", "--max", "60", "--keep-run")
	cmd.Dir, cmd.Env = f.repo, env
	out, err := cmd.CombinedOutput()
	defer b553Reap(t, f)
	if err != nil {
		t.Fatalf("review --keep-run: %v\n%s", err, out)
	}
	workers, _ := filepath.Glob(filepath.Join(f.repo, ".code-forge", "workers", "*.json"))
	pid := 0
	for _, file := range workers {
		if n, err := strconv.Atoi(strings.SplitN(filepath.Base(file), "-", 2)[0]); err == nil && strings.Contains(b511Command(n), "worker") {
			pid = n
		}
	}
	if pid == 0 {
		t.Fatalf("no live worker after review --keep-run (workers %v)", workers)
	}
	var signals []string
	sweep := &worker.OrphanSweep{
		// the live table narrowed to the worker: no other process is judged
		Processes: func() []worker.ProcInfo {
			rows := []worker.ProcInfo{}
			for _, p := range b736Processes(t) {
				if p.PID == pid {
					rows = append(rows, p)
				}
			}
			return rows
		},
		Kill: func(p int, sig syscall.Signal) error {
			signals = append(signals, strconv.Itoa(p)+" "+sig.String())
			return nil
		},
	}
	rows := sweep.Processes()
	if len(rows) != 1 || rows[0].PPID != 1 {
		t.Fatalf("worker %d rows %+v: want one row whose parent is init", pid, rows)
	}
	if orphans := sweep.Orphans(); len(orphans) != 0 {
		t.Errorf("the live worker %d of the kept run is a proven orphan (owner %q): %+v", pid, worker.ReadProcessEnv(rows[0])["CODE_FORGE_OWNER_PID"], orphans)
	}
	sweep.Sweep()
	if len(signals) != 0 {
		t.Errorf("an orphan sweep signalled the live worker: %v", signals)
	}
}

// b736Processes is the live process table (ps, C locale).
func b736Processes(t *testing.T) []worker.ProcInfo {
	t.Helper()
	cmd := exec.Command("/bin/ps", "-A", "-ww", "-o", "uid=,pid=,ppid=,stat=,rss=,lstart=,command=")
	cmd.Env = append(cmd.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return worker.ParseProcesses(string(out))
}
