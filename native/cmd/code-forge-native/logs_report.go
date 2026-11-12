package main

// `logs report` (B554), the report function in src/cli/logs.mjs: the picked
// errors, the npm version check, the built-in cleaning, the 16 KB cap, the
// secret checks, the preview, then gh (after a search for the same
// fingerprint) or a prefilled issue link. Go runs the --no-ai path with no
// prompt; the AI cleaning session, --with-doctor, a known-fix table, a
// terminal prompt and entry shapes Go cannot print like Node stay in Node.

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/session"
)

// lrAIReportMaxBytes is AI_REPORT_MAX_BYTES in src/session/scrub.mjs:
// 16384 minus the bytes of buildScrubPrompt(”) (15686 today).
const lrAIReportMaxBytes = 15686

const (
	lrPackageName  = "@codedology/code-forge"
	lrMaxURLLength = 7500
	lrNpmTimeout   = 10 * time.Second
	lrGhTimeout    = 60 * time.Second
)

// Seams: the package.json the repository and version come from (Node's
// PACKAGE_JSON, next to src/), the clock of a saved report, the kernel
// release of the OS line, and the children (exec in src/util/exec.mjs).
var (
	lrPackageJSON = func() string {
		exe, err := os.Executable()
		if err != nil {
			return ""
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return ""
		}
		return filepath.Join(filepath.Dir(exe), "..", "..", "..", "package.json")
	}
	lrNow = time.Now
	lrRun = func(argv []string, timeout time.Duration) childResult {
		return runChildHooked(argv, timeout, lrRegisterChild)
	}
	// lrHost is process.platform, os.release() and process.arch ("" = unknown).
	lrHost       = func() (string, string, string) { return runtime.GOOS, lrKernelRelease(), lrArch() }
	lrFixesEmpty = logsEmptyFixes
)

// lrEntry is one error log entry as buildReport reads it.
type lrEntry struct {
	fp, verb, sub, kind, ts, exit string
	version, node, platform, arch string
	flags, before                 []string
	hasBefore, isWarning          bool
	warning, message, stack       string
	hasWarning, hasMessage        bool
	cleaned                       any
}

func (e lrEntry) command() string {
	if e.sub != "" {
		return e.verb + " " + e.sub
	}
	return e.verb
}

func lrStrings(v any) ([]string, bool) {
	list, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, false
		}
		out[i] = s
	}
	return out, true
}

// lrParse reads an entry whose every printed field Go formats like a Node
// template; anything else (no stored fingerprint, an object where text is
// printed) is false and the report stays in Node.
func lrParse(obj ledger.Object) (lrEntry, bool) {
	e := lrEntry{cleaned: obj.Get("cleaned")}
	var ok bool
	if e.fp, ok = obj.Get("fp").(string); !ok || !logsFP.MatchString(e.fp) {
		return e, false
	}
	for _, f := range []struct {
		key string
		dst *string
	}{{"verb", &e.verb}, {"kind", &e.kind}, {"ts", &e.ts}, {"version", &e.version}, {"node", &e.node}, {"platform", &e.platform}, {"arch", &e.arch}} {
		if *f.dst, ok = obj.Get(f.key).(string); !ok {
			return e, false
		}
	}
	switch sub := obj.Get("sub").(type) {
	case nil:
	case string:
		e.sub = sub
	default:
		return e, false
	}
	if _, ok := obj.Get("exit").(float64); !ok {
		return e, false
	}
	e.exit = logsString(obj, "exit")
	if raw, isArray := obj.Get("flags").([]any); isArray {
		if e.flags, ok = lrStrings(raw); !ok {
			return e, false
		}
	}
	if raw, isArray := obj.Get("before").([]any); isArray {
		if e.before, ok = lrStrings(raw); !ok {
			return e, false
		}
		e.hasBefore = true
	}
	e.isWarning = e.kind == "warning"
	e.warning, e.hasWarning = obj.Get("warning").(string)
	switch message := obj.Get("message").(type) {
	case nil:
	case string:
		e.message, e.hasMessage = message, true
	default:
		return e, false
	}
	switch stack := obj.Get("stack").(type) {
	case nil:
	case string:
		e.stack = stack
	case bool:
		if stack {
			return e, false
		}
	default:
		return e, false
	}
	return e, true
}

// lrJSSpace is the ECMAScript \s set.
const lrJSSpace = "\\t\\n\\v\\f\\r \\x{a0}\\x{1680}\\x{2000}-\\x{200a}\\x{2028}\\x{2029}\\x{202f}\\x{205f}\\x{3000}\\x{feff}"

var (
	lrOpRef      = regexp.MustCompile("op://(?:[^/\\r\\n\"'`<>]+/[^/\\r\\n\"'`<>]+/)?[^" + lrJSSpace + "\"'`<>)\\]]+")
	lrEmail      = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	lrRepoURL    = regexp.MustCompile(`github\.com[/:]([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?(?:[/?#]|$)`)
	lrBackticks  = regexp.MustCompile("`+")
	lrFooter     = regexp.MustCompile("\\n### Fingerprint\\n\\nFingerprint: [0-9a-f]{12}\\n(?:\\nOther fingerprints: [0-9a-f, ]+\\n)?\\n(?:<!-- code-forge-fp: [0-9a-f]{12} -->[ \\t]*(?:\\r?\\n|$))+[" + lrJSSpace + "]*$")
	lrVersion    = regexp.MustCompile(`^\d+\.\d+\.\d+(?:[-+][0-9A-Za-z.-]+)?$`)
	lrSemver     = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?(?:\+[0-9A-Za-z.-]+)?$`)
	lrDigits     = regexp.MustCompile(`^\d+$`)
	lrEntropyRun = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)
	lrHexOnly    = regexp.MustCompile(`^[0-9a-fA-F]+$`)
)

func lrIn(c byte, set string) bool { return strings.IndexByte(set, c) >= 0 }

const (
	lrAlnum     = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	lrWordDash  = lrAlnum + "_-"
	lrWordUnder = lrAlnum + "_"
)

// lrScrubber is createScrubber in src/util/error-log.mjs.
type lrScrubber struct {
	paths []lrPathStep
	slug  string
	tally map[string]int
}

type lrPathStep struct{ rule, path, to string }

func lrLen16(s string) int { return len(utf16.Encode([]rune(s))) }

// lrPathVariants is pathVariants: the given form without trailing slashes and
// the real path, at least 2 characters, longest first.
func lrPathVariants(p string) []string {
	if lrLen16(p) < 2 {
		return nil
	}
	out := []string{strings.TrimRight(p, "/\\")}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		if abs, err := filepath.Abs(real); err == nil && abs != out[0] {
			out = append(out, abs)
		}
	}
	kept := []string{}
	for _, v := range out {
		if lrLen16(v) >= 2 {
			kept = append(kept, v)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return lrLen16(kept[i]) > lrLen16(kept[j]) })
	return kept
}

func lrNewScrubber(home, cwd, slug string) *lrScrubber {
	homes := lrPathVariants(home)
	steps := []lrPathStep{}
	for _, c := range lrPathVariants(cwd) {
		isHome := false
		for _, h := range homes {
			isHome = isHome || h == c
		}
		if !isHome {
			steps = append(steps, lrPathStep{"project_path", c, "<project>"})
		}
	}
	for _, h := range homes {
		steps = append(steps, lrPathStep{"home", h, "~"})
	}
	sort.SliceStable(steps, func(i, j int) bool { return lrLen16(steps[i].path) > lrLen16(steps[j].path) })
	return &lrScrubber{paths: steps, slug: slug, tally: map[string]int{}}
}

// lrReplace replaces each match of lit at a position where ok holds, left to
// right, as a global JavaScript replace with lookarounds does.
func lrReplace(s, lit, to string, ok func(a, b int) bool, n *int) string {
	var out strings.Builder
	last, i := 0, 0
	for i <= len(s)-len(lit) {
		j := strings.Index(s[i:], lit)
		if j < 0 {
			break
		}
		a, b := i+j, i+j+len(lit)
		if ok(a, b) {
			out.WriteString(s[last:a])
			out.WriteString(to)
			*n++
			last, i = b, b
			continue
		}
		i = a + 1
	}
	out.WriteString(s[last:])
	return out.String()
}

func (sc *lrScrubber) re(s string, re *regexp.Regexp, rule, to string) string {
	return re.ReplaceAllStringFunc(s, func(string) string {
		sc.tally[rule]++
		return to
	})
}

func (sc *lrScrubber) scrub(text string) string {
	s := sc.re(text, lrOpRef, "op_ref", "op://<ref>")
	for _, p := range sc.paths {
		n := 0
		cur := s
		s = lrReplace(cur, p.path, p.to, func(a, b int) bool { return b >= len(cur) || !lrIn(cur[b], lrWordDash) }, &n)
		sc.tally[p.rule] += n
	}
	s = sc.re(s, lrEmail, "email", "<email>")
	if sc.slug != "" {
		n := 0
		cur := s
		s = lrReplace(cur, sc.slug, "<slug>", func(a, b int) bool {
			if a > 0 && lrIn(cur[a-1], lrWordUnder+"<-") {
				return false
			}
			if b < len(cur) && lrIn(cur[b], lrWordUnder+">-") {
				return false
			}
			return !strings.HasPrefix(cur[b:], "://")
		}, &n)
		sc.tally["project_name"] += n
	}
	// item_id: (?<![A-Za-z0-9])[a-z0-9]{26}(?![A-Za-z0-9])
	var out strings.Builder
	last := 0
	for i := 0; i+26 <= len(s); {
		match := i == 0 || !lrIn(s[i-1], lrAlnum)
		for k := i; match && k < i+26; k++ {
			match = s[k] >= 'a' && s[k] <= 'z' || s[k] >= '0' && s[k] <= '9'
		}
		if match && i+26 < len(s) && lrIn(s[i+26], lrAlnum) {
			match = false
		}
		if !match {
			i++
			continue
		}
		out.WriteString(s[last:i])
		out.WriteString("<item-id>")
		sc.tally["item_id"]++
		last = i + 26
		i = last
	}
	out.WriteString(s[last:])
	return out.String()
}

// lrScrubRules is SCRUB_RULES: the order a summary lists them, with labels.
var lrScrubRules = [][3]string{
	{"home", "home path", "home paths"},
	{"project_path", "project path", "project paths"},
	{"project_name", "project name", "project names"},
	{"email", "email", "emails"},
	{"op_ref", "1Password reference", "1Password references"},
	{"item_id", "1Password item ID", "1Password item IDs"},
}

func (sc *lrScrubber) counts() []any {
	out := []any{}
	for _, r := range lrScrubRules {
		if n := sc.tally[r[0]]; n > 0 {
			out = append(out, map[string]any{"rule": r[0], "count": float64(n)})
		}
	}
	return out
}

type lrCount struct {
	rule  string
	count float64
}

// lrMergeCounts is mergeCounts: SCRUB_RULES first, then rules as first seen.
func lrMergeCounts(lists []any) []lrCount {
	order := []string{}
	total := map[string]float64{}
	for _, r := range lrScrubRules {
		order = append(order, r[0])
		total[r[0]] = 0
	}
	for _, list := range lists {
		items, _ := list.([]any)
		for _, item := range items {
			var rule, count any
			switch v := item.(type) {
			case ledger.Object:
				rule, count = v.Get("rule"), v.Get("count")
			case map[string]any:
				rule, count = v["rule"], v["count"]
			}
			name, ok := rule.(string)
			n, isNum := count.(float64)
			if !ok || !isNum || n != math.Trunc(n) || math.Abs(n) > 9007199254740991 || n <= 0 {
				continue
			}
			if _, seen := total[name]; !seen {
				order = append(order, name)
			}
			total[name] += n
		}
	}
	out := []lrCount{}
	for _, name := range order {
		if total[name] > 0 {
			out = append(out, lrCount{name, total[name]})
		}
	}
	return out
}

func lrDescribeCounts(counts []lrCount) string {
	parts := []string{}
	for _, c := range counts {
		label := strings.ReplaceAll(c.rule, "_", " ")
		for _, r := range lrScrubRules {
			if r[0] == c.rule {
				label = r[2]
				if c.count == 1 {
					label = r[1]
				}
			}
		}
		parts = append(parts, strconv.FormatFloat(c.count, 'f', -1, 64)+" "+label)
	}
	if len(parts) == 0 {
		return "Cleaned: nothing needed cleaning."
	}
	return "Cleaned: " + strings.Join(parts, ", ") + "."
}

// lrSecretRule is one REPORT_SECRET_RULES entry: the lookbehind set (a
// character the match may not follow), the pattern, the lookahead set.
type lrSecretRule struct {
	name, before string
	re           *regexp.Regexp
	after        string
}

var lrSecretRules = func() []lrSecretRule {
	r := func(name, before, pattern, after string) lrSecretRule {
		return lrSecretRule{name, before, regexp.MustCompile(pattern), after}
	}
	shaped := "secret-shaped token"
	return []lrSecretRule{
		r("API key (sk-)", lrWordDash, `sk-[A-Za-z0-9_-]{10,}`, ""),
		r("GitHub token", lrWordUnder, `gh[opsur]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}`, ""),
		r("Slack token", lrWordDash, `xox[abprs]-[A-Za-z0-9-]{10,}`, ""),
		r("AWS access key", lrAlnum, `AKIA[0-9A-Z]{16}`, "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"),
		r("private key", "", `-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`, ""),
		r("JWT", lrWordDash, `eyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}`, ""),
		r(shaped, lrWordDash, `sk-[A-Za-z0-9_-]{10,}`, ""),
		r(shaped, lrWordDash, `xai-[A-Za-z0-9]{20,}`, ""),
		r(shaped, lrWordDash, `xox[baprs]-[A-Za-z0-9-]{10,}`, ""),
		r(shaped, lrWordDash, `gh[opsu]_[A-Za-z0-9]{20,}`, ""),
		r(shaped, lrWordDash, `github_pat_[A-Za-z0-9_]{20,}`, ""),
		r(shaped, lrWordDash, `AKIA[0-9A-Z]{12,}`, ""),
		r(shaped, lrWordDash, `AIza[0-9A-Za-z_-]{20,}`, ""),
		r(shaped, lrWordDash, `eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}`, ""),
	}
}()

// test is RegExp.test with the rule's lookarounds: every match start in turn.
func (r lrSecretRule) test(line string) bool {
	for pos := 0; pos <= len(line); {
		loc := r.re.FindStringIndex(line[pos:])
		if loc == nil {
			return false
		}
		a, b := pos+loc[0], pos+loc[1]
		if (a == 0 || !lrIn(line[a-1], r.before)) && (b >= len(line) || !lrIn(line[b], r.after)) {
			return true
		}
		pos = a + 1
	}
	return false
}

func lrEntropy(s string) float64 {
	order := []rune{}
	freq := map[rune]int{}
	for _, ch := range s {
		if freq[ch] == 0 {
			order = append(order, ch)
		}
		freq[ch]++
	}
	length := float64(lrLen16(s))
	bits := 0.0
	for _, ch := range order {
		p := float64(freq[ch]) / length
		bits -= p * math.Log2(p)
	}
	return bits
}

func lrHighEntropy(line string) bool {
	for _, run := range lrEntropyRun.FindAllString(line, -1) {
		if lrHexOnly.MatchString(run) {
			continue
		}
		classes := 0
		for _, set := range []string{"ABCDEFGHIJKLMNOPQRSTUVWXYZ", "abcdefghijklmnopqrstuvwxyz", "0123456789"} {
			if strings.ContainsAny(run, set) {
				classes++
			}
		}
		if classes >= 2 && lrEntropy(run) >= 4 {
			return true
		}
	}
	return false
}

// lrFindSecret is findSecret in src/util/gh.mjs: the rule and 1-based line.
func lrFindSecret(text string) (string, int, bool) {
	for i, line := range strings.Split(text, "\n") {
		for _, r := range lrSecretRules {
			if r.test(line) {
				return r.name, i + 1, true
			}
		}
		if lrHighEntropy(line) {
			return "high-entropy string", i + 1, true
		}
	}
	return "", 0, false
}

// lrCompareVersions is compareVersions in src/util/known-fixes.mjs.
func lrCompareVersions(a, b string) (float64, bool) {
	pa, pb := lrSemver.FindStringSubmatch(logsTrim(a)), lrSemver.FindStringSubmatch(logsTrim(b))
	if pa == nil || pb == nil {
		return 0, false
	}
	num := func(s string) float64 { f, _ := strconv.ParseFloat(s, 64); return f }
	for i := 1; i <= 3; i++ {
		if d := num(pa[i]) - num(pb[i]); d != 0 {
			return d, true
		}
	}
	// FindStringSubmatch gives "" for an absent group; the group, when present, is never empty.
	if pa[4] == "" || pb[4] == "" {
		switch {
		case pa[4] == pb[4]:
			return 0, true
		case pa[4] == "":
			return 1, true
		}
		return -1, true
	}
	xa, xb := strings.Split(pa[4], "."), strings.Split(pb[4], ".")
	for i := 0; i < len(xa) || i < len(xb); i++ {
		if i >= len(xa) {
			return -1, true
		}
		if i >= len(xb) {
			return 1, true
		}
		na, nb := lrDigits.MatchString(xa[i]), lrDigits.MatchString(xb[i])
		switch {
		case na && nb:
			if d := num(xa[i]) - num(xb[i]); d != 0 {
				return d, true
			}
		case na != nb:
			if na {
				return -1, true
			}
			return 1, true
		case xa[i] != xb[i]:
			if lrLess16(xa[i], xb[i]) {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func lrLess16(a, b string) bool {
	ua, ub := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(ua) && i < len(ub); i++ {
		if ua[i] != ub[i] {
			return ua[i] < ub[i]
		}
	}
	return len(ua) < len(ub)
}

// lrPackage is the package.json Node reads: issueRepo and packageVersion.
func lrPackage() (repo, version string) {
	version = "unknown"
	data, err := os.ReadFile(lrPackageJSON())
	if err != nil {
		return "", version
	}
	var pkg any
	if json.Unmarshal(data, &pkg) != nil {
		return "", version
	}
	obj, _ := pkg.(map[string]any)
	if v, ok := obj["version"].(string); ok {
		version = v
	}
	urlOf := func(v any) string {
		if s, ok := v.(string); ok {
			return s
		}
		if m, ok := v.(map[string]any); ok {
			s, _ := m["url"].(string)
			return s
		}
		return ""
	}
	for _, c := range []string{urlOf(obj["bugs"]), urlOf(obj["repository"])} {
		if m := lrRepoURL.FindStringSubmatch(c); m != nil {
			return m[1] + "/" + m[2], version
		}
	}
	return "", version
}

func lrCell(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\n", " ")
	return strings.ReplaceAll(s, "|", "\\|")
}

func lrFenced(text string) string {
	longest := 2
	for _, run := range lrBackticks.FindAllString(text, -1) {
		if len(run) > longest {
			longest = len(run)
		}
	}
	fence := strings.Repeat("`", longest+1)
	return fence + "text\n" + text + "\n" + fence
}

type lrSystem struct{ version, node, os, latest string }

// lrBuildReport is buildReport (no setup check, no fix notes).
func lrBuildReport(entries []lrEntry, system lrSystem, note string) (string, string, []string) {
	pairs := map[string]bool{}
	for _, e := range entries {
		pairs[e.verb+"\x00"+e.kind] = true
	}
	title := fmt.Sprintf("[error report] %d errors", len(entries))
	if len(pairs) == 1 {
		title = "[error report] " + entries[0].verb + " " + entries[0].kind
	}
	if note == "" {
		note = "(not given)"
	}
	latest := system.latest
	if latest == "" {
		latest = "unknown"
	}
	out := []string{"## code-forge error report", "", "### What you were doing", "", note, "", "### Versions", "",
		"- code-forge: " + system.version, "- latest on npm: " + latest, "- Node: " + system.node, "- OS: " + system.os,
		"", "### Error details", "", "| # | when | command | exit | kind | fingerprint |", "|---|---|---|---|---|---|"}
	for i, e := range entries {
		out = append(out, fmt.Sprintf("| %d | %s | %s | %s | %s | %s |", i+1, lrCell(e.ts), lrCell(e.command()), lrCell(e.exit), lrCell(e.kind), e.fp))
	}
	for i, e := range entries {
		flags := "none"
		if len(e.flags) > 0 {
			quoted := make([]string, len(e.flags))
			for k, f := range e.flags {
				quoted[k] = "`" + f + "`"
			}
			flags = strings.Join(quoted, " ")
		}
		out = append(out, "", fmt.Sprintf("#### %d. %s: %s", i+1, e.command(), e.kind), "", "- when: "+e.ts, "- flags: "+flags)
		if e.hasBefore {
			before := "none"
			if len(e.before) > 0 {
				quoted := make([]string, len(e.before))
				for k, c := range e.before {
					quoted[k] = "`" + c + "`"
				}
				before = strings.Join(quoted, ", ")
			}
			out = append(out, "- commands before (oldest first): "+before)
		}
		out = append(out, "- exit: "+e.exit, "- kind: "+e.kind)
		if e.isWarning && e.hasWarning {
			out = append(out, "- warning: "+e.warning)
		}
		message := "(none)"
		if e.hasMessage {
			message = e.message
		}
		out = append(out, "- fingerprint: "+e.fp, fmt.Sprintf("- code-forge %s, Node %s, %s %s", e.version, e.node, e.platform, e.arch),
			"", "message:", "", lrFenced(message))
		if e.stack != "" {
			out = append(out, "", "stack:", "", lrFenced(e.stack))
		}
	}
	fps := []string{}
	seen := map[string]bool{}
	for _, e := range entries {
		if !seen[e.fp] {
			seen[e.fp] = true
			fps = append(fps, e.fp)
		}
	}
	return title, strings.Join(out, "\n") + "\n", fps
}

func lrFooterText(fps []string) string {
	lines := []string{"", "### Fingerprint", "", "Fingerprint: " + fps[0]}
	if len(fps) > 1 {
		lines = append(lines, "", "Other fingerprints: "+strings.Join(fps[1:], ", "))
	}
	lines = append(lines, "")
	for _, fp := range fps {
		lines = append(lines, "<!-- code-forge-fp: "+fp+" -->")
	}
	return strings.Join(lines, "\n") + "\n"
}

// lrCapBytes is capBytes: the first max bytes, never inside a character.
func lrCapBytes(text string, max int) string {
	if len(text) <= max {
		return text
	}
	end := max
	for end > 0 && text[end]&0xc0 == 0x80 {
		end--
	}
	return text[:end]
}

func lrCapReport(title, body string, max int) string {
	if len(title)+2+len(body) <= max {
		return body
	}
	note := fmt.Sprintf("\n\n(report cut to %d bytes)\n", max)
	room := max - len(title) - 2 - len(note)
	if room < 0 {
		room = 0
	}
	return lrCapBytes(body, room) + note
}

func lrDisclosure(warnings bool) string {
	shared := []string{"code-forge, Node and OS versions", "command names and flag NAMES", "the names of the last commands you ran (no flags or values)",
		"exit codes", "error types", "error messages and crash reports after cleaning"}
	if warnings {
		shared = append(shared, "warnings (problems code-forge recovered from) after cleaning")
	}
	return "What you will share (public on GitHub): " + strings.Join(shared, "; ") + ".\n" +
		"Never shared: flag values, file contents, your code, keys or tokens, your home folder, project folder or project name.\n"
}

// lrEncode is encodeURIComponent.
func lrEncode(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if lrIn(c, lrAlnum+"-_.!~*'()") {
			out.WriteByte(c)
		} else {
			fmt.Fprintf(&out, "%%%02X", c)
		}
	}
	return out.String()
}

func lrIssueURL(repo, title, body, fp string) string {
	return "https://github.com/" + repo + "/issues/new?template=error-report.yml&title=" + lrEncode(title) + "&labels=error-report&fingerprint=" + fp + "&details=" + lrEncode(body)
}

// lrSaveReport is saveReport: ~/.code-forge/logs/report-<ISO time>.md.
func lrSaveReport(body, home string) (string, string, bool) {
	if home == "" {
		return "", "", false
	}
	stamp := strings.NewReplacer(":", "-", ".", "-").Replace(lrNow().UTC().Format("2006-01-02T15:04:05.000Z"))
	name := "report-" + stamp + ".md"
	dir := filepath.Join(home, ".code-forge", "logs")
	if os.MkdirAll(dir, 0o700) != nil {
		return "", "", false
	}
	file := filepath.Join(dir, name)
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return "", "", false
	}
	_, err = f.WriteString(body)
	if f.Close() != nil || err != nil {
		return "", "", false
	}
	return file, name, true
}

// lrPrintLink is printLink: a search link and a prefilled issue link; a body
// too long for a link is cut (its fingerprint section kept) and saved.
func lrPrintLink(out func(string), repo, title, body, fp, home string, alwaysSave bool) {
	link := lrIssueURL(repo, title, body, fp)
	if len(link) > lrMaxURLLength {
		saved, name, ok := lrSaveReport(body, home)
		kept, footer := body, ""
		if loc := lrFooter.FindStringIndex(body); loc != nil {
			footer = strings.TrimRightFunc(body[loc[0]+1:loc[1]], errorLogSpace) + "\n"
			kept = body[:loc[0]]
		}
		tail := "\n\n(report cut)\n"
		if ok {
			tail = "\n\n(report cut; full report saved at ~/.code-forge/logs/" + name + ")\n"
		}
		if footer != "" {
			tail += "\n" + footer
		}
		chars := []rune(kept)
		lo, hi := 0, len(chars)
		for lo < hi {
			mid := (lo + hi + 1) / 2
			if len(lrIssueURL(repo, title, string(chars[:mid])+tail, fp)) <= lrMaxURLLength {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		link = lrIssueURL(repo, title, string(chars[:lo])+tail, fp)
		if ok {
			out("The report is too long for a link: it was cut. Full report saved at " + saved + "\n")
		} else {
			out("The report is too long for a link: it was cut.\n")
		}
	} else if alwaysSave {
		if saved, _, ok := lrSaveReport(body, home); ok {
			out("Full report saved at " + saved + "\n")
		}
	}
	out("Already reported? Search first:\nhttps://github.com/" + repo + "/issues?q=" + fp + "\n")
	out("Open this link to file the issue:\n" + link + "\n")
}

// lrOnPath is pathLookup: an executable regular file in a PATH directory.
func lrOnPath(cmd string) bool {
	for _, dir := range strings.Split(os.Getenv("PATH"), string(os.PathListSeparator)) {
		if dir == "" {
			continue
		}
		file := filepath.Join(dir, cmd)
		if syscall.Access(file, 1) != nil {
			continue
		}
		if info, err := os.Stat(file); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// lrRoot is this process's lazy run root (currentRunRoot): made by the first
// child or body file, removed at the end when no child is registered.
var lrRoot string

// lrMu guards lrRoot's first making: children may start on several
// goroutines (B573).
var lrMu sync.Mutex

func lrProcRoot() (string, error) {
	lrMu.Lock()
	defer lrMu.Unlock()
	if lrRoot != "" {
		return lrRoot, nil
	}
	root, err := contextRunRoot("proc-"+strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixMilli(), 10), nil)
	if err == nil {
		lrRoot = root
		lrLazyRoots = append(lrLazyRoots, root)
	}
	return root, err
}

// lrLazyRoots are the lazy roots this process made (released at exit even
// when a verb has since set its run root, B573).
var lrLazyRoots []string

// lrRegisterChild is exec's pid registry: <run-root>/pids/<pid>.json
// (reaper.mjs registerPid) right after the spawn, removed once the child has
// settled. A run root or entry that cannot be written never fails the spawn,
// and an entry never written is never removed (registry hygiene only).
func lrRegisterChild(pid int, argv0 string) func() {
	root, err := lrProcRoot()
	if err != nil {
		return nil
	}
	dir := filepath.Join(root, "pids")
	if session.RegisterPid(dir, pid, argv0) != nil {
		return nil
	}
	return func() { _ = os.Remove(filepath.Join(dir, strconv.Itoa(pid)+".json")) }
}

// lrExec is exec: the run root first (Node's pid registry lives there).
func lrExec(argv []string, timeout time.Duration) childResult {
	_, _ = lrProcRoot()
	return lrRun(argv, timeout)
}

// lrMkdtemp is fs.mkdtemp (libuv): the prefix plus six random letters or
// digits, mode 0700.
func lrMkdtemp(prefix string) (string, error) {
	var err error
	for try := 0; try < 100; try++ {
		var raw [6]byte
		if _, err = rand.Read(raw[:]); err != nil {
			return "", err
		}
		name := []byte(prefix)
		for _, b := range raw {
			name = append(name, lrAlnum[int(b)%len(lrAlnum)])
		}
		if err = os.Mkdir(string(name), 0o700); err == nil || !os.IsExist(err) {
			return string(name), err
		}
	}
	return "", err
}

// lrLatestOnNpm is latestOnNpm: the latest version, or why it is unknown.
func lrLatestOnNpm() (string, string) {
	res := lrExec([]string{"npm", "view", lrPackageName, "version"}, lrNpmTimeout)
	if !res.ok {
		switch {
		case res.timedOut:
			return "", "timed out"
		case res.code != nil:
			return "", fmt.Sprintf("npm exit %d", *res.code)
		}
		return "", "npm did not run"
	}
	if v := logsTrim(nodeUTF8(res.stdout)); lrVersion.MatchString(v) {
		return v, ""
	}
	return "", "npm gave no version"
}

// lrFindReported is findReported (no match option): an earlier issue with
// the fingerprint in its body, or why the search failed.
func lrFindReported(repo, fp string) (number float64, url, state, why string) {
	res := lrExec([]string{"gh", "issue", "list", "--repo", repo, "--state", "all", "--search", fp + " in:body", "--json", "number,url,state", "--limit", "5"}, lrGhTimeout)
	if !res.ok {
		switch {
		case res.code != nil:
			return 0, "", "", fmt.Sprintf("gh exit %d", *res.code)
		case res.timedOut:
			return 0, "", "", "gh timed out"
		}
		return 0, "", "", "gh did not finish"
	}
	var list any
	if json.Unmarshal([]byte(nodeUTF8(res.stdout)), &list) != nil {
		return 0, "", "", "gh gave no list"
	}
	items, ok := list.([]any)
	if !ok {
		return 0, "", "", "gh gave no list"
	}
	for _, item := range items {
		obj, _ := item.(map[string]any)
		n, isNum := obj["number"].(float64)
		link, isURL := obj["url"].(string)
		if !isNum || n != math.Trunc(n) || math.Abs(n) > 9007199254740991 || !isURL || !strings.HasPrefix(link, "https://") {
			continue
		}
		state := "unknown"
		if s, ok := obj["state"].(string); ok {
			state = strings.ToLower(s)
		}
		return n, link, state, ""
	}
	return 0, "", "", ""
}

// lrGhWithBodyFile is ghWithBodyFile: the text in a private file under this
// process's run root (removed after), the first argv, and the second once
// when the first failed over a label. "" when gh succeeded.
func lrGhWithBodyFile(text string, argvs func(file string) [][]string, onOK func(stdout string)) string {
	root, err := lrProcRoot()
	if err != nil {
		return "could not write the report file"
	}
	dir, err := lrMkdtemp(filepath.Join(root, "report-"))
	if err != nil {
		return "could not write the report file"
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "body.md")
	if os.WriteFile(file, []byte(text), 0o600) != nil {
		return "could not write the report file"
	}
	list := argvs(file)
	res := lrExec(list[0], lrGhTimeout)
	if !res.ok && len(list) > 1 && strings.Contains(strings.ToLower(res.stderr), "label") {
		res = lrExec(list[1], lrGhTimeout)
	}
	if !res.ok {
		switch {
		case res.code != nil:
			return fmt.Sprintf("exit %d", *res.code)
		case res.timedOut:
			return "timed out"
		}
		return "did not finish"
	}
	if onOK != nil {
		onOK(nodeUTF8(res.stdout))
	}
	return ""
}

func lrArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	}
	return ""
}

// logsReport runs `logs report` for the picked entries when Go can do all of
// it like Node; otherwise it hands over before any output or side effect.
func logsReport(args []string, flags map[string]string, newest []ledger.Object, last int64, crumb string) int {
	_, withWarnings := flags["include-warnings"]
	kind, byKind := flags["kind"]
	verb, byVerb := flags["verb"]
	_, noAI := flags["no-ai"]
	_, withDoctor := flags["with-doctor"]
	_, dryRun := flags["dry-run"]
	_, yes := flags["yes"]
	_, allowOld := flags["allow-old"]
	switch {
	case !noAI:
		return logsHandover(args, hLogsReport) // the AI cleaning session stays in Node
	case withDoctor:
		return logsHandover(args, hLogsReportDoctor)
	}
	all := make([]lrEntry, len(newest))
	selected := []lrEntry{}
	for i, obj := range newest {
		e, ok := lrParse(obj)
		if !ok {
			return logsHandover(args, hLogsFormat)
		}
		all[i] = e
		if (withWarnings || !e.isWarning) && (!byKind || e.kind == kind) && (!byVerb || e.verb == verb) && int64(len(selected)) < last {
			selected = append(selected, e)
		}
	}
	tty := logsStdinTTY()
	platform, release, arch := lrHost()
	switch {
	case !lrFixesEmpty():
		return logsHandover(args, hLogsReportKnownFixes)
	case platform == "" || release == "" || arch == "":
		return logsHandover(args, hLogsReportSystem)
	case tty && !dryRun && !(yes && allowOld):
		return logsHandover(args, hLogsReportPrompt)
	case !routerHintReadable():
		return logsHandover(args, hRouterHintState)
	}
	contextApplyTmpRoot()
	lrRoot = ""
	defer func() {
		if lrRoot != "" {
			spawnReleaseRoot(lrRoot)
		}
	}()
	before := gatesBreadcrumbName(crumb)
	out := func(s string) { fmt.Fprint(os.Stdout, s) }
	failed := func(code int, text string) int {
		fmt.Fprint(os.Stderr, text)
		var message any
		if trimmed := strings.TrimFunc(text, errorLogSpace); trimmed != "" {
			message = trimmed
		}
		routerHint(nativeVerbFailure("logs", "report", args, code, message, before), code)
		return code
	}
	repo, pkgVersion := lrPackage()
	if repo == "" {
		return failed(1, "logs: package.json names no GitHub repository to report to\n")
	}
	system := lrSystem{version: pkgVersion, node: nodeRunVersion(), os: platform + " " + release + " " + arch}

	latest, why := lrLatestOnNpm()
	if why != "" {
		out("Could not check npm for a newer code-forge (" + why + "); going on.\n")
	} else if d, ok := lrCompareVersions(latest, system.version); ok && d > 0 {
		out("You use " + system.version + ". " + latest + " is out. Many errors are fixed in newer versions: upgrade with npm install -g " + lrPackageName + "@latest and try again first.\n")
		if !dryRun && !allowOld {
			// a terminal without --allow-old was handed over above
			return failed(2, "logs: an older code-forge than the one on npm; not sent — upgrade first, or pass --allow-old to report from this version\n")
		}
	}

	home := os.Getenv("HOME")
	slug := ""
	if loaded, err := config.LoadFile(filepath.Join(physicalCwd(), ".code-forge.yml")); err == nil {
		if project, ok := loaded["project"].(map[string]any); ok {
			slug, _ = project["slug"].(string)
		}
	}
	scrubber := lrNewScrubber(home, physicalCwd(), slug)
	warnings := false
	for _, e := range selected {
		warnings = warnings || e.isWarning
	}
	shared := lrDisclosure(warnings)
	system.latest = latest
	title, built, fps := lrBuildReport(selected, system, flags["note"])
	title = scrubber.scrub(title)
	body := lrCapReport(title, scrubber.scrub(built), lrAIReportMaxBytes)
	blocked := func() bool {
		rule, line, hit := lrFindSecret(title + "\n" + body)
		if hit {
			out(shared)
		}
		return hit && failed(1, fmt.Sprintf("Possible secret found in the report (%s, report line %d; the title is line 1); not sent. Find it with code-forge logs --json and remove it with code-forge logs clear.\n", rule, line)) == 1
	}
	if blocked() {
		return 1
	}
	body += lrFooterText(fps)
	lists := []any{}
	for _, e := range selected {
		lists = append(lists, e.cleaned)
	}
	cleaned := lrMergeCounts(append(lists, scrubber.counts()))
	if blocked() {
		return 1
	}
	out(shared)
	out(lrDescribeCounts(cleaned) + "\nAI cleaning was skipped (--no-ai); only the built-in cleaning ran.\n\nTitle: " + title + "\n\n" + body + "\n")
	if dryRun {
		out("dry run: not sent\n")
		return 0
	}
	if !yes {
		// a terminal without --yes was handed over above
		return failed(2, "logs: no terminal to confirm; not sent — pass --yes to send the report above\n")
	}

	fp := fps[0]
	ghReady := lrOnPath("gh") && lrExec([]string{"gh", "auth", "status"}, lrGhTimeout).ok
	if !ghReady {
		lrPrintLink(out, repo, title, body, fp, home, false)
		return 0
	}
	number, dupURL, state, why := lrFindReported(repo, fp)
	if why != "" {
		out("could not search for an existing report (" + why + "); creating a new one\n")
	}
	if dupURL != "" {
		out("This error was already reported: " + dupURL + " (" + state + ")\n")
		times := 0
		for _, e := range all {
			if e.fp == fp {
				times++
			}
		}
		latestText := latest
		if latestText == "" {
			latestText = "unknown"
		}
		comment := strings.Join([]string{"This error happened again.", "", "- code-forge: " + system.version, "- latest on npm: " + latestText,
			"- Node: " + system.node, "- OS: " + system.os, fmt.Sprintf("- times in the local log: %d", times), "", "<!-- code-forge-fp: " + fp + " -->", ""}, "\n")
		numberText := strconv.FormatFloat(number, 'f', -1, 64)
		why := lrGhWithBodyFile(comment, func(file string) [][]string {
			return [][]string{{"gh", "issue", "comment", numberText, "--repo", repo, "--body-file", file}}
		}, nil)
		if why == "" {
			out("commented: " + dupURL + "\n")
			return 0
		}
		return failed(1, "gh failed ("+why+"); nothing was sent. Add the comment by hand: "+dupURL+"\n")
	}
	labels := []string{"--label", "error-report"}
	kinds := []string{}
	for _, e := range selected {
		known := false
		for _, k := range kinds {
			known = known || k == e.kind
		}
		if !known && len(kinds) < 3 {
			kinds = append(kinds, e.kind)
		}
	}
	for _, k := range kinds {
		labels = append(labels, "--label", "kind:"+k)
	}
	url := ""
	why = lrGhWithBodyFile(body, func(file string) [][]string {
		base := []string{"gh", "issue", "create", "--repo", repo, "--title", title, "--body-file", file}
		return [][]string{append(append([]string{}, base...), labels...), base}
	}, func(stdout string) {
		for _, line := range strings.Split(stdout, "\n") {
			if line = logsTrim(line); strings.HasPrefix(line, "https://") {
				url = line
			}
		}
	})
	if why == "" {
		if url == "" {
			url = "(gh printed no issue URL)"
		}
		out("sent: " + url + "\n")
		return 0
	}
	out("gh failed (" + why + "); here is a link instead\n")
	lrPrintLink(out, repo, title, body, fp, home, true)
	return 0
}
