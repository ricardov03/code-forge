package gates

import (
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

// goFailedTests reads go test output (not -json): the unique failed
// top-level test names and the packages they failed in, or nil when a rerun
// of named tests cannot clear the failure.
func goFailedTests(output string, max int) (names, pkgs []string) {
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
		for j := i + 1; j < len(original); j++ {
			a := original[j]
			if a == "-args" || a == "--args" || a == "--" {
				break
			}
			if !strings.HasPrefix(a, "-") {
				continue
			}
			name, _, valued := strings.Cut(strings.TrimLeft(a, "-"), "=")
			switch {
			case Has(goRerunFlags, name):
				argv = append(argv, a)
			case Has(goRerunValued, name) && valued:
				argv = append(argv, a)
			case Has(goRerunValued, name) && j+1 < len(original):
				argv = append(argv, a, original[j+1])
				j++
			}
		}
	}
	argv = append(argv, "-count="+strconv.Itoa(GoFlakyCount), "-run", "^("+strings.Join(names, "|")+")$")
	return append(argv, pkgs...)
}
