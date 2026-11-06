// Package blockstate implements read-only checks shared by the block preview verbs.
package blockstate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
)

func Text(v any) string        { s, _ := v.(string); return s }
func Map(v any) map[string]any { m, _ := v.(map[string]any); return m }
func Array(v any) []any        { a, _ := v.([]any); return a }
func Hash(b []byte) string     { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// Trim uses JavaScript's WhiteSpace and LineTerminator sets (not Go's U+0085).
func Trim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		return r == ' ' || r >= '\t' && r <= '\r' || r == 0xa0 || r == 0x1680 || r >= 0x2000 && r <= 0x200a || r == 0x2028 || r == 0x2029 || r == 0x202f || r == 0x205f || r == 0x3000 || r == 0xfeff
	})
}

// Flags matches the shared Node declarations, including the two confirmation verbs.
func Flags(args []string, yes bool) (map[string]any, []string, string) {
	flags, pos := map[string]any{}, []string{}
	values := strings.Fields("run level acceptance brief attempt base lines head worker-pid reason file transcript kind report rule line note proof max")
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			pos = append(pos, arg)
			continue
		}
		name, value, inline := strings.Cut(arg[2:], "=")
		multi := name == "owned" || name == "tests"
		boolean := slices.Contains([]string{"json", "remove-worktree", "no-require-reviews"}, name) || yes && name == "yes"
		if !multi && !boolean && !slices.Contains(values, name) {
			b, _ := ledger.Stringify(arg, false)
			return flags, pos, "unknown flag " + string(b)
		}
		if _, found := flags[name]; found && !multi {
			return flags, pos, "--" + name + " given more than once"
		}
		if boolean {
			if inline {
				return flags, pos, "--" + name + " takes no value"
			}
			flags[name] = true
		} else if multi {
			items, _ := flags[name].([]string)
			if inline && value != "" {
				items = append(items, value)
			}
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
				items = append(items, args[i])
			}
			if len(items) == 0 {
				return flags, pos, "--" + name + " needs at least one value"
			}
			flags[name] = items
		} else {
			if !inline {
				if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
					return flags, pos, "--" + name + " needs a value"
				}
				i++
				value = args[i]
			}
			if inline && value == "" {
				return flags, pos, "--" + name + " needs a value"
			}
			flags[name] = value
		}
	}
	return flags, pos, ""
}

var control = regexp.MustCompile(`[\x00-\x1f\x7f]`)

func Finding(f string) string {
	if control.MatchString(f) {
		return "a finding id must not contain control characters"
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`).MatchString(f) {
		return "a finding id is required (letters, digits, . _ : -)"
	}
	return ""
}
func File(f string, normalized bool) string {
	if f == "" {
		return "--file is required"
	}
	if control.MatchString(f) {
		return "--file must not contain control characters"
	}
	clean := path.Clean(f)
	if strings.HasSuffix(f, "/") && clean != "." {
		clean += "/"
	}
	if path.IsAbs(f) || slices.Contains(strings.Split(f, "/"), "..") || strings.HasPrefix(f, "./") || normalized && clean != f {
		return "--file must be repo-root-relative"
	}
	return ""
}

// ReadFile shares the worker's checked descriptor and in-tree parent checks.
func ReadFile(root, file string) ([]byte, bool) {
	b, err := gitx.ReadRegularFileNoFollow(root, file)
	return b, err == nil && b != nil
}

var tokens = regexp.MustCompile("[\\s\\x0b\\p{Zs}\\x{2028}\\x{2029}\\x{feff}\"'`()[\\]{},;<>]+")
var citation = regexp.MustCompile(`^(.+):([1-9][0-9]*)(?:-([1-9][0-9]*))?[.!?]?$`)

func Cited(reason, root string) []string {
	out := []string{}
	for _, token := range tokens.Split(reason, -1) {
		m := citation.FindStringSubmatch(token)
		if m == nil {
			continue
		}
		first, e := strconv.ParseFloat(m[2], 64)
		last := first
		if m[3] != "" {
			last, _ = strconv.ParseFloat(m[3], 64)
		}
		if e != nil || first > 9007199254740991 || last > 9007199254740991 || last < first {
			continue
		}
		b, ok := ReadFile(root, m[1])
		if !ok {
			continue
		}
		// splitLines removes the final empty line.
		count := len(strings.Split(string(b), "\n"))
		if len(b) == 0 {
			count = 0
		}
		if strings.HasSuffix(string(b), "\n") {
			count--
		}
		if first > float64(count) {
			continue
		}
		out = append(out, m[1])
		if len(out) == 10 {
			break
		}
	}
	return out
}
func Evidence(reason, root string, entry map[string]any) bool {
	if len(Cited(reason, root)) > 0 {
		return true
	}
	for _, m := range regexp.MustCompile(`(?i)\bclause\s+([0-9]+)\b`).FindAllStringSubmatch(reason, -1) {
		n, _ := strconv.Atoi(m[1])
		if n >= 1 && n <= len(Array(entry["acceptance"])) {
			return true
		}
	}
	for _, t := range tokens.Split(reason, -1) {
		f := strings.TrimRight(t, ".!?")
		test := regexp.MustCompile(`(^|/)(test|tests|__tests__)/|(^|[./_-])(test|spec)\.[^/]+$`).MatchString(f)
		for _, v := range Array(entry["test_files"]) {
			test = test || v == f
		}
		if _, ok := ReadFile(root, f); test && ok {
			return true
		}
	}
	return false
}

// CitedTests follows the acceptance-test citation syntax without running a test.
func CitedTests(reason, root string, acceptance []any) []string {
	named := map[string]bool{}
	for _, clause := range acceptance {
		for _, v := range Array(Map(clause)["tests"]) {
			if s, ok := v.(string); ok {
				named[s] = true
			}
		}
	}
	out := []string{}
	for _, token := range tokens.Split(reason, -1) {
		file := regexp.MustCompile(`:[1-9][0-9]*(?:-[1-9][0-9]*)?[.!?]?$`).ReplaceAllString(token, "")
		file = strings.TrimRight(file, ".!?")
		if _, ok := ReadFile(root, file); named[file] && ok {
			out = append(out, file)
		}
	}
	return out
}
func Severity(rows []gatecheck.Row, id, file, finding string, key []byte) string {
	rank := map[string]int{"nit": 0, "warning": 1, "critical": 2, "blocker": 3}
	worst, named, bad, unknown := "", false, false, false
	for _, r := range rows {
		if r["block"] != id || r["file"] != file {
			continue
		}
		values := []any{}
		if (r["event"] == "review.triage" || r["event"] == "review.late_finding") && r["finding"] == finding {
			values = append(values, r["severity"])
		} else if r["event"] == "review.result" && r["status"] != "answers" {
			for _, v := range Array(r["open"]) {
				o := Map(v)
				if o["id"] == finding {
					values = append(values, o["severity"])
				}
			}
		}
		for _, v := range values {
			named = true
			bad = bad || !gatecheck.Verified(r, key)
			s := Text(v)
			n, ok := rank[s]
			unknown = unknown || !ok
			if worst == "" || n > rank[worst] {
				worst = s
			}
		}
	}
	if !named {
		return "not-recorded"
	}
	if bad {
		return "unverified"
	}
	if unknown {
		return "unknown"
	}
	return worst
}

type Loaded struct {
	Status string
	State  map[string]any
	Seq    float64
}

// Load checks durable and legacy bytes against the highest verified anchor. It never migrates.
func Load(home, legacy, run, id, file string, rows []gatecheck.Row, key []byte) Loaded {
	result := Loaded{Status: "tampered"}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(id) || File(file, true) != "" {
		return result
	}
	var anchor gatecheck.Row
	for _, r := range rows {
		if r["event"] != "review.state" || r["run"] != run {
			continue
		}
		seq, ok := r["seq"].(float64)
		_, str := r["state_sha"].(string)
		if !ok || seq != float64(int64(seq)) || !str || !gatecheck.Verified(r, key) {
			return result
		}
		if r["block"] == id && r["file"] == file && (anchor == nil || seq > anchor["seq"].(float64)) {
			anchor = r
		}
	}
	name := Hash([]byte(file))[:24] + ".json"
	target := filepath.Join(home, ".code-forge/runs", run, "review-state", id, name)
	var data []byte
	for _, p := range []string{target, filepath.Join(legacy, "review-state", id, name), target} {
		b, e := os.ReadFile(p)
		if e == nil {
			data = b
			break
		}
		if !os.IsNotExist(e) {
			return result
		}
	}
	if anchor == nil {
		if data == nil {
			return Loaded{Status: "none"}
		}
		return result
	}
	result.Seq = anchor["seq"].(float64)
	var body map[string]any
	if data != nil {
		if Hash(data) != anchor["state_sha"] || json.Unmarshal(data, &body) != nil {
			return result
		}
	} else {
		v, exists := anchor["state_body"]
		if !exists {
			result.Status = "lost"
			return result
		}
		body = Map(v)
	}
	verified, err := sign.VerifyRow(body, key)
	if err != nil || !verified.OK || body["run"] != run || body["block"] != id || body["file"] != file || body["seq"] != result.Seq || Map(body["state"])["file"] != file {
		return result
	}
	result.State, result.Status = Map(body["state"]), "ok"
	return result
}
