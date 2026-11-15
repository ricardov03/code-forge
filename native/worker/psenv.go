package worker

// B757: the one reader of the ownership markers from a process environment,
// used by the worker's orphan sweep (ReadProcessEnv) and by the run sweep
// (cmd/code-forge-native run_sweep.go). It is Node's readProcessEnv
// (src/util/reaper.mjs): a value stops at a line end.

import (
	"regexp"
	"strconv"
	"strings"
)

// PsEnvHead is the head of one `ps -E -o pid=,ppid=,lstart=,command=` row.
var PsEnvHead = regexp.MustCompile(`^\s*(\d+)\s+(\d+)\s+(\S+\s+\S+\s+\d+\s+\d+:\d+:\d+\s+\d{4})\s+`)

// EnvMarkerNames are the ownership markers, in Node's order.
var EnvMarkerNames = []string{"HOME", "CODE_FORGE_OWNER_PID", "CODE_FORGE_RUN_ID", "CODE_FORGE_OWNER_START_TIME"}

// EnvFromEnviron reads the markers from /proc/<pid>/environ bytes.
func EnvFromEnviron(data string) map[string]string {
	markers := map[string]string{}
	entries := strings.Split(data, "\x00")
	for _, name := range EnvMarkerNames {
		for _, entry := range entries {
			if strings.HasPrefix(entry, name+"=") {
				markers[name] = entry[len(name)+1:]
				break
			}
		}
	}
	return markers
}

// EnvFromPs reads the markers from `ps -E` output whose first row must still
// be the process (pid, ppid, start time, command); nil when it is not.
func EnvFromPs(pid, ppid int, start, command, out string) map[string]string {
	m := PsEnvHead.FindStringSubmatch(out)
	if m == nil || m[1] != strconv.Itoa(pid) || m[2] != strconv.Itoa(ppid) || strings.Join(strings.Fields(m[3]), " ") != start {
		return nil
	}
	text := out[len(m[0]):]
	// ps appends the environment to argv: strip the observed command first.
	if !strings.HasPrefix(text, command+" ") {
		return nil
	}
	// B747: ps ends the row with "\n", the end of the last variable.
	text = strings.TrimSuffix(text[len(command)+1:], "\n")
	markers := map[string]string{}
	for _, name := range EnvMarkerNames {
		if value, ok := EnvMarker(text, name); ok {
			markers[name] = value
		}
	}
	return markers
}

func psenvJSSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', ' ', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

func psenvLineTerminator(r rune) bool { return r == '\n' || r == '\r' || r == 0x2028 || r == 0x2029 }

// EnvMarker is /(?:^|\s)NAME=(.*?)(?=\s+[A-Za-z_][A-Za-z0-9_]*=|$)/ (no
// lookahead in Go's regexp; JS \s, "." stops at a line end, "$" is the text
// end): the first start whose value reaches a terminator wins.
func EnvMarker(text, name string) (string, bool) {
	runes := []rune(text)
	key := []rune(name + "=")
	identStart := func(r rune) bool { return r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') }
	ident := func(r rune) bool { return identStart(r) || (r >= '0' && r <= '9') }
	// next variable: \s+ then an identifier then '='
	nextVar := func(j int) bool {
		k := j
		for k < len(runes) && psenvJSSpace(runes[k]) {
			k++
		}
		if k == j || k >= len(runes) || !identStart(runes[k]) {
			return false
		}
		for k < len(runes) && ident(runes[k]) {
			k++
		}
		return k < len(runes) && runes[k] == '='
	}
	for i := 0; i+len(key) <= len(runes); i++ {
		if i > 0 && !psenvJSSpace(runes[i-1]) {
			continue
		}
		if string(runes[i:i+len(key)]) != string(key) {
			continue
		}
		start := i + len(key)
		for j := start; ; j++ {
			if j == len(runes) || nextVar(j) {
				return string(runes[start:j]), true
			}
			if psenvLineTerminator(runes[j]) {
				break
			}
		}
	}
	return "", false
}
