package main

import (
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/owned"
)

// B827: how `proof red-green` runs a test file that is not a node:test file,
// as src/proof/runners.mjs (af02c208^): the first proof.runners entry of the
// run snapshot whose match covers the --test file, else the Laravel/Pest
// default for a .php test; argv tokens expanded as whole items; each step in
// a fresh report dir outside the work dir; the env without inherited GIT_*
// plus the entry's variables; the xcodebuild precheck before the export.

const (
	prDefaultTimeoutS   = 600
	prReportName        = "report.xml"
	prResultBundleName  = "r.xcresult"
	prPrecheckTimeout   = 30 * time.Second
	prMaxNamedSims      = 10
	prMaskMinLength     = 8
	prSwiftNoMainReport = "swift test wrote a Swift Testing report but no XCTest report at {report}: keep --parallel in the runner argv (SwiftPM 6.3 writes the XCTest report only with it)"
)

var prSimulatorRuntimes = map[string]string{
	"iOS Simulator": "iOS", "watchOS Simulator": "watchOS", "tvOS Simulator": "tvOS", "visionOS Simulator": "xrOS",
}

var prRuntimeKey = regexp.MustCompile(`^com\.apple\.CoreSimulator\.SimRuntime\.([A-Za-z]+)-(\d+(?:-\d+)*)$`)

// prRunner is one proof.runners entry (RunnerEntry), already validated.
type prRunner struct {
	match   []string
	adapter string
	argv    []string
	env     map[string]string
	timeout time.Duration
	xcode   map[string]any
}

func prStringList(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

// prCheckedRunners is checkedRunners: every entry is checked again (a record
// is a file on disk); one problem refuses the proof.
func prCheckedRunners(list []any) ([]prRunner, error) {
	problems := []string{}
	for i, entry := range list {
		problems = append(problems, configValidator{}.runnerMessages(entry, i)...)
	}
	if len(problems) > 0 {
		return nil, &proofError{"runner-invalid", "the run snapshot's proof.runners is invalid (" + strings.Join(problems, "; ") + "); no proof was run"}
	}
	out := make([]prRunner, 0, len(list))
	for _, value := range list {
		entry := value.(map[string]any)
		r := prRunner{match: prStringList(entry["match"]), argv: prStringList(entry["argv"]), timeout: prDefaultTimeoutS * time.Second}
		r.adapter, _ = entry["adapter"].(string)
		if env, ok := entry["env"].(map[string]any); ok {
			r.env = map[string]string{}
			for k, v := range env {
				r.env[k], _ = v.(string)
			}
		}
		if t, ok := jsInteger(entry["timeout_s"]); ok {
			r.timeout = time.Duration(t) * time.Second
		}
		r.xcode, _ = entry["xcode"].(map[string]any)
		out = append(out, r)
	}
	return out, nil
}

func (r *prRunner) matches(file string) bool {
	return slices.ContainsFunc(r.match, func(glob string) bool { return owned.GlobMatch(file, glob) })
}

// prSelectRunner is selectRunner: the first entry whose match covers file.
func prSelectRunner(runners []prRunner, file string) *prRunner {
	for i := range runners {
		if runners[i].matches(file) {
			return &runners[i]
		}
	}
	return nil
}

// prRunnerCovers is runnerCovers: a file a runner matches is a test, never a source.
func prRunnerCovers(runners []prRunner, file string) bool {
	return prSelectRunner(runners, file) != nil
}

// prPestRunner is defaultPestRunner: a .php test in a Laravel project runs
// `php artisan test`, in a Pest project `vendor/bin/pest`, with a JUnit report.
func prPestRunner(dir, file string, hasCase bool) *prRunner {
	if !strings.HasSuffix(file, ".php") {
		return nil
	}
	isFile := func(rel string) bool {
		info, err := os.Stat(filepath.Join(dir, rel))
		return err == nil && info.Mode().IsRegular()
	}
	var command []string
	switch {
	case isFile("artisan"):
		command = []string{"php", "artisan", "test"}
	case isFile("vendor/bin/pest") || prComposerHasPest(dir):
		command = []string{"vendor/bin/pest"}
	default:
		return nil
	}
	argv := append(command, "{test_file}", "--log-junit", "{report}")
	if hasCase {
		argv = append(argv, "--filter", "{test_filter}")
	}
	return &prRunner{match: []string{file}, adapter: "junit", argv: argv, timeout: prDefaultTimeoutS * time.Second}
}

// prComposerHasPest: composer.json requires pestphp/pest (require or require-dev).
func prComposerHasPest(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		return false
	}
	var composer any
	if json.Unmarshal(data, &composer) != nil {
		return false
	}
	for _, key := range []string{"require", "require-dev"} {
		if _, ok := jsObject(jsObject(composer)[key])["pestphp/pest"].(string); ok {
			return true
		}
	}
	return false
}

func prRegexEscape(s string) string { return proofRegexMeta.ReplaceAllString(s, `\$0`) }

func prUsage(message string) error { return &proofError{"usage", message} }

func prInvalid(message string) error { return &proofError{"runner-invalid", message} }

// prXcodeTestPath is xcodeTestPath: the ::case (<Suite> or <Suite>/<test>),
// or the file's base name without .swift.
func prXcodeTestPath(file, testCase string, hasCase bool) (string, error) {
	if !hasCase {
		return strings.TrimSuffix(path.Base(file), ".swift"), nil
	}
	if strings.HasPrefix(testCase, "-") {
		return "", prUsage(`the test case starts with "-" (it would be read as an option)`)
	}
	if strings.HasPrefix(testCase, "/") || strings.HasSuffix(testCase, "/") || strings.Contains(testCase, "//") {
		return "", prUsage("an xcodebuild test case is <Suite> or <Suite>/<test>, with no empty part")
	}
	if len(strings.Split(testCase, "/")) > 2 {
		return "", prUsage("an xcodebuild test case is <Suite> or <Suite>/<test>; a nested Swift Testing suite (Outer/Inner/test()) is not supported — give the outer <Suite> to run all of it")
	}
	return testCase, nil
}

func (r *prRunner) xcodeTarget() (string, error) {
	target, ok := r.xcode["test_target"].(string)
	if !ok || target == "" || strings.Contains(target, "/") {
		return "", prInvalid("the xcodebuild runner's xcode.test_target is not set to one target name; no proof was run")
	}
	return target, nil
}

// expand is expandRunnerArgv: the entry's argv with its tokens replaced.
func (r *prRunner) expand(file, testCase string, hasCase bool, report, resultBundle string) ([]string, error) {
	if strings.HasPrefix(file, "-") {
		return nil, prUsage("the test path " + jsonOf(file) + ` starts with "-" (it would be read as an option)`)
	}
	known := runnerAdapterTokens[r.adapter]
	if hasCase && testCase == "" {
		return nil, prUsage("--test <file>::<case> needs a case name after ::")
	}
	out := make([]string, len(r.argv))
	if r.adapter == "xcodebuild" {
		target, err := r.xcodeTarget()
		if err != nil {
			return nil, err
		}
		testPath, err := prXcodeTestPath(file, testCase, hasCase)
		if err != nil {
			return nil, err
		}
		if resultBundle == "" {
			resultBundle = filepath.Join(filepath.Dir(report), prResultBundleName)
		}
		for i, item := range r.argv {
			if i == 0 || !slices.Contains(known, item) {
				out[i] = item
				continue
			}
			var value any
			switch item {
			case "{only_testing}":
				value = "-only-testing:" + target + "/" + testPath
			case "{result_bundle}":
				value = resultBundle
			default:
				value = r.xcode[strings.Trim(item, "{}")]
			}
			s, ok := value.(string)
			if !ok {
				return nil, prInvalid("the xcodebuild runner's " + item + " has no value in its xcode object; no proof was run")
			}
			out[i] = s
		}
		return out, nil
	}
	if slices.Contains(known, "{test_filter}") && slices.Contains(r.argv[1:], "{test_filter}") {
		if !hasCase && r.adapter != "swift" {
			return nil, prUsage("the " + r.adapter + " runner for " + file + " filters by test case — give --test <file>::<case>")
		}
		if hasCase && strings.HasPrefix(testCase, "-") {
			return nil, prUsage(`the test case starts with "-" (it would be read as an option)`)
		}
	}
	values := map[string]string{"{test_file}": file, "{test_id}": file, "{report}": report}
	switch {
	case !hasCase:
		values["{test_filter}"] = `\.` + prRegexEscape(strings.TrimSuffix(path.Base(file), ".swift")) + "/"
	case r.adapter == "swift":
		values["{test_filter}"] = `(?:^|[./])` + prRegexEscape(testCase) + `(?![^/])`
	default:
		values["{test_filter}"] = prRegexEscape(testCase)
	}
	if hasCase {
		values["{test_id}"] = file + "::" + testCase
	}
	for i, item := range r.argv {
		if i > 0 && slices.Contains(known, item) {
			out[i] = values[item]
		} else {
			out[i] = item
		}
	}
	return out, nil
}

// envKeys is runnerEnvKeys: the entry's variable names, sorted.
func (r *prRunner) envKeys() []string {
	keys := make([]string, 0, len(r.env))
	for k := range r.env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// childEnv is runnerEnv: env without any inherited GIT_* variable, plus the entry's.
func (r *prRunner) childEnv(env map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range env {
		if !strings.HasPrefix(k, "GIT_") {
			out[k] = v
		}
	}
	for k, v := range r.env {
		out[k] = v
	}
	return out
}

// prReportBase is the temp base a step's report dir is made under (the
// CLI's tmp.root through CODE_FORGE_TMP_ROOT, else the OS temp dir).
func prReportBase() string {
	if root := os.Getenv("CODE_FORGE_TMP_ROOT"); filepath.IsAbs(root) {
		return root
	}
	return os.TempDir()
}

// prHook is RunnerHook: what redGreen calls in place of its node:test steps.
type prHook struct {
	runner         *prRunner
	file, testCase string
	hasCase        bool
	runID, scope   string
}

// prNewHook is runnerHook: the expansion is checked up front (a usage error
// changes nothing) and the entry's long env values are registered as secrets.
func prNewHook(r *prRunner, file, testCase string, hasCase bool, runID string) (*prHook, error) {
	if _, err := r.expand(file, testCase, hasCase, filepath.Join(prReportBase(), prReportName), ""); err != nil {
		return nil, err
	}
	for _, value := range r.env {
		if len(utf16.Encode([]rune(value))) >= prMaskMinLength {
			autopilotRegisterSecret(value)
		}
	}
	h := &prHook{runner: r, file: file, testCase: testCase, hasCase: hasCase, runID: runID}
	if r.adapter == "xcodebuild" {
		target, _ := r.xcodeTarget()
		testPath, _ := prXcodeTestPath(file, testCase, hasCase)
		h.scope = target + "/" + strings.Split(testPath, "/")[0] + "/"
	}
	return h, nil
}

// prStep is one RED or GREEN run with what it read.
type prStep struct {
	run     *proofExecResult
	reports []prReport
	noMain  bool
	names   []string
}

func (h *prHook) step(cwd string, env map[string]string, timeout time.Duration) (*prStep, error) {
	base := prReportBase()
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(base, "proof-report-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	report := filepath.Join(dir, prReportName)
	r := h.runner
	if r.adapter == "xcodebuild" {
		bundle := filepath.Join(dir, prResultBundleName)
		argv, err := r.expand(h.file, h.testCase, h.hasCase, report, bundle)
		if err != nil {
			return nil, err
		}
		childEnv := r.childEnv(env)
		run := proofExec(argv, cwd, childEnv, h.runID, timeout)
		return &prStep{run: run, reports: []prReport{prLoadXcresult(bundle, dir, childEnv, h.runID)}}, nil
	}
	argv, err := r.expand(h.file, h.testCase, h.hasCase, report, "")
	if err != nil {
		return nil, err
	}
	run := proofExec(argv, cwd, r.childEnv(env), h.runID, timeout)
	if r.adapter != "swift" {
		return &prStep{run: run, reports: []prReport{prLoadReport(report)}}, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".xml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return &prStep{run: run, reports: []prReport{prLoadReport(report)}, names: names}, nil
	}
	s := &prStep{run: run, names: names, noMain: !slices.Contains(names, prReportName)}
	if s.noMain {
		s.reports = append(s.reports, prReport{unreadable: prSwiftNoMainReport})
	}
	for _, name := range names {
		s.reports = append(s.reports, prLoadReport(filepath.Join(dir, name)))
	}
	return s, nil
}

// red runs the RED step and classifies it.
func (h *prHook) red(cwd string, env map[string]string, timeout time.Duration) (*prRed, error) {
	s, err := h.step(cwd, env, timeout)
	if err != nil {
		return nil, err
	}
	switch h.runner.adapter {
	case "xcodebuild":
		red := prClassifyXcodeRed(s.run, s.reports[0], h.scope)
		return &red, nil
	case "swift":
		red := prClassifyReport(s.run, s.reports, prSwiftCompileError)
		if s.noMain {
			red = prRed{verdict: "RED_INVALID", kind: "error", failedTests: []string{}}
		}
		red.names, red.swift = s.names, true
		return &red, nil
	}
	red := prClassifyReport(s.run, s.reports, nil)
	return &red, nil
}

// green runs the GREEN step: only the exact word GREEN proves; a step that
// fails to run is NOT_GREEN.
func (h *prHook) green(cwd string, env map[string]string, timeout time.Duration, red *prRed) string {
	s, err := h.step(cwd, env, timeout)
	if err != nil {
		return "NOT_GREEN"
	}
	switch h.runner.adapter {
	case "xcodebuild":
		return prClassifyXcodeGreen(s.run, s.reports[0], red.failedIDs, h.scope)
	case "swift":
		if !red.swift || slices.ContainsFunc(red.names, func(name string) bool { return !slices.Contains(s.names, name) }) {
			return "NOT_GREEN"
		}
	}
	return prClassifyGreen(s.run, s.reports, red.failedIDs)
}

// prPrecheck is adapterPrecheck: xcodebuild needs full Xcode, and a simulator
// destination a device simctl lists as available. It only reads.
func prPrecheck(r *prRunner, runID string) error {
	if r.adapter != "xcodebuild" {
		return nil
	}
	env := r.childEnv(proofTestEnv())
	run := func(argv ...string) (bool, string) {
		res := proofExec(argv, "", env, runID, prPrecheckTimeout)
		return res.ok(), res.stdout
	}
	ok, developerDir := run("xcode-select", "-p")
	developerDir = rgJSTrim(developerDir)
	if !ok || developerDir == "" || strings.Contains(developerDir, "CommandLineTools") {
		return &proofError{"xcode-missing", "xcode-missing: xcode-select -p does not point at a full Xcode (Command Line Tools only, or no developer directory); the xcodebuild runner needs Xcode — install it and run xcode-select -s; no proof was run"}
	}
	destination, _ := r.xcode["destination"].(string)
	pairs := map[string]string{}
	for _, part := range strings.Split(destination, ",") {
		eq := strings.Index(part, "=")
		if eq <= 0 {
			pairs = nil
			break
		}
		key := rgJSTrim(part[:eq])
		if _, dup := pairs[key]; key == "" || dup {
			pairs = nil
			break
		}
		pairs[key] = rgJSTrim(part[eq+1:])
	}
	if pairs == nil {
		return &proofError{"destination-unavailable", "destination-unavailable: the runner's xcode.destination is not a list of key=value pairs; no proof was run"}
	}
	family, simulator := prSimulatorRuntimes[pairs["platform"]]
	if !simulator {
		return nil
	}
	name, hasName := pairs["name"]
	id, hasID := pairs["id"]
	osVersion, hasOS := pairs["OS"]
	if !hasName && !hasID {
		return &proofError{"destination-unavailable", "destination-unavailable: the runner's xcode.destination names no simulator: a simulator destination needs name= or id=; no proof was run"}
	}
	ok, listed := run("xcrun", "simctl", "list", "devices", "available", "-j")
	var sims []prSimulator
	if ok {
		sims = prAvailableSimulators(listed, family)
	}
	if sims == nil {
		return &proofError{"destination-unavailable", "destination-unavailable: xcrun simctl did not list the available simulators; no proof was run"}
	}
	for _, s := range sims {
		if (!hasName || s.name == name) && (!hasID || s.udid == id) && (!hasOS || osVersion == "latest" || s.os == osVersion || strings.HasPrefix(s.os, osVersion+".")) {
			return nil
		}
	}
	names := []string{}
	for _, s := range sims {
		if !slices.Contains(names, s.name) {
			names = append(names, s.name)
		}
	}
	sort.Strings(names)
	if len(names) > prMaxNamedSims {
		names = names[:prMaxNamedSims]
	}
	available := "none"
	if len(names) > 0 {
		available = strings.Join(names, ", ")
	}
	return &proofError{"destination-unavailable", "destination-unavailable: the runner's xcode.destination names no simulator that xcrun simctl lists as available (by name or id, and OS when given); available: " + available + " — code-forge never boots, creates or deletes a simulator; no proof was run"}
}

type prSimulator struct{ name, udid, os string }

// prAvailableSimulators is availableSimulators: nil when the output is not
// the shape this reader knows.
func prAvailableSimulators(text, family string) []prSimulator {
	var doc any
	if json.Unmarshal([]byte(text), &doc) != nil {
		return nil
	}
	devices, ok := jsObject(doc)["devices"].(map[string]any)
	if !ok {
		return nil
	}
	out := []prSimulator{}
	for runtime, value := range devices {
		list, ok := value.([]any)
		if !ok {
			return nil
		}
		m := prRuntimeKey.FindStringSubmatch(runtime)
		if m == nil || m[1] != family {
			continue
		}
		for _, item := range list {
			device, ok := item.(map[string]any)
			if !ok {
				return nil
			}
			name, okName := device["name"].(string)
			udid, okUDID := device["udid"].(string)
			if !okName || !okUDID {
				return nil
			}
			if device["isAvailable"] != true {
				continue
			}
			out = append(out, prSimulator{name, udid, strings.ReplaceAll(m[2], "-", ".")})
		}
	}
	return out
}

// ── reports (junit.mjs) ──

const (
	prMaxReportBytes = 8 * 1024 * 1024
	prMaxFailedTests = 20
	prMaxTestName    = 200
)

// prTest is ReportTest.
type prTest struct {
	id, status, kind string
	needsConsole     bool
}

// prReport is ReportResult: tests, or unreadable (noReport: the file did not exist).
type prReport struct {
	tests      []prTest
	unreadable string
	noReport   bool
}

func (r prReport) readable() bool { return r.unreadable == "" && !r.noReport }

// prRed is JUnitRedParse.
type prRed struct {
	verdict, kind string
	failed        any
	failedTests   []string
	failedIDs     []string
	names         []string // swift: the report files RED read
	swift         bool
}

var (
	prAssertionPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\bAssertionError\b`), regexp.MustCompile(`\bERR_ASSERTION\b`), regexp.MustCompile(`\bExpectationFailedException\b`),
		regexp.MustCompile(`\bAssertionFailedError\b`), regexp.MustCompile(`\bFailed asserting that\b`), regexp.MustCompile(`\bXCTAssert\w*`),
		regexp.MustCompile(`\bXCTFail\b`), regexp.MustCompile(`\bExpectation failed\b`), jsRe(`^\s*assert\s`),
	}
	prXCTestLine   = regexp.MustCompile(`:\d+: error: (?:-\[([^\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}\]]+) ([^\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}\]]+)\]|([^\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}:]+)) : (?:XCTAssert\w* failed\b|failed - )`) // JS \s spelled out: jsRe cannot nest it inside a class
	prSwiftCompile = []*regexp.Regexp{regexp.MustCompile(`\.swift:\d+:\d+: error: `), jsRe(`<BOL>error: (?:emit-module command failed|compile command failed)\b`)}
	prSwiftCrash   = regexp.MustCompile(`\bFatal error\b|\bPrecondition failed\b|\bAssertion failed\b|\bUnexpectedly found nil\b|\bsignal code \d+|\bIllegal instruction\b|\bSegmentation fault\b`)
	prXMLName      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.:-]*`)
	prCharRef      = regexp.MustCompile(`^#(?:([0-9]{1,7})|x([0-9A-Fa-f]{1,6}))$`)
	prJUnitParents = map[string][]string{
		"testsuites": {""}, "testsuite": {"", "testsuites", "testsuite"}, "testcase": {"testsuite"},
		"failure": {"testcase"}, "error": {"testcase"}, "skipped": {"testcase"},
		"system-out": {"testsuites", "testsuite", "testcase"}, "system-err": {"testsuites", "testsuite", "testcase"},
		"properties": {"testsuites", "testsuite", "testcase"}, "property": {"properties"},
	}
	prPredefined = map[string]string{"lt": "<", "gt": ">", "amp": "&", "quot": `"`, "apos": "'"}
)

type prUnreadable string

func prFail(reason string) { panic(prUnreadable(reason)) }

func prXMLChar(cp rune) bool {
	return cp == 0x9 || cp == 0xa || cp == 0xd || cp >= 0x20 && cp <= 0xd7ff || cp >= 0xe000 && cp <= 0xfffd || cp >= 0x10000 && cp <= 0x10ffff
}

func prXMLSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

func prAllSpace(s string) bool {
	for i := 0; i < len(s); i++ {
		if !prXMLSpace(s[i]) {
			return false
		}
	}
	return true
}

// prDecode is decode: the five predefined entities and character references.
func prDecode(raw string) string {
	if !strings.Contains(raw, "&") {
		return raw
	}
	var out strings.Builder
	i := 0
	for {
		amp := strings.Index(raw[i:], "&")
		if amp < 0 {
			out.WriteString(raw[i:])
			return out.String()
		}
		amp += i
		out.WriteString(raw[i:amp])
		semi := strings.Index(raw[amp:], ";")
		if semi < 0 || semi > 12 {
			prFail("a bare & or an unknown entity")
		}
		semi += amp
		ref := raw[amp+1 : semi]
		if v, ok := prPredefined[ref]; ok {
			out.WriteString(v)
		} else {
			m := prCharRef.FindStringSubmatch(ref)
			if m == nil {
				prFail("an unknown entity")
			}
			var cp int64
			for _, c := range m[1] {
				cp = cp*10 + int64(c-'0')
			}
			for _, c := range m[2] {
				cp = cp*16 + int64(strings.IndexRune("0123456789abcdef", c|0x20))
			}
			if cp > 0x10ffff || !prXMLChar(rune(cp)) {
				prFail("a character reference that is not an XML character")
			}
			out.WriteRune(rune(cp))
		}
		i = semi + 1
	}
}

func prSkipSpace(text string, i int) int {
	for i < len(text) && prXMLSpace(text[i]) {
		i++
	}
	return i
}

func prFailureKind(attrs map[string]string) string {
	if t := rgJSTrim(attrs["type"]); t != "" {
		if slices.ContainsFunc(prAssertionPatterns, func(p *regexp.Regexp) bool { return p.MatchString(t) }) {
			return "assertion"
		}
		return "error"
	}
	message := attrs["message"]
	if m := rgJSTrim(message); m == "failed" || m == "failure" {
		return "bare"
	}
	if slices.ContainsFunc(prAssertionPatterns, func(p *regexp.Regexp) bool { return p.MatchString(message) }) {
		return "assertion"
	}
	return "error"
}

// prReadJUnit is readJUnit: a strict reader of a JUnit XML report.
func prReadJUnit(data []byte) (report prReport) {
	defer func() {
		if r := recover(); r != nil {
			reason, ok := r.(prUnreadable)
			if !ok {
				reason = "the report could not be read"
			}
			report = prReport{unreadable: string(reason)}
		}
	}()
	if len(data) > prMaxReportBytes {
		prFail("the report is over 8 MiB")
	}
	if !utf8.Valid(data) {
		prFail("the report is not UTF-8")
	}
	text := strings.TrimPrefix(string(data), "\uFEFF")
	for _, r := range text {
		if !prXMLChar(r) {
			prFail("a character that is not an XML character")
		}
	}
	type openTest struct {
		id              string
		failures        []string
		errors, skipped int
	}
	type frame struct {
		name    string
		test    *openTest
		failure int
		body    bool
	}
	tests := []prTest{}
	stack := []*frame{}
	rootSeen := false
	open := func(name string, attrs map[string]string) {
		parents, known := prJUnitParents[name]
		if !known {
			prFail("an element this reader does not know")
		}
		parent := ""
		if len(stack) > 0 {
			parent = stack[len(stack)-1].name
		}
		if parent == "" && rootSeen {
			prFail("more than one root element")
		}
		if !slices.Contains(parents, parent) {
			prFail("an element in the wrong place")
		}
		if parent == "" {
			rootSeen = true
		}
		f := &frame{name: name, failure: -1}
		switch name {
		case "testcase":
			caseName := attrs["name"]
			if caseName == "" {
				prFail("a testcase without a name")
			}
			id := caseName
			if classname := attrs["classname"]; classname != "" {
				id = classname + "." + caseName
			}
			f.test = &openTest{id: id}
		case "failure":
			t := stack[len(stack)-1].test
			t.failures = append(t.failures, prFailureKind(attrs))
			f.failure = len(t.failures) - 1
		case "error":
			stack[len(stack)-1].test.errors++
		case "skipped":
			stack[len(stack)-1].test.skipped++
		}
		stack = append(stack, f)
	}
	closeTag := func(name string) {
		if len(stack) == 0 || stack[len(stack)-1].name != name {
			prFail("a mismatched end tag")
		}
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if f.failure >= 0 && f.body {
			owner := stack[len(stack)-1].test
			if owner.failures[f.failure] == "bare" {
				owner.failures[f.failure] = "error"
			}
		}
		t := f.test
		if t == nil {
			return
		}
		all := func(pred func(string) bool) bool {
			return !slices.ContainsFunc(t.failures, func(k string) bool { return !pred(k) })
		}
		switch {
		case t.errors > 0:
			tests = append(tests, prTest{id: t.id, status: "error", kind: "error"})
		case len(t.failures) > 0 && all(func(k string) bool { return k == "assertion" }):
			tests = append(tests, prTest{id: t.id, status: "failed", kind: "assertion"})
		case len(t.failures) > 0 && all(func(k string) bool { return k != "error" }):
			tests = append(tests, prTest{id: t.id, status: "failed", kind: "error", needsConsole: true})
		case len(t.failures) > 0:
			tests = append(tests, prTest{id: t.id, status: "failed", kind: "error"})
		case t.skipped > 0:
			tests = append(tests, prTest{id: t.id, status: "skipped"})
		default:
			tests = append(tests, prTest{id: t.id, status: "passed"})
		}
	}
	readAttributes := func(i int) (map[string]string, int, bool) {
		attrs := map[string]string{}
		for {
			j := prSkipSpace(text, i)
			hadSpace := j > i
			i = j
			if strings.HasPrefix(text[i:], "/>") {
				return attrs, i + 2, true
			}
			if i < len(text) && text[i] == '>' {
				return attrs, i + 1, false
			}
			if !hadSpace {
				prFail("a malformed start tag")
			}
			name := prXMLName.FindString(text[i:])
			if name == "" {
				prFail("a malformed attribute")
			}
			i = prSkipSpace(text, i+len(name))
			if i >= len(text) || text[i] != '=' {
				prFail("an attribute without a value")
			}
			i = prSkipSpace(text, i+1)
			if i >= len(text) || text[i] != '"' && text[i] != '\'' {
				prFail("an unquoted attribute value")
			}
			end := strings.IndexByte(text[i+1:], text[i])
			if end < 0 {
				prFail("an unterminated attribute value")
			}
			raw := text[i+1 : i+1+end]
			if strings.Contains(raw, "<") {
				prFail("a < inside an attribute value")
			}
			if _, dup := attrs[name]; dup {
				prFail("a duplicate attribute")
			}
			attrs[name] = prDecode(raw)
			i = i + 1 + end + 1
		}
	}
	i := 0
	for i < len(text) {
		lt := strings.IndexByte(text[i:], '<')
		end := len(text)
		if lt >= 0 {
			lt += i
			end = lt
		}
		chunk := text[i:end]
		if len(stack) == 0 {
			if !prAllSpace(chunk) {
				prFail("text outside the root element")
			}
		} else {
			prDecode(chunk)
			if !prAllSpace(chunk) {
				stack[len(stack)-1].body = true
			}
		}
		if lt < 0 {
			break
		}
		i = lt
		rest := text[i:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			e := strings.Index(text[i+4:], "-->")
			if e < 0 {
				prFail("an unterminated comment")
			}
			i = i + 4 + e + 3
		case strings.HasPrefix(rest, "<![CDATA["):
			if len(stack) == 0 {
				prFail("CDATA outside the root element")
			}
			e := strings.Index(text[i+9:], "]]>")
			if e < 0 {
				prFail("an unterminated CDATA section")
			}
			if !prAllSpace(text[i+9 : i+9+e]) {
				stack[len(stack)-1].body = true
			}
			i = i + 9 + e + 3
		case strings.HasPrefix(rest, "<!"):
			prFail("a DOCTYPE or entity declaration")
		case strings.HasPrefix(rest, "<?"):
			e := strings.Index(text[i+2:], "?>")
			if e < 0 {
				prFail("an unterminated processing instruction")
			}
			i = i + 2 + e + 2
		case strings.HasPrefix(rest, "</"):
			name := prXMLName.FindString(text[i+2:])
			if name == "" {
				prFail("a malformed end tag")
			}
			j := prSkipSpace(text, i+2+len(name))
			if j >= len(text) || text[j] != '>' {
				prFail("a malformed end tag")
			}
			closeTag(name)
			i = j + 1
		default:
			name := prXMLName.FindString(text[i+1:])
			if name == "" {
				prFail("a malformed start tag")
			}
			attrs, e, selfClosing := readAttributes(i + 1 + len(name))
			open(name, attrs)
			if selfClosing {
				closeTag(name)
			}
			i = e
		}
	}
	if len(stack) > 0 {
		prFail("an unclosed element")
	}
	if !rootSeen {
		prFail("no root element")
	}
	return prReport{tests: tests}
}

// prLoadReport is loadReport: never follows a symlink, never blocks on a FIFO.
func prLoadReport(file string) prReport {
	fd, err := syscall.Open(file, syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return prReport{noReport: true, unreadable: "no report was written"}
		}
		return prReport{unreadable: "the report could not be opened"}
	}
	f := os.NewFile(uintptr(fd), file)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return prReport{unreadable: "the report could not be read"}
	}
	if !info.Mode().IsRegular() {
		return prReport{unreadable: "the report is not a regular file"}
	}
	size := info.Size()
	if size > prMaxReportBytes {
		return prReport{unreadable: "the report is over 8 MiB"}
	}
	buf := make([]byte, size+1)
	n := 0
	for n < len(buf) {
		got, err := f.Read(buf[n:])
		n += got
		if got == 0 || err != nil {
			break
		}
	}
	if int64(n) != size {
		return prReport{unreadable: "the report changed while it was read"}
	}
	return prReadJUnit(buf[:n])
}

// prBoundedName: no C0 or C1 control characters, at most 200 characters.
func prBoundedName(id string) string {
	runes := []rune(id)
	for i, r := range runes {
		if r <= 0x1f || r >= 0x7f && r <= 0x9f {
			runes[i] = ' '
		}
	}
	if len(runes) > prMaxTestName {
		runes = runes[:prMaxTestName]
	}
	return string(runes)
}

// prMergeReports: no report, or any unreadable one, is not ok.
func prMergeReports(reports []prReport) ([]prTest, bool) {
	if len(reports) == 0 {
		return nil, false
	}
	all := []prTest{}
	for _, r := range reports {
		if !r.readable() {
			return nil, false
		}
		all = append(all, r.tests...)
	}
	return all, true
}

func prFailures(tests []prTest, ok bool) (any, []string, []string) {
	ids := []string{}
	for _, t := range tests {
		if t.status == "failed" || t.status == "error" {
			ids = append(ids, t.id)
		}
	}
	names := []string{}
	for i, id := range ids {
		if i == prMaxFailedTests {
			break
		}
		names = append(names, prBoundedName(id))
	}
	if !ok {
		return nil, names, ids
	}
	return float64(len(ids)), names, ids
}

func prExitedClean(run *proofExecResult) bool {
	return run.code != nil && *run.code == 0 && run.signal == "" && !run.timedOut
}

// prConfirmXCTest: a bare XCTest failure is an assertion only with its own
// XCTest assertion line in the console, and no crash line.
func prConfirmXCTest(tests []prTest, run *proofExecResult) []prTest {
	if !slices.ContainsFunc(tests, func(t prTest) bool { return t.needsConsole }) {
		return tests
	}
	text := proofANSI.ReplaceAllString(run.stdout+"\n"+run.stderr, "")
	ids, tails := map[string]bool{}, []string{}
	if !prSwiftCrash.MatchString(text) {
		for _, m := range prXCTestLine.FindAllStringSubmatch(text, -1) {
			if m[1] != "" {
				ids[m[1]+"."+m[2]] = true
			} else {
				tails = append(tails, m[3])
			}
		}
	}
	out := make([]prTest, len(tests))
	for i, t := range tests {
		seen := ids[t.id] || slices.ContainsFunc(tails, func(tail string) bool { return t.id == tail || strings.HasSuffix(t.id, "."+tail) })
		if t.needsConsole && seen {
			t = prTest{id: t.id, status: t.status, kind: "assertion"}
		}
		out[i] = t
	}
	return out
}

// prSwiftCompileError is swiftCompileError: a swiftc or SwiftPM build failure line.
func prSwiftCompileError(text string) bool {
	plain := proofANSI.ReplaceAllString(text, "")
	return slices.ContainsFunc(prSwiftCompile, func(p *regexp.Regexp) bool { return p.MatchString(plain) })
}

// prClassifyReport is classifyReport (the RED step).
func prClassifyReport(run *proofExecResult, reports []prReport, compileError func(string) bool) prRed {
	tests, ok := prMergeReports(reports)
	failed, failedTests, failedIDs := prFailures(tests, ok)
	out := func(verdict, kind string) prRed {
		return prRed{verdict: verdict, kind: kind, failed: failed, failedTests: failedTests, failedIDs: failedIDs}
	}
	if prExitedClean(run) {
		return out("NOT_RED", "none")
	}
	if run.timedOut || run.signal != "" {
		return out("RED_INVALID", "fatal")
	}
	if text := proofParseRed(run); text.verdict == "RED_INVALID" && (text.kind == "import" || text.kind == "compile" || text.kind == "fatal") {
		return out("RED_INVALID", text.kind)
	}
	noneWritten := len(reports) > 0 && !slices.ContainsFunc(reports, func(r prReport) bool { return !r.noReport })
	if !ok && noneWritten && compileError != nil && compileError(run.stdout+"\n"+run.stderr) {
		return out("RED_INVALID", "compile")
	}
	if !ok {
		return out("RED_INVALID", "error")
	}
	if !slices.ContainsFunc(tests, func(t prTest) bool { return t.status != "skipped" }) {
		return out("RED_INVALID", "error")
	}
	confirmed := prConfirmXCTest(tests, run)
	if slices.ContainsFunc(confirmed, func(t prTest) bool { return t.kind == "error" || t.status == "error" }) {
		return out("RED_INVALID", "error")
	}
	if slices.ContainsFunc(confirmed, func(t prTest) bool { return t.status == "failed" && t.kind == "assertion" }) {
		return out("RED", "assertion")
	}
	return out("RED_INVALID", "error")
}

// prClassifyGreen is classifyGreen: GREEN only when every test that failed
// at RED is in the report and passed, and nothing failed.
func prClassifyGreen(run *proofExecResult, reports []prReport, redFailed []string) string {
	tests, ok := prMergeReports(reports)
	switch {
	case !prExitedClean(run), !ok,
		!slices.ContainsFunc(tests, func(t prTest) bool { return t.status != "skipped" }),
		slices.ContainsFunc(tests, func(t prTest) bool { return t.status == "failed" || t.status == "error" }),
		len(redFailed) == 0:
		return "NOT_GREEN"
	}
	for _, id := range redFailed {
		runs := 0
		for _, t := range tests {
			if t.id == id {
				runs++
				if t.status != "passed" {
					return "NOT_GREEN"
				}
			}
		}
		if runs == 0 {
			return "NOT_GREEN"
		}
	}
	return "GREEN"
}

// ── xcresult (xcresult.mjs) ──

const prXcresulttoolTimeout = 60 * time.Second

var (
	prNodeParents = map[string][]string{
		"Test Plan": {""}, "Unit test bundle": {"Test Plan"}, "UI test bundle": {"Test Plan"},
		"Test Suite": {"Unit test bundle", "UI test bundle", "Test Suite"}, "Test Case": {"Test Suite"}, "Failure Message": {"Test Case"},
	}
	prResultStatus    = map[string]string{"Passed": "passed", "Failed": "failed", "Skipped": "skipped"}
	prMessagePrefix   = regexp.MustCompile(`^[^\n:]+\.swift:\d+: `)
	prXCTAssertFailed = regexp.MustCompile(`^XCTAssert\w* failed`)
	prBuildFailed     = []*regexp.Regexp{
		jsRe(`<BOL>\*\* (?:TEST )?BUILD FAILED \*\*[ \t\r]*<EOL>`),
		jsRe(`<BOL>[ \t]*Testing cancelled because the build failed\.[ \t\r]*<EOL>`),
	}
	prXcodeScope = regexp.MustCompile(`^[^/]+/[^/]+/$`)
)

// prFailureMessageKind is failureMessageKind.
func prFailureMessageKind(text string) string {
	rest := prMessagePrefix.ReplaceAllString(text, "")
	if m := prXCTAssertFailed.FindString(rest); m != "" && !strings.HasPrefix(rest[len(m):], ": threw error") {
		return "assertion"
	}
	if strings.HasPrefix(rest, "failed - ") || rest == "failed" || strings.HasPrefix(rest, "Expectation failed: ") {
		return "assertion"
	}
	return "error"
}

// prReadXcresult is readXcresult: the `xcresulttool get test-results tests` JSON.
func prReadXcresult(text string) (report prReport) {
	defer func() {
		if r := recover(); r != nil {
			reason, ok := r.(prUnreadable)
			if !ok {
				reason = "the result bundle could not be read"
			}
			report = prReport{unreadable: string(reason)}
		}
	}()
	if len(text) > prMaxReportBytes {
		prFail("the xcresulttool output is over 8 MiB")
	}
	var doc any
	if json.Unmarshal([]byte(text), &doc) != nil {
		prFail("the xcresulttool output is not JSON")
	}
	nodes, ok := jsObject(doc)["testNodes"].([]any)
	if !ok {
		prFail("the xcresulttool output has no testNodes list")
	}
	tests := []prTest{}
	seen := map[string]bool{}
	var walk func(value any, parent, bundle string)
	walk = func(value any, parent, bundle string) {
		node, ok := value.(map[string]any)
		if !ok {
			prFail("a test node that is not an object")
		}
		kind, _ := node["nodeType"].(string)
		parents, known := prNodeParents[kind]
		if !known {
			prFail("a node type this reader does not know")
		}
		if !slices.Contains(parents, parent) {
			prFail("a test node in the wrong place")
		}
		children := []any{}
		if c := node["children"]; c != nil {
			list, ok := c.([]any)
			if !ok {
				prFail("test node children that are not a list")
			}
			children = list
		}
		if kind == "Failure Message" {
			if _, ok := node["name"].(string); !ok {
				prFail("a failure message without a text")
			}
			if len(children) > 0 {
				prFail("a failure message with children")
			}
			return
		}
		if kind == "Unit test bundle" || kind == "UI test bundle" {
			name, _ := node["name"].(string)
			if name == "" || strings.Contains(name, "/") {
				prFail("a test bundle without a name")
			}
			bundle = name
		}
		if kind == "Test Case" {
			ident, _ := node["nodeIdentifier"].(string)
			if ident == "" {
				prFail("a test case without a nodeIdentifier")
			}
			result, _ := node["result"].(string)
			status, known := prResultStatus[result]
			if !known {
				prFail("a test result this reader does not know")
			}
			kinds := []string{}
			for _, child := range children {
				walk(child, "Test Case", bundle)
				name, _ := child.(map[string]any)["name"].(string)
				kinds = append(kinds, prFailureMessageKind(name))
			}
			id := bundle + "/" + ident
			if seen[id] {
				prFail("two test cases with the same id")
			}
			seen[id] = true
			switch {
			case status == "failed":
				kind := "error"
				if len(kinds) > 0 && !slices.ContainsFunc(kinds, func(k string) bool { return k != "assertion" }) {
					kind = "assertion"
				}
				tests = append(tests, prTest{id: id, status: status, kind: kind})
			case len(kinds) > 0:
				prFail("a failure message on a test that did not fail")
			default:
				tests = append(tests, prTest{id: id, status: status})
			}
			return
		}
		for _, child := range children {
			walk(child, kind, bundle)
		}
	}
	for _, node := range nodes {
		walk(node, "", "")
	}
	return prReport{tests: tests}
}

// prLoadXcresult is loadXcresult: lstat the bundle, then xcresulttool.
func prLoadXcresult(bundle, cwd string, env map[string]string, runID string) prReport {
	info, err := os.Lstat(bundle)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return prReport{noReport: true, unreadable: "no report was written"}
		}
		return prReport{unreadable: "the result bundle could not be opened"}
	}
	if !info.IsDir() {
		return prReport{unreadable: "the result bundle is not a directory"}
	}
	res := proofExec([]string{"xcrun", "xcresulttool", "get", "test-results", "tests", "--path", bundle}, cwd, env, runID, prXcresulttoolTimeout)
	if !res.ok() || res.signal != "" || len(res.stdout) > prMaxReportBytes {
		return prReport{unreadable: "xcresulttool failed"}
	}
	return prReadXcresult(res.stdout)
}

func prXcodeBuildFailed(text string) bool {
	plain := proofANSI.ReplaceAllString(text, "")
	return slices.ContainsFunc(prBuildFailed, func(p *regexp.Regexp) bool { return p.MatchString(plain) })
}

func prRanOtherTests(report prReport, scope string) bool {
	return report.readable() && slices.ContainsFunc(report.tests, func(t prTest) bool { return !strings.HasPrefix(t.id, scope) })
}

// prClassifyXcodeRed is classifyXcodeRed: every test must sit under the scope.
func prClassifyXcodeRed(run *proofExecResult, report prReport, scope string) prRed {
	if !prXcodeScope.MatchString(scope) {
		return prRed{verdict: "RED_INVALID", kind: "error", failedTests: []string{}}
	}
	base := prClassifyReport(run, []prReport{report}, prSwiftCompileError)
	invalid := func(kind string) prRed { return prRed{verdict: "RED_INVALID", kind: kind, failedTests: []string{}} }
	readable := report.readable()
	if prExitedClean(run) {
		switch {
		case !readable:
			return invalid("error")
		case prRanOtherTests(report, scope):
			return invalid("error")
		case !slices.ContainsFunc(report.tests, func(t prTest) bool { return t.status != "skipped" }):
			return invalid("error")
		}
		return base
	}
	if run.timedOut || run.signal != "" {
		return base
	}
	output := run.stdout + "\n" + run.stderr
	if prXcodeBuildFailed(output) {
		return invalid("compile")
	}
	if (report.noReport || readable && len(report.tests) == 0) && prSwiftCompileError(output) {
		return invalid("compile")
	}
	if prRanOtherTests(report, scope) {
		return invalid("error")
	}
	return base
}

// prClassifyXcodeGreen is classifyXcodeGreen.
func prClassifyXcodeGreen(run *proofExecResult, report prReport, redFailed []string, scope string) string {
	if !prXcodeScope.MatchString(scope) {
		return "NOT_GREEN"
	}
	verdict := prClassifyGreen(run, []prReport{report}, redFailed)
	if verdict == "GREEN" && prRanOtherTests(report, scope) {
		return "NOT_GREEN"
	}
	return verdict
}
