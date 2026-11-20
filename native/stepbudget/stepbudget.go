// Package stepbudget gives each slow step of a run (land, gate, coder, CI
// wait) a speed budget (B950, F018 part 2). Every finished step records its
// wall time in a per-project history; the budget is the median of the
// earlier samples plus a margin (autopilot.step_budgets.margin, default
// 50%), or a fixed number of minutes from the config. A step over its
// budget is reported by the caller: a WARN line, a ledger row and a found
// block.
package stepbudget

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/filelock"
)

// Steps are the step names a budget covers.
var Steps = []string{"land", "gate", "coder", "ci-wait"}

const (
	// DefaultMargin is the share added to the median (0.5 = 50%).
	DefaultMargin = 0.5
	// DefaultMinSamples is how many earlier samples a measured budget needs.
	DefaultMinSamples = 5
	// Keep is how many samples per step the history holds (newest kept).
	Keep = 50
)

// Config is the budget rule: the margin over the median, the samples a
// measured budget needs, and fixed budgets (milliseconds) per step.
type Config struct {
	Margin     float64
	MinSamples int
	FixedMs    map[string]int64
}

// DefaultConfig is the rule with no config keys set.
func DefaultConfig() Config {
	return Config{Margin: DefaultMargin, MinSamples: DefaultMinSamples, FixedMs: map[string]int64{}}
}

// Verdict is one finished step judged against its budget. Source is
// "config" (a fixed budget), "measured" (median plus margin) or "" when the
// step has no budget yet (too few samples).
type Verdict struct {
	Step     string `json:"step"`
	Ms       int64  `json:"ms"`
	BudgetMs int64  `json:"budget_ms"`
	MedianMs int64  `json:"median_ms"`
	Samples  int    `json:"samples"`
	Source   string `json:"source"`
	Over     bool   `json:"over"`
}

// Known reports a step name a budget covers.
func Known(step string) bool { return slices.Contains(Steps, step) }

// Median is the middle sample (the mean of the two middle ones, rounded).
func Median(samples []int64) int64 {
	if len(samples) == 0 {
		return 0
	}
	s := slices.Clone(samples)
	slices.Sort(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return int64(math.Round(float64(s[n/2-1]+s[n/2]) / 2))
}

// Judge compares ms with the budget the history (earlier samples only) and
// the config give.
func Judge(step string, ms int64, history []int64, cfg Config) Verdict {
	v := Verdict{Step: step, Ms: ms, Samples: len(history), MedianMs: Median(history)}
	if fixed, ok := cfg.FixedMs[step]; ok && fixed > 0 {
		v.BudgetMs, v.Source = fixed, "config"
	} else {
		need := cfg.MinSamples
		if need < 1 {
			need = DefaultMinSamples
		}
		if len(history) < need {
			return v
		}
		margin := cfg.Margin
		if margin < 0 || math.IsNaN(margin) || math.IsInf(margin, 0) {
			margin = DefaultMargin
		}
		v.BudgetMs, v.Source = int64(math.Round(float64(v.MedianMs)*(1+margin))), "measured"
	}
	v.Over = ms > v.BudgetMs
	return v
}

// History is one project's samples per step, oldest first.
type History struct {
	Workspace string             `json:"workspace"`
	Steps     map[string][]int64 `json:"steps"`
}

func key(workspace string) string {
	sum := sha256.Sum256([]byte(workspace))
	return hex.EncodeToString(sum[:8])
}

// Path is the project's history file under home.
func Path(home, workspace string) string {
	return filepath.Join(home, ".code-forge", "stepbudget", key(workspace)+".json")
}

// Read loads the project's history (empty when there is none yet; a file
// that does not parse counts as empty and is rewritten on the next Observe).
func Read(home, workspace string) History {
	h := History{Workspace: workspace, Steps: map[string][]int64{}}
	data, err := os.ReadFile(Path(home, workspace))
	if err != nil {
		return h
	}
	var read History
	if json.Unmarshal(data, &read) != nil || read.Steps == nil {
		return h
	}
	read.Workspace = workspace
	return read
}

// Observe judges a finished step against the project's history, then adds
// its sample (the newest Keep stay), under a lock.
func Observe(home, workspace, step string, ms int64, cfg Config) (Verdict, error) {
	if !Known(step) {
		return Verdict{}, fmt.Errorf("unknown step %q", step)
	}
	if ms < 0 {
		return Verdict{}, errors.New("a step time cannot be negative")
	}
	var v Verdict
	err := filelock.WithLock("stepbudget:"+key(workspace), func() error {
		h := Read(home, workspace)
		samples := h.Steps[step]
		v = Judge(step, ms, samples, cfg)
		samples = append(samples, ms)
		if len(samples) > Keep {
			samples = samples[len(samples)-Keep:]
		}
		h.Steps[step] = samples
		data, err := json.Marshal(h)
		if err != nil {
			return err
		}
		file := Path(home, workspace)
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return err
		}
		tmp := file + ".tmp-" + strconv.Itoa(os.Getpid())
		if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
			return err
		}
		return os.Rename(tmp, file)
	}, filelock.Options{})
	return v, err
}

// Elapsed is the wall time from start to now() in whole milliseconds (0 when
// the clock went back).
func Elapsed(start time.Time, now func() time.Time) int64 {
	if ms := now().Sub(start).Milliseconds(); ms > 0 {
		return ms
	}
	return 0
}

// Basis words where the budget came from.
func (v Verdict) Basis() string {
	if v.Source == "config" {
		return "autopilot.step_budgets." + strings.ReplaceAll(v.Step, "-", "_")
	}
	return "median " + Duration(v.MedianMs) + " of " + strconv.Itoa(v.Samples) + " runs + margin"
}

// Warning is the WARN line of a step over its budget ("" when it is not).
func (v Verdict) Warning(block string) string {
	if !v.Over {
		return ""
	}
	where := ""
	if block != "" {
		where = " (block " + block + ")"
	}
	return fmt.Sprintf("WARN step budget: %s took %s, over its budget %s (%s)%s\n", v.Step, Duration(v.Ms), Duration(v.BudgetMs), v.Basis(), where)
}

// Sink receives what a step over its budget produces: the WARN line, the
// ledger row and the found block.
type Sink struct {
	Warn  func(line string)
	Row   func(v Verdict)
	Found func(v Verdict)
}

// Report hands a step over its budget to the sink, in that order; a step
// within its budget (or with none yet) produces nothing.
func Report(v Verdict, block string, sink Sink) {
	if !v.Over {
		return
	}
	if sink.Warn != nil {
		sink.Warn(v.Warning(block))
	}
	if sink.Row != nil {
		sink.Row(v)
	}
	if sink.Found != nil {
		sink.Found(v)
	}
}

// Duration words a time in milliseconds for a person: 850ms, 42s, 12m30s, 1h05m.
func Duration(ms int64) string {
	switch {
	case ms < 1000:
		return strconv.FormatInt(ms, 10) + "ms"
	case ms < 60_000:
		return strconv.FormatInt(int64(math.Round(float64(ms)/1000)), 10) + "s"
	case ms < 3_600_000:
		s := int64(math.Round(float64(ms) / 1000))
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	}
	m := int64(math.Round(float64(ms) / 60_000))
	return fmt.Sprintf("%dh%02dm", m/60, m%60)
}
