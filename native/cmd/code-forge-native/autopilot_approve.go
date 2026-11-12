package main

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B484: `autopilot approve` in Go up to the owner's terminal prompt:
// validateApproval (src/autopilot/limits.mjs) and the no-terminal refusal of
// runApprove (src/cli/autopilot.mjs). At a terminal the prompt and the
// .code-forge.yml change stay with Node (hand-over autopilot-approve).

const autopilotApproveNoTTY = "autopilot approve: no terminal to confirm; only the owner approves a change, in a terminal (approve has no --yes)"

// autopilotImmutableKeys is IMMUTABLE_KEYS (src/state/config-snapshot.mjs),
// in its order.
var autopilotImmutableKeys = ledger.Object{
	{Key: "version", Value: "the config format is fixed for the run"},
	{Key: "project.slug", Value: "the ledger file of the run is chosen at run start"},
	{Key: "engine", Value: "the run engine is fixed at run start"},
	{Key: "tmp.root", Value: "the worker's run temp root is created at run start"},
	{Key: "keys", Value: "the worker resolves keys once, at start"},
	{Key: "system1.key", Value: "the worker resolves the Jev key once, at start"},
	{Key: "review.external", Value: "the outside reviewer is fixed by the owner before the run starts"},
	{Key: "review.agent_waivers.require_ack", Value: "human acknowledgement of agent waivers is fixed by the owner before the run starts"},
	{Key: "proof.isolation", Value: "every block of the run is isolated the same way"},
	{Key: "proof.worktree", Value: "the worktrees and branches of the open blocks were made with it"},
	{Key: "context.require_coder_tests", Value: "whether close needs the coder's green test run is fixed by the owner before the run starts"},
	{Key: "hooks.on_block_end", Value: "it runs a command on the owner's machine at every block end; fixed by the owner before the run starts"},
	{Key: "proof.runners", Value: "the commands that prove a block are fixed by the owner before the run starts"},
	{Key: "coder.sandbox", Value: "the coder sandbox widens what the coder may write; fixed by the owner before the run starts"},
}

var autopilotKeySegment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// autopilotKeysOverlap: equal, or one under the other.
func autopilotKeysOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+".") || strings.HasPrefix(b, a+".")
}

// autopilotApproveInput is validateApproval's input checks, in its order:
// key, fixed keys, the JSON value, --until. The record checks follow.
func autopilotApproveInput(flags autopilotFlags, now time.Time) (string, error) {
	key, ok := flags.values["key"]
	if !ok || key == "" {
		return "", autopilotUsageFail("--key is required: a dotted config key path, e.g. review.max_rounds_per_file")
	}
	segs := strings.Split(key, ".")
	for _, seg := range segs {
		if !autopilotKeySegment.MatchString(seg) {
			return "", autopilotUsageFail("--key must be a dotted path of plain segments (letters, digits, _ and -), e.g. review.max_rounds_per_file")
		}
	}
	for _, seg := range segs {
		if seg == "__proto__" || seg == "constructor" || seg == "prototype" {
			return "", autopilotUsageFail("--key must not contain __proto__, constructor or prototype")
		}
	}
	for _, fixed := range autopilotImmutableKeys {
		if autopilotKeysOverlap(key, fixed.Key) {
			why, _ := fixed.Value.(string)
			return "", &state.Error{Code: "immutable-key", Message: key + " cannot be approved: " + fixed.Key + " cannot change mid-run (" + why + ")"}
		}
	}
	value, ok := flags.values["value"]
	if !ok {
		return "", autopilotUsageFail(`--value is required: a JSON value, e.g. 4, true, "text" or {"a": 1}`)
	}
	if !json.Valid([]byte(value)) {
		return "", autopilotUsageFail(`--value must be JSON, e.g. 4, true, "text" or {"a": 1}`) // never the value itself
	}
	untilText, ok := flags.values["until"]
	if !ok {
		return "", autopilotUsageFail("--until is required: an ISO-8601 time with an offset, e.g. " + autopilotUntilExample)
	}
	ms, ok := autopilotUntilMs(untilText)
	if !ok {
		return "", autopilotUsageFail("--until must be an ISO-8601 time with an offset (Z or ±hh:mm), e.g. " + autopilotUntilExample)
	}
	if ms <= now.UnixMilli() {
		return "", autopilotUsageFail("--until is in the past; give a time after now, at most 24 h ahead")
	}
	if ms-now.UnixMilli() > 24*60*60*1000 {
		return "", autopilotUsageFail("--until is more than 24 h ahead; a grant lasts at most 24 h")
	}
	return key, nil
}

// autopilotApprovePlan is the rest of validateApproval on the record read
// once (an active run, no active approval on an overlapping key), then the
// terminal check, or the reason Node must run it: an active approval
// whose key or until Go cannot read the way Node does, or a terminal (the
// owner's prompt and the change stay with Node).
func autopilotApprovePlan(runID string, record ledger.Object, flags autopilotFlags, now time.Time) (func(o *runOut) int, handoverReason) {
	key, invalid := autopilotApproveInput(flags, now)
	if invalid != nil {
		return func(o *runOut) int { return autopilotReport(o, "approve", invalid) }, 0
	}
	if record.Get("status") != "active" {
		return func(o *runOut) int {
			return autopilotReport(o, "approve", &state.Error{Code: "run-ended", Message: "run " + runID + " has ended"})
		}, 0
	}
	values, _ := record.Get("autopilot_approvals").([]any)
	for _, value := range values {
		a, isObject := value.(ledger.Object)
		if !isObject || a.Get("status") != "active" {
			continue
		}
		clashKey, keyText := a.Get("key").(string)
		if !keyText {
			return nil, hAutopilotShape // overlaps() throws on a key that is not text
		}
		if !autopilotKeysOverlap(clashKey, key) {
			continue
		}
		until, untilText := a.Get("until").(string)
		if !untilText {
			return nil, hAutopilotShape
		}
		return func(o *runOut) int {
			return autopilotReport(o, "approve", &state.Error{Code: "approval-active", Message: "an approval for " + clashKey + " is active until " + until + "; it must end first"})
		}, 0
	}
	if autopilotIsTTY() {
		return nil, hAutopilotApprove // the owner's prompt and the change stay with Node
	}
	return func(o *runOut) int { o.err(autopilotApproveNoTTY + "\n"); return 2 }, 0
}
