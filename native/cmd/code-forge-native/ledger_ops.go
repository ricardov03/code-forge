package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/childreg"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/runstatus"
	"github.com/ricardov03/code-forge/native/state"
)

// ledgerNow is the clock seam for `ledger outcome --scan-git`.
var ledgerNow = time.Now

var ledgerSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

const ledgerNullRow = "Cannot read properties of null (reading 'event')"

// ledgerDir is Node's ledgerDir(); "" (no HOME) keeps the case in Node, whose
// os.homedir() falls back to the password database.
func ledgerDir() string {
	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".code-forge", "ledger")
}

// ledgerReadRows is Node's readAllRows for a valid slug. Rows Go cannot read
// the way JSON.parse does (lone surrogates, bad UTF-8, a corrupt line whose
// SyntaxError text is Node's) report false.
func ledgerReadRows(dir, slug string) ([]ledger.Row, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, false
	}
	pattern := regexp.MustCompile(`^` + regexp.QuoteMeta(slug) + `(?:\.[0-9]+)?\.jsonl$`)
	for _, entry := range entries {
		if !pattern.MatchString(entry.Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil || !utf8.Valid(data) || ledgerSurrogatePattern.Match(data) {
			return nil, false
		}
	}
	rows, err := ledger.ReadAllRows(dir, slug)
	if err != nil {
		return nil, false
	}
	return rows, true
}

func ledgerTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case float64:
		return x != 0 && !math.IsNaN(x)
	case string:
		return x != ""
	}
	return true
}

// ledgerJSString is JavaScript's String(value) for a decoded JSON value.
func ledgerJSString(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return strconv.FormatBool(x)
	case float64:
		data, _ := canon.Marshal(x)
		return string(data)
	case string:
		return x
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			if item != nil {
				parts[i] = ledgerJSString(item)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

// ledgerJSRound is Math.round.
func ledgerJSRound(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return x
	}
	r := math.Floor(x)
	if x-r >= 0.5 {
		r++
	}
	return r
}

// ledgerToFixed4 is Number.prototype.toFixed(4) for a finite number.
func ledgerToFixed4(x float64) string {
	if math.Abs(x) >= 1e21 {
		data, _ := canon.Marshal(x)
		return string(data)
	}
	return strconv.FormatFloat(x, 'f', 4, 64)
}

// ledgerJSNumber is Number(string): ok false only for NaN.
func ledgerJSNumber(raw string) (float64, bool) {
	s := strings.TrimFunc(raw, errorLogSpace)
	if s == "" {
		return 0, true
	}
	switch s {
	case "Infinity", "+Infinity":
		return math.Inf(1), true
	case "-Infinity":
		return math.Inf(-1), true
	}
	if len(s) > 2 && s[0] == '0' {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[s[1]]
		if base != 0 {
			n, ok := new(big.Int).SetString(s[2:], base)
			if !ok || strings.ContainsAny(s[2:], "+-_") {
				return 0, false
			}
			f, _ := new(big.Float).SetInt(n).Float64()
			return f, true
		}
	}
	if !regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`).MatchString(s) {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return 0, false
	}
	return f, true
}

func ledgerUTF16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// ----- calibration -----

var ledgerBuckets = []struct {
	id       string
	min, max float64
	closed   bool
}{
	{"0.50-0.60", 0.5, 0.6, false}, {"0.60-0.70", 0.6, 0.7, false}, {"0.70-0.80", 0.7, 0.8, false},
	{"0.80-0.85", 0.8, 0.85, false}, {"0.85-0.90", 0.85, 0.9, false}, {"0.90-0.95", 0.9, 0.95, false},
	{"0.95-0.99", 0.95, 0.99, false}, {"0.99-1.00", 0.99, 1.0, true},
}

var ledgerOutcomeMap = map[string]string{"correct": "correct", "wrong": "wrong", "reverted": "wrong", "unknown": "unknown"}

// ledgerMapKey is a SameValueZero Map key; objects and arrays never match a
// later lookup, so they get no key.
func ledgerMapKey(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return "s" + x, true
	case float64:
		return "n" + ledgerJSString(x), true
	case bool:
		return "b" + strconv.FormatBool(x), true
	case nil:
		return "null", true
	}
	return "", false
}

func ledgerCalibration(args []string, slug string, failed func(string) ledgerResult) ledgerResult {
	question, given, ok := ledgerFlag(args, "--question")
	if !ok {
		return ledgerResult{code: 2, err: "ledger calibration: --question requires a value\n"}
	}
	dir, message, ok := ledgerMissing(slug)
	if !ok {
		return ledgerHandedOver(hLedgerRowsFormat)
	}
	if message != "" {
		return failed(message)
	}
	rows, ok := ledgerReadRows(dir, slug)
	if !ok {
		return ledgerHandedOver(hLedgerRowsFormat)
	}
	type outcome struct {
		value   any
		present bool
	}
	answers := []map[string]any{}
	byID := map[string]outcome{}
	for _, value := range rows {
		if value == nil {
			return failed(ledgerNullRow)
		}
		row, isObject := value.(map[string]any)
		if !isObject {
			continue
		}
		if _, number := row["confidence"].(float64); row["event"] == "decision" && number {
			answers = append(answers, row)
		}
	}
	for _, value := range rows {
		row, isObject := value.(map[string]any)
		if !isObject || row["event"] != "outcome" || !ledgerTruthy(row["decision_id"]) {
			continue
		}
		if key, ok := ledgerMapKey(row["decision_id"]); ok {
			v, present := row["outcome"]
			byID[key] = outcome{v, present}
		}
	}
	counts := map[string]map[string]int{}
	for _, b := range ledgerBuckets {
		counts[b.id] = map[string]int{"correct": 0, "wrong": 0, "unknown": 0}
	}
	unbacked, unmapped := 0, 0
	for _, row := range answers {
		if given && question != "" {
			if q, ok := row["question"].(string); !ok || q != question {
				continue
			}
		}
		c := ledgerJSRound(row["confidence"].(float64)*1e6) / 1e6
		bucket := ""
		for _, b := range ledgerBuckets {
			if c >= b.min && (c < b.max || b.closed && c <= b.max) {
				bucket = b.id
				break
			}
		}
		if bucket == "" {
			unbacked++
			continue
		}
		raw := outcome{}
		if id, present := row["decision_id"]; present {
			if key, ok := ledgerMapKey(id); ok {
				raw = byID[key]
			}
		}
		key := "unknown"
		if raw.present {
			mapped, known := ledgerOutcomeMap[ledgerJSString(raw.value)]
			if !known {
				unmapped++
				continue
			}
			key = mapped
		}
		counts[bucket][key]++
	}
	buckets := ledger.Object{}
	for _, b := range ledgerBuckets {
		buckets = append(buckets, ledger.Field{Key: b.id, Value: ledger.Object{
			{Key: "correct", Value: counts[b.id]["correct"]},
			{Key: "wrong", Value: counts[b.id]["wrong"]},
			{Key: "unknown", Value: counts[b.id]["unknown"]},
		}})
	}
	data, err := ledger.Stringify(ledger.Object{
		{Key: "buckets", Value: buckets}, {Key: "unbacked", Value: unbacked}, {Key: "unmapped", Value: unmapped},
	}, true)
	if err != nil {
		return ledgerHandedOver(hLedgerRowsFormat)
	}
	return ledgerResult{out: string(data) + "\n"}
}

// ----- outcome --pr --ci -----

func ledgerCIOutcome(args []string, slug string, failed func(string) ledgerResult) ledgerResult {
	prArg, _ := ledgerValue(args, "--pr")
	pr, _ := strconv.ParseFloat(prArg, 64)
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(prArg) || pr == 0 {
		return ledgerResult{code: 2, err: "ledger outcome: --pr must be a positive integer, got \"" + prArg + "\"\n"}
	}
	ci, _ := ledgerValue(args, "--ci")
	outcomes := map[string]string{"red": "wrong", "green": "correct", "reverted": "reverted"}
	if _, ok := outcomes[ci]; !ok {
		return ledgerResult{code: 2, err: "ledger outcome: --ci must be red|green|reverted, got \"" + ci + "\"\n"}
	}
	if !ledgerSlugPattern.MatchString(slug) {
		return failed(ledgerSlugError(slug))
	}
	dir := ledgerDir()
	if dir == "" {
		return ledgerHandedOver(hLedgerRowsFormat)
	}
	row := ledger.Object{{Key: "event", Value: "outcome"}, {Key: "outcome", Value: outcomes[ci]},
		{Key: "outcome_source", Value: "ci"}, {Key: "pr", Value: pr}}
	if _, err := ledger.AppendObject(dir, slug, row, ledger.DefaultRotateAtBytes); err != nil {
		return ledgerHandedOver(hLedgerRowsFormat)
	}
	return ledgerResult{out: "recorded outcome for PR #" + ledgerJSString(pr) + ": " + ci + "\n"}
}

// ----- outcome --scan-git -----

type ledgerReview struct {
	ms       int64
	block    any
	hasBlock bool
}

type ledgerCommit struct{ hash, committedAt, subject, body string }

var ledgerRevertsBody = regexp.MustCompile(`(?i)This reverts commit ([0-9a-f]{7,40})\b`)
var ledgerRevertPrefix = regexp.MustCompile(`(?i)^Revert ["“]`)
var ledgerReapplyPrefix = regexp.MustCompile(`(?i)^Reapply ["“]`)

func ledgerSubjectDepth(subject string) int {
	depth := 0
	for {
		if m := ledgerRevertPrefix.FindString(subject); m != "" {
			depth++
			subject = subject[len(m):]
		} else if m := ledgerReapplyPrefix.FindString(subject); m != "" {
			depth += 2
			subject = subject[len(m):]
		} else {
			return depth
		}
	}
}

// ledgerGit runs git like Node's exec (GIT_* stripped). usable false: Node's
// text for this failure (a spawn error, an empty stderr, bytes that are not
// UTF-8, more than exec's 10 MiB buffer) stays in Node.
func ledgerGit(dir string, args ...string) (stdout, stderr string, ok, usable bool) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := childreg.Run(cmd)
	if out.Len()+errOut.Len() > 10*1024*1024 || !utf8.Valid(out.Bytes()) || !utf8.Valid(errOut.Bytes()) {
		return "", "", false, false
	}
	if err == nil {
		return out.String(), errOut.String(), true, true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() > 0 && errOut.Len() > 0 {
		return out.String(), errOut.String(), false, true
	}
	return "", "", false, false
}

var ledgerISOTime = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{3}))?)?(Z|[+-]\d{2}:\d{2})$`)

// ledgerParseTime is new Date(text).getTime() for the ISO forms code-forge
// writes; anything else is false (Node keeps that parse).
func ledgerParseTime(text string) (int64, bool) {
	m := ledgerISOTime.FindStringSubmatch(text)
	if m == nil {
		return 0, false
	}
	num := func(s string) int {
		n, _ := strconv.Atoi(s)
		return n
	}
	year, month, day, hour, minute := num(m[1]), num(m[2]), num(m[3]), num(m[4]), num(m[5])
	second, milli := num(m[6]), num(m[7])
	if month < 1 || month > 12 || day < 1 || hour > 23 || minute > 59 || second > 59 {
		return 0, false
	}
	if day > time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day() {
		return 0, false
	}
	offset := 0
	if m[8] != "Z" {
		oh, om := num(m[8][1:3]), num(m[8][4:6])
		if oh > 23 || om > 59 {
			return 0, false
		}
		offset = oh*60 + om
		if m[8][0] == '-' {
			offset = -offset
		}
	}
	t := time.Date(year, time.Month(month), day, hour, minute, second, milli*int(time.Millisecond), time.UTC)
	return t.UnixMilli() - int64(offset)*60000, true
}

// ledgerISO is Date.prototype.toISOString for a TimeClip'd value; false where
// it would throw or print an extended year.
func ledgerISO(ms float64) (string, bool) {
	if math.IsNaN(ms) || math.Abs(ms) > 8.64e15 {
		return "", false
	}
	t := time.UnixMilli(int64(ms)).UTC()
	if t.Year() < 0 || t.Year() > 9999 {
		return "", false
	}
	return t.Format("2006-01-02T15:04:05.000Z"), true
}

// Node's path.dirname/basename ignore trailing slashes.
func ledgerTrimSlash(p string) string {
	trimmed := strings.TrimRight(p, "/")
	if trimmed == "" && p != "" {
		return "/"
	}
	return trimmed
}

func ledgerRealpath(p string) (string, bool) {
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", false
	}
	abs, err := filepath.Abs(real)
	return abs, err == nil
}

func ledgerNearestReal(p string) string {
	tail := []string{}
	current := p
	for {
		if real, ok := ledgerRealpath(current); ok {
			return filepath.Join(append([]string{real}, tail...)...)
		}
		trimmed := ledgerTrimSlash(current)
		parent := filepath.Dir(trimmed)
		if parent == current {
			return p
		}
		tail = append([]string{filepath.Base(trimmed)}, tail...)
		current = parent
	}
}

func ledgerRelative(root, target string) string {
	rel, err := filepath.Rel(root, filepath.Clean(target))
	if err != nil || rel == "." {
		return ""
	}
	return rel
}

func ledgerScanGit(args []string, slug string, failed func(string) ledgerResult) ledgerResult {
	cwdValue, cwdGiven, ok := ledgerFlag(args, "--cwd")
	if !ok {
		return ledgerResult{code: 2, err: "ledger outcome --scan-git: --cwd requires a value\n"}
	}
	cwd := cwdValue
	if !cwdGiven {
		var err error
		if cwd, err = os.Getwd(); err != nil {
			return ledgerHandedOver(hLedgerGit)
		}
	}
	daysValue, daysGiven, ok := ledgerFlag(args, "--days")
	if !ok {
		return ledgerResult{code: 2, err: "ledger outcome --scan-git: --days requires a value\n"}
	}
	days := 7.0
	if daysGiven {
		d, valid := ledgerJSNumber(daysValue)
		if !valid || math.IsInf(d, 0) || d <= 0 {
			return ledgerResult{code: 2, err: "ledger outcome --scan-git: --days must be a finite number > 0, got \"" + daysValue + "\"\n"}
		}
		days = d
	}
	if !ledgerSlugPattern.MatchString(slug) {
		return failed(ledgerSlugError(slug))
	}
	dir := ledgerDir()
	if dir == "" {
		return ledgerHandedOver(hLedgerRowsFormat)
	}
	rows, ok := ledgerReadRows(dir, slug)
	if !ok {
		return ledgerHandedOver(hLedgerRowsFormat)
	}
	type entry struct {
		file   string
		review ledgerReview
	}
	reviewed := []entry{}
	for _, value := range rows {
		if value == nil {
			return failed(ledgerNullRow)
		}
		row, isObject := value.(map[string]any)
		if !isObject || row["event"] != "review.done" || !ledgerTruthy(row["file"]) || !ledgerTruthy(row["ts"]) {
			continue
		}
		file, isString := row["file"].(string)
		ts, tsString := row["ts"].(string)
		if !isString || !tsString {
			return ledgerHandedOver(hLedgerJsValue)
		}
		ms, parsed := ledgerParseTime(ts)
		if !parsed {
			return ledgerHandedOver(hLedgerJsValue)
		}
		review := ledgerReview{ms: ms}
		if block := row["block"]; ledgerTruthy(block) {
			switch block.(type) {
			case map[string]any, []any:
				return ledgerHandedOver(hLedgerJsValue)
			}
			review.block, review.hasBlock = block, true
		}
		reviewed = append(reviewed, entry{file, review})
	}
	out, stderr, ok, usable := ledgerGit(cwd, "rev-parse", "--show-toplevel")
	if !usable {
		return ledgerHandedOver(hLedgerGit)
	}
	if !ok {
		return failed("git rev-parse --show-toplevel failed: " + stderr)
	}
	root := strings.TrimFunc(out, errorLogSpace)
	realCwd, ok := ledgerRealpath(cwd)
	if !ok {
		if realCwd, ok = cwd, true; !filepath.IsAbs(realCwd) {
			abs, err := filepath.Abs(realCwd)
			if err != nil {
				return ledgerHandedOver(hLedgerGit)
			}
			realCwd = abs
		}
	}
	byFile := map[string][]ledgerReview{}
	order := []string{}
	minMs, maxMs := int64(math.MaxInt64), int64(math.MinInt64)
	for _, e := range reviewed {
		var file string
		if strings.HasPrefix(e.file, "/") {
			file = ledgerRelative(root, ledgerNearestReal(e.file))
		} else {
			file = ledgerRelative(root, filepath.Join(realCwd, e.file))
		}
		if _, seen := byFile[file]; !seen {
			order = append(order, file)
		}
		list := byFile[file]
		at := len(list)
		for at > 0 && list[at-1].ms > e.review.ms {
			at--
		}
		list = append(list[:at], append([]ledgerReview{e.review}, list[at:]...)...)
		byFile[file] = list
		if e.review.ms < minMs {
			minMs = e.review.ms
		}
		if e.review.ms > maxMs {
			maxMs = e.review.ms
		}
	}
	found := []ledger.Object{}
	if len(byFile) > 0 {
		windowMs := days * 24 * 60 * 60 * 1000
		now := ledgerNow().UnixMilli()
		since, okSince := ledgerISO(float64(minMs))
		until, okUntil := ledgerISO(math.Min(float64(maxMs)+windowMs, float64(now)))
		if !okSince || !okUntil {
			return ledgerHandedOver(hLedgerJsValue)
		}
		out, stderr, ok, usable := ledgerGit(root, "log", "--pretty=format:%H\x1f%cI\x1f%s\x1f%b\x1e", "--since="+since, "--until="+until)
		if !usable {
			return ledgerHandedOver(hLedgerGit)
		}
		if !ok {
			return failed("git log failed: " + stderr)
		}
		commits := []ledgerCommit{}
		for _, chunk := range strings.Split(out, "\x1e") {
			chunk = strings.TrimPrefix(chunk, "\n")
			if chunk == "" {
				continue
			}
			parts := append(strings.Split(chunk, "\x1f"), "", "", "")
			commits = append(commits, ledgerCommit{parts[0], parts[1], parts[2], parts[3]})
		}
		type looked struct {
			subject, body string
			found         bool
		}
		cache := map[string]looked{}
		for _, c := range commits {
			cache[c.hash] = looked{c.subject, c.body, true}
		}
		gitBroken := false
		lookup := func(sha string) looked {
			if hit, ok := cache[sha]; ok {
				return hit
			}
			out, _, ok, usable := ledgerGit(root, "log", "-1", "--pretty=format:%s\x1f%b", sha+"^{commit}", "--")
			result := looked{}
			if !usable && ok {
				gitBroken = true
			}
			if ok && usable {
				parts := append(strings.Split(out, "\x1f"), "")
				result = looked{parts[0], parts[1], true}
			}
			cache[sha] = result
			return result
		}
		depthOf := func(c ledgerCommit) int {
			levels := 0
			subject, body := c.subject, c.body
			seen := map[string]bool{}
			for i := 0; i < 64; i++ {
				m := ledgerRevertsBody.FindStringSubmatch(body)
				if m == nil {
					return levels + ledgerSubjectDepth(subject)
				}
				sha := strings.ToLower(m[1])
				parent := looked{}
				if !seen[sha] {
					parent = lookup(sha)
				}
				seen[sha] = true
				if !parent.found {
					return levels + max(1, ledgerSubjectDepth(subject))
				}
				levels++
				subject, body = parent.subject, parent.body
			}
			return levels
		}
		for _, c := range commits {
			if ledgerSubjectDepth(c.subject) == 0 && !ledgerRevertsBody.MatchString(c.body) {
				continue
			}
			if depthOf(c)%2 != 1 {
				continue
			}
			if gitBroken {
				return ledgerHandedOver(hLedgerGit)
			}
			committed, parsed := ledgerParseTime(c.committedAt)
			if parsed && committed > now {
				continue
			}
			out, stderr, ok, usable := ledgerGit(root, "-c", "core.quotePath=false", "diff-tree", "--no-commit-id", "--name-only", "-r", c.hash)
			if !usable {
				return ledgerHandedOver(hLedgerGit)
			}
			if !ok {
				return failed("git diff-tree failed for " + c.hash + ": " + stderr)
			}
			if !parsed {
				continue // Invalid Date: no review precedes it
			}
			for _, file := range strings.Split(out, "\n") {
				list, known := byFile[file]
				if file == "" || !known {
					continue
				}
				var review *ledgerReview
				for i := range list {
					if list[i].ms >= committed {
						break
					}
					review = &list[i]
				}
				if review == nil || float64(committed) > float64(review.ms)+windowMs {
					continue
				}
				row := ledger.Object{
					{Key: "event", Value: "outcome"}, {Key: "outcome", Value: "reverted"},
					{Key: "outcome_source", Value: "git"}, {Key: "file", Value: file},
					{Key: "commit", Value: c.hash}, {Key: "subject", Value: c.subject},
					{Key: "committed_at", Value: c.committedAt},
				}
				if review.hasBlock {
					row = append(row, ledger.Field{Key: "block", Value: review.block})
				}
				found = append(found, row)
			}
		}
	}
	already := map[string]bool{}
	for _, value := range rows {
		row, isObject := value.(map[string]any)
		if !isObject || row["event"] != "outcome" || row["outcome_source"] != "git" {
			continue
		}
		key := func(name string) string {
			if v, present := row[name]; present {
				return ledgerJSString(v)
			}
			return "undefined"
		}
		already[key("commit")+"::"+key("file")] = true
	}
	written := 0
	for _, row := range found {
		key := row.Get("commit").(string) + "::" + row.Get("file").(string)
		if already[key] {
			continue
		}
		if _, err := ledger.AppendObject(dir, slug, row, ledger.DefaultRotateAtBytes); err != nil {
			return ledgerHandedOver(hLedgerRowsFormat) // Node dedupes rows already written
		}
		already[key] = true
		written++
	}
	return ledgerResult{out: fmt.Sprintf("scanned git: %d row(s) marked reverted\n", written)}
}

// ----- add coder / outside-review -----

var ledgerIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ledgerRunRecord reads the run like Node's readRun and loads its key like
// loadKey. A message is Node's own StateError text; ok false hands over.
func ledgerRunRecord(run string) (record ledger.Object, message, project, secret string, ok bool) {
	home := os.Getenv("HOME")
	if home == "" {
		return nil, "", "", "", false
	}
	record, err := state.ReadRun(home, run)
	var stateErr *state.Error
	if errors.As(err, &stateErr) {
		return nil, stateErr.Message, "", "", true
	}
	if err != nil {
		return nil, "", "", "", false
	}
	return record, "", "", "", true
}

func ledgerLoadKey(run string) (message, secret string, ok bool) {
	keyFile := filepath.Join(os.Getenv("HOME"), ".code-forge", "runs", run+".key")
	info, err := os.Stat(keyFile)
	if os.IsNotExist(err) {
		return fmt.Sprintf("run %s has no signer key", run), "", true
	}
	if err != nil {
		return "", "", false
	}
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Sprintf("run %s signer key is accessible to other users (mode %o); expected 600", run, info.Mode().Perm()), "", true
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return "", "", false
	}
	if len(key) != 32 {
		return fmt.Sprintf("run %s signer key is not 32 bytes", run), "", true
	}
	return "", hex.EncodeToString(key), true
}

// ledgerProject is the run record's project as appendRow checks it.
func ledgerProject(record ledger.Object) (slug, message string, ok bool) {
	project, isString := record.Get("project").(string)
	if !isString {
		return "", "", false
	}
	if !ledgerSlugPattern.MatchString(project) {
		return "", ledgerSlugError(project), true
	}
	return project, "", true
}

func ledgerAdd(args []string) ledgerResult {
	usage := ledgerResult{code: 2, err: ledgerSubUsage["add"]}
	role := ""
	if len(args) > 0 {
		role = args[0]
	}
	run, _ := ledgerValue(args, "--run")
	block, _ := ledgerValue(args, "--block")
	usdArg, hasUSD := ledgerValue(args, "--usd")
	if role != "coder" || run == "" || block == "" || !hasUSD {
		return usage
	}
	note, hasNote, ok := ledgerFlag(args, "--note")
	if !ok {
		return ledgerResult{code: 2, err: "ledger add: --note requires a value\n"}
	}
	if !ledgerIDPattern.MatchString(run) {
		return ledgerResult{code: 2, err: "ledger add: --run must be a run id (letters, digits, \".\", \"_\", \"-\")\n"}
	}
	if !ledgerIDPattern.MatchString(block) {
		return ledgerResult{code: 2, err: "ledger add: --block must be a block id (letters, digits, \".\", \"_\", \"-\")\n"}
	}
	usd := math.NaN()
	if regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`).MatchString(usdArg) {
		n, _ := strconv.ParseFloat(usdArg, 64)
		usd = ledgerJSRound(n*10000) / 10000
	}
	if math.IsNaN(usd) || math.IsInf(usd, 0) || usd <= 0 {
		return ledgerResult{code: 2, err: "ledger add: --usd must be a number > 0 (at 4 decimals), got \"" + usdArg + "\"\n"}
	}
	if hasNote && ledgerUTF16Len(note) > 500 {
		return ledgerResult{code: 2, err: "ledger add: --note is longer than 500 characters\n"}
	}
	failed := func(message, secret string) ledgerResult {
		return ledgerResult{code: 1, err: "ledger add: " + message + "\n", secret: secret}
	}
	record, message, _, _, ok := ledgerRunRecord(run)
	if !ok {
		return ledgerHandedOver(hLedgerRunRecord)
	}
	if message != "" {
		return failed(message, "")
	}
	message, secret, ok := ledgerLoadKey(run)
	if !ok {
		return ledgerHandedOver(hLedgerRunRecord)
	}
	if message != "" {
		return failed(message, "")
	}
	project, message, ok := ledgerProject(record)
	if !ok {
		return ledgerHandedOver(hLedgerRunRecord)
	}
	if message != "" {
		return failed(message, secret)
	}
	row := ledger.Object{
		{Key: "event", Value: "session"}, {Key: "role", Value: "coder"}, {Key: "manual", Value: true},
		{Key: "block", Value: block}, {Key: "provider", Value: nil}, {Key: "level", Value: nil},
		{Key: "tokens_in", Value: nil}, {Key: "tokens_out", Value: nil}, {Key: "usd", Value: usd},
		{Key: "cost_source", Value: "manual"},
	}
	if hasNote {
		row = append(row, ledger.Field{Key: "note", Value: note})
	}
	if _, err := state.WriteSigned(os.Getenv("HOME"), run, row); err != nil {
		return ledgerHandedOver(hLedgerRunRecord)
	}
	total := "unknown"
	if rows, err := ledger.ReadAllRows(ledgerDir(), project); err == nil {
		mine := []ledger.Row{}
		null := false
		for _, value := range rows {
			if value == nil {
				null = true
			}
			if row, isObject := value.(map[string]any); isObject && row["run"] == run {
				mine = append(mine, row)
			}
		}
		if spent, _, ok := runstatus.RunSpend(mine, run); ok && !null && !math.IsInf(spent, 0) && !math.IsNaN(spent) {
			total = "$" + ledgerToFixed4(spent)
		}
	}
	return ledgerResult{
		out:    fmt.Sprintf("recorded coder spend $%s for run %s block %s · run total %s\n", ledgerToFixed4(usd), run, block, total),
		secret: secret,
	}
}

func ledgerOutsideReview(args []string) ledgerResult {
	usage := func(message string) ledgerResult {
		return ledgerResult{code: 2, err: "ledger outside-review: " + message + "\n" + ledgerOutsideReviewUsage + "\n"}
	}
	single := map[string]string{}
	blocks := []any{}
	seenBlock := map[string]bool{}
	for i := 0; i < len(args); i += 2 {
		flag := args[i]
		switch flag {
		case "--block", "--run", "--pr", "--rounds", "--findings", "--link":
		default:
			return usage(fmt.Sprintf("unknown argument at position %d", i+1))
		}
		if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
			return usage(flag + " requires a value")
		}
		value := args[i+1]
		if flag == "--block" {
			if !seenBlock[value] {
				seenBlock[value] = true
				blocks = append(blocks, value)
			}
			continue
		}
		if _, dup := single[flag]; dup {
			return usage(flag + " is given more than once")
		}
		single[flag] = value
	}
	for _, flag := range []string{"--run", "--pr", "--rounds", "--findings"} {
		if _, ok := single[flag]; !ok {
			return usage(flag + " is required")
		}
	}
	run := single["--run"]
	if !ledgerIDPattern.MatchString(run) {
		return usage("--run must be a run id (letters, digits, \".\", \"_\", \"-\")")
	}
	shaped := func(raw, shape string) (int64, bool) {
		if !regexp.MustCompile(shape).MatchString(raw) {
			return 0, false
		}
		n, err := strconv.ParseInt(raw, 10, 64)
		return n, err == nil && n <= 1<<53-1
	}
	pr, ok := shaped(single["--pr"], `^[1-9][0-9]*$`)
	if !ok {
		return usage("--pr must be a positive integer")
	}
	rounds, ok := shaped(single["--rounds"], `^(0|[1-9][0-9]*)$`)
	if !ok {
		return usage("--rounds must be a non-negative integer")
	}
	findings, ok := shaped(single["--findings"], `^(0|[1-9][0-9]*)$`)
	if !ok {
		return usage("--findings must be a non-negative integer")
	}
	var link any
	if raw, given := single["--link"]; given {
		if ledgerUTF16Len(raw) > 2048 {
			return usage("--link is longer than 2048 characters")
		}
		valid := strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://")
		rest := strings.TrimPrefix(strings.TrimPrefix(raw, "https://"), "http://")
		if !valid || rest == "" || strings.IndexFunc(rest, errorLogSpace) >= 0 {
			return usage("--link must be an https:// or http:// URL with no whitespace")
		}
		link = raw
	}
	failed := func(message, secret string) ledgerResult {
		return ledgerResult{code: 1, err: "ledger outside-review: " + message + "\n", secret: secret}
	}
	record, message, _, _, ok := ledgerRunRecord(run)
	if !ok {
		return ledgerHandedOver(hLedgerRunRecord)
	}
	if message != "" {
		return failed(message, "")
	}
	known := ledger.Object{}
	switch v := record.Get("blocks").(type) {
	case ledger.Object:
		known = v
	case []any:
		if len(blocks) > 0 {
			return ledgerHandedOver(hLedgerRunRecord)
		}
	}
	for _, id := range blocks {
		has := false
		for _, field := range known {
			if field.Key == id {
				has = true
			}
		}
		if !has {
			quoted, _ := ledger.Stringify(id, false)
			return failed(fmt.Sprintf("block %s is not a block of run %s; nothing written", quoted, run), "")
		}
	}
	message, secret, ok := ledgerLoadKey(run)
	if !ok {
		return ledgerHandedOver(hLedgerRunRecord)
	}
	if message != "" {
		return failed(message, "")
	}
	if text, isText := link.(string); isText {
		link = ledgerRedact(text, secret)
	}
	if _, message, ok := ledgerProject(record); !ok {
		return ledgerHandedOver(hLedgerRunRecord)
	} else if message != "" {
		return failed(message, secret)
	}
	row := ledger.Object{
		{Key: "event", Value: "outside_review"}, {Key: "pr", Value: pr}, {Key: "rounds", Value: rounds},
		{Key: "findings", Value: findings}, {Key: "link", Value: link}, {Key: "blocks", Value: blocks},
	}
	if _, err := state.WriteSigned(os.Getenv("HOME"), run, row); err != nil {
		return ledgerHandedOver(hLedgerRunRecord)
	}
	names := []string{}
	for _, b := range blocks {
		names = append(names, b.(string))
	}
	list := strings.Join(names, ", ")
	if list == "" {
		list = "-"
	}
	return ledgerResult{out: fmt.Sprintf("outside_review pr %d · rounds %d · findings %d · blocks %s\n", pr, rounds, findings, list), secret: secret}
}
