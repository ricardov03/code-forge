package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
)

func testCfg() map[string]any {
	return map[string]any{"levels": map[string]any{
		"L1": map[string]any{"provider": "anthropic", "model": "claude-x", "effort": "high",
			"fallback": []any{map[string]any{"provider": "xai", "model": "grok-x"}}},
	}}
}

func testSetup(t *testing.T) (Opts, string) {
	t.Helper()
	dir := t.TempDir()
	prompt := filepath.Join(dir, "brief.md")
	if err := os.WriteFile(prompt, []byte("review this\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "root")
	return Opts{Cfg: testCfg(), Level: "L1", Role: "reviewer", PromptPath: prompt, RunRoot: root}, dir
}

func TestBackoffMsIsSpawnMjsJitter(t *testing.T) {
	for _, c := range []struct {
		n    int
		r, w float64
	}{{0, 0.5, 2000}, {0, 0, 1400}, {0, 1, 2600}, {1, 0.5, 6000}, {1, 0, 4200}} {
		if got := BackoffMs(c.n, func() float64 { return c.r }); got != c.w {
			t.Fatalf("BackoffMs(%d, %v) = %v, want %v", c.n, c.r, got, c.w)
		}
	}
}

// A rate-limited step waits twice through the Sleep seam (no real sleep),
// then the ladder moves on to the next step.
func TestSpawnRateLimitRetriesThroughSleepSeam(t *testing.T) {
	o, _ := testSetup(t)
	var calls []string
	var slept []float64
	var errText strings.Builder
	deps := Deps{
		Stderr: &errText,
		Sleep:  func(ms float64) { slept = append(slept, ms) },
		Random: func() float64 { return 0.5 },
		Exec: func(e execOpts) execResult {
			calls = append(calls, e.Argv[0])
			if e.Argv[0] == "claude" {
				return execResult{Code: 1.0, Stdout: `{"is_error":true,"api_error_status":429,"result":"slow down"}`}
			}
			return execResult{OK: true, Code: 0.0, Stdout: `{"text":"plain answer"}`}
		},
		WriteRow: func(ledger.Object) error { return nil },
	}
	res, err := Spawn(o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "claude,claude,claude,grok" || len(slept) != 2 || slept[0] != 2000 || slept[1] != 6000 {
		t.Fatalf("calls=%v slept=%v", calls, slept)
	}
	if res.Get("status") != "ok" || res.Get("provider") != "xai" || len(res.Get("attempts").([]any)) != 4 {
		t.Fatalf("result = %v", res)
	}
	if !strings.Contains(errText.String(), "rate-limited: provider=anthropic model=claude-x retry in 2.0 s\n") ||
		!strings.Contains(errText.String(), "retry in 6.0 s\n") {
		t.Fatalf("stderr = %q", errText.String())
	}
	if left, _ := os.ReadDir(filepath.Join(o.RunRoot, "sessions")); len(left) != 0 {
		t.Fatalf("session dirs left: %d", len(left))
	}
}

// A real child that never answers is stopped by the hard timeout.
func TestSpawnTimeoutStopsTheChild(t *testing.T) {
	o, dir := testSetup(t)
	o.Cfg = map[string]any{"levels": map[string]any{"L1": map[string]any{"provider": "anthropic", "model": "claude-x"}}}
	o.TimeoutMs = 2000 // room for a loaded machine to start the shell
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\necho '{\"type\":\"system\"}'\necho waiting >&2\nexec sleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var errText strings.Builder
	res, err := Spawn(o, Deps{Stderr: &errText, Bins: map[string]string{"claude": bin}, Environ: []string{"PATH=/usr/bin:/bin"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Get("status") != "timeout" || res.Get("stderr_tail") != "waiting\n" {
		t.Fatalf("result = %v", res)
	}
	events, _ := res.Get("stdout_events").([]any)
	if len(events) != 1 || events[0] != "system" {
		t.Fatalf("stdout_events = %v", res.Get("stdout_events"))
	}
}

// ReviewSpawn runs the review's session with the finding schema and the
// call's row fields; CheckReview refuses what Spawn would leave for Node.
func TestReviewSpawnRunsAClosedBookReviewer(t *testing.T) {
	o, _ := testSetup(t)
	here, _ := os.Getwd()
	schema := filepath.Join(here, "..", "..", "src", "review", "finding.schema.json")
	before := FindingSchemaPath
	FindingSchemaPath = func() (string, error) { return schema, nil }
	t.Cleanup(func() { FindingSchemaPath = before })
	var rows []ledger.Object
	var argv []string
	deps := Deps{
		Stderr:   &strings.Builder{},
		WriteRow: func(r ledger.Object) error { rows = append(rows, r); return nil },
		Exec: func(e execOpts) execResult {
			argv = e.Argv
			return execResult{OK: true, Code: 0.0, Stdout: `{"is_error":false,"structured_output":{"passed":true,"summary":"ok","reviewed_hunks":[],"findings":[]}}`}
		},
	}
	spawn := ReviewSpawn(o, deps)
	res, err := spawn(review.SpawnOpts{Level: "L1", Role: "reviewer", PromptPath: o.PromptPath, RowExtra: ledger.Object{{Key: "file", Value: "a.go"}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Get("status") != "ok" || len(rows) != 1 || rows[0].Get("file") != "a.go" || !strings.Contains(strings.Join(argv, " "), "--json-schema") {
		t.Fatalf("res=%v rows=%v argv=%v", res, rows, argv)
	}
	if err := CheckReview(o, "L1", "reviewer"); err != nil {
		t.Fatalf("CheckReview reviewer = %v", err)
	}
	if err := CheckReview(o, "L1", "coder"); !IsUnsupported(err) {
		t.Fatalf("CheckReview coder = %v, want unsupported", err)
	}
	run := "r-1"
	o.Run = &run
	// B482: the run-pinned gate runs in Go, so a run is served
	if err := CheckReview(o, "L1", "reviewer"); err != nil {
		t.Fatalf("CheckReview with a run = %v, want nil", err)
	}
}
