package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/jev"
)

// B525: the full doctor run with a valid config runs in Go. Each case builds
// the same throwaway setup the Node goldens in testdata/b525/*.golden were
// made on (Node's runDoctorCli in-process, CODE_FORGE_NATIVE=0, a temp HOME,
// a temp CODE_FORGE_TMP_ROOT, a fresh git repo, fake claude/grok CLIs from
// testdata/b523/fake.sh and, for Jev, a closed local port). Masked in both:
// the temp base path (<BASE>), pids ("pid N") and memory numbers ("N MB").
// The worker is B524's fake worker here and Node's real worker there; both
// print the same row. No provider, Jev, HOME or key of the machine is used.

const b525Mixed = "provider: xai\nlevels:\n  L0: {model: grok-9-fast}\n  L1: {model: grok-9-code, effort: high}\n  L2: {provider: anthropic, model: claude-opus-9}\n  L3: {provider: anthropic, model: claude-fable-9}\n"

// b525JevDown is a local port nothing listens on: the Jev call fails with
// a network error in Go and Node alike.
const b525JevDown = "http://127.0.0.1:9/v1/systemone"

type b525Case struct {
	golden, mode, config string
	doc                  bool
	fakes                []string
	jevKey               bool
	worker               string // B524_MODE: "" announces, "exit" never does
	args                 []string
}

type b525Env struct {
	base, home, repo, tmp, bin, pids, argvLog, handoverLog string
}

// b525Setup is the golden setup: the fake CLIs answer --version/--help and
// the probes through fake.sh (FAKE_MODE), the worker is B524's fake (every
// recorded pid is killed at the end), the Jev URL is a closed local port.
func b525Setup(t *testing.T, c b525Case, version string) *b525Env {
	t.Helper()
	td, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	home, repo := b474Project(t, c.config, 0)
	e := &b525Env{base: filepath.Dir(home), home: home, repo: repo}
	e.tmp, e.bin = filepath.Join(e.base, "tmp"), filepath.Join(e.base, "bin")
	e.pids, e.argvLog, e.handoverLog = filepath.Join(e.base, "fake-pids"), filepath.Join(e.base, "argv.log"), filepath.Join(e.base, "handover.log")
	for _, d := range []string{e.tmp, e.bin} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if c.doc {
		if err := os.WriteFile(filepath.Join(repo, "CLAUDE.md"), []byte("# Secret rules of the b525 project\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if version == "" {
		version = `9.9.9 (fake $(basename "$0"))`
	}
	for _, name := range c.fakes {
		script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n--version) echo \"%s\"; exit 0 ;;\n--help) cat \"%s/b525/$(basename \"$0\").help\"; exit 0 ;;\nesac\n. \"%s/b523/fake.sh\"\n", version, td, td)
		if err := os.WriteFile(filepath.Join(e.bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	fake := filepath.Join(e.base, "fake-worker")
	if err := os.WriteFile(fake, []byte(b525FakeWorker), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", e.bin+":/usr/bin:/bin")
	t.Setenv("CODE_FORGE_NATIVE", "0") // Node's golden run says "native binary: not found" too
	t.Setenv("CODE_FORGE_HANDOVER_LOG", e.handoverLog)
	t.Setenv("FAKE_MODE", c.mode)
	t.Setenv("FAKE_LOG", e.argvLog)
	t.Setenv("B524_PIDS", e.pids)
	t.Setenv("B524_PATH_FILE", filepath.Join(e.base, "fake-path"))
	t.Setenv("B524_MODE", c.worker)
	if c.jevKey {
		t.Setenv("CODE_FORGE_KEY_JEV", "b525-fake-key")
	}
	beforeBinary, beforeTimeout, beforeJev, beforeProcs := runWorkerBinary, runLaunchTimeout, doctorJevOpts, runReadProcesses
	runWorkerBinary = func() (string, error) { return fake, nil }
	runLaunchTimeout = 5 * time.Second
	doctorJevOpts = jev.Opts{URL: b525JevDown, Sleep: func(float64) {}}
	// B544: the sweep and memory rows read a sealed (empty) process table,
	// never the machine's: a zombie of another program is not in the golden.
	runReadProcesses = func() []runProc { return []runProc{} }
	t.Cleanup(func() {
		runWorkerBinary, runLaunchTimeout, doctorJevOpts, runReadProcesses = beforeBinary, beforeTimeout, beforeJev, beforeProcs
		for _, pid := range e.launched() {
			if pid > 1 {
				_ = syscall.Kill(pid, syscall.SIGKILL) // only the fake workers this test started
			}
		}
	})
	return e
}

// b525FakeWorker is B524's fake worker plus B525_BAD_ENTRY: it also leaves a
// registry entry with a lone surrogate escape (a registry Go cannot judge).
var b525FakeWorker = strings.Replace(b524FakeWorker, "exec sleep 30\n",
	"if [ -n \"$B525_BAD_ENTRY\" ]; then printf '%s' '{\"x\":\"\\ud800\"}' > .code-forge/workers/bad.json; fi\nexec sleep 30\n", 1)

func (e *b525Env) launched() []int {
	data, _ := os.ReadFile(e.pids)
	out := []int{}
	for _, line := range strings.Fields(string(data)) {
		if pid, err := strconv.Atoi(line); err == nil {
			out = append(out, pid)
		}
	}
	return out
}

var (
	b525PidRe = regexp.MustCompile(`pid [0-9]+`)
	b525MBRe  = regexp.MustCompile(`[0-9]+\.[0-9] MB`)
)

func (e *b525Env) mask(s string) string {
	s = strings.ReplaceAll(s, e.base, "<BASE>")
	s = b525PidRe.ReplaceAllString(s, "pid N")
	return b525MBRe.ReplaceAllString(s, "N MB")
}

// assertClean: no fake worker left, no run root left under the temp root,
// no hand-over logged.
func (e *b525Env) assertClean(t *testing.T) {
	t.Helper()
	for _, pid := range e.launched() {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("worker pid %d still runs after the doctor (%v)", pid, err)
		}
	}
	if left := b524List(t, e.tmp); left != "[]" {
		t.Fatalf("the doctor left %s in the temp root", left)
	}
}

func TestB525DoctorFullRunMatchesNode(t *testing.T) {
	plain := "provider: anthropic\n" + b474Levels
	both := []string{"claude", "grok"}
	for _, c := range []b525Case{
		{golden: "allok", mode: "ok", config: plain, fakes: both, args: []string{"doctor"}},
		{golden: "allok-json", mode: "ok", config: plain, fakes: both, args: []string{"doctor", "--json"}},
		{golden: "jevdown", mode: "ok", config: plain, fakes: both, jevKey: true, args: []string{"doctor"}},
		{golden: "climissing", mode: "ok", config: b525Mixed, fakes: []string{"claude"}, args: []string{"doctor"}},
		{golden: "ping402", mode: "402", config: plain, fakes: both, args: []string{"doctor"}},
		{golden: "isoleak", mode: "leak", config: plain, doc: true, fakes: both, args: []string{"doctor"}},
		{golden: "workerexit", mode: "ok", config: plain, fakes: both, worker: "exit", args: []string{"doctor"}},
	} {
		t.Run(c.golden, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", "b525", c.golden+".golden"))
			if err != nil {
				t.Fatal(err)
			}
			e := b525Setup(t, c, "")
			code, stdout, stderr := runCaptured(t, c.args...)
			got := e.mask(fmt.Sprintf("exit=%d\n--- stdout\n%s--- stderr\n%s", code, stdout, stderr))
			if got != string(want) {
				t.Fatalf("Go differs from Node's golden %s\n--- got\n%s\n--- want\n%s", c.golden, got, want)
			}
			if _, err := os.Stat(e.handoverLog); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Go handed over: %v", err)
			}
			e.assertClean(t)
		})
	}
}

// A case Go cannot serve hands over before any side effect: no session, no
// worker, no run root, nothing under HOME.
func TestB525DoctorHandOverWritesNothing(t *testing.T) {
	plain := "provider: anthropic\n" + b474Levels
	// Node cuts the first --version line at 80 UTF-16 units; a cut through a
	// surrogate pair is a string Go does not reproduce.
	long := strings.Repeat("9", 79) + "\U0001F600"
	for _, c := range []struct {
		name, config, version, reason string
	}{
		{"cli version cut through a surrogate pair", plain, long, "doctor-run-cli-version"},
		{"relative tmp.root", "provider: anthropic\ntmp: {root: rel-tmp}\n" + b474Levels, "", "doctor-run-tmp-root"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := b525Setup(t, b525Case{mode: "ok", config: c.config, fakes: []string{"claude", "grok"}}, c.version)
			expectHandover(t, c.reason, "doctor")
			if _, err := os.Stat(e.argvLog); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("a provider session ran before the hand-over: %v", err)
			}
			if pids := e.launched(); len(pids) != 0 {
				t.Fatalf("a worker started before the hand-over: %v", pids)
			}
			if got := b525Files(t, e.home); got != ".code-forge/native/handovers.jsonl,.code-forge/native/handovers.jsonl.lock" {
				t.Fatalf("HOME got %q before the hand-over (only the hand-over log and its lock may be written)", got)
			}
			if left := b524List(t, e.tmp); left != "[]" {
				t.Fatalf("the temp root got %s before the hand-over", left)
			}
			if left := b524List(t, filepath.Join(e.repo, "rel-tmp")); left != "null" {
				t.Fatalf("the relative temp root was made: %s", left)
			}
		})
	}
}

// B532: a worker registry entry Go cannot judge (Node's liveWorker throws on
// it) hands the run over; the probe stops its worker and removes all it made
// first, and no provider session runs (the worker probe is the first probe).
func TestB532DoctorUndecidedRegistryHandsOver(t *testing.T) {
	e := b525Setup(t, b525Case{mode: "ok", config: "provider: anthropic\n" + b474Levels, fakes: []string{"claude", "grok"}}, "")
	t.Setenv("B525_BAD_ENTRY", "1")
	expectHandover(t, "doctor-worker-registry", "doctor")
	if pids := e.launched(); len(pids) != 1 {
		t.Fatalf("want one fake worker started, got %v", pids)
	}
	e.assertClean(t)
	if _, err := os.Stat(e.argvLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a provider session ran before the hand-over: %v", err)
	}
	if left := b524List(t, filepath.Join(e.home, ".code-forge", "runs")); left != "null" && left != "[]" {
		t.Fatalf("the probe run is left: %s", left)
	}
}

// B532, B535: an errno outside the name table prints Node's "Unknown system
// error -<n>"; a mapped one prints its name.
func TestB532DoctorWorkerErrnoNames(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{syscall.EMFILE, "EMFILE"},
		{&os.PathError{Op: "open", Path: "x", Err: syscall.EACCES}, "EACCES"},
		{syscall.Errno(250), "Unknown system error -250"},
	} {
		if got := dwpCode(c.err); got != c.want {
			t.Fatalf("dwpCode(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

// b525Files lists the regular files under dir, relative, sorted.
func b525Files(t *testing.T, dir string) string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(out, ",")
}
