// Package runstatus computes the read-only status shared by the CLI and daemon.
// Unsupported review-clearing rules return false so the caller can use Node.
package runstatus

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
)

type Object = map[string]any

var runID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
var blockID = regexp.MustCompile(`^B[0-9]+$`)

func obj(v any) Object              { m, _ := v.(map[string]any); return m }
func str(m Object, k string) string { s, _ := m[k].(string); return s }
func nullable(v any) any {
	if s, ok := v.(string); ok {
		return s
	}
	return nil
}
func verified(row Object, key []byte) bool {
	r, err := sign.VerifyRow(row, key)
	return err == nil && r.OK
}

// ReadKey follows loadKey's private-mode and length checks without creating a key.
func ReadKey(home, id string) []byte {
	p := filepath.Join(home, ".code-forge", "runs", id+".key")
	if !runID.MatchString(id) {
		return make([]byte, 32)
	}
	info, err := os.Stat(p)
	if err == nil && info.Mode().Perm()&0077 == 0 {
		if key, err := os.ReadFile(p); err == nil && len(key) == 32 {
			return key
		}
	}
	return make([]byte, 32)
}

// Compute consumes a snapshot. nil rows mean the ledger was unreadable.
func Compute(record Object, rows []ledger.Row, home string) (ledger.Object, bool) {
	id, ok := record["run_id"].(string)
	if !ok {
		return nil, false
	}
	workspace, ok := record["workspace"].(string)
	if !ok {
		return nil, false
	}
	var budget any
	if cfg, err := config.Load(workspace); err == nil {
		if b, ok := obj(cfg["budget"])["usd"].(float64); ok && b > 0 && !math.IsInf(b, 0) {
			budget = b
		}
	}
	key := ReadKey(home, id)
	byBlock := map[string][]Object{}
	sum, unknown := 0.0, 0
	for _, value := range rows {
		row := obj(value)
		if row == nil {
			return nil, false
		}
		// Legacy unscoped reviews can affect the gate even though reviewedFiles is scoped.
		if row["run"] != id && row["run"] != nil {
			continue
		}
		event := str(row, "event")
		if event == "review.late_finding" {
			if _, ok := row["file"].(string); !ok {
				return nil, false
			}
		}
		if event == "review.answered" || event == "review.reopened" || event == "block.acceptance_amended" || event == "review.waived" && row["by"] != "human" {
			return nil, false
		}
		if row["run"] != id {
			continue
		}
		if b, ok := row["block"].(string); ok {
			byBlock[b] = append(byBlock[b], row)
		}
		if (event == "session" || event == "session.background") && row["source"] != "shadow" {
			cost, known := rowUSD(row)
			if known {
				sum += cost
			} else {
				unknown++
			}
		}
	}
	entries := obj(record["blocks"])
	if record["blocks"] != nil && entries == nil {
		return nil, false
	}
	ids := []string{}
	awaiting := []string{}
	for id := range entries {
		ids = append(ids, id)
	}
	// ICU collation is not available in the dependency-free native runtime.
	// ASCII B<number> identifiers have an exact numeric order; other ids fall back.
	for _, id := range ids {
		if !blockID.MatchString(id) {
			return nil, false
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := strings.TrimLeft(ids[i][1:], "0"), strings.TrimLeft(ids[j][1:], "0")
		if len(a) != len(b) {
			return len(a) < len(b)
		}
		if a != b {
			return a < b
		}
		return ids[i] < ids[j]
	})
	blocks := []any{}
	closed, stopped := 0, 0
	for _, bid := range ids {
		entry := obj(entries[bid])
		status := "open"
		if s, ok := entry["status"].(string); ok {
			status = s
		}
		if status == "closed" {
			closed++
			continue
		}
		if status == "stopped" {
			stopped++
			continue
		}
		if status == "awaiting_human_check" {
			awaiting = append(awaiting, bid)
		}
		state := status
		if n, ok := entry["attempt"].(float64); status == "open" && ok && math.Trunc(n) == n && n > 1 {
			encoded, err := ledger.Stringify(n, false)
			if err != nil {
				return nil, false
			}
			state = "attempt-" + string(encoded)
		}
		mine := byBlock[bid]
		var model any
		var gateName any
		gateResult := "none"
		var red any
		for _, r := range mine {
			e := str(r, "event")
			if (e == "session" || e == "session.background") && r["role"] == "coder" && str(r, "model") != "" {
				model = str(r, "model")
				if str(r, "provider") != "" {
					model = str(r, "provider") + "/" + str(r, "model")
				}
			}
			if e == "gate.red" {
				red = nullable(r["gate"])
				gateName = red
				gateResult = "fail"
			}
			if e == "gate.done" {
				gateResult = "fail"
				gateName = red
				if r["all_ok"] == true {
					gateResult = "pass"
					gateName = nil
				}
				red = nil
			}
		}
		count := findings(bid, id, mine, rows, key)
		blocks = append(blocks, ledger.Object{{Key: "id", Value: bid}, {Key: "state", Value: state}, {Key: "level", Value: nullable(entry["level"])}, {Key: "model", Value: model}, {Key: "gate", Value: ledger.Object{{Key: "result", Value: gateResult}, {Key: "name", Value: gateName}}}, {Key: "findings", Value: count}})
	}
	var spent, unknownValue any
	if rows != nil {
		spent = round(sum)
		if math.IsInf(spent.(float64), 0) {
			return nil, false
		}
		unknownValue = unknown
	}
	mode := "plan"
	if record["mode"] == "brief" {
		mode = "brief"
	}
	result := ledger.Object{{Key: "run_id", Value: id}, {Key: "status", Value: nullable(record["status"])}, {Key: "mode", Value: mode}, {Key: "spent_usd", Value: spent}, {Key: "unknown_usd_sessions", Value: unknownValue}, {Key: "budget_usd", Value: budget}, {Key: "blocks", Value: blocks}, {Key: "more", Value: 0}, {Key: "closed", Value: closed}, {Key: "stopped", Value: stopped}}
	if len(awaiting) > 0 {
		sort.Strings(awaiting)
		result.Set("awaiting_human_check", awaiting)
	}
	if flaky := flakyFiles(rows, id); len(flaky) > 0 {
		result.Set("flaky", flaky)
	}
	timed := timeoutBlocks(rows, id)
	for _, v := range blocks {
		b := v.(ledger.Object)
		g := b.Get("gate").(ledger.Object)
		if g.Get("result") == "fail" && timed[b.Get("id").(string)] {
			g.Set("result", "timeout")
		}
	}
	return result, true
}

func amount(v any) (float64, bool) {
	n, ok := v.(float64)
	return n, ok && n >= 0 && !math.IsInf(n, 0) && !math.IsNaN(n)
}
func round(n float64) float64 {
	// Go permits fusing the multiply with the subtraction below. ECMAScript
	// rounds each operation; the explicit conversion prevents that fusion.
	x := float64(n * 10000)
	floor := math.Floor(x)
	if x-floor >= .5 {
		floor++
	}
	return floor / 10000
}
func rowUSD(r Object) (float64, bool) {
	if v, ok := r["usd"]; ok {
		return amount(v)
	}
	if r["usd_unknown"] == true {
		return 0, false
	}
	if v := r["cost_usd"]; v != nil {
		return amount(v)
	}
	if r["event"] == "session.background" {
		return 0, false
	}
	in, a := amount(r["tokens_in"])
	out, b := amount(r["tokens_out"])
	if !a || !b {
		return 0, false
	}
	prices := map[string]map[string]float64{"anthropic": {"L0": .001, "L1": .003, "L2": .015, "L3": .03}, "openai": {"L0": .001, "L1": .0025, "L2": .012, "L3": .025}, "xai": {"L0": .001, "L1": .002, "L2": .01, "L3": .02}}
	p, ok := prices[str(r, "provider")][str(r, "level")]
	return round((in + out) / 1000 * p), ok
}

func timeoutBlocks(rows []ledger.Row, id string) map[string]bool {
	pending, timed := map[string]bool{}, map[string]bool{}
	for _, v := range rows {
		r := obj(v)
		if r["run"] != id {
			continue
		}
		b, ok := r["block"].(string)
		if !ok {
			continue
		}
		switch r["event"] {
		case "gate.red":
			if r["reason"] == "timeout" || r["timed_out"] == true {
				pending[b] = true
				timed[b] = true
			} else {
				delete(pending, b)
				delete(timed, b)
			}
		case "gate.done":
			if r["all_ok"] == true || !pending[b] {
				delete(timed, b)
			}
			delete(pending, b)
		}
	}
	return timed
}

func flakyFiles(rows []ledger.Row, id string) []string {
	flaky := map[string]bool{}
	for _, v := range rows {
		r := obj(v)
		if r["run"] != id || r["event"] != "gate.done" {
			continue
		}
		files, _ := r["flaky"].([]any)
		for _, v := range files {
			if s, ok := v.(string); ok {
				flaky[s] = true
			}
		}
	}
	files := []string{}
	for f := range flaky {
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool {
		return slices.Compare(utf16.Encode([]rune(files[i])), utf16.Encode([]rune(files[j]))) < 0
	})
	return files
}

// Details adds Node's timeout and UTF-16 sorted flaky metadata to one summary.
func Details(summary Object, rows []ledger.Row, id string) {
	timed := timeoutBlocks(rows, id)
	files := flakyFiles(rows, id)
	delete(summary, "flaky")
	if len(files) > 0 {
		summary["flaky"] = files
	}
	blocks, _ := summary["blocks"].([]any)
	for _, v := range blocks {
		b := obj(v)
		g := obj(b["gate"])
		if g["result"] == "fail" && timed[str(b, "id")] {
			g["result"] = "timeout"
		}
	}
}

var findingID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
var reviewEvents = map[string]bool{"review.result": true, "review.round": true, "review.approved": true, "review.triage": true, "review.cap": true, "review.late_finding": true}

func reviewedPath(r Object) (string, bool) {
	f, ok := r["file"].(string)
	if !ok || !reviewEvents[str(r, "event")] {
		return "", false
	}
	if f == "" || strings.ContainsAny(f, "\\\x00") || strings.HasPrefix(f, "/") {
		return "", false
	}
	clean := filepath.ToSlash(filepath.Clean(f))
	if clean == ".." || strings.HasPrefix(clean, "../") || clean == "." {
		return "", false
	}
	return clean, true
}

// findings ports the gate's latest refusal/approval, cap, late ruling and human
// waiver rules. Delegated waivers, answers and reopened caps are declined above.
func findings(block, id string, scoped []Object, rows []ledger.Row, key []byte) int {
	hashes := map[string]string{}
	for _, r := range scoped {
		if f, ok := reviewedPath(r); ok {
			if _, exists := hashes[f]; !exists {
				hashes[f] = ""
			}
			if h, ok := r["content_hash"].(string); ok {
				hashes[f] = h
			}
		}
	}
	if len(hashes) == 0 {
		hasLate := false
		for _, r := range scoped {
			if r["event"] == "review.late_finding" {
				hasLate = true
			}
		}
		if !hasLate {
			return 0
		}
	}
	mine := []Object{}
	for _, v := range rows {
		r := obj(v)
		if r["block"] == block && (r["run"] == nil || r["run"] == id) {
			mine = append(mine, r)
		}
	}
	waived := func(file, finding string) bool {
		for _, r := range mine {
			if r["event"] == "review.waived" && r["by"] == "human" && r["file"] == file && r["finding"] == finding && verified(r, key) {
				return true
			}
		}
		return false
	}
	pairs := map[string]bool{}
	add := func(file string, ids []string) {
		for _, f := range ids {
			if !waived(file, f) {
				pairs[file+"\x00"+f] = true
			}
		}
	}
	capCleared := map[int]bool{}
	for file, hash := range hashes {
		approval, last := -1, -1
		var latest []string
		hasCap := false
		for i, r := range mine {
			if r["file"] != file {
				continue
			}
			if r["event"] == "review.cap" {
				hasCap = true
			}
			if r["event"] == "review.approved" {
				if r["content_hash"] == hash && verified(r, key) {
					approval = i
				}
				continue
			}
			if h, exists := r["content_hash"]; exists && h != hash {
				continue
			}
			ids, denies := denial(r)
			if denies {
				last = i
				latest = ids
			}
		}
		if approval >= 0 && last > approval || approval < 0 && !hasCap {
			add(file, latest)
		}
		for i, r := range mine {
			if r["file"] == file && r["event"] == "review.cap" && approval > i && last <= approval {
				capCleared[i] = true
			}
		}
	}
	for i, r := range mine {
		if r["event"] == "review.cap" && !capCleared[i] {
			ids, _ := denial(r)
			add(str(r, "file"), ids)
		}
	}
	for _, r := range mine {
		if r["event"] != "review.late_finding" {
			continue
		}
		file, f := str(r, "file"), str(r, "finding")
		if _, ok := r["finding"].(string); !ok {
			continue
		}
		ruled := false
		if r["severity"] == "warning" || r["severity"] == "nit" {
			for _, s := range mine {
				if s["event"] == "review.late_ruling" && s["file"] == file && s["finding"] == f && s["ruling"] == "nit" && verified(s, key) {
					ruled = true
				}
			}
		}
		if !ruled {
			add(file, []string{f})
		}
	}
	return len(pairs)
}
func denial(r Object) ([]string, bool) {
	switch r["event"] {
	case "review.result":
		if r["status"] == "stale" || r["status"] == "unavailable" || r["status"] == "answers" || r["status"] == "refused" && r["reason"] == "other-run" {
			return nil, false
		}
		if r["approved"] != true && r["approved"] != false {
			return nil, true
		}
		values, _ := r["open"].([]any)
		ids := []string{}
		severities := map[string]string{}
		ranks := map[string]int{"nit": 0, "warning": 1, "critical": 2, "unknown": 3}
		for i, v := range values {
			f := obj(v)
			id := str(f, "id")
			if !findingID.MatchString(id) {
				id = fmt.Sprintf("X%d", i+1)
			}
			ids = append(ids, id)
			severity := str(f, "severity")
			if _, ok := ranks[severity]; !ok {
				severity = "unknown"
			}
			if had, ok := severities[id]; !ok || ranks[severity] > ranks[had] {
				severities[id] = severity
			}
		}
		critical := false
		for _, severity := range severities {
			if severity == "critical" {
				critical = true
			}
		}
		return ids, r["approved"] != true || critical
	case "review.round":
		return nil, r["open_after"] != float64(0)
	case "review.cap":
		values, _ := r["open"].([]any)
		ids := []string{}
		for _, v := range values {
			if s, ok := v.(string); ok && s != "" {
				ids = append(ids, s)
			}
		}
		return ids, true
	}
	return nil, false
}
