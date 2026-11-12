package main

import (
	"errors"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
)

// B478: `autopilot round` in Go (runAction in src/cli/autopilot.mjs and
// grantExtraRound in src/autopilot/actions.mjs): the delegate's acted
// decision buys one extra fix round for a file, written as a signed
// autopilot.extra_round row.

const roundScope = "round:extra"

// roundDecisionMaxAge is DECISION_MAX_AGE_MS (src/review/gate-check.mjs).
const roundDecisionMaxAge = 30 * 60 * 1000

var roundDecisionID = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

// jsObjectProtoNames are the names `record.blocks?.[block]` finds on
// Object.prototype when the record has no such block.
var jsObjectProtoNames = []string{"constructor", "__defineGetter__", "__defineSetter__", "hasOwnProperty", "__lookupGetter__", "__lookupSetter__", "isPrototypeOf", "propertyIsEnumerable", "toString", "valueOf", "__proto__", "toLocaleString"}

// jsTrimEmpty is `s.trim().length === 0`: JavaScript's white space and line
// terminators (Go's unicode.IsSpace also takes U+0085, JavaScript does not).
func jsTrimEmpty(s string) bool {
	return strings.TrimFunc(s, func(r rune) bool { return r == '\uFEFF' || r != '\u0085' && unicode.IsSpace(r) }) == ""
}

// roundUsage is the usage checks of runAction's need() and grantExtraRound,
// in their order; file is the normalised path.
func roundUsage(flags autopilotFlags) (file string, err error) {
	for _, name := range []string{"block", "file", "decision"} {
		if value, ok := flags.values[name]; !ok || jsTrimEmpty(value) {
			return "", autopilotUsageFail("autopilot round needs --" + name)
		}
	}
	if !roundDecisionID.MatchString(flags.values["decision"]) {
		return "", autopilotUsageFail("autopilot round needs --decision <id> (the delegate's acted decision from autopilot ask)")
	}
	file, ok, _ := roundRepoPath(flags.values["file"])
	if !ok {
		return "", autopilotUsageFail("--file must be a repo-relative path")
	}
	return file, nil
}

// roundRepoPath is repoRelativePath for the paths path.Clean writes the way
// path.posix.normalize does; known=false for the others (a trailing slash, a
// doubled slash, a dot segment).
func roundRepoPath(raw string) (file string, ok, known bool) {
	if raw == "" || strings.HasPrefix(raw, "/") || strings.ContainsAny(raw, "\\\x00") {
		return "", false, true
	}
	if path.Clean(raw) != raw {
		return "", false, false
	}
	if raw == "." || raw == ".." || strings.HasPrefix(raw, "../") {
		return "", false, true
	}
	return raw, true, true
}

// roundSupported says whether Go reads this round the way Node does: no
// block name Node finds on Object.prototype, a path Go normalises the same,
// a readable key and a named decision row Go compares the same.
func roundSupported(home, runID string, record ledger.Object, flags autopilotFlags, rows []ledger.Row) bool {
	block := flags.values["block"]
	if blocks, ok := record.Get("blocks").(ledger.Object); slices.Contains(jsObjectProtoNames, block) && (!ok || blocks.Get(block) == nil) {
		return false
	}
	if raw, has := flags.values["file"]; has {
		if _, _, known := roundRepoPath(raw); !known {
			return false
		}
	}
	if _, err := rebaseKey(home, runID); errors.Is(err, errRebaseUnported) {
		return false
	}
	if row := roundDecisionRow(rows, runID, flags.values["decision"]); row != nil {
		for _, key := range []string{"scope", "decision"} {
			switch row[key].(type) {
			case map[string]any, []any:
				return false // String() / JSON.stringify of an object keeps Node's key order
			}
		}
		if ts, ok := row["ts"].(string); ok {
			if _, _, known := bnDateMs(ts); !known {
				return false
			}
		}
	}
	return true
}

// roundDecisionRow is the first autopilot.decision row of the run with the id.
func roundDecisionRow(rows []ledger.Row, runID, id string) map[string]any {
	for _, value := range rows {
		if row, ok := value.(map[string]any); ok && row["run"] == runID && row["event"] == "autopilot.decision" && row["decision_id"] == id {
			return row
		}
	}
	return nil
}

func roundGrantRefusal(reason string) error {
	return &roundRefused{"the grant refuses (" + reason + "); the question goes to the owner"}
}

type roundRefused struct{ message string }

func (r *roundRefused) Error() string { return r.message }

// roundGrantFor is grantFor(runId, scope, now) for round:extra and
// model:choose (both in the coding budget category).
func roundGrantFor(home, runID, scope string, now time.Time) (ledger.Object, error) {
	checked, err := autopilotCheckExpiry(home, runID, now)
	if err != nil {
		return nil, err
	}
	if checked.Grant == nil {
		return nil, roundGrantRefusal("no-grant")
	}
	switch checked.State {
	case "run-ended", "stopped", "expired":
		return nil, roundGrantRefusal(checked.State)
	}
	if refusal := roundScopeRefusal(checked.Grant, scope); refusal != "" {
		return nil, roundGrantRefusal(refusal)
	}
	if paused, ok := checked.Grant.Get("paused").(ledger.Object); ok {
		for _, entry := range paused {
			if entry.Key == "coding" {
				return nil, roundGrantRefusal("paused")
			}
		}
	}
	return checked.Grant, nil
}

func roundScopeRefusal(grant ledger.Object, scope string) string {
	if roundListHas(grant.Get("deny"), scope) {
		return "denied"
	}
	if !roundListHas(grant.Get("scopes"), scope) {
		return "not-allowed"
	}
	return ""
}

func roundListHas(list any, want string) bool {
	items, _ := list.([]any)
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// roundLiveRefusal is liveGrantRefusal: the record as it is now, inside the
// run lock.
func roundLiveRefusal(home, runID, grantID, scope string, now time.Time) error {
	record, err := state.ReadRun(home, runID)
	if err != nil {
		return err
	}
	grant, _ := record.Get("autopilot").(ledger.Object)
	reason := ""
	switch {
	case grant == nil || grant.Get("grant_id") != grantID:
		reason = "no-grant"
	case record.Get("status") != "active":
		reason = "run-ended"
	default:
		stateName, err := autopilot.GrantState(grant, now)
		if err != nil {
			return err
		}
		if stateName != "active" {
			reason = stateName
		} else {
			reason = roundScopeRefusal(grant, scope)
		}
	}
	if reason == "" {
		return nil
	}
	return roundGrantRefusal(reason)
}

// roundDecisionProblem is decisionProblem; file "" leaves the file out of
// the subject (a level names only the block).
func roundDecisionProblem(rows []map[string]any, id, grantID, scope, decision, block, file string, at int64, key []byte) string {
	var row map[string]any
	for _, r := range rows {
		if r["event"] == "autopilot.decision" && r["decision_id"] == id {
			row = r
			break
		}
	}
	if row == nil {
		return "no autopilot.decision row " + id + " in this run"
	}
	if verified, err := sign.VerifyRow(row, key); err != nil || !verified.OK {
		return "decision " + id + " has no valid MAC"
	}
	if row["acted"] != true {
		return "decision " + id + " did not act (it went to the owner)"
	}
	if row["grant_id"] != grantID {
		return "decision " + id + " is under another grant"
	}
	if row["scope"] != scope {
		return "decision " + id + " is for " + jsString(roundValue(row, "scope")) + ", not " + scope
	}
	if row["decision"] != decision {
		decided := "undefined"
		if value, has := row["decision"]; has {
			text, _ := ledger.Stringify(value, false)
			decided = string(text)
		}
		want, _ := ledger.Stringify(decision, false)
		return "decision " + id + " decided " + decided + ", not " + string(want)
	}
	named, _ := row["subject"].(map[string]any)
	if named["block"] != block {
		return "decision " + id + " is about another block"
	}
	if file != "" && named["file"] != file {
		return "decision " + id + " is about another file"
	}
	ts, _ := row["ts"].(string)
	ms, ok, _ := bnDateMs(ts)
	if !ok || ms > at || at-ms > roundDecisionMaxAge {
		return "decision " + id + " is older than 30 min (or not yet made)"
	}
	return ""
}

// roundValue is row[key], undefined when the key is missing.
func roundValue(row map[string]any, key string) any {
	if value, has := row[key]; has {
		return value
	}
	return jsUndefined{}
}

// roundRunRows is runRowsOf: the run's object rows.
func roundRunRows(home, project, runID string) ([]map[string]any, error) {
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), project)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, value := range rows {
		if row, ok := value.(map[string]any); ok && row["run"] == runID {
			out = append(out, row)
		}
	}
	return out, nil
}

// autopilotRound is runAction('round').
func autopilotRound(o *runOut, home, runID string, flags autopilotFlags) int {
	report := func(err error) int {
		var refused *roundRefused
		if errors.As(err, &refused) {
			o.err("autopilot round: refused — " + refused.message + "\n")
			return 1
		}
		return autopilotReport(o, "round", err)
	}
	clock := func() time.Time { return autopilotNow().Truncate(time.Millisecond) }
	file, err := roundUsage(flags)
	if err != nil {
		return report(err)
	}
	block, decisionID := flags.values["block"], flags.values["decision"]
	record, err := state.ReadRun(home, runID)
	if err != nil {
		return report(err)
	}
	if blocks, _ := record.Get("blocks").(ledger.Object); blocks == nil || !runTruthy(blocks.Get(block)) {
		return report(&state.Error{Code: "unknown_block", Message: "block " + block + " is not in run " + runID})
	}
	grant, err := roundGrantFor(home, runID, roundScope, clock())
	if err != nil {
		return report(err)
	}
	grantID, _ := grant.Get("grant_id").(string)
	key, err := rebaseKey(home, runID)
	if err != nil {
		return report(err)
	}
	autopilotRegisterKey(home, runID) // loadKey registers the key
	project, _ := record.Get("project").(string)
	recordRun, _ := record.Get("run_id").(string)
	var row ledger.Object
	err = state.WithRunLock(home, runID, func() error {
		fresh := clock()
		if err := roundLiveRefusal(home, runID, grantID, roundScope, fresh); err != nil {
			return err
		}
		rows, err := roundRunRows(home, project, recordRun)
		if err != nil {
			return err
		}
		if problem := roundDecisionProblem(rows, decisionID, grantID, roundScope, "allow", block, file, fresh.UnixMilli(), key); problem != "" {
			return &roundRefused{problem + "; the question goes to the owner (autopilot ask, then --decision <id>)"}
		}
		for _, r := range rows {
			if r["event"] == "autopilot.extra_round" && r["block"] == block && r["file"] == file && r["grant_id"] == grantID {
				if verified, err := sign.VerifyRow(r, key); err == nil && verified.OK {
					return &roundRefused{file + " already had its extra round under grant " + grantID}
				}
			}
		}
		row, err = autopilotWriteSigned(home, runID, ledger.Object{{Key: "event", Value: "autopilot.extra_round"}, {Key: "block", Value: block}, {Key: "file", Value: file}, {Key: "grant_id", Value: grantID}, {Key: "decision_id", Value: decisionID}, {Key: "ts", Value: autopilotStamp(fresh)}})
		return err
	})
	if err != nil {
		return report(err)
	}
	o.out("autopilot round run " + runID + ": one extra fix round for " + jsString(row.Get("file")) + " in block " + jsString(row.Get("block")) + " (grant " + jsString(row.Get("grant_id")) + ")\n")
	return 0
}
