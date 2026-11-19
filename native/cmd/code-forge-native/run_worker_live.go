package main

// B885: one probe answers "is this run's worker alive?" for `run status`,
// `status` (forge status) and doctor, the same way block ready and run end
// judge the pin (runCheckPin): a pin is alive when its pid runs with the
// start time the pin recorded. Before, `run status` printed only
// worker_pinned (a pin is recorded), so it said true while block ready said
// worker.down.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// rwlPinAlive reports whether pin (an object with pid and started_at) names a
// running process with that start time.
func rwlPinAlive(pin ledger.Object) bool {
	pid, ok := runSignalable(pin.Get("pid"))
	if !ok {
		return false
	}
	started, isString := pin.Get("started_at").(string)
	if !isString {
		return false
	}
	now, want := runProbe(pid), runNormalize(started)
	return now != nil && want != nil && *now == *want
}

// rwlPins is the run's pins: the workers list when it has entries, else the
// single worker pin when one is recorded.
func rwlPins(workers, worker any) []any {
	if list, isList := workers.([]any); isList && len(list) > 0 {
		return list
	}
	if runTruthy(worker) {
		return []any{worker}
	}
	return nil
}

// rwlLive is the run's worker liveness: live is runCheckPin's verdict (false
// for a pin shape it cannot judge), pins lists each pin {pid, alive,
// started_at}.
func rwlLive(workers any, hasWorkers bool, worker any) (live bool, pins []any) {
	if check, ok := runCheckPin(workers, hasWorkers, worker); ok {
		live = check.ok
	}
	pins = []any{}
	for _, p := range rwlPins(workers, worker) {
		pin, _ := p.(ledger.Object)
		pins = append(pins, ledger.Object{{Key: "pid", Value: pin.Get("pid")}, {Key: "alive", Value: pin != nil && rwlPinAlive(pin)},
			{Key: "started_at", Value: pin.Get("started_at")}})
	}
	return live, pins
}

// rwlLedgerValue is v (decoded from a plain JSON map) in the ledger shapes
// runCheckPin reads; nil when it cannot be written as JSON.
func rwlLedgerValue(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	out, err := ledger.DecodeValue(data)
	if err != nil {
		return nil
	}
	return out
}

// rwlLiveOfMap is rwlLive for a record read as a plain map (forge status).
func rwlLiveOfMap(record map[string]any) (live bool, pins []any) {
	workers, hasWorkers := record["workers"]
	return rwlLive(rwlLedgerValue(workers), hasWorkers, rwlLedgerValue(record["worker"]))
}

// rwlNoLiveLine is the one stderr line `run status` prints when no pin is
// alive.
func rwlNoLiveLine(runID string) string {
	return fmt.Sprintf("run %s: no live worker; fix: code-forge run start --add-worker --run %s\n", runID, runID)
}

// rwlStatusPin is statusCheckPin's answer from the same probe: good, the
// reason word ("down", "replaced") and threw for a pin shape Node throws on.
func rwlStatusPin(record map[string]any) (good bool, reason string, threw bool) {
	workers, hasWorkers := record["workers"]
	check, ok := runCheckPin(rwlLedgerValue(workers), hasWorkers, rwlLedgerValue(record["worker"]))
	if !ok {
		return false, "", true
	}
	if check.ok {
		return true, "", false
	}
	return false, strings.TrimPrefix(check.event, "worker."), false
}

// rwlDoctorRows is one WARN run-worker row per active run that records a
// worker pin but has no live worker (doctor). Runs whose record cannot be
// read are the review-state check's to report.
func rwlDoctorRows(home string) []doctorRow {
	rows := []doctorRow{}
	list, err := os.ReadDir(filepath.Join(home, ".code-forge", "runs"))
	if err != nil {
		return rows
	}
	names := []string{}
	for _, e := range list {
		if strings.HasSuffix(e.Name(), ".json") {
			names = append(names, strings.TrimSuffix(e.Name(), ".json"))
		}
	}
	sort.Slice(names, func(i, j int) bool { return lessUTF16(names[i], names[j]) })
	for _, runID := range names {
		if !dfRunID.MatchString(runID) {
			continue
		}
		record, err := state.ReadRun(home, runID)
		if err != nil || record.Get("status") != "active" {
			continue
		}
		if rwlNoLive(record.Get("workers"), runHas(record, "workers"), record.Get("worker")) {
			rows = append(rows, doctorRow{"run-worker", "WARN", "run worker", strings.TrimSuffix(rwlNoLiveLine(runID), "\n")})
		}
	}
	return rows
}

// rwlStatusLine is forge status's no-live-worker warning: the run status
// line for an active run that rwlNoLive judges; "" else.
func rwlStatusLine(record map[string]any) string {
	workers, hasWorkers := record["workers"]
	if record["status"] != "active" || !rwlNoLive(rwlLedgerValue(workers), hasWorkers, rwlLedgerValue(record["worker"])) {
		return ""
	}
	return strings.TrimSuffix(rwlNoLiveLine(statusRunID(record)), "\n")
}

// rwlNoLive is true when the run records at least one worker pin (an
// object) and the probe finds none of them running. A pin shape the probe
// cannot judge (Node throws on it) gives false: no fix line for it.
func rwlNoLive(workers any, hasWorkers bool, worker any) bool {
	pinned := false
	for _, p := range rwlPins(workers, worker) {
		if _, isObject := p.(ledger.Object); isObject {
			pinned = true
		}
	}
	if !pinned {
		return false
	}
	check, ok := runCheckPin(workers, hasWorkers, worker)
	return ok && !check.ok
}
