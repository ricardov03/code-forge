package main

import (
	"bytes"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// B744: cases where Go refused but Node exits with its own text. Each golden in
// testdata/b744/<name>.txt is Node's exit code, stdout and stderr, recorded
// with CODE_FORGE_NATIVE=0 in a fresh HOME (the HOME path is "<H>").

const b744Token = "sk-ant-api03-xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"

type b744Golden struct {
	code           int
	stdout, stderr string
}

func b744Read(t *testing.T, name, home string) b744Golden {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "b744", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(data), "<H>", home)
	head, rest, ok := strings.Cut(text, "\n-- stdout\n")
	if !ok {
		t.Fatalf("%s: no stdout marker", name)
	}
	stdout, stderr, ok := strings.Cut(rest, "-- stderr\n")
	if !ok {
		t.Fatalf("%s: no stderr marker", name)
	}
	g := b744Golden{stdout: stdout, stderr: stderr}
	switch head {
	case "exit 1":
		g.code = 1
	case "exit 2":
		g.code = 2
	default:
		t.Fatalf("%s: head %q", name, head)
	}
	return g
}

// b744Check runs args through the public router (runGuarded) in a fresh HOME
// with Node unavailable and the hand-over log on; setup runs in that HOME and
// cwd before dispatch. Output must be Node's golden and no hand-over row is
// written.
func b744Check(t *testing.T, name string, setup func(home string), args ...string) {
	t.Helper()
	var seam func([]string)
	if setup != nil {
		seam = func([]string) { setup(os.Getenv("HOME")) }
	}
	res := b744InHome(t, seam, args...)
	want := b744Read(t, name, res.home)
	if res.err != nil || res.code != want.code || res.stdout != want.stdout || res.stderr != want.stderr {
		t.Fatalf("%s: exit %d err %v\nstdout %q\nstderr %q\nwant exit %d stdout %q stderr %q", name, res.code, res.err, res.stdout, res.stderr, want.code, want.stdout, want.stderr)
	}
	if _, err := os.Stat(filepath.Join(res.home, ".code-forge", "native", "handovers.jsonl")); !os.IsNotExist(err) {
		rows := b508Rows(t, res.home)
		t.Fatalf("%s: hand-over rows %+v; want none", name, rows)
	}
}

// b744InHome is b508Run with the cwd set to the fresh HOME (Node's goldens
// were recorded with cwd = HOME, so relative paths match).
func b744InHome(t *testing.T, seam func([]string), args ...string) b508Result {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	inner := func(argv []string) {
		home := os.Getenv("HOME")
		if err := os.Chdir(home); err != nil {
			panic(err)
		}
		if seam != nil {
			seam(argv)
		}
	}
	return b508Run(t, inner, args...)
}

func b744Ledger(home string) {
	dir := filepath.Join(home, ".code-forge", "ledger")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	row := `{"event":"run.start","run":"r1","ts":"2026-01-01T00:00:00.000Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "t.jsonl"), []byte(row), 0o644); err != nil {
		panic(err)
	}
}

func b744Chmod(t *testing.T, path string, mode, restore os.FileMode) {
	if err := os.Chmod(path, mode); err != nil {
		panic(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, restore) })
}

// Case 1 and 2: secret-shaped text in --run or --reason no longer refuses;
// the command runs on and prints Node's "no run record" text.
func TestB744SecretShapedArgvRunsInGo(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"close-run-token", []string{"block", "close", "B1", "--run", b744Token}},
		{"close-reason-token", []string{"block", "close", "B1", "--run", "r1", "--reason=" + b744Token}},
		{"close-reason-split", []string{"block", "close", "B1", "--run", "r1", "--reason", b744Token}},
		{"attempt-run-token", []string{"block", "attempt", "B1", "--run", b744Token}},
	} {
		t.Run(c.name, func(t *testing.T) { b744Check(t, c.name, nil, c.args...) })
	}
}

// Case 6: Node's close has no --dry-run; with --decide Go prints Node's
// unknown-flag usage line, exit 2.
func TestB744CloseDryRunDecidePrintsNodeText(t *testing.T) {
	b744Check(t, "close-dryrun-decide", nil, "block", "close", "B1", "--run", "r1", "--dry-run", "--decide", "--reason", "x")
}

// Case 3 and 5: report slugs with bytes that are not UTF-8 and report I/O
// errors print Node's "report: <message>" text, exit 1.
func TestB744ReportErrorsRunInGo(t *testing.T) {
	ledgerFile := func(home string) string { return filepath.Join(home, ".code-forge", "ledger", "t.jsonl") }
	ledgerDir := func(home string) string { return filepath.Join(home, ".code-forge", "ledger") }
	touch := func(path string) {
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			panic(err)
		}
	}
	mkdir := func(path string) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			panic(err)
		}
	}
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, home string)
		args  []string
	}{
		{"report-slug-ff", nil, []string{"report", "--slug", "\xff"}},
		{"report-slug-a-ff-ff-b", nil, []string{"report", "--slug", "a\xff\xffb"}},
		{"report-ledger-dir", func(t *testing.T, h string) { mkdir(ledgerFile(h)) }, []string{"report", "--slug", "t"}},
		{"report-ledger-parent-file", func(t *testing.T, h string) {
			mkdir(filepath.Join(h, ".code-forge"))
			touch(ledgerDir(h))
		}, []string{"report", "--slug", "t"}},
		{"report-ledger-000", func(t *testing.T, h string) { b744Ledger(h); b744Chmod(t, ledgerFile(h), 0, 0o644) }, []string{"report", "--slug", "t"}},
		{"report-ledgerdir-000", func(t *testing.T, h string) { b744Ledger(h); b744Chmod(t, ledgerDir(h), 0, 0o755) }, []string{"report", "--slug", "t"}},
		{"report-ledgerdir-x-only", func(t *testing.T, h string) { b744Ledger(h); b744Chmod(t, ledgerDir(h), 0o100, 0o755) }, []string{"report", "--slug", "t"}},
		{"report-export-under-file", func(t *testing.T, h string) { b744Ledger(h); touch(filepath.Join(h, "blk")) }, []string{"report", "--slug", "t", "--export", "blk/sub"}},
		{"report-export-is-file", func(t *testing.T, h string) { b744Ledger(h); touch(filepath.Join(h, "blk")) }, []string{"report", "--slug", "t", "--export", "blk"}},
		{"report-export-trailing-file", func(t *testing.T, h string) { b744Ledger(h); touch(filepath.Join(h, "f")) }, []string{"report", "--slug", "t", "--export", "f/"}},
		{"report-export-pace-dir", func(t *testing.T, h string) { b744Ledger(h); mkdir(filepath.Join(h, "ex", "pace.json")) }, []string{"report", "--slug", "t", "--export", "ex"}},
		{"report-export-eacces-parent", func(t *testing.T, h string) {
			b744Ledger(h)
			mkdir(filepath.Join(h, "ro"))
			b744Chmod(t, filepath.Join(h, "ro"), 0o555, 0o755)
		}, []string{"report", "--slug", "t", "--export", "ro/a/b"}},
		{"report-export-ro-existing", func(t *testing.T, h string) {
			b744Ledger(h)
			mkdir(filepath.Join(h, "ro"))
			b744Chmod(t, filepath.Join(h, "ro"), 0o555, 0o755)
		}, []string{"report", "--slug", "t", "--export", "ro"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var setup func(string)
			if c.setup != nil {
				setup = func(home string) { c.setup(t, home) }
			}
			b744Check(t, c.name, setup, c.args...)
		})
	}
}

// Case 4: a coder report with secret-shaped text closes (Node writes the
// report's TESTS line as given); the coder.tests row masks the run key, as
// Node's redact(tests.command) does (block.mjs).
func TestB744CloseReportWithSecretTextCloses(t *testing.T) {
	f := newCloseReviewFixture(t, 1)
	b493Approve(t, f)
	key := hex.EncodeToString(bytes.Repeat([]byte{7}, 32))
	report := "TESTS go test ./... --token " + b744Token + " --key " + key + " exit=0\nnote " + b744Token + "\n===BLOCK B1 COMPLETE===\n"
	closeFixtureWrite(t, f.report, []byte(report))
	b744CloseNode(t, f)
	code, out, errs, reason := b493Close(f)
	if code != 0 || out != b744ClosedOut || errs != b493OpenedWarn {
		t.Fatalf("exit=%d reason=%v\nstdout %q\nstderr %q", code, reason, out, errs)
	}
	lines := b493LedgerLines(t, f)
	want := `"command":"go test ./... --token ` + b744Token + ` --key [REDACTED]"`
	if !strings.Contains(lines, want) || strings.Contains(lines, key) {
		t.Fatalf("ledger %s\nwant the coder.tests command %s and no run key", lines, want)
	}
}

const b744ClosedOut = "reviewed file0.txt ticket b82733d3089a96c3991c870e\nblock B1 closed\n"

// b744CloseNode logs Node's result on the same fixture when
// B744_NODE_GOLDEN=1 (how the bytes above were recorded); a no-op otherwise.
func b744CloseNode(t *testing.T, f *closeReviewFixture) {
	t.Helper()
	b760NoNodeRecorder(t, "B744_NODE_GOLDEN")
	if os.Getenv("B744_NODE_GOLDEN") != "1" {
		return
	}
	bin, err := filepath.Abs("../../../bin/code-forge")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "block", "close", "B1", "--run", "r-close-fixture", "--report", f.report, "--transcript", f.transcript)
	cmd.Dir = f.top
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CODE_FORGE_NODE=") && !strings.HasPrefix(kv, "CODE_FORGE_NATIVE=") {
			env = append(env, kv)
		}
	}
	cmd.Env = append(env, "HOME="+f.home, "CODE_FORGE_NATIVE=0", "CODE_FORGE_HANDOVER_LOG=0")
	var out, errs strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errs
	runErr := cmd.Run()
	t.Logf("NODE err=%v\nout=%q\nerr=%q\nledger=%s", runErr, out.String(), strings.ReplaceAll(errs.String(), f.home, "<HOME>"), strings.ReplaceAll(b493LedgerLines(t, f), f.home, "<HOME>"))
	t.Fatal("B744_NODE_GOLDEN run: Node closed the fixture; rerun without it for the Go check")
}
