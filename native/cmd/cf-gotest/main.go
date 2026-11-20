// Command cf-gotest is code-forge's own test gate (B760; maintainer tool, not shipped). The
// repository's .code-forge.yml runs it as `go -C native run ./cmd/cf-gotest {test_files}`: the gate
// passes the selected Go files as paths from the repository root, and cf-gotest runs `go test
// -count=1` on the packages that hold them, from native/. With no file it runs every package (the
// full suite). Files outside native/ are not Go packages here and are left out.
//
// B952: the go child runs with -trimpath added to GOFLAGS. Without it Go keys each compile by the
// package's directory, so every worktree compiles every native and vendored package again; with it
// the key is the module path, and a new worktree reuses the build cache of the others.
//
// B953: load-aware parallelism. go test gets -p 2 (at most two packages build and run at once)
// unless `--p N` sets N; when the 1-minute load average is over the number of cores and the caller
// set no GOMAXPROCS, the go child gets GOMAXPROCS=cores/2, so several coders' gates cannot push the
// machine load far over its cores. Flags come before the files:
//
//	cf-gotest [--p N] [file ...]
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// defaultP is go test's -p when --p is not given (B953).
const defaultP = 2

// loadAvg and numCPU read the machine; tests replace them.
var (
	loadAvg = machineLoad
	numCPU  = runtime.NumCPU
)

const usage = "usage: cf-gotest [--p N] [file ...]"

type options struct {
	p int
}

// parseFlags reads the leading flags; the rest are the selected files.
func parseFlags(args []string) (options, []string, error) {
	o := options{p: defaultP}
	for len(args) > 0 && strings.HasPrefix(args[0], "--") {
		name, val, hasVal := strings.Cut(strings.TrimPrefix(args[0], "--"), "=")
		if name == "" { // a bare "--" ends the flags
			return o, args[1:], nil
		}
		args = args[1:]
		if !hasVal {
			if len(args) == 0 {
				return o, nil, fmt.Errorf("--%s needs a value", name)
			}
			val, args = args[0], args[1:]
		}
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return o, nil, fmt.Errorf("--%s needs a whole number of 1 or more, got %q", name, val)
		}
		switch name {
		case "p":
			o.p = n
		default:
			return o, nil, fmt.Errorf("unknown flag --%s", name)
		}
	}
	return o, args, nil
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	o, args, err := parseFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "cf-gotest: %v\n%s\n", err, usage)
		return 2
	}
	pkgs, all := packages(args)
	if !all && len(pkgs) == 0 {
		fmt.Fprintf(stdout, "cf-gotest: no Go package in the selection (%d files)\n", len(args))
		return 0
	}
	if all {
		pkgs = []string{"./..."}
	}
	argv := append([]string{"test", "-count=1", "-timeout", "75m", "-p", strconv.Itoa(o.p)}, pkgs...)
	env := loadCap(goEnv(os.Environ()), stdout)
	fmt.Fprintf(stdout, "cf-gotest: go %s\n", strings.Join(argv, " "))
	cmd := exec.Command("go", argv...)
	cmd.Env = env
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return e.ExitCode()
		}
		fmt.Fprintf(stderr, "cf-gotest: %v\n", err)
		return 1
	}
	return 0
}

// goEnv is env with -trimpath in GOFLAGS (added once, the caller's other GOFLAGS kept; a caller's
// own -trimpath=false is kept too).
func goEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	flags := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GOFLAGS="); ok {
			flags = v
			continue
		}
		out = append(out, kv)
	}
	has := false
	for _, f := range strings.Fields(flags) {
		if name, _, _ := strings.Cut(strings.TrimLeft(f, "-"), "="); name == "trimpath" {
			has = true
		}
	}
	if !has {
		flags = strings.TrimSpace(flags + " -trimpath")
	}
	return append(out, "GOFLAGS="+flags)
}

// loadCap adds GOMAXPROCS=cores/2 to env when the 1-minute load is over the core count and env
// has no GOMAXPROCS of its own (B953), and says so on out.
func loadCap(env []string, out io.Writer) []string {
	for _, kv := range env {
		if strings.HasPrefix(kv, "GOMAXPROCS=") {
			return env
		}
	}
	load, ok := loadAvg()
	cores := numCPU()
	if !ok || load <= float64(cores) {
		return env
	}
	procs := max(1, cores/2)
	fmt.Fprintf(out, "cf-gotest: load %.1f is over %d cores: GOMAXPROCS=%d\n", load, cores, procs)
	return append(env, "GOMAXPROCS="+strconv.Itoa(procs))
}

// machineLoad is the 1-minute load average: /proc/loadavg on Linux, `sysctl -n vm.loadavg`
// ("{ 1.23 2.34 3.45 }") on macOS. ok is false when neither answers.
func machineLoad() (float64, bool) {
	text := ""
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		text = string(data)
	} else if out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output(); err == nil {
		text = string(out)
	}
	return parseLoad(text)
}

// parseLoad reads the first number of a load average line ("0.52 0.58 0.59 1/467 123" or
// "{ 0.52 0.58 0.59 }").
func parseLoad(text string) (float64, bool) {
	fields := strings.Fields(strings.Trim(strings.TrimSpace(text), "{}"))
	if len(fields) == 0 {
		return 0, false
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	return v, err == nil
}

// packages maps repository paths to the native/ module's package patterns, sorted and unique.
// No argument means every package (all is true). A file under a testdata folder belongs to the
// package that holds that folder; vendor/ is skipped.
func packages(args []string) (pkgs []string, all bool) {
	if len(args) == 0 {
		return nil, true
	}
	seen := map[string]bool{}
	for _, a := range args {
		p := filepath.ToSlash(filepath.Clean(a))
		if !strings.HasPrefix(p, "native/") || !strings.HasSuffix(p, ".go") {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(strings.TrimPrefix(p, "native/")))
		if i := strings.Index("/"+dir+"/", "/testdata/"); i >= 0 {
			dir = strings.TrimSuffix(("/" + dir + "/")[:i], "/")
			dir = strings.TrimPrefix(dir, "/")
			if dir == "" {
				dir = "."
			}
		}
		if dir == "vendor" || strings.HasPrefix(dir, "vendor/") {
			continue
		}
		pkg := "./" + dir
		if dir == "." {
			pkg = "."
		}
		if !seen[pkg] {
			seen[pkg] = true
			pkgs = append(pkgs, pkg)
		}
	}
	sort.Strings(pkgs)
	return pkgs, false
}
