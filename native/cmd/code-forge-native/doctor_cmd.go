package main

// B474: `code-forge doctor` in Go (src/cli/doctor.mjs, src/doctor/*.mjs).
// Go serves help, every usage error and the `--quick` run (config, harness
// links, Jev key resolution and the reviewer performance rows) with Node's
// text, JSON, redaction and exit codes. B504: the full run without a loadable
// config runs in Go too (doctor_local.go); the full run with a config (live
// Jev call, provider CLI probes, worker start/stop) keeps the Node hand-over
// (doctor_full.go names why), as do the --quick cases named in prepareDoctor.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

const doctorUsage = "usage: code-forge doctor [--quick] [--json] [--cwd <dir>]\n"

// doctorNow is Date.now(); tests pin it.
var doctorNow = func() float64 { return float64(time.Now().UnixMilli()) }

func init() {
	routes["doctor"] = smallVerb{name: "doctor", usage: doctorUsage, prepare: prepareDoctor}.run
}

type doctorRow struct{ id, status, label, detail string }

// doctorFlags is parseFlags(args, {values: ['cwd'], booleans: ['quick', 'json']}).
func doctorFlags(args []string) (map[string]any, []string, string) {
	flags, positionals := map[string]any{}, []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			positionals = append(positionals, arg)
			continue
		}
		name, value, inline := strings.Cut(arg[2:], "=")
		boolean := name == "quick" || name == "json"
		if !boolean && name != "cwd" {
			return flags, positionals, "unknown flag " + statusQuote(arg)
		}
		if _, exists := flags[name]; exists {
			return flags, positionals, "--" + name + " given more than once"
		}
		switch {
		case boolean:
			if inline {
				return flags, positionals, "--" + name + " takes no value"
			}
			flags[name] = true
		case inline:
			if value == "" {
				return flags, positionals, "--" + name + " needs a value"
			}
			flags[name] = value
		default:
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return flags, positionals, "--" + name + " needs a value"
			}
			i++
			flags[name] = args[i]
		}
	}
	return flags, positionals, ""
}

func prepareDoctor(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	flags, positionals, message := doctorFlags(args)
	if message != "" {
		return func(_, stderr io.Writer) int {
			fmt.Fprint(stderr, "doctor: "+message+"\n"+doctorUsage)
			return 2
		}, 0, true
	}
	if len(positionals) > 0 {
		return func(_, stderr io.Writer) int { fmt.Fprint(stderr, doctorUsage); return 2 }, 0, true
	}
	cwd := physicalCwd()
	if dir, ok := flags["cwd"].(string); ok {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(cwd, dir)
		}
		cwd = filepath.Clean(dir)
	}
	home := os.Getenv("HOME")
	if flags["quick"] != true {
		rows, reason := doctorFullRunReason(cwd)
		if reason == 0 && home == "" {
			reason = hDoctorNoHome // Node falls back to the passwd entry
		}
		if reason != 0 {
			return nil, reason, false
		}
		return prepareDoctorNoConfig(cwd, home, rows, flags["json"] == true)
	}
	if home == "" {
		return nil, hDoctorNoHome, false // Node falls back to the passwd entry
	}
	now := doctorNow()
	rows, cfg, reason := doctorConfigRow(cwd)
	if reason != 0 {
		return nil, reason, false
	}
	rows = append(rows, doctorLinksRow(home))
	resolveKeys, reason := doctorKeysRow(cfg, home, now)
	if reason != 0 {
		return nil, reason, false
	}
	keysAt := len(rows)
	rows = append(rows, doctorRow{})
	if cfg != nil {
		if slug := cfg["project"]; slug != nil {
			lines, ok := doctorReviewerLines(cfg, home, now)
			if !ok {
				return nil, hDoctorLedgerShape, false
			}
			for i, line := range lines {
				at := strings.Index(line, ": ")
				rows = append(rows, doctorRow{"reviewer." + strconv.Itoa(i), "INFO", line[:at], line[at+2:]})
			}
		}
	}
	jsonOut := flags["json"] == true
	return func(stdout, stderr io.Writer) int {
		nativeReportedKind = ""
		keyRow, secrets := resolveKeys()
		rows[keysAt] = keyRow
		redactOne := func(text string) string { return redactSecret(text, secrets...) }
		var out string
		if jsonOut {
			out = redactOne(doctorJSON(rows)) + "\n"
		} else {
			var b strings.Builder
			for _, r := range rows {
				b.WriteString(redactOne(fmt.Sprintf("%-4s %s: %s", r.status, r.label, r.detail)) + "\n")
			}
			out = b.String()
		}
		fmt.Fprint(stdout, redactOne(out))
		c := doctorCounts(rows)
		fmt.Fprint(stderr, redactOne(fmt.Sprintf("doctor: %d ok, %d warn, %d fail\n", c["OK"], c["WARN"], c["FAIL"])))
		if c["FAIL"] > 0 {
			return 1
		}
		nativeReportedKind = "" // takeErrorKind runs on every exit
		return 0
	}, 0, true
}

func doctorCounts(rows []doctorRow) map[string]int {
	c := map[string]int{"OK": 0, "WARN": 0, "FAIL": 0, "INFO": 0}
	for _, r := range rows {
		c[r.status]++
	}
	return c
}

// doctorJSON is JSON.stringify({ok, counts, rows}).
func doctorJSON(rows []doctorRow) string {
	c := doctorCounts(rows)
	list := []any{}
	for _, r := range rows {
		list = append(list, ledger.Object{{Key: "id", Value: r.id}, {Key: "status", Value: r.status}, {Key: "label", Value: r.label}, {Key: "detail", Value: r.detail}})
	}
	counts := ledger.Object{}
	for _, s := range []string{"OK", "WARN", "FAIL", "INFO"} {
		counts = append(counts, ledger.Field{Key: s, Value: float64(c[s])})
	}
	data, _ := ledger.Stringify(ledger.Object{{Key: "ok", Value: c["FAIL"] == 0}, {Key: "counts", Value: counts}, {Key: "rows", Value: list}}, false)
	return string(data)
}

// redactSecret is redactString in src/util/redact.mjs over the registered
// secrets: every occurrence (overlaps merged) becomes one [REDACTED].
func redactSecret(text string, secrets ...string) string {
	units := utf16.Encode([]rune(text))
	type span struct{ start, end int }
	var ranges []span
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		sec := utf16.Encode([]rune(secret))
		for at := 0; at+len(sec) <= len(units); at++ {
			match := true
			for j := range sec {
				if units[at+j] != sec[j] {
					match = false
					break
				}
			}
			if match {
				ranges = append(ranges, span{at, at + len(sec)})
			}
		}
	}
	if len(ranges) == 0 {
		return text
	}
	sort.SliceStable(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })
	var out []uint16
	cursor, start, end := 0, ranges[0].start, ranges[0].end
	mask := utf16.Encode([]rune("[REDACTED]"))
	for _, r := range ranges[1:] {
		if r.start <= end {
			if r.end > end {
				end = r.end
			}
			continue
		}
		out = append(append(out, units[cursor:start]...), mask...)
		cursor, start, end = end, r.start, r.end
	}
	out = append(append(append(out, units[cursor:start]...), mask...), units[end:]...)
	return string(utf16.Decode(out))
}

// doctorConfigRow is checkConfig in src/doctor/local.mjs. validateConfig runs
// without seenInCache or hasCliOnPath, so the CLI rules see no seen ids and
// never report a missing CLI.
func doctorConfigRow(cwd string) ([]doctorRow, map[string]any, handoverReason) {
	cfg, order, err := config.LoadOrdered(cwd)
	if err != nil {
		if yamlSyntaxError(err) {
			return nil, nil, hConfigParseError
		}
		var e *config.Error
		if !errors.As(err, &e) {
			return nil, nil, hDoctorConfigSchema
		}
		return []doctorRow{{"config", "FAIL", "config", e.Kind + ": .code-forge.yml cannot be loaded (run code-forge init)"}}, nil, 0
	}
	if len(config.SchemaIssues(cfg, "")) > 0 {
		return nil, nil, hDoctorConfigSchema
	}
	if configHoldsSecretShape(cfg) {
		return nil, nil, hDoctorConfigSecrets
	}
	if sandbox, ok := objAt(cfg, "coder", "sandbox"); ok {
		if dirs, ok := sandbox["add_dirs"].([]any); ok && len(dirs) > 0 {
			return nil, nil, hDoctorSandboxDirs
		}
	}
	v := configValidator{cfg: cfg, order: order, hasCli: func(string) bool { return true }}
	errs, warns := v.run()
	if len(errs) > 0 {
		return []doctorRow{{"config", "FAIL", "config", fmt.Sprintf("%d error(s); first: %s", len(errs), maskTokens(errs[0].message))}}, nil, 0
	}
	if len(warns) > 0 {
		return []doctorRow{{"config", "WARN", "config", fmt.Sprintf("valid with %d warning(s); first: %s", len(warns), maskTokens(warns[0].message))}}, cfg, 0
	}
	return []doctorRow{{"config", "OK", "config", "valid"}}, cfg, 0
}

// doctorLinksRow is checkLinks.
func doctorLinksRow(home string) doctorRow {
	records := readInstallRecords(installsFilePath(home))
	if len(records) == 0 {
		return doctorRow{"links", "WARN", "links", "no harness link recorded (run code-forge init)"}
	}
	var broken []string
	for _, r := range records {
		if _, err := os.Stat(r.target); err != nil {
			broken = append(broken, r.harness+"/"+r.scope)
		}
	}
	if len(broken) > 0 {
		return doctorRow{"links", "FAIL", "links", fmt.Sprintf("%d of %d link(s) do not resolve: %s", len(broken), len(records), strings.Join(broken, ", "))}
	}
	return doctorRow{"links", "OK", "links", fmt.Sprintf("%d link(s) resolve; skill listing UNVERIFIED", len(records))}
}

var (
	keyNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	opRefPattern   = regexp.MustCompile(`^op://[^/\r\n]+/[^/\r\n]+/[^\r\n]+$`)
)

// doctorKeysRow is checkKeys with resolveKey (src/keys/store.mjs) for the
// cases Go serves: the env variable, or the 0600 file store pinned by
// CODE_FORGE_KEY_BACKEND=file, including (B483) an expired entry it deletes,
// an entry it cuts to the cache hours and an op:// read it caches. The
// keychain chain hands over. The returned resolver runs the side effects and
// gives the row with every value Node registers for redaction.
func doctorKeysRow(cfg map[string]any, home string, now float64) (func() (doctorRow, []string), handoverReason) {
	fixed := func(row doctorRow, secrets ...string) func() (doctorRow, []string) {
		return func() (doctorRow, []string) { return row, secrets }
	}
	var ref string
	hasRef := false
	if cfg != nil {
		value, present := any(nil), false
		if sys, ok := cfg["system1"].(map[string]any); ok {
			value, present = sys["key"]
		}
		if !present || value == nil {
			if keys, ok := cfg["keys"].(map[string]any); ok {
				value = keys["jev"]
			}
		}
		ref, hasRef = value.(string)
	}
	hours, fromConfig := 8.0, false
	if keys, ok := cfg["keys"].(map[string]any); ok {
		if h, ok := keys["cache_hours"].(float64); ok && h == math.Trunc(h) && h >= 1 && h <= 24 {
			hours, fromConfig = h, true
		}
	}
	failed := doctorRow{"keys", "FAIL", "keys", "jev key resolution failed (TypeError)"}
	envName, storeName, opRef := "CODE_FORGE_KEY_JEV", "jev", ""
	if hasRef && strings.HasPrefix(ref, "env:") {
		envName = ref[4:]
	}
	if hasRef && strings.HasPrefix(ref, "keychain:") {
		storeName = ref[9:]
		if !keyNamePattern.MatchString(storeName) {
			return fixed(failed), 0
		}
	}
	if hasRef && strings.HasPrefix(ref, "op://") {
		if !opRefPattern.MatchString(ref) {
			return fixed(failed), 0
		}
		opRef = ref
	}
	suffix := ""
	if fromConfig {
		suffix = fmt.Sprintf(" (cache %d h)", int(hours))
	}
	if value := os.Getenv(envName); envName != "" && value != "" {
		return fixed(doctorRow{"keys", "OK", "keys", "jev key from env, no expiry" + suffix}, value), 0
	}
	if os.Getenv("CODE_FORGE_KEY_BACKEND") != "file" {
		return nil, hDoctorKeyStore
	}
	okRow := func(source string, exp *float64) doctorRow {
		if exp != nil && *exp-now < 3_600_000 {
			minutes := math.Max(1, math.Floor((*exp-now)/60_000))
			return doctorRow{"keys", "WARN", "keys", fmt.Sprintf("jev key from %s expires in %d min; re-link with code-forge keys set jev --op <ref from keys.jev>", source, int(minutes))}
		}
		when := "no expiry"
		if exp != nil {
			when = "expires " + time.UnixMilli(int64(math.Trunc(*exp))).UTC().Format("2006-01-02T15:04:05.000Z")
		}
		return doctorRow{"keys", "OK", "keys", "jev key from " + source + ", " + when + suffix}
	}
	latest := now + hours*3_600_000
	keyFile := filepath.Join(home, ".code-forge", "store", storeName+".key")
	var reasons, secrets []string
	var before func()
	raw, err := os.ReadFile(keyFile)
	switch {
	case err == nil:
		if !utf8.Valid(raw) {
			return nil, hDoctorKeyStore
		}
		text := strings.TrimSuffix(string(raw), "\n")
		if text == "" {
			break
		}
		value, exp, ok := decodeKeyEntry(text)
		if !ok {
			return nil, hDoctorKeyStore
		}
		secrets = append(secrets, value)
		switch {
		case exp != nil && *exp <= now:
			// dropped as expired: the file goes, then its index row
			if !keyIndexReadable(home, storeName, false) {
				return nil, hDoctorKeyRefresh
			}
			before = func() {
				if err := os.Remove(keyFile); err != nil && !errors.Is(err, os.ErrNotExist) {
					reasons = append(reasons, "key store error")
				}
				if updateKeyIndex(home, func(index *ledger.Object) { index.Delete(storeName) }) != nil {
					reasons = append(reasons, "key store error")
				}
			}
		case exp != nil && *exp > latest:
			// D2: the shorter expiry applies now; the index row moves only when it exists
			if !keyIndexReadable(home, storeName, true) {
				return nil, hDoctorKeyRefresh
			}
			return func() (doctorRow, []string) {
				if writePrivateFile(keyFile, encodeKeyEntry(value, latest)) == nil {
					_ = updateKeyIndex(home, func(index *ledger.Object) {
						if row, ok := index.Get(storeName).(ledger.Object); ok {
							row = row.Clone()
							row.Set("backend", "file")
							row.Set("exp", latest)
							index.Set(storeName, row)
						}
					})
				}
				return okRow("file", &latest), secrets
			}, 0
		default:
			return fixed(okRow("file", exp), value), 0
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		reasons = append(reasons, "key store error")
	}
	if opRef == "" {
		if before == nil {
			return fixed(doctorRow{"keys", "WARN", "keys", "jev key not resolved" + keyReasons(reasons) + "; System 1 falls back to rules"}, secrets...), 0
		}
		return func() (doctorRow, []string) {
			before()
			return doctorRow{"keys", "WARN", "keys", "jev key not resolved" + keyReasons(reasons) + "; System 1 falls back to rules"}, secrets
		}, 0
	}
	if before == nil && !keyIndexReadable(home, storeName, false) {
		return nil, hDoctorKeyRefresh
	}
	return func() (doctorRow, []string) {
		if before != nil {
			before()
		}
		value, kind := opRead(opRef)
		if kind != "" {
			reasons = append(reasons, opMessages[kind])
			return doctorRow{"keys", "WARN", "keys", "jev key not resolved" + keyReasons(reasons) + "; System 1 falls back to rules"}, secrets
		}
		secrets = append(secrets, value)
		exp := latest
		if writePrivateFile(keyFile, encodeKeyEntry(value, exp)) == nil {
			_ = updateKeyIndex(home, func(index *ledger.Object) {
				index.Set(storeName, ledger.Object{{Key: "source", Value: "op"}, {Key: "backend", Value: "file"}, {Key: "exp", Value: exp}, {Key: "ref", Value: opRef}})
			})
		}
		return okRow("op", &exp), secrets
	}, 0
}

// decodeKeyEntry is decodeEntry: our {v, exp} JSON, else the raw text with no
// expiry. A text Go's JSON reader cannot judge like JSON.parse is not ok.
func decodeKeyEntry(text string) (string, *float64, bool) {
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "{") {
			return "", nil, false
		}
		return text, nil, true
	}
	obj, ok := parsed.(map[string]any)
	if !ok {
		return text, nil, true
	}
	v, ok := obj["v"].(string)
	if !ok {
		return text, nil, true
	}
	if strings.ContainsRune(v, utf8.RuneError) {
		return "", nil, false // a lone surrogate escape: JSON.parse keeps it, Go does not
	}
	if exp, ok := obj["exp"].(float64); ok {
		return v, &exp, true
	}
	return v, nil, true
}

// ── reviewer rows: modelStats + reviewerLines (src/review/model-stats.mjs) ──

var isoTimestamp = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:T(\d{2}):(\d{2})(?::(\d{2})(?:\.(\d{3}))?)?(Z|([+-])(\d{2}):(\d{2})))?$`)

// jsDateParse is Date.parse for the forms Go reads the same way: a missing,
// null or boolean value (NaN) and the ISO date or date-time with Z or an
// offset. ok is false for any other value; the caller hands over.
func jsDateParse(value any) (ms float64, ok bool) {
	switch v := value.(type) {
	case nil, bool:
		return math.NaN(), true
	case string:
		m := isoTimestamp.FindStringSubmatch(v)
		if m == nil {
			return 0, false
		}
		n := func(s string) int { i, _ := strconv.Atoi(s); return i }
		year, month, day := n(m[1]), n(m[2]), n(m[3])
		hour, minute, second, milli := n(m[4]), n(m[5]), n(m[6]), n(m[7])
		if month < 1 || month > 12 || day < 1 || day > time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day() ||
			hour > 23 || minute > 59 || second > 59 {
			return 0, false
		}
		t := time.Date(year, time.Month(month), day, hour, minute, second, milli*int(time.Millisecond), time.UTC)
		if m[9] != "" {
			oh, om := n(m[10]), n(m[11])
			if oh > 23 || om > 59 {
				return 0, false
			}
			offset := time.Duration(oh)*time.Hour + time.Duration(om)*time.Minute
			if m[9] == "+" {
				t = t.Add(-offset)
			} else {
				t = t.Add(offset)
			}
		}
		return float64(t.UnixMilli()), true
	}
	return 0, false
}

// jsonKeyOf is JSON.stringify([...values]) for primitive values (a missing
// value is undefined, written null). Objects and arrays are not ok.
func jsonKeyOf(values ...any) (string, bool) {
	for _, v := range values {
		switch t := v.(type) {
		case string:
			if strings.ContainsRune(t, utf8.RuneError) {
				return "", false // Go read a lone surrogate escape as U+FFFD
			}
		case nil, bool, float64:
		default:
			return "", false
		}
	}
	data, err := ledger.Stringify(values, false)
	return string(data), err == nil
}

// jsString is `${value}` for a primitive.
func jsString(value any) (string, bool) {
	switch v := value.(type) {
	case nil:
		return "null", true
	case bool:
		return strconv.FormatBool(v), true
	case string:
		return v, true
	case float64:
		data, err := ledger.Stringify(v, false)
		return string(data), err == nil
	}
	return "", false
}

func lessUTF16(a, b string) bool {
	x, y := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := 0; i < len(x) && i < len(y); i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return len(x) < len(y)
}

// doctorReviewerLines returns the reviewer lines doctor prints, or ok=false
// when a row's shape decides the bytes in a way Go does not reproduce. A
// ledger Node cannot read gives no lines, as Node's catch does.
func doctorReviewerLines(cfg map[string]any, home string, now float64) ([]string, bool) {
	project, _ := cfg["project"].(map[string]any)
	slugValue, present := project["slug"]
	if !present || slugValue == nil || slugValue == false || slugValue == "" || slugValue == float64(0) {
		return nil, true // `cfg?.project?.slug` is falsy: no reviewer rows
	}
	slug, isString := slugValue.(string)
	if !isString || !regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`).MatchString(slug) {
		return nil, true // ledgerPath throws; doctor catches
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), slug)
	if err != nil {
		return nil, true
	}
	windowDays := 14.0
	if adaptive, ok := valueAt(cfg, "review", "adaptive"); ok {
		switch a := adaptive.(type) {
		case nil:
			return nil, true // destructuring null throws; doctor catches
		case map[string]any:
			if _, hasNow := a["now"]; hasNow {
				return nil, false
			}
			if w, ok := a["window_days"]; ok {
				days, isNum := w.(float64)
				if !isNum {
					return nil, false
				}
				windowDays = days
			}
		default:
			return nil, false
		}
	}
	return reviewerLinesFor(rows, now, windowDays)
}

type attempt struct {
	row        map[string]any
	at         float64
	diagnosed  bool
	group      string
	statusText any
}

// reviewerLinesFor is reviewerLines(modelStats(rows, {now, window_days})).
func reviewerLinesFor(rows []ledger.Row, now, windowDays float64) ([]string, bool) {
	since := now - windowDays*86400000
	type modelData struct {
		attempts []*attempt
		findings map[string]map[string]any
		order    []string
	}
	models := map[string]*modelData{}
	of := func(model string) *modelData {
		if models[model] == nil {
			models[model] = &modelData{findings: map[string]map[string]any{}}
		}
		return models[model]
	}
	var recent []map[string]any
	var recentAt []float64
	for _, raw := range rows {
		r, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		event, _ := r["event"].(string)
		_, modelIsString := r["model"].(string)
		_, sessionsIsList := r["sessions"].([]any)
		relevant := (event == "session" && r["role"] == "reviewer" && modelIsString) ||
			(event == "review.schema_invalid" && r["role"] == "reviewer") ||
			(event == "review.result" && sessionsIsList) || event == "review.waived" || event == "review.unwaived"
		if !relevant {
			continue
		}
		at, ok := jsDateParse(r["ts"])
		if !ok {
			return nil, false
		}
		if at >= since && at <= now {
			recent = append(recent, r)
			recentAt = append(recentAt, at)
		}
	}
	rawGroups := map[string]bool{}
	for i, r := range recent {
		if r["event"] != "session" {
			continue
		}
		group, ok := jsonKeyOf(r["run"], r["block"], r["model"], r["lens"])
		if !ok {
			return nil, false
		}
		model := r["model"].(string)
		of(model).attempts = append(of(model).attempts, &attempt{row: r, at: recentAt[i], group: group, statusText: r["status"]})
		rawGroups[group] = true
	}
	for i, r := range recent {
		if r["event"] != "review.schema_invalid" {
			continue
		}
		model, ok := r["model"].(string)
		if !ok || models[model] == nil {
			continue
		}
		group, ok := jsonKeyOf(r["run"], r["block"], r["model"], r["lens"])
		if !ok {
			return nil, false
		}
		list := models[model].attempts
		for j := len(list) - 1; j >= 0; j-- {
			s := list[j]
			if !s.diagnosed && s.group == group && s.at <= recentAt[i] {
				s.statusText = "invalid-output"
				s.diagnosed = true
				break
			}
		}
	}
	findingKey := func(r map[string]any, id any) (string, bool) {
		return jsonKeyOf(r["run"], r["block"], r["file"], id)
	}
	for _, r := range recent {
		if r["event"] != "review.result" {
			continue
		}
		for _, item := range r["sessions"].([]any) {
			s, ok := item.(map[string]any)
			if !ok || s["role"] != "reviewer" {
				continue
			}
			model, ok := s["model"].(string)
			if !ok {
				continue
			}
			data := of(model)
			pick := func(key string) any {
				if v, present := s[key]; present {
					return v
				}
				return r[key]
			}
			group, ok := jsonKeyOf(pick("run"), pick("block"), pick("model"), pick("lens"))
			if !ok {
				return nil, false
			}
			if !rawGroups[group] {
				data.attempts = append(data.attempts, &attempt{row: s, at: math.NaN(), group: group, statusText: s["status"]})
			}
			var findings []any
			switch f := s["findings"].(type) {
			case nil, string:
			case []any:
				findings = f
			default:
				return nil, true // for…of over a non-iterable throws; doctor catches
			}
			section, hasSection := s["section"]
			for _, item := range findings {
				f, _ := item.(map[string]any)
				id, ok := f["id"].(string)
				if !ok {
					continue
				}
				if hasSection {
					text, ok := jsString(section)
					if !ok {
						return nil, false
					}
					id = "S" + text + "." + id
				}
				key, ok := findingKey(r, id)
				if !ok {
					return nil, false
				}
				if _, seen := data.findings[key]; !seen {
					data.order = append(data.order, key)
				}
				data.findings[key] = f
			}
		}
	}
	waived := map[string]bool{}
	for _, r := range recent {
		if r["event"] != "review.waived" && r["event"] != "review.unwaived" {
			continue
		}
		key, ok := findingKey(r, r["finding"])
		if !ok {
			return nil, false
		}
		waived[key] = r["event"] == "review.waived"
	}
	jsRound := func(v float64) float64 {
		f := math.Floor(v)
		if v-f >= 0.5 {
			f++
		}
		return f
	}
	var names []string
	for model := range models {
		if strings.ContainsRune(model, utf8.RuneError) {
			return nil, false
		}
		if model != "__proto__" {
			names = append(names, model)
		}
	}
	sort.Slice(names, func(i, j int) bool { return lessUTF16(names[i], names[j]) })
	var lines []string
	for _, model := range names {
		data := models[model]
		var seconds []float64
		ok := 0
		for _, a := range data.attempts {
			if d, isNum := a.row["duration_ms"].(float64); isNum && d >= 0 {
				seconds = append(seconds, d/1000)
			}
			if a.statusText == "ok" {
				ok++
			}
		}
		if len(seconds) == 0 {
			continue
		}
		sort.Float64s(seconds)
		n := len(data.attempts)
		mid := len(seconds) / 2
		median := (seconds[mid] + seconds[int(math.Ceil(float64(len(seconds))/2))-1]) / 2
		availability := float64(ok) / float64(n)
		share := 0.0
		if len(data.findings) > 0 {
			count := 0
			for _, key := range data.order {
				if waived[key] {
					count++
				}
			}
			share = float64(count) / float64(len(data.findings))
		}
		medianText, _ := jsString(median)
		lines = append(lines, fmt.Sprintf("reviewer %s: %d sessions, median %s s, ok %s%%, waived %s%%", model, n, medianText,
			strconv.FormatFloat(jsRound(availability*100), 'f', -1, 64), strconv.FormatFloat(jsRound(share*100), 'f', -1, 64)))
	}
	return lines, true
}
