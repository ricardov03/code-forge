package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// B559: Go `logs report` records each npm/gh child in the pid registry of its
// run root like Node's exec (src/util/exec.mjs + reaper.mjs registerPid):
// <run-root>/pids/<pid>.json while the child runs, removed when it settles.
// testdata/b559/fake.sh is a fake npm/gh that waits for its own entry and
// writes what it sees; registry-*.log are Node's (CODE_FORGE_NATIVE=0) logs
// of that fake for the same commands. Masks: every pid -> <PID>, the
// start_time value -> <START>.

var (
	lrRegSeen   = regexp.MustCompile(`(?m)^(\S+) (\S+) (\d+) SEEN \S+ \S+ (\d+)\.json \n\{"pid":(\d+),"start_time":"([^"]*)","argv0":"([^"]*)"\}\n`)
	lrRegStart  = regexp.MustCompile(`"start_time":"[^"]*"`)
	lrRegPid    = regexp.MustCompile(`\b\d+\b`)
	lrRegLstart = regexp.MustCompile(`^[A-Z][a-z]{2} [A-Z][a-z]{2} +\d{1,2} \d{2}:\d{2}:\d{2} \d{4}$`)
)

// lrRegEnv is lrTestEnv with the registry-watching fake first on PATH.
func lrRegEnv(t *testing.T) lrTestCase {
	t.Helper()
	c := lrTestEnv(t, "", false)
	bin := t.TempDir()
	fake, err := os.ReadFile(filepath.Join(lrTestData, "..", "b559", "fake.sh"))
	if err != nil {
		t.Fatal(err)
	}
	b500Write(t, filepath.Join(bin, "fake"), string(fake))
	if err := os.Chmod(filepath.Join(bin, "fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"npm", "gh"} {
		if err := os.Symlink("fake", filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	return c
}

// lrRegCheck compares the fake's log with Node's after the masks, and checks
// each entry names the child that read it, with a real ps start time.
func lrRegCheck(t *testing.T, c lrTestCase, golden string) {
	t.Helper()
	got := c.fakeLog(t)
	seen := lrRegSeen.FindAllStringSubmatch(got, -1)
	if len(seen) != strings.Count(got, " SEEN ") {
		t.Errorf("%d of %d entries parsed:\n%s", len(seen), strings.Count(got, " SEEN "), got)
	}
	for _, m := range seen {
		if m[3] != m[4] || m[3] != m[5] {
			t.Errorf("entry is not the child's own: %q", m[0])
		}
		if m[1] != m[7] {
			t.Errorf("argv0 %q for %s", m[7], m[1])
		}
		if !lrRegLstart.MatchString(m[6]) {
			t.Errorf("start_time %q is not ps lstart", m[6])
		}
	}
	masked := lrRegPid.ReplaceAllString(lrRegStart.ReplaceAllString(got, `"start_time":"<START>"`), "<PID>")
	if want := b500Read(t, filepath.Join(lrTestData, "..", "b559", golden)); masked != want {
		t.Errorf("registry seen by the children:\n%s\nwant (Node):\n%s", masked, want)
	}
}

func TestB559LogsReportDryRunRegistersNpm(t *testing.T) {
	c := lrRegEnv(t)
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--dry-run")
	lrTestCheck(t, "dry run", code, out, errText, 0, c.golden(t, "dry.stdout"), "")
	lrRegCheck(t, c, "registry-dry-run.log")
	if left := c.rootLeft(t); len(left) != 0 {
		t.Errorf("run root or entry left behind: %v", left)
	}
	if b500HandedOver(t, c.home) != "" {
		t.Errorf("handed over: %s", b500HandedOver(t, c.home))
	}
}

func TestB559LogsReportSendRegistersEveryChild(t *testing.T) {
	c := lrRegEnv(t)
	code, out, _ := b500Run(t, "logs", "report", "--no-ai", "--yes")
	if code != 0 || !strings.HasSuffix(out, "sent: https://github.com/x/y/issues/42\n") {
		t.Errorf("send: got %d %q", code, out)
	}
	// Each child sees only its own entry: the one before it was removed when it settled.
	lrRegCheck(t, c, "registry-yes.log")
	if left := c.rootLeft(t); len(left) != 0 {
		t.Errorf("run root or entry left behind: %v", left)
	}
}

func TestB559LogsReportUnwritableRegistryStillRunsChildren(t *testing.T) {
	c := lrRegEnv(t)
	ro := t.TempDir()
	if err := os.Chmod(ro, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(ro, 0o700) })
	t.Setenv("CODE_FORGE_TMP_ROOT", ro)
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--yes")
	if code != 0 || errText != "" || !strings.Contains(out, "Open this link to file the issue:\n") {
		t.Errorf("unwritable registry: got %d\n%s\n%s", code, out, errText)
	}
	lrRegCheck(t, c, "registry-ro.log")
	if b500HandedOver(t, c.home) != "" {
		t.Errorf("handed over: %s", b500HandedOver(t, c.home))
	}
}
