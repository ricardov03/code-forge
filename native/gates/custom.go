package gates

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
)

// ResolveCustom accepts the exact supported command shape. Invalid or exotic
// configurations go back to Node before the proof slot or any command starts.
func ResolveCustom(p *Plan, configured map[string]any) bool {
	v, present := configured["custom"]
	if !present {
		return true
	}
	items, ok := v.([]any)
	if !ok || len(items) > 20 {
		return false
	}
	seen := map[string]bool{}
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			return false
		}
		name, ok := m["name"].(string)
		if !ok || !regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`).MatchString(name) || seen[name] || Has(Order, name) {
			return false
		}
		seen[name] = true
		command := map[string]any{}
		for k, v := range m {
			if k != "name" && k != "evidence" {
				command[k] = v
			}
		}
		// The shared resolver also checks env strings and NULs.
		for k := range command {
			if k != "argv" && k != "env" {
				return false
			}
		}
		resolved, safe := Resolve("", map[string]any{"test": command, "lint": nil, "types": nil, "format": nil})
		if !safe {
			return false
		}
		c := resolved.Commands["test"]
		if c == nil || c.Argv[0] == "" {
			return false
		}
		for _, a := range c.Argv {
			if strings.Contains(a, "{test_files}") || strings.Contains(a, "{changed_files}") {
				return false
			}
		}
		for k := range c.Env {
			if !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(k) {
				return false
			}
		}
		patterns := []string{}
		if v, exists := m["evidence"]; exists {
			a, ok := v.([]any)
			if !ok {
				return false
			}
			patterns = Strings(a)
			if len(patterns) != len(a) {
				return false
			}
			for _, pat := range patterns {
				if pat == "" || strings.HasPrefix(pat, "/") || strings.ContainsAny(pat, "\\\x00") {
					return false
				}
				for _, seg := range strings.Split(pat, "/") {
					if seg == ".." || regexp.MustCompile(`^[A-Za-z]:`).MatchString(seg) {
						return false
					}
				}
			}
		}
		p.Custom = append(p.Custom, Custom{name, c, patterns})
	}
	return true
}

func evidenceMatcher(pattern string) *regexp.Regexp {
	parts := []string{}
	for _, p := range strings.Split(pattern, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	pattern = strings.Join(parts, "/")
	source := ""
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '*' {
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					source += "(?:.*/)?"
				} else {
					source += ".*"
				}
			} else {
				source += "[^/]*"
			}
		} else {
			source += regexp.QuoteMeta(pattern[i : i+1])
		}
	}
	return regexp.MustCompile("^" + source + "$")
}

func collectEvidence(cwd string, patterns []string) (ledger.Object, error) {
	evidence := []any{}
	out := ledger.Object{{Key: "evidence", Value: evidence}}
	if len(patterns) == 0 {
		return out, nil
	}
	root, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		if !evidenceUnavailable(err) {
			return nil, err
		}
		out.Set("missing", patterns)
		return out, nil
	}
	matchers := []*regexp.Regexp{}
	for _, p := range patterns {
		matchers = append(matchers, evidenceMatcher(p))
	}
	matched := []string{}
	err = filepath.WalkDir(root, func(file string, d os.DirEntry, e error) error {
		if e != nil {
			if evidenceUnavailable(e) {
				return nil
			}
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		resolved, e := filepath.EvalSymlinks(file)
		if e != nil {
			if evidenceUnavailable(e) {
				return nil
			}
			return e
		}
		if rel, e := filepath.Rel(root, resolved); e != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(root, file)
		for _, re := range matchers {
			if re.MatchString(rel) {
				matched = append(matched, rel)
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	gitx.SortStrings(matched)
	valid := map[string]bool{}
	for _, f := range matched {
		valid[f] = true
	}
	for i, f := range matched {
		if i >= 200 {
			break
		}
		resolved, e := filepath.EvalSymlinks(filepath.Join(root, f))
		if e != nil {
			if evidenceUnavailable(e) {
				delete(valid, f)
				continue
			}
			return nil, e
		}
		rel, e := filepath.Rel(root, resolved)
		if e != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
			delete(valid, f)
			continue
		}
		handle, e := os.OpenFile(resolved, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if e != nil {
			if evidenceUnavailable(e) {
				delete(valid, f)
				continue
			}
			return nil, e
		}
		st, e := handle.Stat()
		if e != nil {
			handle.Close()
			return nil, e
		}
		if !st.Mode().IsRegular() {
			handle.Close()
			delete(valid, f)
			continue
		}
		hash := sha256.New()
		bytes, e := io.Copy(hash, handle)
		closeErr := handle.Close()
		if e != nil {
			if evidenceUnavailable(e) {
				delete(valid, f)
				continue
			}
			return nil, e
		}
		if closeErr != nil {
			return nil, closeErr
		}
		evidence = append(evidence, ledger.Object{{Key: "path", Value: f}, {Key: "bytes", Value: bytes}, {Key: "sha256", Value: fmt.Sprintf("%x", hash.Sum(nil))}})
	}
	out.Set("evidence", evidence)
	missing := []string{}
	for i, p := range patterns {
		found := false
		for f := range valid {
			if matchers[i].MatchString(f) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		out.Set("missing", missing)
	}
	return out, nil
}

func evidenceUnavailable(e error) bool {
	return errors.Is(e, syscall.ENOENT) || errors.Is(e, syscall.ENOTDIR) || errors.Is(e, syscall.EACCES) || errors.Is(e, syscall.ELOOP)
}

// RepeatFiles refuses source-dependent selection in preflight. Test-only changes
// need no JS import graph; other graphs remain an explicitly supported handover.
func RepeatFiles(cwd string, r *Repeat) ([]string, bool) {
	if r.Base == "" || strings.HasPrefix(r.Base, "-") {
		return nil, false
	}
	diff, e := gitx.Run(cwd, "diff", "--name-only", "-z", r.Base)
	if e != nil {
		return nil, false
	}
	status, e := gitx.Run(cwd, "status", "--porcelain", "-z", "--untracked-files=all")
	if e != nil {
		return nil, false
	}
	all := strings.Split(diff, "\x00")
	for _, entry := range strings.Split(status, "\x00") {
		if strings.HasPrefix(entry, "?? ") {
			all = append(all, entry[3:])
		}
	}
	entries, _ := r.Block.Get("owned_files").([]any)
	kinds, _ := r.Block.Get("owned_kinds").(ledger.Object)
	files := []string{}
	for _, f := range all {
		if f == "" || !owned.OwnsFile(entries, kinds, f) {
			continue
		}
		if !IsChangedTest(f) {
			return nil, false
		}
		st, e := os.Lstat(filepath.Join(cwd, f))
		if e == nil && st.Mode().IsRegular() {
			files = append(files, f)
		} else if e != nil && !os.IsNotExist(e) {
			return nil, false
		}
	}
	return unique(files), true
}
