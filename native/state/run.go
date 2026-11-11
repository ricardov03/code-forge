// Package state reads and writes Node-compatible run records and signed rows.
package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
)

const lockTimeout = 5 * time.Second
const lockPoll = 25 * time.Millisecond

var runPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// Error carries the same stable code and message as Node's StateError.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func runsDir(home string) string { return filepath.Join(home, ".code-forge", "runs") }

func recordPath(home, runID string) (string, error) {
	if !runPattern.MatchString(runID) {
		return "", &Error{"bad-run-id", "run id must match /^[a-z0-9][a-z0-9-]{0,63}$/"}
	}
	return filepath.Join(runsDir(home), runID+".json"), nil
}

// lockWait is how long WithRunLock polls a held lock before it fails. Node
// and most verbs keep the 5000ms deadline; long verbs raise it (SetLockWait).
var lockWait = lockTimeout
var lockNotice func(holder string)
var lockNoticeOnce sync.Once
var lockVerbName string

// SetLockWait makes every later WithRunLock and AwaitRunLock of this process
// wait up to wait for a held run lock, and call notice once, the first time it
// finds the lock held, with a description of its holder (B393).
func SetLockWait(wait time.Duration, notice func(holder string)) {
	lockWait, lockNotice = wait, notice
}

// SetLockVerb names this process's verb in the owner file of a lock it holds.
func SetLockVerb(verb string) { lockVerbName = verb }

func lockedError(runID, lock string) error {
	return &Error{"locked", fmt.Sprintf("run %s is locked (%s); remove it if no forge verb is running", runID, lock)}
}

// lockHolder describes the process that holds lock from the owner file a Go
// verb writes beside it. Node writes none, and an owner file older than the
// lock or naming a dead process is stale.
func lockHolder(lock string) string {
	info, err := os.Stat(lock)
	if err != nil {
		return "an unknown process"
	}
	owner, err := os.Stat(lock + ".owner")
	if err != nil || owner.ModTime().Before(info.ModTime().Add(-time.Second)) {
		return "an unknown process (no owner record; a Node verb?)"
	}
	data, err := os.ReadFile(lock + ".owner")
	if err != nil {
		return "an unknown process (no owner record; a Node verb?)"
	}
	pid, verb, _ := strings.Cut(strings.TrimSpace(string(data)), " ")
	n, err := strconv.Atoi(pid)
	if err != nil || n <= 0 || syscall.Kill(n, 0) == syscall.ESRCH {
		return "an unknown process (stale owner record)"
	}
	if verb == "" {
		return "pid " + pid
	}
	return "pid " + pid + " (" + verb + ")"
}

func lockFile(home, runID string) (string, error) {
	if _, err := recordPath(home, runID); err != nil {
		return "", err
	}
	if err := os.MkdirAll(runsDir(home), 0700); err != nil {
		return "", err
	}
	return filepath.Join(runsDir(home), runID+".lock"), nil
}

// waitLock polls until try succeeds, the lock disappears error-free, or the
// wait ends with the locked error.
func waitLock(home, runID, lock string, try func() (bool, error)) error {
	deadline := time.Now().Add(lockWait)
	for {
		done, err := try()
		if err != nil || done {
			return err
		}
		took, breakErr := breakStaleLock(runsDir(home), lock)
		if breakErr != nil {
			return breakErr
		}
		if took {
			return nil
		}
		if time.Now().After(deadline) {
			return lockedError(runID, lock)
		}
		if lockNotice != nil {
			lockNoticeOnce.Do(func() { lockNotice(lockHolder(lock)) })
		}
		time.Sleep(lockPoll)
	}
}

// AwaitRunLock waits until no process holds the run lock, without taking it.
// A verb that hands the work to another process (Node) calls it first.
func AwaitRunLock(home, runID string) error {
	lock, err := lockFile(home, runID)
	if err != nil {
		return err
	}
	return waitLock(home, runID, lock, func() (bool, error) {
		_, err := os.Stat(lock)
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	})
}

// WithRunLock shares Node's mkdir lock, 5000ms deadline and 25ms polling.
func WithRunLock(home, runID string, fn func() error) (err error) {
	lock, err := lockFile(home, runID)
	if err != nil {
		return err
	}
	err = waitLock(home, runID, lock, func() (bool, error) {
		err := os.Mkdir(lock, 0755)
		if err == nil {
			return true, nil
		}
		if !os.IsExist(err) {
			return false, err
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	owner := lock + ".owner"
	wrote := os.WriteFile(owner, []byte(strconv.Itoa(os.Getpid())+" "+lockVerbName+"\n"), 0600) == nil
	defer func() {
		if wrote {
			_ = os.Remove(owner)
		}
		// Release removes owner.json first, then the lock dir (B423/B426).
		removeLockOwner(lock)
		if releaseErr := os.Remove(lock); releaseErr != nil {
			err = releaseErr
		}
	}()
	if err := writeLockOwner(lock); err != nil {
		return err
	}
	return fn()
}

// ReadRun reads the authoritative record, preserving every object's key order.
func ReadRun(home, runID string) (ledger.Object, error) {
	file, err := recordPath(home, runID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil, &Error{"no-run", fmt.Sprintf("no run record for %s", runID)}
	}
	if err != nil {
		return nil, err
	}
	return ledger.DecodeObject(data)
}

// SaveRun atomically replaces the private authoritative record, then its mirror
// without worker. A read-modify-write goes through UpdateRun (B426).
func SaveRun(home string, record ledger.Object) error {
	runID, _ := record.Get("run_id").(string)
	file, err := recordPath(home, runID)
	if err != nil {
		return err
	}
	workspace, ok := record.Get("workspace").(string)
	if !ok {
		return fmt.Errorf("run workspace must be a string")
	}
	// Node canonicalizes existing paths before testing whether the workspace
	// contains the authoritative runs directory (which would erase worker).
	ws, err := canonical(workspace)
	if err != nil {
		return err
	}
	h, err := canonical(home)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(ws, runsDir(h))
	if err != nil {
		return err
	}
	if !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel) {
		return &Error{"workspace-contains-runs-dir", fmt.Sprintf("the workspace %s contains %s — run code-forge from a project directory, not $HOME or above", ws, runsDir(home))}
	}
	data, err := ledger.Stringify(record, true)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(runsDir(home), 0700); err != nil {
		return err
	}
	// A unique exclusive temp also guarantees 0600 if a stale Node temp exists.
	tmp, err := os.CreateTemp(runsDir(home), runID+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(append(data, '\n'))
	closeErr := tmp.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), file); err != nil {
		return err
	}
	mirror := record.Clone()
	mirror.Delete("worker")
	data, err = ledger.Stringify(mirror, true)
	if err != nil {
		return err
	}
	mirrorFile := filepath.Join(workspace, ".code-forge", "runs", runID+".json")
	if err := os.MkdirAll(filepath.Dir(mirrorFile), 0755); err != nil {
		return err
	}
	mirrorTmp := mirrorFile + ".tmp"
	if err := os.WriteFile(mirrorTmp, append(data, '\n'), 0666); err != nil {
		return err
	}
	if err := os.Rename(mirrorTmp, mirrorFile); err != nil {
		_ = os.Remove(mirrorTmp)
		return err
	}
	return nil
}

func canonical(file string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(file); err == nil {
		return filepath.Abs(resolved)
	}
	return filepath.Abs(file)
}

// WriteSigned signs an ordered row with the run key and appends to the project's
// ledger. The caller may override run, matching Node's {run: runID, ...row}.
func WriteSigned(home, runID string, row ledger.Object) (ledger.Object, error) {
	file, err := recordPath(home, runID)
	if err != nil {
		return nil, err
	}
	keyFile := strings.TrimSuffix(file, ".json") + ".key"
	info, err := os.Stat(keyFile)
	if os.IsNotExist(err) {
		return nil, &Error{"no-key", fmt.Sprintf("run %s has no signer key", runID)}
	}
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm()&0077 != 0 {
		return nil, &Error{"bad-key-mode", fmt.Sprintf("run %s signer key is accessible to other users (mode %o); expected 600", runID, info.Mode().Perm())}
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, &Error{"bad-key", fmt.Sprintf("run %s signer key is not 32 bytes", runID)}
	}
	stamped := ledger.Object{{Key: "run", Value: runID}}
	for _, field := range row {
		stamped.Set(field.Key, field.Value)
	}
	signed, err := sign.SignRow(stamped.Map(), key)
	if err != nil {
		return nil, err
	}
	stamped.Set("ts", signed["ts"])
	stamped.Delete("mac")
	stamped.Set("mac", signed["mac"])
	record, err := ReadRun(home, runID)
	if err != nil {
		return nil, err
	}
	slug, _ := record.Get("project").(string)
	return ledger.AppendObject(filepath.Join(home, ".code-forge", "ledger"), slug, stamped, ledger.DefaultRotateAtBytes)
}
