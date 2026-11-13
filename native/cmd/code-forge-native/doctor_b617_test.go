package main

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
)

// B617: a full doctor with a Codex (openai) coder, an open-book Codex
// reviewer, or a live worker of the project runs in Go. The goldens in
// testdata/b617 were recorded from Node (CODE_FORGE_NATIVE=0
// ./bin/code-forge doctor) on the same throwaway setup as B525's: a temp
// HOME and temp root, a fresh git repo, the B523 fake claude/grok and the
// fake codex testdata/b617/codex.sh on PATH. Each golden holds the exit
// code, stdout, stderr, every CLI argv (masked) and the files left under
// HOME. Masked in both: the temp base path, pids, memory numbers, the
// random names of the doctor's own files. The sweep row is the sealed one
// (B544): Node read the machine's process table when it was recorded.

const b617Levels = "  L0: {model: claude-haiku-4-5-20251001}\n  L3: {model: claude-fable-5-1}\n"

var (
	b617CodexCoder = "provider: anthropic\nlevels:\n" + b617Levels + "  L1: {provider: openai, model: gpt-9-codex, effort: high}\n  L2: {model: claude-opus-5-5}\n"
	b617OpenBook   = "provider: anthropic\nreview: {allow_open_book_codex: true}\nlevels:\n" + b617Levels + "  L1: {model: claude-sonnet-5}\n  L2: {provider: openai, model: gpt-9-codex}\n"
	// the loaded config puts the worktrees in ../wt (an indexing WARN);
	// the live worker's run snapshot decides instead
	b617LiveConfig = "provider: anthropic\nproof: {worktree: {dir: ../wt}}\nlevels:\n" + b617Levels + "  L1: {model: claude-sonnet-5}\n  L2: {model: claude-opus-5-5}\n"
)

const b617SnapLevels = `"levels":{"L0":{"model":"claude-haiku-4-5-20251001"},"L1":{"model":"claude-sonnet-5"},"L2":{"model":"claude-opus-5-5"},"L3":{"model":"claude-fable-5-1"}}`

// b617Snapshots: the run record's snapshot per live case, with its hash
// (Node's configHash). badhash stores a config under another's hash.
var b617Snapshots = map[string][2]string{
	"noindex": {"1d0916cf3c899fd3d1cf59a8769f0619ee472e4ffb8a2844130a4911d6eff45c", `{"provider":"anthropic","proof":{"worktree":{"dir":"../wt.noindex"}},` + b617SnapLevels + `}`},
	"badhash": {"1d0916cf3c899fd3d1cf59a8769f0619ee472e4ffb8a2844130a4911d6eff45c", `{"provider":"anthropic",` + b617SnapLevels + `}`},
	"oldwt":   {"6c518cb1b9211db0b1874f27bdbf6a6a0bb13aceb6f97c62da1d11677adcd68b", `{"provider":"anthropic","proof":{"worktree":{"branch":"cf/{run}/{block}"}},` + b617SnapLevels + `}`},
}

type b617Case struct {
	golden, mode, config, live string
	doc, codex                 bool
	fakes                      []string
}

var b617Cases = []b617Case{
	{golden: "codex-coder", mode: "ok", config: b617CodexCoder, codex: true, fakes: []string{"claude"}},
	{golden: "codex-coder-leak", mode: "leak", config: b617CodexCoder, codex: true, fakes: []string{"claude"}},
	{golden: "openbook", mode: "ok", config: b617OpenBook, doc: true, codex: true, fakes: []string{"claude"}},
	{golden: "openbook-leak", mode: "leak", config: b617OpenBook, doc: true, codex: true, fakes: []string{"claude"}},
	{golden: "live-noindex", mode: "ok", config: b617LiveConfig, live: "noindex", fakes: []string{"claude", "grok"}},
	{golden: "live-oldwt", mode: "ok", config: b617LiveConfig, live: "oldwt", fakes: []string{"claude", "grok"}},
	{golden: "live-badhash", mode: "ok", config: b617LiveConfig, live: "badhash", fakes: []string{"claude", "grok"}},
}

// b617Setup is b525Setup plus the fake codex and the live worker: this
// test process announced as a worker of run r-b617 (a fresh heartbeat), and
// the run's record with the case's config snapshot.
func b617Setup(t *testing.T, c b617Case) *b525Env {
	t.Helper()
	td, err := filepath.Abs(filepath.Join("testdata", "b617")) // before b525Setup moves into the repo
	if err != nil {
		t.Fatal(err)
	}
	e := b525Setup(t, b525Case{mode: c.mode, config: c.config, doc: c.doc, fakes: c.fakes}, "")
	if c.codex {
		script := fmt.Sprintf("#!/bin/sh\nB617_TD=%s; export B617_TD\n. %s/codex.sh\n", td, td)
		if err := os.WriteFile(filepath.Join(e.bin, "codex"), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if c.live == "" {
		return e
	}
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	pid := strconv.Itoa(os.Getpid())
	workers := filepath.Join(e.repo, ".code-forge", "workers")
	runs := filepath.Join(e.home, ".code-forge", "runs")
	for _, d := range []string{workers, runs} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(workers, pid+"-unknown.json"), []byte(`{"pid":`+pid+`,"start_time":"unknown","run":"r-b617","started_at":"`+stamp+`","heartbeat_at":"`+stamp+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snap := b617Snapshots[c.live]
	record := fmt.Sprintf(`{"run_id":"r-b617","config":{"hash":"%s","snapshots":{"%s":%s}}}`+"\n", snap[0], snap[0], snap[1])
	if err := os.WriteFile(filepath.Join(runs, "r-b617.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	if c.live == "oldwt" {
		for _, args := range [][]string{
			{"-c", "user.email=t@e", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
			{"worktree", "add", "-q", "-b", "cf/r-b617/B1", filepath.Join(e.base, "repo-worktrees", "B1")},
		} {
			git := exec.Command("git", append([]string{"-C", e.repo}, args...)...)
			git.Env = gitx.ChildEnv()
			if out, err := git.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %s: %v", args, out, err)
			}
		}
	}
	return e
}

var b617ArgvMasks = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`doctor-\d+-[0-9a-f]{6}`), "doctor-N-X"},
	{regexp.MustCompile(`-\d{10,}-[0-9a-f]{8}`), "-T-X"},
	{regexp.MustCompile(`(repo|facts|coder|reviewer|s2|delegate|isolation|isolation-out)-[0-9a-f]{8}`), "$1-X"},
	{regexp.MustCompile(`doctor-canary-[0-9a-f]{12}`), "doctor-canary-X"},
	{regexp.MustCompile(`cf-canary-[0-9a-f]{24}`), "cf-canary-X"},
}

// b617Report is the golden's text: the run's output, the masked argv log
// and the files left under HOME.
func b617Report(t *testing.T, e *b525Env, code int, stdout, stderr string) string {
	t.Helper()
	argv, _ := os.ReadFile(e.argvLog)
	text := strings.ReplaceAll(string(argv), e.base, "<BASE>")
	for _, m := range b617ArgvMasks {
		text = m.re.ReplaceAllString(text, m.with)
	}
	var files []string
	err := filepath.WalkDir(e.home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, _ := filepath.Rel(e.home, p)
			files = append(files, "./"+rel+"\n")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return e.mask(fmt.Sprintf("exit=%d\n--- stdout\n%s--- stderr\n%s", code, stdout, stderr)) + "--- argv\n" + text + "--- home files\n" + strings.Join(files, "")
}

func TestB617DoctorMatchesNode(t *testing.T) {
	saved := drunPlatform
	drunPlatform = "darwin" // the goldens come from Node on a Mac (the indexing row)
	t.Cleanup(func() { drunPlatform = saved })
	for _, c := range b617Cases {
		t.Run(c.golden, func(t *testing.T) {
			want, err := os.ReadFile(filepath.Join("testdata", "b617", c.golden+".golden"))
			if err != nil {
				t.Fatal(err)
			}
			e := b617Setup(t, c)
			code, stdout, stderr := runCaptured(t, "doctor")
			if got := b617Report(t, e, code, stdout, stderr); got != string(want) {
				t.Fatalf("Go differs from Node's golden %s\n--- got\n%s\n--- want\n%s", c.golden, got, want)
			}
			if data, err := os.ReadFile(e.handoverLog); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Go handed over: %s %v", data, err)
			}
			e.assertClean(t)
		})
	}
}

// A Codex coder under coder.sandbox (B93's sandbox dirs) still hands over,
// before any session, worker or run root.
func TestB617SandboxedCodexCoderHandsOver(t *testing.T) {
	config := strings.Replace(b617CodexCoder, "levels:\n", "coder: {sandbox: {profile: workspace}}\nlevels:\n", 1)
	e := b617Setup(t, b617Case{mode: "ok", config: config, codex: true, fakes: []string{"claude"}})
	expectHandover(t, "doctor-run-ping", "doctor")
	if _, err := os.Stat(e.argvLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a provider session ran before the hand-over: %v", err)
	}
	if pids := e.launched(); len(pids) != 0 {
		t.Fatalf("a worker started before the hand-over: %v", pids)
	}
}

// The live worker's daemon: a socket that answers the version-negotiated
// ping is "live", a run with no socket is "fallback" (daemonRequest). The
// goldens cannot show it: Node's golden run sets CODE_FORGE_NATIVE=0.
func TestB617LiveWorkerDaemonRow(t *testing.T) {
	_, repo := b474Project(t, "", 0)
	t.Setenv("CODE_FORGE_NATIVE", "")
	// a short HOME: the socket path must fit sun_path
	short, err := os.MkdirTemp("/tmp", "b617-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(short) })
	home := short
	t.Setenv("HOME", home)
	runs := filepath.Join(home, ".code-forge", "runs")
	workers := filepath.Join(repo, ".code-forge", "workers")
	for _, d := range []string{filepath.Join(runs, "r-b617"), workers} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(filepath.Join(workers, pid+"-unknown.json"), []byte(`{"pid":`+pid+`,"start_time":"unknown","run":"r-b617","started_at":"`+stamp+`","heartbeat_at":"`+stamp+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snap := b617Snapshots["noindex"]
	record := fmt.Sprintf(`{"run_id":"r-b617","config":{"hash":"%s","snapshots":{"%s":%s}}}`, snap[0], snap[0], snap[1])
	if err := os.WriteFile(filepath.Join(runs, "r-b617.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, reason := dfDaemonRow(repo, home); reason != 0 || got.detail != "fallback" {
		t.Fatalf("no socket: %+v %v, want fallback", got, reason)
	}
	ln, err := net.Listen("unix", filepath.Join(runs, "r-b617", "native.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 256)
			_, _ = conn.Read(buf)
			_, _ = conn.Write([]byte(`{"ok":true,"version":"` + version + `","run":"r-b617"}` + "\n"))
			_ = conn.Close()
		}
	}()
	if got, reason := dfDaemonRow(repo, home); reason != 0 || got.detail != "live" {
		t.Fatalf("answering socket: %+v %v, want live", got, reason)
	}
	// a record from before snapshots: {native: {daemon: false}}, no ping
	if err := os.WriteFile(filepath.Join(runs, "r-b617.json"), []byte(`{"run_id":"r-b617"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, reason := dfDaemonRow(repo, home); reason != 0 || got.detail != "off" {
		t.Fatalf("no snapshot: %+v %v, want off", got, reason)
	}
}
