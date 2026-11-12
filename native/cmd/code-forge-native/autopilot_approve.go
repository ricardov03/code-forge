package main

import (
	"encoding/json"

	"github.com/ricardov03/code-forge/native/humanask"
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

// autopilotApproveRecord is the record half of validateApproval (and of
// approveChange under the lock): an active run, no active approval on an
// overlapping key; the reason Node must run it for an active approval whose
// key or until Go cannot read the way Node does.
func autopilotApproveRecord(runID string, record ledger.Object, key string) (error, handoverReason) {
	if record.Get("status") != "active" {
		return &state.Error{Code: "run-ended", Message: "run " + runID + " has ended"}, 0
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
		return &state.Error{Code: "approval-active", Message: "an approval for " + clashKey + " is active until " + until + "; it must end first"}, 0
	}
	return nil, 0
}

// autopilotApprovePlan is the rest of validateApproval on the record read
// once, then the terminal check. At a terminal (B490) the owner's prompt
// runs in Go when approveChange would refuse after it without a write (no
// config, an unreadable one, or the key already holding the value); a
// change of .code-forge.yml (the YAML write and the run reload) keeps the
// command on Node, which asks the owner.
func autopilotApprovePlan(home, runID string, record ledger.Object, flags autopilotFlags, now time.Time) (func(o *runOut) int, handoverReason) {
	key, invalid := autopilotApproveInput(flags, now)
	if invalid != nil {
		return func(o *runOut) int { return autopilotReport(o, "approve", invalid) }, 0
	}
	if refusal, reason := autopilotApproveRecord(runID, record, key); reason != 0 {
		return nil, reason
	} else if refusal != nil {
		return func(o *runOut) int { return autopilotReport(o, "approve", refusal) }, 0
	}
	if !autopilotIsTTY() {
		return func(o *runOut) int { o.err(autopilotApproveNoTTY + "\n"); return 2 }, 0
	}
	value, ok := autopilotJSONValue(flags.values["value"])
	if !ok || !autopilotPlainPrompt(key, runID, value) {
		return nil, hAutopilotApproveValue
	}
	if _, reason := autopilotApproveChange(record, key, value, hAutopilotApproveWrite); reason != 0 {
		return nil, reason
	}
	if !autopilotPromptable() {
		return nil, hAutopilotPrompt // stdin is a terminal but stdout is not: clack's output stays with Node
	}
	return func(o *runOut) int { return autopilotApprove(o, home, runID, flags, key, value) }, 0
}

// autopilotPlainPrompt: the confirm message is printable ASCII, the text
// clack's wrap measures one column per character. Other text (wide or
// zero-width characters, Unicode normalization) stays with Node.
func autopilotPlainPrompt(key, runID string, value any) bool {
	shown, err := ledger.Stringify(autopilotRedactValue(value), false)
	if err != nil {
		return false
	}
	for _, b := range []byte(key + runID + string(shown)) {
		if b < 0x20 || b > 0x7e {
			return false
		}
	}
	return true
}

// autopilotLoneSurrogate: a \uD800-\uDFFF escape, which JSON.parse keeps as a
// lone UTF-16 unit and Go cannot hold in a string.
var autopilotLoneSurrogate = regexp.MustCompile(`\\u[dD][89a-fA-F][0-9a-fA-F]{2}`)

// autopilotJSONValue is JSON.parse of --value (already valid JSON), with
// JSON.parse's property order.
func autopilotJSONValue(text string) (any, bool) {
	if autopilotLoneSurrogate.MatchString(text) {
		return nil, false
	}
	obj, err := ledger.DecodeObject([]byte(`{"v":` + text + `}`))
	if err != nil {
		return nil, false
	}
	return obj.Get("v"), true
}

// autopilotApproveChange is approveChange's read of the workspace config:
// Node's refusal, or write (the reason passed in) when the file would
// change: that write and the reload stay with Node.
func autopilotApproveChange(record ledger.Object, key string, value any, write handoverReason) (error, handoverReason) {
	workspace, _ := record.Get("workspace").(string)
	js, refusal, ok := autopilotReadWorkspace(workspace)
	if !ok {
		return nil, hAutopilotApproveConfig
	}
	if refusal != nil {
		return refusal, 0
	}
	if current, present := autopilotValueAt(js, strings.Split(key, ".")); present && autopilotSameJSON(current, value) {
		return &state.Error{Code: "no-change", Message: key + " already has that value in .code-forge.yml; nothing to approve"}, 0
	}
	return nil, write
}

// autopilotApprove is runApprove at a terminal: the owner's confirm with
// Node's message, the cancel text, validateApproval again at the fresh
// clock, then approveChange's checks under the approvals lock.
func autopilotApprove(o *runOut, home, runID string, flags autopilotFlags, key string, value any) int {
	ms, _ := autopilotUntilMs(flags.values["until"])
	shown, err := ledger.Stringify(autopilotRedactValue(value), false)
	if err != nil {
		return autopilotReport(o, "approve", err)
	}
	answer, err := autopilotConfirm("Approve " + key + " = " + string(shown) + " for run " + runID + " until " + autopilotStamp(time.UnixMilli(ms)) + "? The old value comes back then.")
	if err != nil {
		o.err("autopilot approve: " + err.Error() + "\n") // Node's catch: a failed prompt is exit 1
		return 1
	}
	if answer != humanask.Confirmed {
		o.out("cancelled — nothing changed\n")
		return 1
	}
	// the prompt may have waited: --until is checked again against now
	now := autopilotNow().Truncate(time.Millisecond)
	if _, invalid := autopilotApproveInput(flags, now); invalid != nil {
		return autopilotReport(o, "approve", invalid)
	}
	check := func() (error, handoverReason) {
		record, err := state.ReadRun(home, runID)
		if err != nil {
			return err, 0
		}
		if refusal, reason := autopilotApproveRecord(runID, record, key); refusal != nil || reason != 0 {
			return refusal, reason
		}
		return nil, 0
	}
	if refusal, reason := check(); reason != 0 {
		autopilotLate = reason
		return 0
	} else if refusal != nil {
		return autopilotReport(o, "approve", refusal)
	}
	var late handoverReason
	err = autopilotWithApprovalsLock(home, runID, func() error {
		refusal, reason := check()
		if reason != 0 || refusal != nil {
			late = reason
			return refusal
		}
		record, err := state.ReadRun(home, runID)
		if err != nil {
			return err
		}
		refusal, late = autopilotApproveChange(record, key, value, hAutopilotApproveChanged)
		return refusal
	})
	if late != 0 && err == nil {
		autopilotLate = late // the file changed during the prompt: Node asks again and writes it
		return 0
	}
	return autopilotReport(o, "approve", err)
}
