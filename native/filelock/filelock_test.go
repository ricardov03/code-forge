package filelock

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestOwnershipAndBreaks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	file, err := Path("unit")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, owner := range []string{`{`, `{"key":"unit","pid":1,"start":"old"}`, `{"key":"other","pid":22,"start":"old"}`} {
		write(owner)
		lock, err := TryLock("unit", Options{Alive: func(int) bool { return false }})
		if err != nil || lock.OK {
			t.Fatalf("malformed/mismatched owner must stay held: %q, %v, %v", owner, lock.OK, err)
		}
	}
	dead := `{"key":"unit","pid":22,"start":"old"}`
	write(dead)
	lock, err := TryLock("unit", Options{Alive: func(int) bool { return true }, Probe: func(int) *string { return nil }})
	if err != nil || lock.OK {
		t.Fatalf("unknown start must fail closed: %v %v", lock.OK, err)
	}
	lock, err = TryLock("unit", Options{Alive: func(int) bool { return false }, AfterRead: func() error { write("replacement"); return nil }})
	if err != nil || lock.OK {
		t.Fatalf("changed bytes must stay held: %v %v", lock.OK, err)
	}
	write(dead)
	lock, err = TryLock("unit", Options{Alive: func(int) bool { return false }})
	if err != nil || !lock.OK {
		t.Fatalf("proven dead owner must be reclaimed: %v %v", lock.OK, err)
	}
	write("replacement")
	lock.Release()
	lock.Release()
	data, err := os.ReadFile(file)
	if err != nil || string(data) != "replacement" {
		t.Fatalf("release must preserve replacement: %q %v", data, err)
	}
	if _, err := os.Stat(file + ".break"); !os.IsNotExist(err) {
		t.Fatalf("break mutex leaked: %v", err)
	}
}

func TestKeyedFIFOAndFailureRelease(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var wg sync.WaitGroup
	order := []int{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = KeyedLock("fifo", func() error { close(entered); <-release; order = append(order, 0); return errors.New("first") })
	}()
	<-entered
	var previous chan struct{}
	keyed.Lock()
	previous = keyed.tails["fifo"]
	keyed.Unlock()
	for i := 1; i <= 8; i++ {
		i := i
		wg.Add(1)
		go func() { defer wg.Done(); _ = KeyedLock("fifo", func() error { order = append(order, i); return nil }) }()
		deadline := time.Now().Add(time.Second)
		for {
			keyed.Lock()
			next := keyed.tails["fifo"]
			keyed.Unlock()
			if next != previous {
				previous = next
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("waiter did not enqueue")
			}
			time.Sleep(time.Millisecond)
		}
	}
	if err := KeyedLock("other", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	close(release)
	wg.Wait()
	if !reflect.DeepEqual(order, []int{0, 1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("nine callbacks must be FIFO: %v", order)
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic must propagate")
			}
		}()
		_ = KeyedLock("panic", func() error { panic("fixture") })
	}()
	if err := KeyedLock("panic", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	keyed.Lock()
	count := len(keyed.tails)
	keyed.Unlock()
	if count != 0 {
		t.Fatalf("want zero retained keys, got %d", count)
	}
}
