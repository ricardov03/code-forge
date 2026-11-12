package worker

// B489: the idle exit of loop.mjs checkIdle (src/cli/sweep.mjs idleExitHours
// and runActivityAt). Every read failure is an error: unknown activity is not
// proof of idleness, so the caller keeps the worker alive.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// IdleExitHours is sweep.mjs idleExitHours: worker.idle_exit_hours when it is
// a finite number >= 0, else 24; 0 turns the idle exit off.
func IdleExitHours(cfg map[string]any) float64 {
	w, _ := cfg["worker"].(map[string]any)
	if h, ok := w["idle_exit_hours"].(float64); ok && !math.IsNaN(h) && !math.IsInf(h, 0) && h >= 0 {
		return h
	}
	return 24
}

// ActivityDeps are runActivityAt's seams.
type ActivityDeps struct {
	NowMs    float64
	RepoRoot string
	// ReadRows is the project ledger (rows of other runs are skipped).
	ReadRows func() ([]any, error)
	// Git runs one git command in dir; nil = git with Node's child env.
	Git func(dir string, args ...string) (string, error)
	// Trees keeps each tree's fingerprint between checks.
	Trees map[string]string
	// Ignored are the poisoned tickets (never served by this worker).
	Ignored func(id string) bool
}

var queueTicketName = regexp.MustCompile(`^[a-f0-9]{24}\.json$`)

func notFound(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, errNotDir)
}

// parseTime is Date.parse for the ISO forms the ledger and run record hold;
// anything else is NaN (ok false).
func parseTime(v any) (float64, bool) {
	s, ok := v.(string)
	if !ok {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, false
	}
	return float64(t.UnixMilli()), true
}

func msOf(t time.Time) float64 { return float64(t.UnixNano()) / 1e6 }

// gitChildEnv is runActivityAt's env: the process env without GIT_*, with a
// fixed git config and no prompts.
func gitChildEnv() []string {
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
}

func runGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gitChildEnv()
	out, err := cmd.Output()
	return string(out), err
}

// RunActivityAt is sweep.mjs runActivityAt for a worker (its repo root is
// known): now while a ticket of the run waits, else the latest of the run's
// start, its ledger rows and its open blocks' trees (now when a tree's
// fingerprint changed since the last check).
func RunActivityAt(record ledger.Object, runID string, d ActivityDeps) (float64, error) {
	now := d.NowMs
	git := d.Git
	if git == nil {
		git = runGit
	}
	queue := QueueDir(d.RepoRoot)
	entries, err := os.ReadDir(queue)
	if err != nil && !notFound(err) {
		return 0, err
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	for _, e := range entries {
		name := e.Name()
		if !queueTicketName.MatchString(name) || names[name[:24]+".done"] || (d.Ignored != nil && d.Ignored(name[:24])) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(queue, name))
		if err != nil {
			return 0, err
		}
		var t any
		if err := json.Unmarshal(data, &t); err != nil {
			return 0, err
		}
		switch v := t.(type) {
		case nil:
			return 0, errors.New("ticket is null")
		case map[string]any:
			if v["run"] == runID {
				return now, nil
			}
		}
	}
	latest, ok := parseTime(record.Get("started_at"))
	if !ok {
		return now, nil
	}
	if d.ReadRows != nil {
		rows, err := d.ReadRows()
		if err != nil {
			return 0, err
		}
		for _, r := range rows {
			row, isObject := r.(map[string]any)
			if !isObject {
				if r == nil {
					return 0, errors.New("ledger row is null")
				}
				continue
			}
			if row["run"] != runID {
				continue
			}
			at, ok := parseTime(row["ts"])
			if !ok {
				at = now
			}
			latest = math.Max(latest, at)
		}
	}
	var blocks []any
	switch b := record.Get("blocks").(type) {
	case nil:
	case ledger.Object:
		for _, f := range b {
			blocks = append(blocks, f.Value)
		}
	case []any:
		blocks = b
	default:
		return 0, errors.New("blocks is not an object")
	}
	for _, raw := range blocks {
		entry, isObject := raw.(ledger.Object)
		if !isObject {
			return 0, errors.New("block entry is not an object")
		}
		if entry.Get("status") != "open" {
			continue
		}
		tree := d.RepoRoot
		var wt ledger.Object
		if w, ok := entry.Get("worktree").(ledger.Object); ok {
			wt = w
			if p, ok := w.Get("path").(string); ok {
				tree = p
			} else if w.Get("path") != nil {
				return 0, errors.New("worktree path is not a string")
			}
		} else if entry.Get("worktree") != nil {
			return 0, errors.New("worktree is not an object")
		}
		info, err := os.Stat(tree)
		if err != nil {
			if wt != nil && notFound(err) {
				continue
			}
			return 0, err
		}
		latest = math.Max(latest, msOf(info.ModTime()))
		listed, err := git(tree, "ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", ".", ":!.code-forge")
		if err != nil {
			return 0, fmt.Errorf("idle worktree check unavailable")
		}
		head, err := git(tree, "log", "-1", "--format=%H:%ct")
		if err != nil {
			return 0, fmt.Errorf("idle worktree check unavailable")
		}
		fingerprint := []string{head}
		if parts := strings.Split(strings.TrimSpace(head), ":"); len(parts) > 1 {
			if n, err := strconv.ParseFloat(parts[1], 64); err == nil {
				latest = math.Max(latest, n*1000)
			}
		}
		dirs := map[string]bool{tree: true}
		for _, file := range strings.Split(listed, "\x00") {
			if file == "" {
				continue
			}
			full := filepath.Join(tree, file)
			for parent := filepath.Dir(full); parent != tree && strings.HasPrefix(parent, tree+string(filepath.Separator)); parent = filepath.Dir(parent) {
				dirs[parent] = true
			}
			st, err := os.Stat(full)
			if err != nil {
				if !notFound(err) {
					return 0, err
				}
				fingerprint = append(fingerprint, file+":missing")
				continue
			}
			m, c := msOf(st.ModTime()), ctimeMs(st)
			latest = math.Max(latest, math.Max(m, c))
			fingerprint = append(fingerprint, fmt.Sprintf("%s:%v:%v:%d", file, m, c, st.Size()))
		}
		for dir := range dirs {
			st, err := os.Stat(dir)
			if err != nil {
				if !notFound(err) {
					return 0, err
				}
				continue
			}
			latest = math.Max(latest, msOf(st.ModTime()))
		}
		stamp := strings.Join(fingerprint, "\x00")
		if d.Trees != nil {
			if previous, seen := d.Trees[tree]; seen && previous != stamp {
				latest = now
			}
			d.Trees[tree] = stamp
		}
	}
	return latest, nil
}
