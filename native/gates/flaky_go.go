package gates

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/testslot"
)

// B798: a red `go test` gate reruns only the top-level tests it names as
// failed, GoFlakyCount times in one `go test -count` call. All runs pass: the
// gate passes and the result names the tests as flaky (with the machine load
// when they failed). Any rerun failure keeps the original red result. More
// than GoFlakyMax failed names, a panic, a build or setup failure, or a failed
// package with no named test is a real failure and is never rerun.
const (
	GoFlakyMax   = 3
	GoFlakyCount = 3
)

// FlakyLoad is the 1-minute load average when a flaky test failed (a test
// seam; testslot's probe in production).
var FlakyLoad = func() float64 { return testslot.SystemResources().Load1 }

var (
	goFailedName = regexp.MustCompile(`^--- FAIL: ([A-Za-z_][A-Za-z0-9_]*) \(`)
	goFailedPkg  = regexp.MustCompile(`^FAIL\t(\S+)(?:\t.*)?$`)
	goOKPkg      = regexp.MustCompile(`^ok\s+\S+`)
)

// goFailedTests reads go test output (-json events too, B893): the unique failed
// top-level test names and the packages they failed in, or nil when a rerun
// of named tests cannot clear the failure.
func goFailedTests(output string, max int) (names, pkgs []string) {
	if events, ok := goTestEvents(output); ok {
		return goFailedTestsJSON(events, max)
	}
	pending := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(ansi.ReplaceAllString(output, ""), "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "panic: ") || strings.HasPrefix(line, "fatal error: ") ||
			strings.Contains(line, "[build failed]") || strings.Contains(line, "[setup failed]"):
			return nil, nil
		case goFailedName.MatchString(line):
			pending = append(pending, goFailedName.FindStringSubmatch(line)[1])
		case goFailedPkg.MatchString(line):
			if len(pending) == 0 {
				return nil, nil
			}
			pkgs = append(pkgs, goFailedPkg.FindStringSubmatch(line)[1])
			names = append(names, pending...)
			pending = []string{}
		case goOKPkg.MatchString(line) && len(pending) > 0:
			return nil, nil
		}
	}
	names, pkgs = unique(names), unique(pkgs)
	if len(pending) > 0 || len(names) == 0 || len(names) > max {
		return nil, nil
	}
	return names, pkgs
}

// goTestEvent is one line of `go test -json` (test2json) output.
type goTestEvent struct {
	Action, Package, Test, Output, FailedBuild string
}

// goTestEvents reads the `go test -json` events of output (B893); ok is
// false when no line is one, so the text rules apply. Other lines (a
// wrapper's own, stderr) are left out.
func goTestEvents(output string) (events []goTestEvent, ok bool) {
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var e goTestEvent
		if json.Unmarshal([]byte(line), &e) != nil || e.Action == "" || e.Package == "" && e.Action != "build-fail" && e.Action != "build-output" {
			continue
		}
		events = append(events, e)
	}
	return events, len(events) > 0
}

// goFailedTestsJSON is goFailedTests for `go test -json` events (B893), by
// the same rules per package: a package that failed with named top-level
// tests gives them; a panic, a build or setup failure, a failed package
// with no named test, a named test in a package that passed or never
// ended, or more than max names gives nil.
func goFailedTestsJSON(events []goTestEvent, max int) (names, pkgs []string) {
	failed := map[string][]string{}
	order := []string{}
	ended := map[string]string{}
	for _, e := range events {
		out := ansi.ReplaceAllString(e.Output, "")
		if strings.HasPrefix(out, "panic: ") || strings.HasPrefix(out, "fatal error: ") ||
			strings.Contains(out, "[build failed]") || strings.Contains(out, "[setup failed]") ||
			e.Action == "build-fail" || e.FailedBuild != "" {
			return nil, nil
		}
		if _, seen := failed[e.Package]; !seen {
			failed[e.Package] = []string{}
			order = append(order, e.Package)
		}
		switch {
		case e.Action == "fail" && e.Test != "" && !strings.Contains(e.Test, "/"):
			failed[e.Package] = append(failed[e.Package], e.Test)
		case (e.Action == "fail" || e.Action == "pass" || e.Action == "skip") && e.Test == "":
			ended[e.Package] = e.Action
		}
	}
	for _, pkg := range order {
		tests := failed[pkg]
		switch ended[pkg] {
		case "fail":
			if len(tests) == 0 {
				return nil, nil
			}
			pkgs = append(pkgs, pkg)
			names = append(names, tests...)
		default:
			if len(tests) > 0 {
				return nil, nil
			}
		}
	}
	names, pkgs = unique(names), unique(pkgs)
	if len(names) == 0 || len(names) > max {
		return nil, nil
	}
	return names, pkgs
}

// goRerunValued and goRerunFlags are the go test build flags a rerun keeps
// from a direct `go test` gate; every other flag (-run, -count, -json, -v,
// -failfast, test binary flags after -args) is left out.
var (
	goRerunValued = []string{"tags", "timeout", "mod", "ldflags", "gcflags", "asmflags", "modfile", "p"}
	goRerunFlags  = []string{"race", "short", "msan", "asan", "trimpath"}
)

// goRerunArgv is `go [-C dir] test <kept flags> -count=3 -run ^(A|B)$ pkgs`
// for a gate whose program is go (a direct go test, or a go run wrapper that
// prints go test output, like code-forge's own cf-gotest); nil for any other
// program, which is never rerun by name.
func goRerunArgv(original, names, pkgs []string) []string {
	return goRerunBuild(original, nil, names, pkgs)
}

// goEchoedTest is a line where a wrapper names the go test it runs
// ("cf-gotest: go test -count=1 -timeout 75m ./a", "+ go test ./...").
var goEchoedTest = regexp.MustCompile(`(?:^|[\s:])(?:\S*/)?go test\s+(.*)$`)

// goRerunCommand is goRerunArgv for the gate output given (B894): a go run
// wrapper's rerun keeps the go test build flags of the first line where the
// wrapper names its go test (its -timeout 75m, say), since the wrapper's own
// argv does not hold them.
func goRerunCommand(original []string, output string, names, pkgs []string) []string {
	var extra []string
	if goRerunWrapper(original) {
		for _, line := range strings.Split(strings.ReplaceAll(ansi.ReplaceAllString(output, ""), "\r\n", "\n"), "\n") {
			if m := goEchoedTest.FindStringSubmatch(line); m != nil {
				extra = goKeptFlags(strings.Fields(m[1]))
				break
			}
		}
	}
	return goRerunBuild(original, extra, names, pkgs)
}

// goRerunWrapper is true for a go gate that is not a direct go test (a go
// run wrapper).
func goRerunWrapper(original []string) bool {
	i := 1
	if i < len(original) && original[i] == "-C" {
		i += 2
	} else if i < len(original) && strings.HasPrefix(original[i], "-C=") {
		i++
	}
	return i >= len(original) || original[i] != "test"
}

// goKeptFlags is the go test build flags of args that a rerun keeps.
func goKeptFlags(args []string) []string {
	kept := []string{}
	for j := 0; j < len(args); j++ {
		a := args[j]
		if a == "-args" || a == "--args" || a == "--" {
			break
		}
		if !strings.HasPrefix(a, "-") {
			continue
		}
		name, _, valued := strings.Cut(strings.TrimLeft(a, "-"), "=")
		switch {
		case Has(goRerunFlags, name):
			kept = append(kept, a)
		case Has(goRerunValued, name) && valued:
			kept = append(kept, a)
		case Has(goRerunValued, name) && j+1 < len(args):
			kept = append(kept, a, args[j+1])
			j++
		}
	}
	return kept
}

// goRerunBuild is the rerun argv: the gate's -C, its kept go test flags,
// extra, then -count, -run and the packages.
func goRerunBuild(original, extra, names, pkgs []string) []string {
	if len(original) < 2 || strings.TrimSuffix(filepath.Base(original[0]), ".exe") != "go" {
		return nil
	}
	argv := []string{original[0]}
	i := 1
	if original[i] == "-C" && i+1 < len(original) {
		argv = append(argv, "-C", original[i+1])
		i += 2
	} else if strings.HasPrefix(original[i], "-C=") {
		argv = append(argv, original[i])
		i++
	}
	argv = append(argv, "test")
	if i < len(original) && original[i] == "test" {
		argv = append(argv, goKeptFlags(original[i+1:])...)
	}
	argv = append(argv, extra...)
	argv = append(argv, "-count="+strconv.Itoa(GoFlakyCount), "-run", "^("+strings.Join(names, "|")+")$")
	return append(argv, pkgs...)
}
