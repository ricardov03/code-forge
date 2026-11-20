package diskguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cacheWith makes a cache dir with one file unused for 48 h and one fresh.
func cacheWith(t *testing.T, now time.Time) (dir, old, fresh string) {
	t.Helper()
	dir = t.TempDir()
	old, fresh = filepath.Join(dir, "ab", "old-a"), filepath.Join(dir, "cd", "fresh-d")
	for _, p := range []string{old, fresh} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(strings.Repeat("x", 1000)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stale := now.Add(-48 * time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatal(err)
	}
	return dir, old, fresh
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func TestB961CheckTrimsAndRefuses(t *testing.T) {
	now := time.Now()
	cases := []struct {
		free      string
		min       float64
		trim, ok  bool
		refusalOK bool
	}{
		{"40", 15, false, true, true}, // room: nothing trimmed
		{"20", 15, true, true, true},  // under 2 x: trim, still passes
		{"10", 15, true, false, false},
		{"1", 0, false, true, true}, // 0 turns the guard off
	}
	for _, c := range cases {
		t.Run(c.free, func(t *testing.T) {
			t.Setenv(TestFreeEnv, c.free)
			dir, old, fresh := cacheWith(t, now)
			r, err := Check(t.TempDir(), c.min, func() string { return dir }, now)
			if err != nil {
				t.Fatal(err)
			}
			if r.OK != c.ok {
				t.Errorf("OK=%v, want %v", r.OK, c.ok)
			}
			if exists(old) == c.trim {
				t.Errorf("old cache file kept=%v, want trimmed=%v", exists(old), c.trim)
			}
			if !exists(fresh) {
				t.Errorf("a cache file used within 24 h was removed")
			}
			if c.trim && (r.Trimmed != 1 || r.Freed != 1000 || !strings.Contains(r.TrimLine(), "removed 1 Go cache files unused for 24 h")) {
				t.Errorf("trim result=%+v line=%q, want 1 file, 1000 bytes", r, r.TrimLine())
			}
			refusal := r.Refusal()
			if (refusal == "") != c.refusalOK {
				t.Errorf("refusal=%q, want refusal=%v", refusal, !c.refusalOK)
			}
			if !c.refusalOK {
				for _, want := range []string{"only 10.0 GB free", "land.min_free_gb (15 GB)", "go clean -cache", "lower land.min_free_gb"} {
					if !strings.Contains(refusal, want) {
						t.Errorf("refusal %q must name %q", refusal, want)
					}
				}
			}
		})
	}
	// The real file system answers when no test value is set.
	t.Setenv(TestFreeEnv, "")
	if free, err := Free(t.TempDir()); err != nil || free == 0 {
		t.Errorf("Free on the real disk = %d, %v", free, err)
	}
}
