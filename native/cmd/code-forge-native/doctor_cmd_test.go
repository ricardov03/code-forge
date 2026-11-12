package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Expected bytes in these tests are the Node CLI's output on the same
// fixtures (CODE_FORGE_NATIVE=0, temp HOME, CODE_FORGE_KEY_BACKEND=file).

const b474Levels = "levels:\n  L0: {model: claude-haiku-4-5-20251001}\n  L1: {model: claude-sonnet-5}\n  L2: {model: claude-opus-5-5}\n  L3: {model: claude-fable-5-1}\n"

const (
	b474NoLinks = "WARN links: no harness link recorded (run code-forge init)\n"
	b474NoKey   = "WARN keys: jev key not resolved; System 1 falls back to rules\n"
	b474Usage   = "usage: code-forge doctor [--quick] [--json] [--cwd <dir>]\n"
)

// b474Project is a temp HOME + git repo with the file key backend and a
// pinned clock.
func b474Project(t *testing.T, config string, nowMs float64) (home, repo string) {
	t.Helper()
	home, repo = smallProject(t, config)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "file")
	t.Setenv("CODE_FORGE_KEY_JEV", "")
	saved := doctorNow
	doctorNow = func() float64 { return nowMs }
	t.Cleanup(func() { doctorNow = saved })
	return home, repo
}

func b474Summary(ok, warn, fail int) string {
	return fmt.Sprintf("doctor: %d ok, %d warn, %d fail\n", ok, warn, fail)
}

// Help, usage errors and unknown flags exit like Node.
func TestB474DoctorArgs(t *testing.T) {
	b474Project(t, "", 0)
	checkSmallCases(t, []smallCase{
		{[]string{"doctor", "--help"}, 0, b474Usage, ""},
		{[]string{"doctor", "-h"}, 0, b474Usage, ""},
		{[]string{"doctor", "--json", "--help"}, 2, "", "doctor: unknown flag \"--help\"\n" + b474Usage},
		{[]string{"doctor", "--bogus"}, 2, "", "doctor: unknown flag \"--bogus\"\n" + b474Usage},
		{[]string{"doctor", "x"}, 2, "", b474Usage},
		{[]string{"doctor", "--quick=1"}, 2, "", "doctor: --quick takes no value\n" + b474Usage},
		{[]string{"doctor", "--cwd"}, 2, "", "doctor: --cwd needs a value\n" + b474Usage},
		{[]string{"doctor", "--cwd="}, 2, "", "doctor: --cwd needs a value\n" + b474Usage},
		{[]string{"doctor", "--cwd", "--quick"}, 2, "", "doctor: --cwd needs a value\n" + b474Usage},
		{[]string{"doctor", "--quick", "--quick"}, 2, "", "doctor: --quick given more than once\n" + b474Usage},
	})
}

// The config row: valid, valid with warnings, invalid, missing, --cwd.
func TestB474DoctorQuickConfigRows(t *testing.T) {
	b474Project(t, "provider: anthropic\n"+b474Levels, 0)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, "OK   config: valid\n" + b474NoLinks + b474NoKey, b474Summary(1, 2, 0)}})

	b474Project(t, "provider: anthropic\norchestrator: L3\ncaps: {coders: 3}\n"+b474Levels, 0)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0,
		"WARN config: valid with 2 warning(s); first: orchestrator is L3 but review.multimodel is off\n" + b474NoLinks + b474NoKey, b474Summary(0, 3, 0)}})

	b474Project(t, "provider: openai\nengine: subprocess\nreview: {multimodel: true, second_provider: openai}\nlevels:\n  L0: {model: gpt-6-luna, effort: max}\n  L1: {model: gpt-6-luna, effort: max}\n  L2: {model: gpt-6-sol}\n  L3: {model: gpt-6-astra}\n", 0)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 1,
		"FAIL config: 5 error(s); first: levels.L0 and levels.L1 resolve to the same (provider, model, effort)\n" + b474NoLinks + b474NoKey, b474Summary(0, 2, 1)}})

	_, repo := b474Project(t, "", 0)
	writeHomeFile(t, repo, "sub/.code-forge.yml", "provider: anthropic\n"+b474Levels)
	checkSmallCases(t, []smallCase{
		{[]string{"doctor", "--quick"}, 1, "FAIL config: not-found: .code-forge.yml cannot be loaded (run code-forge init)\n" + b474NoLinks + b474NoKey, b474Summary(0, 2, 1)},
		{[]string{"doctor", "--quick", "--cwd", "sub"}, 0, "OK   config: valid\n" + b474NoLinks + b474NoKey, b474Summary(1, 2, 0)},
		{[]string{"doctor", "--cwd=" + filepath.Join(repo, "sub"), "--quick"}, 0, "OK   config: valid\n" + b474NoLinks + b474NoKey, b474Summary(1, 2, 0)},
	})
}

func b474Install(harness, scope, target string) string {
	return fmt.Sprintf(`{"harness":%q,"scope":%q,"method":"symlink","source":"/x","target":%q,"linked_at":"2026-01-01T00:00:00.000Z"}`, harness, scope, target)
}

// The links row, and a resolved key masked wherever it shows.
func TestB474DoctorQuickLinksAndRedaction(t *testing.T) {
	home, _ := b474Project(t, "provider: anthropic\n"+b474Levels, 0)
	writeHomeFile(t, home, ".code-forge/installs.json", `{"installs":[`+b474Install("claude", "global", home)+`,`+b474Install("codex", "project", filepath.Join(home, "nope"))+`]}`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 1,
		"OK   config: valid\nFAIL links: 1 of 2 link(s) do not resolve: codex/project\n" + b474NoKey, b474Summary(1, 1, 1)}})

	writeHomeFile(t, home, ".code-forge/installs.json", `[`+b474Install("claude", "global", home)+`]`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0,
		"OK   config: valid\nOK   links: 1 link(s) resolve; skill listing UNVERIFIED\n" + b474NoKey, b474Summary(2, 1, 0)}})

	writeHomeFile(t, home, ".code-forge/installs.json", `[`+b474Install("sekret-value-1", "global", "/nonexistent-b474")+`]`)
	t.Setenv("CODE_FORGE_KEY_JEV", "sekret-value-1")
	checkSmallCases(t, []smallCase{
		{[]string{"doctor", "--quick"}, 1,
			"OK   config: valid\nFAIL links: 1 of 1 link(s) do not resolve: [REDACTED]/global\nOK   keys: jev key from env, no expiry\n", b474Summary(2, 0, 1)},
		{[]string{"doctor", "--quick", "--json"}, 1,
			`{"ok":false,"counts":{"OK":2,"WARN":0,"FAIL":1,"INFO":0},"rows":[{"id":"config","status":"OK","label":"config","detail":"valid"},{"id":"links","status":"FAIL","label":"links","detail":"1 of 1 link(s) do not resolve: [REDACTED]/global"},{"id":"keys","status":"OK","label":"keys","detail":"jev key from env, no expiry"}]}` + "\n", b474Summary(2, 0, 1)},
	})
}

// The keys row: env, env: reference, bad keychain name, file store.
func TestB474DoctorQuickKeyRows(t *testing.T) {
	const now = 1_800_000_000_000 - 5*3_600_000
	b474Project(t, "provider: anthropic\nkeys: {jev: 'env:MY_JEV', cache_hours: 6}\n"+b474Levels, now)
	t.Setenv("MY_JEV", "abc123def")
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick", "--json"}, 0,
		`{"ok":true,"counts":{"OK":2,"WARN":1,"FAIL":0,"INFO":0},"rows":[{"id":"config","status":"OK","label":"config","detail":"valid"},{"id":"links","status":"WARN","label":"links","detail":"no harness link recorded (run code-forge init)"},{"id":"keys","status":"OK","label":"keys","detail":"jev key from env, no expiry (cache 6 h)"}]}` + "\n", b474Summary(2, 1, 0)}})

	b474Project(t, "provider: anthropic\nkeys: {jev: 'keychain:Bad'}\n"+b474Levels, now)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 1,
		"OK   config: valid\n" + b474NoLinks + "FAIL keys: jev key resolution failed (TypeError)\n", b474Summary(1, 1, 1)}})

	home, _ := b474Project(t, "provider: anthropic\n"+b474Levels, now)
	writeHomeFile(t, home, ".code-forge/store/jev.key", "rawkey\n")
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0,
		"OK   config: valid\n" + b474NoLinks + "OK   keys: jev key from file, no expiry\n", b474Summary(2, 1, 0)}})
	writeHomeFile(t, home, ".code-forge/store/jev.key", fmt.Sprintf(`{"v":"k1","exp":%d}`, now+1_830_000))
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0,
		"OK   config: valid\n" + b474NoLinks + "WARN keys: jev key from file expires in 30 min; re-link with code-forge keys set jev --op <ref from keys.jev>\n", b474Summary(1, 2, 0)}})

	home, _ = b474Project(t, "provider: anthropic\nkeys: {cache_hours: 6}\n"+b474Levels, now)
	writeHomeFile(t, home, ".code-forge/store/jev.key", `{"v":"k1","exp":1800000000000}`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0,
		"OK   config: valid\n" + b474NoLinks + "OK   keys: jev key from file, expires 2027-01-15T08:00:00.000Z (cache 6 h)\n", b474Summary(2, 1, 0)}})

	home, _ = b474Project(t, "provider: anthropic\n"+b474Levels, now)
	if err := os.MkdirAll(filepath.Join(home, ".code-forge", "store", "jev.key"), 0o700); err != nil {
		t.Fatal(err)
	}
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0,
		"OK   config: valid\n" + b474NoLinks + "WARN keys: jev key not resolved (key store error); System 1 falls back to rules\n", b474Summary(1, 2, 0)}})
}

const b474Ledger = `{"event":"session","role":"reviewer","model":"m-b","run":"r1","block":"B1","lens":"x","status":"ok","duration_ms":3000,"ts":"TS"}
{"event":"session","role":"reviewer","model":"m-b","run":"r1","block":"B2","status":"timeout","duration_ms":4500,"ts":"TS"}
{"event":"session","role":"reviewer","model":"m-a","run":"r1","block":"B1","status":"ok","duration_ms":1234,"ts":"TS"}
{"event":"review.schema_invalid","role":"reviewer","model":"m-a","run":"r1","block":"B1","ts":"TS"}
{"event":"review.result","run":"r1","block":"B1","file":"f.go","sessions":[{"role":"reviewer","model":"m-a","status":"ok","findings":[{"id":"F1"},{"id":"F2"},{"id":"F3"}]},{"role":"reviewer","model":"m-c","status":"ok","duration_ms":10,"section":2,"findings":[{"id":"F1"}]}],"ts":"TS"}
{"event":"review.waived","run":"r1","block":"B1","file":"f.go","finding":"F1","ts":"TS"}
{"event":"review.waived","run":"r1","block":"B1","file":"f.go","finding":"F2","ts":"TS"}
{"event":"review.unwaived","run":"r1","block":"B1","file":"f.go","finding":"F2","ts":"TS"}
{"event":"session","role":"reviewer","model":"m-old","duration_ms":5,"status":"ok","ts":"2020-01-01T00:00:00Z"}
`

// The reviewer rows from the project's ledger, text and JSON.
func TestB474DoctorQuickReviewerRows(t *testing.T) {
	const now = 1_762_862_400_000 // 2025-11-11T12:00:00.000Z
	home, _ := b474Project(t, "provider: anthropic\nproject: {slug: demo}\n"+b474Levels, now)
	writeHomeFile(t, home, ".code-forge/ledger/demo.jsonl", strings.ReplaceAll(b474Ledger, "TS", "2025-11-11T11:00:00.000Z"))
	lines := "INFO reviewer m-a: 1 sessions, median 1.234 s, ok 0%, waived 33%\nINFO reviewer m-b: 2 sessions, median 3.75 s, ok 50%, waived 0%\nINFO reviewer m-c: 1 sessions, median 0.01 s, ok 100%, waived 0%\n"
	checkSmallCases(t, []smallCase{
		{[]string{"doctor", "--quick"}, 0, "OK   config: valid\n" + b474NoLinks + b474NoKey + lines, b474Summary(1, 2, 0)},
		{[]string{"doctor", "--json", "--quick"}, 0, `{"ok":true,"counts":{"OK":1,"WARN":2,"FAIL":0,"INFO":3},"rows":[{"id":"config","status":"OK","label":"config","detail":"valid"},{"id":"links","status":"WARN","label":"links","detail":"no harness link recorded (run code-forge init)"},{"id":"keys","status":"WARN","label":"keys","detail":"jev key not resolved; System 1 falls back to rules"},{"id":"reviewer.0","status":"INFO","label":"reviewer m-a","detail":"1 sessions, median 1.234 s, ok 0%, waived 33%"},{"id":"reviewer.1","status":"INFO","label":"reviewer m-b","detail":"2 sessions, median 3.75 s, ok 50%, waived 0%"},{"id":"reviewer.2","status":"INFO","label":"reviewer m-c","detail":"1 sessions, median 0.01 s, ok 100%, waived 0%"}]}` + "\n", b474Summary(1, 2, 0)},
	})
	// a review.adaptive window that ends before the rows leaves no lines;
	// so does a corrupt middle line (Node's catch)
	home, _ = b474Project(t, "provider: anthropic\nproject: {slug: demo}\nreview: {adaptive: {window_days: 1}}\n"+b474Levels, now+2*86_400_000)
	writeHomeFile(t, home, ".code-forge/ledger/demo.jsonl", strings.ReplaceAll(b474Ledger, "TS", "2025-11-11T11:00:00.000Z"))
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, "OK   config: valid\n" + b474NoLinks + b474NoKey, b474Summary(1, 2, 0)}})
	home, _ = b474Project(t, "provider: anthropic\nproject: {slug: demo}\n"+b474Levels, now)
	writeHomeFile(t, home, ".code-forge/ledger/demo.jsonl", "{bad\n"+strings.ReplaceAll(b474Ledger, "TS", "2025-11-11T11:00:00.000Z"))
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, "OK   config: valid\n" + b474NoLinks + b474NoKey, b474Summary(1, 2, 0)}})
}

// The cases Go does not serve go to Node with their own reasons.
func TestB474DoctorHandsOverUnportedCases(t *testing.T) {
	// B525: the full run with a valid config runs in Go (doctor_run_test.go).
	plain := "provider: anthropic\n" + b474Levels
	b474Project(t, plain, 0)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	expectHandover(t, "doctor-key-store", "doctor", "--quick")

	for _, c := range []struct{ config, reason string }{
		{"provider: anthropic\nbogus: 1\n" + b474Levels, "doctor-config-schema"},
		{"provider: anthropic\ngates: {test: [npm, sk-abcdefghijklmnopqrstu]}\n" + b474Levels, "doctor-config-secrets"},
		{"provider: anthropic\ncoder: {sandbox: {add_dirs: [\"/\"]}}\n" + b474Levels, "doctor-sandbox-dirs"},
		{"provider: [\n", "config-parse-error"},
	} {
		b474Project(t, c.config, 0)
		expectHandover(t, c.reason, "doctor", "--quick")
	}

	home, _ := b474Project(t, "provider: anthropic\nproject: {slug: demo}\n"+b474Levels, 0)
	writeHomeFile(t, home, ".code-forge/ledger/demo.jsonl", `{"event":"session","role":"reviewer","model":"m","ts":"yesterday"}`+"\n")
	expectHandover(t, "doctor-ledger-shape", "doctor", "--quick")
}

// Without HOME, Node reads the passwd entry: Go hands the run over.
func TestB474DoctorWithoutHomeHandsOver(t *testing.T) {
	b474Project(t, "provider: anthropic\n"+b474Levels, 0)
	t.Setenv("HOME", "")
	code, out, errText := runCaptured(t, "doctor", "--quick")
	if code != 127 || out != "" || !strings.HasPrefix(errText, "code-forge-native: node executable") {
		t.Fatalf("want a Node handover, got code=%d stdout=%q stderr=%q", code, out, errText)
	}
}
