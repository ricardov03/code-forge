package main

import (
	"crypto/rand"
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
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/runstatus"
	"github.com/ricardov03/code-forge/native/state"
)

// B461: `code-forge autopilot` in Go (src/cli/autopilot.mjs). Help, usage
// errors and flag errors of every subcommand, status (no grant, or a grant
// with a stored link), stop (refusals, and a grant with a stored link) and
// start (--yes, or the no-terminal refusal) run here. B478 adds the terminal
// prompt of start, binnacle and log (autopilot_binnacle.go) and the binnacle
// files that status and stop write for a grant with no link. Ask, waive,
// round, level and approve keep their hand-over to Node with their own
// reasons, and so does a binnacle whose data Go cannot read the way Node does.
func init() { routes["autopilot"] = runAutopilot }

const autopilotUsage = "usage: code-forge autopilot start --run <id> --until <ISO-8601 with offset> --delegate <L2|L3> --allow <scope,…> [--deny <scope,…>] [--budget <category>=<usd>,…] [--stop-at <0..1>] [--yes] | status --run <id> [--json] | stop --run <id> | ask --run <id> --scope <scope> --question <text> [--options a,b,…] [--context-file <path>] [--block <id>] [--file <path>] [--finding <id>] [--json] (options for an action: waive,fix · allow,deny · L1,L2) | waive --run <id> --block <id> --file <path> --finding <id> --severity <warning|nit> --reason <text> --decision <id> | round --run <id> --block <id> --file <path> --decision <id> | level --run <id> --block <id> --plan <file> --decision <id> | approve --run <id> --key <dot.path> --value <json> --until <ISO-8601 with offset> | binnacle --run <id> [--json|--markdown] [--link <https url>] | log --run <id> [--json|--markdown]\n"

const autopilotApproveNoYes = "autopilot approve: --yes is not accepted — only the owner approves a limit change, at a terminal; a flag could be passed by the orchestrator or a script, so approve always asks"

// autopilotSubUsage is SUB_USAGE: one usage line per subcommand.
var autopilotSubUsage = func() map[string]string {
	out := map[string]string{}
	body := strings.TrimPrefix(strings.TrimRight(autopilotUsage, "\n"), "usage: code-forge autopilot ")
	for _, line := range strings.Split(body, " | ") {
		word, _, _ := strings.Cut(line, " ")
		out[word] = "usage: code-forge autopilot " + line + "\n"
	}
	return out
}()

var autopilotSubs = []string{"start", "status", "stop", "ask", "waive", "round", "level", "approve", "binnacle", "log"}

var autopilotSpecs = map[string][2][]string{
	"start":    {{"run", "until", "delegate", "allow", "deny", "budget", "stop-at"}, {"yes"}},
	"status":   {{"run"}, {"json"}},
	"stop":     {{"run"}, nil},
	"ask":      {{"run", "scope", "question", "options", "context-file", "block", "file", "finding"}, {"json"}},
	"waive":    {{"run", "block", "file", "finding", "severity", "reason", "decision"}, nil},
	"round":    {{"run", "block", "file", "decision"}, nil},
	"level":    {{"run", "block", "plan", "decision"}, nil},
	"approve":  {{"run", "key", "value", "until"}, nil},
	"binnacle": {{"run", "link"}, {"json", "markdown"}},
	"log":      {{"run"}, {"json", "markdown"}},
}

// Seams for tests: the clock, the terminal probe and the grant id.
var (
	autopilotNow   = time.Now
	autopilotIsTTY = stdinIsTerminal
	// autopilotPromptable: both descriptors are terminals (humanask renders there).
	autopilotPromptable = humanask.Available
	autopilotConfirm    = humanask.Confirm
	autopilotGrantID    = func() (string, error) {
		b := make([]byte, 4)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		return "ap-" + hex.EncodeToString(b), nil
	}
)

type autopilotFlags struct {
	values map[string]string
	bools  map[string]bool
}

// autopilotParse is parseFlags (src/state/cli-args.mjs) with the subcommand's
// spec, then the positional and --run checks of runAutopilot.
func autopilotParse(sub string, args []string) (autopilotFlags, error) {
	spec := autopilotSpecs[sub]
	f := autopilotFlags{values: map[string]string{}, bools: map[string]bool{}}
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			positionals = append(positionals, arg)
			continue
		}
		name, inline, hasInline := strings.Cut(arg[2:], "=")
		boolean := slices.Contains(spec[1], name)
		if !boolean && !slices.Contains(spec[0], name) {
			quoted, _ := ledger.Stringify(arg, false)
			return f, &runFail{"unknown flag " + string(quoted), true}
		}
		if _, seen := f.values[name]; seen || f.bools[name] {
			return f, &runFail{"--" + name + " given more than once", true}
		}
		switch {
		case boolean:
			if hasInline {
				return f, &runFail{"--" + name + " takes no value", true}
			}
			f.bools[name] = true
		case hasInline:
			if inline == "" {
				return f, &runFail{"--" + name + " needs a value", true}
			}
			f.values[name] = inline
		default:
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return f, &runFail{"--" + name + " needs a value", true}
			}
			f.values[name] = args[i+1]
			i++
		}
	}
	if len(positionals) > 0 {
		quoted, _ := ledger.Stringify(positionals[0], false)
		return f, &runFail{"unexpected argument " + string(quoted), true}
	}
	if _, ok := f.values["run"]; !ok {
		return f, &runFail{"autopilot " + sub + " needs --run <id>", true}
	}
	return f, nil
}

func autopilotHandover(args []string, reason handoverReason) int {
	code, err := handover(append([]string{"autopilot"}, args...), reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}

func runAutopilot(args []string) int {
	help := func(s string) bool { return s == "--help" || s == "-h" }
	// The Node router answers help before breadcrumbs and handlers.
	if len(args) > 0 && help(args[0]) {
		fmt.Fprint(os.Stdout, autopilotUsage)
		return 0
	}
	if len(args) > 1 && help(args[1]) {
		if usage, ok := autopilotSubUsage[args[0]]; ok {
			fmt.Fprint(os.Stdout, usage)
			return 0
		}
	}
	home := os.Getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return autopilotHandover(args, hAutopilotHome)
	}
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	known := slices.Contains(autopilotSubs, sub)
	var parseErr error
	var plan func(o *runOut) int
	if known {
		if sub == "approve" && slices.ContainsFunc(args[1:], func(a string) bool { return a == "--yes" || strings.HasPrefix(a, "--yes=") }) {
			plan = func(o *runOut) int { o.err(autopilotApproveNoYes + "\n"); return 2 }
		} else {
			var flags autopilotFlags
			flags, parseErr = autopilotParse(sub, args[1:])
			if parseErr == nil {
				var reason handoverReason
				plan, reason = autopilotPlan(home, sub, flags)
				if plan == nil {
					return autopilotHandover(args, reason)
				}
			}
		}
	}
	crumb, logSub := "autopilot", ""
	if sub == "start" || sub == "status" || sub == "stop" {
		crumb, logSub = "autopilot "+sub, sub
	}
	before := gatesBreadcrumbName(crumb)
	o := &runOut{}
	code := 0
	switch {
	case !known:
		o.err(autopilotUsage)
		code = 2
	case parseErr != nil:
		code = autopilotReport(o, sub, parseErr)
	default:
		code = plan(o)
	}
	if code != 0 {
		var message any
		if o.last != "" {
			message = strings.TrimSpace(o.last)
		}
		nativeVerbFailure("autopilot", logSub, args, code, message, before)
	}
	return code
}

// autopilotReport prints a caught error as `autopilot <sub>: <message>`.
func autopilotReport(o *runOut, sub string, err error) int {
	var fail *runFail
	var stateErr *state.Error
	switch {
	case errors.As(err, &fail):
		o.err(fmt.Sprintf("autopilot %s: %s\n", sub, fail.msg))
		if fail.usage {
			return 2
		}
		return 1
	case errors.As(err, &stateErr):
		o.err(fmt.Sprintf("autopilot %s: %s\n", sub, stateErr.Message))
		return 1
	default:
		o.err(fmt.Sprintf("autopilot %s: %s\n", sub, blockIOMessage(err)))
		return 1
	}
}

var autopilotSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// autopilotPlan reads the record once, before any write, and either returns
// the Go step or the reason Node must run the command.
func autopilotPlan(home, sub string, flags autopilotFlags) (func(o *runOut) int, handoverReason) {
	switch sub {
	case "ask":
		return nil, hAutopilotAsk
	case "waive", "round", "level":
		return nil, hAutopilotAction
	case "approve":
		return nil, hAutopilotApprove
	}
	if sub == "start" && !flags.bools["yes"] && autopilotIsTTY() && !autopilotPromptable() {
		return nil, hAutopilotPrompt // stdin is a terminal but stdout is not: clack's output stays with Node
	}
	runID := flags.values["run"]
	now := autopilotNow().Truncate(time.Millisecond) // a JavaScript Date holds ms
	record, err := state.ReadRun(home, runID)
	if err != nil {
		var stateErr *state.Error
		if !errors.As(err, &stateErr) {
			return nil, hAutopilotRecord
		}
		// checkApprovals answers nothing for no-run and bad-run-id; the
		// subcommand's own read then refuses (start validates its input first).
		return func(o *runOut) int {
			if (sub == "binnacle" || sub == "log") && flags.bools["json"] && flags.bools["markdown"] {
				return autopilotReport(o, sub, autopilotUsageFail("give --json or --markdown, not both"))
			}
			if sub == "start" {
				if _, invalid := autopilotValidate(flags, now); invalid != nil {
					return autopilotReport(o, sub, invalid)
				}
			}
			return autopilotReport(o, sub, err)
		}, 0
	}
	if project, ok := record.Get("project").(string); !ok || !autopilotSlug.MatchString(project) {
		return nil, hAutopilotRecord
	}
	if _, ok := record.Get("workspace").(string); !ok {
		return nil, hAutopilotRecord
	}
	read, err := autopilot.Read(record, now)
	if err != nil {
		return nil, hAutopilotShape // approvals to restore, or a grant shape Go does not read
	}
	files := read.Grant != nil && !runTruthy(read.Grant.Get("link")) && (sub == "status" || sub == "stop" && read.State == "active")
	if files || sub == "binnacle" || sub == "log" || sub == "status" && read.Grant != nil {
		rows, ok := autopilotRows(home, record, runID)
		if !ok {
			return nil, hAutopilotLedger
		}
		// the Markdown files and the binnacle read every row: one Go reads
		// differently from Node keeps the command on Node
		if (files || sub == "binnacle" || sub == "log") && !bnSupported(home, runID, rows, record, now) {
			if files {
				return nil, hAutopilotFiles
			}
			return nil, hAutopilotBinnacle
		}
	}
	if text, has := flags.values["link"]; has && sub == "binnacle" {
		if _, _, known := bnParseLink(text); !known {
			return nil, hAutopilotBinnacle // a URL whose WHATWG form Go does not know
		}
	}
	switch sub {
	case "status":
		return func(o *runOut) int { return autopilotStatus(o, home, runID, flags, now) }, 0
	case "stop":
		return func(o *runOut) int { return autopilotStop(o, home, runID, now) }, 0
	case "binnacle", "log":
		return func(o *runOut) int { return autopilotBinnacle(o, home, runID, sub, flags) }, 0
	}
	return func(o *runOut) int { return autopilotStart(o, home, runID, flags, now) }, 0
}

// autopilotRows reads the project's ledger. ok is false when Node could
// read it differently: an unreadable ledger, or a spend row whose ts Go
// cannot date the way Date.parse does.
func autopilotRows(home string, record ledger.Object, runID string) ([]ledger.Row, bool) {
	project, _ := record.Get("project").(string)
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), project)
	if err != nil {
		return nil, false
	}
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if !ok || !autopilotSpendRow(row) || row["run"] != runID {
			continue
		}
		if ts, isText := row["ts"].(string); isText {
			if _, ok := autopilotParseTS(ts); !ok {
				return nil, false
			}
		}
	}
	return rows, true
}

func autopilotSpendRow(row map[string]any) bool {
	event := row["event"]
	return (event == "session" || event == "session.background") && row["source"] != "shadow"
}

// autopilotParseTS dates the ISO forms rows carry (Date.parse agrees on them).
func autopilotParseTS(ts string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil || !strings.Contains(ts, "T") {
		return time.Time{}, false
	}
	return t, true
}

var autopilotRoleCategory = map[string]string{"coder": "coding", "reviewer": "review", "judge": "review", "s2": "review", "delegate": "review", "author": "review", "facts": "review"}

// autopilotCategorySpend is categorySpend (src/autopilot/limits.mjs).
func autopilotCategorySpend(rows []ledger.Row, runID, category string, since time.Time) float64 {
	sum := 0.0
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if !ok || !autopilotSpendRow(row) || row["run"] != runID {
			continue
		}
		role, _ := row["role"].(string)
		if c, ok := autopilotRoleCategory[role]; !ok || c != category {
			continue
		}
		if ts, isText := row["ts"].(string); isText {
			if t, ok := autopilotParseTS(ts); ok && t.Before(since) {
				continue
			}
		}
		if cost, known := runstatus.RowUSD(row); known {
			sum += cost
		}
	}
	return runstatus.RoundUSD(sum)
}

var autopilotFixedDeny = ledger.Object{
	{Key: "waive:critical", Value: "waive a critical finding"},
	{Key: "waive:proof", Value: "waive a proof finding"},
	{Key: "reviews:skip", Value: "close a block without its reviews (--no-require-reviews)"},
	{Key: "limits:change", Value: "change a limit or rule key (budget.*, review.budgets, review.block_budget_tokens, review.block_budget_usd, review.max_rounds_per_file, review.single_reviewer_max_risk, review.multimodel, review.closed_book, review.allow_open_book_codex, thresholds.*, escalation.*, proof.*, system1.*, production.*, autopilot.*)"},
	{Key: "plan:approve", Value: "approve a plan"},
	{Key: "design:approve", Value: "approve a design"},
	{Key: "pr:merge", Value: "merge a pull request"},
	{Key: "destructive", Value: "run a destructive action"},
	{Key: "budget:raise", Value: "raise a budget"},
}

var autopilotAllowable = []string{"waive:warning", "waive:nit", "round:extra", "model:choose", "close:routine"}

func autopilotFixedDenyScopes() []any {
	out := []any{}
	for _, f := range autopilotFixedDeny {
		out = append(out, f.Key)
	}
	return out
}

func autopilotText(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if v == nil {
		return "null"
	}
	b, _ := ledger.Stringify(v, false)
	return string(b)
}

// autopilotField is `${grant.field}`: undefined when the key is absent.
func autopilotField(o ledger.Object, key string) string {
	if !runHas(o, key) {
		return "undefined"
	}
	return autopilotText(o.Get(key))
}

func autopilotLeft(ms int64) string {
	minutes := ms / 60000
	if ms < 0 {
		minutes = 0
	}
	return fmt.Sprintf("%dh %dm", minutes/60, minutes%60)
}

func autopilotCaps(caps ledger.Object) string {
	keys := []string{}
	for _, f := range caps {
		keys = append(keys, f.Key)
	}
	sortUTF16Strings(keys)
	if len(keys) == 0 {
		return "no caps"
	}
	parts := []string{}
	for _, k := range keys {
		n, _ := caps.Get(k).(float64)
		parts = append(parts, k+" "+statusFixed(n)+" USD")
	}
	return strings.Join(parts, ", ")
}

func sortUTF16Strings(list []string) {
	slices.SortFunc(list, func(a, b string) int {
		return slices.Compare(utf16Units(a), utf16Units(b))
	})
}

func utf16Units(s string) []uint16 {
	out := []uint16{}
	for _, r := range s {
		if r >= 0x10000 {
			r -= 0x10000
			out = append(out, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
		} else {
			out = append(out, uint16(r))
		}
	}
	return out
}

// autopilotPercent is Math.round(stop_at * 100).
func autopilotPercent(v any) string {
	n, _ := v.(float64)
	x := n * 100
	r := math.Floor(x)
	if x-r >= 0.5 {
		r++
	}
	return strconv.FormatFloat(r, 'f', -1, 64)
}

func autopilotStrings(v any) []string {
	out := []string{}
	list, _ := v.([]any)
	for _, item := range list {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

func autopilotStatus(o *runOut, home, runID string, flags autopilotFlags, now time.Time) int {
	checked, err := autopilot.CheckExpiry(home, runID, now)
	if err != nil {
		return autopilotReport(o, "status", err)
	}
	grant := checked.Grant
	data := ledger.Object{{Key: "run", Value: runID}}
	if grant == nil {
		data = append(data, ledger.Object{
			{Key: "state", Value: "none"}, {Key: "grant_id", Value: nil}, {Key: "delegate", Value: nil}, {Key: "until", Value: nil},
			{Key: "time_left_s", Value: nil}, {Key: "scopes", Value: []any{}}, {Key: "deny", Value: []any{}},
			{Key: "fixed_deny", Value: autopilotFixedDenyScopes()}, {Key: "caps", Value: ledger.Object{}}, {Key: "stop_at", Value: nil},
			{Key: "spend", Value: ledger.Object{}}, {Key: "paused", Value: []any{}}, {Key: "link", Value: nil}, {Key: "files", Value: nil},
			{Key: "started_at", Value: nil}, {Key: "stopped_at", Value: nil}, {Key: "expired_at", Value: nil},
		}...)
		if flags.bools["json"] {
			out, _ := ledger.Stringify(data, true)
			o.out(string(out) + "\n")
		} else {
			o.out("autopilot run " + runID + ": no grant\n")
		}
		return 0
	}
	record, err := state.ReadRun(home, runID)
	if err != nil {
		return autopilotReport(o, "status", err)
	}
	project, _ := record.Get("project").(string)
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), project)
	if err != nil {
		return autopilotReport(o, "status", err)
	}
	since, _ := time.Parse("2006-01-02T15:04:05.000Z", autopilotText(grant.Get("started_at")))
	spend := ledger.Object{
		{Key: "coding", Value: autopilotCategorySpend(rows, runID, "coding", since)},
		{Key: "review", Value: autopilotCategorySpend(rows, runID, "review", since)},
	}
	files := autopilotRecordFiles(o, home, runID, grant, now)
	stateName := checked.State
	if stateName != "run-ended" {
		stateName, _ = autopilot.GrantState(grant, now)
	}
	until, _ := time.Parse("2006-01-02T15:04:05.000Z", autopilotText(grant.Get("until")))
	left := 0.0
	if stateName == "active" {
		left = math.Floor(float64(until.UnixMilli()-now.UnixMilli()) / 1000)
	}
	paused := []string{}
	if p, ok := grant.Get("paused").(ledger.Object); ok {
		for _, f := range p {
			paused = append(paused, f.Key)
		}
	}
	sortUTF16Strings(paused)
	pausedAny := []any{}
	for _, p := range paused {
		pausedAny = append(pausedAny, p)
	}
	data = append(data, ledger.Object{
		{Key: "state", Value: stateName}, {Key: "grant_id", Value: grant.Get("grant_id")}, {Key: "delegate", Value: grant.Get("delegate")},
		{Key: "until", Value: grant.Get("until")}, {Key: "time_left_s", Value: left}, {Key: "scopes", Value: grant.Get("scopes")},
		{Key: "deny", Value: grant.Get("deny")}, {Key: "fixed_deny", Value: autopilotFixedDenyScopes()}, {Key: "caps", Value: grant.Get("caps")},
		{Key: "stop_at", Value: grant.Get("stop_at")}, {Key: "spend", Value: spend}, {Key: "paused", Value: pausedAny},
		{Key: "link", Value: grant.Get("link")}, {Key: "files", Value: autopilotFilesValue(files)}, {Key: "started_at", Value: grant.Get("started_at")},
	}...)
	// JSON.stringify leaves out a key whose value is undefined.
	for _, key := range []string{"stopped_at", "expired_at"} {
		if runHas(grant, key) {
			data = append(data, ledger.Field{Key: key, Value: grant.Get(key)})
		}
	}
	if flags.bools["json"] {
		out, _ := ledger.Stringify(data, true)
		o.out(string(out) + "\n")
		return 0
	}
	untilText := autopilotText(grant.Get("until"))
	var head string
	switch stateName {
	case "active":
		head = "active · " + autopilotLeft(int64(left)*1000) + " left (until " + untilText + ")"
	case "stopped":
		head = "stopped at " + autopilotField(grant, "stopped_at") + " (was until " + untilText + ")"
	case "run-ended":
		head = "the run has ended; the grant no longer acts (was until " + untilText + ")"
	default:
		head = "expired at " + untilText
	}
	caps, _ := grant.Get("caps").(ledger.Object)
	spendParts := []string{}
	for _, c := range []string{"coding", "review"} {
		spent := statusFixed(spend.Get(c).(float64))
		if capValue, ok := caps.Get(c).(float64); ok {
			spendParts = append(spendParts, c+" "+spent+" of "+statusFixed(capValue)+" USD")
		} else {
			spendParts = append(spendParts, c+" "+spent+" USD (no cap)")
		}
	}
	pausedText := "none"
	if len(paused) > 0 {
		pausedText = strings.Join(paused, ", ")
	}
	deny := append(autopilotStrings(grant.Get("deny")), autopilotStrings(autopilotFixedDenyScopes())...)
	o.out("autopilot run " + runID + ": " + head + "\n" +
		"  grant     " + autopilotText(grant.Get("grant_id")) + "\n" +
		"  delegate  " + autopilotText(grant.Get("delegate")) + "\n" +
		"  allow     " + strings.Join(autopilotStrings(grant.Get("scopes")), ", ") + "\n" +
		"  deny      " + strings.Join(deny, ", ") + "\n" +
		"  budget    " + autopilotCaps(caps) + " · stop at " + autopilotPercent(grant.Get("stop_at")) + "%\n" +
		"  spend     " + strings.Join(spendParts, ", ") + " · paused " + pausedText + "\n" +
		autopilotRecordText(grant.Get("link"), files))
	return 0
}

func autopilotStop(o *runOut, home, runID string, now time.Time) int {
	// the expiry check first: an expired grant gets its one expire row and no stop row
	checked, err := autopilot.CheckExpiry(home, runID, now)
	if err != nil {
		return autopilotReport(o, "stop", err)
	}
	if checked.State == "expired" {
		return autopilotReport(o, "stop", &runFail{autopilot.ExpiredMessage(runID, autopilotText(checked.Grant.Get("until"))), false})
	}
	var stopped ledger.Object
	err = state.UpdateRun(home, runID, func(record *ledger.Object) error {
		grant, _ := record.Get("autopilot").(ledger.Object)
		if grant == nil {
			return &runFail{"run " + runID + " has no autopilot grant", false}
		}
		if record.Get("status") != "active" {
			return &runFail{"run " + runID + " has ended", false}
		}
		stateName, err := autopilot.GrantState(grant, now)
		if err != nil {
			return err
		}
		if stateName == "stopped" {
			return &runFail{"run " + runID + "'s autopilot grant was already stopped at " + autopilotField(grant, "stopped_at"), false}
		}
		if stateName == "expired" {
			return &runFail{autopilot.ExpiredMessage(runID, autopilotText(grant.Get("until"))), false}
		}
		if _, err := state.WriteSigned(home, runID, ledger.Object{{Key: "event", Value: "autopilot.stop"}, {Key: "grant_id", Value: grant.Get("grant_id")}, {Key: "ts", Value: autopilotStamp(now)}}); err != nil {
			return err
		}
		grant.Set("status", "stopped")
		grant.Set("stopped_at", autopilotStamp(now))
		record.Set("autopilot", grant)
		stopped = grant
		return nil
	})
	if err != nil {
		return autopilotReport(o, "stop", err)
	}
	o.out("autopilot run " + runID + ": grant " + autopilotText(stopped.Get("grant_id")) + " stopped\n" + autopilotRecordText(stopped.Get("link"), autopilotRecordFiles(o, home, runID, stopped, now)))
	return 0
}

type autopilotGrantInput struct {
	until, delegate string
	scopes, deny    []any
	caps            ledger.Object
	stopAt          float64
}

var (
	autopilotUntilPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{1,3}))?)?(Z|([+-])(\d{2}):(\d{2}))$`)
	autopilotMoney        = regexp.MustCompile(`^(?:\d+(?:\.\d+)?|\.\d+)$`)
)

const autopilotUntilExample = "2026-10-07T07:00:00+02:00"

// autopilotUntilMs is untilMs: the calendar-checked epoch ms, or false.
func autopilotUntilMs(text string) (int64, bool) {
	m := autopilotUntilPattern.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	num := func(i int) int { n, _ := strconv.Atoi(m[i]); return n }
	y, mo, d, h, mi, sec := num(1), num(2), num(3), num(4), num(5), num(6)
	frac := 0
	if m[7] != "" {
		frac, _ = strconv.Atoi((m[7] + "00")[:3])
	}
	local := time.Date(y, time.Month(mo), d, h, mi, sec, frac*int(time.Millisecond), time.UTC)
	if local.Year() != y || int(local.Month()) != mo || local.Day() != d || local.Hour() != h || local.Minute() != mi || local.Second() != sec {
		return 0, false
	}
	ms := local.UnixMilli()
	if m[8] == "Z" {
		return ms, true
	}
	oh, om := num(10), num(11)
	if oh > 23 || om > 59 {
		return 0, false
	}
	sign := int64(1)
	if m[9] == "-" {
		sign = -1
	}
	return ms - sign*int64(oh*60+om)*60000, true
}

// autopilotJSSpace is the WhiteSpace and LineTerminator set of String.prototype.trim.
func autopilotJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xA0, 0x1680, 0x2028, 0x2029, 0x202F, 0x205F, 0x3000, 0xFEFF:
		return true
	}
	return r >= 0x2000 && r <= 0x200A
}

func autopilotItems(text string) []string {
	out := []string{}
	for _, item := range strings.Split(text, ",") {
		if item = strings.TrimFunc(item, autopilotJSSpace); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func autopilotQuote(s string) string { b, _ := ledger.Stringify(s, false); return string(b) }

func autopilotUsageFail(msg string) error { return &runFail{msg, true} }

// autopilotValidate is validateGrantInput (src/autopilot/grant.mjs).
func autopilotValidate(flags autopilotFlags, now time.Time) (autopilotGrantInput, error) {
	var in autopilotGrantInput
	untilText, ok := flags.values["until"]
	if !ok {
		return in, autopilotUsageFail("--until is required: an ISO-8601 time with an offset, e.g. " + autopilotUntilExample)
	}
	ms, ok := autopilotUntilMs(untilText)
	if !ok {
		return in, autopilotUsageFail("--until must be an ISO-8601 time with an offset (Z or ±hh:mm), e.g. " + autopilotUntilExample)
	}
	if ms <= now.UnixMilli() {
		return in, autopilotUsageFail("--until is in the past; give a time after now, at most 24 h ahead")
	}
	if ms-now.UnixMilli() > 24*60*60*1000 {
		return in, autopilotUsageFail("--until is more than 24 h ahead; a grant lasts at most 24 h")
	}
	in.until = autopilotStamp(time.UnixMilli(ms))
	delegate := flags.values["delegate"]
	if delegate != "L2" && delegate != "L3" {
		return in, autopilotUsageFail("--delegate must be L2 or L3")
	}
	in.delegate = delegate
	allowable := strings.Join(autopilotAllowable, ", ")
	in.scopes = []any{}
	for _, scope := range autopilotItems(flags.values["allow"]) {
		if slices.Contains(in.scopes, any(scope)) {
			continue
		}
		in.scopes = append(in.scopes, scope)
	}
	if len(in.scopes) == 0 {
		return in, autopilotUsageFail("--allow is required: one or more of " + allowable)
	}
	for _, s := range in.scopes {
		scope := s.(string)
		if why, fixed := autopilotFixedDeny.Get(scope).(string); fixed && runHas(autopilotFixedDeny, scope) {
			return in, autopilotUsageFail("scope " + scope + " is on the fixed deny list and can never be allowed (" + why + ")")
		}
		if !slices.Contains(autopilotAllowable, scope) {
			return in, autopilotUsageFail("unknown scope " + autopilotQuote(scope) + " in --allow; allowable scopes: " + allowable)
		}
	}
	in.deny = []any{}
	if text, given := flags.values["deny"]; given {
		named := []string{}
		for _, scope := range autopilotItems(text) {
			if !slices.Contains(named, scope) {
				named = append(named, scope)
			}
		}
		if len(named) == 0 {
			return in, autopilotUsageFail("--deny needs one or more scopes")
		}
		for _, scope := range named {
			if !slices.Contains(autopilotAllowable, scope) && !runHas(autopilotFixedDeny, scope) {
				return in, autopilotUsageFail("unknown scope " + autopilotQuote(scope) + " in --deny; allowable scopes: " + allowable)
			}
		}
		for _, scope := range named {
			if slices.Contains(autopilotAllowable, scope) {
				in.deny = append(in.deny, scope)
			}
		}
	}
	for _, s := range in.scopes {
		if slices.Contains(in.deny, s) {
			return in, autopilotUsageFail("scope " + s.(string) + " is in both --allow and --deny; name it in one of them")
		}
	}
	in.caps = ledger.Object{}
	if text, given := flags.values["budget"]; given {
		items := autopilotItems(text)
		if len(items) == 0 {
			return in, autopilotUsageFail("--budget needs <category>=<usd>, e.g. review=5,coding=20")
		}
		for _, item := range items {
			category, raw, _ := strings.Cut(item, "=")
			if category != "coding" && category != "review" {
				return in, autopilotUsageFail("unknown budget category " + autopilotQuote(category) + "; categories: coding, review")
			}
			if runHas(in.caps, category) {
				return in, autopilotUsageFail("budget category " + category + " given more than once")
			}
			usd, ok := autopilotAmount(raw)
			if !ok || !(usd > 0) {
				return in, autopilotUsageFail("budget for " + category + " must be a number of USD above 0, e.g. " + category + "=5")
			}
			in.caps = append(in.caps, ledger.Field{Key: category, Value: usd})
		}
	}
	in.stopAt = 0.9
	if text, given := flags.values["stop-at"]; given {
		value, ok := autopilotAmount(text)
		if !ok || !(value > 0 && value <= 1) {
			return in, autopilotUsageFail("--stop-at must be a number above 0 and at most 1, e.g. 0.9")
		}
		in.stopAt = value
	}
	return in, nil
}

// autopilotAmount is MONEY_PATTERN then a finite Number(raw).
func autopilotAmount(raw string) (float64, bool) {
	if !autopilotMoney.MatchString(raw) {
		return 0, false
	}
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
		return 0, false
	}
	return n, true
}

func autopilotStart(o *runOut, home, runID string, flags autopilotFlags, now time.Time) int {
	in, err := autopilotValidate(flags, now) // refuse bad input before asking anyone
	if err != nil {
		return autopilotReport(o, "start", err)
	}
	record, err := state.ReadRun(home, runID)
	if err != nil {
		return autopilotReport(o, "start", err)
	}
	if record.Get("status") != "active" {
		return autopilotReport(o, "start", &runFail{"run " + runID + " has ended", false})
	}
	before, err := autopilot.CheckExpiry(home, runID, now)
	if err != nil {
		return autopilotReport(o, "start", err)
	}
	if before.State == "active" {
		return autopilotReport(o, "start", &runFail{autopilot.ActiveGrantMessage(runID, autopilotText(before.Grant.Get("until"))), false})
	}
	startedAt := now
	if !flags.bools["yes"] {
		if !autopilotIsTTY() {
			o.err("autopilot start: no terminal to confirm; the owner starts autopilot — pass --yes to start it without a prompt\n")
			return 2
		}
		answer, err := autopilotConfirm("Start autopilot for run " + runID + " until " + in.until + "?")
		if err != nil {
			o.err("autopilot start: " + err.Error() + "\n") // Node's catch: a failed prompt is exit 1
			return 1
		}
		if answer != humanask.Confirmed {
			o.out("cancelled — no grant\n")
			return 1
		}
		// the prompt may have waited: --until is checked again (future, at most 24 h) against now
		startedAt = autopilotNow().Truncate(time.Millisecond)
		if in, err = autopilotValidate(flags, startedAt); err != nil {
			return autopilotReport(o, "start", err)
		}
		now = startedAt
	}
	// startGrant: the expiry check again, then under the run lock.
	if _, err := autopilot.CheckExpiry(home, runID, now); err != nil {
		return autopilotReport(o, "start", err)
	}
	id, err := autopilotGrantID()
	if err != nil {
		return autopilotReport(o, "start", err)
	}
	ts := autopilotStamp(now)
	grant := ledger.Object{
		{Key: "grant_id", Value: id}, {Key: "status", Value: "active"}, {Key: "until", Value: in.until}, {Key: "delegate", Value: in.delegate},
		{Key: "scopes", Value: in.scopes}, {Key: "deny", Value: in.deny}, {Key: "caps", Value: in.caps}, {Key: "stop_at", Value: in.stopAt},
		{Key: "started_at", Value: ts}, {Key: "stopped_at", Value: nil}, {Key: "expired_at", Value: nil}, {Key: "link", Value: nil},
	}
	err = state.UpdateRun(home, runID, func(record *ledger.Object) error {
		if record.Get("status") != "active" {
			return &runFail{"run " + runID + " has ended", false}
		}
		if previous, ok := record.Get("autopilot").(ledger.Object); ok {
			if s, err := autopilot.GrantState(previous, now); err == nil && s == "active" {
				return &runFail{autopilot.ActiveGrantMessage(runID, autopilotText(previous.Get("until"))), false}
			}
		}
		// the row first: a grant stored without its signed row would act unrecorded
		if _, err := state.WriteSigned(home, runID, ledger.Object{
			{Key: "event", Value: "autopilot.grant"}, {Key: "grant_id", Value: id}, {Key: "scopes", Value: in.scopes}, {Key: "deny", Value: in.deny},
			{Key: "delegate", Value: in.delegate}, {Key: "until", Value: in.until}, {Key: "caps", Value: in.caps}, {Key: "stop_at", Value: in.stopAt}, {Key: "ts", Value: ts},
		}); err != nil {
			return err
		}
		record.Set("autopilot", grant)
		return nil
	})
	if err != nil {
		return autopilotReport(o, "start", err)
	}
	until, _ := time.Parse("2006-01-02T15:04:05.000Z", in.until)
	scopes := autopilotStrings(in.scopes)
	o.out("autopilot run " + runID + ": grant " + id + " active until " + in.until + " (" + autopilotLeft(until.UnixMilli()-now.UnixMilli()) + ")\n" +
		"  delegate " + in.delegate + " · allow " + strings.Join(scopes, ", ") + " · budget " + autopilotCaps(in.caps) + " · stop at " + autopilotPercent(in.stopAt) + "%\n")
	return 0
}
