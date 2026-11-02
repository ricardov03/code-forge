package rebase

import (
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

// ownsFile mirrors registry.mjs: only *, ? and { make an entry a glob;
// exact paths cover descendants unless their recorded kind is file.
func ownsFile(owned []any, kinds ledger.Object, file string) bool {
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
	var body strings.Builder
	for _, c := range pattern[0] {
		switch c {
		case '*':
			body.WriteString("[^/]*")
		case '?':
			body.WriteString("[^/]")
		default:
			body.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return len(file) > 0 && regexp.MustCompile("^"+body.String()+"$").MatchString(file[0]) && glob(file[1:], pattern[1:])
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
