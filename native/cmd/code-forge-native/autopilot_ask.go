package main

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B484: `autopilot ask` in Go up to the delegate session: runAsk's usage
// checks (src/cli/autopilot.mjs), askDelegate's option and subject checks
// and grantFor's refusals (src/autopilot/delegate.mjs, grant.mjs), in text
// and --json. B536: a grant that lets the question through runs the
// delegate session in Go too (autopilot_delegate.go); the shapes it does
// not serve keep the hand-over autopilot-ask.

var (
	autopilotSubjectBlock   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	autopilotSubjectFinding = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
)

// autopilotScopeCategory is SCOPE_CATEGORY (src/autopilot/scopes.mjs).
var autopilotScopeCategory = map[string]string{"waive:warning": "review", "waive:nit": "review", "round:extra": "coding", "model:choose": "coding", "review:substitute": "review"}

func autopilotJSTrim(s string) string { return strings.TrimFunc(s, autopilotJSSpace) }

// autopilotAskInput is runAsk's checks, then askDelegate's cleanOptions and
// cleanSubject, then grantFor's unknown-scope check: every refusal Node
// gives before it reads the run. It returns the trimmed scope.
func autopilotAskInput(flags autopilotFlags) (string, error) {
	scope, ok := flags.values["scope"]
	if !ok || autopilotJSTrim(scope) == "" {
		return "", autopilotUsageFail("autopilot ask needs --scope <scope>")
	}
	if question, ok := flags.values["question"]; !ok || autopilotJSTrim(question) == "" {
		return "", autopilotUsageFail("autopilot ask needs --question <text>")
	}
	text, given := flags.values["options"]
	options := autopilotItems(text)
	if given && len(options) == 0 {
		return "", autopilotUsageFail("--options needs at least 2 distinct options, e.g. --options waive,fix")
	}
	if file, ok := flags.values["context-file"]; ok {
		at := file
		if !filepath.IsAbs(at) {
			cwd, err := os.Getwd()
			if err != nil {
				return "", autopilotUsageFail("--context-file " + file + " cannot be read")
			}
			at = filepath.Join(cwd, at)
		}
		if _, err := os.ReadFile(at); err != nil {
			return "", autopilotUsageFail("--context-file " + file + " cannot be read") // the path only, never content
		}
	}
	// cleanOptions: each scrubbed (no secret is registered yet), then deduplicated
	cleaned := []string{}
	for _, option := range options {
		option = bnMaskSecrets(autopilotRedact(option))
		if len(option) > 200 {
			return "", autopilotUsageFail("delegate: an option is longer than 200 bytes")
		}
		if !slices.Contains(cleaned, option) {
			cleaned = append(cleaned, option)
		}
	}
	if len(options) > 0 && len(cleaned) < 2 {
		return "", autopilotUsageFail("delegate: give at least 2 distinct options")
	}
	if len(cleaned) > 20 {
		return "", autopilotUsageFail("delegate: at most 20 options")
	}
	if block, ok := flags.values["block"]; ok && !autopilotSubjectBlock.MatchString(block) {
		return "", autopilotUsageFail("delegate: --block must be a block id (letters, digits, . _ -; at most 32 characters)")
	}
	if file, ok := flags.values["file"]; ok && !autopilotSubjectFile(file) {
		return "", autopilotUsageFail("delegate: --file must be a repo-relative path")
	}
	if finding, ok := flags.values["finding"]; ok && !autopilotSubjectFinding.MatchString(finding) {
		return "", autopilotUsageFail("delegate: --finding must be a finding id (letters, digits, . _ : -)")
	}
	scope = autopilotJSTrim(scope)
	known := slices.Contains(autopilotAllowable, scope)
	for _, f := range autopilotFixedDeny {
		known = known || f.Key == scope
	}
	if !known {
		quoted, _ := ledger.Stringify(scope, false)
		return "", &runFail{"unknown autopilot scope " + string(quoted), true}
	}
	return scope, nil
}

// autopilotSubjectFile is cleanSubject's file check: path.posix.normalize
// is path.Clean that keeps a trailing slash.
func autopilotSubjectFile(file string) bool {
	if file == "" || len(utf16.Encode([]rune(file))) > 512 || strings.ContainsAny(file, "\\\x00") || strings.HasPrefix(file, "/") {
		return false
	}
	normalized := path.Clean(file)
	if strings.HasSuffix(file, "/") {
		normalized += "/"
	}
	return normalized != "." && normalized != ".." && !strings.HasPrefix(normalized, "../")
}

// autopilotAskRefusal is grantFor's refusal for a known scope on a grant
// read (no custom checks: the CLI passes none); "" lets the question through.
func autopilotAskRefusal(result autopilot.Result, scope string) string {
	if result.Grant == nil {
		return "no-grant"
	}
	switch result.State {
	case "run-ended", "stopped", "expired":
		return result.State
	}
	fixed := false
	for _, f := range autopilotFixedDeny {
		fixed = fixed || f.Key == scope
	}
	if fixed || roundListHas(result.Grant.Get("deny"), scope) {
		return "denied"
	}
	if !roundListHas(result.Grant.Get("scopes"), scope) {
		return "not-allowed"
	}
	if category, ok := autopilotScopeCategory[scope]; ok {
		if paused, isObject := result.Grant.Get("paused").(ledger.Object); isObject {
			for _, entry := range paused {
				if entry.Key == category {
					return "paused"
				}
			}
		}
	}
	return ""
}

// autopilotAskPlan answers the ask Go can answer on the record read once:
// an input refusal, or a grant refusal (no session; to the owner). A grant
// that lets the question through hands over: the session stays with Node.
func autopilotAskPlan(home, runID string, record ledger.Object, flags autopilotFlags, read autopilot.Result, now time.Time) (func(o *runOut) int, handoverReason) {
	scope, invalid := autopilotAskInput(flags)
	if invalid != nil {
		return func(o *runOut) int { return autopilotReport(o, "ask", invalid) }, 0
	}
	if autopilotAskRefusal(read, scope) == "" {
		// B536: the delegate session runs in Go (autopilot_delegate.go)
		ask, configErr, ok := apdPlan(record, read.Grant, flags, scope)
		if !ok {
			return nil, hAutopilotAsk
		}
		return func(o *runOut) int {
			checked, err := autopilotCheckExpiry(home, runID, now)
			if err != nil {
				return autopilotReport(o, "ask", err)
			}
			if why := autopilotAskRefusal(checked, scope); why != "" {
				return autopilotAskRefused(o, runID, flags, checked, why)
			}
			if configErr != nil {
				return autopilotReport(o, "ask", configErr)
			}
			return apdRun(o, home, runID, flags, ask, checked.Grant)
		}, 0
	}
	return func(o *runOut) int {
		// grantFor: checkExpiry writes the one expire row of a window that is over
		checked, err := autopilotCheckExpiry(home, runID, now)
		if err != nil {
			return autopilotReport(o, "ask", err)
		}
		reason := autopilotAskRefusal(checked, scope)
		if reason == "" {
			reason = autopilotAskRefusal(read, scope)
		}
		return autopilotAskRefused(o, runID, flags, checked, reason)
	}, 0
}

// autopilotAskRefused is runAsk's output for a grantFor refusal (exit 1).
func autopilotAskRefused(o *runOut, runID string, flags autopilotFlags, checked autopilot.Result, reason string) int {
	if !flags.bools["json"] {
		o.out("autopilot ask run " + runID + ": refused (" + reason + ") — no session; the question goes to the owner\n")
		return 1
	}
	var grantID any
	if checked.Grant != nil {
		grantID = checked.Grant.Get("grant_id")
	}
	data, err := ledger.Stringify(ledger.Object{
		{Key: "run", Value: runID}, {Key: "scope", Value: flags.values["scope"]}, {Key: "grant_id", Value: grantID},
		{Key: "decision_id", Value: nil}, {Key: "answered", Value: false}, {Key: "acted", Value: false},
		{Key: "to_owner", Value: true}, {Key: "reason", Value: reason}, {Key: "decision", Value: nil},
		{Key: "confidence", Value: nil}, {Key: "within_scope", Value: nil}, {Key: "escalate", Value: nil},
		{Key: "answer_reason", Value: nil},
	}, false)
	if err != nil {
		return autopilotReport(o, "ask", err)
	}
	o.out(string(data) + "\n")
	return 1
}
