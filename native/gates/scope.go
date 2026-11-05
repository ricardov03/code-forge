// Package gates implements the native built-in gate runner and scope expansion.
package gates

import (
	"crypto/sha256"
	"fmt"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

func Strings(v any) []string {
	out := []string{}
	if a, ok := v.([]any); ok {
		for _, v := range a {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}
func Has(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}

var testName = regexp.MustCompile(`\.(test|spec)\.[^/]+$|Test\.php$|_test\.go$|^test_.*\.py$`)

func IsTest(p string) bool {
	parts := strings.Split(p, "/")
	for _, d := range parts[:len(parts)-1] {
		if Has([]string{"test", "tests", "__tests__", "spec"}, d) {
			return true
		}
	}
	return testName.MatchString(parts[len(parts)-1])
}

type Scope struct {
	Kind, Reason               string
	Files, Changed, Dependents []string
}

func (s Scope) Fields(dependents bool) ledger.Object {
	o := ledger.Object{{Key: "scope", Value: s.Kind}}
	if s.Kind == "full" {
		o.Set("reason", s.Reason)
	} else {
		o.Set("test_count", len(s.Files))
		if s.Kind == "changed" && dependents {
			o.Set("dependents", s.Dependents)
		}
		if s.Reason != "" {
			o.Set("reason", s.Reason)
		}
	}
	return o
}
func List(cwd string) ([]string, error) {
	out, e := gitx.Run(cwd, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if e != nil {
		return nil, e
	}
	set := map[string]bool{}
	for _, f := range strings.Split(out, "\x00") {
		if f != "" {
			set[f] = true
		}
	}
	a := []string{}
	for f := range set {
		a = append(a, f)
	}
	gitx.SortStrings(a)
	return a, nil
}
func Count(entries []any, kinds ledger.Object, listed []string) int {
	covered := map[string]bool{}
	missing := 0
	for _, entry := range entries {
		hit := false
		for _, f := range listed {
			if owned.OwnsFile([]any{entry}, kinds, f) {
				covered[f] = true
				hit = true
			}
		}
		if !hit {
			missing++
		}
	}
	return len(covered) + missing
}
func TestScope(block ledger.Object, listed []string, threshold int) Scope {
	entries, _ := block.Get("owned_files").([]any)
	kinds, _ := block.Get("owned_kinds").(ledger.Object)
	if Count(entries, kinds, listed) > threshold {
		return Scope{Kind: "full", Reason: "threshold"}
	}
	files := Strings(block.Get("test_files"))
	if len(files) == 0 {
		for _, f := range Strings(block.Get("owned_files")) {
			if owned.IsExactEntry(f) && !strings.HasSuffix(f, "/") && IsTest(f) {
				files = append(files, f)
			}
		}
	}
	if len(files) == 0 {
		return Scope{Kind: "full", Reason: "no_tests"}
	}
	return Scope{Kind: "related", Files: files}
}
func SafeFile(f string) bool {
	if f == "" || strings.HasPrefix(f, "-") || filepath.IsAbs(f) || strings.ContainsAny(f, "\\\x00") {
		return false
	}
	for _, s := range strings.Split(f, "/") {
		if s == "" || s == "." || s == ".." {
			return false
		}
	}
	return !regexp.MustCompile(`[\x00-\x1f]`).MatchString(f)
}
func Baseline(cwd string, block ledger.Object, listed []string) ledger.Object {
	entries, _ := block.Get("owned_files").([]any)
	kinds, _ := block.Get("owned_kinds").(ledger.Object)
	files := ledger.Object{}
	root, e := filepath.EvalSymlinks(cwd)
	if e != nil {
		return nil
	}
	for _, f := range listed {
		if !owned.OwnsFile(entries, kinds, f) {
			continue
		}
		if !SafeFile(f) {
			return nil
		}
		p := filepath.Join(root, f)
		st, e := os.Lstat(p)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil || !st.Mode().IsRegular() || len(files) >= 200 {
			return nil
		}
		r, e := filepath.EvalSymlinks(p)
		if e != nil || !strings.HasPrefix(r, root+string(filepath.Separator)) {
			return nil
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return nil
		}
		files.Set(f, fmt.Sprintf("%x", sha256.Sum256(b)))
	}
	return files
}

// ChangedEmptyGraph handles repositories whose tests have no executable JS.
// The CLI preflight delegates other graphs to Node before taking the slot.
func ChangedEmptyGraph(old map[string]any, current ledger.Object, fallback Scope, listed []string, threshold int) Scope {
	s := Scope{Kind: "changed", Files: []string{}, Changed: []string{}, Dependents: []string{}}
	fallbackFiles := func() {
		if fallback.Kind == "related" {
			for _, f := range fallback.Files {
				if Has(listed, f) {
					s.Files = append(s.Files, f)
				}
			}
		}
	}
	if current == nil || old == nil {
		fallbackFiles()
		s.Reason = "no_baseline"
		if fallback.Reason == "threshold" {
			s.Reason = "threshold"
			s.Files = []string{}
		}
		return s
	}
	changes := []string{}
	keys := map[string]bool{}
	for k := range old {
		keys[k] = true
	}
	for _, f := range current {
		keys[f.Key] = true
	}
	for f := range keys {
		if old[f] != current.Get(f) {
			changes = append(changes, f)
		}
	}
	sort.Strings(changes)
	if len(changes) > threshold {
		s.Reason = "threshold"
		return s
	}
	for _, f := range changes {
		if Has(listed, f) {
			s.Changed = append(s.Changed, f)
		}
		if IsTest(f) || strings.HasSuffix(f, "Tests.swift") {
			if Has(listed, f) {
				s.Files = append(s.Files, f)
			}
			continue
		}
		paired := false
		for _, candidate := range listed {
			if pair(f, candidate) {
				s.Files = append(s.Files, candidate)
				paired = true
			}
		}
		if !paired {
			fallbackFiles()
			s.Reason = "unpaired"
			s.Files = unique(s.Files)
			return s
		}
	}
	if len(s.Files) == 0 {
		fallbackFiles()
		s.Reason = "no_changes"
	}
	s.Files = unique(s.Files)
	if len(s.Files) > threshold {
		s.Files = []string{}
		s.Reason = "threshold"
	}
	return s
}
func unique(files []string) []string {
	out := []string{}
	sort.Strings(files)
	for _, f := range files {
		if len(out) == 0 || out[len(out)-1] != f {
			out = append(out, f)
		}
	}
	return out
}
func pair(a, b string) bool {
	ext := filepath.Ext(a)
	if ext != filepath.Ext(b) || a == b || IsTest(a) == IsTest(b) {
		return false
	}
	stem := func(f string) string {
		n := strings.TrimSuffix(filepath.Base(f), ext)
		switch ext {
		case ".mjs", ".ts":
			n = strings.TrimSuffix(strings.TrimSuffix(n, ".test"), ".spec")
		case ".php":
			n = strings.TrimSuffix(n, "Test")
		case ".go":
			n = strings.TrimSuffix(n, "_test")
		case ".py":
			n = strings.TrimPrefix(strings.TrimSuffix(n, "_test"), "test_")
		case ".swift":
			n = strings.TrimSuffix(n, "Tests")
		default:
			return ""
		}
		return n
	}
	return stem(a) != "" && stem(a) == stem(b)
}
func RunnerFiles(argv, files []string) []string {
	ext := ""
	switch filepath.Base(argv[0]) {
	case "node", "bun", "deno", "npm", "npx", "pnpm", "yarn", "bunx", "vitest", "jest", "mocha":
		ext = `\.(js|mjs|cjs|jsx|ts|mts|cts|tsx)$`
	case "go":
		ext = `\.go$`
	case "php", "pest", "phpunit":
		ext = `\.php$`
	case "pytest", "python", "python3":
		ext = `\.py$`
	case "cargo":
		ext = `\.rs$`
	case "swift", "xcodebuild":
		ext = `\.swift$`
	default:
		return files
	}
	r := regexp.MustCompile(ext)
	out := []string{}
	for _, f := range files {
		if r.MatchString(f) {
			out = append(out, f)
		}
	}
	return out
}
