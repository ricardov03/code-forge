package stepbudget

import (
	"testing"
	"time"
)

func TestB950JudgeBudget(t *testing.T) {
	cfg := DefaultConfig()
	hist := []int64{60_000, 70_000, 80_000, 90_000, 100_000} // median 80s, budget 120s
	if v := Judge("gate", 130_000, hist, cfg); !v.Over || v.BudgetMs != 120_000 || v.MedianMs != 80_000 || v.Source != "measured" || v.Samples != 5 {
		t.Fatalf("over budget: %+v", v)
	}
	if v := Judge("gate", 119_000, hist, cfg); v.Over || v.BudgetMs != 120_000 {
		t.Fatalf("under budget: %+v", v)
	}
	if v := Judge("gate", 999_000, hist[:4], cfg); v.Over || v.Source != "" || v.BudgetMs != 0 {
		t.Fatalf("too few samples must give no budget: %+v", v)
	}
	cfg.FixedMs["land"] = 600_000
	if v := Judge("land", 600_001, nil, cfg); !v.Over || v.Source != "config" || v.BudgetMs != 600_000 {
		t.Fatalf("fixed budget: %+v", v)
	}
	cfg.Margin, cfg.MinSamples = 0, 2
	if v := Judge("coder", 81_000, []int64{80_000, 80_000}, cfg); !v.Over || v.BudgetMs != 80_000 {
		t.Fatalf("margin 0: %+v", v)
	}
}

func TestB950ObserveKeepsHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := Config{Margin: 0.5, MinSamples: 3, FixedMs: map[string]int64{}}
	for _, ms := range []int64{1000, 1000, 1000} {
		v, err := Observe(home, "/w", "land", ms, cfg)
		if err != nil || v.Over {
			t.Fatalf("warm-up sample %d: %+v %v", ms, v, err)
		}
	}
	v, err := Observe(home, "/w", "land", 1600, cfg)
	if err != nil || !v.Over || v.BudgetMs != 1500 || v.Samples != 3 {
		t.Fatalf("4th sample: %+v %v", v, err)
	}
	if other, _ := Observe(home, "/other", "land", 1600, cfg); other.Over || other.Samples != 0 {
		t.Fatalf("another project has its own history: %+v", other)
	}
	if got := Read(home, "/w").Steps["land"]; len(got) != 4 {
		t.Fatalf("history: %v", got)
	}
	for i := 0; i < Keep+5; i++ {
		_, _ = Observe(home, "/w", "gate", 10, cfg)
	}
	if got := Read(home, "/w").Steps["gate"]; len(got) != Keep {
		t.Fatalf("history keeps %d, got %d", Keep, len(got))
	}
	if _, err := Observe(home, "/w", "deploy", 1, cfg); err == nil {
		t.Fatal("an unknown step must be refused")
	}
}

func TestB950Duration(t *testing.T) {
	for ms, want := range map[int64]string{850: "850ms", 42_000: "42s", 750_000: "12m30s", 3_900_000: "1h05m"} {
		if got := Duration(ms); got != want {
			t.Errorf("Duration(%d) = %q, want %q", ms, got, want)
		}
	}
}

// A fake clock times the step: over its budget the sink gets the WARN line,
// the row and the found block, in that order; under it, nothing. (The real
// sink, a signed ledger row and an autopilot found block, is checked in
// native/cmd/code-forge-native/autopilot_step_b950_test.go.)
func TestB950ReportFakeClock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := Config{Margin: 0.5, MinSamples: 1, FixedMs: map[string]int64{}}
	start := time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)
	clock := func(d time.Duration) func() time.Time { return func() time.Time { return start.Add(d) } }
	var got []string
	sink := Sink{
		Warn:  func(line string) { got = append(got, "warn:"+line) },
		Row:   func(v Verdict) { got = append(got, "row:"+v.Step) },
		Found: func(v Verdict) { got = append(got, "found:"+v.Step) },
	}
	step := func(d time.Duration) Verdict {
		v, err := Observe(home, "/w", "gate", Elapsed(start, clock(d)), cfg)
		if err != nil {
			t.Fatal(err)
		}
		Report(v, "B7", sink)
		return v
	}
	if v := step(2 * time.Minute); v.Source != "" || len(got) != 0 { // first sample: no budget yet
		t.Fatalf("first: %+v %q", v, got)
	}
	if v := step(170 * time.Second); v.Over || v.BudgetMs != 180_000 || len(got) != 0 { // under 3m00s
		t.Fatalf("under: %+v %q", v, got)
	}
	step(10 * time.Minute) // median of 2m and 2m50s is 2m25s, budget 3m38s
	want := []string{"warn:WARN step budget: gate took 10m00s, over its budget 3m38s (median 2m25s of 2 runs + margin) (block B7)\n", "row:gate", "found:gate"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("over: %q, want %q", got, want)
	}
	if Elapsed(start, clock(-time.Second)) != 0 {
		t.Fatal("a clock that went back must give 0")
	}
}
