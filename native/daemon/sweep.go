package daemon

import (
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/ricardov03/code-forge/native/sign"
)

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
		if row["event"] != "run.end" || runs[run] || !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`).MatchString(run) {
			continue
		}
		key := s.sweepKey(run)
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
	for run := range runs {
		s.queue.PruneRun(run)
	}
}

func (s *server) sweepKey(run string) []byte {
	file := filepath.Join(s.home, ".code-forge", "runs", run+".key")
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		return nil
	}
	key, err := os.ReadFile(file)
	if err != nil || len(key) != 32 {
		return nil
	}
	return key
}
