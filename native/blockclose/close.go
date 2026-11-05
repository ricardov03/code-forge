// Package blockclose publishes a plain agent close in Node's run-lock order.
package blockclose

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
)

type Result struct {
	OK                    bool
	Status, Event, Reason string
	Blockers              []gatecheck.Blocker
}

func object(v any) ledger.Object { o, _ := v.(ledger.Object); return o }
func text(v any) string          { s, _ := v.(string); return s }

func ReadKey(home, run string) ([]byte, error) {
	file := filepath.Join(home, ".code-forge/runs", run+".key")
	st, err := os.Stat(file)
	if os.IsNotExist(err) {
		return nil, &state.Error{Code: "no-key", Message: fmt.Sprintf("run %s has no signer key", run)}
	}
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0077 != 0 {
		return nil, &state.Error{Code: "bad-key-mode", Message: fmt.Sprintf("run %s signer key is accessible to other users (mode %o); expected 600", run, st.Mode().Perm())}
	}
	key, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, &state.Error{Code: "bad-key", Message: fmt.Sprintf("run %s signer key is not 32 bytes", run)}
	}
	return key, nil
}

// Pin mirrors checkWorkerPin. The injected probe is shared with native status.
func Pin(record ledger.Object, live *float64, probe func(float64) string) (event, reason string) {
	pin := object(record.Get("worker"))
	pins, _ := record.Get("workers").([]any)
	valid := func(p ledger.Object) bool {
		n, ok := p.Get("pid").(float64)
		return ok && n > 1 && n == float64(int(n))
	}
	if len(pins) > 1 {
		found := live == nil
		for _, v := range pins {
			p := object(v)
			if !valid(p) {
				return "worker.replaced", "the live worker does not match a pinned worker"
			}
			if live != nil && p.Get("pid") == *live {
				pin = p
				found = true
			}
		}
		if !found {
			return "worker.replaced", "the live worker does not match a pinned worker"
		}
		if live == nil {
			for _, v := range pins {
				p := object(v)
				at := probe(p.Get("pid").(float64))
				if at != "" && at == strings.Join(strings.Fields(text(p.Get("started_at"))), " ") {
					return "", ""
				}
			}
			return "worker.down", "no pinned worker is running"
		}
	}
	if pin == nil {
		return "worker.down", "no worker is pinned for this run"
	}
	if !valid(pin) {
		return "worker.replaced", "the pinned worker pid is not an integer > 1"
	}
	pid := pin.Get("pid").(float64)
	if live != nil && *live != pid {
		return "worker.replaced", fmt.Sprintf("live worker pid %.0f is not the pinned pid %.0f", *live, pid)
	}
	at := probe(pid)
	if at == "" {
		return "worker.down", fmt.Sprintf("pinned worker pid %.0f is not running", pid)
	}
	if at != strings.Join(strings.Fields(text(pin.Get("started_at"))), " ") {
		return "worker.replaced", fmt.Sprintf("pid %.0f was restarted (start time differs from the pin)", pid)
	}
	return "", ""
}

func Tamper(run, id string, rows []gatecheck.Row, key []byte) string {
	reasons := []string{}
	dispatch := false
	for _, r := range rows {
		if r["block"] != id || r["run"] != nil && r["run"] != run || !slices.Contains([]string{"dispatch", "block.attempt", "block.rebase", "block.claim", "block.acceptance_amended", "review.approved", "proof"}, text(r["event"])) {
			continue
		}
		result, err := sign.VerifyRow(r, key)
		if err != nil || !result.OK {
			reasons = append(reasons, text(r["event"])+": "+result.Reason)
		} else if r["event"] == "dispatch" {
			dispatch = true
		}
	}
	if !dispatch {
		reasons = append(reasons, "no verified dispatch row")
	}
	return strings.Join(reasons, "; ")
}

func Stop(home, run, id, event, reason string) (Result, error) {
	err := state.UpdateBlock(home, run, id, func(record, block *ledger.Object) ([]ledger.Object, error) {
		block.Set("status", "stopped")
		block.Set("stop_reason", reason)
		return []ledger.Object{{{Key: "event", Value: event}, {Key: "block", Value: id}, {Key: "reason", Value: reason}}}, nil
	})
	return Result{Status: "stopped", Event: event, Reason: reason}, err
}

func Close(home, run, id string, closeRows []ledger.Object) (Result, error) {
	result := Result{OK: true, Status: "closed"}
	err := state.UpdateBlock(home, run, id, func(record, block *ledger.Object) ([]ledger.Object, error) {
		orphans, _ := record.Get("orphans").([]any)
		if len(orphans) > 0 {
			files := []string{}
			result = Result{Status: "open", Blockers: []gatecheck.Blocker{}}
			for _, value := range orphans {
				file := text(value)
				files = append(files, file)
				result.Blockers = append(result.Blockers, gatecheck.Blocker{Code: "unowned_change", File: file, Detail: "orphan in run registry: " + file})
			}
			result.Reason = "orphans: " + strings.Join(files, ", ")
			return []ledger.Object{}, nil
		}
		checks, _ := block.Get("human_checks").([]any)
		if len(checks) > 0 {
			result.Status = "awaiting_human_check"
		}
		block.Set("status", result.Status)
		rows := append(slices.Clone(closeRows), ledger.Object{{Key: "event", Value: "block.close"}, {Key: "block", Value: id}, {Key: "status", Value: "complete"}})
		if len(checks) > 0 {
			rows = append(rows, ledger.Object{{Key: "event", Value: "block.awaiting_human_check"}, {Key: "block", Value: id}, {Key: "checks", Value: checks}})
		}
		return rows, nil
	})
	return result, err
}

// Findings uses the gate's own finding rules at the last reviewed hashes, just
// as openFindingsOf does. It ignores proof and the absence of an approval.
func Findings(run, id string, rows []gatecheck.Row, key []byte) int {
	files := []gatecheck.File{}
	for _, file := range gatecheck.ReviewedFiles(rows, id) {
		hash := ""
		for _, r := range rows {
			if r["block"] == id && r["run"] == run && gatecheck.ReviewPath(text(r["file"])) == file && slices.Contains([]string{"review.result", "review.round", "review.approved", "review.triage", "review.cap", "review.late_finding"}, text(r["event"])) {
				if h, ok := r["content_hash"].(string); ok {
					hash = h
				}
			}
		}
		files = append(files, gatecheck.File{File: file, Hash: hash})
	}
	pairs := map[string]bool{}
	for _, b := range gatecheck.Check(run, id, files, rows, key, nil, nil, false).Blockers {
		if slices.Contains([]string{"not_approved", "late_unruled", "review_cap", "autopilot_waiver_invalid"}, b.Code) && b.Finding != "" {
			pairs[b.File+"\x00"+b.Finding] = true
		}
	}
	return len(pairs)
}
