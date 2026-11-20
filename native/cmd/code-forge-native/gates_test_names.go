package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
)

const gatesNamesReason = "go_test_names"

type gatesNamedPackage struct {
	Package string
	File    string // repository-relative Go file for a wrapper's {test_files}
	Names   []string
}
type gatesNamedSelection struct {
	Packages []gatesNamedPackage
	Names    []string
	Module   string
}

// Go wrappers (including go -C native run cf-gotest) receive a per-package
// filter through CODE_FORGE_TEST_RUN. Other languages retain their selector.
func gatesNamesModule(cwd string, p gates.Plan) string {
	c := p.Commands["test"]
	if c == nil {
		return ""
	}
	dir := cwd
	if len(c.Argv) > 2 && filepath.Base(c.Argv[0]) == "go" && c.Argv[1] == "-C" {
		dir = filepath.Join(cwd, c.Argv[2])
	}
	if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && info.Mode().IsRegular() {
		return dir
	}
	return ""
}

func gatesGoTestNames(file string) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Name.Name == "TestMain" {
			continue
		}
		name := fn.Name.Name
		if strings.HasPrefix(name, "Test") && len(name) > 4 && !unicode.IsLower([]rune(name[4:])[0]) {
			names = append(names, name)
		}
	}
	return names, nil
}

func gatesNamesSelect(cwd, module, id string, block ledger.Object, listed, changed []string) (*gatesNamedSelection, error) {
	prefix, err := filepath.Rel(cwd, module)
	if err != nil {
		return nil, err
	}
	prefix = filepath.ToSlash(prefix)
	local := func(f string) string {
		f = path.Clean(filepath.ToSlash(f))
		if prefix != "." {
			if !strings.HasPrefix(f, prefix+"/") {
				return ""
			}
			f = strings.TrimPrefix(f, prefix+"/")
		}
		return f
	}
	changedSet := map[string]bool{}
	localChanged := []string{}
	for _, f := range changed {
		changedSet[f] = true
		if l := local(f); l != "" {
			localChanged = append(localChanged, l)
		}
	}
	pkgs, mod := gpkPackages(module, localChanged)
	wanted := map[string]bool{}
	for _, p := range pkgs {
		wanted[p] = true
	}
	explicit := gates.Strings(block.Get("test_files"))
	acceptance, _ := block.Get("acceptance").([]any)
	for _, v := range acceptance {
		explicit = append(explicit, gates.Strings(gatesObj(v).Get("tests"))...)
	}
	requested := map[string]bool{}
	for _, s := range explicit {
		requested[s] = true
	}
	type found struct {
		file, pkg string
		names     []string
	}
	tests := []found{}
	reps := map[string]string{}
	for _, file := range listed {
		l := local(file)
		if l == "" || !strings.HasSuffix(l, ".go") || !gpkTested(module, path.Dir(l)) {
			continue
		}
		pkg := "./" + path.Dir(l)
		if pkg == "./." {
			pkg = "."
		}
		if reps[pkg] == "" {
			reps[pkg] = file
		}
		if mod {
			wanted[pkg] = true
		}
		if !strings.HasSuffix(l, "_test.go") {
			continue
		}
		names, err := gatesGoTestNames(filepath.Join(cwd, filepath.FromSlash(file)))
		if err != nil {
			return nil, fmt.Errorf("changed tests: %w", err)
		}
		tests = append(tests, found{file, pkg, names})
	}
	selected := map[string]map[string]bool{}
	for _, f := range tests {
		for _, name := range f.names {
			blockTest := strings.HasPrefix(name, "Test"+id)
			if blockTest {
				rest := strings.TrimPrefix(name, "Test"+id)
				blockTest = rest == "" || rest[0] < '0' || rest[0] > '9'
			}
			if changedSet[f.file] || blockTest || requested[name] || requested[f.file] || requested[local(f.file)] {
				wanted[f.pkg] = true
				if selected[f.pkg] == nil {
					selected[f.pkg] = map[string]bool{}
				}
				selected[f.pkg][name] = true
			}
		}
	}
	out := &gatesNamedSelection{Module: module, Names: []string{}}
	all := map[string]bool{}
	for pkg := range wanted {
		p := gatesNamedPackage{Package: pkg, File: reps[pkg], Names: []string{}}
		for name := range selected[pkg] {
			p.Names = append(p.Names, name)
			all[name] = true
		}
		sort.Strings(p.Names)
		// A missing selection is not evidence that this package's tests pass.
		// Let the caller use its declared gate and ordinary changed scope.
		if len(p.Names) == 0 {
			return nil, nil
		}
		out.Packages = append(out.Packages, p)
	}
	sort.Slice(out.Packages, func(i, j int) bool { return out.Packages[i].Package < out.Packages[j].Package })
	for name := range all {
		out.Names = append(out.Names, name)
	}
	sort.Strings(out.Names)
	return out, nil
}

func (s *gatesNamedSelection) fields() ledger.Object {
	packages := []string{}
	count := 0
	for _, p := range s.Packages {
		packages = append(packages, p.Package)
		count += len(p.Names)
	}
	return ledger.Object{{Key: "tests_only", Value: fmt.Sprintf("changed tests only (%d tests)", count)}, {Key: "test_count", Value: count}, {Key: "test_names", Value: s.Names}, {Key: "packages", Value: packages}}
}

func gatesNamesPattern(names []string) string {
	if len(names) == 0 {
		return "^$"
	}
	escaped := []string{}
	for _, n := range names {
		escaped = append(escaped, regexp.QuoteMeta(n))
	}
	return "^(" + strings.Join(escaped, "|") + ")$"
}

func gatesNamesGoVerb(argv []string) (int, string) {
	if len(argv) < 2 || filepath.Base(argv[0]) != "go" {
		return 0, ""
	}
	i := 1
	if argv[i] == "-C" {
		i += 2
	}
	if i >= len(argv) {
		return 0, ""
	}
	return i, argv[i]
}

// Keep the caller's build flags while replacing package operands and any old
// test filter/count. A direct go test must never retain a broad -run flag.
func gatesNamesGoArgv(argv []string, pkgs []string, filter string) []string {
	i, verb := gatesNamesGoVerb(argv)
	out := append([]string{}, argv[:i+1]...)
	values := map[string]bool{"-run": true, "-count": true, "-p": true, "-timeout": true, "-tags": true, "-mod": true, "-modfile": true, "-overlay": true, "-coverpkg": true, "-coverprofile": true, "-covermode": true, "-parallel": true, "-cpu": true, "-shuffle": true, "-gcflags": true, "-ldflags": true, "-asmflags": true, "-toolexec": true, "-vet": true, "-o": true, "-bench": true, "-benchtime": true, "-cpuprofile": true, "-memprofile": true, "-memprofilerate": true, "-blockprofile": true, "-blockprofilerate": true, "-mutexprofile": true, "-mutexprofilefraction": true, "-trace": true, "-outputdir": true, "-fuzz": true, "-fuzztime": true, "-fuzzminimizetime": true, "-exec": true, "-pkgdir": true, "-buildmode": true, "-compiler": true, "-installsuffix": true}
	for j := i + 1; j < len(argv); j++ {
		a := argv[j]
		key, _, equal := strings.Cut(a, "=")
		drop := verb == "test" && (key == "-run" || key == "-count" || key == "-trimpath")
		if !strings.HasPrefix(a, "-") {
			continue
		}
		if !drop {
			out = append(out, a)
		}
		if values[key] && !equal && j+1 < len(argv) {
			j++
			if !drop {
				out = append(out, argv[j])
			}
		}
	}
	out = append(out, pkgs...)
	if verb == "test" {
		out = append(out, "-run", filter, "-count=1", "-trimpath")
	}
	return out
}

func gatesNamesPlan(p gates.Plan, s *gatesNamedSelection, pkg *gatesNamedPackage) gates.Plan {
	commands := map[string]*gates.Command{}
	pkgs := []string{}
	for _, entry := range s.Packages {
		pkgs = append(pkgs, entry.Package)
	}
	for name, c := range p.Commands {
		if c == nil {
			continue
		}
		copy := *c
		_, verb := gatesNamesGoVerb(c.Argv)
		if name != "test" && (verb == "vet" || verb == "build") {
			if len(pkgs) == 0 {
				continue
			}
			copy.Argv = gatesNamesGoArgv(c.Argv, pkgs, "")
		}
		if name == "test" {
			if pkg == nil {
				continue
			}
			filter := gatesNamesPattern(pkg.Names)
			if verb == "test" {
				copy.Argv = gatesNamesGoArgv(c.Argv, []string{pkg.Package}, filter)
			} else {
				copy.Argv = []string{}
				for _, a := range c.Argv {
					if a == "{test_files}" {
						copy.Argv = append(copy.Argv, pkg.File)
					} else {
						copy.Argv = append(copy.Argv, a)
					}
				}
				if !gates.Has(c.Argv, "{test_files}") {
					copy.Argv = append(copy.Argv, pkg.File)
				}
			}
			copy.Env = map[string]string{}
			for k, v := range c.Env {
				copy.Env[k] = v
			}
			copy.Env["CODE_FORGE_TEST_RUN"] = filter
		}
		commands[name] = &copy
	}
	p.Commands = commands
	p.ReuseTest, p.Repeat = false, nil
	return p
}

// Full/package and land gates must not inherit a parent block's name filter.
func gatesNamesUnfiltered(p gates.Plan) gates.Plan {
	c := p.Commands["test"]
	if c == nil {
		return p
	}
	commands := make(map[string]*gates.Command, len(p.Commands))
	for name, command := range p.Commands {
		commands[name] = command
	}
	copy := *c
	copy.Env = map[string]string{}
	for k, v := range c.Env {
		copy.Env[k] = v
	}
	copy.Env["CODE_FORGE_TEST_RUN"] = ""
	commands["test"] = &copy
	p.Commands = commands
	return p
}

// Run cheap pre-gates once, then one named test command per selected package.
// Keep a single test result for callers, with each package's evidence attached.
func gatesNamesRun(ctx context.Context, p gates.Plan, s *gatesNamedSelection, cwd, run string, timeout float64, scope gates.Scope) ([]any, bool, error) {
	pre := gatesNamesPlan(p, s, nil)
	results, all, err := gates.Run(ctx, pre, cwd, run, timeout, scope)
	if err != nil {
		return results, all, err
	}
	test := ledger.Object{{Key: "gate", Value: "test"}, {Key: "ok", Value: all}, {Key: "skipped", Value: len(s.Packages) == 0 || !all}, {Key: "duration_ms", Value: int64(0)}, {Key: "stdout", Value: ""}, {Key: "stderr", Value: ""}}
	if !all {
		test.Set("reason", "pre_gate_failed")
		failed := []string{}
		for _, value := range results {
			r := value.(ledger.Object)
			if r.Get("ok") != true && r.Get("gate") != "test" {
				failed = append(failed, fmt.Sprint(r.Get("gate")))
			}
		}
		test.Set("pre_gates_failed", failed)
	}
	packages := []any{}
	var duration int64
	preOK := all
	failedPackage := false
	for _, pkg := range s.Packages {
		if !preOK || ctx.Err() != nil {
			break
		}
		plan := gatesNamesPlan(p, s, &pkg)
		plan.Commands = map[string]*gates.Command{"test": plan.Commands["test"]}
		plan.Custom = nil
		got, ok, e := gates.Run(ctx, plan, cwd, run, timeout, scope)
		if e != nil {
			return nil, false, e
		}
		all = all && ok
		for _, value := range got {
			r := value.(ledger.Object)
			if r.Get("gate") != "test" {
				continue
			}
			r.Set("package", pkg.Package)
			r.Set("test_names", pkg.Names)
			packages = append(packages, r)
			if n, ok := r.Get("duration_ms").(int64); ok {
				duration += n
			}
			for _, k := range []string{"stdout", "stderr"} {
				test.Set(k, fmt.Sprint(test.Get(k))+fmt.Sprint(r.Get(k)))
			}
			// Preserve the first failure's summary even as remaining packages run.
			if !failedPackage {
				test.Set("argv", r.Get("argv"))
				for _, key := range []string{"code", "reason", "timed_out", "cancelled", "flaky", "repeats", "message"} {
					if v := r.Get(key); v != nil {
						test.Set(key, v)
					}
				}
			}
			if r.Get("ok") != true {
				failedPackage = true
			}

		}
	}
	test.Set("duration_ms", duration)
	test.Set("ok", all)
	test.Set("package_results", packages)
	gatesAdd(&test, scope.Fields(true))
	gatesAdd(&test, s.fields())
	replaced := false
	for i, value := range results {
		if value.(ledger.Object).Get("gate") == "test" {
			results[i] = test
			replaced = true
		}
	}
	if !replaced {
		results = append(results, test)
	}
	return results, all, nil
}
