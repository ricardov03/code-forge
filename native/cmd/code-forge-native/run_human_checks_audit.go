package main

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/humancheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

const rhcAuditUsage = "usage: code-forge run human-checks audit --run <id> [--json]\n"
const rhcReopenUsage = "usage: code-forge run human-checks reopen --run <id> --block <id> [--clause <C<n>>] --note <text> [--json]\n"

func rhcPasses(home, run string, record ledger.Object) ([]ledger.Object, error) {
	slug, _ := record.Get("project").(string)
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
	if err != nil {
		return nil, err
	}
	key, _ := blockclose.ReadKey(home, run)
	return humancheck.AutoPasses(run, rows, key), nil
}

func rhcPassText(p ledger.Object) string {
	hash, _ := p.Get("proof_sha256").(string)
	if len(hash) > 12 {
		hash = hash[:12]
	}
	return fmt.Sprintf("%v %v passed by autopilot (grant %v, proof sha256 %s); proof %v", p.Get("block"), p.Get("clause"), p.Get("grant_id"), hash, p.Get("proof"))
}

func rhcHashStatus(path, expected string) string {
	if path == "" || expected == "" {
		return "missing"
	}
	// Never block the audit by opening a FIFO or device from an edited path.
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "missing"
		}
		return "changed"
	}
	if !info.Mode().IsRegular() {
		return "changed"
	}
	f, err := os.Open(path)
	if err != nil {
		return "changed"
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "changed"
	}
	if fmt.Sprintf("%x", h.Sum(nil)) != expected {
		return "changed"
	}
	return "ok"
}

func rhcAudit(home string, args []string, stdout io.Writer) *rhcFail {
	flags, json, fail := rhcFlags(args, "run")
	if fail != nil {
		return fail
	}
	run := flags["run"]
	if run == "" {
		return rhcUsage("--run is required")
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return &rhcFail{1, blockIOMessage(err)}
	}
	passes, err := rhcPasses(home, run, record)
	if err != nil {
		return &rhcFail{1, blockIOMessage(err)}
	}
	list := []any{}
	for _, p := range passes {
		path, _ := p.Get("proof").(string)
		hash, _ := p.Get("proof_sha256").(string)
		status := rhcHashStatus(path, hash)
		evidence := []any{}
		files, _ := p.Get("evidence").([]any)
		for _, v := range files {
			name, _ := bnGet(v, "path").(string)
			want, _ := bnGet(v, "sha256").(string)
			resolved := name
			if resolved != "" && !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(path), resolved)
			}
			got := rhcHashStatus(resolved, want)
			evidence = append(evidence, ledger.Object{{Key: "path", Value: name}, {Key: "status", Value: got}})
			if got != "ok" && (status == "ok" || got == "missing") {
				status = got
			}
		}
		p.Set("status", status)
		p.Set("evidence_audit", evidence)
		list = append(list, p)
		if !json {
			fmt.Fprintf(stdout, "%s; audit %s\n", rhcPassText(p), status)
		}
	}
	if json {
		data, _ := ledger.Stringify(ledger.Object{{Key: "run", Value: run}, {Key: "passes", Value: list}}, false)
		fmt.Fprintln(stdout, string(data))
	} else if len(list) == 0 {
		fmt.Fprintf(stdout, "run %s: no autopilot passes\n", run)
	}
	return nil
}

func rhcReopen(home string, args []string, stdout, stderr io.Writer) *rhcFail {
	flags, json, fail := rhcFlags(args, "run", "block", "clause", "note")
	if fail != nil {
		return fail
	}
	run, block, clause, note := flags["run"], flags["block"], flags["clause"], flags["note"]
	if run == "" || block == "" {
		return rhcUsage("--run and --block are required")
	}
	if !rhcTextOK(note) || !closeTextSafe(note) {
		return rhcUsage("--note must be nonblank safe text without control characters")
	}
	noTTY := func() *rhcFail {
		fmt.Fprintln(stderr, "run human-checks reopen needs the human at a terminal to confirm; nothing was recorded (there is no --yes)")
		return &rhcFail{2, ""}
	}
	if !humanTTY() {
		return noTTY()
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return &rhcFail{1, blockIOMessage(err)}
	}
	// Re-select under the run lock, and reject any change while the owner answered.
	selectPasses := func(rec ledger.Object) ([]ledger.Object, error) {
		if rec.Get("status") != "active" {
			return nil, fmt.Errorf("run %s has ended", run)
		}
		if g := blockObject(rec.Get("autopilot")); g != nil {
			s, err := autopilot.GrantState(g, rhcNow())
			if err != nil {
				return nil, err
			}
			if s == "active" {
				return nil, fmt.Errorf("autopilot-active: reopen is refused while an autopilot grant is active on run %s", run)
			}
		}
		open, fail := rhcOpenFor(home, run, rec)
		if fail != nil {
			return nil, fail
		}
		passes, err := rhcPasses(home, run, rec)
		if err != nil {
			return nil, err
		}
		entry := blockObject(blockObject(rec.Get("blocks")).Get(block))
		if entry.Get("status") != "closed" && entry.Get("status") != "awaiting_human_check" && entry.Get("status") != humancheck.DeferredStatus {
			return nil, fmt.Errorf("block %s is not closed or awaiting human checks", block)
		}
		selected := []ledger.Object{}
		seen := map[string]bool{}
		clauses, _ := entry.Get("acceptance").([]any)
		for i := len(passes) - 1; i >= 0; i-- {
			p := passes[i]
			if p.Get("block") != block {
				continue
			}
			id, _ := p.Get("clause").(string)
			if seen[id] || clause != "" && clause != id {
				continue
			}
			seen[id] = true
			if _, ok := humancheck.Find(open, block, id); ok {
				continue
			}
			valid := false
			for i, v := range clauses {
				c := blockObject(v)
				if humancheck.ClauseID(i) == id && c.Get("kind") == "human_check" && c.Get("clause") == p.Get("check") {
					valid = true
				}
			}
			if valid {
				selected = append(selected, p)
			}
		}
		if len(selected) == 0 {
			suffix := ""
			if clause != "" {
				suffix = " matching " + clause
			}
			return nil, fmt.Errorf("auto-pass-not-found: block %s has no passed autopilot check%s", block, suffix)
		}
		return selected, nil
	}
	// Check expiry before confirmation using the existing owner-verb path.
	if _, err = autopilot.CheckExpiry(home, run, rhcNow()); err != nil {
		return &rhcFail{1, blockIOMessage(err)}
	}
	record, err = state.ReadRun(home, run)
	if err != nil {
		return &rhcFail{1, blockIOMessage(err)}
	}
	selected, err := selectPasses(record)
	if err != nil {
		return &rhcFail{1, err.Error()}
	}
	result, err := humanAsk(fmt.Sprintf("Reopen %d autopilot check(s) of block %s (run %s)? Only the owner may say yes.", len(selected), block, run))
	if result == humanask.NoTTY {
		return noTTY()
	}
	if err != nil || result != humanask.Confirmed {
		return &rhcFail{1, "cancelled — nothing was recorded"}
	}
	rows := []ledger.Object{}
	landed := false
	err = state.UpdateBlock(home, run, "", func(current, _ *ledger.Object) ([]ledger.Object, error) {
		rows = rows[:0]
		again, err := selectPasses(*current)
		if err != nil {
			return nil, err
		}
		before, _ := ledger.Stringify(selected, false)
		after, _ := ledger.Stringify(again, false)
		if string(before) != string(after) {
			return nil, state.BlockChanged(run, block)
		}
		for _, p := range again {
			rows = append(rows, ledger.Object{{Key: "event", Value: humancheck.ReopenedEvent}, {Key: "block", Value: block}, {Key: "clause", Value: p.Get("clause")}, {Key: "revokes", Value: p.Get("decision_id")}, {Key: "note", Value: humanTrim(note)}, {Key: "by", Value: "human"}})
		}
		blocks := blockObject(current.Get("blocks"))
		entry := blockObject(blocks.Get(block))
		landed = entry.Get("landed") != nil
		if entry.Get("status") == "closed" && !landed {
			entry.Set("status", "awaiting_human_check")
			blocks.Set(block, entry)
			current.Set("blocks", blocks)
		}
		return rows, nil
	})
	if err != nil {
		return &rhcFail{1, err.Error()}
	}
	if json {
		data, _ := ledger.Stringify(ledger.Object{{Key: "run", Value: run}, {Key: "reopened", Value: rows}, {Key: "landed", Value: landed}}, false)
		fmt.Fprintln(stdout, string(data))
	} else {
		fmt.Fprintf(stdout, "run %s: reopened %d autopilot check(s) of block %s (by: human)\n", run, len(rows), block)
	}
	if landed {
		fmt.Fprintf(stderr, "block %s has landed; its check is open again: stop or fix block %s before ending the run\n", block, block)
	}
	return nil
}
