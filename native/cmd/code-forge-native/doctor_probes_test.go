package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// doctorFakeCLI writes a fake provider CLI: --version runs version, --help and
// exec --help print the help file.
func doctorFakeCLI(t *testing.T, dir, name, version, helpFile string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) " + version + " ;;\n  exec|--help) cat '" + helpFile + "' ;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func doctorRowsText(rows []doctorRow) string {
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%-4s %s: %s\n", r.status, r.label, r.detail)
	}
	return b.String()
}

// B505: probeCli rows for present, failing --version, a help missing one
// flag, a long version line and a missing CLI (texts checked against Node's
// probeCli on the same fake CLIs).
func TestB505DoctorProbeCli(t *testing.T) {
	saved := doctorProbeTimeout
	t.Cleanup(func() { doctorProbeTimeout = saved })
	fixtures, _ := filepath.Abs("../../../test/fixtures/help")
	dir := t.TempDir()
	claudeHelp := filepath.Join(fixtures, "claude-2.1.282.txt")
	data, err := os.ReadFile(claudeHelp)
	if err != nil {
		t.Fatal(err)
	}
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "--max-budget-usd") {
			kept = append(kept, line)
		}
	}
	shortHelp := filepath.Join(dir, "claude-short.txt")
	if err := os.WriteFile(shortHelp, []byte(strings.Join(kept, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	claude := doctorFakeCLI(t, dir, "claude", `echo "  2.1.282 (Claude Code)"; echo second`, claudeHelp)
	codex := doctorFakeCLI(t, dir, "codex", `echo "codex-cli 0.155.1"`, filepath.Join(fixtures, "codex-0.155.1.txt"))
	grok := doctorFakeCLI(t, dir, "grok", `exit 3`, filepath.Join(fixtures, "grok-1.0.34.txt"))
	long := doctorFakeCLI(t, dir, "claude-short", `printf '%s' "`+strings.Repeat("v", 100)+`"`, shortHelp)
	for _, c := range []struct {
		cli, bin, path string
		want           string
		present        bool
	}{
		{"claude", claude, "", "OK   claude: present, 2.1.282 (Claude Code)\nOK   claude flags: every builder flag is in --help\n", true},
		{"codex", codex, "", "OK   codex: present, codex-cli 0.155.1\nOK   codex flags: every builder flag is in --help\n", true},
		{"grok", grok, "", "WARN grok: present, --version failed\nOK   grok flags: every builder flag is in --help\n", true},
		{"claude", long, "", "OK   claude: present, " + strings.Repeat("v", 80) + "\nFAIL claude flags: missing from --help: --max-budget-usd <amount>\n", true},
		{"grok", filepath.Join(dir, "nope"), "", "FAIL grok: not on PATH\n", false},
		{"codex", "codex-not-there", dir, "FAIL codex: not on PATH\n", false},
		{"codex", "codex", dir, "OK   codex: present, codex-cli 0.155.1\nOK   codex flags: every builder flag is in --help\n", true},
	} {
		rows, present, ok := doctorProbeCli(c.cli, c.bin, c.path)
		if !ok || present != c.present || doctorRowsText(rows) != c.want {
			t.Errorf("%s %s: ok=%v present=%v\n%s\nwant\n%s", c.cli, c.bin, ok, present, doctorRowsText(rows), c.want)
		}
	}
}

// B505: configuredProviders keeps first-seen order over levels and fallbacks.
func TestB505DoctorConfiguredProviders(t *testing.T) {
	cfg := map[string]any{"provider": "anthropic", "levels": map[string]any{
		"L0": map[string]any{"provider": "xai"},
		"L1": map[string]any{"fallback": []any{map[string]any{"provider": "openai"}, "bad"}},
		"L2": true,
		"L3": map[string]any{"provider": "openai"},
	}}
	if got := doctorConfiguredProviders(cfg); !reflect.DeepEqual(got, []string{"xai", "anthropic", "openai"}) {
		t.Fatalf("got %v", got)
	}
	if got := doctorConfiguredProviders(map[string]any{"provider": "anthropic"}); got != nil {
		t.Fatalf("no levels: got %v", got)
	}
}
