package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

// B535: the doctor run finishes its I/O errors like Node instead of handing
// over after the probes started. The Node facts came from real Node 24 runs
// (src/cli/doctor.mjs runDoctorCli in-process and src/doctor/probes.mjs on
// testdata/b523/fake.sh) with the same files made unwritable:
//   - an unwritable temp root: testdata/b525/notmproot.golden;
//   - an unwritable run root: each ping `ping=error(EACCES)`, path deny
//     `probe inconclusive (error: EACCES)` (session() catches the throw);
//   - an unwritable work dir: pingRoles and probeIsolation throw
//     `EACCES: permission denied, open '<work>/<role>-<hex>.md'`,
//     probePathDeny throws `EACCES: permission denied, mkdir '<work>/repo-<hex>'`;
//   - an unwritable ~/.code-forge/runs: runDoctor throws
//     `EACCES: permission denied, open '<runs>/doctor-canary-<hex>.txt'`.

// b535Lock makes dir read-only for the test (restored for the cleanup).
func b535Lock(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

var b535RootName = regexp.MustCompile(`doctor-[0-9]+-[0-9]+-[0-9a-f]{6}`)

// A temp root the doctor cannot write: Node's `probes: no temp root` row,
// the signer row and no probe (no CLI row, no session, no worker).
func TestB535DoctorNoTempRootMatchesNode(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("testdata", "b525", "notmproot.golden"))
	if err != nil {
		t.Fatal(err)
	}
	e := b525Setup(t, b525Case{mode: "ok", config: "provider: anthropic\n" + b474Levels, fakes: []string{"claude", "grok"}}, "")
	b535Lock(t, e.tmp)
	code, stdout, stderr := runCaptured(t, "doctor")
	got := b535RootName.ReplaceAllString(e.mask(fmt.Sprintf("exit=%d\n--- stdout\n%s--- stderr\n%s", code, stdout, stderr)), "doctor-<PID>-<MS>-<HEX>")
	if got != string(want) {
		t.Fatalf("Go differs from Node's golden\n--- got\n%s\n--- want\n%s", got, want)
	}
	if _, err := os.Stat(e.handoverLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Go handed over: %v", err)
	}
	if _, err := os.Stat(e.argvLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a provider session ran with no run root: %v", err)
	}
	if pids := e.launched(); len(pids) != 0 {
		t.Fatalf("a worker started with no run root: %v", pids)
	}
}

// A canary file Node cannot write throws out of runDoctor: the launcher's
// crash line, exit 1, no stdout, the run root and the worker gone.
func TestB535DoctorCanaryThrowCrashesLikeNode(t *testing.T) {
	e := b525Setup(t, b525Case{mode: "ok", config: "provider: anthropic\n" + b474Levels, fakes: []string{"claude", "grok"}}, "")
	runs := filepath.Join(e.home, ".code-forge", "runs")
	b535Lock(t, runs)
	code, stdout, stderr := runCaptured(t, "doctor")
	want := regexp.MustCompile(`^code-forge: verb "doctor" failed: Error: EACCES: permission denied, open '` + regexp.QuoteMeta(runs) + `/doctor-canary-[0-9a-f]{12}\.txt'\n$`)
	if code != 1 || stdout != "" || !want.MatchString(stderr) {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want Node's crash line", code, stdout, stderr)
	}
	if _, err := os.Stat(e.handoverLog); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Go handed over: %v", err)
	}
	e.assertClean(t)
	if left := b524List(t, runs); left != "[]" {
		t.Fatalf("the runs dir got %s", left)
	}
}

func b535Cfg() map[string]any {
	cfg := b523Cfgs()["anth"]
	delete(cfg, "autopilot")
	return cfg
}

// A session that cannot start (its session dir under the run root) is
// Node's caught `error` result, not a hand-over.
func TestB535DoctorProbeSessionErrorRows(t *testing.T) {
	e := b523Setup(t, "")
	b535Lock(t, e.runRoot)
	c := e.ctx(b535Cfg(), "ok")
	rows, ok := doctorPingRoles(c, map[string]bool{})
	want := "FAIL ping.facts | ping facts (anthropic/claude-haiku-9) | ping=error(EACCES)\n" +
		"FAIL ping.coder | ping coder (anthropic/claude-sonnet-9) | ping=error(EACCES)\n" +
		"FAIL ping.reviewer | ping reviewer (anthropic/claude-opus-9) | ping=error(EACCES)\n" +
		"FAIL ping.s2 | ping s2 (anthropic/claude-opus-9) | ping=error(EACCES)\n"
	if !ok || b523Rows(rows) != want {
		t.Fatalf("pings ok=%v rows:\n%s\nwant (Node):\n%s", ok, b523Rows(rows), want)
	}
	rows, ok = doctorProbePathDeny(c, e.runs)
	if want := "WARN path-deny | path deny | probe inconclusive (error: EACCES)\n"; !ok || b523Rows(rows) != want {
		t.Fatalf("path deny ok=%v rows:\n%s\nwant (Node):\n%s", ok, b523Rows(rows), want)
	}
	if left := b524List(t, e.runs); left != "[]" {
		t.Fatalf("the canary is left: %s", left)
	}
}

// A prompt file or temp repo Node cannot write throws: the probe says so
// with Node's message and runs no session.
func TestB535DoctorProbeThrows(t *testing.T) {
	for _, tc := range []struct {
		name, kind, doc, want string
	}{
		{"ping prompt", "ping", "", `^EACCES: permission denied, open '<WORK>/facts-[0-9a-f]{8}\.md'$`},
		{"isolation prompt", "iso", "doc", `^EACCES: permission denied, open '<WORK>/isolation-[0-9a-f]{8}\.md'$`},
		{"path deny repo", "deny", "", `^EACCES: permission denied, mkdir '<WORK>/repo-[0-9a-f]{8}'$`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := b523Setup(t, tc.doc)
			b535Lock(t, e.work)
			var thrown string
			c := e.ctx(b535Cfg(), "ok")
			c.crash = &thrown
			var ok bool
			switch tc.kind {
			case "ping":
				_, ok = doctorPingRoles(c, map[string]bool{})
			case "iso":
				_, ok = doctorProbeIsolation(c)
			default:
				_, ok = doctorProbePathDeny(c, e.runs)
			}
			got := strings.ReplaceAll(thrown, e.work, "<WORK>")
			if ok || !regexp.MustCompile(tc.want).MatchString(got) {
				t.Fatalf("ok=%v thrown=%q, want Node's throw %s", ok, got, tc.want)
			}
			if data, _ := os.ReadFile(e.log); len(data) != 0 {
				t.Fatalf("a session ran: %s", data)
			}
			if left := b524List(t, e.runs); left != "[]" {
				t.Fatalf("the canary is left: %s", left)
			}
		})
	}
}

// An errno Node has no name for prints `Unknown system error -<n>` (a real
// Node 24 doctor run with errno 80 and 250 injected into the worker probe's
// mkdir printed `could not start (Unknown system error -80)` and `-250`).
func TestB535DoctorUnknownErrno(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{syscall.Errno(250), "Unknown system error -250"},
		{&os.PathError{Op: "mkdir", Path: "x", Err: syscall.Errno(4000)}, "Unknown system error -4000"},
		{syscall.ENOEXEC, "ENOEXEC"},
		{syscall.ESOCKTNOSUPPORT, "ESOCKTNOSUPPORT"},
	} {
		if got := dwpCode(c.err); got != c.want {
			t.Errorf("dwpCode(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}
