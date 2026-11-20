package gates

import (
	"reflect"
	"testing"
)

// B894: a go run wrapper's rerun keeps the go test build flags the wrapper
// names in its output; a direct go test keeps its own argv's flags only.
func TestB894GoRerunCommand(t *testing.T) {
	names, pkgs := []string{"TestA"}, []string{"example.invalid/m/a"}
	wrapper := []string{"go", "-C", "native", "run", "./cmd/cf-gotest", "native/a/a.go"}
	cases := []struct {
		name     string
		original []string
		output   string
		want     []string
	}{
		{"wrapper names its go test", wrapper, "cf-gotest: go test -count=1 -timeout 75m -p 3 -json ./a\n--- FAIL: TestA (0s)\nFAIL\texample.invalid/m/a\t0.1s\n",
			[]string{"go", "-C", "native", "test", "-timeout", "75m", "-p", "3", "-count=3", "-run", "^(TestA)$", "example.invalid/m/a"}},
		{"wrapper with a go path, coloured", wrapper, "\x1b[1m+ /usr/local/go/bin/go test -race -tags=x -timeout=20m ./...\x1b[0m\n",
			[]string{"go", "-C", "native", "test", "-race", "-tags=x", "-timeout=20m", "-count=3", "-run", "^(TestA)$", "example.invalid/m/a"}},
		{"only the first named go test", wrapper, "wrap: go test -timeout 75m ./a\n    a_test.go:3: go test -timeout 1s\n",
			[]string{"go", "-C", "native", "test", "-timeout", "75m", "-count=3", "-run", "^(TestA)$", "example.invalid/m/a"}},
		{"wrapper names nothing", wrapper, "--- FAIL: TestA (0s)\n",
			[]string{"go", "-C", "native", "test", "-count=3", "-run", "^(TestA)$", "example.invalid/m/a"}},
		{"direct go test ignores the output", []string{"go", "test", "-timeout=5m", "./..."}, "go test -timeout 75m ./a\n",
			[]string{"go", "test", "-timeout=5m", "-count=3", "-run", "^(TestA)$", "example.invalid/m/a"}},
		{"not go", []string{"make", "test"}, "go test -timeout 75m ./a\n", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := goRerunCommand(c.original, c.output, names, pkgs); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("goRerunCommand = %q, want %q", got, c.want)
			}
		})
	}
}
