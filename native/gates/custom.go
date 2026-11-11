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

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
)

// ResolveCustom is ResolveCustomErr without the message.
func ResolveCustom(p *Plan, configured map[string]any) bool {
	return ResolveCustomErr(p, configured) == nil
}

// ResolveCustomErr adds gates.custom to the plan. A custom gate Node refuses
// is refused with Node's customGateMessages text, joined by "; " (B450).
func ResolveCustomErr(p *Plan, configured map[string]any) error {
	if messages := CustomMessages(configured); len(messages) > 0 {
		return errors.New(strings.Join(messages, "; "))
	}
	items, _ := configured["custom"].([]any)
	for i, item := range items {
		m := item.(map[string]any)
		name := m["name"].(string)
		key := fmt.Sprintf("custom[%d] (%s)", i, config.SafeSegment(name))
		c, problems := command(key, map[string]any{"argv": m["argv"], "env": m["env"]})
		if len(problems) > 0 {
			return errors.New(strings.Join(problems, "; "))
		}
		patterns := []string{}
		if a, ok := m["evidence"].([]any); ok {
			patterns = Strings(a)
		}
		p.Custom = append(p.Custom, Custom{name, c, patterns})
	}
	return nil
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

// RepeatFiles is Node's changedTestFiles (B450): the owned changed tests
// since the block's base that still exist, plus every runnable test that
// reaches an owned changed source through the Go import graph (stopped at
// Repeat.Stop, as the --changed selection is).
func RepeatFiles(cwd string, r *Repeat) ([]string, error) {
	if r.Base == "" || strings.HasPrefix(r.Base, "-") {
		return nil, fmt.Errorf("repeat_new_tests: the block has no recorded base commit (base_sha), so its new tests are unknown; drop gates.repeat_new_tests or reopen the block")
	}
	diff, e := gitx.Run(cwd, "diff", "--name-only", "-z", r.Base)
	if e != nil {
		return nil, fmt.Errorf("repeat_new_tests: cannot list the files changed since %s: %s", r.Base, strings.TrimSpace(e.Error()))
	}
	status, e := gitx.Run(cwd, "status", "--porcelain", "-z", "--untracked-files=all")
	if e != nil {
		return nil, fmt.Errorf("repeat_new_tests: cannot list the untracked files: %s", strings.TrimSpace(e.Error()))
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
	sources := []string{}
	for _, f := range unique(all) {
		if f == "" || !owned.OwnsFile(entries, kinds, f) {
			continue
		}
		if !InChangedTestTree(f) {
			sources = append(sources, f)
			continue
		}
		// A helper, fixture or script under test/ is not repeated (B394).
		if !IsTestFile(f) {
			continue
		}
		st, e := os.Lstat(filepath.Join(cwd, f))
		if e == nil && st.Mode().IsRegular() {
			files = append(files, f)
		} else if e != nil && !os.IsNotExist(e) && !errors.Is(e, syscall.ENOTDIR) {
			return nil, fmt.Errorf("repeat_new_tests: %s", e)
		}
	}
	if len(sources) > 0 {
		listed, e := List(cwd)
		if e != nil {
			return nil, fmt.Errorf("repeat_new_tests: cannot list the repository files: %s", strings.TrimSpace(e.Error()))
		}
		graph := NewGraph(cwd, listed)
		graph.SetStop(r.Stop)
		for _, f := range graph.Dependents(sources) {
			if InChangedTestTree(f) && IsTestFile(f) {
				files = append(files, f)
			}
		}
	}
	return unique(files), nil
}
