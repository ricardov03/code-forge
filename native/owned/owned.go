// Package owned implements the registry's deliberately small owned-path syntax.
package owned

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/ledger"
)

// OwnsFile mirrors registry.mjs: only *, ? and { make an entry a glob;
// exact paths cover descendants unless their recorded kind is file.
func OwnsFile(owned []any, kinds ledger.Object, file string) bool {
	for _, value := range owned {
		entry, ok := value.(string)
		if !ok || entry == "" {
			continue
		}
		if !strings.ContainsAny(entry, "*?{") {
			root := strings.TrimRight(entry, "/")
			if root != "" && (root == file || kinds.Get(entry) != "file" && strings.HasPrefix(file, root+"/")) {
				return true
			}
			continue
		}
		for _, pattern := range braces(entry) {
			if pattern == file || strings.ContainsAny(pattern, "*?") && glob(strings.Split(file, "/"), strings.Split(pattern, "/")) {
				return true
			}
		}
	}
	return false
}

// PathKind uses lstat: even a symlink to a regular file stays directory-like.
func PathKind(root, entry string) string {
	st, err := os.Lstat(filepath.Join(root, filepath.FromSlash(entry)))
	if err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return "absent"
		}
		return "other"
	}
	if st.Mode().IsRegular() {
		return "file"
	}
	if st.IsDir() {
		return "dir"
	}
	return "other"
}

func IsExactEntry(entry string) bool { return !strings.ContainsAny(entry, "*?{") }

// Kinds records only exact entries, preserving their insertion order.
func Kinds(root string, entries []any) ledger.Object {
	out := ledger.Object{}
	for _, value := range entries {
		if entry, ok := value.(string); ok && IsExactEntry(entry) {
			out.Set(entry, PathKind(root, entry))
		}
	}
	return out
}

func glob(file, pattern []string) bool {
	if len(pattern) == 0 {
		return len(file) == 0
	}
	if pattern[0] == "**" {
		for i := 0; i <= len(file); i++ {
			if glob(file[i:], pattern[1:]) {
				return true
			}
		}
		return false
	}
	return len(file) > 0 && segment(utf16.Encode([]rune(file[0])), utf16.Encode([]rune(pattern[0]))) && glob(file[1:], pattern[1:])
}

// Node's non-unicode RegExp matches UTF-16 units, including for ?.
func segment(file, pattern []uint16) bool {
	if len(pattern) == 0 {
		return len(file) == 0
	}
	if pattern[0] == '*' {
		for i := 0; i <= len(file); i++ {
			if segment(file[i:], pattern[1:]) {
				return true
			}
		}
		return false
	}
	return len(file) > 0 && (pattern[0] == '?' || pattern[0] == file[0]) && segment(file[1:], pattern[1:])
}

func braces(pattern string) []string {
	open := strings.IndexByte(pattern, '{')
	if open < 0 {
		return []string{pattern}
	}
	depth, close := 0, -1
	bounds := []int{open}
	for i := open; i < len(pattern); i++ {
		switch pattern[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				close = i
			}
		case ',':
			if depth == 1 {
				bounds = append(bounds, i)
			}
		}
		if close >= 0 {
			break
		}
	}
	if close < 0 {
		return []string{pattern}
	}
	bounds = append(bounds, close)
	result := []string{}
	for i := 0; i < len(bounds)-1; i++ {
		result = append(result, braces(pattern[:open]+pattern[bounds[i]+1:bounds[i+1]]+pattern[close+1:])...)
	}
	return result
}
