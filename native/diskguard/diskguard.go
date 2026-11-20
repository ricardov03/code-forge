// Package diskguard keeps land, gates and run start off a full disk (B961,
// the product form of the B733 disk-guard.sh): under twice the minimum it
// trims Go build cache files unused for 24 h, and under the minimum it
// refuses with the next step.
package diskguard

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// DefaultMinGB is land.min_free_gb when the config does not set it.
const DefaultMinGB = 15

// TrimAge is how long a Go cache file must be unused before a trim removes
// it. Go touches an entry's mtime when it uses it, so running builds keep
// theirs.
const TrimAge = 24 * time.Hour

// TestFreeEnv, when set to a number, is the free space in GB that Free
// reports instead of asking the file system. Test binaries set it so a test
// never depends on the machine's disk.
const TestFreeEnv = "CODE_FORGE_TEST_DISK_FREE_GB"

const gb = 1000 * 1000 * 1000

// Free is the space available to this user on the file system that holds
// path, in bytes.
func Free(path string) (uint64, error) {
	if raw := os.Getenv(TestFreeEnv); raw != "" {
		if v, err := strconv.ParseFloat(raw, 64); err == nil && v >= 0 {
			return uint64(v * gb), nil
		}
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return uint64(st.Bavail) * uint64(st.Bsize), nil
}

// TrimCache removes the regular files under dir whose mtime is older than
// age, and returns how many it removed and their size. A missing dir trims
// nothing.
func TrimCache(dir string, age time.Duration, now time.Time) (files int, bytes int64) {
	if dir == "" {
		return 0, 0
	}
	cut := now.Add(-age)
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil || !info.ModTime().Before(cut) {
			return nil
		}
		if os.Remove(path) == nil {
			files++
			bytes += info.Size()
		}
		return nil
	})
	return files, bytes
}

// Result is one disk check.
type Result struct {
	Path    string
	FreeGB  float64
	MinGB   float64
	Trimmed int   // Go cache files the check removed
	Freed   int64 // their size in bytes
	OK      bool
}

// Check measures the free space on path. Under 2 x minGB it trims cache (the
// Go build cache dir, read only then; "" or an error skips the trim) and
// measures again. OK is false under minGB. minGB 0 turns the check off.
func Check(path string, minGB float64, cache func() string, now time.Time) (Result, error) {
	r := Result{Path: path, MinGB: minGB, OK: true}
	if minGB <= 0 {
		return r, nil
	}
	free, err := Free(path)
	if err != nil {
		return r, err
	}
	if float64(free) < 2*minGB*gb && cache != nil {
		r.Trimmed, r.Freed = TrimCache(cache(), TrimAge, now)
		if r.Trimmed > 0 {
			if free, err = Free(path); err != nil {
				return r, err
			}
		}
	}
	r.FreeGB = float64(free) / gb
	r.OK = float64(free) >= minGB*gb
	return r, nil
}

// TrimLine is the note for a trim that ran; "" when none did.
func (r Result) TrimLine() string {
	if r.Trimmed == 0 {
		return ""
	}
	return fmt.Sprintf("disk guard: %.1f GB free, under 2 x %g GB; removed %d Go cache files unused for 24 h (%.1f GB)", r.FreeGB, r.MinGB, r.Trimmed, float64(r.Freed)/gb)
}

// Refusal is the refusal text with the next step; "" when the check passed.
func (r Result) Refusal() string {
	if r.OK {
		return ""
	}
	return fmt.Sprintf("disk guard: only %.1f GB free on %s, under land.min_free_gb (%g GB), after removing %d Go cache files unused for 24 h; free disk space (go clean -cache, remove old worktrees and build output) and run it again, or lower land.min_free_gb in .code-forge.yml", r.FreeGB, r.Path, r.MinGB, r.Trimmed)
}
