package ledger

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type appendOwner struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"started_at"`
}

// Shared with Node: older writers may still be publishing their identity.
const unidentifiedLockGrace = 30 * time.Second

// appendSelf caches this process's lock identity once ps has produced it.
// A failed read is not cached: Node's `selfStart ??=` retries on the next
// append, so one slow ps on a loaded host must not fail every later append.
var appendSelf struct {
	sync.Mutex
	data []byte
}

// appendPSPath is the preferred ps binary; tests point it at a fake.
var appendPSPath = "/bin/ps"

// appendPSTimeout matches Node's readStartTime execFileSync timeout (5000 ms).
const appendPSTimeout = 5 * time.Second

func appendStartTime(pid int) (string, error) {
	ps := appendPSPath
	if _, err := os.Stat(ps); err != nil {
		ps = "ps"
	}
	ctx, cancel := context.WithTimeout(context.Background(), appendPSTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps, "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	return strings.Join(strings.Fields(string(out)), " "), err
}

// Identified dead/reused pids are stale. Unidentified records need a grace
// period for legacy writers; age never overrides a valid live identity.
func staleAppendLock(file string) (bool, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var owner appendOwner
	if json.Unmarshal(data, &owner) != nil || owner.PID <= 0 || strings.TrimSpace(owner.StartedAt) == "" {
		info, err := os.Stat(file)
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		return time.Since(info.ModTime()) > unidentifiedLockGrace, nil
	}
	alive, err := appendOwnerAlive(owner.PID)
	if err != nil || !alive {
		return !alive && err == nil, err
	}
	start, err := appendStartTime(owner.PID)
	return err == nil && start != "" && start != strings.Join(strings.Fields(owner.StartedAt), " "), nil
}

func appendIdentity() ([]byte, error) {
	appendSelf.Lock()
	defer appendSelf.Unlock()
	if appendSelf.data != nil {
		return appendSelf.data, nil
	}
	start, err := appendStartTime(os.Getpid())
	if err != nil || start == "" {
		return nil, fmt.Errorf("cannot determine ledger lock owner process start time: %v", err)
	}
	data, err := json.Marshal(appendOwner{os.Getpid(), start})
	if err != nil {
		return nil, err
	}
	appendSelf.data = data
	return data, nil
}

// Reclamation uses this SAME link-published protocol on file+".reap".
// Serializing stale checks and rename prevents two reapers from moving a new
// live owner's lock after one has already replaced the stale inode. A killed
// reaper is itself recoverable by the same protocol, with the same deadline.
func lockAppend(file, slug string, deadline time.Time) (func(), error) {
	identity, err := appendIdentity()
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".tmp.")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	n, writeErr := f.Write(identity)
	closeErr := f.Close()
	if writeErr != nil {
		return nil, writeErr
	}
	if n != len(identity) {
		return nil, io.ErrShortWrite
	}
	if closeErr != nil {
		return nil, closeErr
	}
	for {
		// Only the complete identity is visible at the shared lock path.
		err := os.Link(f.Name(), file)
		if err == nil {
			return func() { _ = os.Remove(file) }, nil
		}
		if !os.IsExist(err) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("ledger %s is locked (%s)", slug, file)
		}
		stale, err := staleAppendLock(file)
		if err != nil {
			return nil, err
		}
		if stale {
			if err := reapAppendLock(file, slug, deadline); err != nil {
				return nil, err
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func reapAppendLock(file, slug string, deadline time.Time) error {
	release, err := lockAppend(file+".reap", slug, deadline)
	if err != nil {
		return err
	}
	defer release()
	// Re-read and re-probe under the reclamation lock; the earlier snapshot may
	// already have been replaced by a new writer while we were waiting.
	stale, err := staleAppendLock(file)
	if err != nil || !stale {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), filepath.Base(file)+".stale.")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(name)
	if err := os.Rename(file, name); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.Remove(name)
}
