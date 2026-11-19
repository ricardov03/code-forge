package main

import (
	"strings"
	"testing"
)

// B877 (B801 audit P05): status, decisions and docs answer --help (and -h)
// with the help page on stdout and exit 0, like every other command; before
// they exited 2 with "unknown flag" or the bare usage line on stderr.
func TestB877HelpPagesExitZero(t *testing.T) {
	cases := []struct {
		args  []string
		usage string
	}{
		{[]string{"status", "--help"}, "usage: code-forge status [--run <id>] [--json]"},
		{[]string{"status", "-h"}, "usage: code-forge status [--run <id>] [--json]"},
		{[]string{"decisions", "--help"}, "usage: code-forge decisions list [--run <r>] [--json]"},
		{[]string{"decisions", "list", "--help"}, "usage: code-forge decisions list [--run <r>] [--json]"},
		{[]string{"decisions", "list", "-h"}, "usage: code-forge decisions list [--run <r>] [--json]"},
		{[]string{"docs", "--help"}, "usage: code-forge docs sync|path [--json]"},
		{[]string{"docs", "sync", "--help"}, "usage: code-forge docs sync [--json]"},
		{[]string{"docs", "path", "--help"}, "usage: code-forge docs path [--json]"},
		{[]string{"docs", "path", "-h"}, "usage: code-forge docs path [--json]"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			t.Setenv("NO_COLOR", "1")
			res, repo := b690aInRepo(t, c.args...)
			if res.code != 0 || res.err != nil || res.stderr != "" {
				t.Fatalf("code=%d err=%v stderr=%q; want exit 0 and no stderr", res.code, res.err, res.stderr)
			}
			if !strings.HasPrefix(res.stdout, "Description:\n") || !strings.Contains(res.stdout, "\nUsage:\n  "+c.usage+"\n") || !strings.Contains(res.stdout, "-h, --help") {
				t.Fatalf("stdout=%q; want the help page with %q", res.stdout, c.usage)
			}
			if files := append(b690aFiles(t, repo), b690aFiles(t, res.home)...); len(files) != 0 {
				t.Errorf("files written %q; want none", files)
			}
		})
	}
}

// `help status` (and the others) shows the usage from stdout now, and a
// --help after other flags still answers with the usage, exit 0.
func TestB877HelpAfterOtherFlags(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	for _, args := range [][]string{
		{"status", "--json", "--help"},
		{"decisions", "list", "--json", "--help"},
		{"docs", "sync", "--json", "--help"},
	} {
		res, _ := b690aInRepo(t, args...)
		if res.code != 0 || res.stderr != "" || !strings.HasPrefix(res.stdout, "usage: code-forge "+args[0]) {
			t.Errorf("%q: code=%d stdout=%q stderr=%q; want exit 0 and the usage", args, res.code, res.stdout, res.stderr)
		}
	}
	for _, name := range []string{"status", "decisions list", "docs path"} {
		res, _ := b690aInRepo(t, append([]string{"help"}, strings.Fields(name)...)...)
		if res.code != 0 || !strings.Contains(res.stdout, "Usage:\n  usage: code-forge "+name) {
			t.Errorf("help %s: code=%d stdout=%q; want the page with its usage", name, res.code, res.stdout)
		}
	}
}
