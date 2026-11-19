package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
)

// gflNow is the findings clock (a test seam).
var gflNow = time.Now

// gatesFlakyGo reports each Go test a gate passed only on its reruns (B798):
// one WARN line on stderr, one test.flaky ledger row through write (owner's
// run, block and worktree first) and, inside a run, one finding in the run's
// findings list, so the flake gets its own fix block in the same session
// (B800 reads the list). A finding already listed is not added again.
func gatesFlakyGo(home string, owner ledger.Object, results []any, write func(ledger.Object) error) error {
	run, _ := owner.Get("run").(string)
	block, _ := owner.Get("block").(string)
	for _, v := range results {
		r, _ := v.(ledger.Object)
		tests, _ := r.Get("flaky_tests").([]any)
		for _, t := range tests {
			test, _ := t.(ledger.Object)
			name, _ := test.Get("test").(string)
			pkgs, _ := test.Get("packages").([]string)
			load, _ := r.Get("flaky_load").(float64)
			loadText := strconv.FormatFloat(load, 'f', -1, 64)
			gatesErrf("gates: WARN flaky test %s (%s): failed, then passed %d/%d reruns (load %s); the gate passes and a fix is listed\n",
				name, strings.Join(pkgs, ", "), gates.GoFlakyCount, gates.GoFlakyCount, loadText)
			row := owner.Clone()
			row.Set("event", "test.flaky")
			row.Set("gate", r.Get("gate"))
			row.Set("test", name)
			row.Set("packages", pkgs)
			row.Set("load", load)
			row.Set("reruns", gates.GoFlakyCount)
			if e := write(row); e != nil {
				return e
			}
			if run == "" {
				continue
			}
			rerun, _ := r.Get("flaky_rerun").([]string)
			finding := ledger.Object{
				{Key: "ts", Value: gflNow().UTC().Format("2006-01-02T15:04:05.000Z")},
				{Key: "source", Value: "flaky-test"},
				{Key: "fingerprint", Value: "flaky-test:" + strings.Join(pkgs, ",") + "." + name},
				{Key: "run", Value: run},
				{Key: "block", Value: block},
				{Key: "title", Value: "Fix flaky test " + name + " (failed at load " + loadText + ", passed " + strconv.Itoa(gates.GoFlakyCount) + "/" + strconv.Itoa(gates.GoFlakyCount) + " reruns)"},
				{Key: "acceptance", Value: "a red test that fails " + name + " the way it failed, then green with no dependence on machine load or wall time"},
				{Key: "evidence", Value: ledger.Object{{Key: "test", Value: name}, {Key: "packages", Value: pkgs}, {Key: "load", Value: load}, {Key: "rerun", Value: rerun}}},
			}
			if e := gatesAddFinding(home, run, finding); e != nil {
				return e
			}
		}
	}
	return nil
}

// gatesFindingsFile is the run's findings list: one JSON object per line.
func gatesFindingsFile(home, run string) string {
	return filepath.Join(home, ".code-forge", "runs", run, "findings.jsonl")
}

// gatesAddFinding appends finding to the run's findings list under a lock,
// unless a row with its fingerprint is already there.
func gatesAddFinding(home, run string, finding ledger.Object) error {
	file := gatesFindingsFile(home, run)
	return filelock.WithLock("findings:"+run, func() error {
		data, err := os.ReadFile(file)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, line := range bytes.Split(data, []byte("\n")) {
			var row struct{ Fingerprint string }
			if json.Unmarshal(line, &row) == nil && row.Fingerprint == finding.Get("fingerprint") {
				return nil
			}
		}
		line, err := ledger.Stringify(finding, false)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(line, '\n')); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("findings %s: %w", file, err)
		}
		return nil
	}, filelock.Options{})
}
