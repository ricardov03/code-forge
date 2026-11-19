package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/yamledit"
	"gopkg.in/yaml.v3"
)

// B791: `code-forge config fix` rewrites the .code-forge.yml shapes an older
// release accepted and this one refuses. Each fix is a named rule; the edit
// goes through yamledit (with B781's splice fallback), so comments, blank
// lines and key order stay. A file with YAML anchors, aliases or tags is
// never edited, and an edit that would not read back as the same config
// minus the old shape, or that would add a schema issue, writes nothing.
// Unknown keys are reported, never deleted. `upgrade` runs the fixer on the
// cwd's .code-forge.yml after a successful upgrade and never fails on it.
//
// Rule for later blocks (owner 2026-11-19): a block that removes or renames
// a config key a release accepted adds a rule to cfxRules.

const configUsage = "usage: code-forge config fix [--dry-run] [--file <path>]\n"

func init() {
	routes["config"] = smallVerb{name: "config", usage: configUsage, subs: []string{"fix"},
		subUsage: map[string]string{"fix": configUsage}, prepare: prepareConfigFix}.run
}

// cfxRule is one old shape: whether a loaded config holds it, the text edit
// that removes it, what the config must read as afterwards, a hint about
// the removed value, and the step to do by hand when the text cannot be
// edited safely.
type cfxRule struct {
	name, reason, byHand string
	applies              func(cfg map[string]any) bool
	edit                 func(text string) (string, error)
	expect               func(plain map[string]any)
	hint                 func(cfg map[string]any) string
}

const cfxInvariantsReason = "gates.extra.invariants is refused since 0.22.0 and no engine ever ran its script; removed it"

const cfxInvariantsByHand = "remove gates.extra.invariants from .code-forge.yml; to run its script as a gate, add it to gates.custom as {name, argv}"

var cfxRules = []cfxRule{
	{
		name:   "gates-extra-invariants",
		reason: cfxInvariantsReason,
		byHand: cfxInvariantsByHand,
		applies: func(cfg map[string]any) bool {
			extra, ok := objAt(cfg, "gates", "extra")
			if !ok {
				return false
			}
			_, has := extra["invariants"]
			return has
		},
		// prune 1: a gates.extra left empty goes too; gates keeps its keys.
		edit: func(text string) (string, error) {
			return yamledit.RemoveSpliceFirst(text, []string{"gates", "extra", "invariants"}, 1)
		},
		expect: func(plain map[string]any) {
			gates, _ := plain["gates"].(map[string]any)
			extra, _ := gates["extra"].(map[string]any)
			delete(extra, "invariants")
			if len(extra) == 0 {
				delete(gates, "extra")
			}
		},
		hint: func(cfg map[string]any) string {
			extra, _ := objAt(cfg, "gates", "extra")
			script, ok := extra["invariants"].(string)
			if !ok || script == "" {
				return "to run its script as a gate, add it to gates.custom as {name, argv}"
			}
			return "to run " + strconv.Quote(config.MaskSecrets(script)) + " as a gate, add it to gates.custom as {name, argv}"
		},
	},
}

// cfxResult is one fixer pass over a config text.
type cfxResult struct {
	file          string
	before, after string
	applied       []cfxRule
	hints         []string
	needed        []cfxRule // rules that apply, when the text cannot be edited
	unsafe        string    // why nothing was edited ("" = edited or nothing to do)
	loadErr       string    // the file does not load
	left          []string  // schema issues left in place (unknown keys first named)
}

var cfxUnknownKey = regexp.MustCompile(`must NOT have additional property "((?:[^"\\]|\\.)*)"`)

// cfxIssueLines words the schema issues the fixer leaves: an unknown key by
// its dotted path, any other issue by its schema text.
func cfxIssueLines(cfg map[string]any) []string {
	var out []string
	for _, issue := range config.SchemaIssues(cfg, "") {
		if m := cfxUnknownKey.FindStringSubmatch(issue.Text); m != nil {
			key := m[1]
			if unq, err := strconv.Unquote(`"` + m[1] + `"`); err == nil {
				key = unq
			}
			path := strings.ReplaceAll(strings.TrimPrefix(issue.Path, "/"), "/", ".")
			if path != "" {
				key = path + "." + key
			}
			out = append(out, "unknown key "+config.MaskSecrets(key)+": left in place; remove or rename it by hand")
			continue
		}
		out = append(out, "schema: "+issue.Text+": left in place; fix it by hand")
	}
	return out
}

// cfxUnsafe names a shape the fixer never edits: an explicit tag, an anchor,
// an alias or a << merge key ("" = none).
func cfxUnsafe(text string) string {
	if apdHasTag(text) {
		return "a YAML tag (!!...)"
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return "YAML the fixer cannot read"
	}
	var walk func(n *yaml.Node) bool
	walk = func(n *yaml.Node) bool {
		if n.Anchor != "" || n.Kind == yaml.AliasNode || n.Kind == yaml.ScalarNode && n.Value == "<<" && n.Tag == "!!merge" {
			return true
		}
		for _, c := range n.Content {
			if walk(c) {
				return true
			}
		}
		return false
	}
	if walk(&doc) {
		return "a YAML anchor or alias"
	}
	return ""
}

// cfxRun reads text and works out the fixed text; it writes nothing.
func cfxRun(file, text string) cfxResult {
	r := cfxResult{file: file, before: text, after: text}
	cfg, _, err := config.ParseTextOrdered(text, file)
	if err != nil {
		r.loadErr = config.MaskSecrets(err.Error())
		return r
	}
	for _, rule := range cfxRules {
		if rule.applies(cfg) {
			r.needed = append(r.needed, rule)
		}
	}
	if len(r.needed) == 0 {
		r.left = cfxIssueLines(cfg)
		return r
	}
	refuse := func(why string) cfxResult {
		r.unsafe, r.after, r.applied, r.hints = why, text, nil, nil
		return r
	}
	if why := cfxUnsafe(text); why != "" {
		return refuse(why)
	}
	plain, err := config.ParseYAML(text)
	want, isMap := plain.(map[string]any)
	if err != nil || !isMap {
		return refuse("YAML the fixer cannot read")
	}
	out := text
	for _, rule := range r.needed {
		edited, err := rule.edit(out)
		if err != nil {
			why := err.Error()
			if u, ok := err.(*yamledit.Unsupported); ok {
				why = u.Reason
			}
			return refuse(why)
		}
		out = edited
		rule.expect(want)
		r.applied = append(r.applied, rule)
		r.hints = append(r.hints, rule.hint(cfg))
	}
	// The safety net: the written text loads, reads as the old config minus
	// the old shapes, holds none of them, and adds no schema issue.
	fixed, _, err := config.ParseTextOrdered(out, file)
	if err != nil {
		return refuse("a text that would not load after the edit")
	}
	gotPlain, err := config.ParseYAML(out)
	if err != nil {
		return refuse("a text that would not load after the edit")
	}
	got, err1 := canon.Marshal(gotPlain)
	exp, err2 := canon.Marshal(want)
	if err1 != nil || err2 != nil || string(got) != string(exp) {
		return refuse("a shape the edit would change beyond the old key")
	}
	known := map[string]bool{}
	for _, issue := range config.SchemaIssues(cfg, "") {
		known[issue.Text] = true
	}
	for _, issue := range config.SchemaIssues(fixed, "") {
		if !known[issue.Text] {
			return refuse("a config that would be invalid after the edit")
		}
	}
	for _, rule := range cfxRules {
		if rule.applies(fixed) {
			return refuse("a shape the edit did not remove")
		}
	}
	r.after = out
	r.left = cfxIssueLines(fixed)
	return r
}

// cfxWrite replaces file's bytes through a temp file and a rename, keeping
// its mode; a symlinked config is written at its target.
func cfxWrite(file, text string) error {
	target := file
	if real, err := filepath.EvalSymlinks(file); err == nil {
		target = real
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".code-forge.yml.b791-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, werr := tmp.WriteString(text)
	cerr := tmp.Close()
	if werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(name, info.Mode().Perm())
	}
	if werr == nil {
		werr = os.Rename(name, target)
	}
	if werr != nil {
		_ = os.Remove(name)
	}
	return werr
}

// cfxDiff is a unified diff of two texts with no context lines.
func cfxDiff(file, a, b string) string {
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.SplitAfter(strings.TrimSuffix(s, "\n"), "\n")
	}
	x, y := split(a), split(b)
	for i := range x {
		x[i] = strings.TrimSuffix(x[i], "\n")
	}
	for i := range y {
		y[i] = strings.TrimSuffix(y[i], "\n")
	}
	n, m := len(x), len(y)
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out strings.Builder
	out.WriteString("--- " + file + "\n+++ " + file + " (fixed)\n")
	i, j := 0, 0
	for i < n || j < m {
		if i < n && j < m && x[i] == y[j] {
			i, j = i+1, j+1
			continue
		}
		si, sj := i, j
		for i < n || j < m {
			if i < n && j < m && x[i] == y[j] {
				break
			}
			if j >= m || i < n && lcs[i+1][j] >= lcs[i][j+1] {
				i++
			} else {
				j++
			}
		}
		start := func(s, count int) int {
			if count == 0 {
				return s
			}
			return s + 1
		}
		fmt.Fprintf(&out, "@@ -%d,%d +%d,%d @@\n", start(si, i-si), i-si, start(sj, j-sj), j-sj)
		for _, l := range x[si:i] {
			out.WriteString("-" + l + "\n")
		}
		for _, l := range y[sj:j] {
			out.WriteString("+" + l + "\n")
		}
	}
	return config.MaskSecrets(out.String())
}

func prepareConfigFix(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	usage := func() (func(stdout, stderr io.Writer) int, handoverReason, bool) {
		return func(_, stderr io.Writer) int {
			fmt.Fprint(stderr, configUsage)
			return 2
		}, 0, true
	}
	if len(args) == 0 || args[0] != "fix" {
		return usage()
	}
	cwd := physicalCwd()
	file, dryRun, hasFile := filepath.Join(cwd, config.Filename), false, false
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "--dry-run" && !dryRun:
			dryRun = true
		case args[i] == "--file" && !hasFile:
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "--") {
				return usage()
			}
			file, hasFile = args[i+1], true
			if !filepath.IsAbs(file) {
				file = filepath.Join(cwd, file)
			}
			file = filepath.Clean(file)
			i++
		default:
			return usage()
		}
	}
	return func(stdout, stderr io.Writer) int {
		return cfxCommand(stdout, stderr, file, dryRun)
	}, 0, true
}

func cfxCommand(stdout, stderr io.Writer, file string, dryRun bool) int {
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(stderr, "config fix: no config at %s\n", file)
		} else {
			fmt.Fprintf(stderr, "config fix: cannot read %s: %s\n", file, config.MaskSecrets(err.Error()))
		}
		return 1
	}
	r := cfxRun(file, string(data))
	switch {
	case r.loadErr != "":
		fmt.Fprintf(stderr, "config fix: %s does not load (%s); nothing was changed; fix it by hand, then run code-forge validate\n", file, r.loadErr)
		return 1
	case r.unsafe != "":
		var b strings.Builder
		fmt.Fprintf(&b, "config fix: %s holds %s, so it cannot be edited safely; nothing was changed\n", file, r.unsafe)
		for _, rule := range r.needed {
			b.WriteString("  " + rule.name + ": " + rule.byHand + "\n")
		}
		b.WriteString("  then run: code-forge validate\n")
		fmt.Fprint(stderr, b.String())
		return 1
	}
	var b strings.Builder
	verb := "fixed"
	if dryRun && len(r.applied) > 0 {
		verb = "would fix"
		b.WriteString(cfxDiff(file, r.before, r.after))
	}
	for i, rule := range r.applied {
		b.WriteString(verb + " " + rule.name + " (" + rule.reason + ")\n  " + r.hints[i] + "\n")
	}
	for _, line := range r.left {
		b.WriteString(line + "\n")
	}
	switch {
	case dryRun && len(r.applied) > 0:
		b.WriteString("dry run: nothing was written\n")
	case len(r.applied) > 0:
		if err := cfxWrite(file, r.after); err != nil {
			fmt.Fprint(stdout, b.String())
			fmt.Fprintf(stderr, "config fix: cannot write %s: %s; nothing was changed\n", file, config.MaskSecrets(err.Error()))
			return 1
		}
		b.WriteString(file + ": fixed (" + plural(len(r.applied), "rule") + "); run code-forge validate to check it\n")
	case len(r.left) == 0:
		b.WriteString(file + ": config ok\n")
	}
	fmt.Fprint(stdout, b.String())
	if len(r.left) > 0 {
		return 1
	}
	return 0
}

// cfxUpgradeLines is the fixer pass upgrade runs on the cwd's config: the
// lines it prints, "" when the cwd has no .code-forge.yml. It never fails
// the upgrade: any trouble becomes a line naming the command to run.
func cfxUpgradeLines(cwd string) (lines string) {
	file := filepath.Join(cwd, config.Filename)
	defer func() {
		if recover() != nil {
			lines = "config: not checked; check it with: code-forge config fix --dry-run\n"
		}
	}()
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		return "config: not changed (" + file + " cannot be read); check it with: code-forge validate\n"
	}
	r := cfxRun(file, string(data))
	switch {
	case r.loadErr != "":
		return "config: not changed (" + file + " does not load); check it with: code-forge validate\n"
	case r.unsafe != "":
		return "config: not changed (" + file + " holds " + r.unsafe + "); see what to change with: code-forge config fix --dry-run\n"
	}
	var b strings.Builder
	if len(r.applied) > 0 {
		if err := cfxWrite(file, r.after); err != nil {
			return "config: not changed (" + file + " cannot be written); fix it with: code-forge config fix\n"
		}
		for i, rule := range r.applied {
			b.WriteString("config: fixed " + rule.name + " (" + rule.reason + ")\nconfig: " + r.hints[i] + "\n")
		}
	}
	for _, line := range r.left {
		b.WriteString("config: " + line + "\n")
	}
	if b.Len() == 0 {
		return "config ok\n"
	}
	return b.String()
}
