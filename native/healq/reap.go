package healq

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/ricardov03/code-forge/native/filelock"
)

var lockedPath = regexp.MustCompile(`is locked \(([^)]+)\)`)

// OwnerFile is the owner record a lock directory may hold: JSON with pid and
// start (or started_at), the `ps -o lstart=` start time of that pid.
const OwnerFile = "owner.json"

// Reap removes the lock named in a failure text ("... is locked (<path>)")
// when its owner pid is proven dead, and says what it did. It only touches a
// path under <home>/.code-forge. A lock without a readable owner (pid) is kept:
// nothing proves it stale. A lock directory is proven by its owner.json; a
// lock file by its own JSON body (the ledger and file locks write pid + start).
func Reap(home, detail string) (path, outcome string) {
	m := lockedPath.FindStringSubmatch(detail)
	if m == nil {
		return "", "no lock path"
	}
	path = filepath.Clean(m[1])
	root := filepath.Join(home, ".code-forge") + string(filepath.Separator)
	if !filepath.IsAbs(path) || !strings.HasPrefix(path, root) {
		return path, "kept: outside " + root
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return path, "gone"
	}
	if err != nil {
		return path, "kept: " + err.Error()
	}
	owner := path
	if info.IsDir() {
		owner = filepath.Join(path, OwnerFile)
	} else if !info.Mode().IsRegular() {
		return path, "kept: not a file or directory"
	}
	seen, err := os.ReadFile(owner)
	if err != nil {
		return path, "kept: no owner pid"
	}
	pid, start, ok := ownerOf(seen)
	if !ok {
		return path, "kept: no owner pid"
	}
	if !Dead(pid, start) {
		return path, "kept: owner alive"
	}
	// The owner bytes must be the ones proven dead (a new owner may have taken
	// a lock file in between).
	if current, err := os.ReadFile(owner); err != nil || !bytes.Equal(current, seen) {
		return path, "kept: owner changed"
	}
	if err := os.Remove(owner); err != nil {
		return path, "kept: " + err.Error()
	}
	if info.IsDir() {
		if err := os.Remove(path); err != nil {
			return path, "kept: " + err.Error()
		}
	}
	return path, "removed"
}

func ownerOf(data []byte) (int, string, bool) {
	var owner struct {
		PID       float64 `json:"pid"`
		Start     string  `json:"start"`
		StartedAt string  `json:"started_at"`
	}
	if json.Unmarshal(data, &owner) != nil || owner.PID <= 1 || owner.PID > 1<<31-1 || owner.PID != float64(int(owner.PID)) {
		return 0, "", false
	}
	start := owner.Start
	if start == "" {
		start = owner.StartedAt
	}
	return int(owner.PID), strings.Join(strings.Fields(start), " "), true
}

// Dead is true when pid no longer exists, or exists with another start time
// (the pid was reused). An unknown start time keeps the owner alive.
func Dead(pid int, start string) bool {
	if pid <= 1 {
		return false
	}
	if syscall.Kill(pid, 0) == syscall.ESRCH {
		return true
	}
	if start == "" {
		return false
	}
	actual := filelock.ProcessStartTime(pid)
	return actual != nil && *actual != start
}
