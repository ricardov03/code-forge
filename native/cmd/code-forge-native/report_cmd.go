package main

// B456: `code-forge report --slug <slug> [--json] [--export <dir>]` in Go, byte-equal to
// src/cli/report.mjs + src/ledger/{report,spend,prices,outside-review,export}.mjs. Go serves a
// ledger whose rows hold the value types code-forge writes; a row shape where JavaScript
// coercion would decide the bytes (a non-string block or run, a null row, an hour-24 timestamp,
// a lone surrogate, a number past the double range) keeps the Node hand-over.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/ledger"
)

func init() { routes["report"] = runReportVerb }

const reportUsage = "usage: code-forge report --slug <slug> [--json] [--export <dir>]\n"

// reportSectionNames is SECTION_NAMES (src/ledger/report.mjs), in order.
var reportSectionNames = []string{
	"cost_per_block", "lane_distribution", "escalations", "s1_calls_per_block", "s2_rate",
	"review_budget", "review_depth_and_findings", "fix_rounds", "review_unavailable", "proof_time",
	"forecast_vs_actual_lines", "blocks_stopped_at_l3", "run_stop_counts", "outside_review",
	"agent_waivers", "pace", "run_time", "specialist_outcomes",
}

// reportPaceColumns is PACE_COLUMNS, in column order.
var reportPaceColumns = [][2]string{
	{"openToCloseMinutes", "open->close min"}, {"coderMinutes", "coder min"},
	{"reviewWaitMinutes", "review wait min"}, {"reviewTimeouts", "review timeouts"},
	{"reviewUnavailable", "unavailable answers"}, {"gateMinutes", "gate min"}, {"gateRedCount", "gate reds"},
	{"landWaitMinutes", "close->land min"}, {"claimsAfterOpen", "claims after open"}, {"stopCount", "stops"},
	{"machineWaitMinutes", "machine.wait min"}, {"deviceMinutes", "device min"},
	{"lockWaitMinutes", "lock wait min"}, {"lockWaitCount", "lock waits"},
}

var (
	reportSlugPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	reportKnownLedger  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*(?:\.\d+)?\.jsonl$`)
	reportKnownSuffix  = regexp.MustCompile(`(?:\.\d+)?\.jsonl$`)
	reportTimestamp    = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?(?:Z|([+-])(\d{2}):(\d{2}))$`)
	reportLoneSurrogat = regexp.MustCompile(`\\u[dD][89a-fA-F][0-9a-fA-F]{2}`)
)

// reportProtoNames are Object.prototype properties: a price or role lookup by one of these
// reaches a function in JavaScript instead of a missing key.
var reportProtoNames = map[string]bool{
	"constructor": true, "hasOwnProperty": true, "isPrototypeOf": true, "propertyIsEnumerable": true,
	"toLocaleString": true, "toString": true, "valueOf": true, "__proto__": true,
	"__defineGetter__": true, "__defineSetter__": true, "__lookupGetter__": true, "__lookupSetter__": true,
}

var reportPrices = map[string]map[string]float64{
	"anthropic": {"L0": 0.001, "L1": 0.003, "L2": 0.015, "L3": 0.03},
	"openai":    {"L0": 0.001, "L1": 0.0025, "L2": 0.012, "L3": 0.025},
	"xai":       {"L0": 0.001, "L1": 0.002, "L2": 0.01, "L3": 0.02},
}

var reportRoleBucket = map[string]string{
	"coder": "coderUsd", "author": "coderUsd", "reviewer": "reviewUsd", "judge": "reviewUsd",
	"s2": "s1s2Usd", "delegate": "s1s2Usd", "facts": "factsUsd",
}

// rpUndef is JavaScript's undefined: an absent property. JSON output omits it from objects
// and writes null for it in arrays.
type rpUndef struct{}

var rpU = rpUndef{}

func rpGet(row ledger.Object, key string) any {
	for _, f := range row {
		if f.Key == key {
			return f.Value
		}
	}
	return rpU
}

func rpNullish(v any) bool { return v == nil || v == rpU }

func rpStr(v any) (string, bool) { s, ok := v.(string); return s, ok }

func rpNum(v any) (float64, bool) { n, ok := v.(float64); return n, ok }

func rpIs(row ledger.Object, key, want string) bool {
	s, ok := rpGet(row, key).(string)
	return ok && s == want
}

// rpStrictEq is ===. Two parsed objects or arrays are never the same object.
func rpStrictEq(a, b any) bool {
	switch x := a.(type) {
	case rpUndef:
		return b == rpU
	case nil:
		return b == nil
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	}
	return false
}

// rpJSNum is String(n) for a finite number.
func rpJSNum(n float64) string {
	b, _ := ledger.Stringify(n, false)
	return string(b)
}

// rpTemplate is `${v}` for the value kinds the report safety check lets through.
func rpTemplate(v any) string {
	switch x := v.(type) {
	case rpUndef:
		return "undefined"
	case nil:
		return "null"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return rpJSNum(x)
	case string:
		return x
	}
	return "[object Object]"
}

// rpRound is Math.round: the nearest integer, halves toward +Infinity.
func rpRound(x float64) float64 {
	f := math.Floor(x)
	if x-f >= 0.5 {
		f++
	}
	return f
}

func rpRoundUsd(n float64) float64 { return rpRound(n*10000) / 10000 }

// rpFixed is Number.prototype.toFixed(digits): the exact binary value, ties away from zero.
func rpFixed(n float64, digits int) string {
	if math.Abs(n) >= 1e21 {
		return rpJSNum(n)
	}
	negative := n < 0
	r := new(big.Rat).SetFloat64(math.Abs(n))
	r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)))
	q, rem := new(big.Int), new(big.Int)
	q.QuoRem(r.Num(), r.Denom(), rem)
	if new(big.Int).Lsh(rem, 1).Cmp(r.Denom()) >= 0 {
		q.Add(q, big.NewInt(1))
	}
	s := q.String()
	for len(s) < digits+1 {
		s = "0" + s
	}
	if digits > 0 {
		s = s[:len(s)-digits] + "." + s[len(s)-digits:]
	}
	if negative && strings.Trim(s, "0.") != "" {
		s = "-" + s
	}
	return s
}

// rpSet adds key unless the value is undefined, like JSON.stringify of a JS object.
func rpSet(o *ledger.Object, key string, v any) {
	if v == rpU {
		return
	}
	o.Set(key, v)
}

// rpArr turns undefined items into null, like JSON.stringify of an array.
func rpArr(items []any) []any {
	out := make([]any, len(items))
	for i, v := range items {
		if v == rpU {
			v = nil
		}
		out[i] = v
	}
	return out
}

func rpLessUTF16(a, b string) bool {
	left, right := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(left) && i < len(right); i++ {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return len(left) < len(right)
}

// rpTimestamp is report.mjs's timestamp()/paceTimestamp(): the parsed ms of a full ISO
// timestamp with a real calendar date, or ok=false. sure=false marks an hour of 24, which V8
// reads as the next day; Go keeps the hand-over there.
func rpTimestamp(v any) (ms float64, ok, sure bool) {
	s, isStr := v.(string)
	if !isStr {
		return 0, false, true
	}
	m := reportTimestamp.FindStringSubmatch(s)
	if m == nil {
		return 0, false, true
	}
	atoi := func(t string) int { n, _ := strconv.Atoi(t); return n }
	year, month, day, hour, minute, second := atoi(m[1]), atoi(m[2]), atoi(m[3]), atoi(m[4]), atoi(m[5]), atoi(m[6])
	if hour == 24 {
		return 0, false, false
	}
	if month < 1 || month > 12 || day < 1 || hour > 23 || minute > 59 || second > 59 {
		return 0, false, true
	}
	offset := 0
	if m[8] != "" {
		oh, om := atoi(m[9]), atoi(m[10])
		if oh > 23 || om > 59 {
			return 0, false, true
		}
		offset = oh*60 + om
		if m[8] == "-" {
			offset = -offset
		}
	}
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if t.Day() != day {
		return 0, false, true
	}
	frac := 0
	if m[7] != "" {
		digits := (m[7] + "00")[:3]
		frac = atoi(digits)
	}
	total := t.UnixMilli() + int64(hour)*3600000 + int64(minute)*60000 + int64(second)*1000 + int64(frac) - int64(offset)*60000
	return float64(total), true, true
}

// reportRows reads every ledger row like readAllRows + parseLines. ok=false hands over.
func reportRows(dir, slug string) ([]ledger.Object, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(slug) + `\.(\d+)\.jsonl$`)
	type rotation struct {
		name string
		n    float64
	}
	rotated := []rotation{}
	for _, entry := range entries {
		match := pattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		if len(match[1]) > 15 {
			return nil, false
		}
		n, _ := strconv.ParseFloat(match[1], 64)
		rotated = append(rotated, rotation{entry.Name(), n})
	}
	sort.SliceStable(rotated, func(i, j int) bool { return rotated[i].n < rotated[j].n })
	names := []string{}
	for _, r := range rotated {
		names = append(names, r.name)
	}
	names = append(names, slug+".jsonl")
	rows := []ledger.Object{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !utf8.Valid(data) {
			return nil, false
		}
		lines := []string{}
		for _, line := range strings.Split(string(data), "\n") {
			if line != "" {
				lines = append(lines, line)
			}
		}
		for i, line := range lines {
			if reportLoneSurrogat.MatchString(line) {
				return nil, false
			}
			obj, err := ledger.DecodeObject([]byte(line))
			if err == nil {
				rows = append(rows, obj)
				continue
			}
			if !json.Valid([]byte(line)) {
				if i == len(lines)-1 {
					continue
				}
				return nil, false
			}
			var value any
			if json.Unmarshal([]byte(line), &value) != nil || value == nil {
				return nil, false
			}
			// a number, string, boolean or array row has none of the named properties
			rows = append(rows, ledger.Object{})
		}
	}
	return rows, true
}

// reportSafe holds the row shapes whose JavaScript reading Go reproduces exactly.
func reportSafe(rows []ledger.Object) bool {
	strOrNull := func(v any) bool { _, ok := v.(string); return ok || rpNullish(v) }
	numOrNull := func(v any) bool { _, ok := v.(float64); return ok || rpNullish(v) }
	countKey := func(v any) bool {
		switch x := v.(type) {
		case rpUndef, nil, bool, float64:
			return true
		case string:
			return x != "__proto__"
		}
		return false
	}
	for _, row := range rows {
		if !strOrNull(rpGet(row, "run")) || !strOrNull(rpGet(row, "block")) {
			return false
		}
		for _, key := range []string{"role", "provider", "level", "lane"} {
			if !strOrNull(rpGet(row, key)) {
				return false
			}
		}
		if p, ok := rpGet(row, "provider").(string); ok && reportProtoNames[p] {
			return false
		}
		if r, ok := rpGet(row, "role").(string); ok && reportProtoNames[r] {
			return false
		}
		for _, key := range []string{"ts", "queued_at", "started_at"} {
			if _, _, sure := rpTimestamp(rpGet(row, key)); !sure {
				return false
			}
		}
		event, _ := rpGet(row, "event").(string)
		switch event {
		case "review.plan":
			step := rpGet(row, "degrade_step")
			if !strOrNull(rpGet(row, "file")) || !(strOrNull(step) || numOrNull(step)) {
				return false
			}
			if s, ok := step.(string); ok && s == "__proto__" {
				return false
			}
		case "outcome":
			if !strOrNull(rpGet(row, "file")) {
				return false
			}
		case "review.done":
			if !strOrNull(rpGet(row, "file")) || !countKey(rpGet(row, "context_mode")) || !numOrNull(rpGet(row, "round")) {
				return false
			}
			switch f := rpGet(row, "findings_by_severity").(type) {
			case rpUndef, nil:
			case ledger.Object:
				for _, field := range f {
					if _, ok := field.Value.(float64); !ok || field.Key == "__proto__" {
						return false
					}
				}
			default:
				return false
			}
		case "review.unavailable":
			if !countKey(rpGet(row, "reason")) {
				return false
			}
		case "decision":
			if !countKey(rpGet(row, "question")) {
				return false
			}
		case "proof":
			if !numOrNull(rpGet(row, "duration_ms")) {
				return false
			}
		case "outcome.recorded":
			if !strOrNull(rpGet(row, "skill")) || !strOrNull(rpGet(row, "status")) || !strOrNull(rpGet(row, "step")) ||
				!numOrNull(rpGet(row, "cost_usd")) || !numOrNull(rpGet(row, "minutes")) {
				return false
			}
		case "gate.done":
			switch rpGet(row, "repeats").(type) {
			case rpUndef, nil, ledger.Object:
			default:
				return false
			}
		case "block.awaiting_human_check":
			if _, ok := rpGet(row, "block").(string); !ok {
				return false
			}
		}
	}
	return true
}

type reportSpend struct {
	usd      float64
	unknown  int
	sessions int
}

func rpIsSpend(row ledger.Object) bool {
	return (rpIs(row, "event", "session") || rpIs(row, "event", "session.background")) && !rpIs(row, "source", "shadow")
}

func rpAmount(v any) (float64, bool) {
	n, ok := v.(float64)
	return n, ok && n >= 0
}

// rpRowUsd is rowUsd (src/ledger/spend.mjs).
func rpRowUsd(row ledger.Object) (float64, bool) {
	if usd := rpGet(row, "usd"); usd != rpU {
		return rpAmount(usd)
	}
	if b, ok := rpGet(row, "usd_unknown").(bool); ok && b {
		return 0, false
	}
	if cost := rpGet(row, "cost_usd"); !rpNullish(cost) {
		return rpAmount(cost)
	}
	if rpIs(row, "event", "session.background") {
		return 0, false
	}
	in, okIn := rpAmount(rpGet(row, "tokens_in"))
	out, okOut := rpAmount(rpGet(row, "tokens_out"))
	if !okIn || !okOut {
		return 0, false
	}
	provider, _ := rpGet(row, "provider").(string)
	level, _ := rpGet(row, "level").(string)
	perK, ok := reportPrices[provider][level]
	if !ok {
		return 0, false
	}
	return rpRound(((in+out)/1000)*perK*10000) / 10000, true
}

func rpSumSpend(rows []ledger.Object) reportSpend {
	s := reportSpend{}
	for _, row := range rows {
		if !rpIsSpend(row) {
			continue
		}
		s.sessions++
		if cost, ok := rpRowUsd(row); ok {
			s.usd += cost
		} else {
			s.unknown++
		}
	}
	s.usd = rpRoundUsd(s.usd)
	return s
}

type reportRunSpend struct {
	run   any // string or nil
	spend reportSpend
}

func rpSpendByRun(rows []ledger.Object) []reportRunSpend {
	order := []any{}
	groups := map[any][]ledger.Object{}
	for _, row := range rows {
		if !rpIsSpend(row) {
			continue
		}
		var key any
		if s, ok := rpGet(row, "run").(string); ok {
			key = s
		}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], row)
	}
	out := []reportRunSpend{}
	for _, key := range order {
		out = append(out, reportRunSpend{key, rpSumSpend(groups[key])})
	}
	return out
}

// reportBlockCost is one cost_per_block entry; extras keeps the order of the keys JavaScript
// adds after the fixed ones (stopped is deleted and re-added at the end).
type reportBlockCost struct {
	run                                                        any
	block                                                      string
	level, lane                                                any
	completed                                                  bool
	reviewUsd, coderUsd, s1s2Usd, factsUsd, otherUsd, totalUsd float64
	proofTimeMs                                                float64
	wallMs, humanWaitMs                                        any
	unknownUsdSessions                                         int
	gateTimeMs, reviewTimeMs                                   float64
	extras                                                     []string
}

func (e *reportBlockCost) has(key string) bool {
	for _, k := range e.extras {
		if k == key {
			return true
		}
	}
	return false
}

func (e *reportBlockCost) add(key string) {
	if !e.has(key) {
		e.extras = append(e.extras, key)
	}
}

func (e *reportBlockCost) drop(key string) {
	for i, k := range e.extras {
		if k == key {
			e.extras = append(e.extras[:i], e.extras[i+1:]...)
			return
		}
	}
}

func (e *reportBlockCost) object() ledger.Object {
	o := ledger.Object{
		{Key: "run", Value: e.run}, {Key: "block", Value: e.block}, {Key: "level", Value: e.level}, {Key: "lane", Value: e.lane},
		{Key: "completed", Value: e.completed}, {Key: "reviewUsd", Value: e.reviewUsd}, {Key: "coderUsd", Value: e.coderUsd},
		{Key: "s1s2Usd", Value: e.s1s2Usd}, {Key: "factsUsd", Value: e.factsUsd}, {Key: "otherUsd", Value: e.otherUsd},
		{Key: "proofTimeMs", Value: e.proofTimeMs}, {Key: "wallMs", Value: e.wallMs}, {Key: "humanWaitMs", Value: e.humanWaitMs},
		{Key: "totalUsd", Value: e.totalUsd}, {Key: "unknownUsdSessions", Value: float64(e.unknownUsdSessions)},
	}
	for _, key := range e.extras {
		switch key {
		case "stopped":
			o.Set(key, true)
		case "gateTimeMs":
			o.Set(key, e.gateTimeMs)
		case "reviewTimeMs":
			o.Set(key, e.reviewTimeMs)
		}
	}
	return o
}

func rpElapsed(start, end any) any {
	s, okS, _ := rpTimestamp(start)
	e, okE, _ := rpTimestamp(end)
	if !okS || !okE || e-s < 0 {
		return nil
	}
	return e - s
}

func rpTS(v any) any {
	if _, ok, _ := rpTimestamp(v); ok {
		return v
	}
	return nil
}

func rpCostPerBlock(rows []ledger.Object) []*reportBlockCost {
	runsOfBlock := map[string]map[string]bool{}
	for _, row := range rows {
		block, _ := rpGet(row, "block").(string)
		run, ok := rpGet(row, "run").(string)
		if block == "" || !ok {
			continue
		}
		if runsOfBlock[block] == nil {
			runsOfBlock[block] = map[string]bool{}
		}
		runsOfBlock[block][run] = true
	}
	type times struct{ firstDispatch, completion, stopped, awaiting, humanWait any }
	byKey := map[string]*reportBlockCost{}
	timeOf := map[string]*times{}
	order := []string{}
	keep := map[string]bool{}
	for _, row := range rows {
		block, _ := rpGet(row, "block").(string)
		if block == "" {
			continue
		}
		var run any
		if s, ok := rpGet(row, "run").(string); ok {
			run = s
		} else if runs := runsOfBlock[block]; len(runs) == 1 {
			for r := range runs {
				run = r
			}
		}
		key := rpTemplate(run) + "\x00" + block
		if run == nil {
			key = "\x00" + block
		}
		e := byKey[key]
		if e == nil {
			e = &reportBlockCost{run: run, block: block}
			byKey[key] = e
			timeOf[key] = &times{}
			order = append(order, key)
		}
		t := timeOf[key]
		event, _ := rpGet(row, "event").(string)
		switch event {
		case "dispatch", "block.close", "block.awaiting_human_check", "block.human_pass":
			e.drop("stopped")
			t.stopped = nil
		}
		switch event {
		case "dispatch":
			if v := rpGet(row, "level"); !rpNullish(v) {
				e.level = v
			}
			if v := rpGet(row, "lane"); !rpNullish(v) {
				e.lane = v
			}
			e.completed = false
		case "block.close":
			e.completed = true
			keep[key] = true
		case "block.awaiting_human_check":
			e.completed = false
		case "block.human_pass":
			e.completed = true
		case "block.stop":
			e.completed = false
			e.add("stopped")
		}
		if ts := rpTS(rpGet(row, "ts")); ts != nil {
			switch event {
			case "dispatch":
				if t.firstDispatch == nil {
					t.firstDispatch = ts
				}
				t.completion, t.awaiting, t.humanWait = nil, nil, nil
			case "block.close":
				t.completion, t.humanWait = ts, nil
			case "block.awaiting_human_check":
				t.awaiting, t.completion, t.humanWait = ts, nil, nil
			case "block.human_pass":
				t.completion = ts
				t.humanWait = rpElapsed(t.awaiting, ts)
			case "block.stop":
				t.stopped = ts
			}
		}
		if rpIsSpend(row) {
			keep[key] = true
			if cost, ok := rpRowUsd(row); ok {
				role, _ := rpGet(row, "role").(string)
				bucket, known := reportRoleBucket[role]
				if !known || rpNullish(rpGet(row, "role")) {
					bucket = "otherUsd"
				}
				switch bucket {
				case "coderUsd":
					e.coderUsd += cost
				case "reviewUsd":
					e.reviewUsd += cost
				case "s1s2Usd":
					e.s1s2Usd += cost
				case "factsUsd":
					e.factsUsd += cost
				default:
					e.otherUsd += cost
				}
				e.totalUsd += cost
			} else {
				e.unknownUsdSessions++
			}
		}
		d, isNum := rpGet(row, "duration_ms").(float64)
		if event == "proof" && isNum {
			e.proofTimeMs += d
		}
		if event == "gate.done" && isNum && d >= 0 {
			e.gateTimeMs += d
			e.add("gateTimeMs")
			keep[key] = true
		}
		if event == "review.result" && isNum && d >= 0 {
			e.reviewTimeMs += d
			e.add("reviewTimeMs")
			keep[key] = true
		}
	}
	out := []*reportBlockCost{}
	for _, key := range order {
		e := byKey[key]
		if !(e.completed || e.has("stopped") || keep[key]) {
			continue
		}
		t := timeOf[key]
		if e.completed {
			e.wallMs = rpElapsed(t.firstDispatch, t.completion)
			e.humanWaitMs = t.humanWait
		}
		if e.has("stopped") {
			e.wallMs = rpElapsed(t.firstDispatch, t.stopped)
		}
		out = append(out, e)
	}
	for _, e := range out {
		e.reviewUsd, e.coderUsd, e.s1s2Usd = rpRoundUsd(e.reviewUsd), rpRoundUsd(e.coderUsd), rpRoundUsd(e.s1s2Usd)
		e.factsUsd, e.otherUsd, e.totalUsd = rpRoundUsd(e.factsUsd), rpRoundUsd(e.otherUsd), rpRoundUsd(e.totalUsd)
	}
	return out
}

// rpCountBy is countBy: an undefined key is skipped, any other key is String(key).
func rpCountBy(rows []ledger.Object, keyOf func(ledger.Object) any) ledger.Object {
	out := ledger.Object{}
	for _, row := range rows {
		k := keyOf(row)
		if k == rpU {
			continue
		}
		key := rpTemplate(k)
		n, _ := out.Get(key).(float64)
		out.Set(key, n+1)
	}
	return out
}

func rpFilter(rows []ledger.Object, keep func(ledger.Object) bool) []ledger.Object {
	out := []ledger.Object{}
	for _, row := range rows {
		if keep(row) {
			out = append(out, row)
		}
	}
	return out
}

func rpEvent(name string) func(ledger.Object) bool {
	return func(row ledger.Object) bool { return rpIs(row, "event", name) }
}

func rpField(key string) func(ledger.Object) any {
	return func(row ledger.Object) any { return rpGet(row, key) }
}

func rpOrDefault(v, def any) any {
	if rpNullish(v) {
		return def
	}
	return v
}

func rpRunTime(rows []ledger.Object, costs []*reportBlockCost) ledger.Object {
	order := []string{}
	groups := map[string][]ledger.Object{}
	outside := []ledger.Object{}
	for _, row := range rows {
		run, ok := rpGet(row, "run").(string)
		if !ok {
			outside = append(outside, row)
			continue
		}
		if _, seen := groups[run]; !seen {
			order = append(order, run)
		}
		groups[run] = append(groups[run], row)
	}
	spend := map[string]float64{}
	for _, r := range rpSpendByRun(rows) {
		if s, ok := r.run.(string); ok {
			spend[s] = r.spend.usd
		}
	}
	runs := []any{}
	totalWall := 0.0
	totalClosed := 0
	for _, run := range order {
		list := groups[run]
		timed := rpFilter(list, func(r ledger.Object) bool { return rpTS(rpGet(r, "ts")) != nil })
		var started, ended any
		for _, r := range timed {
			if rpIs(r, "event", "run.start") {
				started = rpGet(r, "ts")
				break
			}
		}
		if started == nil && len(timed) > 0 {
			started = rpGet(timed[0], "ts")
		}
		for _, r := range timed {
			if rpIs(r, "event", "run.end") {
				ended = rpGet(r, "ts")
			}
		}
		open := ended == nil
		end := ended
		if open {
			end = nil
			if len(timed) > 0 {
				end = rpGet(timed[len(timed)-1], "ts")
			}
		}
		wall := rpElapsed(started, end)
		closed := 0
		for _, b := range costs {
			if s, ok := b.run.(string); ok && s == run && b.completed {
				closed++
			}
		}
		stopped := map[string]bool{}
		for _, r := range list {
			if b, _ := rpGet(r, "block").(string); rpIs(r, "event", "block.stop") && b != "" {
				stopped[b] = true
			}
		}
		o := ledger.Object{
			{Key: "run", Value: run}, {Key: "started", Value: started}, {Key: "ended", Value: ended},
			{Key: "wallMs", Value: wall}, {Key: "usd", Value: spend[run]},
			{Key: "blocks_closed", Value: float64(closed)}, {Key: "blocks_stopped", Value: float64(len(stopped))},
		}
		if open {
			o.Set("open", true)
		}
		runs = append(runs, o)
		if w, ok := wall.(float64); ok {
			totalWall += w
		}
		totalClosed += closed
	}
	out := ledger.Object{{Key: "runs", Value: runs}}
	if len(outside) > 0 {
		out.Set("outside_runs", ledger.Object{{Key: "usd", Value: rpSumSpend(outside).usd}, {Key: "rows", Value: float64(len(outside))}})
	}
	out.Set("total", ledger.Object{
		{Key: "runs", Value: float64(len(runs))}, {Key: "wallMs", Value: totalWall},
		{Key: "usd", Value: rpSumSpend(rows).usd}, {Key: "blocks_closed", Value: float64(totalClosed)},
	})
	return out
}

func rpPace(rows []ledger.Object) ledger.Object {
	type metrics map[string]float64
	type paceRun struct {
		run any
		m   metrics
	}
	type paceBlock struct {
		run                any
		block              string
		m                  metrics
		opened             bool
		start, close, land any
	}
	measured := func(v any) float64 {
		if n, ok := v.(float64); ok && n >= 0 {
			return n
		}
		return 0
	}
	elapsed := func(start, end any) float64 {
		s, okS := start.(float64)
		e, okE := end.(float64)
		if okS && okE && e >= s {
			return e - s
		}
		return 0
	}
	stamp := func(v any) any {
		if ms, ok, _ := rpTimestamp(v); ok {
			return ms
		}
		return nil
	}
	runKey := func(run any) string {
		if s, ok := run.(string); ok {
			return "s" + s
		}
		return "n"
	}
	runs := map[string]*paceRun{}
	runOrder := []string{}
	blocks := map[string]*paceBlock{}
	blockOrder := []string{}
	for _, row := range rows {
		var run any
		if s, ok := rpGet(row, "run").(string); ok {
			run = s
		}
		rk := runKey(run)
		if runs[rk] == nil {
			runs[rk] = &paceRun{run, metrics{}}
			runOrder = append(runOrder, rk)
		}
		m := runs[rk].m
		event, _ := rpGet(row, "event").(string)
		if block, ok := rpGet(row, "block").(string); ok && block != "" {
			bk := rk + "\x00" + block
			if blocks[bk] == nil {
				blocks[bk] = &paceBlock{run: run, block: block, m: metrics{}}
				blockOrder = append(blockOrder, bk)
			}
			b := blocks[bk]
			m = b.m
			if event == "dispatch" || event == "block.open" {
				b.opened = true
				if b.start == nil {
					b.start = stamp(rpGet(row, "ts"))
				}
				b.close, b.land = nil, nil
			}
			if event == "block.close" {
				if ts := stamp(rpGet(row, "ts")); ts != nil {
					b.close = ts
				}
			}
			if event == "land.timing" {
				if ts := stamp(rpGet(row, "ts")); ts != nil {
					b.land = ts
				}
			}
			if event == "block.claim" && b.opened {
				m["claimsAfterOpen"]++
			}
		}
		if event == "session" && rpIs(row, "role", "coder") {
			m["coderMinutes"] += measured(rpGet(row, "duration_ms"))
		}
		if event == "review.result" {
			m["reviewWaitMinutes"] += elapsed(stamp(rpGet(row, "queued_at")), stamp(rpGet(row, "started_at")))
		}
		if event == "review.session_timeout" {
			m["reviewTimeouts"]++
		}
		if event == "review.unavailable" {
			m["reviewUnavailable"]++
		}
		if event == "gate.done" {
			m["gateMinutes"] += measured(rpGet(row, "duration_ms"))
		}
		if event == "gate.red" {
			m["gateRedCount"]++
		}
		if event == "block.stop" {
			m["stopCount"]++
		}
		if event == "machine.wait" {
			m["machineWaitMinutes"] += measured(rpOrDefault(rpGet(row, "duration_ms"), rpGet(row, "wait_ms")))
		}
		if strings.HasPrefix(event, "device.") {
			m["deviceMinutes"] += measured(rpGet(row, "duration_ms"))
		}
		if event == "lock.wait" {
			m["lockWaitCount"]++
			m["lockWaitMinutes"] += measured(rpOrDefault(rpGet(row, "wait_ms"), rpGet(row, "duration_ms")))
		}
	}
	add := func(target, source metrics) {
		for _, col := range reportPaceColumns {
			target[col[0]] += source[col[0]]
		}
	}
	minutes := func(o *ledger.Object, m metrics) {
		for _, col := range reportPaceColumns {
			v := m[col[0]]
			if strings.HasSuffix(col[0], "Minutes") {
				v /= 60000
			}
			o.Set(col[0], v)
		}
	}
	perBlock := []any{}
	for _, bk := range blockOrder {
		b := blocks[bk]
		b.m["openToCloseMinutes"] = elapsed(b.start, b.close)
		b.m["landWaitMinutes"] = elapsed(b.close, b.land)
		add(runs[runKey(b.run)].m, b.m)
		o := ledger.Object{{Key: "run", Value: b.run}, {Key: "block", Value: b.block}}
		minutes(&o, b.m)
		perBlock = append(perBlock, o)
	}
	total := metrics{}
	perRun := []any{}
	for _, rk := range runOrder {
		r := runs[rk]
		add(total, r.m)
		o := ledger.Object{{Key: "run", Value: r.run}}
		minutes(&o, r.m)
		perRun = append(perRun, o)
	}
	totalObj := ledger.Object{}
	minutes(&totalObj, total)
	return ledger.Object{{Key: "blocks", Value: perBlock}, {Key: "runs", Value: perRun}, {Key: "total", Value: totalObj}}
}

func rpOutsideReview(rows []ledger.Object, costs []*reportBlockCost) []any {
	isCount := func(v any) bool {
		n, ok := v.(float64)
		return ok && n >= 0 && n == math.Trunc(n) && n <= 9007199254740991
	}
	latest := map[float64]ledger.Object{}
	prs := []float64{}
	for _, row := range rows {
		if !rpIs(row, "event", "outside_review") {
			continue
		}
		pr, ok := rpGet(row, "pr").(float64)
		if !ok || pr != math.Trunc(pr) || math.Abs(pr) > 9007199254740991 || pr <= 0 {
			continue
		}
		if !isCount(rpGet(row, "rounds")) || !isCount(rpGet(row, "findings")) {
			continue
		}
		if _, seen := latest[pr]; !seen {
			prs = append(prs, pr)
		}
		latest[pr] = row
	}
	sort.Float64s(prs)
	out := []any{}
	for _, pr := range prs {
		row := latest[pr]
		blocks := []any{}
		if list, ok := rpGet(row, "blocks").([]any); ok {
			for _, b := range list {
				if _, isStr := b.(string); isStr {
					blocks = append(blocks, b)
				}
			}
		}
		run, hasRun := rpGet(row, "run").(string)
		usd, unknown := 0.0, 0
		if hasRun {
			for _, e := range costs {
				r, ok := e.run.(string)
				if !ok || r != run {
					continue
				}
				for _, b := range blocks {
					if b.(string) == e.block {
						usd += e.totalUsd
						unknown += e.unknownUsdSessions
						break
					}
				}
			}
		}
		var link any
		if s, ok := rpGet(row, "link").(string); ok {
			link = s
		}
		out = append(out, ledger.Object{
			{Key: "pr", Value: pr}, {Key: "rounds", Value: rpGet(row, "rounds")}, {Key: "findings", Value: rpGet(row, "findings")},
			{Key: "link", Value: link}, {Key: "blocks", Value: blocks}, {Key: "code_forge_usd", Value: rpRoundUsd(usd)},
			{Key: "unknown_usd_sessions", Value: float64(unknown)},
		})
	}
	return out
}

func rpForecast(rows []ledger.Object) []any {
	mapKey := func(v any) string {
		switch x := v.(type) {
		case string:
			return "s" + x
		case nil:
			return "n"
		}
		return "u"
	}
	jsonKey := func(r ledger.Object) string {
		run := rpGet(r, "run")
		block := rpGet(r, "block")
		if block == rpU {
			block = nil
		}
		if run == rpU {
			run = nil
		}
		return mapKey(run) + "\x00" + mapKey(block)
	}
	forecasts := map[string]any{}
	for _, r := range rpFilter(rows, rpEvent("dispatch")) {
		forecasts[mapKey(rpGet(r, "block"))] = rpGet(r, "lines")
	}
	forecastOf := func(block any) any {
		v, ok := forecasts[mapKey(block)]
		if !ok || rpNullish(v) {
			return nil
		}
		return v
	}
	gates := map[string]ledger.Object{}
	gateOrder := []string{}
	for _, r := range rpFilter(rows, rpEvent("gate.done")) {
		k := jsonKey(r)
		if _, seen := gates[k]; !seen {
			gateOrder = append(gateOrder, k)
		}
		gates[k] = r
	}
	repeatsN := func(r ledger.Object) any {
		if o, ok := rpGet(r, "repeats").(ledger.Object); ok {
			return rpGet(o, "n")
		}
		return rpU
	}
	out := []any{}
	closedKeys := map[string]bool{}
	for _, r := range rows {
		if !rpIs(r, "event", "block.close") {
			continue
		}
		actual, ok := rpGet(r, "lines_actual").(float64)
		if !ok {
			continue
		}
		k := jsonKey(r)
		closedKeys[k] = true
		o := ledger.Object{}
		rpSet(&o, "block", rpGet(r, "block"))
		o.Set("forecast", forecastOf(rpGet(r, "block")))
		o.Set("actual", actual)
		if g, ok := gates[k]; ok {
			rpSet(&o, "repeats", repeatsN(g))
		}
		out = append(out, o)
	}
	for _, k := range gateOrder {
		g := gates[k]
		if _, ok := rpGet(g, "repeats").(ledger.Object); !ok || closedKeys[k] {
			continue
		}
		o := ledger.Object{}
		rpSet(&o, "block", rpGet(g, "block"))
		o.Set("forecast", forecastOf(rpGet(g, "block")))
		o.Set("actual", nil)
		rpSet(&o, "repeats", repeatsN(g))
		out = append(out, o)
	}
	return out
}

type reportBuilt struct {
	sections ledger.Object
	costs    []*reportBlockCost
	flaky    []any
	flakyTxt []string
}

func rpBuild(rows []ledger.Object) reportBuilt {
	acted := rpFilter(rows, func(r ledger.Object) bool { return !rpIs(r, "source", "shadow") })
	costs := rpCostPerBlock(acted)
	s := ledger.Object{}
	costObjs := []any{}
	for _, e := range costs {
		costObjs = append(costObjs, e.object())
	}
	s.Set("cost_per_block", costObjs)

	lanes := []any{}
	for _, r := range rpFilter(acted, rpEvent("dispatch")) {
		o := ledger.Object{}
		rpSet(&o, "block", rpGet(r, "block"))
		o.Set("lanePicked", rpOrDefault(rpGet(r, "lane"), nil))
		o.Set("levelOpened", rpOrDefault(rpGet(r, "level"), nil))
		lanes = append(lanes, o)
	}
	s.Set("lane_distribution", lanes)

	escalations := []any{}
	for _, r := range rpFilter(acted, rpEvent("escalation")) {
		o := ledger.Object{}
		rpSet(&o, "block", rpGet(r, "block"))
		rpSet(&o, "trigger", rpGet(r, "trigger"))
		escalations = append(escalations, o)
	}
	s.Set("escalations", escalations)

	decisions := rpFilter(acted, rpEvent("decision"))
	s.Set("s1_calls_per_block", rpCountBy(rpFilter(decisions, func(r ledger.Object) bool {
		return rpIs(r, "source", "jev") || rpIs(r, "source", "rules")
	}), rpField("block")))
	s2 := rpFilter(decisions, func(r ledger.Object) bool { return rpIs(r, "source", "s2") || rpIs(r, "source", "s2-fallback") })
	s.Set("s2_rate", ledger.Object{
		{Key: "calls", Value: rpCountBy(s2, rpField("question"))},
		{Key: "overrules", Value: rpCountBy(rpFilter(s2, func(r ledger.Object) bool {
			b, ok := rpGet(r, "overrule").(bool)
			return ok && b
		}), rpField("question"))},
	})

	fileKey := func(r ledger.Object) string {
		return rpTemplate(rpGet(r, "block")) + "::" + rpTemplate(rpGet(r, "file"))
	}
	plans := rpFilter(acted, rpEvent("review.plan"))
	degraded := map[string]bool{}
	degradedCount := 0
	for _, r := range plans {
		u, c := rpGet(r, "depth_unconstrained"), rpGet(r, "depth_chosen")
		if u != rpU && c != rpU && !rpStrictEq(u, c) {
			degradedCount++
			degraded[fileKey(r)] = true
		}
	}
	missed := map[string]bool{}
	for _, r := range rpFilter(acted, rpEvent("outcome")) {
		if b, ok := rpGet(r, "missed_after_degrade").(bool); ok && b && degraded[fileKey(r)] {
			missed[fileKey(r)] = true
		}
	}
	s.Set("review_budget", ledger.Object{
		{Key: "filesPlanned", Value: float64(len(plans))}, {Key: "degradedCount", Value: float64(degradedCount)},
		{Key: "missedAfterDegrade", Value: float64(len(missed))},
		{Key: "byDegradeStep", Value: rpCountBy(plans, func(r ledger.Object) any { return rpTemplate(rpOrDefault(rpGet(r, "degrade_step"), 0.0)) })},
	})

	done := rpFilter(acted, rpEvent("review.done"))
	findings := ledger.Object{}
	for _, r := range done {
		f, ok := rpGet(r, "findings_by_severity").(ledger.Object)
		if !ok {
			continue
		}
		ordered := append(ledger.Object{}, f...)
		sort.SliceStable(ordered, func(i, j int) bool {
			a, okA := rpArrayIndex(ordered[i].Key)
			b, okB := rpArrayIndex(ordered[j].Key)
			if okA != okB {
				return okA
			}
			return okA && a < b
		})
		for _, field := range ordered {
			n, _ := findings.Get(field.Key).(float64)
			findings.Set(field.Key, n+field.Value.(float64))
		}
	}
	s.Set("review_depth_and_findings", ledger.Object{
		{Key: "contextModeDistribution", Value: rpCountBy(done, rpField("context_mode"))},
		{Key: "findingsBySeverity", Value: findings},
	})

	rounds := ledger.Object{}
	for _, r := range done {
		if f, _ := rpGet(r, "file").(string); f == "" {
			continue
		}
		key := fileKey(r)
		prev, _ := rounds.Get(key).(float64)
		round, _ := rpOrDefault(rpGet(r, "round"), 0.0).(float64)
		rounds.Set(key, math.Max(prev, round))
	}
	s.Set("fix_rounds", rounds)
	s.Set("review_unavailable", rpCountBy(rpFilter(acted, rpEvent("review.unavailable")), rpField("reason")))

	proof := ledger.Object{}
	for _, r := range rpFilter(acted, rpEvent("proof")) {
		key := rpTemplate(rpGet(r, "block"))
		prev, _ := proof.Get(key).(float64)
		d, _ := rpOrDefault(rpGet(r, "duration_ms"), 0.0).(float64)
		proof.Set(key, prev+d)
	}
	s.Set("proof_time", proof)
	s.Set("forecast_vs_actual_lines", rpForecast(acted))

	l3 := []any{}
	for _, r := range rpFilter(acted, rpEvent("block.stop")) {
		if rpIs(r, "level", "L3") {
			l3 = append(l3, rpGet(r, "block"))
		}
	}
	s.Set("blocks_stopped_at_l3", rpArr(l3))

	scopes := func(flag string) float64 {
		seen := map[string]bool{}
		for _, r := range acted {
			if b, ok := rpGet(r, flag).(bool); ok && b {
				seen[rpTemplate(rpOrDefault(rpGet(r, "block"), rpOrDefault(rpGet(r, "run"), "unscoped")))] = true
			}
		}
		return float64(len(seen))
	}
	noEngine := 0
	for _, r := range acted {
		if rpIs(r, "event", "run.stop") && rpIs(r, "reason", "no_engine") {
			noEngine++
		}
	}
	s.Set("run_stop_counts", ledger.Object{
		{Key: "overspend", Value: scopes("overspend")}, {Key: "l3Fallback", Value: scopes("l3_fallback")},
		{Key: "noEngineStops", Value: float64(noEngine)},
	})
	s.Set("outside_review", rpOutsideReview(acted, costs))

	waivers := []any{}
	for i, row := range acted {
		if !rpIs(row, "event", "review.waived") || !rpIs(row, "by", "agent") {
			continue
		}
		o := ledger.Object{}
		for _, key := range []string{"block", "file", "finding", "severity", "reason", "verdict"} {
			rpSet(&o, key, rpGet(row, key))
		}
		unwaived := false
		for _, undo := range acted[i+1:] {
			if rpIs(undo, "event", "review.unwaived") && rpIs(undo, "by", "human") &&
				rpStrictEq(rpGet(undo, "run"), rpGet(row, "run")) && rpStrictEq(rpGet(undo, "block"), rpGet(row, "block")) &&
				rpStrictEq(rpGet(undo, "file"), rpGet(row, "file")) && rpStrictEq(rpGet(undo, "finding"), rpGet(row, "finding")) {
				unwaived = true
				break
			}
		}
		o.Set("unwaived", unwaived)
		waivers = append(waivers, o)
	}
	s.Set("agent_waivers", waivers)
	s.Set("pace", rpPace(acted))
	s.Set("run_time", rpRunTime(acted, costs))

	outcomes := []any{}
	cost, minutes := 0.0, 0.0
	for _, r := range rpFilter(acted, rpEvent("outcome.recorded")) {
		o := ledger.Object{}
		rpSet(&o, "skill", rpGet(r, "skill"))
		o.Set("step", rpOrDefault(rpGet(r, "step"), nil))
		rpSet(&o, "status", rpGet(r, "status"))
		o.Set("cost_usd", rpOrDefault(rpGet(r, "cost_usd"), nil))
		o.Set("minutes", rpOrDefault(rpGet(r, "minutes"), nil))
		c, _ := rpGet(r, "cost_usd").(float64)
		m, _ := rpGet(r, "minutes").(float64)
		cost += c
		minutes += m
		outcomes = append(outcomes, o)
	}
	s.Set("specialist_outcomes", ledger.Object{
		{Key: "outcomes", Value: outcomes},
		{Key: "total", Value: ledger.Object{{Key: "count", Value: float64(len(outcomes))}, {Key: "cost_usd", Value: cost}, {Key: "minutes", Value: minutes}}},
	})

	hasAwaiting := false
	for _, r := range acted {
		if rpIs(r, "event", "block.awaiting_human_check") {
			hasAwaiting = true
			break
		}
	}
	if hasAwaiting {
		waiting := map[string]string{}
		for _, r := range acted {
			key := rpTemplate(rpOrDefault(rpGet(r, "run"), "")) + "\x00" + rpTemplate(rpGet(r, "block"))
			event, _ := rpGet(r, "event").(string)
			if event == "block.awaiting_human_check" {
				waiting[key] = rpGet(r, "block").(string)
			}
			if event == "block.human_pass" || event == "block.stop" || event == "dispatch" {
				delete(waiting, key)
			}
		}
		unique := map[string]bool{}
		list := []string{}
		for _, b := range waiting {
			if !unique[b] {
				unique[b] = true
				list = append(list, b)
			}
		}
		sort.SliceStable(list, func(i, j int) bool { return rpLessUTF16(list[i], list[j]) })
		items := []any{}
		for _, b := range list {
			items = append(items, b)
		}
		s.Set("awaiting_human_check", items)
	}

	// flaky reads every row, shadow ones included, like report.mjs
	type flakyRun struct {
		run   any
		files map[string]bool
	}
	recovered := map[string]*flakyRun{}
	order := []string{}
	for _, row := range rows {
		list, ok := rpGet(row, "flaky").([]any)
		if !rpIs(row, "event", "gate.done") || !ok {
			continue
		}
		var run any
		key := "n"
		if r, ok := rpGet(row, "run").(string); ok {
			run, key = r, "s"+r
		}
		f := recovered[key]
		if f == nil {
			f = &flakyRun{run, map[string]bool{}}
		}
		for _, item := range list {
			if s, ok := item.(string); ok {
				f.files[s] = true
			}
		}
		if len(f.files) > 0 && recovered[key] == nil {
			recovered[key] = f
			order = append(order, key)
		}
	}
	built := reportBuilt{sections: s, costs: costs}
	for _, key := range order {
		f := recovered[key]
		files := []string{}
		for name := range f.files {
			files = append(files, name)
		}
		sort.Slice(files, func(i, j int) bool { return rpLessUTF16(files[i], files[j]) })
		items := []any{}
		for _, name := range files {
			items = append(items, name)
		}
		built.flaky = append(built.flaky, ledger.Object{{Key: "run", Value: f.run}, {Key: "files", Value: items}})
		label := "outside runs"
		if r, ok := f.run.(string); ok {
			label = "run " + r
		}
		built.flakyTxt = append(built.flakyTxt, label+" flaky: "+strings.Join(files, ", ")+"\n")
	}
	return built
}

func rpArrayIndex(key string) (uint64, bool) {
	n, err := strconv.ParseUint(key, 10, 32)
	return n, err == nil && n < 4294967295 && strconv.FormatUint(n, 10) == key
}

func rpDuration(ms any) string {
	n, ok := ms.(float64)
	if !ok {
		return "-"
	}
	seconds := math.Floor(n / 1000)
	if seconds < 60 {
		return rpJSNum(seconds) + "s"
	}
	minutes := math.Floor(seconds / 60)
	if minutes < 60 {
		return rpJSNum(minutes) + "m " + rpJSNum(math.Mod(seconds, 60)) + "s"
	}
	return rpJSNum(math.Floor(minutes/60)) + "h " + rpJSNum(math.Mod(minutes, 60)) + "m " + rpJSNum(math.Mod(seconds, 60)) + "s"
}

func rpDash(v any) string {
	if rpNullish(v) {
		return "-"
	}
	return rpTemplate(v)
}

func rpPretty(v any) string {
	b, _ := ledger.Stringify(v, true)
	return string(b) + "\n"
}

func rpCostLines(costs []*reportBlockCost) string {
	lines := []string{"run | block | USD | wall | human wait | status"}
	for _, b := range costs {
		status := "open"
		if b.completed {
			status = "completed"
		} else if b.has("stopped") {
			status = "stopped"
		}
		run := "outside runs"
		if r, ok := b.run.(string); ok {
			run = r
		}
		var gate, review any
		if b.has("gateTimeMs") {
			gate = b.gateTimeMs
		}
		if b.has("reviewTimeMs") {
			review = b.reviewTimeMs
		}
		lines = append(lines, fmt.Sprintf("%s | %s | $%s | %s | %s | %s", run, b.block, rpFixed(b.totalUsd, 4), rpDuration(b.wallMs), rpDuration(b.humanWaitMs), status))
		lines = append(lines, fmt.Sprintf("  level %s · lane %s · coder $%s · review $%s · S1+S2 $%s · facts $%s · other $%s · gates %s · review time %s · proof %s · %d unknown",
			rpDash(b.level), rpDash(b.lane), rpFixed(b.coderUsd, 4), rpFixed(b.reviewUsd, 4), rpFixed(b.s1s2Usd, 4), rpFixed(b.factsUsd, 4), rpFixed(b.otherUsd, 4),
			rpDuration(gate), rpDuration(review), rpDuration(b.proofTimeMs), b.unknownUsdSessions))
	}
	return strings.Join(lines, "\n") + "\n"
}

func rpRunTimeLines(data ledger.Object) string {
	lines := []string{"run | started | ended | wall | USD | closed | stopped"}
	for _, item := range data.Get("runs").([]any) {
		r := item.(ledger.Object)
		ended := rpDash(r.Get("ended"))
		if r.Get("open") == true {
			ended = "open"
		}
		lines = append(lines, fmt.Sprintf("%s | %s | %s | %s | $%s | %s | %s", r.Get("run"), rpDash(r.Get("started")), ended, rpDuration(r.Get("wallMs")),
			rpFixed(r.Get("usd").(float64), 4), rpJSNum(r.Get("blocks_closed").(float64)), rpJSNum(r.Get("blocks_stopped").(float64))))
	}
	if o, ok := data.Get("outside_runs").(ledger.Object); ok {
		lines = append(lines, fmt.Sprintf("outside runs: $%s (%s rows, no wall time)", rpFixed(o.Get("usd").(float64), 4), rpJSNum(o.Get("rows").(float64))))
	}
	t := data.Get("total").(ledger.Object)
	n := t.Get("runs").(float64)
	plural := "s"
	if n == 1 {
		plural = ""
	}
	lines = append(lines, fmt.Sprintf("total (%s run%s): %s | $%s | %s closed", rpJSNum(n), plural, rpDuration(t.Get("wallMs")), rpFixed(t.Get("usd").(float64), 4), rpJSNum(t.Get("blocks_closed").(float64))))
	return strings.Join(lines, "\n") + "\n"
}

func rpOutsideLines(section []any) string {
	if len(section) == 0 {
		return "outside review: none\n"
	}
	plural := func(n float64, word string) string {
		if n == 1 {
			return rpJSNum(n) + " " + word
		}
		return rpJSNum(n) + " " + word + "s"
	}
	out := ""
	for _, item := range section {
		e := item.(ledger.Object)
		unknown := ""
		if u := e.Get("unknown_usd_sessions").(float64); u > 0 {
			unknown = " (+" + rpJSNum(u) + " unknown)"
		}
		blocks := []string{}
		for _, b := range e.Get("blocks").([]any) {
			blocks = append(blocks, b.(string))
		}
		list := "-"
		if len(blocks) > 0 {
			list = strings.Join(blocks, ", ")
		}
		out += fmt.Sprintf("PR #%s · %s · %s · code-forge $%s%s · %s\n", rpJSNum(e.Get("pr").(float64)), plural(e.Get("rounds").(float64), "outside round"),
			plural(e.Get("findings").(float64), "finding"), rpFixed(e.Get("code_forge_usd").(float64), 2), unknown, list)
	}
	return out
}

func rpPaceLines(pace ledger.Object) string {
	header := []string{"run", "block"}
	for _, col := range reportPaceColumns {
		header = append(header, col[1])
	}
	line := func(run, block string, m ledger.Object) string {
		parts := []string{run, block}
		for _, col := range reportPaceColumns {
			parts = append(parts, rpJSNum(m.Get(col[0]).(float64)))
		}
		return strings.Join(parts, " | ")
	}
	runName := func(v any) string {
		if s, ok := v.(string); ok {
			return s
		}
		return "outside runs"
	}
	lines := []string{strings.Join(header, " | ")}
	for _, item := range pace.Get("blocks").([]any) {
		b := item.(ledger.Object)
		lines = append(lines, line(runName(b.Get("run")), b.Get("block").(string), b))
	}
	for _, item := range pace.Get("runs").([]any) {
		r := item.(ledger.Object)
		lines = append(lines, line(runName(r.Get("run")), "total", r))
	}
	lines = append(lines, line("all runs", "total", pace.Get("total").(ledger.Object)))
	return strings.Join(lines, "\n") + "\n"
}

func rpSpendLines(rows []ledger.Object) string {
	acted := rpFilter(rows, func(r ledger.Object) bool { return !rpIs(r, "source", "shadow") })
	unknown := func(n int) string {
		if n > 0 {
			return fmt.Sprintf(" · %d with unknown price (not counted)", n)
		}
		return ""
	}
	lines := []string{"spend per run (estimated USD, open blocks included):"}
	runs := rpSpendByRun(acted)
	for _, r := range runs {
		if s, ok := r.run.(string); ok {
			lines = append(lines, fmt.Sprintf("  run %s: $%s · %d session(s)%s", s, rpFixed(r.spend.usd, 4), r.spend.sessions, unknown(r.spend.unknown)))
		}
	}
	for _, r := range runs {
		if r.run == nil {
			lines = append(lines, fmt.Sprintf("  outside runs: $%s · %d session(s)%s", rpFixed(r.spend.usd, 4), r.spend.sessions, unknown(r.spend.unknown)))
			break
		}
	}
	total := rpSumSpend(acted)
	lines = append(lines, fmt.Sprintf("  total: $%s · %d session(s)%s", rpFixed(total.usd, 4), total.sessions, unknown(total.unknown)))
	return strings.Join(lines, "\n") + "\n"
}

func rpModeLines(rows []ledger.Object) string {
	brief := map[string]bool{}
	for _, r := range rows {
		if run, ok := rpGet(r, "run").(string); ok && rpIs(r, "event", "run.mode") && rpIs(r, "mode", "brief") {
			brief[run] = true
		}
	}
	seen := map[string]bool{}
	lines := []string{}
	for _, r := range rows {
		run, ok := rpGet(r, "run").(string)
		if !ok || !rpIs(r, "event", "run.start") || seen[run] {
			continue
		}
		seen[run] = true
		mode := "plan"
		if brief[run] {
			mode = "brief"
		}
		lines = append(lines, "  run "+run+": "+mode)
	}
	if len(lines) == 0 {
		return ""
	}
	return "run mode:\n" + strings.Join(lines, "\n") + "\n"
}

func rpSpecialistLines(data ledger.Object) string {
	cost := func(v any) string {
		n, ok := v.(float64)
		if !ok {
			return "-"
		}
		return "$" + rpFixed(n, 4)
	}
	lines := []string{"skill | step | status | cost_usd | minutes"}
	for _, item := range data.Get("outcomes").([]any) {
		r := item.(ledger.Object)
		lines = append(lines, fmt.Sprintf("%s | %s | %s | %s | %s", rpTemplate(rpGet(r, "skill")), rpDash(r.Get("step")), rpTemplate(rpGet(r, "status")), cost(r.Get("cost_usd")), rpDash(r.Get("minutes"))))
	}
	t := data.Get("total").(ledger.Object)
	lines = append(lines, fmt.Sprintf("total (%s outcomes): %s | %s minutes", rpJSNum(t.Get("count").(float64)), cost(t.Get("cost_usd")), rpJSNum(t.Get("minutes").(float64))))
	return strings.Join(lines, "\n") + "\n"
}

func rpFlagValue(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag {
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				return args[i+1]
			}
			return ""
		}
	}
	return ""
}

type reportRun func(stdout, stderr io.Writer) int

func rpFail(message string, code int) reportRun {
	return func(_, stderr io.Writer) int { fmt.Fprint(stderr, message); return code }
}

// reportDecide returns the Go run for a case Go serves, or nil to hand over. It reads the
// ledger but writes nothing.
func reportDecide(args []string) reportRun {
	slug := rpFlagValue(args, "--slug")
	if slug == "" {
		return rpFail(reportUsage, 2)
	}
	home := os.Getenv("HOME")
	if !utf8.ValidString(slug) || home == "" {
		return nil
	}
	if !reportSlugPattern.MatchString(slug) {
		quoted, _ := ledger.Stringify(slug, false)
		return rpFail("report: ledgerPath: slug must match /^[a-z0-9][a-z0-9-]*$/ (got "+string(quoted)+")\n", 1)
	}
	dir := filepath.Join(home, ".code-forge", "ledger")
	info, err := os.Stat(filepath.Join(dir, slug+".jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		var perr *os.PathError
		if errors.As(err, &perr) && !errors.Is(perr.Err, os.ErrNotExist) {
			return nil
		}
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil
		}
		seen := map[string]bool{}
		slugs := []string{}
		for _, e := range entries {
			if !e.Type().IsRegular() || !reportKnownLedger.MatchString(e.Name()) {
				continue
			}
			name := reportKnownSuffix.ReplaceAllString(e.Name(), "")
			if !seen[name] {
				seen[name] = true
				slugs = append(slugs, name)
			}
		}
		sort.Strings(slugs)
		known := strings.Join(slugs, ", ")
		if known == "" {
			known = "none"
		}
		return rpFail(fmt.Sprintf("report: no ledger for slug \"%s\"; known slugs: %s\n", slug, known), 1)
	}
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	rows, ok := reportRows(dir, slug)
	if !ok || !reportSafe(rows) {
		return nil
	}
	built := rpBuild(rows)
	reported := built.sections
	if len(built.flaky) > 0 {
		reported = append(reported.Clone(), ledger.Field{Key: "flaky", Value: built.flaky})
	}
	if exportDir := rpFlagValue(args, "--export"); exportDir != "" {
		return func(stdout, _ io.Writer) int {
			if os.MkdirAll(exportDir, 0777) != nil {
				return -1
			}
			for _, field := range reported {
				if os.WriteFile(filepath.Join(exportDir, field.Key+".json"), []byte(rpPretty(field.Value)), 0666) != nil {
					return -1
				}
			}
			fmt.Fprintf(stdout, "exported %d section file(s) to %s\n", len(reported), exportDir)
			return 0
		}
	}
	for _, arg := range args {
		if arg == "--json" {
			return func(stdout, _ io.Writer) int { fmt.Fprint(stdout, rpPretty(reported)); return 0 }
		}
	}
	return func(stdout, _ io.Writer) int {
		var b strings.Builder
		for _, field := range built.sections {
			var body string
			switch field.Key {
			case "specialist_outcomes":
				continue
			case "cost_per_block":
				body = rpCostLines(built.costs)
			case "run_time":
				body = rpRunTimeLines(field.Value.(ledger.Object))
			case "outside_review":
				body = rpOutsideLines(field.Value.([]any))
			case "pace":
				body = rpPaceLines(field.Value.(ledger.Object))
			default:
				body = rpPretty(field.Value)
			}
			b.WriteString("== " + field.Key + " ==\n" + body)
		}
		b.WriteString(rpSpendLines(rows))
		b.WriteString(rpModeLines(rows))
		for _, line := range built.flakyTxt {
			b.WriteString(line)
		}
		b.WriteString("== specialist_outcomes ==\n" + rpSpecialistLines(built.sections.Get("specialist_outcomes").(ledger.Object)))
		fmt.Fprint(stdout, b.String())
		return 0
	}
}

func runReportVerb(args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(os.Stdout, reportUsage)
		return 0
	}
	plan := reportDecide(args)
	if plan == nil {
		code, _ := handover(append([]string{"report"}, args...))
		return code
	}
	contextApplyTmpRoot()
	before := gatesBreadcrumbName("report")
	stderr := &contextErrWriter{}
	code := plan(os.Stdout, stderr)
	if code == -1 {
		// an export directory or file Go could not write: Node redoes the same idempotent
		// writes and reports its own error text
		code, _ = handover(append([]string{"report"}, args...))
		return code
	}
	if code != 0 {
		var message any
		if stderr.last != "" {
			message = stderr.last
		}
		nativeVerbFailure("report", "", args, code, message, before)
	}
	return code
}
