package gates

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
)

var baseSHA = regexp.MustCompile(`^[a-f0-9]{40,64}$`)

// MergeBase matches the oracle's distinction between a shared checkout and a
// block branch. Missing Git evidence never authorizes reuse.
func MergeBase(cwd string, block ledger.Object) string {
	branch, _ := block.Get("base_branch").(string)
	if branch == "" {
		return ""
	}
	current, err := gitx.Run(cwd, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil && strings.TrimSpace(current) == branch {
		return ""
	}
	base, err := gitx.Run(cwd, "merge-base", "HEAD", "refs/heads/"+branch)
	base = strings.TrimSpace(base)
	if err != nil || !baseSHA.MatchString(base) {
		return ""
	}
	return base
}

// BaseDelta counts only the block's changes after a rebase, including untracked
// paths and deletions, rather than every owned file inherited from the base.
func BaseDelta(cwd, base string, block ledger.Object) ([]string, error) {
	tracked, err := gitx.Run(cwd, "diff", "--no-renames", "--name-only", "-z", base)
	if err != nil {
		return nil, err
	}
	untracked, err := gitx.Run(cwd, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	entries, _ := block.Get("owned_files").([]any)
	kinds, _ := block.Get("owned_kinds").(ledger.Object)
	files := []string{}
	for _, file := range strings.Split(tracked+untracked, "\x00") {
		if file != "" && owned.OwnsFile(entries, kinds, file) {
			files = append(files, file)
		}
	}
	return unique(files), nil
}

func BaseHashes(cwd, base string, files []string) map[string]any {
	hashes := map[string]any{}
	for _, file := range unique(files) {
		data, err := gitx.Run(cwd, "show", base+":"+file)
		if err == nil {
			hashes[file] = fmt.Sprintf("%x", sha256.Sum256([]byte(data)))
			continue
		}
		listed, err := gitx.Run(cwd, "ls-tree", "-z", base, "--", file)
		if err != nil || listed != "" {
			return nil
		}
	}
	return hashes
}

var documentation = regexp.MustCompile(`^docs/.*\.(md|mdx|rst|txt)$|^(CHANGELOG|README|LICENSE)(\.(md|rst|txt))?$`)

func releaseManifest(file string) bool {
	return Has([]string{"package.json", "npm-shrinkwrap.json", "package-lock.json", ".claude-plugin/plugin.json"}, file)
}

func withoutVersion(data []byte) ([]byte, error) {
	obj, err := ledger.DecodeObject(data)
	if err != nil {
		return nil, err
	}
	obj.Delete("version")
	if packages, ok := obj.Get("packages").(ledger.Object); ok {
		if root, ok := packages.Get("").(ledger.Object); ok {
			root.Delete("version")
			packages.Set("", root)
			obj.Set("packages", packages)
		}
	}
	return canon.Marshal(obj)
}

func ReusableBaseMove(cwd, old, base string) bool {
	if !baseSHA.MatchString(old) {
		return false
	}
	if old == base {
		return true
	}
	diff, err := gitx.Run(cwd, "diff", "--no-renames", "--name-only", "-z", old, base)
	if err != nil {
		return false
	}
	for _, file := range strings.Split(diff, "\x00") {
		if file == "" || documentation.MatchString(file) {
			continue
		}
		if !releaseManifest(file) {
			return false
		}
		before, e1 := gitx.Run(cwd, "show", old+":"+file)
		after, e2 := gitx.Run(cwd, "show", base+":"+file)
		a, e3 := withoutVersion([]byte(before))
		b, e4 := withoutVersion([]byte(after))
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || string(a) != string(b) {
			return false
		}
	}
	return true
}

// TreeStamp is valid only after preflight has proved the dependency graph empty.
// Documentation cannot then be a hidden test input. All other bytes and modes
// are included; symlinks and unreadable inputs fail closed.
func TreeStamp(cwd string) string {
	listed, err := List(cwd)
	if err != nil {
		return ""
	}
	hash := sha256.New()
	for _, file := range listed {
		if documentation.MatchString(file) {
			continue
		}
		info, err := os.Lstat(filepath.Join(cwd, file))
		var header []byte
		if os.IsNotExist(err) {
			header, _ = canon.Marshal([]any{file, "deleted"})
			hash.Write(header)
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return ""
		}
		data, err := os.ReadFile(filepath.Join(cwd, file))
		if err != nil {
			return ""
		}
		if releaseManifest(file) {
			data, err = withoutVersion(data)
			if err != nil {
				return ""
			}
		}
		header, _ = canon.Marshal([]any{file, uint32(info.Mode().Perm() & 0111), len(data)})
		hash.Write(header)
		hash.Write(data)
	}
	return fmt.Sprintf("%x", hash.Sum(nil))
}
