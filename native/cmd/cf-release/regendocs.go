// cf-release regen-docs (B959): one command that regenerates every generated doc and test pin,
// once, in order, so a wave that changed help text, a config key or an init default runs it once
// on its combined tip instead of four commands by hand. `--check` runs every step in check mode
// (no record switch) and lists each stale one.
package main

import (
	"fmt"
	"strings"
)

const rdUsage = "usage: cf-release regen-docs [--check]"

// rdGoTests are the Go tests that hold recorded pins: the command summaries (B613), the doc
// example runs (B801) and the init goldens (B989). One `go test` run compiles the package once.
const rdGoTests = "^(TestB613SummariesFile|TestB801DocExamplesRun|TestB528InitWizardMatchesNode|" +
	"TestB611InitWizardJevAndDoctorMatchNode|TestB703InitWizardMatchesNode|TestB726InitWizardMatchesNode|TestB762.*Golden)$"

// rdRecordEnv are the switches that make those tests write their pins instead of failing.
var rdRecordEnv = [][2]string{{"CODE_FORGE_WRITE_SUMMARIES", "1"}, {"CODE_FORGE_B801_RECORD", "1"},
	{"CODE_FORGE_INIT_GOLDEN_RECORD", "1"}}

// rdStep is one generated-doc step: a child command, or (argv nil) the in-process config doc.
// env is set in the child's environment; drop is removed from it.
type rdStep struct {
	name string
	argv []string
	env  [][2]string
	drop []string
}

// rdSpawn runs a child step from the repository root with env as its whole environment; tests
// replace it.
var rdSpawn = func(root string, argv []string, env []string) result {
	return spawn(root, argv, 0, env)
}

func rdSteps(check bool) []rdStep {
	flag := func(argv ...string) []string {
		if check {
			return append(argv, "--check")
		}
		return argv
	}
	goTest := rdStep{name: "test pins (command summaries, B801 doc examples, init goldens)",
		argv: []string{"go", "-C", "native", "test", "./cmd/code-forge-native", "-run", rdGoTests, "-count=1"}}
	if check {
		// a record switch left in the caller's shell must not turn the check into a rewrite
		for _, kv := range rdRecordEnv {
			goTest.drop = append(goTest.drop, kv[0])
		}
	} else {
		goTest.env = rdRecordEnv
	}
	return []rdStep{
		goTest,
		{name: "docs/reference/commands.md and commands.json", argv: flag("sh", "scripts/gen-commands-doc.sh")},
		{name: "docs/reference/config.md"},
	}
}

func regenDocsMain(a *app, args []string, root string) int {
	check := false
	for _, arg := range args {
		if arg != "--check" {
			a.err(rdUsage + "\n")
			return 2
		}
		check = true
	}
	verb := "regen-docs"
	if check {
		verb = "regen-docs --check"
	}
	var stale []string
	for _, step := range rdSteps(check) {
		ok := true
		if step.argv == nil {
			var cdArgs []string
			if check {
				cdArgs = []string{"--check"}
			}
			ok = configDocMain(a, cdArgs, root) == 0
		} else {
			r := rdSpawn(root, step.argv, envWith(step.drop, step.env...))
			if r.failed() || r.code != 0 {
				ok = false
				a.err(rdTail(r.stdout+r.stderr+r.errMsg, 40))
			}
		}
		if ok {
			a.out(fmt.Sprintf("%s: ok: %s\n", verb, step.name))
			continue
		}
		if !check {
			a.err(fmt.Sprintf("%s: failed: %s\n", verb, step.name))
			return 1
		}
		stale = append(stale, step.name)
		a.err(fmt.Sprintf("%s: stale: %s\n", verb, step.name))
	}
	if len(stale) > 0 {
		a.err(fmt.Sprintf("%s: %d stale; run `sh scripts/cf-release.sh regen-docs` once and commit the result\n", verb, len(stale)))
		return 1
	}
	if check {
		a.out("regen-docs --check: every generated doc and pin is up to date\n")
	} else {
		a.out("regen-docs: done; review `git diff` and commit it as one commit\n")
	}
	return 0
}

// rdTail keeps the last n lines of a child's output, so a failing go test shows its FAIL lines.
func rdTail(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n") + "\n"
}
