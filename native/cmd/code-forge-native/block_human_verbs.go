package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/blockopen"
	"github.com/ricardov03/code-forge/native/blockops"
	"github.com/ricardov03/code-forge/native/blockstate"
	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/state"
)

// B453: the human-only block verbs Node's block.mjs serves after a terminal
// question (accept-hit, amend-acceptance, unwaive, ack-waivers, the human
// waive) and attempt. The terminal seams let tests answer the question.
var (
	blockAskAvailable = humanask.Available
	blockAskConfirm   = humanask.Confirm
)

func init() {
	blockSubs["accept-hit"] = runBlockAcceptHit
	blockSubs["amend-acceptance"] = runBlockAmendAcceptance
	blockSubs["unwaive"] = func(args []string) int { return runBlockAgentWaiver("unwaive", args) }
	blockSubs["ack-waivers"] = func(args []string) int { return runBlockAgentWaiver("ack-waivers", args) }
	blockSubs["attempt"] = runBlockAttempt
}

// humanVerb holds one invocation. Before mark() nothing irreversible has
// happened (no output, breadcrumb, expiry row or question), so an unported
// case may still go to Node; after it, every error is printed here.
type humanVerb struct {
	sub                 string
	args                []string
	crumbName, crumbSub string
	noYes               string
	marked              bool
	before              []any
	json                bool
}

func newHumanVerb(sub string, args []string) *humanVerb {
	h := &humanVerb{sub: sub, args: args, crumbName: "block", noYes: "there is no --yes"}
	switch sub {
	case "attempt":
		h.crumbName, h.crumbSub = "block attempt", "attempt"
	case "waive":
		h.crumbName, h.crumbSub = "block waive", "waive"
		h.noYes = "--yes works only for a recorded warning or nit finding, with evidence"
	}
	return h
}

func (h *humanVerb) original() []string { return append([]string{h.sub}, h.args...) }
func (h *humanVerb) mark() {
	if !h.marked {
		h.marked = true
		h.before = gatesBreadcrumbName(h.crumbName)
	}
}
func (h *humanVerb) fallback() int {
	return blockHandover(h.original(), hBlockUnportedSubcommand)
}
func (h *humanVerb) text(code int, text string) int {
	if !h.marked && (!closeTextSafe(text) || reviewWaitLogging() && (strings.ContainsFunc(text, func(r rune) bool { return r > 127 }) || h.sub == "waive" && strings.Contains(text, "'"))) {
		return h.fallback()
	}
	h.mark()
	fmt.Fprint(os.Stderr, text)
	nativeVerbFailure("block", h.crumbSub, h.original(), code, strings.TrimSpace(text), h.before)
	return code
}
func (h *humanVerb) fail(err error) int {
	var se *state.Error
	if errors.As(err, &se) {
		code := 1
		if se.Code == "usage" {
			code = 2
		}
		return h.text(code, fmt.Sprintf("block %s: %s\n", h.sub, se.Message))
	}
	if !h.marked {
		return h.fallback()
	}
	return h.text(1, fmt.Sprintf("block %s: %s\n", h.sub, blockIOMessage(err)))
}
func (h *humanVerb) refuse(code, message string) int {
	return h.fail(&state.Error{Code: code, Message: message})
}
func (h *humanVerb) usage(message string) int { return h.refuse("usage", message) }

// ask is block.mjs confirmAtTerminal: the terminal check, then before (its
// refusals print as errors), then the question. -1 means confirmed.
func (h *humanVerb) ask(id, message string, before func() int) int {
	noTTY := fmt.Sprintf("block %s needs the human at a terminal to confirm; nothing was recorded (%s)\n", h.sub, h.noYes)
	if !blockAskAvailable() {
		return h.text(2, noTTY)
	}
	if before != nil {
		if code := before(); code >= 0 {
			return code
		}
	}
	h.mark()
	result, err := blockAskConfirm(message)
	if result == humanask.NoTTY {
		return h.text(2, noTTY)
	}
	if err != nil || result != humanask.Confirmed {
		return h.text(1, fmt.Sprintf("block %s %s: cancelled — nothing was recorded\n", id, h.sub))
	}
	return -1
}

func (h *humanVerb) emit(value ledger.Object) {
	data, _ := ledger.Stringify(value, false)
	fmt.Fprintln(os.Stdout, string(data))
}
func (h *humanVerb) say(text string) {
	if !h.json {
		fmt.Fprint(os.Stdout, text)
	}
}

// start is the shared head of block.mjs: help, the flag parse, the block id and
// --run, the amend-acceptance form and the close-only --remove-worktree.
func (h *humanVerb) start(form, help string) (flags map[string]any, pos []string, run, id string, code int) {
	for _, arg := range h.args {
		if arg == "--help" || arg == "-h" {
			fmt.Fprint(os.Stdout, blockVerbUsage(h.sub, form)+help)
			return nil, nil, "", "", 0
		}
	}
	flags, pos, message := blockFlags(h.args)
	if message != "" {
		return nil, nil, "", "", h.usage(message)
	}
	h.json = flags["json"] == true
	run = blockString(flags["run"])
	if len(pos) == 0 || pos[0] == "" || run == "" {
		return nil, nil, "", "", h.usage("a block id and --run are required")
	}
	if h.sub == "amend-acceptance" {
		reason, ok := flags["reason"].(string)
		_, file := flags["acceptance"].(string)
		if len(pos) != 1 || !file || !ok || humanTrim(reason) == "" || strings.ContainsFunc(reason, blockCc) {
			return nil, nil, "", "", h.usage("amend-acceptance needs one block id, --acceptance and a nonblank --reason without control characters")
		}
	}
	if flags["remove-worktree"] == true {
		return nil, nil, "", "", h.usage("--remove-worktree is a block close flag")
	}
	return flags, pos, run, pos[0], -1
}

// blockCc is the Unicode Cc class (\p{Cc}).
func blockCc(r rune) bool { return r < 0x20 || r >= 0x7f && r <= 0x9f }

var blockControl = regexp.MustCompile(`[\x00-\x1f\x7f]`)

func blockLedgerRows(home string, record ledger.Object) ([]gatecheck.Row, bool) {
	values, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), blockString(record.Get("project")))
	if err != nil {
		return nil, false
	}
	rows := []gatecheck.Row{}
	for _, v := range values {
		r, ok := v.(map[string]any)
		if !ok {
			return nil, false
		}
		rows = append(rows, r)
	}
	return rows, true
}

func blockRunRows(rows []gatecheck.Row, run string) []gatecheck.Row {
	out := []gatecheck.Row{}
	for _, r := range rows {
		if r["run"] == run {
			out = append(out, r)
		}
	}
	return out
}

// blockExpirySafe: Node's lazy expiry checks are ones Go runs the same way (a
// readable grant, no active approval whose text Node would print).
func blockExpirySafe(record ledger.Object) bool { return autopilot.Supported(record) }

func blockSHA(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// ---- accept-hit ----

var acceptHitLine = regexp.MustCompile(`^transcript line (\d+): (.+)$`)

func runBlockAcceptHit(args []string) int {
	h := newHumanVerb("accept-hit", args)
	flags, pos, run, id, code := h.start("<id> --run <r> [--rule <rule-id>] --line <n> --transcript <path> --reason <text>", "")
	if code >= 0 {
		return code
	}
	if len(pos) != 1 {
		return h.usage("block accept-hit takes one block id")
	}
	for _, name := range []string{"block", "run", "rule", "line", "transcript", "reason"} {
		value, label := flags[name], "--"+name
		if name == "block" {
			value, label = id, "block id"
		}
		if s, ok := value.(string); ok && blockControl.MatchString(s) {
			return h.usage(label + " must not contain control characters")
		}
	}
	_, hasRule := flags["rule"]
	required := []string{"transcript", "reason"}
	if hasRule {
		required = append([]string{"rule"}, required...)
	}
	for _, name := range required {
		if s, ok := flags[name].(string); !ok || humanTrim(s) == "" {
			return h.usage("--" + name + " is required")
		}
	}
	lineText, _ := flags["line"].(string)
	line, err := strconv.ParseInt(lineText, 10, 64)
	if !regexp.MustCompile(`^-?[0-9]+$`).MatchString(lineText) || err != nil || line > 1<<53-1 || line < -(1<<53-1) {
		return h.usage("--line must be an integer")
	}
	noSuchHit := func() int {
		return h.refuse("accept-hit-no-such-hit", "accept-hit-no-such-hit: the named transcript hit was not found; nothing was recorded")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return h.fallback()
	}
	record, err := state.ReadRun(home, run)
	if err != nil || blockObject(blockObject(record.Get("blocks")).Get(id)) == nil {
		return noSuchHit()
	}
	transcript, rule := flags["transcript"].(string), blockString(flags["rule"])
	data, err := os.ReadFile(transcript)
	if err != nil {
		return noSuchHit()
	}
	text := string(data)
	hits, ok := gatecheck.Transcript(text)
	// Node pairs Codex item lines without --rule; Go leaves Codex logs to Node.
	if !ok || !hasRule && strings.Contains(text, "thread.started") || !blockStorageSafe(home, run, record) {
		return h.fallback()
	}
	lines := strings.Split(text, "\n")
	type hit struct {
		rule string
		line int64
	}
	selected := []hit{}
	for _, b := range hits {
		m := acceptHitLine.FindStringSubmatch(b.Detail)
		if m == nil {
			return h.fallback()
		}
		n, _ := strconv.ParseInt(m[1], 10, 64)
		if n == line && (!hasRule || m[2] == rule) {
			selected = append(selected, hit{m[2], n})
		}
	}
	if line < 1 || line > int64(len(lines)) || len(selected) == 0 {
		return noSuchHit()
	}
	reason := humanTrim(blockString(flags["reason"]))
	rows := []ledger.Object{}
	names := []string{}
	for _, s := range selected {
		rows = append(rows, ledger.Object{{Key: "block", Value: id}, {Key: "rule", Value: s.rule}, {Key: "line", Value: float64(s.line)}, {Key: "line_sha256", Value: blockSHA([]byte(lines[s.line-1]))}, {Key: "reason", Value: reason}, {Key: "by", Value: "human"}})
		names = append(names, fmt.Sprintf("%s at line %d", s.rule, s.line))
	}
	what := fmt.Sprintf("hit %s at line %d", selected[0].rule, selected[0].line)
	if !hasRule {
		what = "hits " + strings.Join(names, ", ")
	}
	if code := h.ask(id, fmt.Sprintf("Accept false transcript %s in %s of block %s (run %s)? Only the owner may say yes.", what, transcript, id, run), nil); code >= 0 {
		return code
	}
	accepted := []any{}
	for i, row := range rows {
		signed := append(ledger.Object{{Key: "event", Value: "rule_break.accepted"}}, row...)
		if _, err := state.WriteSigned(home, run, signed); err != nil {
			return h.fail(err)
		}
		h.say(fmt.Sprintf("block %s accepted %s transcript line %d (by: human)\n", id, selected[i].rule, selected[i].line))
		accepted = append(accepted, ledger.Object{{Key: "rule", Value: selected[i].rule}, {Key: "line", Value: float64(selected[i].line)}, {Key: "reason", Value: reason}, {Key: "by", Value: "human"}})
	}
	if h.json {
		out := ledger.Object{{Key: "block", Value: id}, {Key: "event", Value: "rule_break.accepted"}}
		if hasRule {
			out = append(out, accepted[0].(ledger.Object)...)
		} else {
			out.Set("accepted_hits", accepted)
		}
		h.emit(out)
	}
	return 0
}

// ---- amend-acceptance ----

type amendPrepared struct {
	acceptance       []any
	oldSHA, newSHA   string
	oldCount, clause int
}

// prepareAmend is block.mjs prepareAcceptanceAmendment. ok is false for a
// stored acceptance Go cannot count or hash the way Node does.
func prepareAmend(record ledger.Object, id string, value any) (amendPrepared, error, bool) {
	block := blockObject(blockObject(record.Get("blocks")).Get(id))
	if block == nil {
		return amendPrepared{}, &state.Error{Code: "unknown_block", Message: fmt.Sprintf("block %s is not in run %s", id, blockString(record.Get("run_id")))}, true
	}
	if block.Get("status") != "open" {
		return amendPrepared{}, &state.Error{Code: "not-open", Message: fmt.Sprintf("not-open: block %s is not open", id)}, true
	}
	clauses, err := blockopen.Acceptance(value)
	if err != nil {
		return amendPrepared{}, err, true
	}
	old, isList := block.Get("acceptance").([]any)
	if !isList {
		return amendPrepared{}, nil, false
	}
	oldJSON, err1 := canon.Marshal(old)
	newJSON, err2 := canon.Marshal(clauses)
	if err1 != nil || err2 != nil {
		return amendPrepared{}, nil, false
	}
	p := amendPrepared{acceptance: clauses, oldSHA: blockSHA(oldJSON), newSHA: blockSHA(newJSON), oldCount: len(old)}
	if p.oldSHA == p.newSHA {
		return p, &state.Error{Code: "acceptance-unchanged", Message: "acceptance-unchanged: the clause list is unchanged; nothing was recorded"}, true
	}
	return p, nil, true
}

func blockGrantActive(record ledger.Object, now time.Time) (bool, bool) {
	g := record.Get("autopilot")
	if g == nil {
		return false, true
	}
	grant, ok := g.(ledger.Object)
	if !ok {
		return false, false
	}
	s, err := autopilot.GrantState(grant, now)
	if err != nil {
		return false, false
	}
	return s == "active", true
}

func blockUUID() (string, error) {
	u := make([]byte, 16)
	if _, err := rand.Read(u); err != nil {
		return "", err
	}
	u[6], u[8] = u[6]&0x0f|0x40, u[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[:4], u[4:6], u[6:8], u[8:10], u[10:]), nil
}

func runBlockAmendAcceptance(args []string) int {
	h := newHumanVerb("amend-acceptance", args)
	help := "  --acceptance <file>  JSON/YAML list [{clause, tests}]; tests is a nonempty list of test ids.\n                       Human checks use {clause, kind: human_check} without tests.\n"
	flags, _, run, id, code := h.start("<id> --run <r> --acceptance <file> --reason <text>", help)
	if code >= 0 {
		return code
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return h.fallback()
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return h.fail(err)
	}
	file, reason := flags["acceptance"].(string), flags["reason"].(string)
	parse := func() (any, int) {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, h.fallback()
		}
		value, err := blockopen.ParseAcceptance(data)
		if err != nil {
			return nil, h.fallback()
		}
		return value, -1
	}
	active, ok := blockGrantActive(record, time.Now())
	if !ok || !blockStorageSafe(home, run, record) {
		return h.fallback()
	}
	// Refuse human checks before any terminal question (Node's checkExpiry
	// reads the grant just found active).
	if read, err := autopilot.Read(record, time.Now()); active && err == nil && read.State == "active" {
		value, code := parse()
		if code >= 0 {
			return code
		}
		clauses, err := blockopen.Acceptance(value)
		if err != nil {
			return h.fail(err)
		}
		human := []string{}
		for _, c := range clauses {
			if blockObject(c).Get("kind") == "human_check" {
				human = append(human, statusQuote(blockString(blockObject(c).Get("clause"))))
			}
		}
		if len(human) > 0 {
			return h.refuse("autopilot-human-check", "autopilot-human-check: an active autopilot grant cannot wait on human_check clauses: "+strings.Join(human, ", ")+"; turn each into a clause with tests or ask the owner")
		}
	}
	noGrant := &state.Error{Code: "autopilot-active", Message: "amend-acceptance is refused while an autopilot grant is active"}
	var prepared amendPrepared
	var value any
	before := func() int {
		if active {
			return h.fail(noGrant)
		}
		var code int
		if value, code = parse(); code >= 0 {
			return code
		}
		p, err, ok := prepareAmend(record, id, value)
		if !ok {
			return h.fallback()
		}
		if err != nil {
			return h.fail(err)
		}
		prepared = p
		return -1
	}
	message := func() string {
		return fmt.Sprintf("Amend acceptance of block %s (run %s) from %d clauses to %d clauses? Only the owner may say yes.", id, run, prepared.oldCount, len(prepared.acceptance))
	}
	if !blockAskAvailable() {
		return h.ask(id, "", nil)
	}
	if code := before(); code >= 0 {
		return code
	}
	if code := h.ask(id, message(), nil); code >= 0 {
		return code
	}
	var row ledger.Object
	err = state.UpdateBlock(home, run, "", func(current, _ *ledger.Object) ([]ledger.Object, error) {
		if a, ok := blockGrantActive(*current, time.Now()); a || !ok {
			return nil, noGrant
		}
		again, err, ok := prepareAmend(*current, id, value)
		if !ok {
			return nil, &state.Error{Code: "acceptance-moved", Message: fmt.Sprintf("acceptance changed while confirming block %s; confirm again", id)}
		}
		if err != nil {
			return nil, err
		}
		if again.oldSHA != prepared.oldSHA {
			return nil, &state.Error{Code: "acceptance-moved", Message: fmt.Sprintf("acceptance changed while confirming block %s; confirm again", id)}
		}
		blocks := blockObject(current.Get("blocks"))
		block := blockObject(blocks.Get(id))
		block.Set("acceptance", again.acceptance)
		checks := []any{}
		for _, c := range again.acceptance {
			if blockObject(c).Get("kind") == "human_check" {
				checks = append(checks, blockObject(c).Get("clause"))
			}
		}
		if len(checks) > 0 {
			block.Set("human_checks", checks)
		} else {
			block.Delete("human_checks")
		}
		revision, err := blockUUID()
		if err != nil {
			return nil, err
		}
		block.Set("acceptance_revision", revision)
		blocks.Set(id, block)
		current.Set("blocks", blocks)
		row = ledger.Object{{Key: "event", Value: "block.acceptance_amended"}, {Key: "block", Value: id}, {Key: "old_sha256", Value: prepared.oldSHA}, {Key: "new_sha256", Value: again.newSHA}, {Key: "clauses", Value: float64(len(again.acceptance))}, {Key: "reason", Value: reason}, {Key: "by", Value: "human"}}
		return []ledger.Object{row}, nil
	})
	if err != nil {
		return h.fail(err)
	}
	// resetAcceptanceWatch: only the commit-watch cursor goes.
	top, err := gitx.RepoTop(blockString(record.Get("workspace")))
	if err != nil {
		return h.fail(err)
	}
	if err := os.Remove(filepath.Join(top, ".code-forge", "queue", "watch", id+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return h.fail(err)
	}
	h.say(fmt.Sprintf("block %s acceptance amended (%d clauses; by: human)\n", id, len(prepared.acceptance)))
	if h.json {
		h.emit(row)
	}
	return 0
}

// ---- unwaive / ack-waivers ----

func runBlockAgentWaiver(sub string, args []string) int {
	h := newHumanVerb(sub, args)
	form := "<id> --run <r>"
	if sub == "unwaive" {
		form = "<id> <finding-id> --run <r> --file <path> --reason <text>"
	}
	flags, pos, run, id, code := h.start(form, "")
	if code >= 0 {
		return code
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return h.fallback()
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return h.fail(err)
	}
	if sub == "unwaive" && len(pos) != 2 {
		return h.usage("block unwaive takes one block id and one finding id")
	}
	if sub == "ack-waivers" && len(pos) != 1 {
		return h.usage("block ack-waivers takes one block id")
	}
	if !blockStorageSafe(home, run, record) {
		return h.fallback()
	}
	entry := blockObject(blockObject(record.Get("blocks")).Get(id))
	var row ledger.Object
	var counted []gatecheck.Row
	if sub == "unwaive" {
		finding, file := pos[1], blockString(flags["file"])
		if msg := blockstate.Finding(finding); msg != "" {
			return h.usage(msg)
		}
		if _, ok := flags["file"].(string); !ok {
			return h.usage("--file is required")
		}
		if msg := blockstate.File(file, false); msg != "" {
			return h.usage(msg)
		}
		reason, ok := flags["reason"].(string)
		if !ok || humanTrim(reason) == "" || blockControl.MatchString(reason) {
			return h.usage("--reason must be nonblank and contain no control characters")
		}
		none := func() int {
			return h.refuse("unwaive-no-agent-waiver", "unwaive-no-agent-waiver: no verified agent waiver for this block, file and finding; nothing was recorded")
		}
		if entry == nil {
			return none()
		}
		workspace := blockString(record.Get("workspace"))
		top, err := gitx.RepoTop(workspace)
		if err != nil {
			return h.fallback()
		}
		// Node's plain path.relative, without resolving the workspace.
		prefix, err := filepath.Rel(top, workspace)
		if err != nil {
			return h.fallback()
		}
		if prefix == "." {
			prefix = ""
		}
		if !gatecheck.Owns(filepath.ToSlash(prefix), entry, file) {
			return none()
		}
		all, ok := blockLedgerRows(home, record)
		if !ok {
			return h.fallback()
		}
		key, err := blockclose.ReadKey(home, run)
		if err != nil {
			return h.fail(err)
		}
		found := false
		for _, r := range blockRunRows(all, run) {
			found = found || r["block"] == id && r["file"] == file && r["finding"] == finding && r["event"] == "review.waived" && r["by"] == "agent" && gatecheck.Verified(r, key)
		}
		if !found {
			return none()
		}
		row = ledger.Object{{Key: "event", Value: "review.unwaived"}, {Key: "block", Value: id}, {Key: "file", Value: file}, {Key: "finding", Value: finding}, {Key: "reason", Value: reason}, {Key: "by", Value: "human"}}
	} else {
		all, ok := blockLedgerRows(home, record)
		if !ok {
			return h.fallback()
		}
		if entry != nil {
			key, err := blockclose.ReadKey(home, run)
			if err != nil {
				return h.fail(err)
			}
			counted = gatecheck.AgentWaivers(blockRunRows(all, run), id, key)
		}
		if len(counted) == 0 {
			return h.refuse("ack-waivers-none", "ack-waivers-none: the block has no counted agent waivers; nothing was recorded")
		}
		digest, ok := gatecheck.AgentWaiverDigest(counted)
		if !ok {
			return h.fallback()
		}
		for _, w := range counted {
			for _, k := range []string{"file", "finding", "reason", "verdict"} {
				if _, isText := w[k].(string); !isText {
					return h.fallback()
				}
			}
		}
		row = ledger.Object{{Key: "event", Value: "agent_waivers.acked"}, {Key: "block", Value: id}, {Key: "digest", Value: digest}, {Key: "count", Value: float64(len(counted))}, {Key: "by", Value: "human"}}
	}
	message := fmt.Sprintf("Acknowledge the listed agent waivers of block %s (run %s)? Only the owner may say yes.", id, run)
	if sub == "unwaive" {
		message = fmt.Sprintf("Undo agent waivers for finding %s in %s of block %s (run %s)? Only the owner may say yes.", pos[1], blockString(flags["file"]), id, run)
	}
	list := func() int {
		h.mark()
		out := os.Stdout
		if h.json {
			out = os.Stderr
		}
		for _, w := range counted {
			fmt.Fprintf(out, "block %s: agent waiver %s %s reason: %s; verdict: %s\n", id, w["file"], w["finding"], w["reason"], w["verdict"])
		}
		return -1
	}
	if code := h.ask(id, message, list); code >= 0 {
		return code
	}
	if _, err := state.WriteSigned(home, run, row); err != nil {
		return h.fail(err)
	}
	if sub == "unwaive" {
		h.say(fmt.Sprintf("block %s unwaived %s in %s (by: human)\n", id, pos[1], blockString(flags["file"])))
	} else {
		h.say(fmt.Sprintf("block %s acknowledged %d agent waiver(s) (by: human)\n", id, len(counted)))
	}
	if h.json {
		h.emit(row)
	}
	return 0
}

// ---- attempt ----

func runBlockAttempt(args []string) int {
	h := newHumanVerb("attempt", args)
	_, _, run, id, code := h.start("<id> --run <r>", "")
	if code >= 0 {
		return code
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return h.fallback()
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return h.fail(err)
	}
	// The registry shapes Node's spreads and += read the same way, and quiet
	// expiry checks; anything else stays with Node before the expiry rows.
	shape := func(r ledger.Object) bool {
		block := blockObject(blockObject(r.Get("blocks")).Get(id))
		if block == nil || block.Get("status") != "open" {
			return true // mutateRun refuses these itself
		}
		files, ok := block.Get("owned_files").([]any)
		if !ok {
			return false
		}
		for _, f := range files {
			if _, ok := f.(string); !ok {
				return false
			}
		}
		if k := block.Get("owned_kinds"); k != nil {
			if _, ok := k.(ledger.Object); !ok {
				return false
			}
		}
		n, ok := block.Get("attempt").(float64)
		return ok && n == math.Trunc(n) && math.Abs(n) < 1<<53
	}
	if !shape(record) || !blockExpirySafe(record) || !blockStorageSafe(home, run, record) {
		return h.fallback()
	}
	if _, err := blockopen.Claims(home, run, blockString(record.Get("project")), id); errors.Is(err, blockops.ErrHandover) {
		return h.fallback()
	}
	h.mark()
	blockGrantExpiry(home, run, os.Stderr)
	var attempt float64
	kept := 0
	err = state.UpdateBlock(home, run, id, func(current, block *ledger.Object) ([]ledger.Object, error) {
		if !shape(*current) {
			return nil, &state.Error{Code: "usage", Message: fmt.Sprintf("block %s changed while recording the attempt; run block attempt again", id)}
		}
		claims, err := blockopen.Claims(home, run, blockString(current.Get("project")), id)
		if err != nil {
			return nil, err
		}
		files, _ := block.Get("owned_files").([]any)
		merged := slices.Clone(files)
		for _, c := range claims {
			if !slices.Contains(merged, any(c)) {
				merged = append(merged, c)
			}
		}
		claimList := []any{}
		for _, c := range claims {
			claimList = append(claimList, c)
		}
		kinds := owned.Kinds(blockString(current.Get("workspace")), claimList)
		for _, f := range blockObject(block.Get("owned_kinds")) {
			kinds.Set(f.Key, f.Value)
		}
		attempt = block.Get("attempt").(float64) + 1
		kept = len(claims)
		block.Set("owned_files", merged)
		block.Set("owned_kinds", kinds)
		block.Set("attempt", attempt)
		return []ledger.Object{{{Key: "event", Value: "block.attempt"}, {Key: "block", Value: id}, {Key: "attempt", Value: attempt}}}, nil
	})
	if err != nil {
		return h.fail(err)
	}
	n := strconv.FormatFloat(attempt, 'f', -1, 64)
	h.say(fmt.Sprintf("block %s attempt %s\n", id, n))
	if kept > 0 {
		h.say(fmt.Sprintf("kept claims: %d\n", kept))
	}
	if h.json {
		out := ledger.Object{{Key: "block", Value: id}, {Key: "event", Value: "attempt"}, {Key: "attempt", Value: attempt}}
		if kept > 0 {
			out.Set("kept_claims", float64(kept))
		}
		h.emit(out)
	}
	return 0
}

// ---- waive at a terminal ----

// waiveHuman is block.mjs's human waive: waivePrecheck, the question, the lazy
// expiry checks, then waiveFinding's signed review.waived row.
func (p *blockPreview) waiveHuman() int {
	h := newHumanVerb("waive", p.args)
	h.json = p.flags["json"] == true
	if !blockExpirySafe(p.record) || !blockStorageSafe(p.home, p.run, p.record) {
		return h.fallback()
	}
	file, finding := blockString(p.flags["file"]), p.pos[1]
	precheck := func() int {
		if msg := blockstate.Finding(finding); msg != "" {
			return h.usage(msg)
		}
		if msg := blockstate.File(file, false); msg != "" {
			return h.usage(msg)
		}
		entry := blockObject(blockObject(p.record.Get("blocks")).Get(p.id))
		if entry == nil {
			return h.refuse("unknown_block", fmt.Sprintf("block %s is not in run %s", p.id, p.run))
		}
		workspace := blockString(p.record.Get("workspace"))
		top, err := gitx.RepoTop(workspace)
		if err != nil {
			return h.fallback()
		}
		if !gatecheck.Owns(gatecheck.Prefix(top, workspace), entry, file) {
			return h.refuse("waive-not-owned", fmt.Sprintf("waive-not-owned: block %s does not own %s; nothing was recorded", p.id, file))
		}
		if finding == "proof" {
			return -1
		}
		rows, ok := blockLedgerRows(p.home, p.record)
		if !ok {
			return h.fallback()
		}
		for _, r := range rows {
			if r["run"] != p.run || r["block"] != p.id || r["file"] != file {
				continue
			}
			switch r["event"] {
			case "review.triage", "review.late_finding":
				if r["finding"] == finding {
					return -1
				}
			case "review.result":
				if slices.Contains(gatecheck.OpenFindingIDs(r["open"]), finding) {
					return -1
				}
			case "review.cap":
				if list, ok := r["open"].([]any); ok && slices.Contains(list, any(finding)) {
					return -1
				}
			}
		}
		return h.refuse("waive-unknown-finding", fmt.Sprintf("waive-unknown-finding: no review row of block %s names finding %s in %s; nothing was recorded", p.id, finding, file))
	}
	if code := h.ask(p.id, fmt.Sprintf("Waive finding %s in %s of block %s (run %s)? Only the owner may say yes.", finding, file, p.id, p.run), precheck); code >= 0 {
		return code
	}
	blockGrantExpiry(p.home, p.run, os.Stderr)
	reason := humanTrim(blockString(p.flags["reason"]))
	if reason == "" {
		return h.usage("block waive needs --reason")
	}
	record, err := state.ReadRun(p.home, p.run)
	if err != nil {
		return h.fail(err)
	}
	if blockObject(blockObject(record.Get("blocks")).Get(p.id)) == nil {
		return h.refuse("unknown_block", fmt.Sprintf("block %s is not in run %s", p.id, p.run))
	}
	row := ledger.Object{{Key: "event", Value: "review.waived"}, {Key: "block", Value: p.id}, {Key: "file", Value: file}, {Key: "finding", Value: finding}, {Key: "reason", Value: reason}, {Key: "by", Value: "human"}}
	if _, err := state.WriteSigned(p.home, p.run, row); err != nil {
		return h.fail(err)
	}
	h.say(fmt.Sprintf("block %s waived %s in %s (by: human)\n", p.id, finding, file))
	if h.json {
		h.emit(ledger.Object{{Key: "block", Value: p.id}, {Key: "event", Value: "waived"}, {Key: "finding", Value: finding}, {Key: "file", Value: file}, {Key: "by", Value: "human"}})
	}
	return 0
}
