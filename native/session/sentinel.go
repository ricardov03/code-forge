package session

import (
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B549: src/engines/sentinel.mjs parseSentinel with no block id filter, for
// the coder session's `sentinel` result field.

const jsNotWS = `[^\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`

var (
	sentinelComplete = regexp.MustCompile(`^===BLOCK` + jsWS + `+(` + jsNotWS + `+)` + jsWS + `+COMPLETE===$`)
	sentinelFailed   = regexp.MustCompile(`^===BLOCK` + jsWS + `+(` + jsNotWS + `+)` + jsWS + `+FAILED:` + jsWS + `*(` + jsDot + `*?)` + jsWS + `*===$`)
	sentinelTests    = regexp.MustCompile(`^TESTS` + jsWS + `+(` + jsNotWS + jsDot + `*?)` + jsWS + `+exit=(0|[1-9][0-9]{0,2})$`)
)

// sentinelArrayIndex is a key JavaScript orders first (an array index).
func sentinelArrayIndex(key string) (uint64, bool) {
	if key == "" || (len(key) > 1 && key[0] == '0') {
		return 0, false
	}
	n, err := strconv.ParseUint(key, 10, 64)
	if err != nil || n >= 4294967295 {
		return 0, false
	}
	return n, true
}

// sentinelValues is Object.values(decoded) filtered to strings, in JS order.
func sentinelValues(obj ledger.Object) []string {
	type indexed struct {
		n uint64
		v any
	}
	var ints []indexed
	var rest []any
	for _, f := range obj {
		if n, ok := sentinelArrayIndex(f.Key); ok {
			ints = append(ints, indexed{n, f.Value})
		} else {
			rest = append(rest, f.Value)
		}
	}
	sort.SliceStable(ints, func(i, j int) bool { return ints[i].n < ints[j].n })
	var out []string
	for _, x := range ints {
		if s, ok := x.v.(string); ok {
			out = append(out, s)
		}
	}
	for _, v := range rest {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// sentinelDecode is JSON.parse(output): (value, true) for one JSON text.
func sentinelDecode(output string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(output))
	v, err := decodeValue(dec)
	if err != nil {
		return nil, false
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false
	}
	return v, true
}

// ParseSentinel is parseSentinel(output): the last sentinel line, or nil.
func ParseSentinel(output string) any {
	if decoded, ok := sentinelDecode(output); ok {
		switch v := decoded.(type) {
		case string:
			output = v
		case ledger.Object:
			output = strings.Join(sentinelValues(v), "\n")
		}
	}
	var last ledger.Object
	var tests ledger.Object
	for _, raw := range strings.Split(output, "\n") {
		line := trimJS(raw)
		if m := sentinelComplete.FindStringSubmatch(line); m != nil {
			last = ledger.Object{{Key: "status", Value: "complete"}, {Key: "blockId", Value: m[1]}}
			if tests != nil {
				last.Set("tests", tests)
			}
			tests = nil
			continue
		}
		if m := sentinelFailed.FindStringSubmatch(line); m != nil {
			last = ledger.Object{{Key: "status", Value: "failed"}, {Key: "blockId", Value: m[1]}, {Key: "reason", Value: m[2]}}
			if tests != nil {
				last.Set("tests", tests)
			}
			tests = nil
			continue
		}
		if m := sentinelTests.FindStringSubmatch(line); m != nil {
			if n, _ := strconv.Atoi(m[2]); n <= 255 {
				tests = ledger.Object{{Key: "command", Value: m[1]}, {Key: "exit", Value: float64(n)}}
			}
		}
	}
	if last == nil {
		return nil
	}
	return last
}
