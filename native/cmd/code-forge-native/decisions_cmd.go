package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

func init() { routes["decisions"] = runDecisions }

const decisionsUsage = "usage: code-forge decisions list [--run <r>] [--json]"

// runDecisions serves `decisions list` (B390): the close.decision rows the
// autopilot signed when it decided a block close for the owner.
func runDecisions(args []string) int { return decisionsMain(args, os.Stdout, os.Stderr) }

var decisionsSlug = regexp.MustCompile(`^([a-z0-9][a-z0-9-]*)\.`)

func decisionsMain(args []string, stdout, stderr io.Writer) int {
	usage := func() int { fmt.Fprintln(stderr, decisionsUsage); return 2 }
	if len(args) == 0 || args[0] != "list" {
		return usage()
	}
	run, asJSON := "", false
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "--json" && !asJSON:
			asJSON = true
		case args[i] == "--run" && run == "" && i+1 < len(args) && args[i+1] != "" && !strings.HasPrefix(args[i+1], "-"):
			run = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--run=") && run == "" && len(args[i]) > len("--run="):
			run = strings.TrimPrefix(args[i], "--run=")
		default:
			return usage()
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "decisions: no home directory")
		return 1
	}
	dir := filepath.Join(home, ".code-forge", "ledger")
	slugs := []string{}
	if run != "" {
		record, err := state.ReadRun(home, run)
		if err != nil {
			fmt.Fprintln(stderr, "decisions:", err)
			return 1
		}
		slugs = append(slugs, blockString(record.Get("project")))
	} else {
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(stderr, "decisions:", blockIOMessage(err))
			return 1
		}
		for _, e := range entries {
			if m := decisionsSlug.FindStringSubmatch(e.Name()); m != nil && strings.HasSuffix(e.Name(), ".jsonl") && !slices.Contains(slugs, m[1]) {
				slugs = append(slugs, m[1])
			}
		}
	}
	keys := map[string][]byte{}
	out := []any{}
	for _, slug := range slugs {
		all, err := ledger.ReadAllRows(dir, slug)
		if err != nil {
			fmt.Fprintln(stderr, "decisions:", blockIOMessage(err))
			return 1
		}
		for _, v := range all {
			r, ok := v.(map[string]any)
			if !ok || r["event"] != "close.decision" {
				continue
			}
			rowRun, _ := r["run"].(string)
			if run != "" && rowRun != run {
				continue
			}
			key, seen := keys[rowRun]
			if !seen {
				key, _ = blockclose.ReadKey(home, rowRun)
				keys[rowRun] = key
			}
			verified := key != nil && gatecheck.Verified(r, key)
			o := ledger.Object{{Key: "run", Value: rowRun}, {Key: "ts", Value: r["ts"]}, {Key: "block", Value: r["block"]}, {Key: "actor", Value: r["actor"]}, {Key: "grant_id", Value: r["grant_id"]}, {Key: "reason", Value: r["reason"]}, {Key: "rule_breaks", Value: decisionsList(r["rule_breaks"])}, {Key: "findings", Value: decisionsList(r["findings"])}, {Key: "verified", Value: verified}}
			out = append(out, o)
		}
	}
	if asJSON {
		data, _ := ledger.Stringify(out, false)
		fmt.Fprintln(stdout, string(data))
		return 0
	}
	if len(out) == 0 {
		fmt.Fprintln(stdout, "decisions: none")
		return 0
	}
	for _, v := range out {
		o := v.(ledger.Object)
		what := []string{}
		for _, h := range o.Get("rule_breaks").([]any) {
			m, _ := h.(map[string]any)
			what = append(what, fmt.Sprintf("rule_break %v line %v", m["rule"], m["line"]))
		}
		for _, f := range o.Get("findings").([]any) {
			m, _ := f.(map[string]any)
			what = append(what, fmt.Sprintf("waived %v %v (%v, %v)", m["file"], m["finding"], m["severity"], m["decision_id"]))
		}
		mark := ""
		if o.Get("verified") != true {
			mark = " UNVERIFIED (no valid MAC)"
		}
		fmt.Fprintf(stdout, "%v %v %v by %v (grant %v): %s; reason: %v%s\n", o.Get("ts"), o.Get("run"), o.Get("block"), o.Get("actor"), o.Get("grant_id"), strings.Join(what, "; "), o.Get("reason"), mark)
	}
	return 0
}

func decisionsList(v any) []any {
	list, _ := v.([]any)
	if list == nil {
		return []any{}
	}
	return list
}
