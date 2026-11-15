package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// B690b: `npm run known-fix -- check` (scripts/known-fix.mjs) is gone with
// src/. The built-in table data/known-fixes.json is now the only copy, and
// this test is its check: an array of entries {fp, fixed_in, summary, issue},
// each fingerprint once.

var (
	b690bFixFP      = regexp.MustCompile(`^[0-9a-f]{12}$`)
	b690bFixVersion = regexp.MustCompile(`^(0|[1-9]\d{0,5})\.(0|[1-9]\d{0,5})\.(0|[1-9]\d{0,5})$`)
	b690bFixPlain   = regexp.MustCompile(`[\x00-\x1f\x7f-\x9f\x{2028}\x{2029}\x{202a}-\x{202e}\x{2066}-\x{2069}]`)
)

// b690bKnownFixProblems is validateKnownFixes of src/util/known-fixes.mjs.
func b690bKnownFixProblems(data []byte) []string {
	var table []json.RawMessage
	if err := json.Unmarshal(data, &table); err != nil {
		return []string{"the table must be a JSON array"}
	}
	var problems []string
	seen := map[string]int{}
	for i, raw := range table {
		add := func(p string) { problems = append(problems, fmt.Sprintf("entry %d: %s", i+1, p)) }
		var e map[string]any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&e); err != nil || e == nil {
			add("not an object")
			continue
		}
		for k := range e {
			if k != "fp" && k != "fixed_in" && k != "summary" && k != "issue" {
				add(fmt.Sprintf("unknown key %q", k))
			}
		}
		fp, _ := e["fp"].(string)
		if !b690bFixFP.MatchString(fp) {
			add("fp must be 12 lowercase hex characters")
		} else if first, dup := seen[fp]; dup {
			add(fmt.Sprintf("fp is already listed in entry %d", first))
		} else {
			seen[fp] = i + 1
		}
		if v, _ := e["fixed_in"].(string); !b690bFixVersion.MatchString(v) {
			add("fixed_in must be a version x.y.z")
		}
		summary, ok := e["summary"].(string)
		switch {
		case !ok || strings.TrimSpace(summary) == "":
			add("summary must be a non-empty string")
		case summary != strings.TrimSpace(summary) || b690bFixPlain.MatchString(summary):
			add("summary must be one plain line")
		case utf8.RuneCountInString(summary) > 200:
			add("summary must be at most 200 characters")
		}
		issue, has := e["issue"]
		if n, isNum := issue.(json.Number); !has || (issue != nil && (!isNum || !b690bPositiveInt(n))) {
			add("issue must be a positive issue number or null")
		}
	}
	return problems
}

func b690bPositiveInt(n json.Number) bool {
	v, err := n.Int64()
	return err == nil && v > 0 && v <= 1<<53-1
}

func TestB690bKnownFixesTableIsValid(t *testing.T) {
	if problems := b690bKnownFixProblems(logsKnownFixes); len(problems) != 0 {
		t.Fatalf("data/known-fixes.json:\n  %s", strings.Join(problems, "\n  "))
	}
}

func TestB690bKnownFixesCheckRefusesBadEntries(t *testing.T) {
	good := `{"fp":"0123456789ab","fixed_in":"0.4.0","summary":"review no longer hangs.","issue":42}`
	for name, tc := range map[string]struct {
		table string
		want  string
	}{
		"good":       {"[" + good + "]", ""},
		"null issue": {`[{"fp":"0123456789ab","fixed_in":"0.4.0","summary":"s","issue":null}]`, ""},
		"not array":  {`{}`, "the table must be a JSON array"},
		"bad fp":     {`[{"fp":"0123","fixed_in":"0.4.0","summary":"s","issue":null}]`, "entry 1: fp must be 12 lowercase hex characters"},
		"duplicate":  {"[" + good + "," + good + "]", "entry 2: fp is already listed in entry 1"},
		"version":    {`[{"fp":"0123456789ab","fixed_in":"v1","summary":"s","issue":null}]`, "entry 1: fixed_in must be a version x.y.z"},
		"two lines":  {`[{"fp":"0123456789ab","fixed_in":"0.4.0","summary":"a\nb","issue":null}]`, "entry 1: summary must be one plain line"},
		"no issue":   {`[{"fp":"0123456789ab","fixed_in":"0.4.0","summary":"s"}]`, "entry 1: issue must be a positive issue number or null"},
		"zero issue": {`[{"fp":"0123456789ab","fixed_in":"0.4.0","summary":"s","issue":0}]`, "entry 1: issue must be a positive issue number or null"},
		"extra key":  {`[{"fp":"0123456789ab","fixed_in":"0.4.0","summary":"s","issue":null,"x":1}]`, `entry 1: unknown key "x"`},
	} {
		got := strings.Join(b690bKnownFixProblems([]byte(tc.table)), "|")
		if got != tc.want {
			t.Errorf("%s: problems %q, want %q", name, got, tc.want)
		}
	}
}
