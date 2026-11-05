// Package reviewq implements the caller-side review request rules.
package reviewq

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/owned"
)

func bad(message string) error { return &gitx.Error{Code: "bad-path", Message: message} }
func real(file string) string {
	if p, err := filepath.EvalSymlinks(file); err == nil {
		return p
	}
	p, _ := filepath.Abs(file)
	return p
}
func inside(rel string) bool {
	return rel != "" && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Inside checks metadata only: deletions are reviewable, and a down worker
// must not cause an early content read or a not-regular-file refusal.
func Inside(root, file string) error {
	if file == "" || len(utf16.Encode([]rune(file))) > 4096 {
		return bad("file must be a non-empty relative path")
	}
	if strings.ContainsAny(file, "\x00\\") {
		return bad("file must be a POSIX path")
	}
	if filepath.IsAbs(file) {
		return bad("file must be relative to the repository root, not absolute")
	}
	parts := strings.Split(file, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return bad("file must not contain \"\", \".\" or \"..\" segments")
		}
	}
	if p := strings.ToLower(parts[0]); p == ".git" || p == ".code-forge" {
		return bad("file may not be under " + parts[0] + "/")
	}
	full := filepath.Join(root, file)
	if info, err := os.Lstat(full); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return bad("the path is a symlink")
	}
	rel, err := filepath.Rel(real(root), filepath.Join(real(filepath.Dir(full)), filepath.Base(full)))
	if err != nil || !inside(rel) {
		return bad("the path is outside the repository")
	}
	return nil
}

func Normalize(raw, cwd, root string) (string, error) {
	if raw == "" {
		return "", bad("a path is required")
	}
	if filepath.IsAbs(raw) {
		return "", bad("give the path relative to the current directory, not absolute")
	}
	for _, part := range strings.FieldsFunc(raw, func(c rune) bool { return c == '/' || c == '\\' }) {
		if part == ".." {
			return "", bad("the path may not contain a \"..\" segment")
		}
	}
	joined := filepath.Join(real(cwd), raw)
	rel, err := filepath.Rel(real(root), filepath.Join(real(filepath.Dir(joined)), filepath.Base(joined)))
	if err != nil || !inside(rel) {
		return "", bad("the path is outside the repository")
	}
	rel = filepath.ToSlash(rel)
	return rel, Inside(root, rel)
}

var defaults = []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb", "composer.lock", "Cargo.lock", "Gemfile.lock", "poetry.lock", "uv.lock", "go.sum", "Package.resolved", "Podfile.lock", "pubspec.lock", "mix.lock", "flake.lock"}

// Exclusion returns the first rule. Exact exclude rules match only the file,
// whereas owned exact entries may cover descendants; glob rules share syntax.
func Exclusion(cfg map[string]any, file string) (string, bool) {
	rules := []any{}
	for _, name := range defaults {
		rules = append(rules, "**/"+name)
	}
	if review, ok := cfg["review"].(map[string]any); ok && review["exclude"] != nil {
		var list bool
		rules, list = review["exclude"].([]any)
		if !list {
			return "", false
		}
	}
	for _, value := range rules {
		rule, ok := value.(string)
		if !ok {
			return "", false
		}
		if rule == file || strings.ContainsAny(rule, "*?{") && owned.OwnsFile([]any{rule}, nil, file) {
			return rule, true
		}
	}
	return "", true
}
