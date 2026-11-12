package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/blockopen"
	"github.com/ricardov03/code-forge/native/blockops"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/contextpack"
	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gosafe"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/procguard"
	"github.com/ricardov03/code-forge/native/session"
	"github.com/ricardov03/code-forge/native/state"
)

// B553: the `review` run in Go (src/cli/review.mjs runReview from the config
// load on): config and --acceptance checks, the --run record, the temp root,
// then `run start` → `block open` → `review-file` per file → `review-file
// --wait` (at most 8 at once, one deadline) → summary → `block stop` → `run
// end` and the worker reap, every step a child of this same binary with a
// GIT_*-free env in its own process group, plus Node's SIGINT/SIGTERM rules.
// Cases Go does not judge like Node (an unusual YAML acceptance file, a run
// record or config error that is not Node's own refusal, a temp root that
// cannot be made) hand over before anything is written or printed.

const (
	rvfDefaultMaxS   = 900
	rvfMaxWaiters    = 8
	rvfDefaultClause = "Review for correctness, security and test quality; no intent was stated."
	rvfStepTimeout   = 120 * time.Second
	rvfKillGrace     = 2 * time.Second
)

// rvfNow is the wait deadline clock (Date.now); a test seam.
var rvfNow = time.Now

// rvfSelf is the binary every step runs as a child (Node's process.execPath
// + bin/code-forge.mjs); a test seam.
var rvfSelf = func() (string, error) { return runWorkerBinary() }

var errRvfInterrupted = errors.New("interrupted")

type rvfInput struct {
	args             []string
	home, root, cwd  string
	base, baseFrom   string
	files            []string
	skipped          []reviewSkip
	json, keepRun    bool
	intent           *string
	acceptance       string // a --acceptance file, "" for the generated one
	runID            string
	reuse            bool
	maxS             float64
	tmp, self        string
	runEnded         bool // --run names an ended run: refuse after the skipped notes
	childEnv         []string
	blockIDGenerator func() string
}

// rvfPrepare is everything runReview reads before its first write or child:
// the config, --acceptance, the --run record and the temp root. A nil plan
// hands over for the reason.
func rvfPrepare(args []string, parsed cliParsed, root, sha, from string, files []string, skipped []reviewSkip) (func(o *runOut) int, handoverReason) {
	usage := func(text string) (func(o *runOut) int, handoverReason) {
		return func(o *runOut) int { o.err("review: " + text + "\n"); return 2 }, 0
	}
	if rvfWorkerHandsOff(files) {
		return nil, hReviewWorkerHandOff
	}
	cfg, err := config.LoadUnchecked(root)
	if err != nil {
		var cfgErr *config.Error
		if !errors.As(err, &cfgErr) {
			return nil, hReviewConfig
		}
		if cfgErr.Kind == "not-found" {
			return usage("no .code-forge.yml at the repository root — run `code-forge init` first")
		}
		return usage(".code-forge.yml does not load (" + cfgErr.Kind + ") — run `code-forge validate`")
	}
	cwd, err := runRealCwd()
	if err != nil {
		return nil, hReviewGit
	}
	in := rvfInput{args: args, home: os.Getenv("HOME"), root: root, cwd: cwd, base: sha, baseFrom: from,
		files: files, skipped: skipped, json: parsed.booleans["json"], keepRun: parsed.booleans["keep-run"], maxS: rvfDefaultMaxS}
	if text, ok := parsed.values["intent"]; ok {
		in.intent = &text
	}
	if max, ok := parsed.values["max"]; ok {
		n, _ := strconv.Atoi(strings.TrimSuffix(max, "s"))
		in.maxS = float64(n)
	}
	if file, ok := parsed.values["acceptance"]; ok {
		in.acceptance = runResolve(cwd, file)
		data, err := os.ReadFile(in.acceptance)
		if err != nil {
			return usage("--acceptance " + in.acceptance + ": not a readable YAML or JSON file")
		}
		value, err := blockopen.ParseAcceptance(data)
		if err != nil {
			if errors.Is(err, blockops.ErrHandover) {
				return nil, hReviewAcceptanceShape
			}
			return usage("--acceptance " + in.acceptance + ": not a readable YAML or JSON file")
		}
		if !rvfAcceptanceOK(value) {
			return usage("--acceptance " + in.acceptance + ": must be a non-empty list of {clause, tests: [test ids…]}")
		}
	}
	if id, ok := parsed.values["run"]; ok {
		in.runID = id
		record, err := state.ReadRun(in.home, id)
		var stateErr *state.Error
		switch {
		case err == nil:
			if record.Get("status") != "active" {
				in.runEnded = true
				return rvfEndedPlan(in), 0
			}
			in.reuse = true
		case errors.As(err, &stateErr) && stateErr.Code == "no-run":
		default:
			return nil, hReviewRunRecord
		}
	} else {
		in.runID = drunNewRunID()
	}
	self, err := rvfSelf()
	if err != nil {
		return nil, hReviewSelf
	}
	in.self = self
	var tmpRoot *string
	if tmp, ok := cfg["tmp"].(map[string]any); ok {
		if text, ok := tmp["root"].(string); ok {
			abs := runResolve(root, text)
			tmpRoot = &abs
		}
	}
	tmp, err := contextMakeRunRoot("review-"+in.runID, tmpRoot)
	if err != nil {
		return nil, hReviewTmpRoot
	}
	in.tmp = tmp
	in.childEnv = rvfChildEnv(args)
	return func(o *runOut) int { return rvfRun(in, o) }, 0
}

// rvfWorkerHandsOff: files whose ticket the Go worker gives to a Node worker
// (several owned files bring the moved-code hints, a test-like name the
// test-quality hint: native/review engine.go). The worker withdraws its
// announcement before the Node worker boots, and a `review-file --wait`
// already polling reads that gap as worker_down, where Node's own run
// reviews the file; so these runs stay Node's whole.
func rvfWorkerHandsOff(files []string) bool {
	if len(files) > 1 {
		return true
	}
	low := strings.ToLower(files[0])
	return strings.Contains(low, "test") || strings.Contains(low, "spec")
}

// rvfEndedPlan: the skipped notes, then Node's refusal of an ended --run.
func rvfEndedPlan(in rvfInput) func(o *runOut) int {
	return func(o *runOut) int {
		for _, s := range in.skipped {
			o.err("review: skipped " + s.file + " (" + s.reason + ")\n")
		}
		o.err("review: run " + in.runID + " has ended — give another --run or none\n")
		return 2
	}
}

// rvfAcceptanceOK is readAcceptance's shape check.
func rvfAcceptanceOK(value any) bool {
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return false
	}
	for _, item := range list {
		c, _ := item.(ledger.Object)
		if text, ok := c.Get("clause").(string); !ok || text == "" {
			return false
		}
		tests, ok := c.Get("tests").([]any)
		if !ok || len(tests) == 0 {
			return false
		}
		for _, t := range tests {
			if text, ok := t.(string); !ok || text == "" {
				return false
			}
		}
	}
	return true
}

// rvfChildEnv is scrubbedEnv() plus exec.mjs's owner fields and run id.
func rvfChildEnv(args []string) []string {
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	set := func(key, value string) {
		for i, kv := range env {
			if strings.HasPrefix(kv, key+"=") {
				env[i] = key + "=" + value
				return
			}
		}
		env = append(env, key+"="+value)
	}
	unset := func(key string) {
		out := env[:0]
		for _, kv := range env {
			if !strings.HasPrefix(kv, key+"=") {
				out = append(out, kv)
			}
		}
		env = out
	}
	set("CODE_FORGE_OWNER_PID", strconv.Itoa(os.Getpid()))
	if start := filelock.ProcessStartTime(os.Getpid()); start != nil {
		set("CODE_FORGE_OWNER_START_TIME", *start)
	} else {
		unset("CODE_FORGE_OWNER_START_TIME")
	}
	if _, has := os.LookupEnv("CODE_FORGE_RUN_ID"); !has {
		// process.argv's `--run <id>` (or `--run=<id>`) of this review
		var runID *string
		at := -1
		for i, a := range args {
			if a == "--run" {
				at = i
				break
			}
		}
		if at >= 0 {
			if at+1 < len(args) {
				runID = &args[at+1]
			}
		} else {
			for _, a := range args {
				if strings.HasPrefix(a, "--run=") {
					v := strings.TrimPrefix(a, "--run=")
					runID = &v
					break
				}
			}
		}
		if runID != nil && *runID != "" {
			set("CODE_FORGE_RUN_ID", *runID)
		}
	}
	return env
}

// rvfBlockID is `R-<UTC yyyymmdd-hhmmss>-<4 hex>`.
func rvfBlockID() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return "R-" + time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

type rvfResult struct {
	code     *int
	signal   string
	stdout   string
	stderr   string
	timedOut bool
}

// exitText is `${code ?? signal}`.
func (r rvfResult) exitText() string {
	if r.code != nil {
		return strconv.Itoa(*r.code)
	}
	if r.signal != "" {
		return r.signal
	}
	return "null"
}

var rvfSignalNames = map[syscall.Signal]string{
	syscall.SIGHUP: "SIGHUP", syscall.SIGINT: "SIGINT", syscall.SIGQUIT: "SIGQUIT", syscall.SIGILL: "SIGILL",
	syscall.SIGTRAP: "SIGTRAP", syscall.SIGABRT: "SIGABRT", syscall.SIGBUS: "SIGBUS", syscall.SIGFPE: "SIGFPE",
	syscall.SIGKILL: "SIGKILL", syscall.SIGUSR1: "SIGUSR1", syscall.SIGSEGV: "SIGSEGV", syscall.SIGUSR2: "SIGUSR2",
	syscall.SIGPIPE: "SIGPIPE", syscall.SIGALRM: "SIGALRM", syscall.SIGTERM: "SIGTERM",
}

func rvfSignalName(sig syscall.Signal) string {
	if name, ok := rvfSignalNames[sig]; ok {
		return name
	}
	return "SIG" + strconv.Itoa(int(sig))
}

type rvfFlow struct {
	in        rvfInput
	o         *runOut
	outMu     sync.Mutex
	mu        sync.Mutex
	live      map[int]bool
	abort     chan struct{}
	abortSig  string
	signals   int
	inCleanup bool
	started   bool
	blockOpen bool
	blockID   string
}

func (f *rvfFlow) note(s string) {
	f.outMu.Lock()
	defer f.outMu.Unlock()
	f.o.err("review: " + s + "\n")
}

func (f *rvfFlow) aborted() bool {
	select {
	case <-f.abort:
		return true
	default:
		return false
	}
}

// exec is exec.mjs for one `code-forge` child: argv only, its own process
// group, stdin /dev/null, a pid-registry entry in this run root while it
// lives, SIGTERM to the group on timeout and SIGKILL 2 s later.
func (f *rvfFlow) exec(args []string, timeout time.Duration) rvfResult {
	cmd := exec.Command(f.in.self, args...)
	cmd.Dir = f.in.root
	cmd.Env = f.in.childEnv
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		return rvfResult{}
	}
	pid := cmd.Process.Pid
	f.mu.Lock()
	f.live[pid] = true
	f.mu.Unlock()
	registry := filepath.Join(f.in.tmp, "pids")
	registered := session.RegisterPid(registry, pid, f.in.self) == nil
	done := make(chan struct{})
	gosafe.Go("review-step-wait", func() { _ = cmd.Wait(); close(done) })
	timedOut := false
	timer := time.NewTimer(timeout)
	select {
	case <-done:
		timer.Stop()
	case <-timer.C:
		timedOut = true
		_ = syscall.Kill(-pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(rvfKillGrace):
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			<-done
		}
	}
	f.mu.Lock()
	delete(f.live, pid)
	f.mu.Unlock()
	if registered {
		_ = os.Remove(filepath.Join(registry, strconv.Itoa(pid)+".json"))
	}
	res := rvfResult{stdout: strings.ToValidUTF8(stdout.String(), "�"), stderr: strings.ToValidUTF8(stderr.String(), "�"), timedOut: timedOut}
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		res.signal = rvfSignalName(ws.Signal())
	} else {
		code := cmd.ProcessState.ExitCode()
		res.code = &code
	}
	return res
}

// cf runs one flow step, cut short by the first signal.
func (f *rvfFlow) cf(args []string, timeout time.Duration) (rvfResult, error) {
	if f.aborted() {
		return rvfResult{}, errRvfInterrupted
	}
	ch := make(chan rvfResult, 1)
	gosafe.Go("review-step", func() { ch <- f.exec(args, timeout) })
	select {
	case res := <-ch:
		if f.aborted() {
			return rvfResult{}, errRvfInterrupted
		}
		return res, nil
	case <-f.abort:
		return rvfResult{}, errRvfInterrupted
	}
}

type rvfStepError struct{ message string }

func (e *rvfStepError) Error() string { return e.message }

// must is review.mjs must(): a nonzero exit fails the flow.
func rvfMust(res rvfResult, what string) error {
	if res.code != nil && *res.code == 0 {
		return nil
	}
	text := res.stderr
	if text == "" {
		text = res.stdout
	}
	lines := strings.Split(runJSTrim(text), "\n")
	return &rvfStepError{what + " exited " + res.exitText() + ": " + lines[len(lines)-1]}
}

// rvfLastJSON is lastJSON: the last line of the trimmed text as JSON, or nil.
func rvfLastJSON(text string) any {
	lines := strings.Split(runJSTrim(text), "\n")
	v, err := ledger.DecodeValue([]byte(lines[len(lines)-1]))
	if err != nil {
		return nil
	}
	return v
}

// rvfGet is `v?.[key]`: the value and whether the property exists.
func rvfGet(v any, key string) (any, bool) {
	o, ok := v.(ledger.Object)
	if !ok {
		return nil, false
	}
	for _, field := range o {
		if field.Key == key {
			return field.Value, true
		}
	}
	return nil, false
}

// rvfCoalesce is `a ?? b ?? …` over properties (nil = null or absent).
func rvfCoalesce(v any, keys []string, fallback any) any {
	for _, key := range keys {
		if value, _ := rvfGet(v, key); value != nil {
			return value
		}
	}
	return fallback
}

// rvfTemplate is `${value}` for a property read.
func rvfTemplate(v any, present bool) string {
	if !present {
		return "undefined"
	}
	return runJSString(v)
}

type rvfOutcome struct {
	file, result string
	reason       *string
	findings     []any
	size         bool
	tokensIn     any // float64 or nil
	budget       any
	section      *string
}

func (o rvfOutcome) object() ledger.Object {
	var reason any
	if o.reason != nil {
		reason = *o.reason
	}
	findings := o.findings
	if findings == nil {
		findings = []any{}
	}
	obj := ledger.Object{{Key: "file", Value: o.file}, {Key: "result", Value: o.result}, {Key: "reason", Value: reason}, {Key: "findings", Value: findings}}
	if o.size {
		obj = append(obj, ledger.Field{Key: "tokens_in", Value: o.tokensIn}, ledger.Field{Key: "budget", Value: o.budget})
		if o.section != nil {
			obj = append(obj, ledger.Field{Key: "section", Value: *o.section})
		}
	}
	return obj
}

// rvfClassify is classify(file, waited).
func rvfClassify(file string, waited any) rvfOutcome {
	findings := []any{}
	if list, ok := rvfCoalesce(waited, []string{"fix_list"}, nil).([]any); ok {
		findings = list
	}
	out := func(result string, reason *string) rvfOutcome {
		return rvfOutcome{file: file, result: result, reason: reason, findings: findings}
	}
	str := func(s string) *string { return &s }
	if !runTruthy(waited) {
		return out("unavailable", str("no answer"))
	}
	status, _ := rvfGet(waited, "status")
	if status == "pending" {
		return out("stopped", str("timeout"))
	}
	if status != "done" {
		return out("unavailable", str(runJSString(rvfCoalesce(waited, []string{"reason", "status"}, "no answer"))))
	}
	r, _ := rvfGet(waited, "result")
	if _, isObject := r.(ledger.Object); !runTruthy(r) || !isObject {
		if _, isArray := r.([]any); !isArray {
			return out("unavailable", str("unreadable result"))
		}
	}
	if approved, _ := rvfGet(r, "approved"); approved == true {
		return out("approved", nil)
	}
	rStatus, _ := rvfGet(r, "status")
	if rStatus == "no_change" {
		return out("unchanged", nil)
	}
	fin := func(key string) any {
		v, _ := rvfGet(r, key)
		if n, ok := v.(float64); ok && !math.IsInf(n, 0) && !math.IsNaN(n) {
			return n
		}
		return nil
	}
	withSize := func(o rvfOutcome) rvfOutcome {
		o.size, o.tokensIn, o.budget = true, fin("tokens_in"), fin("budget")
		if section, ok := rvfCoalesce(r, []string{"section"}, nil).(string); ok {
			o.section = &section
		}
		return o
	}
	if rStatus == "stopped" {
		o := out("stopped", str(runJSString(rvfCoalesce(r, []string{"stopped"}, "stopped"))))
		if stopped, _ := rvfGet(r, "stopped"); stopped == "split_required" {
			o = withSize(o)
		}
		return o
	}
	if rStatus == "split_required" {
		return withSize(out("stopped", str("split_required")))
	}
	if rStatus == "reviewed" && len(findings) > 0 {
		return out("findings", nil)
	}
	return out("unavailable", str(runJSString(rvfCoalesce(r, []string{"reason", "status"}, "not approved"))))
}

func rvfPlural(n int, word string) string {
	if n == 1 {
		return strconv.Itoa(n) + " " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// rvfLabel is label(o).
func rvfLabel(o rvfOutcome) string {
	if o.result == "findings" {
		return rvfPlural(len(o.findings), "finding")
	}
	reason := "null"
	if o.reason != nil {
		reason = *o.reason
	}
	_, tokensOK := o.tokensIn.(float64)
	_, budgetOK := o.budget.(float64)
	if o.result == "stopped" && reason == "split_required" && tokensOK && budgetOK {
		section := ""
		if o.section != nil && *o.section != "" {
			section = "; section " + reviewQuote(*o.section)
		}
		return o.result + ": " + reason + " (~" + runNumber(o.tokensIn) + " tokens, budget " + runNumber(o.budget) + section + ")"
	}
	if o.result == "stopped" || o.result == "unavailable" {
		return o.result + ": " + reason
	}
	return o.result
}

type rvfTotals struct{ files, approved, unchanged, withFindings, findings, stopped, unavailable int }

func rvfCount(outcomes []rvfOutcome) rvfTotals {
	t := rvfTotals{files: len(outcomes)}
	for _, o := range outcomes {
		switch o.result {
		case "approved":
			t.approved++
		case "unchanged":
			t.unchanged++
		case "findings":
			t.withFindings++
			t.findings += len(o.findings)
		case "stopped":
			t.stopped++
		case "unavailable":
			t.unavailable++
		}
	}
	return t
}

func (t rvfTotals) object() ledger.Object {
	return ledger.Object{{Key: "files", Value: t.files}, {Key: "approved", Value: t.approved}, {Key: "unchanged", Value: t.unchanged},
		{Key: "with_findings", Value: t.withFindings}, {Key: "findings", Value: t.findings}, {Key: "stopped", Value: t.stopped}, {Key: "unavailable", Value: t.unavailable}}
}

func rvfUTF16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// rvfSortJS is Array.prototype.sort(): UTF-16 code unit order.
func rvfSortJS(list []string) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := utf16.Encode([]rune(list[i])), utf16.Encode([]rune(list[j]))
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
}

// rvfRenderText is renderText(outcomes).
func rvfRenderText(outcomes []rvfOutcome) string {
	width := 0
	for _, o := range outcomes {
		if n := rvfUTF16Len(o.file); n > width {
			width = n
		}
	}
	lines := []string{}
	for _, o := range outcomes {
		lines = append(lines, o.file+strings.Repeat(" ", width-rvfUTF16Len(o.file))+"  "+rvfLabel(o))
	}
	for _, o := range outcomes {
		if o.result == "approved" || o.result == "unchanged" || len(o.findings) == 0 {
			continue
		}
		lines = append(lines, "", "fix list: "+o.file)
		for _, item := range o.findings {
			field := func(key string) string { v, ok := rvfGet(item, key); return rvfTemplate(v, ok) }
			lines = append(lines, "  "+field("id")+" · "+field("severity")+" · lines "+field("lines"), "    claim: "+field("claim"), "    fix:   "+field("fix"))
		}
	}
	t := rvfCount(outcomes)
	unchanged := ""
	if t.unchanged > 0 {
		unchanged = " · " + strconv.Itoa(t.unchanged) + " unchanged"
	}
	files := "file"
	if t.files != 1 {
		files = "files"
	}
	lines = append(lines, "", "totals: "+strconv.Itoa(t.files)+" "+files+" · "+strconv.Itoa(t.approved)+" approved · "+strconv.Itoa(t.withFindings)+
		" with findings ("+rvfPlural(t.findings, "finding")+") · "+strconv.Itoa(t.stopped)+" stopped · "+strconv.Itoa(t.unavailable)+" unavailable"+unchanged)
	return strings.Join(lines, "\n") + "\n"
}

// rvfRun is runReview from the run on: the flow, its cleanup and the signals.
func rvfRun(in rvfInput, o *runOut) int {
	f := &rvfFlow{in: in, o: o, live: map[int]bool{}, abort: make(chan struct{})}
	for _, s := range in.skipped {
		f.note("skipped " + s.file + " (" + s.reason + ")")
	}
	gen := in.blockIDGenerator
	if gen == nil {
		gen = rvfBlockID
	}
	f.blockID = gen()

	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	stopSignals := make(chan struct{})
	signalsDone := make(chan struct{})
	gosafe.Go("review-signals", func() {
		defer close(signalsDone)
		for {
			select {
			case <-stopSignals:
				return
			case s := <-sigs:
				f.onSignal(s.(syscall.Signal))
			}
		}
	})

	code := 1
	if err := f.flow(&code); err != nil {
		if errors.Is(err, errRvfInterrupted) {
			f.mu.Lock()
			name := f.abortSig
			f.mu.Unlock()
			f.note("interrupted by " + name + "; stopping the review")
		} else {
			f.note("failed: " + err.Error())
		}
		code = 1
	}
	f.cleanup()
	signal.Stop(sigs)
	close(stopSignals)
	<-signalsDone
	if err := os.RemoveAll(in.tmp); err != nil {
		f.note("temp root not removed: " + contextpack.NodeError(err))
	}
	return code
}

// onSignal is review.mjs onSignal plus exec.mjs's forwarding of the signal
// to every live child group.
func (f *rvfFlow) onSignal(sig syscall.Signal) {
	f.mu.Lock()
	groups := []int{}
	for pid := range f.live {
		groups = append(groups, pid)
	}
	f.mu.Unlock()
	for _, pid := range groups {
		_ = syscall.Kill(-pid, sig)
	}
	name := rvfSignalName(sig)
	if sig == syscall.SIGHUP {
		// exec.mjs re-raises a signal no other listener handles
		if len(groups) > 0 {
			signal.Reset(syscall.SIGHUP)
			_ = syscall.Kill(os.Getpid(), syscall.SIGHUP)
		}
		return
	}
	f.mu.Lock()
	f.signals++
	n, inCleanup := f.signals, f.inCleanup
	f.mu.Unlock()
	if n == 1 {
		if inCleanup {
			f.note(name + " during cleanup: finishing it (a second " + name + " exits at once)")
			return
		}
		for _, pid := range groups {
			if syscall.Kill(-pid, syscall.SIGTERM) != nil {
				_ = syscall.Kill(pid, syscall.SIGTERM)
			}
		}
		f.mu.Lock()
		f.abortSig = name
		f.mu.Unlock()
		close(f.abort)
	} else if n == 2 {
		go f.hardExit(name)
	}
}

// hardExit is the second signal: a quick block stop, run end and reap, then exit 1.
func (f *rvfFlow) hardExit(name string) {
	f.note("second " + name + ": ending the run and exiting now")
	f.mu.Lock()
	blockOpen, started := f.blockOpen, f.started
	f.mu.Unlock()
	if blockOpen {
		f.exec([]string{"block", "stop", f.blockID, "--run", f.in.runID, "--reason", "review only: interrupted"}, 10*time.Second)
	}
	if started && !f.in.keepRun {
		f.exec([]string{"run", "end", "--run", f.in.runID}, 10*time.Second)
		rvfReapWorkers(f.in.home, f.in.root, f.in.runID)
	}
	_ = os.RemoveAll(f.in.tmp)
	os.Exit(1)
}

func (f *rvfFlow) flow(code *int) error {
	in := f.in
	acceptance := in.acceptance
	if acceptance == "" {
		clause := rvfDefaultClause
		if in.intent != nil {
			clause = *in.intent
		}
		acceptance = filepath.Join(in.tmp, "acceptance.json")
		data, _ := ledger.Stringify([]any{ledger.Object{{Key: "clause", Value: clause}, {Key: "tests", Value: []any{"review-only"}}}}, false)
		if err := os.WriteFile(acceptance, data, 0o600); err != nil {
			return errors.New(contextpack.NodeError(err))
		}
	}
	reused := ""
	if in.reuse {
		reused = " (reused)"
	}
	files := rvfPlural(len(in.files), "file")
	f.note("base " + in.base[:12] + " (" + in.baseFrom + ") · " + files + " · run " + in.runID + reused)
	if !in.reuse {
		f.mu.Lock()
		f.started = true
		f.mu.Unlock()
		res, err := f.cf([]string{"run", "start", "--cwd", in.root, "--run", in.runID}, rvfStepTimeout)
		if err != nil {
			return err
		}
		if err := rvfMust(res, "run start"); err != nil {
			return err
		}
	}
	f.mu.Lock()
	f.blockOpen = true
	f.mu.Unlock()
	res, err := f.cf(append([]string{"block", "open", f.blockID, "--run", in.runID, "--level", "L2", "--base", in.base, "--acceptance", acceptance, "--owned"}, in.files...), rvfStepTimeout)
	if err != nil {
		return err
	}
	if err := rvfMust(res, "block open"); err != nil {
		return err
	}
	type queued struct {
		file   string
		ticket string
		has    bool
		answer any
	}
	queue := []queued{}
	for _, file := range in.files {
		res, err := f.cf([]string{"review-file", file, "--block", f.blockID, "--run", in.runID}, rvfStepTimeout)
		if err != nil {
			return err
		}
		answer := rvfLastJSON(res.stdout)
		ticket, has := rvfCoalesce(answer, []string{"ticket"}, nil).(string)
		queue = append(queue, queued{file, ticket, has, answer})
	}
	deadline := rvfNow().UnixMilli() + int64(in.maxS*1000)
	outcomes := make([]rvfOutcome, len(queue))
	errs := make([]error, len(queue))
	finish := func(i int, o rvfOutcome) {
		outcomes[i] = o
		f.note(o.file + "  " + rvfLabel(o))
	}
	for i, q := range queue {
		if !q.has {
			finish(i, rvfClassify(q.file, ledger.Object{
				{Key: "status", Value: rvfCoalesce(q.answer, []string{"status"}, "refused")},
				{Key: "reason", Value: rvfCoalesce(q.answer, []string{"reason", "status"}, "not queued")},
			}))
		}
	}
	slots := make(chan struct{}, rvfMaxWaiters)
	var wg sync.WaitGroup
	for i, q := range queue {
		if !q.has {
			continue
		}
		slots <- struct{}{}
		wg.Add(1)
		gosafe.Go("review-wait", func() {
			defer wg.Done()
			defer func() { <-slots }()
			left := deadline - rvfNow().UnixMilli()
			if left <= 0 {
				finish(i, rvfClassify(q.file, ledger.Object{{Key: "status", Value: "pending"}}))
				return
			}
			secs := (left + 999) / 1000
			res, err := f.cf([]string{"review-file", "--wait", q.ticket, "--max", strconv.FormatInt(secs, 10) + "s"}, time.Duration(left+60_000)*time.Millisecond)
			if err != nil {
				errs[i] = err
				return
			}
			finish(i, rvfClassify(q.file, rvfLastJSON(res.stdout)))
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	allOK := true
	for _, o := range outcomes {
		if o.result != "approved" && o.result != "unchanged" {
			allOK = false
		}
	}
	*code = 1
	if allOK {
		*code = 0
	}
	f.outMu.Lock()
	defer f.outMu.Unlock()
	if in.json {
		list := []any{}
		for _, o := range outcomes {
			list = append(list, o.object())
		}
		skipped := []any{}
		for _, s := range in.skipped {
			skipped = append(skipped, ledger.Object{{Key: "file", Value: s.file}, {Key: "reason", Value: s.reason}})
		}
		doc := ledger.Object{{Key: "ok", Value: allOK}, {Key: "base", Value: in.base}, {Key: "base_from", Value: in.baseFrom},
			{Key: "run", Value: in.runID}, {Key: "block", Value: f.blockID}, {Key: "files", Value: list},
			{Key: "skipped", Value: skipped}, {Key: "totals", Value: rvfCount(outcomes).object()}}
		text, _ := ledger.Stringify(doc, false)
		f.o.out(string(text) + "\n")
	} else {
		f.o.out(rvfRenderText(outcomes))
	}
	return nil
}

// cleanup is runReview's finally: block stop, run end and the worker reap,
// each step independent; the review's exit code stays its own.
func (f *rvfFlow) cleanup() {
	f.mu.Lock()
	f.inCleanup = true
	blockOpen, started := f.blockOpen, f.started
	f.mu.Unlock()
	step := func(args []string) rvfResult {
		res := f.exec(args, 60*time.Second)
		f.mu.Lock()
		signals := f.signals
		f.mu.Unlock()
		if res.code == nil && res.signal != "" && !res.timedOut && signals > 0 {
			return f.exec(args, 60*time.Second)
		}
		return res
	}
	in := f.in
	if blockOpen {
		stopped := step([]string{"block", "stop", f.blockID, "--run", in.runID, "--reason", "review only: never closed, no proof"})
		if stopped.code == nil || *stopped.code != 0 {
			f.note("block stop exited " + stopped.exitText())
		}
	}
	if started && !in.keepRun {
		ended := step([]string{"run", "end", "--run", in.runID})
		if ended.code == nil || *ended.code != 0 {
			f.note("run end exited " + ended.exitText() + ": " + runJSTrim(ended.stderr))
		}
		rvfReapWorkers(in.home, in.root, in.runID)
	} else if started {
		f.note("run " + in.runID + " kept (--keep-run): reuse it with --run " + in.runID + "; end it with `code-forge run end --run " + in.runID + "`")
	}
}

type rvfWorker struct {
	pid  int
	same func() bool
}

// rvfReapWorkers is reapWorkers: this run's worker (the pin in the run
// record, the queue's announcement) gets SIGTERM, 5 s, then SIGKILL to its
// group, each only while its start time still matches.
func rvfReapWorkers(home, root, runID string) int {
	found := []rvfWorker{}
	if record, err := state.ReadRun(home, runID); err == nil {
		pin, _ := record.Get("worker").(ledger.Object)
		if pid, ok := pin.Get("pid").(float64); ok && pid == math.Trunc(pid) && pid > 1 && pid <= math.MaxInt32 {
			startedAt := pin.Get("started_at")
			found = append(found, rvfWorker{int(pid), func() bool {
				now := filelock.ProcessStartTime(int(pid))
				return now != nil && startedAt == *now
			}})
		}
	}
	if w, ok := runLiveWorker(root, 0, false); ok && w != nil && w.Get("run") == runID {
		pid, _ := w.Get("pid").(float64)
		dup := false
		for _, x := range found {
			if float64(x.pid) == pid {
				dup = true
			}
		}
		if !dup && pid > 1 && pid == math.Trunc(pid) && pid <= math.MaxInt32 {
			startTime := w.Get("start_time")
			found = append(found, rvfWorker{int(pid), func() bool {
				if startTime == "unknown" {
					return true
				}
				now := filelock.ProcessStartTime(int(pid))
				return now != nil && startTime == *now
			}})
		}
	}
	killed := 0
	for _, w := range found {
		if !runAlive(float64(w.pid)) || !w.same() {
			continue
		}
		if procguard.Kill(w.pid, syscall.SIGTERM) != nil {
			continue
		}
		until := time.Now().Add(5 * time.Second)
		for runAlive(float64(w.pid)) && time.Now().Before(until) {
			time.Sleep(100 * time.Millisecond)
		}
		if runAlive(float64(w.pid)) && w.same() {
			if procguard.Kill(-w.pid, syscall.SIGKILL) != nil {
				_ = procguard.Kill(w.pid, syscall.SIGKILL)
			}
			killed++
		}
	}
	return killed
}
