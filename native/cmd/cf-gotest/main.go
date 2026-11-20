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
//
// B807: `cf-gotest --shards N [file ...]` runs one package's tests the way CI does, on this
// machine: one `go test -c` build, the test list from that binary, the split of
// scripts/ci-shards.sh (`pick I N`, the same round-robin CI's shards use: one split rule), then N
// runs of the same binary with disjoint -test.run sets, at most --p of them at once. Each shard's
// output is printed whole, in shard order; any red shard makes the run red. With no file it shards
// ./cmd/code-forge-native (the slow package); a selection must be one package.
//
//	cf-gotest --shards N [--p N] [file ...]
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
	"sync"
	"time"
)

// defaultP is go test's -p when --p is not given (B953).
const defaultP = 2

// loadAvg and numCPU read the machine; shardScript is the CI split script and nativeDir the
// folder package paths are under (cf-gotest runs from native/). Tests replace them.
var (
	loadAvg     = machineLoad
	numCPU      = runtime.NumCPU
	shardScript = filepath.Join("..", "scripts", "ci-shards.sh")
	nativeDir   = "."
)

// shardDefaultPkg is the package --shards runs when no file is selected.
const shardDefaultPkg = "./cmd/code-forge-native"

const usage = "usage: cf-gotest [--shards N] [--p N] [file ...]"

type options struct {
	p      int
	shards int // 0: one plain go test run
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
		case "shards":
			o.shards = n
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
	if o.shards > 0 {
		return runShards(o, pkgs, all, stdout, stderr)
	}
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

// runShards is --shards: build once, split the test list with the CI rule, run the shards.
func runShards(o options, pkgs []string, all bool, stdout, stderr io.Writer) int {
	if all {
		pkgs = []string{shardDefaultPkg}
	}
	if len(pkgs) != 1 {
		fmt.Fprintf(stderr, "cf-gotest: --shards runs one package; the selection has %d (%s)\n%s\n", len(pkgs), strings.Join(pkgs, " "), usage)
		return 2
	}
	pkg := pkgs[0]
	env := loadCap(goEnv(os.Environ()), stdout)
	tmp, err := os.MkdirTemp("", "cf-gotest-")
	if err != nil {
		fmt.Fprintf(stderr, "cf-gotest: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, "pkg.test")
	argv := []string{"test", "-c", "-o", bin, "-p", strconv.Itoa(o.p), pkg}
	fmt.Fprintf(stdout, "cf-gotest: go %s\n", strings.Join(argv, " "))
	build := exec.Command("go", argv...)
	build.Dir, build.Env, build.Stdout, build.Stderr = nativeDir, env, stdout, stderr
	if err := build.Run(); err != nil {
		return exitCode(err, stderr)
	}
	dir := filepath.Join(nativeDir, filepath.FromSlash(pkg))
	list := exec.Command(bin, "-test.list", ".*")
	list.Dir, list.Env = dir, env
	listed, err := list.Output()
	if err != nil {
		fmt.Fprintf(stderr, "cf-gotest: listing the tests of %s: %v\n", pkg, err)
		return 1
	}
	full, err := shardPick(string(listed), 0, 1)
	if err != nil || len(full) == 0 {
		fmt.Fprintf(stderr, "cf-gotest: %s has no tests to shard (%v)\n", pkg, err)
		return 1
	}
	shards := make([][]string, o.shards)
	seen := map[string]int{}
	for i := range shards {
		if shards[i], err = shardPick(string(listed), i, o.shards); err != nil {
			fmt.Fprintf(stderr, "cf-gotest: shard %d of %d: %v\n", i+1, o.shards, err)
			return 1
		}
		for _, n := range shards[i] {
			seen[n]++
		}
	}
	// The split must cover every test exactly once, or a test could be lost in silence.
	for _, n := range full {
		if seen[n] != 1 {
			fmt.Fprintf(stderr, "cf-gotest: the split has %s in %d shards, want 1\n", n, seen[n])
			return 1
		}
	}
	if len(seen) != len(full) {
		fmt.Fprintf(stderr, "cf-gotest: the split has %d tests, the list %d\n", len(seen), len(full))
		return 1
	}
	fmt.Fprintf(stdout, "cf-gotest: %d tests of %s in %d shards, %d at once\n", len(full), pkg, o.shards, min(o.p, o.shards))

	type result struct {
		out  []byte
		err  error
		took time.Duration
	}
	results := make([]result, len(shards))
	slots := make(chan struct{}, o.p)
	var wg sync.WaitGroup
	for i, names := range shards {
		if len(names) == 0 {
			continue
		}
		wg.Add(1)
		go func(i int, names []string) {
			defer wg.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			start := time.Now()
			cmd := exec.Command(bin, "-test.run", "^("+strings.Join(names, "|")+")$", "-test.count=1", "-test.timeout", "75m")
			cmd.Dir, cmd.Env = dir, env
			out, err := cmd.CombinedOutput()
			results[i] = result{out, err, time.Since(start)}
		}(i, names)
	}
	wg.Wait()
	red := 0
	for i, names := range shards {
		if len(names) == 0 {
			fmt.Fprintf(stdout, "cf-gotest: shard %d/%d has no tests\n", i+1, o.shards)
			continue
		}
		r := results[i]
		fmt.Fprintf(stdout, "=== cf-gotest shard %d/%d (%d tests)\n", i+1, o.shards, len(names))
		stdout.Write(r.out)
		state := "ok  "
		if r.err != nil {
			state = "FAIL"
			red++
		}
		fmt.Fprintf(stdout, "%s\t%s [shard %d/%d]\t%.1fs\n", state, pkg, i+1, o.shards, r.took.Seconds())
	}
	if red > 0 {
		fmt.Fprintf(stdout, "cf-gotest: %d of %d shards FAIL\n", red, o.shards)
		return 1
	}
	fmt.Fprintf(stdout, "cf-gotest: all %d shards ok\n", o.shards)
	return 0
}

// shardPick is shard i (0-based) of n of the test names in listed, by scripts/ci-shards.sh pick.
func shardPick(listed string, i, n int) ([]string, error) {
	cmd := exec.Command("sh", shardScript, "pick", strconv.Itoa(i), strconv.Itoa(n))
	cmd.Stdin = strings.NewReader(listed)
	var errb strings.Builder
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s pick: %v %s", shardScript, err, strings.TrimSpace(errb.String()))
	}
	return strings.Fields(string(out)), nil
}

func exitCode(err error, stderr io.Writer) int {
	if e, ok := err.(*exec.ExitError); ok {
		return e.ExitCode()
	}
	fmt.Fprintf(stderr, "cf-gotest: %v\n", err)
	return 1
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
