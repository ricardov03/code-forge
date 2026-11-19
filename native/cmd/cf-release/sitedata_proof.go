package main

// B784: the /proof page's samples, picked from code-forge's own ledger rows by fixed rules (see
// docs/releasing.md). Every value is a count, a sum, a time or a short id that the ledger holds;
// a sample with no matching rows is null and the page hides its card. No path, file name, prompt,
// model or free text leaves the ledger: ids must match sdpIDRe, everything else is a number.

import (
	"math"
	"regexp"
	"time"

	"github.com/ricardov03/code-forge/native/runstatus"
)

var (
	// sdpIDRe is a run, block, finding or gate id that can go on the page: short, no slash,
	// no dot-dot, no space.
	sdpIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	// sdpScopeRe is a gate.done scope word (full, changed, ...).
	sdpScopeRe = regexp.MustCompile(`^[a-z][a-z_-]{0,23}$`)
)

// sdpNightMs is the longest wall time of an "overnight batch" run: 12 hours.
const sdpNightMs = 12 * 3600 * 1000

func sdpID(v any) (string, bool) {
	s, ok := v.(string)
	return s, ok && sdpIDRe.MatchString(s)
}

// sdpTS is a row's ts in milliseconds.
func sdpTS(row map[string]any) (float64, bool) {
	s, ok := row["ts"].(string)
	if !ok {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0, false
	}
	return float64(t.UnixMilli()), true
}

func sdpDate(ms float64) string {
	return time.UnixMilli(int64(ms)).UTC().Format("2006-01-02")
}

// sdpDuration is the report's duration text: 45s, 13m 19s, 1h 18m 46s.
func sdpDuration(ms float64) string {
	seconds := math.Floor(ms / 1000)
	if seconds < 60 {
		return jsNumberString(seconds) + "s"
	}
	minutes := math.Floor(seconds / 60)
	if minutes < 60 {
		return jsNumberString(minutes) + "m " + jsNumberString(math.Mod(seconds, 60)) + "s"
	}
	return jsNumberString(math.Floor(minutes/60)) + "h " + jsNumberString(math.Mod(minutes, 60)) + "m " + jsNumberString(math.Mod(seconds, 60)) + "s"
}

type sdpRun struct {
	id                      string
	start, end              float64
	hasStart, hasEnd, first bool
	firstTS                 float64
	usd                     float64
	closed                  map[string]bool
	stopped                 map[string]bool
}

type sdpBlock struct {
	run, block             string
	dispatch, stop         float64
	hasDispatch, isStopped bool
	usd                    float64
}

type sdpFileReview struct {
	rounds  int
	warning string // the last warning finding id seen open
}

func sdpIsSpend(row map[string]any) bool {
	return (row["event"] == "session" || row["event"] == "session.background") && row["source"] != "shadow"
}

func sdpObj(pairs ...any) *jsObject {
	o := &jsObject{vals: map[string]any{}}
	for i := 0; i < len(pairs); i += 2 {
		o.set(pairs[i].(string), pairs[i+1])
	}
	return o
}

// sdpSamples adds overnight, single, stopped, finding and gate to proof.
func sdpSamples(proof *jsObject, rows []map[string]any) {
	runs := map[string]*sdpRun{}
	var order []*sdpRun
	blocks := map[[2]string]*sdpBlock{}
	var blockOrder []*sdpBlock
	runOf := func(id string) *sdpRun {
		r := runs[id]
		if r == nil {
			r = &sdpRun{id: id, closed: map[string]bool{}, stopped: map[string]bool{}}
			runs[id] = r
			order = append(order, r)
		}
		return r
	}
	blockOf := func(run, block string) *sdpBlock {
		key := [2]string{run, block}
		b := blocks[key]
		if b == nil {
			b = &sdpBlock{run: run, block: block}
			blocks[key] = b
			blockOrder = append(blockOrder, b)
		}
		return b
	}
	closedPairs := map[[2]string]bool{}
	for _, row := range rows {
		if row["event"] == "block.close" && row["status"] == "complete" {
			run, _ := row["run"].(string)
			block, _ := row["block"].(string)
			closedPairs[[2]string{run, block}] = true
		}
	}

	type findingPick struct {
		run, block, finding string
		rounds              int
		ts                  float64
		hasTS               bool
	}
	var finding *findingPick
	reviews := map[[3]string]*sdpFileReview{}
	waived := map[[4]string]bool{}
	var gate *jsObject

	for _, row := range rows {
		runID, runOK := row["run"].(string)
		if !runOK || runID == "" {
			continue
		}
		r := runOf(runID)
		event, _ := row["event"].(string)
		ts, hasTS := sdpTS(row)
		if hasTS {
			if !r.first {
				r.first, r.firstTS = true, ts
			}
			if event == "run.start" && !r.hasStart {
				r.hasStart, r.start = true, ts
			}
			if event == "run.end" {
				r.hasEnd, r.end = true, ts
			}
		}
		if sdpIsSpend(row) {
			if cost, known := runstatus.RowUSD(row); known {
				r.usd += cost
			}
		}
		blockID, blockOK := row["block"].(string)
		if !blockOK || blockID == "" {
			continue
		}
		b := blockOf(runID, blockID)
		if sdpIsSpend(row) {
			if cost, known := runstatus.RowUSD(row); known {
				b.usd += cost
			}
		}
		switch event {
		case "dispatch":
			if hasTS && !b.hasDispatch {
				b.hasDispatch, b.dispatch = true, ts
			}
			b.isStopped = false
		case "block.close", "block.awaiting_human_check", "block.human_pass":
			b.isStopped = false
			if event == "block.close" && row["status"] == "complete" {
				r.closed[blockID] = true
				delete(r.stopped, blockID)
			}
		case "block.stop":
			b.isStopped = hasTS
			b.stop = ts
			if !r.closed[blockID] {
				r.stopped[blockID] = true
			}
		case "review.waived":
			file, _ := row["file"].(string)
			id, _ := row["finding"].(string)
			waived[[4]string{runID, blockID, file, id}] = true
		case "review.result":
			file, ok := row["file"].(string)
			if !ok || file == "" {
				break
			}
			key := [3]string{runID, blockID, file}
			fr := reviews[key]
			if fr == nil {
				fr = &sdpFileReview{}
				reviews[key] = fr
			}
			fr.rounds++
			if row["approved"] == true {
				if fr.warning != "" && !waived[[4]string{runID, blockID, file, fr.warning}] && closedPairs[[2]string{runID, blockID}] {
					if _, ok := sdpID(runID); ok {
						if _, ok := sdpID(blockID); ok {
							finding = &findingPick{runID, blockID, fr.warning, fr.rounds, ts, hasTS}
						}
					}
				}
				delete(reviews, key)
				break
			}
			if list, ok := row["open"].([]any); ok {
				for _, item := range list {
					f, _ := item.(map[string]any)
					if id, ok := sdpID(f["id"]); ok && f["severity"] == "warning" {
						fr.warning = id
					}
				}
			}
		case "gate.done":
			if row["all_ok"] != true || row["cancelled"] == true || !hasTS || !closedPairs[[2]string{runID, blockID}] {
				break
			}
			if _, ok := sdpID(runID); !ok {
				break
			}
			if _, ok := sdpID(blockID); !ok {
				break
			}
			list, _ := row["results"].([]any)
			results := []any{}
			for _, item := range list {
				res, _ := item.(map[string]any)
				name, ok := sdpID(res["gate"])
				if !ok {
					results = nil
					break
				}
				outcome := "failed"
				if res["skipped"] == true {
					outcome = "skipped"
				} else if res["ok"] == true {
					outcome = "ok"
				}
				var wallMs, wall any
				if d, ok := res["duration_ms"].(float64); ok && d >= 0 && res["skipped"] != true {
					wallMs, wall = jsNum(d), sdpDuration(d)
				}
				results = append(results, sdpObj("gate", name, "result", outcome, "wall_ms", wallMs, "wall", wall))
			}
			if len(results) == 0 {
				break
			}
			g := sdpObj("run", runID, "block", blockID, "date", sdpDate(ts))
			var scope any
			if s, ok := row["scope"].(string); ok && sdpScopeRe.MatchString(s) {
				scope = s
			}
			g.set("scope", scope)
			g.set("results", results)
			gate = g
		}
	}

	// Runs: an ended run with its start (run.start, else its first timed row) and a wall >= 0.
	runSample := func(want func(r *sdpRun, wall float64) bool) any {
		var pick *sdpRun
		pickStart := 0.0
		for _, r := range order {
			start, ok := r.start, r.hasStart
			if !ok {
				start, ok = r.firstTS, r.first
			}
			if !ok || !r.hasEnd || r.end < start || !want(r, r.end-start) {
				continue
			}
			if _, ok := sdpID(r.id); !ok {
				continue
			}
			if pick == nil || start >= pickStart {
				pick, pickStart = r, start
			}
		}
		if pick == nil {
			return nil
		}
		wall := pick.end - pickStart
		return sdpObj("run", pick.id, "date", sdpDate(pickStart), "wall_ms", jsNum(wall), "wall", sdpDuration(wall),
			"usd", jsNum(runstatus.RoundUSD(pick.usd)), "closed", jsNum(len(pick.closed)), "stopped", jsNum(len(pick.stopped)))
	}
	// overnight: 3 or more closed blocks within one night (12 hours at most); runs that stay
	// open for days are not a batch. single: exactly one closed block and none stopped.
	proof.set("overnight", runSample(func(r *sdpRun, wall float64) bool { return len(r.closed) >= 3 && wall <= sdpNightMs }))
	proof.set("single", runSample(func(r *sdpRun, _ float64) bool { return len(r.closed) == 1 && len(r.stopped) == 0 }))

	// Stopped block: its last state is block.stop, with a dispatch before the stop and spend
	// recorded against it (the card shows that cost is kept when a block stops).
	var stopped *sdpBlock
	for _, b := range blockOrder {
		if !b.isStopped || !b.hasDispatch || b.stop < b.dispatch || runstatus.RoundUSD(b.usd) <= 0 {
			continue
		}
		if _, ok := sdpID(b.run); !ok {
			continue
		}
		if _, ok := sdpID(b.block); !ok {
			continue
		}
		if stopped == nil || b.stop >= stopped.stop {
			stopped = b
		}
	}
	if stopped == nil {
		proof.set("stopped", nil)
	} else {
		wall := stopped.stop - stopped.dispatch
		proof.set("stopped", sdpObj("run", stopped.run, "block", stopped.block, "date", sdpDate(stopped.stop),
			"wall_ms", jsNum(wall), "wall", sdpDuration(wall), "usd", jsNum(runstatus.RoundUSD(stopped.usd))))
	}

	if finding == nil {
		proof.set("finding", nil)
	} else {
		var date any
		if finding.hasTS {
			date = sdpDate(finding.ts)
		}
		proof.set("finding", sdpObj("run", finding.run, "block", finding.block, "finding", finding.finding,
			"severity", "warning", "rounds", jsNum(finding.rounds), "date", date))
	}
	if gate == nil {
		proof.set("gate", nil)
	} else {
		proof.set("gate", gate)
	}
}
