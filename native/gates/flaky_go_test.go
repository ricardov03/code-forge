package gates

import (
	"reflect"
	"testing"
)

// B798: which go test failures a rerun by name may clear.
func TestB798GoFailedTests(t *testing.T) {
	const pkgA = "--- FAIL: TestA (0.30s)\n    a_test.go:9: boom\n    --- FAIL: TestA/sub (0.10s)\nFAIL\nFAIL\texample.invalid/m/a\t0.31s\n"
	cases := []struct {
		name, output string
		names, pkgs  []string
	}{
		{"one test", pkgA, []string{"TestA"}, []string{"example.invalid/m/a"}},
		{"two packages", "ok  \texample.invalid/m/c\t0.1s\n" + pkgA + "--- FAIL: TestB (0.01s)\n--- FAIL: TestC (0.01s)\nFAIL\nFAIL\texample.invalid/m/b\t0.02s\nFAIL\n", []string{"TestA", "TestB", "TestC"}, []string{"example.invalid/m/a", "example.invalid/m/b"}},
		{"a wrapper's lines around it", "cf-gotest: go test -count=1 ./a\n" + pkgA, []string{"TestA"}, []string{"example.invalid/m/a"}},
		{"verbose output", "=== RUN   TestA\n--- FAIL: TestA (0.00s)\nFAIL\nFAIL\texample.invalid/m/a\t0.01s\n", []string{"TestA"}, []string{"example.invalid/m/a"}},
		{"more than the cap", "--- FAIL: TestA (0s)\n--- FAIL: TestB (0s)\n--- FAIL: TestC (0s)\n--- FAIL: TestD (0s)\nFAIL\texample.invalid/m/a\t0.1s\n", nil, nil},
		{"a panic", "--- FAIL: TestA (0.00s)\npanic: boom [recovered]\nFAIL\texample.invalid/m/a\t0.01s\n", nil, nil},
		{"a test timeout", "panic: test timed out after 10m0s\nFAIL\texample.invalid/m/a\t600s\n", nil, nil},
		{"a build failure", "# example.invalid/m/a\na.go:1: syntax error\nFAIL\texample.invalid/m/a [build failed]\n", nil, nil},
		{"a failed package without a named test", pkgA + "FAIL\texample.invalid/m/b\t0.01s\n", nil, nil},
		{"named tests with no package line", "--- FAIL: TestA (0.00s)\n", nil, nil},
		{"json output", `{"Action":"fail","Package":"example.invalid/m/a","Test":"TestA"}` + "\n", nil, nil},
		{"node test output", "not ok 1 - test/a.test.mjs\n", nil, nil},
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

// B798: the rerun keeps go's -C and the go test build flags, and drops the
// rest; a gate whose program is not go is never rerun by name.
func TestB798GoRerunArgv(t *testing.T) {
	names, pkgs := []string{"TestA", "TestB"}, []string{"example.invalid/m/a"}
	cases := []struct {
		name     string
		original []string
		want     []string
	}{
		{"go test", []string{"go", "test", "./...", "-count=1"}, []string{"go", "test", "-count=3", "-run", "^(TestA|TestB)$", "example.invalid/m/a"}},
		{"build flags kept", []string{"/usr/local/go/bin/go", "test", "-race", "-tags", "x y", "-timeout=5m", "-run", "Old", "-v", "-json", "./a", "-args", "-short"}, []string{"/usr/local/go/bin/go", "test", "-race", "-tags", "x y", "-timeout=5m", "-count=3", "-run", "^(TestA|TestB)$", "example.invalid/m/a"}},
		{"go -C run wrapper", []string{"go", "-C", "native", "run", "./cmd/cf-gotest", "a.go"}, []string{"go", "-C", "native", "test", "-count=3", "-run", "^(TestA|TestB)$", "example.invalid/m/a"}},
		{"not go", []string{"make", "test"}, nil},
		{"go alone", []string{"go"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := goRerunArgv(c.original, names, pkgs); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("goRerunArgv = %q, want %q", got, c.want)
			}
		})
	}
}
