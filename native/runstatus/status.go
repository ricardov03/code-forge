// Package runstatus computes the read-only status shared by the CLI and daemon.
// ComputeErr reports the cases Node's status fails on as errors.
package runstatus

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
)

type Object = map[string]any

var runID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func obj(v any) Object              { m, _ := v.(map[string]any); return m }
func str(m Object, k string) string { s, _ := m[k].(string); return s }
func nullable(v any) any {
	if s, ok := v.(string); ok {
		return s
	}
	return nil
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

// Compute consumes a snapshot. nil rows mean the ledger was unreadable. ok is
// false only where ComputeErr reports an error.
func Compute(record Object, rows []ledger.Row, home string) (ledger.Object, bool) {
	summary, err := ComputeErr(record, rows, home)
	if err == nil {
		// Callers other than status write the summary as JSON or compare the
		// spend; an infinite total stays unreadable for them, as before.
		if n, ok := summary.Get("spent_usd").(float64); ok && math.IsInf(n, 0) {
			return summary, false
		}
	}
	return summary, err == nil
}

// ErrUnorderedBlockID: a block id outside printable ASCII needs Node's ICU
// localeCompare to order the blocks; Go has no collator for it.
var ErrUnorderedBlockID = errors.New("block id outside printable ASCII")

// ComputeErr is Node's statusOf (briefStatus plus the gate's open findings and
// timeout details). The error carries the message Node's status prints after
// "status: " when its computation throws.
func ComputeErr(record Object, rows []ledger.Row, home string) (ledger.Object, error) {
	rawID, present := record["run_id"]
	id := gatecheck.JSString(rawID, present)
	var budget any
	// loadProjectConfig(record.workspace): a missing workspace is the default
	// parameter, process.cwd(); any other non-string throws (no budget).
	workspace, ok := record["workspace"].(string)
	if _, present := record["workspace"]; !present {
		cwd, err := os.Getwd()
		workspace, ok = cwd, err == nil
	}
	if ok {
		if cfg, err := config.Load(workspace); err == nil {
			if b, ok := obj(cfg["budget"])["usd"].(float64); ok && b > 0 && !math.IsInf(b, 0) {
				budget = b
			}
		}
	}
	key := ReadKey(home, id)
	all := []gatecheck.Row{}
	byBlock := map[string][]Object{}
	sum, unknown := 0.0, 0
	for _, value := range rows {
		if value == nil {
			// runSpend reads row.run of every row: a null row throws in Node.
			return nil, fmt.Errorf("Cannot read properties of null (reading 'run')")
		}
		row := obj(value)
		if row == nil {
			continue
		}
		all = append(all, row)
		if row["run"] != id {
			continue
		}
		if b, ok := row["block"].(string); ok {
			byBlock[b] = append(byBlock[b], row)
		}
		event := str(row, "event")
		if (event == "session" || event == "session.background") && row["source"] != "shadow" {
			cost, known := rowUSD(row)
			if known {
				sum += cost
			} else {
				unknown++
			}
		}
	}
	// statusOf lists awaiting blocks over Object.keys(record.blocks ?? {}).
	awaiting := []string{}
	keys, entries := blockKeys(record["blocks"])
	for _, bid := range keys {
		entry, isObject := entries[bid]
		if entry == nil && isObject {
			return nil, fmt.Errorf("Cannot read properties of null (reading 'status')")
		}
		if obj(entry)["status"] == "awaiting_human_check" {
			awaiting = append(awaiting, bid)
		}
	}
	// briefStatus reads record.blocks only when it is an object (arrays too).
	ids := []string{}
	if _, isText := record["blocks"].(string); !isText {
		ids = append(ids, keys...)
	}
	for _, bid := range ids {
		if _, ok := blockIDCompare(bid, bid); !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnorderedBlockID, strconv.Quote(bid))
		}
	}
	sort.SliceStable(ids, func(i, j int) bool { c, _ := blockIDCompare(ids[i], ids[j]); return c < 0 })
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
		state := status
		if n, ok := entry["attempt"].(float64); status == "open" && ok && math.Trunc(n) == n && n > 1 {
			encoded, _ := ledger.Stringify(n, false)
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
		count := openFindings(bid, id, mine, all, key)
		blocks = append(blocks, ledger.Object{{Key: "id", Value: bid}, {Key: "state", Value: state}, {Key: "level", Value: nullable(entry["level"])}, {Key: "model", Value: model}, {Key: "gate", Value: ledger.Object{{Key: "result", Value: gateResult}, {Key: "name", Value: gateName}}}, {Key: "findings", Value: count}})
	}
	var spent, unknownValue any
	if rows != nil {
		// Node's roundUsd keeps an overflowed total as Infinity: status prints
		// "$Infinity" and its JSON writes null.
		spent = round(sum)
		unknownValue = unknown
	}
	mode := "plan"
	if record["mode"] == "brief" {
		mode = "brief"
	}
	result := ledger.Object{{Key: "run_id", Value: id}, {Key: "status", Value: nullable(record["status"])}, {Key: "mode", Value: mode}, {Key: "spent_usd", Value: spent}, {Key: "unknown_usd_sessions", Value: unknownValue}, {Key: "budget_usd", Value: budget}, {Key: "blocks", Value: blocks}, {Key: "more", Value: 0}, {Key: "closed", Value: closed}, {Key: "stopped", Value: stopped}}
	if len(awaiting) > 0 {
		sortUTF16(awaiting)
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
	return result, nil
}

// blockKeys is Object.keys(record.blocks ?? {}) with the matching values: an
// object's keys, an array's indexes, a string's indexes; isObject on a key
// marks a value read from an object or array (a string index reads a character).
func blockKeys(v any) ([]string, map[string]any) {
	entries := map[string]any{}
	keys := []string{}
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			keys = append(keys, k)
			entries[k] = e
		}
		// Object.keys lists integer-like keys first in numeric order, then the
		// rest in insertion order; the caller sorts, so any stable order works.
		sort.Strings(keys)
	case []any:
		for i, e := range x {
			k := strconv.Itoa(i)
			keys = append(keys, k)
			entries[k] = e
		}
	case string:
		for i, c := range utf16.Encode([]rune(x)) {
			k := strconv.Itoa(i)
			keys = append(keys, k)
			entries[k] = string(utf16.Decode([]uint16{c}))
		}
	}
	return keys, entries
}

func sortUTF16(list []string) {
	sort.Slice(list, func(i, j int) bool {
		return slices.Compare(utf16.Encode([]rune(list[i])), utf16.Encode([]rune(list[j]))) < 0
	})
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
	// priceSession: a price that is not an amount (an overflow to Infinity)
	// is unknown.
	cost := round((in + out) / 1000 * p)
	if _, finite := amount(cost); !finite {
		return 0, false
	}
	return cost, ok
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

// findingCodes are the gate refusal codes that name one open review finding.
var findingCodes = map[string]bool{"not_approved": true, "late_unruled": true, "review_cap": true, "autopilot_waiver_invalid": true}

// openFindings is Node's openFindingsOf: the block gate's own check over the
// files the block's review rows name, each at the hash of its last review row.
func openFindings(block, run string, mine []Object, all []gatecheck.Row, key []byte) int {
	scoped := make([]gatecheck.Row, len(mine))
	for i, r := range mine {
		scoped[i] = r
	}
	hashes := map[string]string{}
	for _, r := range scoped {
		hash, ok := r["content_hash"].(string)
		if !ok {
			continue
		}
		if files := gatecheck.ReviewedFiles([]gatecheck.Row{r}, block); len(files) > 0 {
			hashes[files[0]] = hash
		}
	}
	files := []gatecheck.File{}
	for _, f := range gatecheck.ReviewedFiles(scoped, block) {
		files = append(files, gatecheck.File{File: f, Hash: hashes[f]})
	}
	if len(files) == 0 {
		late := false
		for _, r := range scoped {
			if r["event"] == "review.late_finding" {
				late = true
			}
		}
		if !late {
			return 0
		}
	}
	result := gatecheck.Evaluate(gatecheck.Input{Run: run, Block: block, Files: files, ProofFiles: []gatecheck.File{}, Rows: all, Key: key, HighPaths: []any{}})
	pairs := map[string]bool{}
	for _, b := range result.Blockers {
		if findingCodes[b.Code] && b.Finding != "" {
			pairs[b.File+"\x00"+b.Finding] = true
		}
	}
	return len(pairs)
}
