package main

import (
	"reflect"
	"strings"
	"testing"
)

// B723b tests: a Go project's changed-package gate (B667) also tests every
// module package whose Deps, TestImports or XTestImports reach a changed
// package (B722: B696 changed canon, config imports canon, and a changed-only
// run never tested config). The fixture module is real; go list runs on it.

// b723bModule: b imports a; c imports a only in its _test.go; d imports a
// only in an external test package; f imports b (a through Deps); g's test
// imports b; h imports e; e imports nothing of the module.
var b723bModule = map[string]string{
	"go.mod":           "module example.invalid/m\n\ngo 1.22\n",
	"a/a.go":           "package a\n\nfunc A() int { return 1 }\n",
	"b/b.go":           "package b\n\nimport \"example.invalid/m/a\"\n\nvar B = a.A()\n",
	"c/c.go":           "package c\n",
	"c/c_test.go":      "package c\n\nimport (\n\t\"testing\"\n\n\t\"example.invalid/m/a\"\n)\n\nfunc TestC(t *testing.T) { _ = a.A() }\n",
	"d/d.go":           "package d\n",
	"d/d_ext_test.go":  "package d_test\n\nimport (\n\t\"testing\"\n\n\t\"example.invalid/m/a\"\n)\n\nfunc TestD(t *testing.T) { _ = a.A() }\n",
	"e/e.go":           "package e\n\nfunc E() int { return 2 }\n",
	"f/f.go":           "package f\n\nimport \"example.invalid/m/b\"\n\nvar F = b.B\n",
	"g/g.go":           "package g\n",
	"g/g_test.go":      "package g\n\nimport (\n\t\"testing\"\n\n\t\"example.invalid/m/b\"\n)\n\nfunc TestG(t *testing.T) { _ = b.B }\n",
	"h/h.go":           "package h\n\nimport \"example.invalid/m/e\"\n\nvar H = e.E()\n",
	"README.md":        "# m\n",
	"scripts/build.sh": "echo build\n",
}

func b723bTests(calls []string) []string {
	tests := []string{}
	for _, c := range calls {
		if strings.HasPrefix(c, "block|test") {
			tests = append(tests, c)
		}
	}
	return tests
}

func TestB723bGoImportersTested(t *testing.T) {
	cases := []struct {
		name               string
		changed            map[string]*string
		test               string
		packages, importer []any
	}{
		{"one changed package", map[string]*string{"a/a.go": b667Body("package a\n\nfunc A() int { return 3 }\n")},
			"block|test ./a ./b ./c ./d ./f ./g -count=1", []any{"./a"}, []any{"./b", "./c", "./d", "./f", "./g"}},
		// B725: two changed packages at once (a list with a newline).
		{"two changed packages", map[string]*string{"a/a.go": b667Body("package a\n\nfunc A() int { return 3 }\n"), "e/e.go": b667Body("package e\n\nfunc E() int { return 4 }\n")},
			"block|test ./a ./b ./c ./d ./e ./f ./g ./h -count=1", []any{"./a", "./e"}, []any{"./b", "./c", "./d", "./f", "./g", "./h"}},
		{"leaf package", map[string]*string{"h/h.go": b667Body("package h\n\nimport \"example.invalid/m/e\"\n\nvar H = e.E() + 1\n")},
			"block|test ./h -count=1", []any{"./h"}, []any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newB667GateFixtureOn(t, b723bModule, nil, tc.changed)
			code, out := f.gates(t)
			if code != 0 {
				t.Fatalf("exit=%d, want 0: %s", code, out)
			}
			calls := b667Calls(t, f.log)
			if tests := b723bTests(calls); !reflect.DeepEqual(tests, []string{tc.test}) {
				t.Errorf("go test calls=%q, want %q (all calls %q)", tests, tc.test, calls)
			}
			row := f.done(t)
			if row["reason"] != "go_packages" || !reflect.DeepEqual(row["packages"], tc.packages) || !reflect.DeepEqual(row["importer_packages"], tc.importer) {
				t.Errorf("gate.done reason=%v packages=%v importer_packages=%v, want go_packages %v %v: %v", row["reason"], row["packages"], row["importer_packages"], tc.packages, tc.importer, row)
			}
		})
	}
}

func TestB723bGoListFailureFailsGate(t *testing.T) {
	change := map[string]*string{"a/a.go": b667Body("package a\n\nfunc A() int { return 3 }\n")}
	cases := []struct {
		name, marker string
		base         map[string]string
		want         string
	}{
		{"go list exits non-zero", ".list-fail", b723bModule, "go: fake list failure"},
		{"go list lists nothing", ".list-empty", b723bModule, "listed no packages"},
		{"broken import in the module", "", func() map[string]string {
			m := map[string]string{}
			for k, v := range b723bModule {
				m[k] = v
			}
			m["e/e.go"] = "package e\n\nimport \"example.invalid/m/nope\"\n\nvar E = nope.X\n"
			return m
		}(), "example.invalid/m/nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newB667GateFixtureOn(t, tc.base, nil, change)
			if tc.marker != "" {
				b667WriteFile(t, f.log+tc.marker, "")
			}
			code, out := f.gates(t)
			if code == 0 {
				t.Fatalf("exit=0 after a go list failure, want a failed gate: %s", out)
			}
			if !strings.Contains(out, "importer packages") || !strings.Contains(out, tc.want) {
				t.Errorf("output=%q, want a clear importer-packages go list error with %q", out, tc.want)
			}
			if testing.Verbose() {
				t.Logf("gate output: %s", out)
			}
			if tests := b723bTests(b667Calls(t, f.log)); len(tests) != 0 {
				t.Errorf("go test ran %q after a go list failure, want none", tests)
			}
		})
	}
}
