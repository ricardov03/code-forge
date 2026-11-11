package main

import (
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B404: gates run --changed, batch land and the land merge gates never run the
// full Node suite. Their test scope comes from what changed, read in Go:
//   - the block's --tests entries, its owned and changed tests and the
//     stem-paired tests of changed sources, in any language;
//   - every test that imports a changed file, directly or through other files
//     (gates.Graph: static imports, literal import()/require(), new URL());
//   - for a change under native/: test/compat/go.test.mjs (go build, vet and
//     test) and the compat tests named after the Go files (B399); for a native
//     build input also every non-compat test that builds or runs the native
//     binary, found by its text or a test helper it imports (B330);
//   - for a fixture, schema, compat-golden or src/config change: the config
//     golden test (B409).
// An empty selection skips the test gate; it never widens to the full suite.
// The full suite runs locally only with an explicit gates run --full.

// gatesSelectable reports a test command that takes {test_files} and runs
// under the plan's kind.
func gatesSelectable(p gates.Plan) bool {
	c := p.Commands["test"]
	return c != nil && gates.Has(c.Argv, "{test_files}") && (!p.Restricted || gates.Has(p.Names, "test"))
}

// gatesAllGoOnly reports paths that hold no Node product file (the B399 allow
// list). An empty list is not Go-only.
func gatesAllGoOnly(paths []string) bool {
	if len(paths) == 0 {
		return false
	}
	for _, p := range paths {
		if !gatesGoOnlyPath(p) {
			return false
		}
	}
	return true
}

type gatesSelection struct {
	tests, owned, changed []string
}

// gatesSelectFiles is the B404 test list, in a fixed order and without
// duplicates: the block's --tests entries, owned and changed test files, the
// native set, import-graph dependents, then the config golden test.
func gatesSelectFiles(cwd string, listed []string, s gatesSelection) []string {
	inList := map[string]bool{}
	for _, f := range listed {
		inList[f] = true
	}
	files := []string{}
	seen := map[string]bool{}
	add := func(f string) {
		if !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	exact := func(f string) bool { return gates.SafeFile(f) && !strings.ContainsAny(f, "*?[") }
	for _, f := range s.tests {
		if exact(f) {
			add(f)
		}
	}
	for _, f := range s.owned {
		if exact(f) && gates.IsTestName(f) {
			add(f)
		}
	}
	for _, f := range s.changed {
		if exact(f) && (gates.IsTestName(f) || gates.IsChangedTest(f)) && inList[f] {
			add(f)
		}
	}
	// Stem-paired tests of changed sources, in any language (FooTests.swift
	// for Foo.swift, test_foo.py for foo.py); the runner filter later keeps
	// the ones the test command can take.
	for _, f := range s.changed {
		if gates.IsChangedTest(f) {
			continue
		}
		for _, candidate := range listed {
			if gates.Pair(f, candidate) {
				add(candidate)
			}
		}
	}
	paths := append(append([]string{}, s.owned...), s.changed...)
	native, build, golden := false, false, false
	for _, p := range paths {
		native = native || p == "native" || strings.HasPrefix(p, "native/") || p == "scripts/build-native.mjs"
		build = build || gates.NativeInput(p)
		golden = golden || gates.GoldenInput(p)
	}
	graph := gates.NewGraph(cwd, listed)
	// go.test.mjs (go build, vet and test) runs for a native change only, and
	// only when the repository has it; a block that changes only tests runs
	// just those tests.
	if native {
		if inList[gatesGoTest] {
			add(gatesGoTest)
		}
		for _, f := range gatesGoOnlyCompat(paths, listed) {
			add(f)
		}
	}
	if build {
		for _, f := range graph.NativeTests() {
			if !strings.HasPrefix(f, "test/compat/") {
				add(f)
			}
		}
	}
	for _, f := range graph.Dependents(s.changed) {
		add(f)
	}
	if golden && inList[gates.GoldenTest] {
		add(gates.GoldenTest)
	}
	return files
}

// gatesChangedSelect is the --changed scope of one block: reason go_only for
// a block with no Node product file, else import_graph.
func gatesChangedSelect(cwd string, block ledger.Object, listed, changed []string, goOnly bool) gates.Scope {
	files := gatesSelectFiles(cwd, listed, gatesSelection{tests: gates.Strings(block.Get("test_files")), owned: gates.Strings(block.Get("owned_files")), changed: changed})
	reason := "import_graph"
	if goOnly {
		reason = "go_only"
	}
	return gates.Scope{Kind: "changed", Report: "related", Reason: reason, Files: files, Changed: []string{}, Dependents: []string{}}
}

// gatesRunnerFiles keeps the files the test command can take: gates.RunnerFiles
// filters by a known runner; a Go test file only goes to a go runner (in a
// Node repository it runs inside test/compat/go.test.mjs).
func gatesRunnerFiles(argv, files []string) []string {
	goRunner := len(argv) > 0 && filepath.Base(argv[0]) == "go"
	out := []string{}
	for _, f := range gates.RunnerFiles(argv, files) {
		if goRunner || !strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
	}
	return out
}

// gatesSelectedReason reports a B404 scope (its test_files go on gate.done).
func gatesSelectedReason(reason string) bool {
	return reason == "go_only" || reason == "import_graph"
}

// gatesOwnedListed is the changed set when a block has no merge base: its
// owned entries that name a listed file.
func gatesOwnedListed(block ledger.Object, listed []string) []string {
	inList := map[string]bool{}
	for _, f := range listed {
		inList[f] = true
	}
	out := []string{}
	for _, f := range gates.Strings(block.Get("owned_files")) {
		if inList[f] {
			out = append(out, f)
		}
	}
	return out
}
