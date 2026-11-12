package main

import (
	"bytes"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// release:site waits for npm with an injected clock: no real sleep, and a timeout leaves nothing changed.
func TestReleaseSiteNpmWaitTimesOut(t *testing.T) {
	f := newFixture(t, fixtureOpts{site: true})
	f.use(map[string]string{"FAKE_NPM_VIEW": "0.20.0"})
	var out, errb bytes.Buffer
	a := newApp(&out, &errb)
	clock := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	var slept []time.Duration
	a.now = func() time.Time { return clock }
	a.sleep = func(d time.Duration) { slept = append(slept, d); clock = clock.Add(d) }
	before := f.state()
	code := run(a, []string{"release-site", "--wait-min", "1.5"}, f.repo)
	if code != 1 {
		t.Fatalf("exit %d, want 1; stderr %q", code, errb.String())
	}
	waits := strings.Count(out.String(), "Waiting for npm to serve @example/pkg@0.21.0 (poll every 30 s)\n")
	if waits != 3 || len(slept) != 3 {
		t.Fatalf("waits %d, sleeps %v, want 3 of each; stdout %q", waits, slept, out.String())
	}
	for _, d := range slept {
		if d != 30*time.Second {
			t.Fatalf("slept %v, want 30s each", slept)
		}
	}
	want := "Timed out after 1.5 min waiting for npm to serve @example/pkg@0.21.0; nothing changed"
	if !strings.Contains(errb.String(), want) {
		t.Fatalf("stderr %q does not name the timeout %q", errb.String(), want)
	}
	files := func(s string) string { return s[:strings.Index(s, "--- fake log\n")] }
	if after := f.state(); files(after) != files(before) {
		t.Fatalf("state changed after a timeout\n%s", firstDiff(files(before), files(after)))
	}
	for _, line := range strings.Split(strings.TrimSpace(f.mask(string(f.readLog()))), "\n") {
		if !strings.HasSuffix(line, " npm view @example/pkg@0.21.0 version") && !strings.Contains(line, " git ") {
			t.Fatalf("a timed-out run ran %q; only npm view polls are allowed", line)
		}
	}
}

func TestJSNumber(t *testing.T) {
	cases := map[string]float64{"": 0, " 20 ": 20, "1.5": 1.5, "0x10": 16, "1e1": 10, ".5": 0.5, "Infinity": math.Inf(1)}
	for in, want := range cases {
		if got := jsNumber(in); got != want {
			t.Errorf("jsNumber(%q) = %v, want %v", in, got, want)
		}
	}
	for _, in := range []string{"abc", "1_0", "0x1_0", "1.2.3"} {
		if got := jsNumber(in); !math.IsNaN(got) {
			t.Errorf("jsNumber(%q) = %v, want NaN", in, got)
		}
	}
}

func TestToFixed2(t *testing.T) {
	cases := map[float64]string{0: "0.00", 1.005: "1.00", 2.675: "2.67", 0.125: "0.13", 12.3456: "12.35", 1.5: "1.50"}
	for in, want := range cases {
		if got := toFixed2(in); got != want {
			t.Errorf("toFixed2(%v) = %q, want %q", in, got, want)
		}
	}
}

func (f *fixture) readLog() []byte {
	data, _ := os.ReadFile(f.env["FAKE_LOG"])
	return data
}
