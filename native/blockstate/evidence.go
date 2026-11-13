package blockstate

import (
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/contextpack"
)

// Window is one citedEvidence item: {path, start, end, content}.
type Window struct {
	Path       string
	Start, End int
	Content    string
}

// rowPath is ticket.mjs assertRowPath.
func rowPath(value string) bool {
	n := 0
	for _, r := range value {
		n++
		if r >= 0x10000 {
			n++
		}
	}
	if value == "" || n > 4096 || strings.ContainsAny(value, "\x00\\") || strings.HasPrefix(value, "/") {
		return false
	}
	segments := strings.Split(value, "/")
	for _, s := range segments {
		if s == "" || s == "." || s == ".." {
			return false
		}
	}
	top := strings.ToLower(segments[0])
	return top != ".git" && top != ".code-forge"
}

// CitedWindows is agent-waiver.mjs citedEvidence: numbered windows for the
// first ten readable repo citations, ten lines either side, clipped to the
// file and eighty lines.
func CitedWindows(reason, root string) []Window {
	out := []Window{}
	for _, token := range tokens.Split(reason, -1) {
		m := citation.FindStringSubmatch(token)
		if m == nil {
			continue
		}
		first, e1 := strconv.ParseFloat(m[2], 64)
		last := first
		var e2 error
		if m[3] != "" {
			last, e2 = strconv.ParseFloat(m[3], 64)
		}
		if e1 != nil || e2 != nil || first > 9007199254740991 || last > 9007199254740991 || last < first {
			continue
		}
		if !rowPath(m[1]) {
			continue
		}
		b, ok := ReadFile(root, m[1])
		if !ok {
			continue
		}
		text := contextpack.DecodeUTF8(b)
		var lines []string
		if text != "" {
			lines = strings.Split(text, "\n")
			if lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
		}
		if first > float64(len(lines)) {
			continue
		}
		start := max(1, int(first)-10)
		end := min(float64(len(lines)), last+10, float64(start+79))
		parts := []string{}
		for i := start; i <= int(end); i++ {
			parts = append(parts, strconv.Itoa(i)+"| "+lines[i-1])
		}
		out = append(out, Window{Path: m[1], Start: start, End: int(end), Content: strings.Join(parts, "\n")})
		if len(out) == 10 {
			break
		}
	}
	return out
}
