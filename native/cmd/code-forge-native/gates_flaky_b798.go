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
//
// B958: when this call lists a flaky test and the run's list then holds more
// than gatesFlakyRunMax flaky tests, it writes a test.flaky_limit row and
// returns a *gatesFlakyLimit: the gate is red.
func gatesFlakyGo(home string, owner ledger.Object, results []any, write func(ledger.Object) error) error {
	run, _ := owner.Get("run").(string)
	block, _ := owner.Get("block").(string)
	flaky := false
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
			flaky = true
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
	if !flaky {
		return nil
	}
	return gatesFlakyRunLimit(home, run, owner, write)
}

// gatesFlakyRunMax is the most flaky tests one run may list and stay green (B958).
const gatesFlakyRunMax = 3

// gatesFlakyLimit is the red of a run with too many flaky tests (B958).
type gatesFlakyLimit struct {
	run   string
	tests []string
}

func (e *gatesFlakyLimit) Error() string {
	return fmt.Sprintf("gates: %d flaky tests in run %s (%s), more than %d: the gate is red; fix the flaky tests first (B958)",
		len(e.tests), e.run, strings.Join(e.tests, ", "), gatesFlakyRunMax)
}

// gatesFlakyRunLimit counts the flaky tests in the run's findings list and,
// above gatesFlakyRunMax, writes the test.flaky_limit row and returns the red.
func gatesFlakyRunLimit(home, run string, owner ledger.Object, write func(ledger.Object) error) error {
	data, err := os.ReadFile(gatesFindingsFile(home, run))
	if err != nil {
		return err
	}
	tests := []string{}
	for _, line := range bytes.Split(data, []byte("\n")) {
		var row struct {
			Source, Fingerprint string
		}
		if json.Unmarshal(line, &row) == nil && row.Source == "flaky-test" {
			tests = append(tests, strings.TrimPrefix(row.Fingerprint, "flaky-test:"))
		}
	}
	if len(tests) <= gatesFlakyRunMax {
		return nil
	}
	row := owner.Clone()
	row.Set("event", "test.flaky_limit")
	row.Set("tests", tests)
	row.Set("max", gatesFlakyRunMax)
	if e := write(row); e != nil {
		return e
	}
	return &gatesFlakyLimit{run: run, tests: tests}
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
