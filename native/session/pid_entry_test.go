package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
)

// B692: a reader that finds <pid>.json (a sweep, the B573 fake git) must
// read the whole entry. The entry used to be written in place, so a reader
// that came between the create and the write read an empty or cut entry
// (a sweep skips it; the fake git logged a line without its entry).
func TestRegisterPidEntryIsWholeWhenSeen(t *testing.T) {
	pid := os.Getpid()
	var partial, seen atomic.Int64
	for i := 0; i < 300; i++ {
		dir := filepath.Join(t.TempDir(), "pids")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, strconv.Itoa(pid)+".json")
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case <-stop:
					return
				default:
				}
				data, err := os.ReadFile(file)
				if err != nil {
					continue
				}
				seen.Add(1)
				var entry struct {
					PID       int    `json:"pid"`
					StartTime string `json:"start_time"`
					Argv0     string `json:"argv0"`
				}
				if json.Unmarshal(data, &entry) != nil || entry.PID != pid || entry.Argv0 != "git" {
					partial.Add(1)
				}
				return
			}
		}()
		if err := RegisterPid(dir, pid, "git"); err != nil {
			close(stop)
			<-done
			t.Fatal(err)
		}
		<-done
		close(stop)
		info, err := os.Stat(file)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("entry mode: %v %v", info, err)
		}
		names, _ := os.ReadDir(dir)
		if len(names) != 1 {
			t.Fatalf("pids dir holds %d names, want only the entry", len(names))
		}
	}
	if partial.Load() != 0 {
		t.Fatalf("%d of %d readers saw a cut entry", partial.Load(), seen.Load())
	}
}
