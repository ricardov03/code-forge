package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B667 (owner rule 2026-11-14: "We ALWAYS want speed"): in a Go project the
// per-block gate (gates run --run --block --changed, used by block land and
// batch land) runs `go test` only on the changed Go packages. go vet and gofmt
// keep their whole-module argv. A batch land runs ONE full `go test ./...` at
// the end, before any push (block_land_batch.go). gates.go_changed_packages:
// false turns the per-block narrowing off.

// gpkReasonPackages and gpkReasonNoChanges are the gate.done reasons of the
// Go package scope; gpkReasonGoMod keeps the full suite for a go.mod change.
const (
	gpkReasonPackages  = "go_packages"
	gpkReasonNoChanges = "no_changes"
	gpkReasonGoMod     = "go_mod"
)

// gpkPattern is the argv item the package list replaces.
const gpkPattern = "./..."

// gpkMode reports a plan whose test gate is a whole-module `go test ./...` in
// a Go module root, with gates.go_changed_packages not false.
func gpkMode(cwd string, p gates.Plan, configured map[string]any) bool {
	if v, ok := configured["go_changed_packages"]; ok && v == false {
		return false
	}
	c := p.Commands["test"]
	if c == nil || (p.Restricted && !gates.Has(p.Names, "test")) || len(c.Argv) < 3 {
		return false
	}
	if filepath.Base(c.Argv[0]) != "go" || c.Argv[1] != "test" || !gates.Has(c.Argv, gpkPattern) {
		return false
	}
	for _, a := range c.Argv {
		if strings.Contains(a, "{test_files}") || strings.Contains(a, "{changed_files}") || strings.Contains(a, "{test_classes}") {
			return false
		}
	}
	info, err := os.Stat(filepath.Join(cwd, "go.mod"))
	return err == nil && info.Mode().IsRegular()
}

// gpkProject is gpkMode for a directory's own config (the land merge gates).
func gpkProject(cwd string) bool {
	_, ok := gpkProjectGo(cwd)
	return ok
}

// gpkProjectGo is gpkProject with the test command's go binary.
func gpkProjectGo(cwd string) (string, bool) {
	loaded, err := config.Load(cwd)
	if err != nil {
		return "", false
	}
	configured := blockMap(loaded["gates"])
	p, safe := gates.Resolve(cwd, configured)
	if !safe || !gpkMode(cwd, p, configured) {
		return "", false
	}
	return gpkGo(p), true
}

// gpkGo is the go binary of a gpkMode plan's test command.
func gpkGo(p gates.Plan) string {
	if c := p.Commands["test"]; c != nil && len(c.Argv) > 0 {
		return c.Argv[0]
	}
	return "go"
}

// gpkPackages maps changed paths (relative to cwd) to the Go packages to test:
// the directory of a changed .go file, or the package that holds a changed
// testdata file. A directory that no longer holds a .go file (deleted) is
// dropped, and so are directories `go test ./...` skips (., _ and vendor
// segments, nested modules). goMod is true for a change to the root go.mod,
// go.sum or go.work: every package can break, so the full suite runs.
func gpkPackages(cwd string, changed []string) (pkgs []string, goMod bool) {
	seen := map[string]bool{}
	pkgs = []string{}
	for _, f := range changed {
		f = path.Clean(filepath.ToSlash(f))
		if f == "go.mod" || f == "go.sum" || f == "go.work" || f == "go.work.sum" {
			goMod = true
			continue
		}
		if f == "." || strings.HasPrefix(f, "../") || path.IsAbs(f) {
			continue
		}
		parts := strings.Split(f, "/")
		dir := ""
		testdata := -1
		for i, part := range parts[:len(parts)-1] {
			if part == "testdata" {
				testdata = i
				break
			}
		}
		switch {
		case testdata >= 0:
			dir = strings.Join(parts[:testdata], "/")
		case strings.HasSuffix(f, ".go"):
			dir = path.Dir(f)
		default:
			continue
		}
		if dir == "" {
			dir = "."
		}
		if seen[dir] || !gpkTested(cwd, dir) {
			continue
		}
		seen[dir] = true
		if dir == "." {
			pkgs = append(pkgs, ".")
		} else {
			pkgs = append(pkgs, "./"+dir)
		}
	}
	sort.Strings(pkgs)
	return pkgs, goMod
}

// gpkTested reports a directory `go test ./...` reaches that holds a .go file.
func gpkTested(cwd, dir string) bool {
	if dir != "." {
		for _, part := range strings.Split(dir, "/") {
			if part == "vendor" || part == "testdata" || strings.HasPrefix(part, ".") || strings.HasPrefix(part, "_") {
				return false
			}
		}
		for d := dir; d != "." && d != "/"; d = path.Dir(d) {
			if _, err := os.Stat(filepath.Join(cwd, filepath.FromSlash(d), "go.mod")); err == nil {
				return false
			}
		}
	}
	entries, err := os.ReadDir(filepath.Join(cwd, filepath.FromSlash(dir)))
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}

// gpkScope is the --changed scope of a Go project: the changed packages and
// their importers (B723b), the full suite for a go.mod change, or no_changes
// (test gate skipped). Scope.Changed holds the changed packages,
// Scope.Dependents the importer packages and Scope.Files both (the go test
// argv). rootRelative is true for paths from a plain git diff
// (repository-root relative); the workspace prefix is then taken off. goBin
// is the test command's go; a go list error fails the scope (never an empty
// or whole-module run).
func gpkScope(cwd, goBin string, changed []string, rootRelative bool) (gates.Scope, error) {
	prefix, err := "", error(nil)
	if rootRelative {
		prefix, err = gitx.Run(cwd, "rev-parse", "--show-prefix")
	}
	if p := strings.TrimSpace(prefix); err == nil && p != "" {
		local := []string{}
		for _, f := range changed {
			if strings.HasPrefix(f, p) {
				local = append(local, strings.TrimPrefix(f, p))
			} else {
				local = append(local, f)
			}
		}
		changed = local
	}
	pkgs, goMod := gpkPackages(cwd, changed)
	if goMod {
		return gates.Scope{Kind: "full", Report: "full", Reason: gpkReasonGoMod, Files: []string{}, Changed: []string{}, Dependents: []string{}}, nil
	}
	if len(pkgs) == 0 {
		return gates.Scope{Kind: "changed", Reason: gpkReasonNoChanges, Files: []string{}, Changed: []string{}, Dependents: []string{}}, nil
	}
	importers, err := gpkImporters(cwd, goBin, pkgs)
	if err != nil {
		return gates.Scope{}, err
	}
	files := append(append([]string{}, pkgs...), importers...)
	sort.Strings(files)
	return gates.Scope{Kind: "changed", Reason: gpkReasonPackages, Files: files, Changed: pkgs, Dependents: importers}, nil
}

// gpkListed is one package of `go list -json ./...`.
type gpkListed struct {
	ImportPath   string
	Dir          string
	Module       *struct{ Dir string }
	Deps         []string
	TestImports  []string
	XTestImports []string
}

// gpkImporters is every package `go test ./...` reaches (other than the
// changed ones) whose build or tests can see a changed package (B722/B723:
// B696 changed canon, config imports canon, and a changed-only run never
// tested config). A package is affected when it is changed or its Deps
// (transitive) hold a changed package; it is an importer when it is affected
// or its TestImports or XTestImports hold an affected package. pkgs are
// cwd-relative package dirs (./a); the result is sorted. A go list error or an
// empty listing is an error.
func gpkImporters(cwd, goBin string, pkgs []string) ([]string, error) {
	cmd := exec.Command(goBin, "list", "-json=ImportPath,Dir,Module,Deps,TestImports,XTestImports", "./...")
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("go changed packages: cannot list the importer packages of %s (go list ./... failed): %s", strings.Join(pkgs, " "), msg)
	}
	listed := []gpkListed{}
	dec := json.NewDecoder(&stdout)
	for {
		var l gpkListed
		if err := dec.Decode(&l); err == io.EOF {
			break
		} else if err != nil {
			return nil, fmt.Errorf("go changed packages: cannot list the importer packages of %s (go list ./... output): %v", strings.Join(pkgs, " "), err)
		}
		listed = append(listed, l)
	}
	if len(listed) == 0 {
		return nil, fmt.Errorf("go changed packages: cannot list the importer packages of %s: go list ./... listed no packages", strings.Join(pkgs, " "))
	}
	want := map[string]bool{}
	for _, p := range pkgs {
		want[p] = true
	}
	rel := map[string]string{}
	changed := map[string]bool{}
	for _, l := range listed {
		root := cwd
		if l.Module != nil && l.Module.Dir != "" {
			root = l.Module.Dir
		}
		r, err := filepath.Rel(root, l.Dir)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			continue
		}
		dir := "."
		if r = filepath.ToSlash(r); r != "." {
			dir = "./" + r
		}
		rel[l.ImportPath] = dir
		if want[dir] {
			changed[l.ImportPath] = true
		}
	}
	hit := func(list []string, set map[string]bool) bool {
		for _, p := range list {
			if set[p] {
				return true
			}
		}
		return false
	}
	affected := map[string]bool{}
	for p := range changed {
		affected[p] = true
	}
	for _, l := range listed {
		if hit(l.Deps, changed) {
			affected[l.ImportPath] = true
		}
	}
	out := []string{}
	for _, l := range listed {
		dir, ok := rel[l.ImportPath]
		if !ok || want[dir] {
			continue
		}
		if affected[l.ImportPath] || hit(l.TestImports, affected) || hit(l.XTestImports, affected) {
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out, nil
}

// gpkFields sets a Go package scope's gate.done / result fields: the changed
// packages and, separately, their importer packages (B723b).
func gpkFields(row *ledger.Object, s gates.Scope) {
	changed, importers := s.Changed, s.Dependents
	if changed == nil {
		changed = []string{}
	}
	if importers == nil {
		importers = []string{}
	}
	row.Set("packages", changed)
	row.Set("importer_packages", importers)
}

// gpkScoped reports a Go package scope from gpkScope that changes the test
// gate (packages or no_changes).
func gpkScoped(s gates.Scope) bool {
	return s.Kind == "changed" && (s.Reason == gpkReasonPackages || s.Reason == gpkReasonNoChanges)
}

// gpkPlan returns the plan whose test command takes the package list in place
// of every ./... item; no_changes drops the test gate. The plan passed in is
// not changed.
func gpkPlan(p gates.Plan, s gates.Scope) gates.Plan {
	commands := make(map[string]*gates.Command, len(p.Commands))
	for name, command := range p.Commands {
		commands[name] = command
	}
	p.Commands = commands
	c := commands["test"]
	if c == nil {
		return p
	}
	if len(s.Files) == 0 {
		delete(commands, "test")
		return p
	}
	scoped := *c
	scoped.Argv = []string{}
	for _, a := range c.Argv {
		if a == gpkPattern {
			scoped.Argv = append(scoped.Argv, s.Files...)
		} else {
			scoped.Argv = append(scoped.Argv, a)
		}
	}
	commands["test"] = &scoped
	return p
}

// gpkChanged is the changed paths between two commits, relative to cwd (the
// single-block land merge gate).
func gpkChanged(cwd, from, to string) []string {
	out, err := gitx.Run(cwd, "diff", "--relative", "--name-only", "-z", from, to, "--")
	if err != nil {
		return nil
	}
	files := []string{}
	for _, f := range strings.Split(out, "\x00") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	return files
}
