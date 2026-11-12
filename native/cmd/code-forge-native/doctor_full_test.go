package main

import (
	"os"
	"path/filepath"
	"testing"
)

// B487: the full doctor run (no --quick) still goes to Node, but the hand-over
// names why: a valid config runs the live checks (Jev call, provider CLI
// model probes, worker start and stop), no valid config needs the process
// sweep scan and the tmp/worker-registry helpers Go does not have on this
// branch, and a config Go cannot judge keeps the general reason.
func TestB487DoctorFullRunHandOverReasons(t *testing.T) {
	plain := "provider: anthropic\n" + b474Levels
	for _, c := range []struct {
		name, config, reason string
		args                 []string
	}{
		{"valid config", plain, "doctor-live-probes", []string{"doctor"}},
		{"valid config json", plain, "doctor-live-probes", []string{"doctor", "--json"}},
		{"config with warnings", "provider: anthropic\norchestrator: L3\ncaps: {coders: 3}\n" + b474Levels, "doctor-live-probes", []string{"doctor"}},
		{"no config", "", "doctor-full-no-config", []string{"doctor"}},
		{"invalid config", "provider: openai\nengine: subprocess\nreview: {multimodel: true, second_provider: openai}\nlevels:\n  L0: {model: gpt-6-luna, effort: max}\n  L1: {model: gpt-6-luna, effort: max}\n  L2: {model: gpt-6-sol}\n  L3: {model: gpt-6-astra}\n", "doctor-full-no-config", []string{"doctor", "--json"}},
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
	_, repo := b474Project(t, "provider: anthropic\n"+b474Levels, 0)
	empty, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	expectHandover(t, "doctor-full-no-config", "doctor")
	expectHandover(t, "doctor-live-probes", "doctor", "--cwd", repo)
	rel, err := filepath.Rel(empty, repo)
	if err != nil {
		t.Fatal(err)
	}
	expectHandover(t, "doctor-live-probes", "doctor", "--cwd="+rel)
}
