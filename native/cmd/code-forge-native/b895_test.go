package main

import (
	"strings"
	"testing"
)

// TestB895UnknownFlagNamesTheFlagAndPrintsTheUsage: the verbs that used to
// word the refusal differently (models, validate: "unknown argument";
// resolve: "unknown level"; block settle: an unquoted flag) print the one
// refusal line every verb prints, then the usage `<command> --help` shows,
// exit 2, nothing on stdout. A second usage error that is not an unknown
// flag keeps its own text and gains no usage.
func TestB895UnknownFlagNamesTheFlagAndPrintsTheUsage(t *testing.T) {
	for _, c := range []struct {
		args  []string
		usage string
	}{
		{[]string{"models", "--bogus-flag"}, "usage: code-forge models [--refresh --from-cli-caches]\n"},
		{[]string{"validate", "--bogus-flag"}, "usage: code-forge validate [--file <path>]\n"},
		{[]string{"resolve", "--bogus-flag"}, "usage: code-forge resolve <L0|L1|L2|L3>\n"},
		{[]string{"block", "settle", "B1", "--run", "r1", "--bogus-flag"}, "usage: code-forge block settle <id> --run <r> "},
		{[]string{"run", "end", "--bogus-flag"}, "usage: code-forge run end --run <id>"},
		{[]string{"proof", "tier", "--bogus-flag"}, "usage: code-forge proof tier"},
	} {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			res, _ := b690aInRepo(t, c.args...)
			name := c.args[0]
			if c.args[0] != "models" && c.args[0] != "validate" && c.args[0] != "resolve" {
				name += " " + c.args[1]
			}
			want := name + ": unknown flag \"--bogus-flag\"\n" + c.usage
			if res.code != 2 || res.err != nil || res.stdout != "" || !strings.HasPrefix(res.stderr, want) || strings.Count(strings.ToLower(res.stderr), "usage:") != 1 {
				t.Fatalf("code=%d err=%v stdout=%q stderr=%q; want exit 2 and stderr starting %q with one usage", res.code, res.err, res.stdout, res.stderr, want)
			}
		})
	}
	// Other usage errors are unchanged: no usage is added to them.
	res, _ := b690aInRepo(t, "models", "stray")
	if res.code != 2 || res.stderr != "[code-forge:error] unknown argument: stray\n" {
		t.Fatalf("models stray: code=%d stderr=%q", res.code, res.stderr)
	}
	res, _ = b690aInRepo(t, "resolve", "L9")
	if res.code != 2 || !strings.HasPrefix(res.stderr, "[code-forge:error] unknown level \"L9\"") || strings.Contains(res.stderr, "usage:") {
		t.Fatalf("resolve L9: code=%d stderr=%q", res.code, res.stderr)
	}
}
