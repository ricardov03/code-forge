package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
)

// The Node router (bin/code-forge.mjs) runs maybeHint (src/util/error-hint.mjs)
// after a failed verb took an errors.jsonl entry: one line on a terminal, not
// for agents or usage errors, once per fingerprint per 24 hours, remembered in
// ~/.code-forge/logs/hints.json. A verb Go serves does the same: before any
// output it asks routerHintReadable whether Go can judge the hint state, and
// after nativeVerbFailure it calls routerHint with the entry's fingerprint.

const routerHintLine = "code-forge: this error was saved to the local log. To send it to us: code-forge logs report\n"

// routerLogNow is the clock of the hint state (a test seam).
var routerLogNow = time.Now

// routerLogStderrTTY reports whether stderr is a terminal (a test seam).
var routerLogStderrTTY = func() bool { return humanask.IsTTY(os.Stderr) }

var routerHintISO = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{3}Z$`)
var routerHintFP = regexp.MustCompile(`^[0-9a-f]{12}$`)

const routerHintWindowMs = int64(24 * 60 * 60 * 1000)

// routerHintMayShow: maybeHint could print on this run (the log is on, there
// is a HOME, stderr is a terminal, no agent harness).
func routerHintMayShow() bool {
	if !reviewWaitLogging() || os.Getenv("HOME") == "" || !routerLogStderrTTY() {
		return false
	}
	for _, name := range nativeStatusAgentVars {
		if os.Getenv(name) != "" {
			return false
		}
	}
	return true
}

func routerHintFile() string {
	return filepath.Join(os.Getenv("HOME"), ".code-forge", "logs", "hints.json")
}

// routerHintState reads hints.json as maybeHint does (a missing or broken
// file is an empty state). ok is false when Go cannot judge it like Node: a
// fingerprint's time in a form only Date.parse knows, or an object Go cannot
// decode in JavaScript property order.
func routerHintState() (ledger.Object, bool) {
	state := ledger.Object{}
	data, err := os.ReadFile(routerHintFile())
	if err != nil {
		return state, true
	}
	var parsed any
	if json.Unmarshal(data, &parsed) != nil {
		return state, true
	}
	if _, isObject := parsed.(map[string]any); !isObject {
		return state, true
	}
	obj, err := ledger.DecodeObject(data)
	if err != nil {
		return nil, false
	}
	for _, field := range obj {
		if ts, ok := field.Value.(string); ok && routerHintFP.MatchString(field.Key) && !routerHintISO.MatchString(ts) {
			return nil, false
		}
	}
	return obj, true
}

// routerHintReadable is false only when a hint could show and Go cannot judge
// the hint state; the caller then hands the run to Node before any output.
func routerHintReadable() bool {
	if !routerHintMayShow() {
		return true
	}
	_, ok := routerHintState()
	return ok
}

// routerHintWithin is Node's test for a remembered time (an ISO form only).
func routerHintWithin(ts string, nowMs int64) bool {
	t, err := time.Parse("2006-01-02T15:04:05.000Z", ts)
	if err != nil {
		return false
	}
	ms := t.UnixMilli()
	return nowMs-ms < routerHintWindowMs && nowMs >= ms
}

// routerHint is maybeHint after nativeVerbFailure: fp is the logged entry's
// fingerprint ("" when nothing was logged or the entry is a warning).
func routerHint(fp string, code int) {
	if fp == "" || !routerHintFP.MatchString(fp) || code == 2 || !routerHintMayShow() {
		return
	}
	state, ok := routerHintState()
	if !ok {
		return // routerHintReadable handed such a run over before any output.
	}
	now := routerLogNow()
	nowMs := now.UnixMilli()
	if seen, ok := state.Get(fp).(string); ok && routerHintWithin(seen, nowMs) {
		return
	}
	kept := ledger.Object{}
	for _, field := range state {
		ts, ok := field.Value.(string)
		if !ok || !routerHintFP.MatchString(field.Key) || field.Key == fp || !routerHintWithin(ts, nowMs) {
			continue
		}
		kept = append(kept, field)
	}
	kept = append(kept, ledger.Field{Key: fp, Value: now.UTC().Format("2006-01-02T15:04:05.000Z")})
	data, err := ledger.Stringify(kept, false)
	if err != nil {
		return
	}
	fmt.Fprint(os.Stderr, routerHintLine)
	file := routerHintFile()
	if os.MkdirAll(filepath.Dir(file), 0700) != nil {
		return
	}
	tmp := fmt.Sprintf("%s.tmp-%d", file, os.Getpid())
	if os.WriteFile(tmp, append(data, '\n'), 0600) != nil || os.Rename(tmp, file) != nil {
		_ = os.Remove(tmp)
	}
}
