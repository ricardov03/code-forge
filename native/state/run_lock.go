package state

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/testslot"
)

// lockOwnerFile sits inside a Go-held run lock dir and names its holder.
// Node holders write none; they are judged by the dir's age alone.
const lockOwnerFile = "owner.json"

// ownerlessStaleAge is how old a lock dir with no owner file must be before a
// waiter breaks it (an old Go binary or a Node holder that died).
const ownerlessStaleAge = 10 * time.Minute

type lockOwner struct {
	PID   int    `json:"pid"`
	Start string `json:"start"`
	Verb  string `json:"verb"`
}

// writeLockOwner records this process (pid, start token, verb) in the lock.
func writeLockOwner(lock string) error {
	start, _ := testslot.ProcessStart(os.Getpid())
	data, err := json.Marshal(lockOwner{PID: os.Getpid(), Start: start, Verb: lockVerb()})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(lock, lockOwnerFile), append(data, '\n'), 0644)
}

// removeLockOwner removes the owner file; WithRunLock then removes the dir.
func removeLockOwner(lock string) {
	_ = os.Remove(filepath.Join(lock, lockOwnerFile))
}

// lockVerb is the leading words of the command line, up to the first flag.
func lockVerb() string {
	var words []string
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "-") || len(words) == 2 {
			break
		}
		words = append(words, arg)
	}
	if len(words) == 0 && len(os.Args) > 0 {
		return filepath.Base(os.Args[0])
	}
	return strings.Join(words, " ")
}

// ownerAlive is false when pid is gone, a zombie, or a different process
// (its start token differs from the recorded one). Unknown counts as alive.
func ownerAlive(pid int, start string) bool {
	if pid <= 0 || pid > math.MaxInt32 {
		return false
	}
	if syscall.Kill(pid, 0) == syscall.ESRCH {
		return false
	}
	now, gone := testslot.ProcessStart(pid)
	if gone {
		return false
	}
	return start == "" || now == "" || now == start
}

// staleLock judges a lock dir: stale when its owner is not alive, or when it
// has no readable owner file and is older than ownerlessStaleAge. why names
// the holder for the log line.
func staleLock(lock string) (stale bool, why string) {
	info, err := os.Stat(lock)
	if err != nil {
		return false, ""
	}
	data, err := os.ReadFile(filepath.Join(lock, lockOwnerFile))
	if err == nil {
		var owner lockOwner
		if json.Unmarshal(data, &owner) == nil && owner.PID != 0 {
			if ownerAlive(owner.PID, owner.Start) {
				return false, ""
			}
			return true, fmt.Sprintf("pid %d", owner.PID)
		}
	}
	// No owner file (or one still being written): only the dir's age counts.
	if age := time.Since(info.ModTime()); age > ownerlessStaleAge {
		return true, fmt.Sprintf("no owner file, idle %s", age.Round(time.Second))
	}
	return false, ""
}

// breakStaleLock breaks lock once when its holder is gone and takes it. Every
// breaker serializes on a kernel lock of the runs dir (released by the kernel
// if the breaker dies) and judges the lock again under it, so a lock just
// taken by a live waiter is never broken. The stale dir is first renamed to a
// unique name, then removed. took is true when this call now holds lock.
func breakStaleLock(runs, lock string) (took bool, err error) {
	if stale, _ := staleLock(lock); !stale {
		return false, nil
	}
	dir, err := os.Open(runs)
	if err != nil {
		return false, err
	}
	defer dir.Close()
	for {
		err = syscall.Flock(int(dir.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		return false, err
	}
	defer syscall.Flock(int(dir.Fd()), syscall.LOCK_UN)
	stale, why := staleLock(lock)
	if !stale {
		return false, nil
	}
	grave := fmt.Sprintf("%s.stale-%d-%d", lock, os.Getpid(), time.Now().UnixNano())
	if err := os.Rename(lock, grave); err != nil {
		return false, nil // gone or not ours to move: keep waiting
	}
	if entries, err := os.ReadDir(grave); err == nil {
		for _, entry := range entries {
			_ = os.Remove(filepath.Join(grave, entry.Name()))
		}
	}
	_ = os.Remove(grave)
	fmt.Fprintf(os.Stderr, "code-forge: broke stale run lock (%s)\n", why)
	if err := os.Mkdir(lock, 0755); err != nil {
		return false, nil // a plain waiter took it first: wait for it
	}
	return true, nil
}
