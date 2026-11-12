package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B478: `autopilot level` in Go (runAction in src/cli/autopilot.mjs and
// chooseCoderLevel in src/autopilot/actions.mjs): the coder level for a
// block from its recorded lane and the plan's level, written as a signed
// autopilot.level row once the delegate chose exactly that level.

const levelScope = "model:choose"

var (
	levelBlockID   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	levelToken     = regexp.MustCompile(`\bL[0-3]\b`)
	levelLaneExact = regexp.MustCompile(`^L[0-3]$`)
	levelHead      = regexp.MustCompile(`^(#{1,6})\s+(.*)$`)
	levelRule      = regexp.MustCompile(`^\s*\|[\s|:-]+\|\s*$`)
	levelDispatch  = regexp.MustCompile(`(?i)dispatch order[^:]*:`)
)

// levelJSOnlySpace: white space JavaScript's \s matches and Go's does not.
func levelJSOnlySpace(r rune) bool {
	return r == '\v' || r == '\uFEFF' || r != '\u0085' && r > 0x7f && unicode.IsSpace(r)
}

// jsTrim is String.prototype.trim (see jsTrimEmpty).
func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return r == '\uFEFF' || r != '\u0085' && unicode.IsSpace(r) })
}

// levelPlain is plan-check's plain(): no ** and trimmed.
func levelPlain(s string) string { return jsTrim(strings.ReplaceAll(s, "**", "")) }

// levelCells is cells(): the cells of a table row, `\|` kept inside a cell.
func levelCells(line string) []string {
	text := jsTrim(line)
	parts := []string{}
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '|' && (i == 0 || text[i-1] != '\\') {
			parts = append(parts, text[start:i])
			start = i + 1
		}
	}
	parts = append(parts, text[start:])
	if len(parts) < 2 {
		return []string{}
	}
	out := []string{}
	for _, part := range parts[1 : len(parts)-1] {
		out = append(out, jsTrim(part))
	}
	return out
}

// levelPlanEntry is parsePlan(text).blocks.find(b => b.id === block): found,
// and the block's level ("" for none). known=false when Go could read the
// table differently from Node (non-ASCII header text, whose case folding
// differs).
func levelPlanEntry(text, block string) (found bool, level string, known bool) {
	if strings.ContainsFunc(text, levelJSOnlySpace) {
		return false, "", false
	}
	lines := strings.Split(text, "\n")
	for i := range lines {
		lines[i] = strings.TrimSuffix(lines[i], "\r")
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if levelHead.MatchString(line) || levelDispatch.MatchString(levelPlain(line)) {
			continue
		}
		if !strings.HasPrefix(jsTrim(line), "|") || i+1 >= len(lines) || !levelRule.MatchString(lines[i+1]) {
			continue
		}
		header := []string{}
		for _, cell := range levelCells(line) {
			h := levelPlain(cell)
			for _, r := range h {
				if r >= utf8.RuneSelf {
					return false, "", false
				}
			}
			header = append(header, strings.ToLower(h))
		}
		col := func(name string) int {
			for k, h := range header {
				if h == name || strings.HasPrefix(h, name+" ") || strings.HasPrefix(h, name+"(") {
					return k
				}
			}
			return -1
		}
		prefixed := func(name string) int {
			for k, h := range header {
				if strings.HasPrefix(h, name) {
					return k
				}
			}
			return -1
		}
		idCol, ownedCol, accCol := col("id"), prefixed("owned_files"), prefixed("acceptance")
		if idCol < 0 || ownedCol < 0 || accCol < 0 {
			continue
		}
		levelCol := col("level")
		j := i + 2
		for ; j < len(lines) && strings.HasPrefix(jsTrim(lines[j]), "|"); j++ {
			row := levelCells(lines[j])
			at := func(c int) string {
				if c >= 0 && c < len(row) {
					return row[c]
				}
				return ""
			}
			id := levelPlain(at(idCol))
			if id == "" || id != block {
				continue
			}
			return true, levelToken.FindString(levelPlain(at(levelCol))), true
		}
		i = j - 1
	}
	return false, "", true
}

// jsBasename is path.basename (POSIX).
func jsBasename(p string) string {
	p = strings.TrimRight(p, "/")
	if p == "" {
		return ""
	}
	return p[strings.LastIndex(p, "/")+1:]
}

type levelLane struct {
	lane, source string
	decisionID   any
}

// levelRecordedLane is recordedLanes(rows, planName).get(block); known=false
// when a counted answer is not ASCII (toUpperCase differs from Go's).
func levelRecordedLane(rows []ledger.Row, planName, block string) (lane *levelLane, known bool) {
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if !ok || row["event"] != "decision" || row["question"] != "lane" || row["block"] != block {
			continue
		}
		answer, isText := row["answer"].(string)
		if (row["source"] != "jev" && row["source"] != "rules") || !isText {
			continue
		}
		if plan, has := row["plan"]; has {
			name, isName := plan.(string)
			if !isName || jsBasename(name) != jsBasename(planName) {
				continue
			}
		}
		for _, r := range answer {
			if r >= utf8.RuneSelf {
				return nil, false
			}
		}
		normalised := jsTrim(answer)
		if token := levelToken.FindString(strings.ToUpper(normalised)); token != "" {
			normalised = token
		}
		var decisionID any
		if id, isID := row["decision_id"].(string); isID {
			decisionID = id
		}
		lane = &levelLane{normalised, row["source"].(string), decisionID}
	}
	return lane, true
}

// levelPlanPath is path.resolve(cwd, plan).
func levelPlanPath(plan string) string {
	if filepath.IsAbs(plan) {
		return filepath.Clean(plan)
	}
	cwd := bnCwd()
	return filepath.Clean(filepath.Join(cwd, plan))
}

// levelUsage is need('plan'), need('block'), need('decision') and the
// usage checks of chooseCoderLevel, in their order.
func levelUsage(flags autopilotFlags) (planText, plan string, found bool, level string, err error) {
	for _, name := range []string{"plan", "block", "decision"} {
		if value, ok := flags.values[name]; !ok || jsTrimEmpty(value) {
			return "", "", false, "", autopilotUsageFail("autopilot level needs --" + name)
		}
	}
	block := flags.values["block"]
	if !levelBlockID.MatchString(block) {
		return "", "", false, "", autopilotUsageFail("autopilot level needs --block <id>")
	}
	if !roundDecisionID.MatchString(flags.values["decision"]) {
		return "", "", false, "", autopilotUsageFail("autopilot level needs --decision <id> (the delegate's acted decision from autopilot ask)")
	}
	plan = levelPlanPath(flags.values["plan"])
	data, readErr := os.ReadFile(plan)
	if readErr != nil {
		return "", plan, false, "", autopilotUsageFail("--plan " + plan + " cannot be read")
	}
	found, level, _ = levelPlanEntry(string(data), block)
	if !found {
		return "", plan, false, "", autopilotUsageFail("block " + block + " is not in the plan's block table")
	}
	if level == "" {
		return "", plan, true, "", autopilotUsageFail("block " + block + " has no level (L0–L3) in the plan")
	}
	return string(data), plan, true, level, nil
}

// levelSupported says whether Go reads this level the way Node does: a
// plan file that is UTF-8 with an ASCII header, ASCII lane answers, a
// readable key and a named decision row Go compares the same.
func levelSupported(home, runID string, record ledger.Object, flags autopilotFlags, rows []ledger.Row) bool {
	if !levelPlanKnown(flags) {
		return false
	}
	if plan, ok := flags.values["plan"]; ok && !jsTrimEmpty(plan) {
		if _, known := levelRecordedLane(rows, levelPlanPath(plan), flags.values["block"]); !known {
			return false
		}
	}
	return roundSupported(home, runID, record, autopilotFlags{values: map[string]string{"block": flags.values["block"], "decision": flags.values["decision"]}}, rows)
}

// levelPlanKnown: the plan file, when it reads, is UTF-8 (readFile would
// replace bad bytes) and its block table reads the same in Go.
func levelPlanKnown(flags autopilotFlags) bool {
	plan, ok := flags.values["plan"]
	if !ok || jsTrimEmpty(plan) {
		return true
	}
	data, err := os.ReadFile(levelPlanPath(plan))
	if err != nil {
		return true
	}
	if !utf8.Valid(data) {
		return false
	}
	_, _, known := levelPlanEntry(string(data), flags.values["block"])
	return known
}

func levelIndex(level string) int { n, _ := strconv.Atoi(level[1:]); return n }

// autopilotLevel is runAction('level').
func autopilotLevel(o *runOut, home, runID string, flags autopilotFlags) int {
	report := func(err error) int {
		if refused, ok := err.(*roundRefused); ok {
			o.err("autopilot level: refused — " + refused.message + "\n")
			return 1
		}
		return autopilotReport(o, "level", err)
	}
	clock := func() time.Time { return autopilotNow().Truncate(time.Millisecond) }
	_, plan, _, planLevel, err := levelUsage(flags)
	if err != nil {
		return report(err)
	}
	block, decisionID := flags.values["block"], flags.values["decision"]
	record, err := state.ReadRun(home, runID)
	if err != nil {
		return report(err)
	}
	now := clock()
	grant, err := roundGrantFor(home, runID, levelScope, now)
	if err != nil {
		return report(err)
	}
	grantID, _ := grant.Get("grant_id").(string)
	project, _ := record.Get("project").(string)
	all, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), project)
	if err != nil {
		return report(err)
	}
	key, err := rebaseKey(home, runID)
	if err != nil {
		return report(err)
	}
	autopilotRegisterKey(home, runID) // loadKey registers the key
	runRows := []map[string]any{}
	for _, value := range all {
		if row, ok := value.(map[string]any); ok && row["run"] == runID {
			runRows = append(runRows, row)
		}
	}
	recorded, _ := levelRecordedLane(all, plan, block)
	if recorded == nil {
		return report(&roundRefused{"block " + block + " has no recorded lane — run forge jev ask lane --block " + block + "; the level goes to the owner"})
	}
	if !levelLaneExact.MatchString(recorded.lane) {
		quoted, _ := ledger.Stringify(recorded.lane, false)
		return report(&roundRefused{"block " + block + "'s recorded lane is " + string(quoted) + ", not a level; the level goes to the owner"})
	}
	level := "L" + strconv.Itoa(min(levelIndex(recorded.lane), levelIndex(planLevel)+1, 2))
	noDecision := func(problem string) error {
		return &roundRefused{problem + "; the question goes to the owner (autopilot ask, then --decision <id>)"}
	}
	if problem := roundDecisionProblem(runRows, decisionID, grantID, levelScope, level, block, "", now.UnixMilli(), key); problem != "" {
		return report(noDecision(problem))
	}
	recordRun, _ := record.Get("run_id").(string)
	var row ledger.Object
	err = state.WithRunLock(home, runID, func() error {
		fresh := clock()
		if err := roundLiveRefusal(home, runID, grantID, levelScope, fresh); err != nil {
			return err
		}
		rows, err := roundRunRows(home, project, recordRun)
		if err != nil {
			return err
		}
		if problem := roundDecisionProblem(rows, decisionID, grantID, levelScope, level, block, "", fresh.UnixMilli(), key); problem != "" {
			return noDecision(problem)
		}
		row, err = autopilotWriteSigned(home, runID, ledger.Object{
			{Key: "event", Value: "autopilot.level"},
			{Key: "block", Value: block},
			{Key: "level", Value: level},
			{Key: "lane", Value: recorded.lane},
			{Key: "lane_source", Value: recorded.source},
			{Key: "lane_decision_id", Value: recorded.decisionID},
			{Key: "plan_level", Value: planLevel},
			{Key: "grant_id", Value: grantID},
			{Key: "decision_id", Value: decisionID},
			{Key: "ts", Value: autopilotStamp(fresh)},
		})
		return err
	})
	if err != nil {
		return report(err)
	}
	o.out("autopilot level run " + runID + ": block " + jsString(row.Get("block")) + " codes at " + jsString(row.Get("level")) + " (lane " + jsString(row.Get("lane")) + ", plan " + jsString(row.Get("plan_level")) + ", grant " + jsString(row.Get("grant_id")) + "); block open without --level uses it\n")
	return 0
}
