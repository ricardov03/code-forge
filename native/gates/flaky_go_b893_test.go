package gates

import (
	"reflect"
	"strings"
	"testing"
)

// b893JSON is go test -json output: each line one event.
func b893JSON(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

// B893: a go test -json gate gets the same rerun by name as plain output.
func TestB893GoFailedTestsJSON(t *testing.T) {
	const (
		pkgA = "example.invalid/m/a"
		pkgB = "example.invalid/m/b"
	)
	failA := []string{
		`{"Action":"start","Package":"example.invalid/m/a"}`,
		`{"Action":"run","Package":"example.invalid/m/a","Test":"TestA"}`,
		`{"Action":"output","Package":"example.invalid/m/a","Test":"TestA","Output":"=== RUN   TestA\n"}`,
		`{"Action":"run","Package":"example.invalid/m/a","Test":"TestA/sub"}`,
		`{"Action":"fail","Package":"example.invalid/m/a","Test":"TestA/sub","Elapsed":0.1}`,
		`{"Action":"output","Package":"example.invalid/m/a","Test":"TestA","Output":"--- FAIL: TestA (0.30s)\n"}`,
		`{"Action":"fail","Package":"example.invalid/m/a","Test":"TestA","Elapsed":0.3}`,
		`{"Action":"pass","Package":"example.invalid/m/a","Test":"TestOK","Elapsed":0}`,
		`{"Action":"output","Package":"example.invalid/m/a","Output":"FAIL\texample.invalid/m/a\t0.31s\n"}`,
		`{"Action":"fail","Package":"example.invalid/m/a","Elapsed":0.31}`,
	}
	passC := `{"Action":"pass","Package":"example.invalid/m/c","Elapsed":0.1}`
	cases := []struct {
		name, output string
		names, pkgs  []string
	}{
		{"one test", b893JSON(failA...), []string{"TestA"}, []string{pkgA}},
		{"two packages and a passed one", b893JSON(append(append([]string{passC}, failA...),
			`{"Action":"fail","Package":"example.invalid/m/b","Test":"TestB"}`,
			`{"Action":"fail","Package":"example.invalid/m/b","Test":"TestC"}`,
			`{"Action":"fail","Package":"example.invalid/m/b"}`)...), []string{"TestA", "TestB", "TestC"}, []string{pkgA, pkgB}},
		{"a wrapper's lines and stderr around it", "cf-gotest: go test -json ./a\n" + b893JSON(failA...) + "FAIL\n", []string{"TestA"}, []string{pkgA}},
		{"more than the cap", b893JSON(
			`{"Action":"fail","Package":"example.invalid/m/a","Test":"TestA"}`,
			`{"Action":"fail","Package":"example.invalid/m/a","Test":"TestB"}`,
			`{"Action":"fail","Package":"example.invalid/m/a","Test":"TestC"}`,
			`{"Action":"fail","Package":"example.invalid/m/a","Test":"TestD"}`,
			`{"Action":"fail","Package":"example.invalid/m/a"}`), nil, nil},
		{"a panic", b893JSON(
			`{"Action":"output","Package":"example.invalid/m/a","Test":"TestA","Output":"panic: boom [recovered]\n"}`,
			`{"Action":"fail","Package":"example.invalid/m/a","Test":"TestA"}`,
			`{"Action":"fail","Package":"example.invalid/m/a"}`), nil, nil},
		{"a test timeout", b893JSON(
			`{"Action":"output","Package":"example.invalid/m/a","Output":"panic: test timed out after 10m0s\n"}`,
			`{"Action":"fail","Package":"example.invalid/m/a","Elapsed":600}`), nil, nil},
		{"a build failure (go 1.24 events)", b893JSON(
			`{"ImportPath":"example.invalid/m/a","Action":"build-output","Output":"a.go:1: syntax error\n"}`,
			`{"ImportPath":"example.invalid/m/a","Action":"build-fail"}`,
			`{"Action":"fail","Package":"example.invalid/m/a","Elapsed":0,"FailedBuild":"example.invalid/m/a"}`), nil, nil},
		{"a build failure (older go)", b893JSON(
			`{"Action":"output","Package":"example.invalid/m/a","Output":"FAIL\texample.invalid/m/a [build failed]\n"}`,
			`{"Action":"fail","Package":"example.invalid/m/a"}`), nil, nil},
		{"a failed package without a named test", b893JSON(append(failA, `{"Action":"fail","Package":"example.invalid/m/b"}`)...), nil, nil},
		{"a named test in a package that passed", b893JSON(
			`{"Action":"fail","Package":"example.invalid/m/a","Test":"TestA"}`,
			`{"Action":"pass","Package":"example.invalid/m/a"}`), nil, nil},
		{"a named test with no package end", b893JSON(`{"Action":"fail","Package":"example.invalid/m/a","Test":"TestA"}`), nil, nil},
		{"all passed", b893JSON(passC), nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			names, pkgs := goFailedTests(c.output, GoFlakyMax)
			if !reflect.DeepEqual(names, c.names) || !reflect.DeepEqual(pkgs, c.pkgs) {
				t.Fatalf("goFailedTests = %q %q, want %q %q", names, pkgs, c.names, c.pkgs)
			}
		})
	}
}
