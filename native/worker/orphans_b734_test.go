package worker

import (
	"strconv"
	"syscall"
	"testing"
	"time"
)

// B734: the orphan sweep reads one environment per init-parented process of
// this user (`ps -E` on macOS, 317 on the owner's machine), so a SIGTERM that
// lands in the sweep after a drain waited for every read; under a background
// QoS throttle that passed 30 s. A stopping worker now takes no further read
// and signals no further process.

func b734Rows(n int) []ProcInfo {
	rows := make([]ProcInfo, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, ProcInfo{UID: 501, PID: 1000 + i, PPID: 1, State: "S", Start: "Wed Nov 11 09:00:00 2026", Command: "proc " + strconv.Itoa(i)})
	}
	return rows
}

// every row is an orphan of run r1 (HOME /h/run) whose owner 900 is dead
func b734Sweep(rows []ProcInfo, reads *int, kills *[]int, stopping func() bool, onRead func(int)) *OrphanSweep {
	clock := fixedNow
	return &OrphanSweep{RunID: "r1", Home: "/h/run", UID: 501, Self: 600, Parent: 601, GraceMs: 1,
		Processes: func() []ProcInfo { return rows },
		ReadEnv: func(ProcInfo) map[string]string {
			*reads++
			if onRead != nil {
				onRead(*reads)
			}
			return map[string]string{"HOME": "/h/run", "CODE_FORGE_OWNER_PID": "900", "CODE_FORGE_RUN_ID": "r1"}
		},
		Alive: func(int) bool { return false },
		Kill: func(pid int, sig syscall.Signal) error {
			if sig == syscall.SIGTERM {
				*kills = append(*kills, pid)
			}
			return nil
		},
		Sleep:    func(d time.Duration) { clock = clock.Add(d) },
		Now:      func() time.Time { return clock },
		Stopping: stopping,
	}
}

// A stop during the environment reads ends the sweep: no read after it and
// no process signalled.
func TestB734SweepEndsOnStopDuringReads(t *testing.T) {
	stopped := false
	reads, kills := 0, []int{}
	s := b734Sweep(b734Rows(50), &reads, &kills, func() bool { return stopped }, func(n int) {
		if n == 3 {
			stopped = true // the worker's SIGTERM arrives here
		}
	})
	s.Sweep()
	if reads != 3 || len(kills) != 0 {
		t.Fatalf("after the stop: %d env reads (want 3), signalled %v (want none)", reads, kills)
	}
}

// A stop while proven orphans are being stopped: the one in hand finishes,
// no further one is signalled.
func TestB734SweepEndsOnStopBetweenStops(t *testing.T) {
	stopped := false
	reads, kills := 0, []int{}
	s := b734Sweep(b734Rows(5), &reads, &kills, func() bool { return stopped || len(kills) > 0 }, nil)
	s.Sweep()
	if reads != 5 || len(kills) != 1 || kills[0] != 1000 {
		t.Fatalf("reads %d (want 5), signalled %v (want [1000])", reads, kills)
	}
}

// Not stopping (or no Stopping seam): the whole sweep runs as Node's does.
func TestB734SweepRunsWholeWhenNotStopping(t *testing.T) {
	for _, stopping := range []func() bool{nil, func() bool { return false }} {
		reads, kills := 0, []int{}
		s := b734Sweep(b734Rows(5), &reads, &kills, stopping, nil)
		s.Sweep()
		if reads != 5 || len(kills) != 5 {
			t.Fatalf("reads %d, signalled %v: want 5 and 5", reads, kills)
		}
	}
}
