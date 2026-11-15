package daemon

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/sign"
)

// sweepRunPattern is the run id shape a run.end row must have to be pruned.
// It is compiled once: B749 measured one compile per run.end row.
var sweepRunPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// sweep is called under the server lock after refreshing the tail index. It is
// best effort: an unreadable key or invalid signature never authorizes deletion.
func (s *server) sweep(now time.Time) {
	if !s.lastSweep.IsZero() && now.Sub(s.lastSweep) < time.Minute {
		return
	}
	s.lastSweep = now
	grace := 10 * time.Minute
	if value, err := time.ParseDuration(os.Getenv("CODE_FORGE_PRUNE_GRACE")); err == nil && value >= 0 {
		grace = value
	}
	blocks := make(map[string]bool)
	runs := make(map[string]bool)
	currentKey := s.sweepKey(s.run)
	// keys holds each ended run's key file read once per sweep (nil: none or
	// unusable), so a run with many run.end rows is not read again per row.
	// listed names the runs with a key file (one directory read); a run not
	// listed has no key, so its rows cost no file system call. A directory
	// that cannot be listed falls back to reading each key.
	keys := make(map[string][]byte)
	listed := s.sweepKeyNames()
	for _, value := range s.rows {
		row := mapping(value)
		ts, err := time.Parse(time.RFC3339Nano, text(row, "ts"))
		run := text(row, "run")
		if run == s.run {
			if len(currentKey) == 32 && (row["event"] == "block.close" || row["event"] == "block.open") {
				verified, verifyErr := sign.VerifyRow(row, currentKey)
				if verifyErr == nil && verified.OK {
					blocks[text(row, "block")] = row["event"] == "block.close" && row["status"] == "complete" && err == nil && now.Sub(ts) >= grace
				}
			}
			continue
		}
		if err != nil || now.Sub(ts) < grace {
			continue
		}
		if row["event"] != "run.end" || runs[run] || !sweepRunPattern.MatchString(run) {
			continue
		}
		key, read := keys[run]
		if !read && listed != nil && !listed[run] {
			key, read = nil, true
			keys[run] = nil
		}
		if !read {
			key = s.sweepKey(run)
			keys[run] = key
		}
		if len(key) != 32 {
			continue
		}
		verified, err := sign.VerifyRow(row, key)
		if err == nil && verified.OK {
			runs[run] = true
		}
	}
	for block, eligible := range blocks {
		// The explicit path also verifies the latest signed open/close state.
		if eligible {
			s.prune(block)
		}
	}
	// B755: PruneRun refreshes the whole queue (one directory read and one
	// stat per ticket) even for a run with no ticket, which was half of a
	// sweep over 100k ended runs; only runs that hold a ticket are pruned.
	held := s.queue.TicketRuns()
	for run := range runs {
		if held[run] {
			s.queue.PruneRun(run)
		}
	}
}

// sweepKeyEntry is a key file read by an earlier sweep and the stamp of the
// file when it was read (B755).
type sweepKeyEntry struct {
	stamp string
	key   []byte
}

// sweepKey returns run's 32-byte key, or nil when the file is missing, open
// to group or others, or not 32 bytes. B755: a key read by an earlier sweep
// is reused while the file's stamp (size, mtime, ctime, mode, device, inode)
// is the same; any change to the file, its mode or its identity reads it
// again, so the cache never hides a change.
func (s *server) sweepKey(run string) []byte {
	file := filepath.Join(s.home, ".code-forge", "runs", run+".key")
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		delete(s.sweepKeys, run)
		return nil
	}
	stamp := sweepStamp(info)
	if cached, ok := s.sweepKeys[run]; ok && cached.stamp == stamp {
		sign.KeyLoaded(cached.key) // loadKey registers the key (B710)
		return cached.key
	}
	delete(s.sweepKeys, run)
	key, err := os.ReadFile(file)
	if err != nil || len(key) != 32 {
		return nil
	}
	sign.KeyLoaded(key) // loadKey registers the key (B710)
	if s.sweepKeys == nil {
		s.sweepKeys = make(map[string]sweepKeyEntry)
	}
	s.sweepKeys[run] = sweepKeyEntry{stamp: stamp, key: key}
	return key
}

// sweepKeyNames lists the runs directory once and returns the run ids that
// have a "<run>.key" entry, or nil when the directory cannot be listed.
func (s *server) sweepKeyNames() map[string]bool {
	entries, err := os.ReadDir(filepath.Join(s.home, ".code-forge", "runs"))
	if err != nil {
		return nil
	}
	names := make(map[string]bool)
	for _, entry := range entries {
		if run, ok := strings.CutSuffix(entry.Name(), ".key"); ok {
			names[run] = true
		}
	}
	return names
}
