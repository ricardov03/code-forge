package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// b878InGatesRepo runs args in a temp folder whose .code-forge.yml has a
// test gate that writes a marker file, so a test can see whether a gate ran.
func b878InGatesRepo(t *testing.T, args ...string) (b508Result, string) {
	t.Helper()
	repo := t.TempDir()
	marker := filepath.Join(t.TempDir(), "gate-ran")
	gate := "touch " + strconv.Quote(marker)
	if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte("version: 1\nprovider: anthropic\n"+b474Levels+"gates:\n  test: [/bin/sh, -c, "+strconv.Quote(gate)+"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(repo); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(cwd) }()
	return b508Run(t, nil, args...), marker
}

// B878 (B801 audit P06): gates detect, gates run and version refuse an
// unknown flag with exit 2, the flag named and the usage; before, gates
// detect and gates run ignored it (exit 0, every gate ran) and version
// ignored it too.
func TestB878UnknownFlagIsAUsageError(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"gates", "detect", "--bogus"}, "gates detect: unknown flag \"--bogus\"\nusage: code-forge gates detect [--cwd <dir>]\n"},
		{[]string{"gates", "run", "--bogus"}, "gates run: unknown flag \"--bogus\"\n" + gatesSubUsage["run"]},
		// A typo of --changed used to run the whole suite.
		{[]string{"gates", "run", "--cwd", ".", "--chnaged"}, "gates run: unknown flag \"--chnaged\"\n" + gatesSubUsage["run"]},
		{[]string{"gates", "secret-scan", "--file", "x", "--bogus"}, "gates secret-scan: unknown flag \"--bogus\"\n" + gatesSubUsage["secret-scan"]},
		{[]string{"version", "--bogus"}, "version: unknown flag \"--bogus\"\nusage: code-forge version\n"},
		{[]string{"--version", "--bogus"}, "version: unknown flag \"--bogus\"\nusage: code-forge version\n"},
		{[]string{"version", "extra"}, "version: unexpected argument \"extra\"\nusage: code-forge version\n"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			res, marker := b878InGatesRepo(t, c.args...)
			if res.code != 2 || res.err != nil || res.stdout != "" || res.stderr != c.want {
				t.Fatalf("code=%d err=%v stdout=%q stderr=%q; want exit 2 and %q", res.code, res.err, res.stdout, res.stderr, c.want)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("a gate ran; an unknown flag must stop before any gate")
			}
		})
	}
}

// The flags gates run and gates detect read still work, the value of a
// value flag is not taken for a flag, and the gate runs.
func TestB878KnownGatesFlagsStillRun(t *testing.T) {
	res, marker := b878InGatesRepo(t, "gates", "run", "--cwd", ".", "--timeout-ms", "60000", "--json")
	if res.code != 0 || !strings.HasPrefix(res.stdout, "{\"stack\":") {
		t.Fatalf("gates run: code=%d stdout=%q stderr=%q; want exit 0 and the result line", res.code, res.stdout, res.stderr)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the test gate did not run: %v", err)
	}
	res, _ = b878InGatesRepo(t, "gates", "detect", "--cwd", ".")
	if res.code != 0 || !strings.HasPrefix(res.stdout, "{\"stack\":") {
		t.Fatalf("gates detect: code=%d stdout=%q stderr=%q; want exit 0", res.code, res.stdout, res.stderr)
	}
}

// B878 (B801 audit P12): an unknown verb is a usage error (exit 2) and its
// list names only the visible verbs (not land or native-status).
func TestB878UnknownVerbExitsTwoWithVisibleVerbs(t *testing.T) {
	res, _ := b690aInRepo(t, "no-such-verb")
	if res.code != 2 || res.stdout != "" || !strings.HasPrefix(res.stderr, "code-forge: unknown verb \"no-such-verb\"\nusage: code-forge <command> [options] [arguments]\nVerbs:\n") {
		t.Fatalf("code=%d stdout=%q stderr=%q; want exit 2, the usage and the verb list", res.code, res.stdout, res.stderr)
	}
	for _, hidden := range []string{"land", "native-status"} {
		if strings.Contains(res.stderr, "\n  "+hidden+"\n") {
			t.Errorf("the verb list names the hidden verb %s: %q", hidden, res.stderr)
		}
	}
	for _, entry := range helpCommands {
		if !entry.hidden && !strings.Contains(entry.name, " ") && !strings.Contains(res.stderr, "\n  "+entry.name+"\n") {
			t.Errorf("the verb list misses %s", entry.name)
		}
	}
}

// B878 guard: every command of the help table answers --help with its help
// page (exit 0), and every visible command refuses an unknown flag as a
// usage error: exit 2, nothing on stdout, and stderr naming the flag or
// printing the usage. A new verb, or a verb that starts to ignore unknown
// flags, fails here.
func TestB878EveryCommandHelpAndUnknownFlag(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, entry := range helpCommands {
		words := strings.Fields(entry.name)
		t.Run(entry.name+" --help", func(t *testing.T) {
			res, _ := b690aInRepo(t, append(words, "--help")...)
			if res.code != 0 || res.err != nil || res.stderr != "" || !strings.HasPrefix(res.stdout, "Description:\n") {
				t.Fatalf("code=%d err=%v stdout=%q stderr=%q; want exit 0 and the help page", res.code, res.err, res.stdout, res.stderr)
			}
		})
		if entry.hidden {
			continue
		}
		t.Run(entry.name+" --bogus-flag", func(t *testing.T) {
			res, _ := b690aInRepo(t, append(words, "--bogus-flag")...)
			usage := strings.Contains(strings.ToLower(res.stderr), "usage:")
			if res.code != 2 || res.err != nil || res.stdout != "" || !(usage || strings.Contains(res.stderr, "--bogus-flag")) {
				t.Fatalf("code=%d err=%v stdout=%q stderr=%q; want exit 2, no stdout, and stderr naming --bogus-flag or the usage", res.code, res.err, res.stdout, res.stderr)
			}
		})
	}
}
