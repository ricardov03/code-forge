package gatecheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
)

// stopExitsLine is src/review/stop-exits.mjs stopExitsLine.
const stopExitsLine = "exits: block reopen, block waive, review-file --answers, re-decompose"

// ResultFile is a ticket's signed result file as the gate reads it (Node's
// resultFilesFor). A nil *ResultFile is a missing or unverifiable file.
type ResultFile struct{ Status, Approved, Findings jv }

// AcceptedHit is a transcript hit a signed rule_break.accepted row cleared.
// By is "human", or "autopilot" for a B390 decision under an active grant.
type AcceptedHit struct {
	Rule   string
	Line   int
	Reason any
	By     string
}

// Input is the whole of checkBlockReviews' input. ProofFiles nil means Files.
type Input struct {
	Run, Block  string
	Files       []File
	ProofFiles  []File
	Rows        []Row
	Key         []byte
	HighPaths   []any
	Unowned     []string
	NoChanges   bool
	Hits        []Blocker
	Transcript  string
	ResultFiles map[string]*ResultFile
	// Ordered holds a row's JSON.parse property order, keyed by the row's
	// map pointer (OrderedKey). B498: an object or array decision prints
	// with JSON.stringify in that order; without it the result is unknown.
	Ordered map[uintptr]ledger.Object
}

// OrderedKey is the Input.Ordered key of a row.
func OrderedKey(r Row) uintptr { return reflect.ValueOf(r).Pointer() }

// jv is a JSON value as JavaScript reads a property: ok false is undefined.
type jv struct {
	v  any
	ok bool
}

func prop(r map[string]any, k string) jv { v, ok := r[k]; return jv{v, ok} }

// strictEq is JavaScript's === on JSON values; objects and arrays are never equal.
func strictEq(a, b jv) bool {
	if !a.ok || !b.ok {
		return !a.ok && !b.ok
	}
	switch x := a.v.(type) {
	case nil:
		return b.v == nil
	case string:
		y, ok := b.v.(string)
		return ok && x == y
	case float64:
		y, ok := b.v.(float64)
		return ok && x == y
	case bool:
		y, ok := b.v.(bool)
		return ok && x == y
	}
	return false
}

func str(s string) jv { return jv{s, true} }

func nonEmptyText(v jv) bool { s, isText := v.v.(string); return v.ok && isText && s != "" }

// Truthy is JavaScript's Boolean(v) for a JSON value; has false is undefined.
func Truthy(v any, has bool) bool {
	if !has {
		return false
	}
	switch x := v.(type) {
	case nil:
		return false
	case string:
		return x != ""
	case bool:
		return x
	case float64:
		return x != 0 && x == x
	}
	return true
}

// jsString is String(v) for a JSON value.
func jsString(v jv) string {
	if !v.ok {
		return "undefined"
	}
	switch x := v.v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case map[string]any:
		return "[object Object]"
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			if item != nil {
				parts[i] = jsString(jv{item, true})
			}
		}
		return strings.Join(parts, ",")
	}
	data, _ := ledger.Stringify(v.v, false)
	return string(data)
}

type verifier struct {
	key   []byte
	cache map[uintptr]bool
}

func (v *verifier) ok(r Row) bool {
	p := reflect.ValueOf(r).Pointer()
	if got, has := v.cache[p]; has {
		return got
	}
	got := Verified(r, v.key)
	v.cache[p] = got
	return got
}

type finding struct {
	id       string
	severity any // a string, or nil when the row records none (a cap)
}
type denial struct {
	event string
	open  []finding
}

var openRank = map[string]int{"nit": 0, "warning": 1, "critical": 2, "unknown": 3}

// openFindings is gate-check.mjs openFindings.
func openFindings(v any) []finding {
	list, ok := v.([]any)
	if !ok {
		return []finding{}
	}
	out := []finding{}
	for i, value := range list {
		f, _ := value.(map[string]any)
		id, isString := f["id"].(string)
		if !isString || !findingID.MatchString(id) {
			id = fmt.Sprintf("X%d", i+1)
		}
		severity, _ := f["severity"].(string)
		if _, known := openRank[severity]; !known || severity == "unknown" {
			severity = "unknown"
		}
		at := slices.IndexFunc(out, func(f finding) bool { return f.id == id })
		if at < 0 {
			out = append(out, finding{id, severity})
		} else if openRank[severity] > openRank[out[at].severity.(string)] {
			out[at].severity = severity
		}
	}
	return out
}

// denialOf is gate-check.mjs denialOf.
func denialOf(r Row, results map[string]*ResultFile) *denial {
	event := Text(r["event"])
	switch event {
	case "review.result":
		if NeverReviewed(r) {
			return nil
		}
		judge := func(approved any, open []finding) *denial {
			if approved == true && !slices.ContainsFunc(open, func(f finding) bool { return f.severity == "critical" }) {
				return nil
			}
			return &denial{event, open}
		}
		if approved, ok := r["approved"].(bool); ok {
			return judge(approved, openFindings(r["open"]))
		}
		var file *ResultFile
		if ticket, ok := r["ticket"].(string); ok {
			file = results[ticket]
		}
		if file == nil || !strictEq(file.Status, prop(r, "status")) {
			return &denial{event, []finding{}}
		}
		var approved any
		if file.Approved.ok {
			approved = file.Approved.v
		}
		return judge(approved, openFindings(file.Findings.v))
	case "review.round":
		if r["open_after"] == float64(0) {
			return nil
		}
		return &denial{event, []finding{}}
	case "review.cap":
		open := []finding{}
		for _, v := range array(r["open"]) {
			id, ok := v.(string)
			if ok && id != "" && !slices.ContainsFunc(open, func(f finding) bool { return f.id == id }) {
				open = append(open, finding{id, nil})
			}
		}
		return &denial{event, open}
	}
	return nil
}

var severityRank = map[string]int{"nit": 0, "warning": 1, "critical": 2, "blocker": 3}

// recordedSeverity is gate-check.mjs recordedSeverity: the severity, or ""
// with why not-recorded, unverified or unknown.
func recordedSeverity(rows []Row, block, file, id jv, ok func(Row) bool) (string, string) {
	type named struct {
		row      Row
		severity jv
	}
	all := []named{}
	for _, r := range rows {
		if !strictEq(prop(r, "block"), block) || !strictEq(prop(r, "file"), file) {
			continue
		}
		event := r["event"]
		if event == "review.triage" || event == "review.late_finding" {
			if strictEq(prop(r, "finding"), id) {
				all = append(all, named{r, prop(r, "severity")})
			}
		} else if list, isList := r["open"].([]any); event == "review.result" && r["status"] != "answers" && isList {
			for _, v := range list {
				if o, isMap := v.(map[string]any); isMap && strictEq(prop(o, "id"), id) {
					all = append(all, named{r, prop(o, "severity")})
				}
			}
		}
	}
	if len(all) == 0 {
		return "", "not-recorded"
	}
	for _, n := range all {
		if !ok(n.row) {
			return "", "unverified"
		}
	}
	worst := ""
	for _, n := range all {
		s, isString := n.severity.v.(string)
		rank, known := severityRank[s]
		if !isString || !known {
			return "", "unknown"
		}
		if worst == "" || rank > severityRank[worst] {
			worst = s
		}
	}
	return worst, ""
}

func delegable(severity any) bool {
	return severity == nil || severity == "warning" || severity == "nit"
}

func answerable(severity any) bool {
	return severity == nil || severity == "critical" || severity == "warning" || severity == "nit"
}

// isUnwaived is agent-waiver.mjs isUnwaived over rows after the waiver.
func isUnwaived(rows []Row, at int, ok func(Row) bool) bool {
	w := rows[at]
	for _, r := range rows[at+1:] {
		if r["event"] == "review.unwaived" && strictEq(prop(r, "run"), prop(w, "run")) && strictEq(prop(r, "block"), prop(w, "block")) &&
			strictEq(prop(r, "file"), prop(w, "file")) && strictEq(prop(r, "finding"), prop(w, "finding")) && r["by"] == "human" && ok(r) {
			return true
		}
	}
	return false
}

// countedAgentWaivers is agent-waiver.mjs countedAgentWaivers.
func countedAgentWaivers(rows []Row, block string, ok func(Row) bool) []Row {
	out := []Row{}
	for i, r := range rows {
		if r["block"] != block || r["event"] != "review.waived" || r["by"] != "agent" || r["verdict"] != "agree" {
			continue
		}
		test, _ := r["test"].(string)
		if !(r["severity"] == "warning" || r["severity"] == "nit" || r["severity"] == "critical" && r["autopilot"] == true && test != "") {
			continue
		}
		if !ok(r) || isUnwaived(rows, i, ok) {
			continue
		}
		same := []Row{}
		for _, other := range rows {
			if strictEq(prop(other, "run"), prop(r, "run")) {
				same = append(same, other)
			}
		}
		if severity, _ := recordedSeverity(same, str(block), prop(r, "file"), prop(r, "finding"), ok); severity != "" && r["severity"] == severity {
			out = append(out, r)
		}
	}
	return out
}

// AgentWaivers are the counted agent waiver rows of the block (Node agentWaivers).
func AgentWaivers(rows []Row, block string, key []byte) []Row {
	v := &verifier{key, map[uintptr]bool{}}
	return countedAgentWaivers(rows, block, v.ok)
}

// AgentReopens are the verified agent reopen rows of the block (Node agentReopens).
func AgentReopens(rows []Row, block string, key []byte) []Row {
	out := []Row{}
	for _, r := range rows {
		if r["event"] == "review.reopened" && r["block"] == block && r["by"] == "agent" && Verified(r, key) {
			out = append(out, r)
		}
	}
	return out
}

// AgentWaiversAcked is agent-waiver.mjs agentWaiversAcked. known is false when
// the digest order needs a collation Go does not carry (non-ASCII text).
func AgentWaiversAcked(requireAck bool, run, block string, rows []Row, key []byte) (acked, known bool) {
	if !requireAck {
		return true, true
	}
	counted := AgentWaivers(rows, block, key)
	if len(counted) == 0 {
		return true, true
	}
	candidates := []Row{}
	for _, r := range rows {
		if r["event"] == "agent_waivers.acked" && r["block"] == block && r["run"] == run && r["by"] == "human" {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		return false, true
	}
	set, ordered, ok := agentWaiverSet(counted)
	if !ok {
		return false, false
	}
	if !ordered {
		return ackedByAnyOrder(set, func(e waiverEntry) []any { return e.item }, candidates, key)
	}
	digest, ok := waiverSetDigest(set)
	if !ok {
		return false, false
	}
	for _, r := range candidates {
		if r["digest"] == digest && Verified(r, key) {
			return true, true
		}
	}
	return false, true
}

// AgentWaiverDigest is agent-waiver.mjs agentWaiverDigest over the counted
// rows. ok is false when the order needs a collation Go does not carry.
func AgentWaiverDigest(counted []Row) (string, bool) {
	set, ordered, ok := agentWaiverSet(counted)
	if !ok || !ordered {
		return "", false
	}
	return waiverSetDigest(set)
}

type waiverEntry struct {
	text string
	item []any
}

// agentWaiverSet is the [file, finding, mac] set of the counted rows in
// Node's localeCompare order. ordered is false when two or more items need a
// collation Go does not carry; ok is false when an item cannot be written.
func agentWaiverSet(counted []Row) (set []waiverEntry, ordered, ok bool) {
	set = []waiverEntry{}
	for _, r := range counted {
		item := []any{r["file"], r["finding"], r["mac"]}
		for i, k := range []string{"file", "finding", "mac"} {
			if _, has := r[k]; !has {
				item[i] = nil // JSON.stringify writes undefined array items as null
			}
		}
		text, err := ledger.Stringify(item, false)
		if err != nil {
			return nil, false, false
		}
		set = append(set, waiverEntry{string(text), item})
	}
	known := true
	slices.SortStableFunc(set, func(a, b waiverEntry) int {
		if less, ok := localeLess(a.text, b.text); !ok {
			known = false
			return 0
		} else if less {
			return -1
		}
		if less, _ := localeLess(b.text, a.text); less {
			return 1
		}
		return 0
	})
	return set, known || len(set) <= 1, true
}

// waiverSetDigest is the sha256 hex of JSON.stringify of the ordered items.
func waiverSetDigest(set []waiverEntry) (string, bool) {
	items := []any{}
	for _, e := range set {
		items = append(items, e.item)
	}
	data, err := ledger.Stringify(items, false)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true
}

// ackedByAnyOrder decides an ack whose order needs a collation Go does not
// carry (a non-ASCII file name). Node's digest is the digest of one order of
// the set, so when no order's digest is on a verified candidate Node does not
// ack either. A match stays unknown: Go cannot prove Node sorts to that order.
func ackedByAnyOrder[E any](set []E, item func(E) []any, candidates []Row, key []byte) (bool, bool) {
	if len(set) > 8 {
		return false, false
	}
	signed := map[string]bool{}
	for _, r := range candidates {
		if d, ok := r["digest"].(string); ok && Verified(r, key) {
			signed[d] = true
		}
	}
	if len(signed) == 0 {
		return false, true
	}
	order := make([]int, len(set))
	for i := range order {
		order[i] = i
	}
	var walk func(k int) bool
	walk = func(k int) bool {
		if k == len(order) {
			items := make([]any, len(order))
			for i, at := range order {
				items[i] = item(set[at])
			}
			data, err := ledger.Stringify(items, false)
			if err != nil {
				return true
			}
			sum := sha256.Sum256(data)
			return signed[hex.EncodeToString(sum[:])]
		}
		for i := k; i < len(order); i++ {
			order[k], order[i] = order[i], order[k]
			found := walk(k + 1)
			order[k], order[i] = order[i], order[k]
			if found {
				return true
			}
		}
		return false
	}
	if walk(0) {
		return false, false
	}
	return false, true
}

var hitDetail = regexp.MustCompile(`^transcript line (\d+): (.+)$`)

func jsTrim(s string) string {
	return strings.Trim(s, "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff")
}

// acceptedHits is accept-hit.mjs acceptedTranscriptHits over the scanned hits.
// B390: a row by "autopilot" also counts when it names a signed
// autopilot.grant of this run whose window holds the row's time (autopilot
// is the stand-in decider the owner granted; see grantProblem).
func acceptedHits(in Input, ok func(Row) bool, autopilotOK func(Row) bool) ([]Blocker, []AcceptedHit) {
	remaining, accepted := []Blocker{}, []AcceptedHit{}
	if in.Transcript == "" {
		return append(remaining, in.Hits...), accepted
	}
	approvals := []Row{}
	for _, r := range in.Rows {
		reason, isString := r["reason"].(string)
		if r["event"] == "rule_break.accepted" && r["run"] == in.Run && r["block"] == in.Block && (r["by"] == "human" || r["by"] == "autopilot" && autopilotOK(r)) && isString && jsTrim(reason) != "" && ok(r) {
			approvals = append(approvals, r)
		}
	}
	lines := strings.Split(in.Transcript, "\n")
	for _, hit := range in.Hits {
		m := hitDetail.FindStringSubmatch(hit.Detail)
		line := 0
		if m != nil {
			line, _ = strconv.Atoi(m[1])
		}
		if line < 1 || line > len(lines) {
			remaining = append(remaining, hit)
			continue
		}
		sum := sha256.Sum256([]byte(lines[line-1]))
		hash := hex.EncodeToString(sum[:])
		at := slices.IndexFunc(approvals, func(r Row) bool { return r["rule"] == m[2] && r["line_sha256"] == hash })
		if at >= 0 {
			by, _ := approvals[at]["by"].(string)
			accepted = append(accepted, AcceptedHit{m[2], line, approvals[at]["reason"], by})
		} else {
			remaining = append(remaining, hit)
		}
	}
	return remaining, accepted
}

type gate struct {
	in        Input
	v         *verifier
	mine      []Row
	amended   bool
	current   map[string]string
	human     []Row
	agent     []Row
	autopilot []Row
	answers   []Row
	runRows   []Row
	unknown   bool
	// except: the answers ticket whose own rows answered leaves out (B609)
	except *string
}

func (g *gate) ok(r Row) bool { return g.v.ok(r) }

func automaticReopen(r Row) bool {
	from, a := r["from_hash"].(string)
	to, b := r["to_hash"].(string)
	return r["by"] == "auto" && r["reason"] == "auto-requeue: content changed after stopped review" && r["from"] == "stopped" && a && b && from != to
}

func (g *gate) superseded(at int) bool {
	cap := g.mine[at]
	file, _ := cap["file"].(string)
	hash, has := g.current[file]
	if !has || cap["file"] != file {
		return false
	}
	for i := at + 1; i < len(g.mine); i++ {
		r := g.mine[i]
		verdict, _ := r["verdict"].(map[string]any)
		if r["event"] != "review.reopened" || r["run"] != g.in.Run || r["file"] != file ||
			!(r["by"] == "human" || r["by"] == "agent" && verdict["verdict"] == "agree" || automaticReopen(r)) || !g.ok(r) {
			continue
		}
		for j := i + 1; j < len(g.mine); j++ {
			round := g.mine[j]
			if round["event"] == "review.round" && round["run"] == g.in.Run && round["file"] == file && round["content_hash"] == hash && g.ok(round) {
				return true
			}
		}
	}
	return false
}

type latest struct {
	lastApproval int
	denial       *denial
	latest       *denial
	latestRow    Row
}

func (g *gate) latestOf(file, hash string) latest {
	out := latest{lastApproval: -1}
	lastDenial := -1
	for i, r := range g.mine {
		if r["file"] != file {
			continue
		}
		if r["event"] == "review.approved" {
			if r["content_hash"] == hash && g.ok(r) {
				out.lastApproval = i
			}
			continue
		}
		if h := prop(r, "content_hash"); h.ok && !strictEq(h, str(hash)) {
			continue
		}
		if d := denialOf(r, g.in.ResultFiles); d != nil {
			out.latest, out.latestRow, lastDenial = d, r, i
		}
	}
	if out.lastApproval >= 0 && lastDenial > out.lastApproval {
		out.denial = out.latest
	}
	return out
}

// matches is Node's r.finding === id && r.file === file; file may be
// undefined (a late finding row with no file).
func (g *gate) matches(r Row, file, id jv) bool {
	return strictEq(prop(r, "finding"), id) && strictEq(prop(r, "file"), file)
}

func (g *gate) answered(fileV, id jv, severity any) bool {
	file, isText := fileV.v.(string)
	if strictEq(id, str("proof")) || !answerable(severity) || !isText || !fileV.ok {
		return false
	}
	now, has := g.current[file]
	if !has {
		return false
	}
	for _, r := range g.answers {
		by, isString := r["by_file"].(string)
		if g.except != nil && r["ticket"] == *g.except {
			continue
		}
		if r["file"] == file && r["content_hash"] == now && isString && by != file && strictEq(prop(r, "finding"), id) {
			if target, ok := g.current[by]; ok && r["by_hash"] == target {
				return true
			}
		}
	}
	return false
}

// waiverOf is reviewRules' waiverOf: "human", "answer", "agent", "autopilot"
// or "" with why the autopilot waivers do not count.
func (g *gate) waiverOf(file, id string, severity, answerSeverity any) (string, string) {
	return g.waiverOfValue(str(file), str(id), severity, answerSeverity)
}

func (g *gate) waiverOfValue(file, id jv, severity, answerSeverity any) (string, string) {
	if slices.ContainsFunc(g.human, func(r Row) bool { return g.matches(r, file, id) }) {
		return "human", ""
	}
	if g.answered(file, id, answerSeverity) {
		return "answer", ""
	}
	if !strictEq(id, str("proof")) && (delegable(severity) || severity == "critical") {
		recorded, _ := recordedSeverity(g.mine, str(g.in.Block), file, id, g.ok)
		may := delegable(severity) && (recorded == "warning" || recorded == "nit") || severity == "critical" && recorded == "critical"
		if may && slices.ContainsFunc(g.agent, func(r Row) bool { return g.matches(r, file, id) && r["severity"] == recorded }) {
			return "agent", ""
		}
	}
	named := []Row{}
	for _, r := range g.autopilot {
		if g.matches(r, file, id) {
			named = append(named, r)
		}
	}
	if len(named) == 0 {
		return "", ""
	}
	if !delegable(severity) {
		return "", "the latest review records it " + jsString(jv{severity, true}) + "; only the human waives it"
	}
	first := ""
	for i, w := range named {
		problem := g.autopilotProblem(w)
		if problem == "" {
			return "autopilot", ""
		}
		if i == 0 {
			first = problem
		}
	}
	return "", first
}

func (g *gate) waiverFor(file, id string, answerSeverity any) (bool, string) {
	return g.waiverForValue(str(file), str(id), answerSeverity)
}

func (g *gate) waiverForValue(file, id jv, answerSeverity any) (bool, string) {
	by, invalid := g.waiverOfValue(file, id, nil, answerSeverity)
	return by != "", invalid
}

// date reads Date.parse and marks the result unknown when Go cannot match V8.
func (g *gate) date(v jv) (float64, bool) {
	ms, ok, known := jsDateParse(v.v, v.ok)
	if !known {
		g.unknown = true
	}
	return ms, ok
}

func (g *gate) autopilotProblem(w Row) string {
	if !g.ok(w) {
		return "the waiver has no valid MAC"
	}
	if w["finding"] == "proof" {
		return "proof is never delegated"
	}
	recorded, why := recordedSeverity(g.mine, prop(w, "block"), prop(w, "file"), prop(w, "finding"), g.ok)
	if recorded == "" {
		return "the finding's severity is " + why + "; critical findings are never delegated"
	}
	if recorded != "warning" && recorded != "nit" {
		return "the finding is " + recorded + "; critical findings are never delegated"
	}
	if w["severity"] != recorded {
		return "the waiver says " + jsString(prop(w, "severity")) + " but the finding is recorded as " + recorded
	}
	at, valid := math.NaN(), false
	if ts, isString := w["ts"].(string); isString {
		at, valid = g.date(str(ts))
	}
	if !valid {
		return "the waiver has no time"
	}
	scope := "waive:" + recorded
	if problem := g.grantProblem(prop(w, "grant_id"), scope, at, "the waiver"); problem != "" {
		return problem
	}
	return g.decisionProblem(w, scope, at)
}

func (g *gate) grantProblem(grantID jv, scope string, at float64, what string) string {
	id, isString := grantID.v.(string)
	var grant Row
	for _, r := range g.runRows {
		if r["event"] == "autopilot.grant" && isString && r["grant_id"] == id && g.ok(r) {
			grant = r
			break
		}
	}
	if grant == nil {
		return "no signed autopilot.grant row for grant " + jsString(grantID) + " in this run"
	}
	from, a := g.date(prop(grant, "ts"))
	until, b := g.date(prop(grant, "until"))
	if !(a && b && at >= from && at < until) {
		return what + " is outside grant " + id + "'s window"
	}
	scopes, isList := grant["scopes"].([]any)
	deny, denies := grant["deny"].([]any)
	// An empty scope asks only for the grant's window (B390 rule_break decisions).
	if scope != "" && (!isList || !slices.Contains(scopes, any(scope)) || denies && slices.Contains(deny, any(scope))) {
		return "grant " + id + " does not allow " + scope
	}
	for _, r := range g.in.Rows {
		if (r["event"] != "autopilot.stop" && r["event"] != "autopilot.expire") || r["grant_id"] != id {
			continue
		}
		ts, valid := math.NaN(), false
		if text, isText := r["ts"].(string); isText {
			ts, valid = g.date(str(text))
		}
		if !valid || ts <= at {
			word := "expired"
			if r["event"] == "autopilot.stop" {
				word = "stopped"
			}
			return "grant " + id + " was " + word + " before " + what
		}
	}
	return ""
}

func (g *gate) decisionProblem(w Row, scope string, at float64) string {
	id, isString := w["decision_id"].(string)
	if !isString || id == "" {
		return "no delegate decision named"
	}
	var row Row
	for _, r := range g.runRows {
		if r["event"] == "autopilot.decision" && r["decision_id"] == id {
			row = r
			break
		}
	}
	if row == nil {
		return "no autopilot.decision row " + id + " in this run"
	}
	if !g.ok(row) {
		return "decision " + id + " has no valid MAC"
	}
	if row["acted"] != true {
		return "decision " + id + " did not act (it went to the owner)"
	}
	if !strictEq(prop(row, "grant_id"), prop(w, "grant_id")) {
		return "decision " + id + " is under another grant"
	}
	if row["scope"] != scope {
		return "decision " + id + " is for " + jsString(prop(row, "scope")) + ", not " + scope
	}
	if row["decision"] != "waive" {
		said := "undefined"
		if v, has := row["decision"]; has {
			switch v.(type) {
			case map[string]any, []any:
				// Go maps lose JSON.stringify's key order: print the value
				// as JSON.parse read it (B498), or mark the result unknown.
				if ordered, has := g.in.Ordered[OrderedKey(row)]; has && hasOrderedField(ordered, "decision") {
					v = ordered.Get("decision")
				} else if containsMap(v) {
					g.unknown = true
				}
			}
			data, _ := ledger.Stringify(v, false)
			said = string(data)
		}
		return "decision " + id + " decided " + said + `, not "waive"`
	}
	named, _ := row["subject"].(map[string]any)
	for _, k := range []string{"block", "file", "finding"} {
		if want := prop(w, k); want.ok && !strictEq(prop(named, k), want) {
			return "decision " + id + " is about another " + k
		}
	}
	ts, valid := math.NaN(), false
	if text, isText := row["ts"].(string); isText {
		ts, valid = g.date(str(text))
	}
	if !valid || ts > at || at-ts > 30*60*1000 {
		return "decision " + id + " is older than 30 min (or not yet made)"
	}
	return ""
}

func (g *gate) refusalSeverity(file, hash, id string) string {
	worst := ""
	for _, r := range g.mine {
		if r["file"] != file || r["content_hash"] != hash || !g.ok(r) {
			continue
		}
		d := denialOf(r, g.in.ResultFiles)
		if d == nil {
			continue
		}
		for _, o := range d.open {
			if o.id != id || o.severity == nil {
				continue
			}
			sev := o.severity.(string)
			if worst == "" || openRank[sev] > openRank[worst] {
				worst = sev
			}
		}
	}
	if worst == "" {
		return "unknown"
	}
	return worst
}

type namedOpen struct {
	ids []string
	sev map[string]any
}

func (n *namedOpen) has(id string) bool { _, ok := n.sev[id]; return ok }

func (n *namedOpen) set(id string, severity any) {
	if _, has := n.sev[id]; !has {
		n.ids = append(n.ids, id)
	}
	n.sev[id] = severity
}

func (g *gate) capIDsAt(file, hash string) []string {
	ids := []string{}
	for i, cap := range g.mine {
		list, isList := cap["open"].([]any)
		if cap["event"] != "review.cap" || cap["file"] != file || cap["content_hash"] != hash || !isList || !g.ok(cap) || g.superseded(i) {
			continue
		}
		for _, v := range list {
			if id, ok := v.(string); ok && id != "" && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func (g *gate) reviewedOpen(file, hash string) *namedOpen {
	if !slices.ContainsFunc(g.mine, func(r Row) bool {
		return r["event"] == "review.round" && r["file"] == file && r["content_hash"] == hash && g.ok(r)
	}) {
		return nil
	}
	l := g.latestOf(file, hash)
	if l.latest == nil || l.latestRow == nil || l.latestRow["content_hash"] != hash || !g.ok(l.latestRow) || len(l.latest.open) == 0 {
		return nil
	}
	named := &namedOpen{sev: map[string]any{}}
	for _, f := range l.latest.open {
		named.set(f.id, f.severity)
	}
	for _, id := range g.capIDsAt(file, hash) {
		named.set(id, g.refusalSeverity(file, hash, id))
	}
	for i, cap := range g.mine {
		if cap["event"] != "review.cap" || cap["file"] != file || g.superseded(i) {
			continue
		}
		list, isList := cap["open"].([]any)
		if !isList {
			return nil
		}
		for _, v := range list {
			if id, ok := v.(string); !ok || id == "" {
				return nil
			}
		}
		for _, v := range list {
			if id := v.(string); !named.has(id) {
				named.set(id, g.refusalSeverity(file, hash, id))
			}
		}
	}
	return named
}

func (g *gate) waivedAll(file, hash string) *Waived {
	named := g.reviewedOpen(file, hash)
	if named == nil {
		return nil
	}
	w := Waived{File: file, Hash: hash, Findings: []string{}, Answered: []string{}}
	byHuman, byAgent := true, false
	for _, id := range named.ids {
		severity := named.sev[id]
		by, _ := g.waiverOf(file, id, severity, severity)
		switch by {
		case "":
			return nil
		case "answer":
			w.Answered = append(w.Answered, id)
		default:
			w.Findings = append(w.Findings, id)
			byHuman = byHuman && by == "human"
			byAgent = byAgent || by == "agent"
		}
	}
	switch {
	case len(w.Findings) == 0:
		w.By = nil
	case byHuman:
		w.By = "human"
	case byAgent:
		w.By = "agent"
	default:
		w.By = "autopilot"
	}
	gitx.SortStrings(w.Findings)
	gitx.SortStrings(w.Answered)
	return &w
}

// newGate is reviewRules' setup (B609: shared with the worker's answers check).
func newGate(in Input) *gate {
	g := &gate{in: in, v: &verifier{in.Key, map[uintptr]bool{}}, current: map[string]string{}}
	amendedAt := -1
	for i, r := range in.Rows {
		if r["event"] == "block.acceptance_amended" && r["block"] == in.Block && r["run"] == in.Run && g.ok(r) {
			amendedAt = i
		}
	}
	g.amended = amendedAt >= 0
	for i, r := range in.Rows {
		run := prop(r, "run")
		if r["block"] == in.Block && (!run.ok || strictEq(run, str(in.Run))) && !(i < amendedAt && strings.HasPrefix(Text(r["event"]), "review.")) {
			g.mine = append(g.mine, r)
		}
		if r["run"] == in.Run {
			g.runRows = append(g.runRows, r)
		}
	}
	for _, f := range append(slices.Clone(in.ProofFiles), in.Files...) {
		g.current[f.File] = f.Hash
	}
	for _, r := range g.mine {
		if r["event"] != "review.waived" || r["by"] != "human" || !g.ok(r) {
			continue
		}
		g.human = append(g.human, r)
	}
	g.agent = countedAgentWaivers(g.mine, in.Block, g.ok)
	for _, r := range g.mine {
		if r["event"] == "review.waived" && r["by"] == "autopilot" {
			g.autopilot = append(g.autopilot, r)
		}
		if r["event"] == "review.answered" && g.ok(r) && r["run"] == in.Run {
			g.answers = append(g.answers, r)
		}
	}
	return g
}

// Evaluate is gate-check.mjs checkBlockReviews. Result.Unknown is true when a
// date or value needed an engine detail Go cannot reproduce; callers hand over.
func Evaluate(in Input) Result {
	if in.ProofFiles == nil {
		in.ProofFiles = in.Files
	}
	g := newGate(in)
	hashOf := map[string]string{}
	for _, f := range in.Files {
		hashOf[f.File] = f.Hash
	}
	result := Result{Blockers: []Blocker{}, Waived: []Waived{}, Accepted: []AcceptedHit{}}
	add := func(code, file, id, detail string) {
		result.Blockers = append(result.Blockers, Blocker{Code: code, File: file, Finding: id, Detail: detail})
	}
	for _, file := range in.Unowned {
		add("unowned_change", file, "", file+" changed but no block of the run owns it — block claim it, or put it back")
	}
	if in.NoChanges {
		add("no_changes", "", "", "block "+in.Block+" changed no owned file and has no reviews")
	}
	// addValue adds a late or cap blocker whose file and finding are the
	// row's own values: Blocker.File is String(file) when truthy, Finding the
	// finding when it is text, and Raw keeps both when either is not a
	// non-empty string (Node prints and keys them as they are).
	addValue := func(code string, file, id jv, detail string) {
		b := Blocker{Code: code, Detail: detail}
		if Truthy(file.v, file.ok) {
			b.File = jsString(file)
		}
		if s, isText := id.v.(string); isText && id.ok {
			b.Finding = s
		}
		if !nonEmptyText(file) || !nonEmptyText(id) {
			b.Raw = &Raw{File: file.v, HasFile: file.ok, Finding: id.v, HasFinding: id.ok}
		}
		result.Blockers = append(result.Blockers, b)
	}
	orInvalidValue := func(code string, file, id jv, detail, invalid string) {
		if invalid == "" {
			addValue(code, file, id, detail)
		} else {
			addValue("autopilot_waiver_invalid", file, id, "the autopilot waiver of "+jsString(id)+" in "+jsString(file)+" does not count: "+invalid)
		}
	}
	orInvalid := func(code, file, id, detail, invalid string) {
		if invalid == "" {
			add(code, file, id, detail)
		} else {
			add("autopilot_waiver_invalid", file, id, "the autopilot waiver of "+id+" in "+file+" does not count: "+invalid)
		}
	}
	capped := func(file string) bool {
		return slices.ContainsFunc(g.mine, func(r Row) bool { return r["event"] == "review.cap" && r["file"] == file })
	}
	type staleFile struct {
		file string
		d    *denial
	}
	stale := []staleFile{}
	for _, f := range in.Files {
		approvals, signed := 0, false
		for _, r := range g.mine {
			if r["event"] == "review.approved" && r["file"] == f.File && r["content_hash"] == f.Hash {
				approvals++
				signed = signed || g.ok(r)
			}
		}
		if approvals == 0 {
			if all := g.waivedAll(f.File, f.Hash); all != nil {
				result.Waived = append(result.Waived, *all)
			} else {
				detail := "no review.approved row for " + f.File + " at its current content"
				if g.amended {
					detail += " (acceptance amended)"
				}
				if capped(f.File) {
					detail += "; " + stopExitsLine
				}
				add("unreviewed", f.File, "", detail)
			}
		} else if !signed {
			add("unsigned", f.File, "", "the review.approved row for "+f.File+" has no valid MAC")
		}
		l := g.latestOf(f.File, f.Hash)
		if signed {
			if l.denial != nil {
				stale = append(stale, staleFile{f.File, l.denial})
			}
		} else if l.latest != nil && len(l.latest.open) > 0 && !capped(f.File) {
			stale = append(stale, staleFile{f.File, l.latest})
		}
	}
	for _, r := range g.mine {
		if r["event"] == "rule_break" {
			id := r["id"]
			if id == nil {
				id = r["rule"]
			}
			if id == nil {
				id = "unnamed"
			}
			// String(r.id ?? r.rule ?? 'unnamed'): a number, list or object
			// prints as Node prints it (B451).
			add("rule_break", "", "", "rule_break row: "+jsString(jv{id, true}))
		}
	}
	remaining, accepted := acceptedHits(in, g.ok, func(r Row) bool {
		at, valid := math.NaN(), false
		if ts, isString := r["ts"].(string); isString {
			at, valid = g.date(str(ts))
		}
		return valid && g.ok(r) && g.grantProblem(prop(r, "grant_id"), "", at, "the acceptance") == ""
	})
	result.Blockers = append(result.Blockers, remaining...)
	result.Accepted = accepted
	notApproved := map[string]bool{}
	for _, s := range stale {
		if len(s.d.open) == 0 {
			add("not_approved", s.file, "", fmt.Sprintf("a later review of %s's current content did not approve it (%s) and names no finding; review it again", s.file, s.d.event))
			continue
		}
		for _, f := range s.d.open {
			notApproved[s.file+"\x00"+f.id] = true
			if by, invalid := g.waiverOf(s.file, f.id, f.severity, f.severity); by == "" {
				orInvalid("not_approved", s.file, f.id, fmt.Sprintf("the latest review of %s's current content left %s open and it is not waived", s.file, f.id), invalid)
			}
		}
	}
	for _, r := range g.mine {
		if r["event"] != "review.late_finding" {
			continue
		}
		// Node reads late.file and late.finding as they are: undefined,
		// null, a number or "" print as String(value).
		fileV, idV := prop(r, "file"), prop(r, "finding")
		if r["severity"] == "warning" || r["severity"] == "nit" {
			if slices.ContainsFunc(g.mine, func(ruling Row) bool {
				return ruling["event"] == "review.late_ruling" && g.matches(ruling, fileV, idV) && ruling["ruling"] == "nit" && g.ok(ruling)
			}) {
				continue
			}
		}
		severity := any("unknown")
		if s, ok := r["severity"].(string); ok {
			severity = s
		}
		if waived, invalid := g.waiverForValue(fileV, idV, severity); !waived {
			orInvalidValue("late_unruled", fileV, idV, "late finding "+jsString(idV)+" in "+jsString(fileV)+" has no ruling", invalid)
		}
	}
	for i, cap := range g.mine {
		if cap["event"] != "review.cap" || g.superseded(i) {
			continue
		}
		fileV := prop(cap, "file")
		file, isText := fileV.v.(string)
		hash, has := hashOf[file]
		has = has && isText && fileV.ok
		if has {
			l := g.latestOf(file, hash)
			if l.lastApproval > i && l.denial == nil {
				continue
			}
		}
		for _, v := range array(cap["open"]) {
			idV := jv{v, true}
			if notApproved[jsString(fileV)+"\x00"+jsString(idV)] {
				continue
			}
			severity := "unknown"
			if id, isID := v.(string); has && isID {
				severity = g.refusalSeverity(file, hash, id)
			}
			if waived, invalid := g.waiverForValue(fileV, idV, severity); !waived {
				orInvalidValue("review_cap", fileV, idV, jsString(fileV)+" stopped at review_cap; finding "+jsString(idV)+" is open and not waived; "+stopExitsLine, invalid)
			}
		}
	}
	for _, f := range in.ProofFiles {
		if f.Hash == gitx.DeletedHash {
			continue
		}
		high := owned.OwnsFile(in.HighPaths, nil, f.File)
		for _, r := range g.mine {
			if r["file"] == f.File {
				risk, _ := r["risk"].(float64)
				high = high || !g.ok(r) || risk >= 2 || r["security_sensitive"] == true
			}
		}
		if !high {
			continue
		}
		covered := false
		for _, r := range g.mine {
			if r["event"] == "proof" && r["run"] == in.Run && r["step"] == "red-green" && r["proven"] == true && r["red_kind"] == "assertion" && g.ok(r) && slices.Contains(array(r["covers"]), any(f.File)) {
				covered = true
			}
		}
		if !covered {
			if waived, invalid := g.waiverFor(f.File, "proof", "unknown"); !waived {
				orInvalid("unproven", f.File, "", f.File+" is high tier and has no proven red→green row covering it", invalid)
			}
		}
	}
	if len(result.Blockers) > 0 {
		result.Waived = []Waived{}
	}
	result.Unknown = g.unknown
	return result
}

var ticketID = regexp.MustCompile(`^[0-9a-f]{24}$`)

// LoadResultFiles is gate-check.mjs resultFilesFor: one entry per review.result
// row of the block that has no approved field and is a verdict, keyed by
// ticket. A missing, unparsable, mismatched or unsigned file is nil.
func LoadResultFiles(mainTop, run, block string, rows []Row, key []byte) map[string]*ResultFile {
	out := map[string]*ResultFile{}
	for _, r := range rows {
		ticket, isString := r["ticket"].(string)
		if _, has := r["approved"]; r["event"] != "review.result" || r["block"] != block || has || NeverReviewed(r) || !isString {
			continue
		}
		if _, seen := out[ticket]; seen {
			continue
		}
		out[ticket] = nil
		if !ticketID.MatchString(ticket) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(mainTop, ".code-forge", "reviews", run, ticket+".json"))
		if err != nil {
			continue
		}
		var result map[string]any
		if json.Unmarshal(data, &result) != nil || result == nil || result["ticket"] != ticket || result["run"] != run || !Verified(result, key) {
			continue
		}
		out[ticket] = &ResultFile{prop(result, "status"), prop(result, "approved"), prop(result, "findings")}
	}
	return out
}

// JSString is String(v) for a JSON value; present false is undefined.
func JSString(v any, present bool) string { return jsString(jv{v, present}) }

func hasOrderedField(o ledger.Object, key string) bool {
	for _, f := range o {
		if f.Key == key {
			return true
		}
	}
	return false
}

// containsMap is true when v holds an object, whose key order a Go map lost.
func containsMap(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		return true
	case []any:
		for _, item := range x {
			if containsMap(item) {
				return true
			}
		}
	}
	return false
}

// OpenFindingIDs are the ids gate-check.mjs openFindings reads from a
// review.result open list (an id that is not a finding id reads as X<n>).
func OpenFindingIDs(v any) []string {
	out := []string{}
	for _, f := range openFindings(v) {
		out = append(out, f.id)
	}
	return out
}
