package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/runstatus"
	"github.com/ricardov03/code-forge/native/state"
)

const statusUsage = "usage: code-forge status [--run <id>] [--json]\n"

func statusQuote(s string) string { b, _ := ledger.Stringify(s, false); return string(b) }
func statusFlags(args []string) (run string, jsonFlag bool, message string) {
	seen := map[string]bool{}
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			positionals = append(positionals, a)
			continue
		}
		name, inline, eq := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		if name != "run" && name != "json" {
			return "", false, "unknown flag " + statusQuote(a)
		}
		if seen[name] {
			return "", false, "--" + name + " given more than once"
		}
		seen[name] = true
		if name == "json" {
			if eq {
				return "", false, "--json takes no value"
			}
			jsonFlag = true
			continue
		}
		if eq {
			if inline == "" {
				return "", false, "--run needs a value"
			}
			run = inline
		} else {
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				return "", false, "--run needs a value"
			}
			i++
			run = args[i]
		}
	}
	if len(positionals) > 0 {
		return "", false, "unexpected argument " + statusQuote(positionals[0])
	}
	// Empty non-inline values are accepted by parseFlags, then rejected by readRun.
	if seen["run"] && run == "" {
		return "", jsonFlag, "bad-run-id"
	}
	return
}

// Compute explicitly sorts block ids; the renderer constructs ordered summaries.
// Source object insertion order is therefore not needed by this map-based input.
func statusMap(record ledger.Object) (map[string]any, error) {
	b, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}
func statusReal(path string) string {
	if p, e := filepath.EvalSymlinks(path); e == nil {
		path = p
	}
	p, _ := filepath.Abs(path)
	return p
}

// directStatus stages all streams before emitting anything, so unsupported state
// can hand over without duplicate summaries or warnings. It never takes a lock.
func directStatus(args []string) (int, bool) {
	run, jsonFlag, message := statusFlags(args)
	if message != "" {
		if message == "bad-run-id" {
			fmt.Fprintln(os.Stderr, "status: run id must match /^[a-z0-9][a-z0-9-]{0,63}$/")
			return 1, true
		}
		fmt.Fprint(os.Stderr, "status: "+message+"\n"+statusUsage)
		return 2, true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return 0, false
	}
	records := []map[string]any{}
	if run != "" {
		r, err := state.ReadRun(home, run)
		if err != nil {
			var e *state.Error
			if errors.As(err, &e) && (e.Code == "no-run" || e.Code == "bad-run-id") {
				fmt.Fprintln(os.Stderr, "status:", e.Message)
				return 1, true
			}
			return 0, false
		}
		m, err := statusMap(r)
		if err != nil {
			return 0, false
		}
		records = append(records, m)
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return 0, false
		}
		want := statusReal(config.ProjectRootFor(cwd))
		names, err := os.ReadDir(filepath.Join(home, ".code-forge", "runs"))
		if err == nil {
			for _, n := range names {
				if !strings.HasSuffix(n.Name(), ".json") {
					continue
				}
				r, e := state.ReadRun(home, strings.TrimSuffix(n.Name(), ".json"))
				if e != nil {
					continue
				}
				m, err := statusMap(r)
				if err != nil {
					return 0, false
				}
				w, ok := m["workspace"].(string)
				if ok && m["status"] != "ended" && statusReal(w) == want {
					records = append(records, m)
				}
			}
		}
		sort.SliceStable(records, func(i, j int) bool {
			a, _ := records[i]["started_at"].(string)
			b, _ := records[j]["started_at"].(string)
			if a != b {
				return a > b
			}
			return fmt.Sprint(records[i]["run_id"]) > fmt.Sprint(records[j]["run_id"])
		})
	}
	summaries := []any{}
	texts := []string{}
	warnings := []string{}
	for _, record := range records {
		slug, ok := record["project"].(string)
		if !ok {
			return 0, false
		}
		rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
		if err != nil {
			var numeric *json.UnmarshalTypeError
			if errors.As(err, &numeric) {
				return 0, false
			}
			rows = nil
		}
		summary, ok := runstatus.Compute(record, rows, home)
		if !ok {
			return 0, false
		}
		summaries = append(summaries, summary)
		if !jsonFlag {
			text := renderStatus(summary, statusEndHint(record))
			if id, _ := record["run_id"].(string); id != "" {
				for _, line := range statusQueueLines(home, id) {
					text += "\n" + line
				}
			}
			texts = append(texts, text)
		}
		workerWarnings, ok := statusWorkerWarnings(record)
		if !ok {
			return 0, false
		}
		warnings = append(warnings, workerWarnings...)
	}
	var output string
	if jsonFlag {
		b, err := ledger.Stringify(ledger.Object{{Key: "runs", Value: summaries}}, false)
		if err != nil {
			return 0, false
		}
		output = string(b) + "\n"
	} else if len(texts) == 0 {
		output = "no active run\n"
	} else {
		output = strings.Join(texts, "\n\n") + "\n"
	}
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, w)
	}
	if _, err := fmt.Fprint(os.Stdout, output); err != nil {
		return 1, true
	}
	return 0, true
}

func statusString(v any) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprint(v)
}
func statusMoney(v any) string {
	// Go and ECMAScript both round the exact binary float to the nearest decimal,
	// but JS toFixed breaks ties away from zero. The rational formatter below
	// handles the halfway cases (e.g. 0.125) too.
	n, _ := v.(float64)
	return "$" + statusFixed(n)
}
func statusFixed(n float64) string {
	if math.Abs(n) >= 1e21 {
		b, _ := ledger.Stringify(n, false)
		return string(b)
	}
	negative := n < 0
	n = math.Abs(n)
	r := new(big.Rat).SetFloat64(n)
	r.Mul(r, big.NewRat(100, 1))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(r.Num(), r.Denom(), rem)
	if new(big.Int).Lsh(rem, 1).Cmp(r.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	s := q.String()
	for len(s) < 3 {
		s = "0" + s
	}
	s = s[:len(s)-2] + "." + s[len(s)-2:]
	if negative {
		s = "-" + s
	}
	return s
}
func renderStatus(s ledger.Object, hint string) string {
	spent := "$?"
	if s.Get("spent_usd") != nil {
		spent = statusMoney(s.Get("spent_usd"))
		if n, ok := s.Get("unknown_usd_sessions").(int); ok && n > 0 {
			spent += "+?"
		}
	}
	line := fmt.Sprintf("run %v · %s · %v · spent %s", s.Get("run_id"), statusString(s.Get("status")), s.Get("mode"), spent)
	if s.Get("budget_usd") != nil {
		line += " of " + statusMoney(s.Get("budget_usd"))
	}
	lines := []string{line}
	closed, _ := s.Get("closed").(int)
	stopped, _ := s.Get("stopped").(int)
	tally := ""
	if closed+stopped > 0 {
		tally = fmt.Sprintf("closed %d · stopped %d", closed, stopped)
	}
	awaiting := ""
	if a, ok := s.Get("awaiting_human_check").([]string); ok && len(a) > 0 {
		b, _ := ledger.Stringify(a, false)
		awaiting = "awaiting_human_check: " + string(b)
	}
	flaky := ""
	if a, ok := s.Get("flaky").([]string); ok && len(a) > 0 {
		flaky = "flaky: " + strings.Join(a, ", ")
	}
	room := 9
	for _, v := range []string{tally, awaiting, flaky, hint} {
		if v != "" {
			room--
		}
	}
	blocks := s.Get("blocks").([]any)
	shown := len(blocks)
	if shown > room {
		shown = room - 1
	}
	for _, v := range blocks[:shown] {
		b := v.(ledger.Object)
		g := b.Get("gate").(ledger.Object)
		gate := fmt.Sprint(g.Get("result"))
		if name, ok := g.Get("name").(string); ok && name != "" {
			gate += " (" + name + ")"
		}
		lines = append(lines, fmt.Sprintf("%v %v %s · %s · gate %s · findings %v", b.Get("id"), b.Get("state"), statusString(b.Get("level")), statusString(b.Get("model")), gate, b.Get("findings")))
	}
	if shown < len(blocks) {
		lines = append(lines, fmt.Sprintf("… +%d more blocks", len(blocks)-shown))
	}
	for _, v := range []string{awaiting, tally, flaky, hint} {
		if v != "" {
			lines = append(lines, v)
		}
	}
	return strings.Join(lines, "\n")
}

func statusGit(cwd string, args ...string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = cwd
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "GIT_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	b, err := cmd.Output()
	return strings.TrimSpace(string(b)), err == nil
}
func statusEndHint(record map[string]any) string {
	if record["status"] != "active" {
		return ""
	}
	blocks, _ := record["blocks"].(map[string]any)
	open := 0
	for _, v := range blocks {
		b, _ := v.(map[string]any)
		if b["status"] != "open" {
			continue
		}
		open++
		w, _ := b["worktree"].(map[string]any)
		branch, _ := w["branch"].(string)
		base, _ := b["base_branch"].(string)
		if branch == "" || base == "" {
			return ""
		}
		cwd, _ := record["workspace"].(string)
		if _, ok := statusGit(cwd, "merge-base", "--is-ancestor", "refs/heads/"+branch, "refs/heads/"+base); !ok {
			return ""
		}
	}
	if open == 0 {
		return ""
	}
	return fmt.Sprintf("all open blocks are merged: end the run with: code-forge run end --run %v", record["run_id"])
}

func statusStart(pid float64) string {
	if pid <= 0 || pid != float64(int(pid)) {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(int(pid)))
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.Join(strings.Fields(string(b)), " ")
}
func statusPin(pin map[string]any) (bool, string) {
	pid, ok := pin["pid"].(float64)
	if !ok || pid <= 1 || pid != float64(int(pid)) {
		return false, "replaced"
	}
	start := statusStart(pid)
	if start == "" {
		return false, "down"
	}
	at, _ := pin["started_at"].(string)
	return start == strings.Join(strings.Fields(at), " "), "replaced"
}
func statusWorkerWarnings(record map[string]any) ([]string, bool) {
	if record["status"] == "ended" {
		return nil, true
	}
	workspace, _ := record["workspace"].(string)
	root, ok := statusGit(workspace, "rev-parse", "--show-toplevel")
	if !ok {
		return nil, true
	}
	entries, err := os.ReadDir(filepath.Join(root, ".code-forge", "workers"))
	if err != nil {
		return nil, true
	}
	pins, _ := record["workers"].([]any)
	if record["workers"] != nil && pins == nil {
		return nil, false
	}
	if record["workers"] == nil {
		if p, ok := record["worker"].(map[string]any); ok {
			pins = []any{p}
		}
	}
	for _, v := range pins {
		pin, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		if _, ok := pin["started_at"].(string); !ok {
			return nil, false
		}
		if _, ok := pin["pid"].(float64); !ok {
			return nil, false
		}
	}
	unpinned := []map[string]any{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, ".code-forge", "workers", e.Name()))
		if err != nil {
			continue
		}
		var w map[string]any
		if json.Unmarshal(b, &w) != nil {
			continue
		}
		pid, pok := w["pid"].(float64)
		start, sok := w["start_time"].(string)
		rid, rok := w["run"].(string)
		started, _ := w["started_at"].(string)
		beat, _ := w["heartbeat_at"].(string)
		if !pok || pid <= 1 || pid != float64(int(pid)) || !sok || start == "" || strings.ContainsAny(start, "/\\") || !rok || rid == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339Nano, started); err != nil {
			return nil, false
		}
		at, err := time.Parse(time.RFC3339Nano, beat)
		if err != nil {
			return nil, false
		}
		if e.Name() != fmt.Sprintf("%.0f-%s.json", pid, start) || rid != record["run_id"] {
			continue
		}
		if err := syscall.Kill(int(pid), 0); err != nil && !errors.Is(err, syscall.EPERM) {
			continue
		}
		age := time.Since(at)
		if !(age <= time.Minute && age >= -5*time.Second) && start != "unknown" && statusStart(pid) != start {
			continue
		}
		pinned := false
		for _, v := range pins {
			p, _ := v.(map[string]any)
			s, _ := p["started_at"].(string)
			if p["pid"] == pid && strings.Join(strings.Fields(s), " ") == strings.Join(strings.Fields(start), " ") {
				pinned = true
			}
		}
		if !pinned {
			unpinned = append(unpinned, w)
		}
	}
	suffix := ""
	if len(unpinned) == 1 && len(pins) > 0 {
		good := false
		reason := "down"
		if len(pins) > 1 {
			for _, v := range pins {
				p, _ := v.(map[string]any)
				if ok, r := statusPin(p); ok {
					good = true
				} else if r == "replaced" {
					pid, _ := p["pid"].(float64)
					if pid <= 1 || pid != float64(int(pid)) {
						good = false
						reason = "replaced"
						break
					}
				}
			}
		} else {
			// Match Node's checkWorkerPin: a single pin probes legacy worker,
			// even when workers[0] is absent from or differs from that field.
			p, _ := record["worker"].(map[string]any)
			good, reason = statusPin(p)
			if p == nil {
				reason = "down"
			}
		}
		if !good {
			p, _ := pins[0].(map[string]any)
			pid, _ := ledger.Stringify(p["pid"], false)
			suffix = fmt.Sprintf(" (pinned pid %s is %s)", pid, reason)
		}
	}
	warnings := []string{}
	for _, w := range unpinned {
		s := fmt.Sprintf("warning: live worker %.0f is not pinned: run start --reattach --run %v", w["pid"], record["run_id"])
		if len(unpinned) > 1 {
			s += fmt.Sprintf(" --worker-pid %.0f", w["pid"])
		} else {
			s += suffix
		}
		warnings = append(warnings, s)
	}
	return warnings, true
}
