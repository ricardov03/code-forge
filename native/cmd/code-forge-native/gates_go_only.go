package main

import (
	"regexp"
	"sort"
	"strings"

	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B399: a block that changes no Node product file runs no full Node suite
// under gates run --changed. Its test scope is the block's own Node tests,
// test/compat/go.test.mjs (it runs `go test ./...`) and the compat tests named
// after what the block's Go files touch (gatesGoOnlyCompat). The full compat set
// takes about 20 minutes, so it never runs whole here.
const gatesGoTest = "test/compat/go.test.mjs"

var gatesRunnableTest = regexp.MustCompile(`\.(test|spec)\.[^/]+$`)

// gatesNodeProduct: src/**, bin/**, scripts/**/*.mjs except
// scripts/build-native.mjs, package.json and package-lock.json. A directory or
// glob entry under scripts/ may cover an .mjs file, so it counts too.
func gatesNodeProduct(p string) bool {
	switch {
	case p == "src" || strings.HasPrefix(p, "src/"), p == "bin" || strings.HasPrefix(p, "bin/"):
		return true
	case p == "package.json", p == "package-lock.json":
		return true
	case p == "scripts/build-native.mjs":
		return false
	case p == "scripts" || strings.HasPrefix(p, "scripts/"):
		return strings.HasSuffix(p, ".mjs") || strings.HasSuffix(p, "/") || p == "scripts" || strings.ContainsAny(p, "*?[")
	}
	return false
}

func gatesGoOnlyTest(p string) bool {
	return strings.HasPrefix(p, "test/") && gates.SafeFile(p) && !strings.ContainsAny(p, "*?[") &&
		gatesRunnableTest.MatchString(p) && !strings.HasSuffix(p, "_test.go")
}

// gatesGoOnlyPath is the allow list of a Go-only block. A test helper,
// fixture, directory or glob under test/ may reach any Node test, so it keeps
// today's scope.
func gatesGoOnlyPath(p string) bool {
	if gatesNodeProduct(p) {
		return false
	}
	switch {
	case p == "scripts/build-native.mjs", p == "CHANGELOG.md":
		return true
	case p == "native" || strings.HasPrefix(p, "native/"), p == "docs" || strings.HasPrefix(p, "docs/"),
		p == "plans" || strings.HasPrefix(p, "plans/"), p == ".github" || strings.HasPrefix(p, ".github/"):
		return true
	case strings.HasPrefix(p, "test/"):
		return gatesGoOnlyTest(p)
	}
	return strings.HasSuffix(p, ".md")
}

// gatesGoOnly reports a block whose every owned entry is on the Go-only allow
// list, gated by a test command that takes {test_files}. Changed paths are a
// subset of the owned entries (gates.BaseDelta keeps owned paths only).
func gatesGoOnly(block ledger.Object, p gates.Plan) bool {
	c := p.Commands["test"]
	if c == nil || !gates.Has(c.Argv, "{test_files}") || (p.Restricted && !gates.Has(p.Names, "test")) {
		return false
	}
	owned := gates.Strings(block.Get("owned_files"))
	if len(owned) == 0 {
		return false
	}
	for _, f := range owned {
		if !gatesGoOnlyPath(f) {
			return false
		}
	}
	return true
}

// gatesGoOnlyKeys names what one owned Go path touches, longest first:
// native/cmd/code-forge-native/<a>_<b>_<c>.go gives a-b-c, a-b and a (a _test
// suffix is dropped); native/<package>/... gives the package name.
func gatesGoOnlyKeys(p string) []string {
	const cmd = "native/cmd/code-forge-native/"
	if strings.HasPrefix(p, cmd) && strings.HasSuffix(p, ".go") && !strings.Contains(p[len(cmd):], "/") {
		parts := strings.Split(strings.TrimSuffix(strings.TrimSuffix(p[len(cmd):], ".go"), "_test"), "_")
		keys := []string{}
		for n := len(parts); n > 0; n-- {
			keys = append(keys, strings.Join(parts[:n], "-"))
		}
		return keys
	}
	parts := strings.Split(p, "/")
	if len(parts) > 2 && parts[0] == "native" && parts[1] != "cmd" && parts[1] != "vendor" && parts[1] != "bin" {
		return []string{parts[1]}
	}
	return nil
}

// gatesGoOnlyCompat: for each owned path, the compat tests test/compat/<name>.test.mjs
// (a leading parity- dropped from name) whose name is the longest key or starts
// with key + "-". Example: status_direct.go runs parity-status-direct; status.go
// runs parity-status and parity-status-direct; native/gates/ runs parity-gates-*.
// A path with no match adds nothing.
func gatesGoOnlyCompat(owned, listed []string) []string {
	names := map[string]string{}
	for _, f := range listed {
		if strings.HasPrefix(f, "test/compat/") && strings.HasSuffix(f, ".test.mjs") && !strings.Contains(f[len("test/compat/"):], "/") && f != gatesGoTest {
			names[f] = strings.TrimPrefix(strings.TrimSuffix(f[len("test/compat/"):], ".test.mjs"), "parity-")
		}
	}
	picked := map[string]bool{}
	for _, p := range owned {
		for _, key := range gatesGoOnlyKeys(p) {
			hit := false
			for f, name := range names {
				if name == key || strings.HasPrefix(name, key+"-") {
					picked[f], hit = true, true
				}
			}
			if hit {
				break
			}
		}
	}
	out := []string{}
	for f := range picked {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}
