package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/proof"
	"github.com/ricardov03/code-forge/native/state"
	"io"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

func gatesObj(v any) ledger.Object { o, _ := v.(ledger.Object); return o }

// Each public route is one process invocation. Keep the last complete stderr
// write, matching captureStderr, and publish logging only after preflight accepts.
var gatesLastMessage any

func gatesErrf(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	if strings.TrimSpace(s) != "" {
		gatesLastMessage = strings.TrimSpace(s)
	}
	fmt.Fprint(os.Stderr, s)
}
func gatesFinish(args []string, code int) {
	before := gatesBreadcrumb()
	if code != 0 {
		gatesFailure(args, code, gatesLastMessage, before)
	}
}
func gatesScrubber(slug string) (func(string) string, func() []any) {
	tally := map[string]int{}
	type step struct {
		rule, from, to string
		re             *regexp.Regexp
		left, right    bool
	}
	steps := []step{{rule: "op_ref", to: "op://<ref>", re: regexp.MustCompile("op://(?:[^/\\r\\n\"'`<>]+/[^/\\r\\n\"'`<>]+/)?[^\\s\"'`<>)\\]]+")}}
	cwd, _ := os.Getwd()
	home := os.Getenv("HOME")
	paths := []step{}
	seen := map[string]bool{}
	for _, p := range []struct{ rule, path, to string }{{"project_path", cwd, "<project>"}, {"home", home, "~"}} {
		for _, v := range []string{p.path, proof.Real(p.path)} {
			v = strings.TrimRight(v, "/\\")
			if len(v) < 2 || seen[v] || (p.rule == "project_path" && proof.Real(cwd) == proof.Real(home)) {
				continue
			}
			seen[v] = true
			paths = append(paths, step{rule: p.rule, from: v, to: p.to, right: true})
		}
	}
	sort.SliceStable(paths, func(i, j int) bool { return len(paths[i].from) > len(paths[j].from) })
	steps = append(steps, paths...)
	steps = append(steps, step{rule: "email", to: "<email>", re: regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)})
	if slug != "" {
		steps = append(steps, step{rule: "project_name", from: slug, to: "<slug>", left: true, right: true})
	}
	steps = append(steps, step{rule: "item_id", to: "<item-id>", re: regexp.MustCompile(`[a-z0-9]{26}`), left: true, right: true})
	word := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
	}
	clean := func(s string) string {
		for _, step := range steps {
			r := step.re
			if r == nil {
				r = regexp.MustCompile(regexp.QuoteMeta(step.from))
			}
			matches := r.FindAllStringIndex(s, -1)
			for i := len(matches) - 1; i >= 0; i-- {
				a, b := matches[i][0], matches[i][1]
				if step.left && a > 0 && (word(s[a-1]) || step.rule == "project_name" && s[a-1] == '<') {
					continue
				}
				if step.right && b < len(s) && (word(s[b]) || step.rule == "project_name" && s[b] == '>') {
					continue
				}
				if step.rule == "project_name" && strings.HasPrefix(s[b:], "://") {
					continue
				}
				s = s[:a] + step.to + s[b:]
				tally[step.rule]++
			}
		}
		return s
	}
	counts := func() []any {
		out := []any{}
		for _, rule := range []string{"home", "project_path", "project_name", "email", "op_ref", "item_id"} {
			if tally[rule] > 0 {
				out = append(out, ledger.Object{{Key: "rule", Value: rule}, {Key: "count", Value: tally[rule]}})
			}
		}
		return out
	}
	return clean, counts
}
func gatesBreadcrumb() []any { return gatesBreadcrumbName("gates run") }

func gatesBreadcrumbName(name string) []any {
	if !reviewWaitLogging() || os.Getenv("HOME") == "" {
		return []any{}
	}
	dir := filepath.Join(os.Getenv("HOME"), ".code-forge", "logs")
	file := filepath.Join(dir, "breadcrumbs.json")
	before := []any{}
	if data, err := os.ReadFile(file); err == nil {
		_ = json.Unmarshal(data, &before)
	}
	next := []any{}
	for _, v := range before {
		if s, ok := v.(string); ok && reviewWaitCrumb.MatchString(s) {
			next = append(next, s)
		}
	}
	if len(next) > 5 {
		next = next[len(next)-5:]
	}
	previous := append([]any{}, next...)
	next = append(next, name)
	if len(next) > 5 {
		next = next[len(next)-5:]
	}
	if os.MkdirAll(dir, 0700) != nil {
		return previous
	}
	_ = os.Chmod(dir, 0700)
	tmp := file + ".tmp-" + strconv.Itoa(os.Getpid())
	defer os.Remove(tmp)
	data, _ := ledger.Stringify(next, false)
	if os.WriteFile(tmp, append(data, '\n'), 0600) == nil {
		_ = os.Rename(tmp, file)
	}
	return previous
}
func gatesFailure(args []string, code int, message any, before []any) {
	nativeVerbFailure("gates", "run", args, code, message, before)
}

func nativeVerbFailure(verbName, subName string, args []string, code int, message any, before []any) {
	if !reviewWaitLogging() || os.Getenv("HOME") == "" {
		return
	}
	cleaned := []any{}
	slug := ""
	var loadedConfig map[string]any
	if cwd, err := os.Getwd(); err == nil {
		if loaded, err := config.LoadFile(filepath.Join(cwd, ".code-forge.yml")); err == nil {
			loadedConfig = loaded
			if project, ok := loadedConfig["project"].(map[string]any); ok {
				slug, _ = project["slug"].(string)
			}
		}
	}
	clean, counts := gatesScrubber(slug)
	if text, ok := message.(string); ok {
		text = clean(text)
		if len(text) > 2048 {
			end := 2048
			for !utf8.RuneStart(text[end]) {
				end--
			}
			text = text[:end]
		}
		message = text
	}
	flags := []any{}
	afterLong := false
	for _, arg := range args {
		if arg == "--" {
			break
		}
		if m := reviewWaitLogFlag.FindStringSubmatch(arg); m != nil {
			flags = append(flags, clean("--"+m[1]))
			afterLong = m[2] == ""
			continue
		}
		if m := reviewWaitLogShort.FindStringSubmatch(arg); m != nil && !afterLong {
			flags = append(flags, clean("-"+m[1]))
		}
		afterLong = false
	}
	verb := clean(verbName)
	cleaned = counts()
	kind := "error"
	if code == 2 {
		kind = "usage"
	}
	normalized, _ := message.(string)
	normalized = reviewWaitLogQuotes.ReplaceAllString(normalized, " ")
	normalized = reviewWaitLogPlaceholders.ReplaceAllString(normalized, " ")
	normalized = reviewWaitLogPaths.ReplaceAllString(normalized, " ")
	normalized = strings.TrimSpace(reviewWaitLogWords.ReplaceAllString(normalized, " "))
	if verbName == "block" && subName == "" {
		normalized = landFingerprintMessage(blockString(message))
	}
	fp := sha256.Sum256([]byte(verb + "\n" + subName + "\n" + kind + "\n" + normalized))
	var sub any
	if subName != "" {
		sub = subName
	}
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	entry := ledger.Object{
		{Key: "ts", Value: time.Now().UTC().Format("2006-01-02T15:04:05.000Z")},
		{Key: "version", Value: version}, {Key: "node", Value: reviewWaitNodeVersion},
		{Key: "platform", Value: runtime.GOOS}, {Key: "arch", Value: arch},
		{Key: "verb", Value: verb}, {Key: "sub", Value: sub}, {Key: "flags", Value: flags},
		{Key: "exit", Value: code}, {Key: "kind", Value: kind}, {Key: "message", Value: message},
		{Key: "stack", Value: nil}, {Key: "cleaned", Value: cleaned},
		{Key: "fp", Value: fmt.Sprintf("%x", fp[:6])}, {Key: "before", Value: before},
	}
	data, err := ledger.Stringify(entry, false)
	if err != nil {
		return
	}
	file := filepath.Join(os.Getenv("HOME"), ".code-forge", "logs", "errors.jsonl")
	if os.MkdirAll(filepath.Dir(file), 0700) != nil {
		return
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	_, err = f.Write(append(data, '\n'))
	_ = f.Close()
	if err != nil {
		return
	}
	info, err := os.Stat(file)
	if err != nil || info.Size() <= 1024*1024 {
		return
	}
	all, err := os.ReadFile(file)
	if err != nil {
		return
	}
	lines := []string{}
	for _, line := range strings.Split(string(all), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	used, start := int64(0), len(lines)
	for start > 0 {
		size := int64(len(lines[start-1]) + 1)
		if start < len(lines) && used+size > info.Size()/2 {
			break
		}
		used += size
		start--
	}
	tmp := file + ".tmp-" + strconv.Itoa(os.Getpid())
	defer os.Remove(tmp)
	if os.WriteFile(tmp, []byte(strings.Join(lines[start:], "\n")+"\n"), 0600) == nil {
		_ = os.Rename(tmp, file)
	}
}
func gatesAdd(o *ledger.Object, fields ledger.Object) {
	for _, f := range fields {
		o.Set(f.Key, f.Value)
	}
}
func gatesSay(o ledger.Object, secrets []string) {
	b, _ := ledger.Stringify(gates.Redact(o, secrets), false)
	fmt.Fprintln(os.Stdout, gates.Mask(string(b), secrets))
}
func gatesError(e error) (int, bool) { gatesErrf("gates run: %s\n", e); return 1, true }
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
func gatesNative(args []string, o gatesOptions) (int, bool) { return gatesNativeStep(args, o, nil) }

func gatesNativeStep(args []string, o gatesOptions, step *landStep, reasons ...*handoverReason) (int, bool) {
	stdout, stderr := step.writers()
	gatesErrf := func(format string, args ...any) {
		if step == nil {
			gatesErrf(format, args...)
			return
		}
		fmt.Fprintf(stderr, format, args...)
	}
	gatesError := func(e error) (int, bool) { gatesErrf("gates run: %s\n", e); return 1, true }
	gatesSay := func(o ledger.Object, secrets []string) {
		b, _ := ledger.Stringify(gates.Redact(o, secrets), false)
		fmt.Fprintln(stdout, gates.Mask(string(b), secrets))
	}
	if os.Getenv("CODE_FORGE_TMP_ROOT") == "" {
		here, _ := os.Getwd()
		routerConfig, _ := config.Load(here)
		tmp, _ := routerConfig["tmp"].(map[string]any)
		if root, ok := tmp["root"].(string); ok && root != "" {
			return 0, false
		}
	}
	home := os.Getenv("HOME")
	var record ledger.Object
	var readErr error
	if o.run != "" {
		record, readErr = state.ReadRun(home, o.run)
	}
	if readErr != nil {
		var stateErr *state.Error
		if !errors.As(readErr, &stateErr) {
			return 0, false
		}
	} else if o.run != "" {
		if record.Get("run_id") != o.run {
			return 0, false
		}
		if _, ok := record.Get("workspace").(string); !ok {
			return 0, false
		}
	}
	block := gatesObj(gatesObj(record.Get("blocks")).Get(o.block))
	light := block.Get("kind") == "light"
	cwd := o.cwd
	main := cwd
	var tree *proof.Tree
	if readErr == nil && o.run != "" {
		workspace, _ := record.Get("workspace").(string)
		var e error
		tree, e = proof.BlockTree(workspace, block)
		if e != nil {
			var commandError *gitx.CommandError
			var gitError *gitx.Error
			if errors.As(e, &commandError) || errors.As(e, &gitError) {
				return 0, false
			}
			return gatesError(fmt.Errorf("block %s of run %s: %s; no gate was run", o.block, o.run, e))
		}
		if tree != nil {
			for _, a := range args {
				if strings.HasPrefix(a, "--cwd=") {
					gatesErrf("gates run: block %s of run %s works in its worktree %s, not in %s — drop it (--cwd=<dir> is not read as --cwd); no gate was run\n", o.block, o.run, tree.Workspace, a)
					return 2, true
				}
			}
			if o.cwdGiven && proof.Real(cwd) != proof.Real(tree.Workspace) {
				gatesErrf("gates run: block %s of run %s works in its worktree %s, not in --cwd %s — drop --cwd (or give the worktree); no gate was run\n", o.block, o.run, tree.Workspace, cwd)
				return 2, true
			}
			cwd = tree.Workspace
			main = workspace
		}
	}
	loaded, _ := config.Load(main)
	configured, _ := loaded["gates"].(map[string]any)
	p, safe := gates.Resolve(cwd, configured)
	if !safe {
		return 0, false
	}
	if !gates.ResolveCustom(&p, configured) {
		return 0, false
	}
	// B335: {test_classes} in a fixed gate's argv takes the block's own test
	// classes from gates.test_class_map; an empty filter drops the item.
	classRules, classErr := gates.ParseClassMap(configured["test_class_map"])
	if classErr != nil {
		return gatesError(classErr)
	}
	if _, e := gates.WithClasses(p, ""); e != nil {
		return gatesError(e)
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
	if n, ok := gatesNumber(configured["full_suite_threshold_files"]); ok && n >= float64(math.MaxInt64) {
		return 0, false
	}
	if v, ok := configured["rerun_failed"]; ok {
		n, is := gatesNumber(v)
		if !is || (n != 0 && n != 1) {
			return 0, false
		}
		p.Rerun = n != 0
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
				custom := false
				for _, c := range p.Custom {
					if c.Name == name {
						custom = true
					}
				}
				if !gates.Has(gates.Order, name) && !custom {
					return 0, false
				}
				if value, present := configured[name]; present && value == nil {
					return 0, false
				}
			}
		}
		if names, ok := kinds[kind].([]any); ok && !light && o.run != "" {
			p.Kind = kind
			p.Restricted = true
			p.Names = gates.Strings(names)
		}
	} else if configured["kinds"] != nil {
		return 0, false
	}
	if light {
		scope := gates.TestScope(block, nil, math.MaxInt)
		c := p.Commands["test"]
		if scope.Kind != "related" || c == nil || !gates.Has(c.Argv, "{test_files}") {
			return gatesError(fmt.Errorf("light-tests: block tests and a gates.test {test_files} item are required"))
		}
		p.Light = scope.Files
		o.changed = false
	}
	if v, exists := configured["repeat_new_tests"]; exists && o.run != "" {
		n, ok := gatesNumber(v)
		if !ok || n < 2 || n > 10 || n != math.Trunc(n) {
			return 0, false
		}
		base, _ := block.Get("base_sha").(string)
		if wt := gatesObj(block.Get("worktree")); wt.Get("base_sha") != nil {
			base, _ = wt.Get("base_sha").(string)
		}
		p.Repeat = &gates.Repeat{N: int(n), Base: base, Block: block, OnSkip: func(s string) { gatesErrf("%s\n", s) }}
		if c := p.Commands["test"]; c != nil && (!p.Restricted || gates.Has(p.Names, "test")) && gates.Has(c.Argv, "{test_files}") {
			if _, safe := gates.RepeatFiles(cwd, p.Repeat); !safe {
				return 0, false
			}
		}
	}
	// Preserve platform-specific spawn diagnostics in Node. Resolve against the
	// child's cwd and PATH, including configured environment overrides.
	for name, command := range p.Commands {
		if command != nil && (!p.Restricted || gates.Has(p.Names, name)) && !gatesExecutable(cwd, command) {
			return 0, false
		}
	}
	for _, c := range p.Custom {
		if (!p.Restricted || gates.Has(p.Names, c.Name)) && !gatesExecutable(cwd, c.Command) {
			return 0, false
		}
	}
	var listed []string
	var listErr error
	if o.run != "" && (gates.Scoped(p) || o.changed) {
		listed, listErr = gates.List(cwd)
		if listErr != nil {
			return 0, false
		}
	}
	// B399/B404: --changed never hands over to Node and never widens to the
	// full suite. A block with no Node product file keeps the go_only reason;
	// every other block gets its tests from the Go import graph
	// (gates_select.go). An explicit --full runs the full suite.
	goOnly := o.changed && o.run != "" && gatesGoOnly(block, p)
	selected := o.changed && !o.full && o.run != "" && listErr == nil && gatesSelectable(p)
	// Child spawn errors and cwd errors have platform-specific Node text.
	if st, e := os.Stat(cwd); e != nil || !st.IsDir() {
		return 0, false
	}
	if step != nil && step.preflight {
		return 0, true
	}
	secrets := gates.Masks(p)
	started := false
	failure := func(e error) (int, bool) {
		if started {
			return gatesError(fmt.Errorf("%s", gates.Mask(e.Error(), secrets)))
		}
		return gatesError(e)
	}
	parent := context.Background()
	if step != nil && step.ctx != nil {
		parent = step.ctx
	}
	ctx, cancel := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if step == nil {
		gatesWarn(cwd)
	} else {
		gatesWarn(cwd, stderr)
	}
	var results []any
	all := false
	var files ledger.Object
	var changedScope *gates.Scope
	base := ""
	slug := o.slug
	withSlot := func(fn func(ledger.Object, ledger.Object) error) (string, error) {
		if o.run == "" {
			return "", fn(nil, nil)
		}
		return proof.WithSlot(home, o.run, o.block, fn)
	}
	holder, e := withSlot(func(r, b ledger.Object) error {
		if !o.slugGiven {
			slug, _ = r.Get("project").(string)
		}
		scope := gates.Scope{Kind: "full", Reason: "no_tests"}
		var current ledger.Object
		treeStamp, fullStamp := "", ""
		var delta []string
		if o.run != "" && (gates.Scoped(p) || o.changed) {
			scope = gates.TestScope(b, listed, threshold)
			if listErr != nil {
				scope = gates.Scope{Kind: "full", Reason: "threshold"}
			} else {
				current = gates.Baseline(cwd, b, listed)
			}
			if o.changed {
				base = gates.MergeBase(cwd, b)
				if base != "" {
					var err error
					delta, err = gates.BaseDelta(cwd, base, b)
					if err != nil {
						return err
					}
					scope = gates.TestScopeCount(b, len(delta), threshold)
				}
				rows, e := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
				if e != nil {
					return e
				}
				var old map[string]any
				var previous map[string]any
				for _, value := range rows {
					row, ok := value.(map[string]any)
					if !ok {
						continue
					}
					if row["event"] == "gate.done" && row["run"] == o.run && row["block"] == o.block && row["all_ok"] == true {
						wt := gatesObj(b.Get("worktree")).Get("path")
						if row["worktree"] == wt {
							previous = row
							old, _ = row["files"].(map[string]any)
						}
					}
				}
				// Test pairing follows the current base; text-only eligibility must
				// still account for code changes since the last green run.
				textBaseline := old
				textRejected, forcedFull := false, false
				if base != "" && current != nil && previous["test_base"] != base {
					paths := append([]string{}, delta...)
					for _, file := range current {
						paths = append(paths, file.Key)
					}
					old = gates.BaseHashes(cwd, base, paths)
				}
				if previous == nil {
					textBaseline = old
				}
				// Malformed/foreign baselines require Node's full validation.
				if old != nil {
					if len(old) > 200 {
						old = nil
					}
					for f, v := range old {
						hash, ok := v.(string)
						entries, _ := b.Get("owned_files").([]any)
						kinds, _ := b.Get("owned_kinds").(ledger.Object)
						if !gates.SafeFile(f) || !owned.OwnsFile(entries, kinds, f) || !ok || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(hash) {
							old = nil
							break
						}
					}
					// A rejected baseline never qualifies for the text-only scope.
					if old == nil {
						textBaseline = nil
						textRejected = true
					}
				}
				candidates := []string{}
				for _, file := range listed {
					if _, e := os.Stat(filepath.Join(cwd, file)); e == nil {
						candidates = append(candidates, file)
					}
				}
				scope = gates.ChangedEmptyGraph(old, current, scope, candidates, threshold)
				if base != "" {
					scope.Report = "full"
					if len(scope.Files) > 0 {
						scope.Report = "related"
					}
					if previous == nil && scope.Reason == "" {
						scope.Reason = "no_baseline"
					}
					if current != nil {
						treeStamp = gates.TreeStamp(cwd)
						fullStamp = gatesFullStamp(treeStamp, p, configured, b)
					}
					oldBase, _ := previous["test_base"].(string)
					safeBase := gates.ReusableBaseMove(cwd, oldBase, base)
					forcedFull = oldBase != "" && oldBase != base && !safeBase
					if forcedFull {
						scope.Kind = "full"
						scope.Files = []string{}
						scope.Report, scope.Reason = "full", "no_baseline"
					}
					var prior map[string]any
					if results, ok := previous["results"].([]any); ok {
						for _, result := range results {
							if result, ok := result.(map[string]any); ok && result["gate"] == "test" {
								prior = result
							}
						}
					}
					p.ReuseTest = safeBase && fullStamp != "" && prior["full_stamp"] == fullStamp && prior["ok"] == true && prior["skipped"] != true && p.Repeat == nil && ctx.Err() == nil
					p.ReuseReason = "no_full_baseline"
					if p.ReuseTest {
						p.ReuseReason = "same_tree"
					} else if fullStamp == "" {
						p.ReuseReason = "no_stamp"
					} else if prior["full_stamp"] != nil {
						p.ReuseReason = "tree_changed"
					}
				}
				if textScope, ok := gates.TextOnlyScope(textBaseline, current, b, candidates); ok && !forcedFull && !textRejected && (previous == nil || textBaseline != nil) {
					scope = textScope
					p.ReuseTest = false
				}
				// Reuse eligibility does not widen execution: source base moves
				// above still require full scope, while missing evidence does not.
				changedScope = &scope
			}
		}
		// A text-only skip or a reused full run on the same tree costs less.
		if selected && scope.Reason != "text_only" && !p.ReuseTest {
			changed := delta
			if base == "" {
				changed = gatesOwnedListed(b, listed)
			}
			scope = gatesChangedSelect(cwd, b, listed, changed, goOnly)
			scope.Files = gatesRunnerFiles(p.Commands["test"].Argv, scope.Files)
			changedScope = &scope
		}
		if o.run == "" && o.scope != nil && gatesSelectable(p) {
			scope = *o.scope
			scope.Files = gatesRunnerFiles(p.Commands["test"].Argv, scope.Files)
		}
		if o.full {
			scope = gates.Scope{Kind: "full", Report: "full", Reason: "explicit", Files: []string{}, Changed: []string{}, Dependents: []string{}}
			p.ReuseTest = false
			changedScope = &scope
		}
		var err error
		started = true
		runPlan := p
		// An empty B404 selection skips the test gate; it never runs the
		// unscoped (full) command.
		emptySelection := gatesSelectedReason(scope.Reason) && len(scope.Files) == 0
		if scope.Reason == "text_only" || emptySelection {
			runPlan.Commands = make(map[string]*gates.Command, len(p.Commands))
			for name, command := range p.Commands {
				if name != "test" {
					runPlan.Commands[name] = command
				}
			}
		}
		if gates.HasClasses(runPlan) {
			filter := ""
			if o.run != "" {
				filter = gates.BlockClassFilter(classRules, b)
			}
			if runPlan, err = gates.WithClasses(runPlan, filter); err != nil {
				return err
			}
		}
		// B352: the machine-wide FIFO test queue; full scope waits for the
		// whole machine, changed scope shares slots that fit cores and memory.
		releaseSlot, err := gatesQueueSlot(ctx, runPlan, scope, cwd, o.run, o.block, stderr)
		if err != nil {
			return err
		}
		results, all, err = gates.Run(ctx, runPlan, cwd, o.run, timeout, scope)
		releaseSlot()
		if err != nil {
			return err
		}
		if scope.Reason == "text_only" || emptySelection {
			for i, result := range results {
				r := result.(ledger.Object)
				if r.Get("gate") == "test" {
					gatesAdd(&r, scope.Fields(true))
					results[i] = r
				}
			}
		}
		if all && current != nil {
			afterList, e := gates.List(cwd)
			if e == nil {
				after := gates.Baseline(cwd, b, afterList)
				a, _ := ledger.Stringify(current, false)
				z, _ := ledger.Stringify(after, false)
				if string(a) == string(z) {
					files = after
					if fullStamp != "" && p.Repeat == nil && treeStamp == gates.TreeStamp(cwd) && base == gates.MergeBase(cwd, b) {
						for i, result := range results {
							r := result.(ledger.Object)
							if r.Get("gate") == "test" && r.Get("ok") == true && r.Get("skipped") != true && r.Get("scope") == "full" && r.Get("flaky") == nil {
								r.Set("full_stamp", fullStamp)
								results[i] = r
							}
						}
					}
				}
			}
		}
		return nil
	})
	if e != nil {
		return failure(e)
	}
	if holder != "" {
		gatesSay(ledger.Object{{Key: "result", Value: "proof.busy"}, {Key: "holder", Value: holder}}, nil)
		return 1, true
	}
	cancelled := ctx.Err() != nil
	all = all && !cancelled
	duration := int64(0)
	var repeats, flaky any
	for _, v := range results {
		r := v.(ledger.Object)
		if repeats == nil && r.Get("repeats") != nil {
			repeats = r.Get("repeats")
		}
		if flaky == nil && r.Get("flaky") != nil {
			flaky = r.Get("flaky")
		}
		switch n := v.(ledger.Object).Get("duration_ms").(type) {
		case int:
			duration += int64(n)
		case int64:
			duration += n
		}
	}
	for _, v := range results {
		r := v.(ledger.Object)
		if !cancelled && r.Get("skipped") != true && r.Get("code") == nil {
			gatesErrf("%s\n", gates.Mask(fmt.Sprintf("%s: %s", r.Get("gate"), r.Get("message")), secrets))
		}
	}
	if flaky != nil && !gates.Has(args, "--json") {
		gatesErrf("flaky: %s\n", strings.Join(flaky.([]string), ", "))
	}
	owner := ledger.Object{{Key: "run", Value: o.run}, {Key: "block", Value: o.block}}
	if tree != nil {
		owner.Set("worktree", tree.Path)
	}
	appendRow := func(row ledger.Object) error {
		if repeats != nil {
			return gatesSignedRow(home, o.run, slug, row)
		}
		_, e := ledger.AppendObject(filepath.Join(home, ".code-forge", "ledger"), slug, row, ledger.DefaultRotateAtBytes)
		return e
	}
	summaries := []any{}
	customs := []any{}
	evidence := ledger.Object{}
	var scoped ledger.Object
	for _, v := range results {
		r := v.(ledger.Object)
		summary := ledger.Object{}
		for _, k := range []string{"gate", "ok", "skipped", "duration_ms"} {
			summary.Set(k, r.Get(k))
		}
		if r.Get("flaky") != nil {
			summary.Set("flaky", r.Get("flaky"))
		}
		for _, key := range []string{"full_stamp", "reused", "reuse_reason"} {
			if value := r.Get(key); value != nil {
				summary.Set(key, value)
			}
		}
		if r.Get("reason") == "text_only" {
			summary.Set("reason", "text_only")
		}
		if r.Get("custom") == true {
			items, _ := r.Get("evidence").([]any)
			entry := ledger.Object{{Key: "name", Value: r.Get("gate")}, {Key: "ok", Value: r.Get("ok")}, {Key: "evidence_count", Value: len(items)}}
			if r.Get("missing") != nil {
				entry.Set("missing", r.Get("missing"))
			}
			customs = append(customs, entry)
			evidence.Set(r.Get("gate").(string), items)
		}
		summaries = append(summaries, summary)
		if r.Get("scope") != nil && scoped == nil {
			scoped = gatesScopeOf(r)
		}
		if slug != "" && !cancelled && r.Get("skipped") != true && r.Get("ok") != true {
			row := owner.Clone()
			row.Set("event", "gate.red")
			row.Set("gate", r.Get("gate"))
			row.Set("code", r.Get("code"))
			gatesAdd(&row, gatesScopeOf(r))
			row.Set("duration_ms", r.Get("duration_ms"))
			row.Set("total_duration_ms", duration)
			if r.Get("custom") == true {
				row.Set("custom", true)
			}
			if r.Get("reason") != nil {
				row.Set("reason", r.Get("reason"))
			}
			if r.Get("repeats") != nil {
				row.Set("repeats", r.Get("repeats"))
			}
			if e := appendRow(row); e != nil {
				return failure(e)
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
		if base != "" {
			row.Set("test_base", base)
		}
		if p.ReuseTest && scoped != nil {
			gatesAdd(&row, scoped)
		} else if changedScope != nil {
			gatesAdd(&row, changedScope.Fields(true))
			if gatesSelectedReason(changedScope.Reason) {
				row.Set("test_files", changedScope.Files)
			}
		} else {
			gatesAdd(&row, scoped)
		}
		if len(customs) > 0 {
			row.Set("custom", customs)
			row.Set("evidence", evidence)
		}
		if files != nil {
			row.Set("files", files)
		}
		if repeats != nil {
			row.Set("repeats", repeats)
		}
		if flaky != nil {
			row.Set("flaky", flaky)
		}
		if e := appendRow(row); e != nil {
			return failure(e)
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
	if repeats != nil {
		out.Set("repeats", repeats)
	}
	if flaky != nil {
		out.Set("flaky", flaky)
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
	for s != "" {
		if strings.HasPrefix(s, "//") {
			end := strings.IndexByte(s, '\n')
			if end < 0 {
				return true
			}
			s = s[end+1:]
		} else if strings.HasPrefix(s, "/*") {
			end := strings.Index(s[2:], "*/")
			if end < 0 {
				return false
			}
			s = s[end+4:]
		} else {
			return false
		}
		s = strings.TrimSpace(s)
	}
	return true
}
func gatesExecutable(cwd string, c *gates.Command) bool {
	return c.Argv[0] == "{test_files}" || c.Argv[0] == "{changed_files}" || gates.Executable(cwd, c) != ""
}
func gatesWarn(cwd string, writers ...io.Writer) {
	warn := func(format string, args ...any) { gatesErrf(format, args...) }
	if len(writers) > 0 {
		warn = func(format string, args ...any) { fmt.Fprintf(writers[0], format, args...) }
	}
	dirs := []string{proof.Real(cwd)}
	if out, e := gitx.WithEnv(cwd, os.Environ(), "rev-parse", "--show-toplevel", "--git-common-dir"); e == nil {
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
				warn("warn: %s has package.json but no node_modules; run npm ci there first\n", dir)
			}
		}
	}
}
