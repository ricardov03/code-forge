package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/humancheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B635 (issue #37 E): the owner's look at what autopilot did.
//
//	autopilot digest --run <id> [--json]
//	autopilot ratify <row-id> --run <id> [--json]
//	autopilot revert <row-id> --run <id> [--json]
//
// digest lists every signed autopilot act of the run that the owner has not
// ratified or reverted yet: deferred human checks (B632), substitute reviews
// (B630), autopilot and agent waivers, temporary config changes (active
// approvals), grant extends and until-done ends (B634). Each row has a
// stable id ("dg-" and the first 12 hex of the source row's mac) and the
// exact commands. ratify and revert are the owner's: a terminal yes (no
// --yes), refused under an active autopilot grant, as run human-checks clear.
// Each writes one signed autopilot.ratify or autopilot.revert row naming the
// digest id; revert also undoes the act: a deferred block goes back to
// awaiting_human_check, a waiver gets a signed review.unwaived row, a
// substitute no longer counts at close (review-file --fresh re-runs it), an
// approval's old value comes back now. An extend or an until-done end has
// nothing left to undo once the grant has ended: ratify only.

const (
	apdgDeferred  = "deferred-human"
	apdgSubst     = "substitute"
	apdgWaiver    = "waiver"
	apdgConfig    = "temp-config"
	apdgExtend    = "extend"
	apdgUntilDone = "until-done-end"
)

// apdgNow is the clock seam of the three verbs.
var apdgNow = time.Now

// apdgRow is one digest row and the source row it names.
type apdgRow struct {
	ID, Kind, Block, File, Why string
	When                       any
	NoRevert                   string // why revert has nothing to undo; "" when it has
	src                        gatecheck.Row
	approvalID                 string
}

func (r apdgRow) ratifyCommand(run string) string {
	return "code-forge autopilot ratify " + r.ID + " --run " + run
}

func (r apdgRow) revertCommand(run string) string {
	if r.NoRevert != "" {
		return ""
	}
	return "code-forge autopilot revert " + r.ID + " --run " + run
}

func (r apdgRow) object(run string) ledger.Object {
	opt := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	o := ledger.Object{{Key: "id", Value: r.ID}, {Key: "kind", Value: r.Kind}, {Key: "block", Value: opt(r.Block)}, {Key: "file", Value: opt(r.File)},
		{Key: "when", Value: r.When}, {Key: "why", Value: r.Why}, {Key: "ratify", Value: r.ratifyCommand(run)}, {Key: "revert", Value: opt(r.revertCommand(run))}}
	if r.NoRevert != "" {
		o.Set("revert_note", r.NoRevert)
	}
	return o
}

func apdgID(r gatecheck.Row) string {
	mac, _ := r["mac"].(string)
	if len(mac) < 12 {
		return ""
	}
	return "dg-" + mac[:12]
}

func apdgText(v any) string { s, _ := v.(string); return s }

// apdgCollect derives the open digest rows of run from its record, the
// project's rows and the run key. A row that does not verify is no act.
func apdgCollect(run string, record ledger.Object, all []any, key []byte) []apdgRow {
	rows := []gatecheck.Row{}
	for _, v := range all {
		if r, ok := v.(map[string]any); ok && r["run"] == run {
			rows = append(rows, r)
		}
	}
	if key == nil {
		return []apdgRow{}
	}
	ok := func(r gatecheck.Row) bool { return gatecheck.Verified(r, key) }
	settled := map[string]bool{}
	for _, r := range rows {
		if (r["event"] == "autopilot.ratify" || r["event"] == "autopilot.revert") && r["by"] == "human" && ok(r) {
			settled[apdgText(r["digest_id"])] = true
		}
	}
	blocks := blockObject(record.Get("blocks"))
	open := humancheck.Open(run, record, all, key)
	counted := map[string]bool{} // the macs of the counted agent waivers
	seenBlock := map[string]bool{}
	for _, r := range rows {
		if b := apdgText(r["block"]); b != "" && !seenBlock[b] && r["event"] == "review.waived" && r["by"] == "agent" {
			seenBlock[b] = true
			for _, w := range gatecheck.AgentWaivers(rows, b, key) {
				counted[apdgText(w["mac"])] = true
			}
		}
	}
	unwaived := func(at int) bool {
		w := rows[at]
		for _, r := range rows[at+1:] {
			if r["event"] == "review.unwaived" && r["by"] == "human" && r["block"] == w["block"] && r["file"] == w["file"] && r["finding"] == w["finding"] && ok(r) {
				return true
			}
		}
		return false
	}
	out := []apdgRow{}
	for i, r := range rows {
		event := r["event"]
		row := apdgRow{Block: apdgText(r["block"]), When: r["ts"], src: r}
		switch {
		case event == "autopilot.decision" && r["acted"] == true && r["scope"] == autopilot.DeferHumanScope:
			if blockObject(blocks.Get(row.Block)).Get("status") != humancheck.DeferredStatus {
				continue // its checks were cleared: the owner already looked
			}
			checks := []string{}
			for _, e := range open {
				if e.Block != row.Block {
					continue
				}
				c := e.Clause + " " + statusQuote(e.Check)
				if s, isText := e.WaitsFor.(string); isText {
					c += " (waits for: " + s + ")"
				}
				checks = append(checks, c)
			}
			row.Kind = apdgDeferred
			row.Why = "block " + row.Block + " closed " + humancheck.DeferredStatus + " with its human checks deferred to the owner: " + strings.Join(checks, "; ")
		case event == "autopilot.decision" && r["acted"] == true && r["scope"] == autopilot.SubstituteScope:
			row.Kind, row.File = apdgSubst, apdgText(r["file"])
			row.Why = row.File + " was approved by a substitute review: " + apdgText(r["reason"])
		case event == "review.waived" && (r["by"] == "autopilot" || r["by"] == "agent" && counted[apdgText(r["mac"])]):
			if r["by"] == "autopilot" && unwaived(i) {
				continue
			}
			row.Kind, row.File = apdgWaiver, apdgText(r["file"])
			sev := apdgText(r["severity"])
			if sev != "" {
				sev += " "
			}
			row.Why = fmt.Sprintf("%sfinding %s of %s waived by %s: %s", sev, apdgText(r["finding"]), row.File, apdgText(r["by"]), apdgText(r["reason"]))
		case event == "autopilot.approve":
			id := apdgText(r["approval_id"])
			values, at := autopilotStored(record, id)
			if at < 0 || blockObject(values[at]).Get("status") != "active" {
				continue // restored or skipped already
			}
			row.Kind, row.approvalID = apdgConfig, id
			old := autopilotText(r["old"])
			if r["old_absent"] == true {
				old = "(not set)"
			}
			row.Why = fmt.Sprintf("config %s changed from %s to %s until %s (approval %s)", apdgText(r["key"]), old, autopilotText(r["new"]), autopilotText(r["until"]), id)
		case event == "autopilot.extend":
			row.Kind = apdgExtend
			from := "no end"
			if r["from"] != nil {
				from = autopilotText(r["from"])
			}
			row.Why = fmt.Sprintf("grant %s extended from %s to %s", apdgText(r["grant_id"]), from, autopilotText(r["until"]))
			row.NoRevert = "an extend is undone by autopilot stop while its grant runs; once the grant has ended there is nothing to revert"
		case event == "autopilot.expire" && r["reason"] == "done":
			names := []string{}
			list, _ := r["blocks"].([]any)
			for _, b := range list {
				names = append(names, apdgText(b))
			}
			row.Kind = apdgUntilDone
			row.Why = fmt.Sprintf("until-done grant %s ended: every planned block closed (%s)", apdgText(r["grant_id"]), strings.Join(names, ", "))
			row.NoRevert = "the grant has ended; a new grant is autopilot start"
		default:
			continue
		}
		row.ID = apdgID(r)
		if row.ID == "" || settled[row.ID] || !ok(r) {
			continue
		}
		out = append(out, row)
	}
	return out
}

// apdgRead reads the record, the project's rows and the key of run.
func apdgRead(home, run string) (ledger.Object, []any, []byte, error) {
	record, err := state.ReadRun(home, run)
	if err != nil {
		return nil, nil, nil, err
	}
	slug, _ := record.Get("project").(string)
	if slug == "" {
		return nil, nil, nil, &runFail{fmt.Sprintf("run %s names no project", run), false}
	}
	all, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
	if err != nil {
		return nil, nil, nil, err
	}
	key, _ := blockclose.ReadKey(home, run) // no valid key: no row verifies
	return record, all, key, nil
}

// apdgParse reads `[<row-id>] --run <id> [--json]`: digest takes no row id,
// ratify and revert exactly one.
func apdgParse(sub string, args []string) (id, run string, asJSON bool, err error) {
	positionals := []string{}
	seen := map[string]bool{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			positionals = append(positionals, a)
			continue
		}
		name, value, inline := strings.Cut(a[2:], "=")
		if name != "run" && name != "json" {
			quoted, _ := ledger.Stringify(a, false)
			return "", "", false, &runFail{"unknown flag " + string(quoted), true}
		}
		if seen[name] {
			return "", "", false, &runFail{"--" + name + " given more than once", true}
		}
		seen[name] = true
		if name == "json" {
			if inline {
				return "", "", false, &runFail{"--json takes no value", true}
			}
			asJSON = true
			continue
		}
		if !inline {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return "", "", false, &runFail{"--run needs a value", true}
			}
			value = args[i+1]
			i++
		}
		if value == "" {
			return "", "", false, &runFail{"--run needs a value", true}
		}
		run = value
	}
	want := 1
	if sub == "digest" {
		want = 0
	}
	if len(positionals) > want {
		quoted, _ := ledger.Stringify(positionals[want], false)
		return "", "", false, &runFail{"unexpected argument " + string(quoted), true}
	}
	if len(positionals) < want {
		return "", "", false, &runFail{"autopilot " + sub + " needs a digest row id (autopilot digest --run <id> lists them)", true}
	}
	if run == "" {
		return "", "", false, &runFail{"autopilot " + sub + " needs --run <id>", true}
	}
	if want == 1 {
		id = positionals[0]
	}
	return id, run, asJSON, nil
}

func apdgSub(sub string) bool { return sub == "digest" || sub == "ratify" || sub == "revert" }

// apdgRun serves the three verbs.
func apdgRun(o *runOut, home, sub string, args []string) int {
	id, run, asJSON, err := apdgParse(sub, args)
	if err != nil {
		return autopilotReport(o, sub, err)
	}
	if sub == "digest" {
		return apdgDigest(o, home, run, asJSON)
	}
	return apdgSettle(o, home, sub, id, run, asJSON)
}

func apdgDigest(o *runOut, home, run string, asJSON bool) int {
	record, all, key, err := apdgRead(home, run)
	if err != nil {
		return autopilotReport(o, "digest", err)
	}
	rows := apdgCollect(run, record, all, key)
	if asJSON {
		list := make([]any, len(rows))
		for i, r := range rows {
			list[i] = r.object(run)
		}
		data, _ := ledger.Stringify(ledger.Object{{Key: "run", Value: run}, {Key: "rows", Value: list}}, false)
		o.out(string(data) + "\n")
		return 0
	}
	if len(rows) == 0 {
		o.out("run " + run + ": no autopilot act waits for the owner's look\n")
		return 0
	}
	var b strings.Builder
	fmt.Fprintf(&b, "run %s: %d autopilot act(s) wait for the owner's look\n", run, len(rows))
	for _, r := range rows {
		block := r.Block
		if block == "" {
			block = "-"
		}
		fmt.Fprintf(&b, "%s %s %s %s: %s\n", r.ID, r.Kind, block, autopilotText(r.When), r.Why)
		fmt.Fprintf(&b, "  ratify: %s\n", r.ratifyCommand(run))
		if r.NoRevert != "" {
			fmt.Fprintf(&b, "  revert: none (%s)\n", r.NoRevert)
		} else {
			fmt.Fprintf(&b, "  revert: %s\n", r.revertCommand(run))
		}
	}
	o.out(b.String())
	return 0
}

func apdgFind(rows []apdgRow, id string) (apdgRow, bool) {
	for _, r := range rows {
		if r.ID == id {
			return r, true
		}
	}
	return apdgRow{}, false
}

// apdgSettle is ratify and revert: the owner's guard, then the signed rows
// under the run lock (the row is found again there).
func apdgSettle(o *runOut, home, sub, id, run string, asJSON bool) int {
	fail := func(msg string) int { return autopilotReport(o, sub, &runFail{msg, false}) }
	record, all, key, err := apdgRead(home, run)
	if err != nil {
		return autopilotReport(o, sub, err)
	}
	notOpen := fmt.Sprintf("digest-row-not-open: run %s has no open digest row %s (autopilot digest --run %s lists them); nothing was recorded", run, id, run)
	row, found := apdgFind(apdgCollect(run, record, all, key), id)
	if !found {
		return fail(notOpen)
	}
	if sub == "revert" && row.NoRevert != "" {
		return fail(fmt.Sprintf("revert-not-applicable: %s is a %s row: %s; ratify it instead; nothing was recorded", id, row.Kind, row.NoRevert))
	}
	noTerminal := "autopilot " + sub + " needs the human at a terminal to confirm; nothing was recorded (there is no --yes)\n"
	if !humanTTY() {
		o.err(noTerminal)
		return 2
	}
	activeGrant := fmt.Sprintf("autopilot %s is refused while an autopilot grant is active on run %s", sub, run)
	grant, err := autopilotCheckExpiry(home, run, apdgNow())
	if err != nil {
		return autopilotReport(o, sub, err)
	}
	if grant.State == "active" {
		return fail(activeGrant)
	}
	verb := "Ratify"
	if sub == "revert" {
		verb = "Revert"
	}
	result, askErr := humanAsk(fmt.Sprintf("%s autopilot act %s (%s) of run %s: %s? Only the owner may say yes.", verb, id, row.Kind, run, row.Why))
	if result == humanask.NoTTY {
		o.err(noTerminal)
		return 2
	}
	if askErr != nil || result != humanask.Confirmed {
		o.err(fmt.Sprintf("autopilot %s %s: cancelled — nothing was recorded\n", sub, id))
		return 1
	}
	now := apdgNow()
	settle := ledger.Object{{Key: "event", Value: "autopilot." + sub}, {Key: "digest_id", Value: id}, {Key: "kind", Value: row.Kind},
		{Key: "source_event", Value: row.src["event"]}, {Key: "block", Value: apdgOpt(row.Block)}, {Key: "file", Value: apdgOpt(row.File)}}
	switch row.Kind {
	case apdgSubst:
		settle = append(settle, ledger.Field{Key: "decision_id", Value: row.src["decision_id"]}, ledger.Field{Key: "content_hash", Value: row.src["content_hash"]})
	case apdgDeferred:
		settle = append(settle, ledger.Field{Key: "decision_id", Value: row.src["decision_id"]})
	case apdgWaiver:
		settle = append(settle, ledger.Field{Key: "finding", Value: row.src["finding"]})
	case apdgConfig:
		settle = append(settle, ledger.Field{Key: "approval_id", Value: row.approvalID}, ledger.Field{Key: "key", Value: row.src["key"]})
	}
	settle = append(settle, ledger.Field{Key: "by", Value: "human"}, ledger.Field{Key: "ts", Value: autopilotStamp(now)})
	var said string
	if sub == "revert" && row.Kind == apdgConfig {
		said, err = apdgRevertConfig(home, run, row, settle, now, notOpen)
	} else {
		said, err = apdgWrite(home, sub, run, id, row, settle, notOpen, activeGrant)
	}
	if err != nil {
		return autopilotReport(o, sub, err)
	}
	if asJSON {
		data, _ := ledger.Stringify(settle, false)
		o.out(string(data) + "\n")
		return 0
	}
	past := "ratified"
	if sub == "revert" {
		past = "reverted"
	}
	o.out(fmt.Sprintf("run %s: %s %s %s (by: human)\n", run, row.Kind, id, past))
	if said != "" {
		o.out(said)
	}
	return 0
}

func apdgOpt(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// apdgWrite writes ratify, and every revert but a config one, under the run
// lock: the grant and the row are checked again there.
func apdgWrite(home, sub, run, id string, row apdgRow, settle ledger.Object, notOpen, activeGrant string) (string, error) {
	said := ""
	err := state.WithRunLock(home, run, func() error {
		current, all, key, err := apdgRead(home, run)
		if err != nil {
			return err
		}
		if g := blockObject(current.Get("autopilot")); g != nil {
			if s, err := autopilot.GrantState(g, apdgNow()); err != nil || s == "active" {
				return &runFail{activeGrant, false}
			}
		}
		again, found := apdgFind(apdgCollect(run, current, all, key), id)
		if !found || again.Kind != row.Kind {
			return &runFail{notOpen, false}
		}
		rows := []ledger.Object{settle}
		if sub == "revert" {
			if current.Get("status") != "active" {
				return &runFail{fmt.Sprintf("run %s has ended; only ratify is left; nothing was recorded", run), false}
			}
			switch row.Kind {
			case apdgDeferred:
				blocks := blockObject(current.Get("blocks"))
				entry := blockObject(blocks.Get(row.Block))
				checks, _ := entry.Get("human_checks").([]any)
				entry.Set("status", "awaiting_human_check")
				blocks.Set(row.Block, entry)
				current.Set("blocks", blocks)
				rows = append(rows, ledger.Object{{Key: "event", Value: "block.awaiting_human_check"}, {Key: "block", Value: row.Block}, {Key: "checks", Value: checks}})
				said = fmt.Sprintf("run %s: block %s is awaiting_human_check again (block human-pass, or run human-checks clear, ends it)\n", run, row.Block)
			case apdgWaiver:
				rows = append(rows, ledger.Object{{Key: "event", Value: "review.unwaived"}, {Key: "block", Value: row.Block}, {Key: "file", Value: row.File},
					{Key: "finding", Value: row.src["finding"]}, {Key: "reason", Value: "autopilot revert " + id}, {Key: "by", Value: "human"}})
				said = fmt.Sprintf("run %s: finding %s of %s is open again in block %s\n", run, apdgText(row.src["finding"]), row.File, row.Block)
			case apdgSubst:
				said = fmt.Sprintf("run %s: the substitute review of %s no longer counts at close; a normal review: code-forge review-file %s --block %s --run %s --fresh\n", run, row.File, row.File, row.Block, run)
			}
		}
		for _, r := range rows {
			if _, err := autopilotWriteSigned(home, run, r); err != nil {
				return err
			}
		}
		if sub == "revert" && row.Kind == apdgDeferred {
			return state.SaveRun(home, current)
		}
		return nil
	})
	var se *state.Error
	if errors.As(err, &se) {
		return "", &runFail{se.Message, false}
	}
	return said, err
}

// apdgRevertConfig brings an approval's old value back now: its window
// ends now and checkApprovals' restore (autopilot_restore.go) runs. A
// restore Go cannot run is refused before anything changes.
func apdgRevertConfig(home, run string, row apdgRow, settle ledger.Object, now time.Time, notOpen string) (string, error) {
	now = now.Truncate(time.Millisecond)
	end := func(record ledger.Object) bool {
		values, at := autopilotStored(record, row.approvalID)
		if at < 0 || blockObject(values[at]).Get("status") != "active" {
			return false
		}
		a := blockObject(values[at]).Clone()
		a.Set("until", autopilotStamp(now))
		values[at] = a
		record.Set("autopilot_approvals", values)
		return true
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return "", err
	}
	trial := record.Clone()
	if values, ok := trial.Get("autopilot_approvals").([]any); ok {
		trial.Set("autopilot_approvals", append([]any{}, values...))
	}
	if !end(trial) {
		return "", &runFail{notOpen, false}
	}
	if _, reason := autopilotRestorePlan(home, run, trial, now); reason != 0 {
		return "", &runFail{fmt.Sprintf("revert-config-unsupported: approval %s cannot be restored here (%s); nothing was recorded", row.approvalID, handoverReasons[reason]), false}
	}
	err = state.WithRunLock(home, run, func() error {
		latest, err := state.ReadRun(home, run)
		if err != nil {
			return err
		}
		if !end(latest) {
			return &runFail{notOpen, false}
		}
		return state.SaveRun(home, latest)
	})
	if err != nil {
		return "", err
	}
	latest, err := state.ReadRun(home, run)
	if err != nil {
		return "", err
	}
	check, restoreErr, reason := autopilotRestore(home, run, latest, now)
	if reason != 0 {
		return "", &runFail{fmt.Sprintf("approval %s now ends at %s; the next autopilot command restores it (%s)", row.approvalID, autopilotStamp(now), handoverReasons[reason]), false}
	}
	if restoreErr != nil {
		return "", restoreErr
	}
	said := check.text(true)
	restored := false
	for _, id := range append(append([]string{}, check.restored...), check.skipped...) {
		restored = restored || id == row.approvalID
	}
	if !restored {
		return "", &runFail{strings.TrimRight(said, "\n") + "; the revert is not recorded yet", false}
	}
	if _, err := autopilotWriteSigned(home, run, settle); err != nil {
		return "", err
	}
	return said, nil
}

// apdgSubstReverted (B635): a verified autopilot.revert row names the
// substitute review of file at hash in block: it no longer counts at close,
// and review-file --fresh re-runs it.
func apdgSubstReverted(rows []gatecheck.Row, run, block, file, hash string, key []byte) bool {
	for _, r := range rows {
		if r["event"] == "autopilot.revert" && r["kind"] == apdgSubst && r["by"] == "human" && r["run"] == run && r["block"] == block &&
			r["file"] == file && r["content_hash"] == hash && gatecheck.Verified(r, key) {
			return true
		}
	}
	return false
}

// apdgSubstRevertedRun reads the rows of run for apdgSubstReverted; an
// unreadable run or ledger is no revert.
func apdgSubstRevertedRun(home, run, block, file, hash string) bool {
	record, all, key, err := apdgRead(home, run)
	if err != nil || record == nil || key == nil {
		return false
	}
	rows := []gatecheck.Row{}
	for _, v := range all {
		if r, ok := v.(map[string]any); ok {
			rows = append(rows, r)
		}
	}
	return apdgSubstReverted(rows, run, block, file, hash, key)
}
