package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/childreg"
	"github.com/ricardov03/code-forge/native/humancheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/runstatus"
	"github.com/ricardov03/code-forge/native/state"
)

// B478: the autopilot binnacle and full log (src/autopilot/binnacle.mjs) in
// Go: buildBinnacle, buildFullLog, their Markdown, the scrub every text goes
// through, the git owner of the byline, the two files in the run dir and the
// `--link` store. A row Go cannot read the way Node does (a timestamp
// Date.parse may read differently, an id localeCompare orders outside
// printable ASCII, a link outside the plain https subset) keeps the hand-over.

const (
	bnBinnacleFile = "autopilot-binnacle.md"
	bnLogFile      = "autopilot-log.md"
	bnNoneYet      = "none yet"
	bnLinkMax      = 2048
)

var bnExtraEvents = []string{"block.close", "review.session_timeout", "review.schema_fallback", "worker.down", "worker.replaced", "ledger.tamper", "budget.refused", "gate.red", "gate.done"}

// jsUndefined is a missing property: `${x}` gives "undefined", `??` skips it.
type jsUndefined struct{}

var undef any = jsUndefined{}

// bnGet is `v?.[key]` for the JSON values rows and records hold.
func bnGet(v any, key string) any {
	switch m := v.(type) {
	case map[string]any:
		if x, ok := m[key]; ok {
			return x
		}
	case ledger.Object:
		if runHas(m, key) {
			return m.Get(key)
		}
	}
	return undef
}

func bnNullish(v any) bool { return v == nil || v == undef }

// bnObject: typeof v === 'object' && v !== null (arrays included).
func bnObject(v any) bool {
	switch v.(type) {
	case map[string]any, ledger.Object, []any:
		return true
	}
	return false
}

// bnJSString is String(v).
func bnJSString(v any) string {
	switch x := v.(type) {
	case jsUndefined:
		return "undefined"
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case float64:
		b, _ := canon.Marshal(x)
		return string(b)
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			if !bnNullish(item) {
				parts[i] = bnJSString(item)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

// bnCo is `a ?? b ?? …` rendered as text.
func bnCo(vs ...any) string {
	for _, v := range vs[:len(vs)-1] {
		if !bnNullish(v) {
			return bnJSString(v)
		}
	}
	return bnJSString(vs[len(vs)-1])
}

func bnStr(v any) bool { s, ok := v.(string); return ok && s != "" }

func bnStrOr(v any) any {
	if bnStr(v) {
		return v
	}
	return nil
}

func bnFirst(vs ...any) any {
	for _, v := range vs {
		if bnStr(v) {
			return v
		}
	}
	return nil
}

func bnU(v any, fallback string) string {
	if bnNullish(v) || v == "" {
		return fallback
	}
	return bnJSString(v)
}

func bnList(v any) string {
	list, ok := v.([]any)
	if !ok {
		return ""
	}
	parts := make([]string, len(list))
	for i, item := range list {
		if !bnNullish(item) {
			parts[i] = bnJSString(item)
		}
	}
	return strings.Join(parts, ", ")
}

func bnLen16(s string) int { return len(utf16.Encode([]rune(s))) }

func bnSlice16(s string, n int) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return s
	}
	return string(utf16.Decode(units[:n]))
}

func bnShort(v any) any {
	if !bnStr(v) {
		return nil
	}
	return bnSlice16(v.(string), 7)
}

// bnEq is ===: two distinct JSON objects are never equal.
func bnEq(a, b any) bool {
	switch x := a.(type) {
	case jsUndefined:
		return b == undef
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	}
	return false
}

var bnDecimal = regexp.MustCompile(`^[+-]?(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?$`)

// apJSNumber is Number(v).
func apJSNumber(v any) float64 {
	switch x := v.(type) {
	case jsUndefined:
		return math.NaN()
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return x
	case string:
		s := strings.TrimFunc(x, autopilotJSSpace)
		switch {
		case s == "":
			return 0
		case s == "Infinity" || s == "+Infinity":
			return math.Inf(1)
		case s == "-Infinity":
			return math.Inf(-1)
		}
		for prefix, base := range map[string]int{"0x": 16, "0X": 16, "0o": 8, "0O": 8, "0b": 2, "0B": 2} {
			if strings.HasPrefix(s, prefix) {
				n, err := strconv.ParseUint(s[2:], base, 64)
				if err != nil {
					return math.NaN()
				}
				return float64(n)
			}
		}
		if !bnDecimal.MatchString(s) {
			return math.NaN()
		}
		n, _ := strconv.ParseFloat(s, 64)
		return n
	case []any:
		return apJSNumber(bnJSString(x))
	}
	return math.NaN()
}

var bnISO = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?(?:Z|[+-]\d{2}:\d{2})$`)

// bnDateMs is Date.parse for the ISO forms Go reads exactly the same way.
// known is false for any other text: Date.parse may still read it.
func bnDateMs(s string) (ms int64, ok, known bool) {
	if bnISO.MatchString(s) {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.UnixMilli(), true, true
		}
	}
	return 0, false, false
}

// bnCollate is a.localeCompare(b) (ICU root, no numeric option) for printable
// ASCII; ok is false for any other character.
func bnCollate(a, b string) (int, bool) {
	const order = " _-,;:!?.'\"()[]{}@*/\\&#%`^+<=>|~$0123456789abcdefghijklmnopqrstuvwxyz"
	weights := func(s string) ([]int, []bool, bool) {
		w, up := make([]int, len(s)), make([]bool, len(s))
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c < 0x20 || c > 0x7e {
				return nil, nil, false
			}
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
				up[i] = true
			}
			w[i] = strings.IndexByte(order, c)
		}
		return w, up, true
	}
	wa, ua, okA := weights(a)
	wb, ub, okB := weights(b)
	if !okA || !okB {
		return 0, false
	}
	if c := slices.Compare(wa, wb); c != 0 {
		return c, true
	}
	for i := range ua {
		if ua[i] != ub[i] {
			if !ua[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return strings.Compare(a, b), true
}

// ---- the scrub (makeScrub: createScrubber over maskSecretTokens(redact(text)))

const bnJSSpace = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

var (
	bnOpRef  = regexp.MustCompile("op://(?:[^/\\r\\n\"'`<>]+/[^/\\r\\n\"'`<>]+/)?[^" + bnJSSpace + "\"'`<>)\\]]+")
	bnEmail  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	bnSecret = []*regexp.Regexp{
		regexp.MustCompile(`sk-[A-Za-z0-9_-]{10,}`),
		regexp.MustCompile(`xai-[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`gh[opsu]_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`github_pat_[A-Za-z0-9_]{20,}`),
		regexp.MustCompile(`AKIA[0-9A-Z]{12,}`),
		regexp.MustCompile(`AIza[0-9A-Za-z_-]{20,}`),
		regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}`),
	}
)

func bnWord(c byte, dash bool) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || dash && (c == '_' || c == '-')
}

// bnMaskSecrets is maskSecretTokens: each pattern behind (?<![A-Za-z0-9_-]),
// replaced globally the way String.prototype.replace scans.
func bnMaskSecrets(text string) string {
	for _, re := range bnSecret {
		var b strings.Builder
		pos, from := 0, 0
		for from <= len(text) {
			loc := re.FindStringIndex(text[from:])
			if loc == nil {
				break
			}
			start, end := from+loc[0], from+loc[1]
			if start > 0 && bnWord(text[start-1], true) {
				from = start + 1
				continue
			}
			b.WriteString(text[pos:start])
			b.WriteString("[REDACTED]")
			pos, from = end, end
		}
		b.WriteString(text[pos:])
		text = b.String()
	}
	return text
}

type bnScrubber struct{ paths [][2]string }

func bnPathVariants(p string) []string {
	if bnLen16(p) < 2 {
		return nil
	}
	out := []string{strings.TrimRight(p, `/\`)}
	if real, err := filepath.EvalSymlinks(p); err == nil && !slices.Contains(out, real) {
		out = append(out, real)
	}
	kept := []string{}
	for _, v := range out {
		if bnLen16(v) >= 2 {
			kept = append(kept, v)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return bnLen16(kept[i]) > bnLen16(kept[j]) })
	return kept
}

// bnNewScrub is makeScrub({home, cwd}); cwd "" is no working dir.
func bnNewScrub(home, cwd string) func(string) string {
	homes := bnPathVariants(home)
	s := bnScrubber{}
	for _, c := range bnPathVariants(cwd) {
		if !slices.Contains(homes, c) {
			s.paths = append(s.paths, [2]string{c, "<project>"})
		}
	}
	for _, h := range homes {
		s.paths = append(s.paths, [2]string{h, "~"})
	}
	sort.SliceStable(s.paths, func(i, j int) bool { return bnLen16(s.paths[i][0]) > bnLen16(s.paths[j][0]) })
	return func(text string) string { return s.scrub(bnMaskSecrets(autopilotRedact(text))) }
}

func (s bnScrubber) scrub(text string) string {
	text = bnOpRef.ReplaceAllLiteralString(text, "op://<ref>")
	for _, p := range s.paths {
		var b strings.Builder
		pos, from := 0, 0
		for {
			at := strings.Index(text[from:], p[0])
			if at < 0 {
				break
			}
			start := from + at
			end := start + len(p[0])
			if end < len(text) && bnWord(text[end], true) {
				from = start + 1
				continue
			}
			b.WriteString(text[pos:start])
			b.WriteString(p[1])
			pos, from = end, end
		}
		b.WriteString(text[pos:])
		text = b.String()
	}
	text = bnEmail.ReplaceAllLiteralString(text, "<email>")
	var b strings.Builder
	pos := 0
	for i := 0; i+26 <= len(text); {
		ok := i == 0 || !bnWord(text[i-1], false)
		for j := i; ok && j < i+26; j++ {
			c := text[j]
			ok = c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		}
		if ok && (i+26 == len(text) || !bnWord(text[i+26], false)) {
			b.WriteString(text[pos:i])
			b.WriteString("<item-id>")
			i += 26
			pos = i
			continue
		}
		i++
	}
	b.WriteString(text[pos:])
	return b.String()
}

func bnDeepScrub(v any, scrub func(string) string) any {
	switch x := v.(type) {
	case string:
		return scrub(x)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = bnDeepScrub(item, scrub)
		}
		return out
	case ledger.Object:
		out := ledger.Object{}
		for _, f := range x {
			out = append(out, ledger.Field{Key: f.Key, Value: bnDeepScrub(f.Value, scrub)})
		}
		return out
	}
	return v
}

// ---- the build

type bnItem struct {
	row    any
	time   any // string or nil
	approx bool
	index  int
}

// bnBuild holds one build; unsupported is set for any input Go cannot read
// the way Node does.
type bnBuild struct {
	runID       string
	home        string
	unsupported bool
}

func (b *bnBuild) ordered(rows []ledger.Row) []bnItem {
	out := []bnItem{}
	var last any
	for index, row := range rows {
		if _, ok := row.(map[string]any); !ok || !bnEq(bnGet(row, "run"), b.runID) || bnEq(bnGet(row, "source"), "shadow") {
			continue
		}
		ts := bnGet(row, "ts")
		good := false
		if bnStr(ts) {
			_, ok, known := bnDateMs(ts.(string))
			if !known {
				b.unsupported = true
			}
			good = ok
		}
		if good {
			last = ts
		}
		t := last
		if good {
			t = ts
		}
		out = append(out, bnItem{row: row, time: t, approx: !good, index: index})
	}
	return out
}

func bnEvent(row any) string { s, _ := bnGet(row, "event").(string); return s }

func bnInLog(row any) bool {
	event, isText := bnGet(row, "event").(string)
	return isText && (strings.HasPrefix(event, "autopilot.") || slices.Contains(bnExtraEvents, event)) ||
		event == "review.waived" && bnEq(bnGet(row, "by"), "autopilot") ||
		event == "session" && bnEq(bnGet(row, "role"), "delegate")
}

func bnUSD(n float64) string { return statusFixed(n) }

func bnGateScope(row any) string {
	g := func(k string) any { return bnGet(row, k) }
	if bnEq(g("scope"), "related") {
		if n, ok := g("test_count").(float64); ok && n == math.Trunc(n) {
			return " · related tests (" + bnJSString(n) + ")"
		}
		return " · related tests"
	}
	if !bnEq(g("scope"), "full") {
		return ""
	}
	why := ""
	if bnEq(g("reason"), "threshold") {
		why = "the block owns more files than the threshold"
	} else if bnEq(g("reason"), "no_tests") {
		why = "no test file known"
	}
	if why != "" {
		return " · full suite (" + why + ")"
	}
	return " · full suite"
}

func bnMoneyOr(v any) string {
	if n, ok := v.(float64); ok {
		return bnUSD(n)
	}
	return "unknown"
}

// bnDescribe is describe(row): the event label, detail and link.
func bnDescribe(row any) (string, string, any) {
	g := func(k string) any { return bnGet(row, k) }
	scope := "unknown scope"
	if bnStr(g("scope")) {
		scope = g("scope").(string)
	}
	switch bnEvent(row) {
	case "autopilot.grant":
		return "Grant started", "grant " + bnU(g("grant_id"), "unknown") + " · delegate " + bnU(g("delegate"), "unknown") + " · allow " + bnList(g("scopes")) + " · until " + bnU(g("until"), "unknown"), nil
	case "autopilot.stop":
		return "Grant stopped", "grant " + bnU(g("grant_id"), "unknown"), nil
	case "autopilot.expire":
		return "Grant expired", "grant " + bnU(g("grant_id"), "unknown") + " · window ended " + bnU(g("until"), "unknown"), nil
	case "autopilot.link":
		return "Log link stored", "grant " + bnU(g("grant_id"), "unknown"), bnStrOr(g("link"))
	case "autopilot.decision":
		conf := ""
		if _, ok := g("confidence").(float64); ok {
			conf = " · confidence " + bnU(g("confidence"), "unknown")
		}
		if g("acted") == true {
			return "Delegate decided", scope + " · acted · decision " + bnU(g("decision"), "no decision") + conf + " · " + bnU(g("decision_id"), "unknown"), nil
		}
		return "Question to the owner", scope + " · to owner (" + bnCo(g("owner_reason"), "unknown") + ")" + conf + " · " + bnU(g("decision_id"), "unknown"), nil
	case "autopilot.pause":
		return "Budget category paused", bnU(g("category"), "unknown") + " · spent " + bnUSD(apJSNumber(bnCoV(g("spent_usd"), 0.0))) + " of " + bnUSD(apJSNumber(bnCoV(g("cap_usd"), 0.0))) + " USD", nil
	case "autopilot.approve":
		return "Owner approval", bnU(g("key"), "unknown") + " · until " + bnU(g("until"), "unknown") + " · " + bnU(g("approval_id"), "unknown"), nil
	case "autopilot.restore":
		return "Approval restored", bnCo(g("key"), g("approval_id"), "approval"), nil
	case "autopilot.restore_skipped":
		reason := "changed by hand"
		if !bnEq(g("reason"), "changed-by-hand") {
			reason = bnCo(g("reason"), "unknown")
		}
		return "Restore skipped", bnCo(g("key"), g("approval_id"), "approval") + " · " + reason, nil
	case "autopilot.extra_round":
		return "Extra fix round", "grant " + bnU(g("grant_id"), "unknown") + " · decision " + bnU(g("decision_id"), "unknown"), nil
	case "autopilot.level":
		return "Coder level chosen", bnU(g("level"), "unknown") + " · lane " + bnCo(g("lane"), "none") + " · plan " + bnCo(g("plan_level"), "none") + " · decision " + bnU(g("decision_id"), "unknown"), nil
	case "autopilot.issue_failed":
		return "Waiver issue failed", "finding " + bnU(g("finding"), "unknown finding") + " · no issue opened", nil
	case "review.waived":
		return "Waiver by autopilot", "finding " + bnU(g("finding"), "unknown finding") + " · " + bnCo(g("severity"), "unknown severity") + " · issue " + bnCo(g("issue"), "none") + " · decision " + bnU(g("decision_id"), "unknown"), bnStrOr(g("issue"))
	case "session":
		return "Delegate session", bnCo(g("level"), "level unknown") + " · " + bnCo(g("provider"), "provider unknown") + " · status " + bnCo(g("status"), "unknown"), nil
	case "block.close":
		detail := "status " + bnCo(g("status"), "unknown")
		if sha := bnShort(bnFirst(g("commit"), g("sha"), g("commit_sha"))); bnStr(sha) {
			detail += " · commit " + sha.(string)
		}
		return "Block closed", detail, bnFirst(g("pr_url"), g("pr"))
	case "review.session_timeout":
		retried := "gave up"
		if g("retried") == true {
			retried = "retried"
		}
		return "Review session timed out", bnCo(g("lens"), g("role"), "session") + " · attempt " + bnU(g("attempt"), "unknown") + " · " + retried, nil
	case "review.schema_fallback":
		event := "Review answer off-schema twice"
		if bnEq(g("trigger"), "timeout") {
			event = "Review broken twice (timeout)"
		}
		refused := "retried once"
		if _, ok := g("refused").(string); ok {
			refused = "refused (" + bnU(g("refused"), "unknown") + ")"
		}
		return event, bnCo(g("lens"), g("role"), "session") + " · " + bnU(g("from_model"), "unknown model") + " → " + bnU(g("to_model"), "unknown model") + " · " + refused, nil
	case "worker.down":
		return "Worker down", "the pinned worker is not running", nil
	case "worker.replaced":
		return "Worker replaced", "the pinned worker was replaced by another process", nil
	case "ledger.tamper":
		return "Ledger check failed", "block " + bnCo(g("block"), "unknown") + " stopped", nil
	case "budget.refused":
		return "Budget refused a session", bnCo(g("role"), "session") + " · spent " + bnMoneyOr(g("spent_usd")) + " of " + bnMoneyOr(g("budget_usd")) + " USD", nil
	case "gate.red":
		return "Gate red", bnCo(g("gate"), "gate") + " · exit " + bnCo(g("code"), "unknown"), nil
	case "gate.done":
		green := "not all green"
		if g("all_ok") == true {
			green = "all green"
		}
		return "Gates finished", bnCo(g("stack"), "stack") + " · " + green + bnGateScope(row), nil
	}
	return bnJSString(g("event")), "", nil
}

// bnCoV is `a ?? fallback` as a value.
func bnCoV(v, fallback any) any {
	if bnNullish(v) {
		return fallback
	}
	return v
}

func (b *bnBuild) fullLog(rows []ledger.Row, scrub func(string) string) []any {
	mine := []bnItem{}
	for _, x := range b.ordered(rows) {
		if bnInLog(x.row) {
			mine = append(mine, x)
		}
	}
	key := func(x bnItem) int64 {
		if s, ok := x.time.(string); ok {
			if ms, ok, _ := bnDateMs(s); ok {
				return ms
			}
		}
		return 0
	}
	sort.SliceStable(mine, func(i, j int) bool {
		if a, c := key(mine[i]), key(mine[j]); a != c {
			return a > c
		}
		return mine[i].index > mine[j].index
	})
	out := []any{}
	for _, x := range mine {
		event, detail, link := bnDescribe(x.row)
		out = append(out, bnDeepScrub(ledger.Object{
			{Key: "time", Value: x.time}, {Key: "event", Value: event}, {Key: "block", Value: bnStrOr(bnGet(x.row, "block"))},
			{Key: "file", Value: bnStrOr(bnGet(x.row, "file"))}, {Key: "detail", Value: detail}, {Key: "link", Value: link},
		}, scrub))
	}
	return out
}

func bnWhere(block, file any) string {
	out := ""
	if bnStr(block) {
		out += " · " + bnU(block, "unknown")
	}
	if bnStr(file) {
		out += " · " + bnU(file, "unknown")
	}
	return out
}

var bnSchemaFallbackFix = map[string]string{
	"closed-book": "Set a second level that can run closed-book (not Codex), or review the file by hand.",
	"same-model":  "Set review.second_levels to a different model than the one that failed, or review the file by hand.",
	"budget":      "Raise the budget (only you can) or review the file by hand.",
}

func bnIncident(time any, what, effect, fix string) ledger.Object {
	return ledger.Object{{Key: "time", Value: time}, {Key: "what", Value: what}, {Key: "effect", Value: effect}, {Key: "fix", Value: fix}}
}

func bnSchemaIncident(r, time any) ledger.Object {
	g := func(k string) any { return bnGet(r, k) }
	var what string
	if bnEq(g("trigger"), "timeout") {
		what = "A " + bnCo(g("lens"), g("role"), "review") + " session broke twice on the same packet, the second time by timing out (" + bnU(g("from_model"), "unknown model") + ")"
	} else {
		what = "A " + bnCo(g("lens"), g("role"), "review") + " answer failed the schema twice on the same packet (" + bnU(g("from_model"), "unknown model") + ")"
	}
	refused, isText := g("refused").(string)
	if !isText {
		return bnIncident(time, what, "It was retried once on the second level ("+bnU(g("to_model"), "unknown model")+").", "Check the file's next review result.")
	}
	fix, known := bnSchemaFallbackFix[refused]
	if !known {
		fix = "Review the file by hand."
	}
	return bnIncident(time, what, "The second-level try was refused ("+bnU(refused, "unknown")+"); the review stays unavailable.", fix)
}

func bnPlural(n int, word string) string {
	if n == 1 {
		return strconv.Itoa(n) + " " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

func (b *bnBuild) blockLine(blockRows []any, entry any, id string) ledger.Object {
	pick := func(keys ...string) any {
		for i := len(blockRows) - 1; i >= 0; i-- {
			for _, k := range keys {
				if v := bnGet(blockRows[i], k); bnStr(v) {
					return v
				}
			}
		}
		return nil
	}
	branch, pr, sha := pick("branch"), pick("pr_url", "pr"), bnShort(pick("commit", "commit_sha", "sha"))
	files := func(ev string) int {
		seen := []string{}
		for _, r := range blockRows {
			if f := bnGet(r, "file"); bnEvent(r) == ev && bnStr(f) && !slices.Contains(seen, f.(string)) {
				seen = append(seen, f.(string))
			}
		}
		return len(seen)
	}
	results, proofs := 0, []any{}
	var gateRow any
	closed := false
	for _, r := range blockRows {
		switch bnEvent(r) {
		case "review.result":
			results++
		case "proof":
			proofs = append(proofs, r)
		case "gate.done":
			gateRow = r
		case "block.close":
			closed = true
		}
	}
	review := "none yet"
	if results+files("review.approved")+files("review.waived") != 0 {
		review = bnPlural(results, "result") + " · " + strconv.Itoa(files("review.approved")) + " approved · " + strconv.Itoa(files("review.waived")) + " waived"
	}
	tests := "not recorded"
	if len(proofs) > 0 {
		tests = bnPlural(len(proofs), "proof run")
		if iso := bnGet(proofs[len(proofs)-1], "isolation"); bnStr(iso) {
			tests += ", last " + iso.(string)
		}
	}
	gates := "none yet"
	switch {
	case gateRow != nil && bnGet(gateRow, "all_ok") == true:
		gates = "green"
	case gateRow != nil:
		gates = "red"
	case closed:
		gates = "passed at close"
	}
	status := bnGet(entry, "status")
	if bnNullish(status) {
		status = "unknown"
		if closed {
			status = "closed"
		}
	}
	stateText := bnJSString(status)
	switch {
	case bnEq(status, "closed"):
		stateText = "Closed, ready for you to mark ready and merge"
	case bnEq(status, "open"):
		stateText = "Open"
	case bnEq(status, "stopped"):
		stateText = "Stopped"
	case bnEq(status, humancheck.DeferredStatus):
		stateText = "Closed with human checks deferred to you (B632); it may land"
	}
	parts := []string{}
	if branch != nil {
		parts = append(parts, branch.(string))
	}
	if sha != nil {
		parts = append(parts, "commit "+sha.(string))
	}
	if pr != nil {
		parts = append(parts, pr.(string))
	}
	branchText := strings.Join(parts, " · ")
	if branchText == "" {
		branchText = "not recorded"
	}
	return ledger.Object{{Key: "block", Value: id}, {Key: "branch", Value: branchText}, {Key: "review", Value: review}, {Key: "tests", Value: tests}, {Key: "gates", Value: gates}, {Key: "state", Value: stateText}}
}

// binnacle is buildBinnacle; record is the run record, owner "" is unknown.
func (b *bnBuild) binnacle(rows []ledger.Row, record ledger.Object, now time.Time, scrub func(string) string, owner string) ledger.Object {
	runID := b.runID
	mine := b.ordered(rows)
	all := make([]any, len(mine))
	for i, x := range mine {
		all[i] = x.row
	}
	lastOf := func(event string) any {
		for i := len(all) - 1; i >= 0; i-- {
			if bnEvent(all[i]) == event {
				return all[i]
			}
		}
		return nil
	}
	var grant any
	if g := record.Get("autopilot"); bnObject(g) {
		grant = g
	}
	grantRow, linkRow := lastOf("autopilot.grant"), lastOf("autopilot.link")
	link := bnStrOr(bnGet(grant, "link"))
	if link == nil {
		link = bnStrOr(bnGet(linkRow, "link"))
	}

	// ---- blocks
	var blocksRec ledger.Object
	switch v := record.Get("blocks").(type) {
	case ledger.Object:
		blocksRec = v
	case []any:
		b.unsupported = true // Object.keys of an array
	}
	entry := func(id string) any { return bnGet(blocksRec, id) }
	ids := []string{}
	for _, f := range blocksRec {
		ids = append(ids, f.Key)
	}
	closedRow := func(id string) bool {
		for _, r := range all {
			if bnEvent(r) == "block.close" && bnEq(bnGet(r, "block"), id) {
				return true
			}
		}
		return false
	}
	for _, r := range all {
		if v := bnGet(r, "block"); bnEvent(r) == "block.close" && bnStr(v) && !slices.Contains(ids, v.(string)) {
			ids = append(ids, v.(string))
		}
	}
	sort.SliceStable(ids, func(i, j int) bool {
		oa, ob := bnGet(entry(ids[i]), "opened_at"), bnGet(entry(ids[j]), "opened_at")
		ta, tb := "", ""
		if !bnNullish(oa) {
			ta = bnJSString(oa)
		}
		if !bnNullish(ob) {
			tb = bnJSString(ob)
		}
		c, ok := bnCollate(ta, tb)
		if c == 0 {
			c, ok = bnCollate(ids[i], ids[j])
		}
		if !ok {
			b.unsupported = true
		}
		return c < 0
	})
	blocks := []any{}
	closedIDs := []string{}
	deferredIDs := []string{} // B632
	// B633: the open human checks of blocks past open hold the run's end.
	held := []humancheck.Entry{}
	if rehHeld(record) {
		key, _ := blockclose.ReadKey(b.home, runID)
		held = rehHolding(record, humancheck.Open(runID, record, all, key))
	}
	for _, id := range ids {
		blockRows := []any{}
		for _, r := range all {
			if bnEq(bnGet(r, "block"), id) {
				blockRows = append(blockRows, r)
			}
		}
		line := b.blockLine(blockRows, entry(id), id)
		if own := bnHeldFor(held, id); len(own) > 0 && bnEq(bnGet(entry(id), "status"), "closed") {
			line.Set("state", bnHeldState(len(own)))
		}
		blocks = append(blocks, line)
		status := bnGet(entry(id), "status")
		if bnNullish(status) {
			status = ""
			if closedRow(id) {
				status = "closed"
			}
		}
		if bnEq(status, "closed") {
			closedIDs = append(closedIDs, id)
		}
		if bnEq(status, humancheck.DeferredStatus) {
			deferredIDs = append(deferredIDs, id)
		}
	}

	// ---- decisions
	decisions := []any{}
	stopCmd := "code-forge autopilot stop --run " + runID
	decision := func(time any, d, why, reverse string) {
		decisions = append(decisions, ledger.Object{{Key: "time", Value: time}, {Key: "decision", Value: d}, {Key: "why", Value: why}, {Key: "reverse", Value: reverse}})
	}
	for _, x := range mine {
		g := func(k string) any { return bnGet(x.row, k) }
		switch {
		case bnEvent(x.row) == "autopilot.decision":
			if g("acted") == true {
				conf := "no confidence"
				if _, ok := g("confidence").(float64); ok {
					conf = "confidence " + bnJSString(g("confidence"))
				}
				decision(x.time, bnU(g("scope"), "unknown scope")+": the delegate chose \""+bnU(g("decision"), "no decision")+"\""+bnWhere(bnGet(g("subject"), "block"), bnGet(g("subject"), "file")),
					bnCo(g("reason"), "no reason recorded")+" ("+conf+")", "Overrule it: undo the action it allowed (see its own entry) and, to stop further ones, run "+stopCmd)
			} else {
				decision(x.time, "Asked the owner ("+bnU(g("scope"), "unknown scope")+"): "+bnCo(g("question"), "question not recorded"), "Not acted on: "+bnCo(g("failure"), g("owner_reason"), "unknown"), "Nothing was done; answer it yourself (see Open questions).")
			}
		case bnEvent(x.row) == "review.waived" && bnEq(g("by"), "autopilot"):
			decision(x.time, "Waived "+bnU(g("finding"), "unknown finding")+" ("+bnCo(g("severity"), "unknown severity")+") in "+bnU(g("file"), "unknown file")+", block "+bnU(g("block"), "unknown block"),
				bnCo(g("reason"), "no reason recorded")+" (decision "+bnU(g("decision_id"), "unknown")+")", "Reopen the finding: fix it in "+bnU(g("file"), "unknown file")+", then delete the waiver issue ("+bnCo(g("issue"), "none was opened")+")")
		case bnEvent(x.row) == "autopilot.extra_round":
			decision(x.time, "One extra fix round for "+bnU(g("file"), "unknown file")+", block "+bnU(g("block"), "unknown block"), "The delegate allowed it (decision "+bnU(g("decision_id"), "unknown")+")", "The round is spent; refuse further ones by stopping the grant or denying round:extra.")
		case bnEvent(x.row) == "autopilot.level":
			decision(x.time, "Block "+bnU(g("block"), "unknown block")+" codes at "+bnU(g("level"), "unknown"), "Lane "+bnCo(g("lane"), "none")+", plan level "+bnCo(g("plan_level"), "none")+" (decision "+bnU(g("decision_id"), "unknown")+")", "Open the block again with code-forge block open --level <other level>.")
		case bnEvent(x.row) == "autopilot.approve":
			decision(x.time, "You approved "+bnU(g("key"), "unknown")+" until "+bnU(g("until"), "unknown"), "Owner approval "+bnU(g("approval_id"), "unknown"), "It restores itself at "+bnU(g("until"), "unknown")+"; or set the old value back in .code-forge.yml and run code-forge run reload.")
		}
	}

	// ---- open questions and owner actions
	toOwner := []any{}
	for _, x := range mine {
		if bnEvent(x.row) == "autopilot.decision" && bnGet(x.row, "acted") != true {
			toOwner = append(toOwner, x.row)
		}
	}
	questions := []any{}
	ownerActions := []any{}
	action := func(text string) {
		ownerActions = append(ownerActions, ledger.Object{{Key: "text", Value: text}, {Key: "done", Value: false}})
	}
	for _, r := range toOwner {
		questions = append(questions, bnCo(bnGet(r, "question"), "question not recorded")+" ("+bnU(bnGet(r, "scope"), "unknown scope")+", "+bnCo(bnGet(r, "owner_reason"), "unknown")+")")
	}
	for _, r := range toOwner {
		action("Answer: " + bnCo(bnGet(r, "question"), "question not recorded") + " (" + bnU(bnGet(r, "scope"), "unknown scope") + ")")
	}
	// the latest review.done per (block, file), in first-seen key order (a Map)
	doneKeys := []string{}
	latestDone := map[string]bnItem{}
	for _, x := range mine {
		if bnEvent(x.row) != "review.done" {
			continue
		}
		k := bnJSString(bnGet(x.row, "block")) + "\x00" + bnJSString(bnGet(x.row, "file"))
		if _, seen := latestDone[k]; !seen {
			doneKeys = append(doneKeys, k)
		}
		latestDone[k] = x
	}
	for _, k := range doneKeys {
		x := latestDone[k]
		if !(apJSNumber(bnGet(bnGet(x.row, "findings_by_severity"), "critical")) > 0) {
			continue
		}
		cleared := false
		for _, y := range mine {
			ev := bnEvent(y.row)
			if y.index > x.index && bnEq(bnGet(y.row, "block"), bnGet(x.row, "block")) && bnEq(bnGet(y.row, "file"), bnGet(x.row, "file")) && (ev == "review.approved" || ev == "review.waived" && bnEq(bnGet(y.row, "by"), "human")) {
				cleared = true
			}
		}
		if !cleared {
			action("Decide the critical finding in " + bnU(bnGet(x.row, "file"), "unknown file") + " (block " + bnU(bnGet(x.row, "block"), "unknown block") + "): critical findings are never delegated")
		}
	}
	paused := []any{}
	if grant != nil {
		var keys []string
		if p, ok := bnGet(grant, "paused").(ledger.Object); ok {
			for _, f := range p {
				keys = append(keys, f.Key)
			}
		}
		sortUTF16Strings(keys)
		for _, k := range keys {
			paused = append(paused, k)
		}
	} else {
		for _, r := range all {
			if bnEvent(r) != "autopilot.pause" {
				continue
			}
			c := bnGet(r, "category")
			if _, isText := c.(string); !isText && c != undef {
				b.unsupported = true // a Set of other values
			}
			if !slices.ContainsFunc(paused, func(p any) bool { return bnEq(p, c) }) {
				paused = append(paused, c)
			}
		}
	}
	for _, c := range paused {
		action("Budget " + bnU(c, "unknown category") + " is paused: raise the cap or leave it (only you can raise a budget)")
		var p any = undef
		if grant != nil {
			if cs, ok := c.(string); ok {
				p = bnGet(bnGet(grant, "paused"), cs)
			}
		} else {
			for _, r := range all {
				if bnEvent(r) == "autopilot.pause" && bnEq(bnGet(r, "category"), c) {
					p = r
					break
				}
			}
		}
		if runTruthy(bnJSONish(p)) {
			questions = append(questions, "Budget "+bnU(c, "unknown category")+" stopped at "+bnUSD(apJSNumber(bnCoV(bnGet(p, "spent_usd"), 0.0)))+" of "+bnUSD(apJSNumber(bnCoV(bnGet(p, "cap_usd"), 0.0)))+" USD: raise the cap or leave it?")
		}
	}
	for _, r := range all {
		if bnEvent(r) == "autopilot.restore_skipped" {
			action("Check " + bnCo(bnGet(r, "key"), bnGet(r, "approval_id"), "the approval") + ": it was changed by hand, so it was not restored")
		}
	}
	for _, id := range closedIDs {
		if own := bnHeldFor(held, id); len(own) > 0 { // B633: not ready
			action(bnHeldAction(runID, id, own))
			continue
		}
		var pr any
		for i := len(all) - 1; i >= 0 && pr == nil; i-- {
			if bnEq(bnGet(all[i], "block"), id) {
				pr = bnFirst(bnFirst(bnGet(all[i], "pr_url"), bnGet(all[i], "pr")))
			}
		}
		text := "Mark block " + id + " ready and merge it"
		if pr != nil {
			text += " (" + pr.(string) + ")"
		}
		action(text)
	}
	// B626: a block whose blockers are all owner-only gets one owner line.
	for _, line := range settleOwnerLines(all, runID, settleOpenBlock(record)) {
		action(line)
	}

	// B632: one line per deferred block with the owner's clear command.
	if len(deferredIDs) > 0 {
		key, _ := blockclose.ReadKey(b.home, runID)
		open := humancheck.Open(runID, record, all, key)
		for _, id := range deferredIDs {
			action(bnDeferredAction(runID, id, open))
		}
	}

	// ---- incidents
	incidents := []any{}
	for _, x := range mine {
		r := x.row
		g := func(k string) any { return bnGet(r, k) }
		switch bnEvent(r) {
		case "review.session_timeout":
			effect, fix := "No retry left; the review was marked unavailable.", "Run the review again."
			if g("retried") == true {
				effect, fix = "It was retried.", "None needed unless it repeats."
			}
			incidents = append(incidents, bnIncident(x.time, "A "+bnCo(g("lens"), g("role"), "review")+" session timed out (attempt "+bnU(g("attempt"), "unknown")+")", effect, fix))
		case "review.schema_fallback":
			incidents = append(incidents, bnSchemaIncident(r, x.time))
		case "worker.down", "worker.replaced":
			what := "The pinned worker was replaced"
			if bnEvent(r) == "worker.down" {
				what = "The pinned worker was down"
			}
			incidents = append(incidents, bnIncident(x.time, what, "Reviews wait until the worker is back.", "Restart the worker for this run."))
		case "ledger.tamper":
			incidents = append(incidents, bnIncident(x.time, "A ledger check failed for block "+bnCo(g("block"), "unknown"), "The block was stopped.", "Look at the block before reopening it."))
		case "autopilot.issue_failed":
			incidents = append(incidents, bnIncident(x.time, "The waiver issue for "+bnU(g("finding"), "unknown finding")+" in "+bnU(g("file"), "unknown file")+" could not be opened", "The waiver stands without an issue.", "Open the tracking issue by hand."))
		case "budget.refused":
			incidents = append(incidents, bnIncident(x.time, "A "+bnCo(g("role"), "session")+" session was refused: budget "+bnMoneyOr(g("spent_usd"))+" of "+bnMoneyOr(g("budget_usd"))+" USD", "The session did not run.", "Raise the budget (only you can) or finish by hand."))
		case "autopilot.pause":
			incidents = append(incidents, bnIncident(x.time, "Budget category "+bnU(g("category"), "unknown")+" reached its stop at "+bnUSD(apJSNumber(bnCoV(g("spent_usd"), 0.0)))+" of "+bnUSD(apJSNumber(bnCoV(g("cap_usd"), 0.0)))+" USD", "Delegated actions in "+bnU(g("category"), "unknown")+" stopped.", "Raise the cap or leave it paused."))
		}
	}

	// ---- timeline (newest first)
	timeline := []any{}
	for _, e := range b.fullLog(rows, scrub) {
		o := e.(ledger.Object)
		text := o.Get("event").(string)
		if s, ok := o.Get("block").(string); ok && s != "" {
			text += " · " + s
		}
		if s, ok := o.Get("file").(string); ok && s != "" {
			text += " · " + s
		}
		if s := o.Get("detail").(string); s != "" {
			text += " — " + s
		}
		timeline = append(timeline, ledger.Object{{Key: "time", Value: o.Get("time")}, {Key: "event", Value: text}})
	}

	// ---- status
	stateText := "no grant"
	if grant != nil {
		if s := record.Get("status"); runTruthy(bnJSONish(s)) && !bnEq(s, "active") {
			stateText = "run ended"
		} else if g, ok := grant.(ledger.Object); ok {
			name, err := autopilot.GrantState(g, now)
			if err != nil {
				b.unsupported = true
			}
			stateText = name
		} else {
			b.unsupported = true
		}
	}
	waived, results := 0, 0
	approved, prs, readFiles := []string{}, []string{}, []string{}
	addUnique := func(list *[]string, v string) {
		if !slices.Contains(*list, v) {
			*list = append(*list, v)
		}
	}
	gateRuns := []int{}
	for i, r := range all {
		g := func(k string) any { return bnGet(r, k) }
		ev := bnEvent(r)
		if ev == "review.waived" && bnEq(g("by"), "autopilot") {
			waived++
		}
		if ev == "review.approved" {
			addUnique(&approved, bnU(g("block"), "unknown block")+"\x00"+bnU(g("file"), "unknown file"))
		}
		if ev == "review.result" {
			results++
		}
		if pr := bnFirst(g("pr_url"), g("pr")); pr != nil {
			addUnique(&prs, pr.(string))
		}
		if ev == "gate.done" {
			gateRuns = append(gateRuns, i)
		}
		if (ev == "review.waived" && bnEq(g("by"), "autopilot") || ev == "autopilot.extra_round") && bnStr(g("file")) {
			addUnique(&readFiles, g("file").(string))
		}
	}
	for _, r := range toOwner {
		if f := bnGet(bnGet(r, "subject"), "file"); bnStr(f) {
			addUnique(&readFiles, f.(string))
		}
	}
	fullSuite := -1
	for k := len(gateRuns) - 1; k >= 0; k-- {
		if !bnEq(bnGet(all[gateRuns[k]], "scope"), "related") {
			fullSuite = gateRuns[k]
			break
		}
	}
	fullSuiteText := "not recorded"
	switch {
	case fullSuite < 0 && len(gateRuns) > 0:
		fullSuiteText = "not recorded · related tests only"
	case fullSuite >= 0:
		fullSuiteText = "red"
		if bnGet(all[fullSuite], "all_ok") == true {
			fullSuiteText = "green"
		}
		if gateRuns[len(gateRuns)-1] != fullSuite {
			fullSuiteText += " · no full suite since " + bnU(bnGet(all[fullSuite], "ts"), "unknown time")
		}
	}
	base := "unknown"
	for _, id := range ids {
		if v := bnGet(entry(id), "base_sha"); bnStr(v) {
			base = bnShort(v).(string)
			break
		}
	}
	approx := 0
	for _, x := range mine {
		if x.approx {
			approx++
		}
	}
	var spendText string
	if g, ok := grant.(ledger.Object); ok {
		caps := bnGet(g, "caps")
		since, _ := time.Parse("2006-01-02T15:04:05.000Z", bnJSString(g.Get("started_at")))
		parts := []string{}
		for _, c := range []string{"coding", "review"} {
			spent := bnUSD(autopilotCategorySpend(rows, runID, c, since))
			if capValue, isNumber := bnGet(caps, c).(float64); isNumber {
				parts = append(parts, c+" "+spent+" of "+bnUSD(capValue)+" USD")
			} else {
				parts = append(parts, c+" "+spent+" USD (no cap)")
			}
		}
		spendText = strings.Join(parts, ", ")
	} else {
		total, _, _ := runstatus.RunSpend(all, runID)
		spendText = bnUSD(total) + " USD in total"
	}
	resultText := "no blocks yet"
	if len(ids) > 0 {
		resultText = strconv.Itoa(len(closedIDs)) + " of " + bnPlural(len(ids), "block") + " closed"
	}
	prText := "none recorded"
	if len(prs) > 0 {
		prText = strings.Join(prs, ", ")
	}
	filesText := "none"
	if len(readFiles) > 0 {
		filesText = strings.Join(readFiles[:min(5, len(readFiles))], ", ")
		if len(readFiles) > 5 {
			filesText += " and " + strconv.Itoa(len(readFiles)-5) + " more"
		}
	}
	window := "no grant"
	switch {
	case grant != nil:
		g := func(k string) any { return bnGet(grant, k) }
		window = stateText + " · grant " + bnU(g("grant_id"), "unknown") + " · " + bnU(g("started_at"), "unknown") + " to " + bnU(g("until"), "unknown") + " · delegate " + bnU(g("delegate"), "unknown") + " · allow " + bnList(g("scopes"))
	case grantRow != nil:
		window = "grant " + bnU(bnGet(grantRow, "grant_id"), "unknown") + " · until " + bnU(bnGet(grantRow, "until"), "unknown")
	}
	linkText := "none"
	if link != nil {
		linkText = link.(string)
	}
	item := func(name, value string) any {
		return ledger.Object{{Key: "item", Value: name}, {Key: "value", Value: value}}
	}
	status := []any{item("Result", resultText)}
	if len(held) > 0 { // B633
		status = append(status, item("Human checks", bnHumanChecksItem(runID, held)))
	}
	status = append(status,
		item("Stack/PRs", prText), item("Full suite", fullSuiteText),
		item("Reviews", bnPlural(results, "result")+" · "+bnPlural(len(approved), "file")+" approved · "+strconv.Itoa(waived)+" waived by autopilot"),
		item("Files to read", filesText), item("Base", base), item("Spend", spendText), item("Autopilot window", window), item("Log link", linkText),
	)
	if approx == 1 {
		status = append(status, item("Clock note", "1 row carries no valid timestamp and takes the time of the row before it"))
	} else if approx > 1 {
		status = append(status, item("Clock note", strconv.Itoa(approx)+" rows carry no valid timestamp and take the time of the rows before them"))
	}
	var ownerValue any
	if owner != "" {
		ownerValue = owner
	}
	out := ledger.Object{
		{Key: "title", Value: "Autopilot run " + runID},
		{Key: "byline", Value: ledger.Object{{Key: "date", Value: now.UTC().Format("2006-01-02")}, {Key: "owner", Value: ownerValue}}},
		{Key: "status", Value: status}, {Key: "decisions", Value: decisions}, {Key: "blocks", Value: blocks},
		{Key: "open_questions", Value: questions}, {Key: "owner_actions", Value: ownerActions},
		{Key: "incidents", Value: incidents}, {Key: "timeline", Value: timeline},
	}
	return bnDeepScrub(out, scrub).(ledger.Object)
}

// bnJSONish maps a missing value to nil for truthiness checks.
func bnJSONish(v any) any {
	if v == undef {
		return nil
	}
	return v
}

// ---- Markdown

func bnCell(v any) string {
	text := ""
	if !bnNullish(v) {
		text = bnJSString(v)
	}
	text = strings.ReplaceAll(text, "\r\n", " ")
	text = strings.ReplaceAll(text, "\n", " ")
	return strings.ReplaceAll(text, "|", `\|`)
}

func bnTable(head []string, body [][]any) []string {
	if len(body) == 0 {
		return []string{bnNoneYet, ""}
	}
	dashes := make([]string, len(head))
	for i := range dashes {
		dashes[i] = "---"
	}
	lines := []string{"| " + strings.Join(head, " | ") + " |", "| " + strings.Join(dashes, " | ") + " |"}
	for _, row := range body {
		cells := make([]string, len(row))
		for i, c := range row {
			cells[i] = bnCell(c)
		}
		lines = append(lines, "| "+strings.Join(cells, " | ")+" |")
	}
	return append(lines, "")
}

func bnRows(list any, keys ...string) [][]any {
	out := [][]any{}
	items, _ := list.([]any)
	for _, item := range items {
		o := item.(ledger.Object)
		row := []any{}
		for _, k := range keys {
			v := o.Get(k)
			if k == "time" && v == nil {
				v = ""
			}
			row = append(row, v)
		}
		out = append(out, row)
	}
	return out
}

func bnFinish(lines []string, scrub func(string) string) string {
	return scrub(strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n")
}

func bnRenderBinnacle(b ledger.Object, scrub func(string) string) string {
	byline := b.Get("byline").(ledger.Object)
	owner := "owner not set"
	if s, ok := byline.Get("owner").(string); ok {
		owner = s
	}
	lines := []string{"# " + b.Get("title").(string), "", byline.Get("date").(string) + " · " + owner, ""}
	lines = append(lines, "## Status at a glance", "")
	lines = append(lines, bnTable([]string{"Item", "Value"}, bnRows(b.Get("status"), "item", "value"))...)
	lines = append(lines, "## Decisions", "")
	lines = append(lines, bnTable([]string{"Time", "Decision", "Why", "What would reverse it"}, bnRows(b.Get("decisions"), "time", "decision", "why", "reverse"))...)
	lines = append(lines, "## Blocks", "")
	lines = append(lines, bnTable([]string{"Block", "Branch / PR", "Review", "Tests", "Gates", "State"}, bnRows(b.Get("blocks"), "block", "branch", "review", "tests", "gates", "state"))...)
	lines = append(lines, "## Open questions", "")
	questions, _ := b.Get("open_questions").([]any)
	if len(questions) == 0 {
		lines = append(lines, bnNoneYet)
	}
	for i, q := range questions {
		lines = append(lines, strconv.Itoa(i+1)+". "+bnCell(q))
	}
	lines = append(lines, "", "## Actions only you can take", "")
	actions, _ := b.Get("owner_actions").([]any)
	if len(actions) == 0 {
		lines = append(lines, bnNoneYet)
	}
	for _, a := range actions {
		o := a.(ledger.Object)
		mark := " "
		if o.Get("done") == true {
			mark = "x"
		}
		lines = append(lines, "- ["+mark+"] "+bnCell(o.Get("text")))
	}
	lines = append(lines, "", "## Incidents", "")
	lines = append(lines, bnTable([]string{"Time", "What", "Effect", "Fix"}, bnRows(b.Get("incidents"), "time", "what", "effect", "fix"))...)
	lines = append(lines, "## Timeline", "")
	timeline, _ := b.Get("timeline").([]any)
	if len(timeline) == 0 {
		lines = append(lines, bnNoneYet)
	}
	for _, t := range timeline {
		o := t.(ledger.Object)
		when := "unknown time"
		if s, ok := o.Get("time").(string); ok {
			when = s
		}
		lines = append(lines, "- "+when+" — "+bnCell(o.Get("event")))
	}
	lines = append(lines, "")
	return bnFinish(lines, scrub)
}

func bnRenderLog(runID string, entries []any, scrub func(string) string) string {
	body := [][]any{}
	for _, e := range entries {
		o := e.(ledger.Object)
		row := []any{}
		for _, k := range []string{"time", "event", "block", "file", "detail", "link"} {
			v := o.Get(k)
			if v == nil {
				v = ""
			}
			row = append(row, v)
		}
		body = append(body, row)
	}
	lines := append([]string{"# Autopilot full log " + runID, "", "Newest first.", ""}, bnTable([]string{"Time", "Event", "Block", "File", "Detail", "Link"}, body)...)
	return bnFinish(lines, scrub)
}

// ---- I/O

// bnGitOwner is gitOwner: the workspace's git user.name, GIT_* dropped.
var bnGitOwner = func(workspace any) string {
	ws, ok := workspace.(string)
	if !ok || ws == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", ws, "config", "user.name")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if cmd.Env == nil {
		cmd.Env = []string{}
	}
	out, err := childreg.Output(cmd)
	if err != nil {
		return ""
	}
	name := strings.TrimFunc(string(out), autopilotJSSpace)
	if n := bnLen16(name); n == 0 || n > 200 {
		return ""
	}
	return name
}

// bnCwd is process.cwd(): the kernel's answer, never $PWD.
func bnCwd() string {
	wd, err := syscall.Getwd()
	if err != nil {
		return ""
	}
	return wd
}

func bnWorkspace(record ledger.Object) string {
	ws, _ := record.Get("workspace").(string)
	return ws
}

func bnWriteFile(home, runID, name, text string) (string, error) {
	dir := filepath.Join(home, ".code-forge", "runs", runID)
	if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
		return "", &os.PathError{Op: "mkdir", Path: dir, Err: syscall.EEXIST} // mkdirSync's answer for a file there
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file := filepath.Join(dir, name)
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	tmp := file + "." + strconv.Itoa(os.Getpid()) + "." + hex.EncodeToString(suffix) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err == nil {
		_, err = f.WriteString(text)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = os.Rename(tmp, file)
	}
	if err != nil {
		_ = os.Remove(tmp) // never leave the temp file behind
		return "", err
	}
	return file, nil
}

// bnWriteFiles is writeAutopilotFiles: the binnacle, then the log.
func bnWriteFiles(home, runID string, rows []ledger.Row, record ledger.Object, now time.Time, owner string) (string, string, error) {
	b := &bnBuild{runID: runID, home: home}
	scrub := bnNewScrub(home, bnWorkspace(record))
	binnacle, err := bnWriteFile(home, runID, bnBinnacleFile, bnRenderBinnacle(b.binnacle(rows, record, now, scrub, owner), scrub))
	if err != nil {
		return "", "", err
	}
	log, err := bnWriteFile(home, runID, bnLogFile, bnRenderLog(runID, b.fullLog(rows, scrub), scrub))
	if err != nil {
		return "", "", err
	}
	return binnacle, log, nil
}

// bnRefresh is refreshAutopilotFiles.
func bnRefresh(home, runID string, now time.Time) (string, string, error) {
	record, err := state.ReadRun(home, runID)
	if err != nil {
		return "", "", err
	}
	project, _ := record.Get("project").(string)
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), project)
	if err != nil {
		return "", "", err
	}
	return bnWriteFiles(home, runID, rows, record, now, bnGitOwner(record.Get("workspace")))
}

// autopilotRecordFiles is recordFiles: nil without a grant or with a link.
func autopilotRecordFiles(o *runOut, home, runID string, grant ledger.Object, now time.Time) ledger.Object {
	if grant == nil || runTruthy(grant.Get("link")) {
		return nil
	}
	binnacle, log, err := bnRefresh(home, runID, now)
	if err != nil {
		o.err("autopilot: the binnacle files could not be written (" + blockIOMessage(err) + ")\n")
		return nil
	}
	return ledger.Object{{Key: "binnacle", Value: binnacle}, {Key: "log", Value: log}}
}

// autopilotFilesValue is the status JSON's files: null or {binnacle, log}.
func autopilotFilesValue(files ledger.Object) any {
	if files == nil {
		return nil
	}
	return files
}

// autopilotRecordText is recordText.
func autopilotRecordText(link any, files ledger.Object) string {
	if runTruthy(link) {
		return "  link      " + bnJSString(link) + "\n"
	}
	if files != nil {
		return "  link      none\n  binnacle  " + files.Get("binnacle").(string) + "\n  full log  " + files.Get("log").(string) + "\n"
	}
	return "  link      none\n"
}

// bnSupported dry-runs every build over the record and rows: false when any
// input is read differently by Go than by Node.
func bnSupported(home, runID string, rows []ledger.Row, record ledger.Object, now time.Time) bool {
	b := &bnBuild{runID: runID, home: home}
	scrub := bnNewScrub(home, bnWorkspace(record))
	b.binnacle(rows, record, now, scrub, "")
	b.fullLog(rows, scrub)
	return !b.unsupported
}

var (
	bnLinkPlain = regexp.MustCompile(`^https://([a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*)(/[A-Za-z0-9\-._~!$&'()*+,;=:@/]*)?(\?[A-Za-z0-9\-._~!$&()*+,;=:@/?]*)?(#[A-Za-z0-9\-._~!$&'()*+,;=:@/?]*)?$`)
	bnLinkIPv4  = regexp.MustCompile(`(?:^|\.)(?:\d+|0x[0-9a-f]*)$`)
)

// bnParseLink is parseLink for the plain https subset whose WHATWG href Go
// knows: a lowercase DNS host without port, credentials, punycode or IPv4
// form; a path without dot segments or percent escapes. known is false for any
// other text (Node reads it).
func bnParseLink(text string) (link string, usage error, known bool) {
	if text == "" || bnLen16(text) > bnLinkMax {
		return "", &runFail{"--link must be an https URL of at most " + strconv.Itoa(bnLinkMax) + " characters", true}, true
	}
	m := bnLinkPlain.FindStringSubmatch(text)
	if m == nil || strings.Contains(m[1], "xn--") || bnLinkIPv4.MatchString(m[1]) {
		return "", nil, false
	}
	for _, seg := range strings.Split(m[2], "/") {
		if seg == "." || seg == ".." {
			return "", nil, false
		}
	}
	link = text
	if m[2] == "" {
		link = "https://" + m[1] + "/" + m[3] + m[4]
	}
	if bnMaskSecrets(link) != link {
		return "", &runFail{"--link looks like it carries a secret; give a plain URL", true}, true
	}
	return link, nil, true
}

// autopilotStoreLink is storeLink: the signed autopilot.link row, then the
// grant's link, under the run lock.
func autopilotStoreLink(home, runID, link string, now time.Time) error {
	if _, err := autopilotCheckExpiry(home, runID, now); err != nil {
		return err
	}
	return state.UpdateRun(home, runID, func(record *ledger.Object) error {
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
		if stateName != "active" {
			return &runFail{"run " + runID + "'s autopilot grant is " + stateName + "; the link goes into an active grant", false}
		}
		if _, err := autopilotWriteSigned(home, runID, ledger.Object{{Key: "event", Value: "autopilot.link"}, {Key: "grant_id", Value: grant.Get("grant_id")}, {Key: "link", Value: link}, {Key: "ts", Value: autopilotStamp(now)}}); err != nil {
			return err
		}
		grant.Set("link", link)
		record.Set("autopilot", grant)
		return nil
	})
}

// autopilotBinnacle is runBinnacle.
func autopilotBinnacle(o *runOut, home, runID, sub string, flags autopilotFlags) int {
	if flags.bools["json"] && flags.bools["markdown"] {
		return autopilotReport(o, sub, autopilotUsageFail("give --json or --markdown, not both"))
	}
	clock := func() time.Time { return autopilotNow().Truncate(time.Millisecond) }
	if _, has := flags.values["link"]; has && sub == "binnacle" {
		checked, err := autopilotCheckExpiry(home, runID, clock())
		if err != nil {
			return autopilotReport(o, sub, err)
		}
		if checked.State == "expired" {
			return autopilotReport(o, sub, &runFail{autopilot.ExpiredMessage(runID, autopilotText(checked.Grant.Get("until"))), false})
		}
		link, usage, _ := bnParseLink(flags.values["link"])
		if usage != nil {
			return autopilotReport(o, sub, usage)
		}
		if err := autopilotStoreLink(home, runID, link, clock()); err != nil {
			return autopilotReport(o, sub, err)
		}
	}
	now := clock()
	if _, err := autopilotCheckExpiry(home, runID, now); err != nil {
		return autopilotReport(o, sub, err)
	}
	record, err := state.ReadRun(home, runID)
	if err != nil {
		return autopilotReport(o, sub, err)
	}
	project, _ := record.Get("project").(string)
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), project)
	if err != nil {
		return autopilotReport(o, sub, err)
	}
	owner := bnGitOwner(record.Get("workspace"))
	b := &bnBuild{runID: runID, home: home}
	scrub := bnNewScrub(home, bnWorkspace(record))
	switch {
	case flags.bools["json"]:
		var data any
		if sub == "binnacle" {
			data = b.binnacle(rows, record, now, scrub, owner)
		} else {
			data = b.fullLog(rows, scrub)
		}
		text, _ := ledger.Stringify(data, true)
		o.out(string(text) + "\n")
	case flags.bools["markdown"]:
		binnacle, log, err := bnWriteFiles(home, runID, rows, record, now, owner)
		if err != nil {
			return autopilotReport(o, sub, err)
		}
		if sub == "binnacle" {
			o.out(binnacle + "\n")
		} else {
			o.out(log + "\n")
		}
	case sub == "binnacle":
		o.out(bnRenderBinnacle(b.binnacle(rows, record, now, scrub, owner), scrub))
	default:
		// renderLogMarkdown without a context: the home dir and process.cwd()
		o.out(bnRenderLog(runID, b.fullLog(rows, scrub), bnNewScrub(home, bnCwd())))
	}
	return 0
}

// bnDeferredAction is the owner action for one closed_deferred_human block:
// its open human checks and the command that clears one (B632).
func bnDeferredAction(runID, id string, open []humancheck.Entry) string {
	checks, clauses := []string{}, []string{}
	for _, e := range open {
		if e.Block != id {
			continue
		}
		text := e.Clause + " " + statusQuote(e.Check)
		if w, ok := e.WaitsFor.(string); ok {
			text += " (waits for: " + w + ")"
		}
		checks = append(checks, text)
		clauses = append(clauses, e.Clause)
	}
	if len(clauses) == 0 {
		return "Block " + id + " has human checks deferred to you, but none is open now: see code-forge run human-checks list --run " + runID
	}
	clause := clauses[0]
	if len(clauses) > 1 {
		clause = "<" + strings.Join(clauses, "|") + ">"
	}
	return "Check block " + id + " by hand (its human checks were deferred under autopilot): " + strings.Join(checks, "; ") + "; then clear each with code-forge run human-checks clear --run " + runID + " --block " + id + " --clause " + clause + " --note \"<what you checked>\""
}
