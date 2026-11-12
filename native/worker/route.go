package worker

// B492: src/review/route.mjs (countLines, routeRules, pickRoutes, routeTicket,
// adaptiveTicket) and src/review/model-stats.mjs (modelStats, pickReviewer)
// for the worker's per-ticket route rows. Only the rows are ported: the routed
// config feeds the review sessions, which stay in Node. Every input Node would
// treat in a way Go cannot name for sure (a plain Error, a Date.parse it may
// accept, a JS coercion) is errRouteUnsure, and the ticket stays with Node.

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// errRouteInvalid is RouteError: the ticket is `unavailable`, `route-invalid`.
var errRouteInvalid = errors.New("route-invalid")

// errRouteUnsure: Node throws a plain error (worker-error) or does something
// Go does not model; the ticket goes to Node with nothing written.
var errRouteUnsure = errors.New("routing input Go does not model")

const defaultSessionTimeoutS = 300

// countLines is route.mjs countLines: newline bytes, plus one for a last line
// with no newline; nil (a deleted file) and empty bytes have 0 lines.
func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	n := 0
	for _, b := range data {
		if b == '\n' {
			n++
		}
	}
	if data[len(data)-1] == '\n' {
		return n
	}
	return n + 1
}

func obj(v any) (map[string]any, bool) { m, ok := v.(map[string]any); return m, ok }

func has(m map[string]any, k string) bool { _, ok := m[k]; return ok }

func isText(v any) bool { s, ok := v.(string); return ok && s != "" }

func isInteger(v any) bool {
	f, ok := v.(float64)
	return ok && !math.IsInf(f, 0) && f == math.Trunc(f)
}

func isPositiveInt(v any) bool { return isInteger(v) && v.(float64) >= 1 }

// optString: absent, or a string (null is neither).
func optString(m map[string]any, k string) bool {
	v, ok := m[k]
	if !ok {
		return true
	}
	_, isStr := v.(string)
	return isStr
}

func isFallbackItem(v any) bool {
	m, ok := obj(v)
	if !ok || !isText(m["model"]) || !optString(m, "effort") {
		return false
	}
	p, ok := m["provider"]
	return !ok || isText(p)
}

func isEntry(v any) bool {
	m, ok := obj(v)
	if !ok || !isText(m["model"]) || !optString(m, "provider") || !optString(m, "effort") {
		return false
	}
	fb, ok := m["fallback"]
	if !ok {
		return true
	}
	list, ok := fb.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if !isFallbackItem(item) {
			return false
		}
	}
	return true
}

func usableRule(v any) bool {
	if !isEntry(v) {
		return false
	}
	r := v.(map[string]any)
	if !isInteger(r["min_lines"]) || r["min_lines"].(float64) < 0 {
		return false
	}
	if l, ok := r["level"]; ok && l != "L2" && l != "L3" {
		return false
	}
	if t, ok := r["session_timeout_s"]; ok && !isPositiveInt(t) {
		return false
	}
	if s, ok := r["second"]; ok && !isEntry(s) {
		return false
	}
	if p, ok := r["pin"]; ok {
		if _, isBool := p.(bool); !isBool {
			return false
		}
	}
	return true
}

func ruleLevel(r map[string]any) string {
	if l, ok := r["level"].(string); ok {
		return l
	}
	return "L2"
}

func reviewOf(cfg map[string]any) map[string]any {
	r, _ := obj(cfg["review"])
	return r
}

// routeRules: [] for an absent or null key; errRouteInvalid otherwise when
// the list is not one of usable rules or names a (level, min_lines) twice.
func routeRules(cfg map[string]any) ([]map[string]any, error) {
	routes, ok := reviewOf(cfg)["routes"]
	if !ok || routes == nil {
		return nil, nil
	}
	list, ok := routes.([]any)
	if !ok {
		return nil, errRouteInvalid
	}
	out := []map[string]any{}
	seen := map[string]bool{}
	for _, v := range list {
		if !usableRule(v) {
			return nil, errRouteInvalid
		}
		r := v.(map[string]any)
		k := ruleLevel(r) + "\x00" + strconv.FormatFloat(r["min_lines"].(float64), 'g', -1, 64)
		if seen[k] {
			return nil, errRouteInvalid
		}
		seen[k] = true
		out = append(out, r)
	}
	return out, nil
}

type pickedRoute struct {
	level string
	rule  map[string]any
}

func pickRoutes(cfg map[string]any, lines int, deleted bool) ([]pickedRoute, error) {
	rules, err := routeRules(cfg)
	if err != nil {
		return nil, err
	}
	if lines < 0 {
		return nil, errRouteInvalid
	}
	if deleted || len(rules) == 0 {
		return nil, nil
	}
	out := []pickedRoute{}
	for _, level := range []string{"L2", "L3"} {
		var best map[string]any
		for _, r := range rules {
			if ruleLevel(r) != level || r["min_lines"].(float64) > float64(lines) {
				continue
			}
			if best == nil || r["min_lines"].(float64) > best["min_lines"].(float64) {
				best = r
			}
		}
		if best != nil {
			out = append(out, pickedRoute{level, best})
		}
	}
	return out, nil
}

// providerOf is ruleProvider + RouteError: the rule's own provider replaces
// the level's, else the top-level provider; neither (or an empty one) is
// route-invalid.
func providerOf(cfg map[string]any, rule map[string]any) (string, error) {
	p, ok := rule["provider"]
	if !ok || p == nil {
		p = cfg["provider"]
	}
	if s, ok := p.(string); ok && s != "" {
		return s, nil
	}
	return "", errRouteInvalid
}

type routed struct {
	timeoutS float64
	rows     []ledger.Object
}

// explicitRoutes is routeTicket without performance: the rows of the routed
// levels and the routed session timeout.
func explicitRoutes(cfg map[string]any, lines int, deleted bool, timeoutS float64) (routed, error) {
	picked, err := pickRoutes(cfg, lines, deleted)
	if err != nil {
		return routed{}, err
	}
	if len(picked) == 0 {
		return routed{timeoutS: timeoutS}, nil
	}
	timeout := math.Inf(-1)
	for _, p := range picked {
		if t, ok := p.rule["session_timeout_s"].(float64); ok && t > timeout {
			timeout = t
		}
	}
	if math.IsInf(timeout, -1) {
		timeout = timeoutS
	}
	multimodel := reviewOf(cfg)["multimodel"] == true
	rows := []ledger.Object{}
	for _, p := range picked {
		provider, err := providerOf(cfg, p.rule)
		if err != nil {
			return routed{}, err
		}
		var second any
		if s, ok := p.rule["second"]; ok && !(p.level == "L2" && multimodel) {
			second = s.(map[string]any)["model"]
		}
		rows = append(rows, ledger.Object{
			{Key: "event", Value: "review.route"}, {Key: "level", Value: p.level},
			{Key: "min_lines", Value: p.rule["min_lines"]}, {Key: "lines", Value: float64(lines)},
			{Key: "provider", Value: provider}, {Key: "model", Value: p.rule["model"]},
			{Key: "session_timeout_s", Value: timeout}, {Key: "second_model", Value: second},
		})
	}
	return routed{timeoutS: timeout, rows: rows}, nil
}

type candidate struct{ model, provider string }

// adaptiveOpts are the `review.adaptive` keys model-stats reads, with Node's
// destructuring defaults; any other type is errRouteUnsure.
type adaptiveOpts struct {
	windowDays, minSessions, explore float64
	enabled                          bool
}

func adaptiveOf(cfg map[string]any) (adaptiveOpts, error) {
	o := adaptiveOpts{windowDays: 14, minSessions: 10, explore: 0.1, enabled: true}
	raw, ok := reviewOf(cfg)["adaptive"]
	if !ok || raw == nil {
		return o, nil
	}
	a, ok := obj(raw)
	if !ok {
		return o, errRouteUnsure
	}
	num := func(k string, dst *float64) bool {
		v, ok := a[k]
		if !ok {
			return true
		}
		f, isNum := v.(float64)
		*dst = f
		return isNum
	}
	if !num("window_days", &o.windowDays) || !num("min_sessions", &o.minSessions) || !num("explore", &o.explore) {
		return o, errRouteUnsure
	}
	// a min_sessions ≤ 0 makes a model with no stats "measured": Node then throws
	if o.minSessions <= 0 {
		return o, errRouteUnsure
	}
	if v, ok := a["enabled"]; ok {
		b, isBool := v.(bool)
		if !isBool {
			return o, errRouteUnsure
		}
		o.enabled = b
	}
	return o, nil
}

// adaptiveRoutes is adaptiveTicket's rows (the L2 row first, then the
// explicit non-L2 rows).
func adaptiveRoutes(cfg map[string]any, lines int, deleted bool, timeoutS float64, rows []any, ticketID, runID string, now time.Time) (routed, error) {
	picked, err := pickRoutes(cfg, lines, deleted)
	if err != nil {
		return routed{}, err
	}
	legacy, err := explicitRoutes(cfg, lines, deleted, timeoutS)
	if err != nil {
		return routed{}, err
	}
	var rule map[string]any
	for _, p := range picked {
		if p.level == "L2" {
			rule = p.rule
		}
	}
	// resolveLevel(cfg, 'L2'): a plain Error (worker-error in Node) for no
	// model or no provider
	levels, _ := obj(cfg["levels"])
	l2, ok := obj(levels["L2"])
	if !ok || !isText(l2["model"]) {
		return routed{}, errRouteUnsure
	}
	primaryProvider := l2["provider"]
	if primaryProvider == nil {
		primaryProvider = cfg["provider"]
	}
	if !isText(primaryProvider) {
		return routed{}, errRouteUnsure
	}
	entries := []candidate{}
	if rule != nil {
		p, err := providerOf(cfg, rule)
		if err != nil {
			return routed{}, err
		}
		entries = append(entries, candidate{rule["model"].(string), p})
	}
	entries = append(entries, candidate{l2["model"].(string), primaryProvider.(string)})
	if fb, ok := l2["fallback"]; ok && fb != nil {
		list, ok := fb.([]any)
		if !ok {
			return routed{}, errRouteUnsure
		}
		for _, item := range list {
			m, ok := obj(item)
			// an item with no provider string would route to `provider: undefined`
			if !ok || !isText(m["model"]) || !isText(m["provider"]) {
				return routed{}, errRouteUnsure
			}
			entries = append(entries, candidate{m["model"].(string), m["provider"].(string)})
		}
	}
	if rule != nil {
		if list, ok := rule["fallback"].([]any); ok {
			for _, item := range list {
				m := item.(map[string]any)
				p, _ := m["provider"].(string)
				if !has(m, "provider") {
					if p, err = providerOf(cfg, rule); err != nil {
						return routed{}, err
					}
				}
				entries = append(entries, candidate{m["model"].(string), p})
			}
		}
	}
	review := reviewOf(cfg)
	var secondProvider any
	if review["multimodel"] == true {
		sl, _ := obj(review["second_levels"])
		l2s, _ := obj(sl["L2"])
		secondProvider = l2s["provider"]
		if secondProvider == nil {
			secondProvider = review["second_provider"]
		}
	}
	candidates := []candidate{}
	for _, c := range entries {
		if sp, ok := secondProvider.(string); ok && sp == c.provider {
			continue
		}
		dup := false
		for _, o := range candidates {
			if o == c {
				dup = true
			}
		}
		if !dup {
			candidates = append(candidates, c)
		}
	}
	pinned := rule != nil && rule["pin"] == true
	if len(candidates) == 0 || (pinned && candidates[0].model != rule["model"]) {
		return routed{}, errRouteInvalid
	}
	for _, c := range candidates {
		if protoKeys[c.model] {
			return routed{}, errRouteUnsure
		}
	}
	opts, err := adaptiveOf(cfg)
	if err != nil {
		return routed{}, err
	}
	for _, r := range rows {
		if r == nil {
			return routed{}, errRouteUnsure // rows.find((r) => r.event …) throws
		}
	}
	var chosen candidate
	var reason any
	var previous map[string]any
	for _, r := range rows {
		m, _ := obj(r)
		if m != nil && m["event"] == "review.route" && m["level"] == "L2" && m["ticket"] == ticketID && m["run"] == runID {
			previous = m
			break
		}
	}
	prior := -1
	if previous != nil {
		for i, c := range candidates {
			if previous["model"] == c.model && previous["provider"] == c.provider {
				prior = i
				break
			}
		}
	}
	switch {
	case prior >= 0:
		chosen = candidates[prior]
		reason = previous["reason"]
		if reason == nil {
			reason = "configured-order"
		}
	case pinned:
		chosen, reason = candidates[0], "pinned"
	default:
		stats, err := modelStats(rows, opts.windowDays, now)
		if err != nil {
			return routed{}, err
		}
		chosen, reason = pickReviewer(candidates, stats, opts, ticketID)
	}
	var second any
	for _, r := range legacy.rows {
		if r.Get("level") == "L2" {
			second = r.Get("second_model")
		}
	}
	var minLines any
	if rule != nil {
		minLines = rule["min_lines"]
	}
	out := []ledger.Object{{
		{Key: "event", Value: "review.route"}, {Key: "level", Value: "L2"}, {Key: "min_lines", Value: minLines},
		{Key: "lines", Value: float64(lines)}, {Key: "provider", Value: chosen.provider}, {Key: "model", Value: chosen.model},
		{Key: "reason", Value: reason}, {Key: "session_timeout_s", Value: legacy.timeoutS}, {Key: "second_model", Value: second},
	}}
	for _, r := range legacy.rows {
		if r.Get("level") != "L2" {
			out = append(out, r)
		}
	}
	return routed{timeoutS: legacy.timeoutS, rows: out}, nil
}

// protoKeys: names a plain JS object inherits; `stats[model]` would not be undefined.
var protoKeys = map[string]bool{"__proto__": true, "constructor": true, "toString": true, "toLocaleString": true, "valueOf": true, "hasOwnProperty": true, "isPrototypeOf": true, "propertyIsEnumerable": true, "__defineGetter__": true, "__defineSetter__": true, "__lookupGetter__": true, "__lookupSetter__": true}

var isoStamp = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,3}))?Z$`)

// dateParse is Date.parse(v) in ms: (ms, true, nil) for a valid ISO UTC
// stamp, (0, false, nil) where Node gets NaN for sure, errRouteUnsure otherwise.
func dateParse(v any) (float64, bool, error) {
	switch s := v.(type) {
	case nil, bool, map[string]any:
		return 0, false, nil
	case string:
		if s == "" {
			return 0, false, nil
		}
		m := isoStamp.FindStringSubmatch(s)
		if m == nil {
			return 0, false, errRouteUnsure
		}
		n := make([]int, 6)
		for i := range n {
			n[i], _ = strconv.Atoi(m[i+1])
		}
		ms := 0
		if m[7] != "" {
			frac := m[7]
			for len(frac) < 3 {
				frac += "0"
			}
			ms, _ = strconv.Atoi(frac)
		}
		t := time.Date(n[0], time.Month(n[1]), n[2], n[3], n[4], n[5], 0, time.UTC)
		if n[1] < 1 || n[1] > 12 || t.Day() != n[2] || n[3] > 23 || n[4] > 59 || n[5] > 59 {
			return 0, false, errRouteUnsure
		}
		return float64(t.UnixMilli() + int64(ms)), true, nil
	}
	return 0, false, errRouteUnsure
}

type modelStat struct {
	sessions                                 int
	median                                   *float64
	availability, findingsPerSession, waived float64
}

type attemptRow struct {
	fields map[string]any
	status any
}

// jsonKey is JSON.stringify of an array of values (undefined → null).
func jsonKey(values ...any) string {
	b, err := ledger.Stringify(values, false)
	if err != nil {
		return "\x00unencodable"
	}
	return string(b)
}

func merged(r, s map[string]any, k string) any {
	if v, ok := s[k]; ok {
		return v
	}
	return r[k]
}

// modelStats is model-stats.mjs modelStats over the rows in the window.
func modelStats(rows []any, windowDays float64, nowT time.Time) (map[string]*modelStat, error) {
	now := float64(nowT.UnixMilli())
	since := now - windowDays*86400000
	type recentRow struct {
		m  map[string]any
		at float64
	}
	recent := []recentRow{}
	for _, r := range rows {
		m, isObj := obj(r)
		var ts any
		if isObj {
			ts = m["ts"]
		} else if _, isArr := r.([]any); isArr {
			ts = nil // [].ts is undefined
		}
		at, ok, err := dateParse(ts)
		if err != nil {
			return nil, err
		}
		if ok && at >= since && at <= now {
			if !isObj {
				return nil, errRouteUnsure
			}
			recent = append(recent, recentRow{m, at})
		}
	}
	type data struct {
		attempts []*attemptRow
		findings map[string]any
		order    []string
	}
	models := map[string]*data{}
	order := []string{}
	of := func(model string) *data {
		if models[model] == nil {
			models[model] = &data{findings: map[string]any{}}
			order = append(order, model)
		}
		return models[model]
	}
	group := func(get func(string) any) string {
		return jsonKey(get("run"), get("block"), get("model"), get("lens"))
	}
	rawGroups := map[string]bool{}
	for _, r := range recent {
		model, ok := r.m["model"].(string)
		if r.m["event"] != "session" || r.m["role"] != "reviewer" || !ok {
			continue
		}
		if protoKeys[model] {
			return nil, errRouteUnsure
		}
		of(model).attempts = append(of(model).attempts, &attemptRow{fields: r.m, status: r.m["status"]})
		rawGroups[group(func(k string) any { return r.m[k] })] = true
	}
	diagnosed := map[*attemptRow]bool{}
	for _, r := range recent {
		if r.m["event"] != "review.schema_invalid" || r.m["role"] != "reviewer" {
			continue
		}
		model, ok := r.m["model"].(string)
		if !ok || models[model] == nil {
			continue
		}
		g := group(func(k string) any { return r.m[k] })
		attempts := models[model].attempts
		for i := len(attempts) - 1; i >= 0; i-- {
			s := attempts[i]
			sat, _, _ := dateParse(s.fields["ts"])
			if !diagnosed[s] && group(func(k string) any { return s.fields[k] }) == g && sat <= r.at {
				s.status = "invalid-output"
				diagnosed[s] = true
				break
			}
		}
	}
	for _, r := range recent {
		sessions, ok := r.m["sessions"].([]any)
		if r.m["event"] != "review.result" || !ok {
			continue
		}
		for _, sv := range sessions {
			s, ok := obj(sv)
			if !ok {
				if sv == nil {
					continue // s?.role
				}
				continue
			}
			model, ok := s["model"].(string)
			if s["role"] != "reviewer" || !ok {
				continue
			}
			if protoKeys[model] {
				return nil, errRouteUnsure
			}
			d := of(model)
			if !rawGroups[group(func(k string) any { return merged(r.m, s, k) })] {
				d.attempts = append(d.attempts, &attemptRow{fields: s, status: s["status"]})
			}
			var findings []any
			switch f := s["findings"].(type) {
			case nil:
			case []any:
				findings = f
			default:
				return nil, errRouteUnsure
			}
			for _, fv := range findings {
				f, ok := obj(fv)
				if !ok {
					continue
				}
				id, ok := f["id"].(string)
				if !ok {
					continue
				}
				if sec, ok := s["section"]; ok {
					switch v := sec.(type) {
					case string:
						id = "S" + v + "." + id
					case float64:
						if !isInteger(v) || math.Abs(v) >= 1e21 {
							return nil, errRouteUnsure
						}
						id = "S" + strconv.FormatFloat(v, 'f', -1, 64) + "." + id
					case nil:
						id = "Snull." + id
					default:
						return nil, errRouteUnsure
					}
				}
				k := jsonKey(r.m["run"], r.m["block"], r.m["file"], id)
				if _, seen := d.findings[k]; !seen {
					d.order = append(d.order, k)
				}
				d.findings[k] = f
			}
		}
	}
	waived := map[string]bool{}
	for _, r := range recent {
		switch r.m["event"] {
		case "review.waived":
			waived[jsonKey(r.m["run"], r.m["block"], r.m["file"], r.m["finding"])] = true
		case "review.unwaived":
			delete(waived, jsonKey(r.m["run"], r.m["block"], r.m["file"], r.m["finding"]))
		}
	}
	out := map[string]*modelStat{}
	for _, model := range order {
		d := models[model]
		seconds := []float64{}
		ok := 0
		for _, a := range d.attempts {
			if v, isNum := a.fields["duration_ms"].(float64); isNum && v >= 0 {
				seconds = append(seconds, v/1000)
			}
			if a.status == "ok" {
				ok++
			}
		}
		sort.Float64s(seconds)
		n := len(d.attempts)
		st := &modelStat{sessions: n}
		if len(seconds) > 0 {
			mid := len(seconds) / 2
			m := (seconds[mid] + seconds[(len(seconds)+1)/2-1]) / 2
			st.median = &m
		}
		if n > 0 {
			st.availability = float64(ok) / float64(n)
		}
		if len(d.order) > 0 {
			w := 0
			for _, k := range d.order {
				if waived[k] {
					w++
				}
			}
			st.waived = float64(w) / float64(len(d.order))
		}
		out[model] = st
	}
	return out, nil
}

// pickReviewer is model-stats.mjs pickReviewer.
func pickReviewer(candidates []candidate, stats map[string]*modelStat, o adaptiveOpts, ticketID string) (candidate, string) {
	first := candidates[0]
	if !o.enabled {
		return first, "disabled"
	}
	count := func(c candidate) int {
		if s := stats[c.model]; s != nil {
			return s.sessions
		}
		return 0
	}
	measured := []candidate{}
	for _, c := range candidates {
		if s := stats[c.model]; s != nil && float64(s.sessions) >= o.minSessions && s.median != nil {
			measured = append(measured, c)
		}
	}
	if len(measured) == 0 {
		return first, "configured-order"
	}
	least, most := count(candidates[0]), count(candidates[0])
	for _, c := range candidates {
		least = min(least, count(c))
		most = max(most, count(c))
	}
	less := []candidate{}
	for _, c := range candidates {
		if count(c) == least && count(c) < most {
			less = append(less, c)
		}
	}
	hash := sha256.Sum256([]byte(ticketID))
	if len(less) > 0 && float64(binary.BigEndian.Uint32(hash[0:4]))/0x100000000 < o.explore {
		return less[int(binary.BigEndian.Uint32(hash[4:8]))%len(less)], "explore"
	}
	isMeasured := false
	for _, c := range measured {
		if c == first {
			isMeasured = true
		}
	}
	if !isMeasured {
		return first, "configured-order"
	}
	score := func(c candidate) float64 {
		s := stats[c.model]
		return s.availability * (1 - s.waived) / math.Max(*s.median, 0.001)
	}
	best := measured[0]
	for _, c := range measured[1:] {
		if score(c) > score(best) {
			best = c
		}
	}
	return best, "performance"
}
