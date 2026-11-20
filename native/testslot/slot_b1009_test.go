package testslot

import (
	"testing"
	"time"
)

// B1009: the machine this was seen on (2026-11-19): 10 cores, load 13-18,
// little free memory, so Slots is 1 and a second gate waits for the whole
// first run. A by-name gate (a few tests) must not wait behind it.
var b1009Busy = Resources{Cores: 10, Load1: 18, FreeBytes: 3 << 30, Known: true}

func TestB1009NamesGatePassesBigRun(t *testing.T) {
	for _, big := range []string{Changed, Full} {
		t.Run(big, func(t *testing.T) {
			q := testQueue(t, b1009Busy)
			rec := &recorder{holding: map[string]string{}}
			h := start(t, q, rec, "H", big)
			if !admittedWithin(h, admitWait) {
				t.Fatal("H: a lone run on an empty queue must start at once")
			}
			waitTickets(t, q, 1)
			// The reproduced wait: a changed gate behind the big holder waits.
			w := start(t, q, rec, "W", Changed)
			waitTickets(t, q, 2)
			f := start(t, q, rec, "F", Full)
			waitTickets(t, q, 3)
			if admittedWithin(w, 150*time.Millisecond) {
				t.Fatal("W (changed) started beside the big holder on a loaded machine: the old wait is gone, check the fake")
			}
			// A by-name gate passes the holder and both waiters.
			n := start(t, q, rec, "N", Names)
			if !admittedWithin(n, time.Second) {
				t.Fatalf("N (by name) waited behind %s holder H and waiters W, F; order %v", big, rec.snapshot())
			}
			if admittedWithin(w, 50*time.Millisecond) || admittedWithin(f, 10*time.Millisecond) {
				t.Fatalf("N let a big waiter start beside H; order %v", rec.snapshot())
			}
			finish(t, n)
			finish(t, h)
			if !admittedWithin(w, admitWait) {
				t.Fatal("W must start once H ended")
			}
			finish(t, w)
			if !admittedWithin(f, admitWait) {
				t.Fatal("F must start once W ended")
			}
			finish(t, f)
			if got := rec.snapshot(); len(got) != 4 || got[0] != "H" || got[1] != "N" || got[2] != "W" || got[3] != "F" {
				t.Fatalf("admission order = %v, want [H N W F]", got)
			}
			if left := filesLeft(t, q); len(left) != 0 {
				t.Fatalf("files left = %v, want none", left)
			}
		})
	}
}

func TestB1009NamesSlotsAreFIFOAndCapped(t *testing.T) {
	// 8 cores: two by-name slots, whatever the load.
	res := Resources{Cores: 8, Load1: 30, FreeBytes: 4 << 30, Known: true}
	if n := NameSlots(res); n != 2 {
		t.Fatalf("NameSlots(8 cores) = %d, want 2", n)
	}
	if n := NameSlots(Resources{Cores: 2}); n != 1 {
		t.Fatalf("NameSlots(2 cores) = %d, want 1", n)
	}
	q := testQueue(t, res)
	rec := &recorder{holding: map[string]string{}}
	h := start(t, q, rec, "H", Full)
	if !admittedWithin(h, admitWait) {
		t.Fatal("H must start")
	}
	waitTickets(t, q, 1)
	n1 := start(t, q, rec, "N1", Names)
	if !admittedWithin(n1, time.Second) {
		t.Fatal("N1 must start beside the full holder")
	}
	n2 := start(t, q, rec, "N2", Names)
	if !admittedWithin(n2, admitWait) {
		t.Fatal("N2 must take the second by-name slot")
	}
	n3 := start(t, q, rec, "N3", Names)
	waitTickets(t, q, 4)
	n4 := start(t, q, rec, "N4", Names)
	waitTickets(t, q, 5)
	if admittedWithin(n3, 150*time.Millisecond) || admittedWithin(n4, 10*time.Millisecond) {
		t.Fatalf("N3/N4 started with both by-name slots held; order %v", rec.snapshot())
	}
	finish(t, n1)
	if !admittedWithin(n3, admitWait) {
		t.Fatal("N3 (first by-name waiter) must start when a by-name slot frees")
	}
	if admittedWithin(n4, 150*time.Millisecond) {
		t.Fatalf("N4 passed N3 or took a third slot; order %v", rec.snapshot())
	}
	finish(t, n2)
	if !admittedWithin(n4, admitWait) {
		t.Fatal("N4 must start next")
	}
	finish(t, n3)
	finish(t, n4)
	finish(t, h)
	if got := rec.snapshot(); len(got) != 5 || got[1] != "N1" || got[2] != "N2" || got[3] != "N3" || got[4] != "N4" {
		t.Fatalf("admission order = %v, want [H N1 N2 N3 N4]", got)
	}
	// Low memory holds a by-name gate back beside another holder.
	low := testQueue(t, Resources{Cores: 8, Load1: 1, FreeBytes: 512 << 20, Known: true})
	rec = &recorder{holding: map[string]string{}}
	h = start(t, low, rec, "H", Changed)
	if !admittedWithin(h, admitWait) {
		t.Fatal("H must start")
	}
	waitTickets(t, low, 1)
	n := start(t, low, rec, "N", Names)
	if admittedWithin(n, 150*time.Millisecond) {
		t.Fatal("N started beside H with free memory under the floor")
	}
	finish(t, h)
	if !admittedWithin(n, time.Second) {
		t.Fatal("N must start once nothing holds")
	}
	finish(t, n)
}
