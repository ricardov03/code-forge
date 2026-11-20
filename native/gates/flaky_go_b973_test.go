package gates

import (
	"reflect"
	"testing"
)

// B973: the flaky rerun follows cf-gotest's rules: -p 2 unless the gate set
// -p, -trimpath for a go run wrapper (never twice), the gate's own choice
// for a direct go test.
func TestB973GoRerunCfGotestRules(t *testing.T) {
	names, pkgs := []string{"TestA"}, []string{"example.invalid/m/a"}
	wrapper := []string{"go", "-C", "native", "run", "./cmd/cf-gotest", "native/a/a.go"}
	tail := []string{"-count=3", "-run", "^(TestA)$", "example.invalid/m/a"}
	cases := []struct {
		name     string
		original []string
		output   string
		want     []string
	}{
		{"direct go test", []string{"go", "test", "./..."}, "", append([]string{"go", "test", "-p", "2"}, tail...)},
		{"direct go test with -p", []string{"go", "test", "-p=4", "./..."}, "", append([]string{"go", "test", "-p=4"}, tail...)},
		{"direct go test with -trimpath", []string{"go", "test", "-trimpath", "./..."}, "", append([]string{"go", "test", "-trimpath", "-p", "2"}, tail...)},
		{"wrapper", wrapper, "", append([]string{"go", "-C", "native", "test", "-trimpath", "-p", "2"}, tail...)},
		{"wrapper that names -trimpath and -p", wrapper, "cf-gotest: go test -trimpath -p 1 ./a\n", append([]string{"go", "-C", "native", "test", "-trimpath", "-p", "1"}, tail...)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := goRerunCommand(c.original, c.output, names, pkgs); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("goRerunCommand = %q, want %q", got, c.want)
			}
		})
	}
}

// B973: over the core count with no GOMAXPROCS set, the rerun gets
// GOMAXPROCS=cores/2 (at least 1); the gate's env is not changed.
func TestB973GoRerunEnvLoadCap(t *testing.T) {
	before := FlakyCores
	FlakyCores = func() int { return 8 }
	t.Cleanup(func() { FlakyCores = before })
	t.Setenv("GOMAXPROCS", "")
	gate := map[string]string{"A": "1"}
	if got := goRerunEnv(gate, 8); !reflect.DeepEqual(got, gate) {
		t.Fatalf("load 8 on 8 cores: %v", got)
	}
	if got := goRerunEnv(gate, 9.5); !reflect.DeepEqual(got, map[string]string{"A": "1", "GOMAXPROCS": "4"}) {
		t.Fatalf("load 9.5 on 8 cores: %v", got)
	}
	if len(gate) != 1 {
		t.Fatalf("the gate env changed: %v", gate)
	}
	if got := goRerunEnv(map[string]string{"GOMAXPROCS": "6"}, 20); got["GOMAXPROCS"] != "6" {
		t.Fatalf("a gate's GOMAXPROCS was replaced: %v", got)
	}
	t.Setenv("GOMAXPROCS", "3")
	if got := goRerunEnv(nil, 20); got["GOMAXPROCS"] != "" {
		t.Fatalf("this process's GOMAXPROCS was overridden: %v", got)
	}
	FlakyCores = func() int { return 1 }
	t.Setenv("GOMAXPROCS", "")
	if got := goRerunEnv(nil, 5); got["GOMAXPROCS"] != "1" {
		t.Fatalf("one core: %v", got)
	}
}
