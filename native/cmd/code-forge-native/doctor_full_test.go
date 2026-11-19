package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B487/B787: a full doctor run on a config that does not check clean runs in
// Go: the FAIL config row names the first error and the no-config rows
// follow (no hand-over). B525: a valid config runs in Go (doctor_run_test.go).
func TestB487DoctorFullRunInvalidConfigsRunInGo(t *testing.T) {
	for _, c := range []struct {
		name, config, row string
		args              []string
	}{
		{"invalid config", "provider: openai\nengine: subprocess\nreview: {multimodel: true, second_provider: openai}\nlevels:\n  L0: {model: gpt-6-luna, effort: max}\n  L1: {model: gpt-6-luna, effort: max}\n  L2: {model: gpt-6-sol}\n  L3: {model: gpt-6-astra}\n", `"config"`, []string{"doctor", "--json"}},
		{"schema issue", "provider: anthropic\nbogus: 1\n" + b474Levels, `FAIL config: 1 schema error(s); first: (root) must NOT have additional property "bogus"`, []string{"doctor"}},
		{"yaml syntax", "provider: [\n", "FAIL config: parse-error: .code-forge.yml:", []string{"doctor"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b474Project(t, c.config, 0)
			code, out, errText := cpRunNoHandover(t, c.args...)
			if code != 1 || !strings.Contains(out, c.row) {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errText)
			}
		})
	}
}

// --cwd picks the project the config row is judged on, as the quick run does.
func TestB487DoctorFullRunFollowsCwd(t *testing.T) {
	_, repo := b474Project(t, "provider: anthropic\nbogus: 1\n"+b474Levels, 0)
	empty, _ := filepath.EvalSymlinks(t.TempDir())
	if err := os.Chdir(empty); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := runCaptured(t, "doctor"); code != 1 || strings.Contains(out, "schema error") {
		t.Fatalf("no config here: Go runs the doctor, got code=%d %q", code, out)
	}
	rel, err := filepath.Rel(empty, repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"doctor", "--cwd", repo}, {"doctor", "--cwd=" + rel}} {
		if code, out, _ := cpRunNoHandover(t, args...); code != 1 || !strings.Contains(out, "FAIL config: 1 schema error(s)") {
			t.Fatalf("%q = %d %q", args, code, out)
		}
	}
}
