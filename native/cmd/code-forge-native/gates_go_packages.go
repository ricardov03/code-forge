package main

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
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
	loaded, err := config.Load(cwd)
	if err != nil {
		return false
	}
	configured := blockMap(loaded["gates"])
	p, safe := gates.Resolve(cwd, configured)
	return safe && gpkMode(cwd, p, configured)
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

// gpkScope is the --changed scope of a Go project: the changed packages, the
// full suite for a go.mod change, or no_changes (test gate skipped).
// rootRelative is true for paths from a plain git diff (repository-root
// relative); the workspace prefix is then taken off.
func gpkScope(cwd string, changed []string, rootRelative bool) gates.Scope {
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
		return gates.Scope{Kind: "full", Report: "full", Reason: gpkReasonGoMod, Files: []string{}, Changed: []string{}, Dependents: []string{}}
	}
	reason := gpkReasonPackages
	if len(pkgs) == 0 {
		reason = gpkReasonNoChanges
	}
	return gates.Scope{Kind: "changed", Reason: reason, Files: pkgs, Changed: []string{}, Dependents: []string{}}
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
