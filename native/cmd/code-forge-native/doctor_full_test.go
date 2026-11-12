package main

import (
	"os"
	"path/filepath"
	"testing"
)

// B487: a full doctor run (no --quick) Go does not serve names why it hands
// over: a config that loads but fails validation feeds the sweep, daemon and
// indexing rows (B504: Go runs the no-config path, doctor_local_run_test.go),
// and a config Go cannot judge keeps the general reason. B525: a valid config
// runs in Go (doctor_run_test.go).
func TestB487DoctorFullRunHandOverReasons(t *testing.T) {
	for _, c := range []struct {
		name, config, reason string
		args                 []string
	}{
		{"invalid config", "provider: openai\nengine: subprocess\nreview: {multimodel: true, second_provider: openai}\nlevels:\n  L0: {model: gpt-6-luna, effort: max}\n  L1: {model: gpt-6-luna, effort: max}\n  L2: {model: gpt-6-sol}\n  L3: {model: gpt-6-astra}\n", "doctor-full-invalid-config", []string{"doctor", "--json"}},
		{"schema issue", "provider: anthropic\nbogus: 1\n" + b474Levels, "doctor-full-run", []string{"doctor"}},
		{"yaml syntax", "provider: [\n", "doctor-full-run", []string{"doctor"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b474Project(t, c.config, 0)
			expectHandover(t, c.reason, c.args...)
		})
	}
}

// --cwd picks the project the reason is judged on, as the quick run does.
func TestB487DoctorFullRunReasonFollowsCwd(t *testing.T) {
	_, repo := b474Project(t, "provider: anthropic\nbogus: 1\n"+b474Levels, 0)
	empty, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := runCaptured(t, "doctor"); code != 1 {
		t.Fatalf("no config here: Go runs the doctor, got code=%d", code)
	}
	expectHandover(t, "doctor-full-run", "doctor", "--cwd", repo)
	rel, err := filepath.Rel(empty, repo)
	if err != nil {
		t.Fatal(err)
	}
	expectHandover(t, "doctor-full-run", "doctor", "--cwd="+rel)
}
