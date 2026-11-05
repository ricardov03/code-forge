package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/proof"
	"github.com/ricardov03/code-forge/native/state"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

func gatesObj(v any) ledger.Object { o, _ := v.(ledger.Object); return o }
func gatesAdd(o *ledger.Object, fields ledger.Object) {
	for _, f := range fields {
		o.Set(f.Key, f.Value)
	}
}
func gatesSay(o ledger.Object, secrets []string) {
	b, _ := ledger.Stringify(gates.Redact(o, secrets), false)
	fmt.Fprintln(os.Stdout, gates.Mask(string(b), secrets))
}
func gatesError(e error) (int, bool) { fmt.Fprintln(os.Stderr, "gates run:", e); return 1, true }
func gatesNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, e := n.Float64()
		return f, e == nil
	case float64:
		return n, true
	}
	return 0, false
}
func gatesNative(args []string, o gatesOptions) (int, bool) {
	home := os.Getenv("HOME")
	record, readErr := state.ReadRun(home, o.run)
	block := gatesObj(gatesObj(record.Get("blocks")).Get(o.block))
	if block.Get("kind") == "light" {
		return 0, false
	}
	cwd := o.cwd
	main := cwd
	var tree *proof.Tree
	if readErr == nil {
		workspace, _ := record.Get("workspace").(string)
		var e error
		tree, e = proof.BlockTree(workspace, block)
		if e != nil {
			return gatesError(fmt.Errorf("block %s of run %s: %s; no gate was run", o.block, o.run, e))
		}
		if tree != nil {
			for _, a := range args {
				if strings.HasPrefix(a, "--cwd=") {
					fmt.Fprintf(os.Stderr, "gates run: block %s of run %s works in its worktree %s, not in %s — drop it (--cwd=<dir> is not read as --cwd); no gate was run\n", o.block, o.run, tree.Workspace, a)
					return 2, true
				}
			}
			if o.cwdGiven && proof.Real(cwd) != proof.Real(tree.Workspace) {
				fmt.Fprintf(os.Stderr, "gates run: block %s of run %s works in its worktree %s, not in --cwd %s — drop --cwd (or give the worktree); no gate was run\n", o.block, o.run, tree.Workspace, cwd)
				return 2, true
			}
			cwd = tree.Workspace
			main = workspace
		}
	}
	loaded, _ := config.Load(main)
	configured, _ := loaded["gates"].(map[string]any)
	if _, ok := configured["repeat_new_tests"]; ok {
		return 0, false
	}
	if _, ok := configured["custom"]; ok {
		return 0, false
	}
	p, safe := gates.Resolve(cwd, configured)
	if !safe {
		return 0, false
	}
	timeout := 300000.0
	if v, ok := configured["timeout_s"]; ok {
		n, number := gatesNumber(v)
		if !number || n < 30 || n > 7200 || n != float64(int(n)) {
			return gatesError(fmt.Errorf("gates.timeout_s must be an integer 30..7200"))
		}
		timeout = n * 1000
	}
	if o.timeoutGiven {
		timeout = o.timeout
	}
	threshold := 10
	if n, ok := gatesNumber(configured["full_suite_threshold_files"]); ok && n >= 0 && n == float64(int(n)) {
		threshold = int(n)
	}
	kind, safe := gatesKind(block)
	if !safe {
		return 0, false
	}
	if kinds, ok := configured["kinds"].(map[string]any); ok {
		for k, v := range kinds {
			if !gates.Has([]string{"code", "docs", "contract"}, k) {
				return 0, false
			}
			a, ok := v.([]any)
			if !ok || len(gates.Strings(a)) != len(a) {
				return 0, false
			}
			for _, name := range gates.Strings(a) {
				if !gates.Has(gates.Order, name) {
					return 0, false
				}
			}
		}
		if names, ok := kinds[kind].([]any); ok {
			p.Kind = kind
			p.Restricted = true
			p.Names = gates.Strings(names)
		}
	} else if configured["kinds"] != nil {
		return 0, false
	}
	// A rerun-enabled test can recover a failure. Decide before any writes or
	// child execution, so a fallback never repeats an already-run command.
	if p.Commands["test"] != nil && (!p.Restricted || gates.Has(p.Names, "test")) {
		if n, ok := gatesNumber(configured["rerun_failed"]); !ok || n != 0 {
			return 0, false
		}
	}
	if v, ok := configured["rerun_failed"]; ok {
		n, is := gatesNumber(v)
		if !is || (n != 0 && n != 1) {
			return 0, false
		}
	}
	listed, listErr := gates.List(cwd)
	if o.changed {
		if listErr != nil {
			return 0, false
		}
		for _, file := range listed {
			if !gates.IsTest(file) && !strings.HasSuffix(file, "Tests.swift") {
				continue
			}
			if !gatesEmptyTest(cwd, file) {
				return 0, false
			}
		}
	}
	// Child spawn errors and cwd errors have platform-specific Node text.
	if st, e := os.Stat(cwd); e != nil || !st.IsDir() {
		return 0, false
	}
	secrets := gates.Masks(p)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	gatesWarn(cwd)
	var results []any
	all := false
	var files ledger.Object
	var changedScope *gates.Scope
	slug := o.slug
	holder, e := proof.WithSlot(home, o.run, o.block, func(r, b ledger.Object) error {
		if !o.slugGiven {
			slug, _ = r.Get("project").(string)
		}
		scope := gates.Scope{Kind: "full", Reason: "no_tests"}
		var current ledger.Object
		if gates.Scoped(p) || o.changed {
			scope = gates.TestScope(b, listed, threshold)
			if listErr != nil {
				scope = gates.Scope{Kind: "full", Reason: "threshold"}
			} else {
				current = gates.Baseline(cwd, b, listed)
			}
			if o.changed {
				rows, e := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
				if e != nil {
					return e
				}
				var old map[string]any
				for _, value := range rows {
					row, ok := value.(map[string]any)
					if !ok {
						continue
					}
					if row["event"] == "gate.done" && row["run"] == o.run && row["block"] == o.block && row["all_ok"] == true {
						wt := gatesObj(b.Get("worktree")).Get("path")
						if row["worktree"] == wt {
							old, _ = row["files"].(map[string]any)
						}
					}
				}
				// Malformed/foreign baselines require Node's full validation.
				if old != nil {
					if len(old) > 200 {
						return fmt.Errorf("unsupported changed baseline")
					}
					for f, v := range old {
						hash, ok := v.(string)
						if !gates.SafeFile(f) || !ok || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(hash) {
							old = nil
							break
						}
					}
				}
				scope = gates.ChangedEmptyGraph(old, current, scope, listed, threshold)
				changedScope = &scope
			}
		}
		var err error
		results, all, err = gates.Run(ctx, p, cwd, o.run, timeout, scope)
		if err != nil {
			return err
		}
		if all && current != nil {
			afterList, e := gates.List(cwd)
			if e == nil {
				after := gates.Baseline(cwd, b, afterList)
				a, _ := ledger.Stringify(current, false)
				z, _ := ledger.Stringify(after, false)
				if string(a) == string(z) {
					files = after
				}
			}
		}
		return nil
	})
	if e != nil {
		return gatesError(e)
	}
	if holder != "" {
		gatesSay(ledger.Object{{Key: "result", Value: "proof.busy"}, {Key: "holder", Value: holder}}, nil)
		return 1, true
	}
	cancelled := ctx.Err() != nil
	all = all && !cancelled
	duration := int64(0)
	for _, v := range results {
		switch n := v.(ledger.Object).Get("duration_ms").(type) {
		case int:
			duration += int64(n)
		case int64:
			duration += n
		}
	}
	owner := ledger.Object{{Key: "run", Value: o.run}, {Key: "block", Value: o.block}}
	if tree != nil {
		owner.Set("worktree", tree.Path)
	}
	appendRow := func(row ledger.Object) error {
		_, e := ledger.AppendObject(filepath.Join(home, ".code-forge", "ledger"), slug, gates.Redact(row, secrets).(ledger.Object), ledger.DefaultRotateAtBytes)
		return e
	}
	summaries := []any{}
	var scoped ledger.Object
	for _, v := range results {
		r := v.(ledger.Object)
		summary := ledger.Object{}
		for _, k := range []string{"gate", "ok", "skipped", "duration_ms"} {
			summary.Set(k, r.Get(k))
		}
		summaries = append(summaries, summary)
		if r.Get("scope") != nil && scoped == nil {
			scoped = gatesScopeOf(r)
		}
		if !cancelled && r.Get("skipped") != true && r.Get("code") == nil {
			fmt.Fprintln(os.Stderr, gates.Mask(fmt.Sprintf("%s: %s", r.Get("gate"), r.Get("message")), secrets))
		}
		if slug != "" && !cancelled && r.Get("skipped") != true && r.Get("ok") != true {
			row := owner.Clone()
			row.Set("event", "gate.red")
			row.Set("gate", r.Get("gate"))
			row.Set("code", r.Get("code"))
			gatesAdd(&row, gatesScopeOf(r))
			row.Set("duration_ms", r.Get("duration_ms"))
			row.Set("total_duration_ms", duration)
			if r.Get("reason") != nil {
				row.Set("reason", r.Get("reason"))
			}
			if e := appendRow(row); e != nil {
				return gatesError(e)
			}
		}
	}
	if slug != "" {
		row := owner.Clone()
		event := "gate.done"
		if cancelled {
			event = "gate.cancelled"
		}
		row.Set("event", event)
		row.Set("stack", p.Stack)
		row.Set("all_ok", all)
		if cancelled {
			row.Set("cancelled", true)
		}
		row.Set("duration_ms", duration)
		row.Set("results", summaries)
		if changedScope != nil {
			gatesAdd(&row, changedScope.Fields(true))
		} else {
			gatesAdd(&row, scoped)
		}
		if files != nil {
			row.Set("files", files)
		}
		if e := appendRow(row); e != nil {
			return gatesError(e)
		}
	}
	out := ledger.Object{{Key: "stack", Value: p.Stack}}
	if tree != nil {
		out.Set("worktree", tree.Path)
	}
	out.Set("results", results)
	out.Set("allOk", all)
	out.Set("duration_ms", duration)
	if cancelled {
		out.Set("cancelled", true)
	}
	if p.Restricted && len(p.Names) == 0 {
		out.Set("message", "0 gates for kind "+kind)
	}
	gatesSay(out, secrets)
	if all {
		return 0, true
	}
	return 1, true
}
func gatesScopeOf(r ledger.Object) ledger.Object {
	s, _ := r.Get("scope").(string)
	o := ledger.Object{}
	if s == "" {
		return o
	}
	o.Set("scope", s)
	if s == "full" {
		o.Set("reason", r.Get("reason"))
	} else {
		o.Set("test_count", r.Get("test_count"))
		if s == "changed" && r.Get("reason") != nil {
			o.Set("reason", r.Get("reason"))
		}
	}
	return o
}
func gatesKind(b ledger.Object) (string, bool) {
	if s, ok := b.Get("kind").(string); ok && gates.Has([]string{"code", "docs", "contract"}, s) {
		return s, true
	}
	files := gates.Strings(b.Get("owned_files"))
	if len(files) == 0 {
		return "code", true
	}
	kind := "docs"
	for _, f := range files {
		lower := strings.ToLower(f)
		ext := filepath.Ext(lower)
		if gates.Has([]string{".md", ".mdx", ".markdown", ".rst", ".adoc"}, ext) {
			continue
		}
		contract := false
		for _, suffix := range []string{".schema.json", ".schema.yaml", ".schema.yml", ".proto", ".graphql", ".gql", ".avsc", ".openapi.json", ".openapi.yaml", ".openapi.yml"} {
			if strings.HasSuffix(lower, suffix) {
				contract = true
			}
		}
		if gates.Has([]string{".json", ".yaml", ".yml"}, ext) {
			for _, d := range strings.Split(filepath.Dir(lower), "/") {
				if gates.Has([]string{"schema", "schemas", "contract", "contracts"}, d) {
					contract = true
				}
			}
		}
		if !contract {
			return "code", true
		}
		kind = "contract"
	}
	return kind, true
}
func gatesEmptyTest(cwd, file string) bool {
	b, e := os.ReadFile(filepath.Join(cwd, file))
	if e != nil {
		return false
	}
	s := strings.TrimSpace(string(b))
	return s == "" || regexp.MustCompile(`(?s)^(/\*.*?\*/\s*|//[^\n]*(\n|$)\s*)*$`).MatchString(s)
}
func gatesWarn(cwd string) {
	dirs := []string{proof.Real(cwd)}
	if out, e := gitx.Run(cwd, "rev-parse", "--show-toplevel", "--git-common-dir"); e == nil {
		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		if len(lines) > 0 {
			dirs = append(dirs, proof.Real(lines[0]))
		}
		if len(lines) > 1 {
			common := lines[1]
			if !filepath.IsAbs(common) {
				common = filepath.Join(cwd, common)
			}
			dirs = append(dirs, proof.Real(filepath.Dir(common)))
		}
	}
	seen := map[string]bool{}
	for _, dir := range dirs {
		if seen[dir] {
			continue
		}
		seen[dir] = true
		b, e := os.ReadFile(filepath.Join(dir, "package.json"))
		if e != nil {
			continue
		}
		var pkg map[string]any
		if json.Unmarshal(b, &pkg) != nil {
			continue
		}
		deps := false
		for _, k := range []string{"dependencies", "devDependencies"} {
			d, _ := pkg[k].(map[string]any)
			if len(d) > 0 {
				deps = true
			}
		}
		if deps {
			if st, e := os.Stat(filepath.Join(dir, "node_modules")); e != nil || !st.IsDir() {
				fmt.Fprintf(os.Stderr, "warn: %s has package.json but no node_modules; run npm ci there first\n", dir)
			}
		}
	}
}
