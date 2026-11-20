package gates

import (
	"reflect"
	"testing"
)

func TestB1008EchoedCommandsAndUnreadableLoad(t *testing.T) {
	names, pkgs := []string{"TestFlaky"}, []string{"example.invalid/m/a"}
	for _, tc := range []struct {
		line   string
		echoed bool
	}{
		{"go test -timeout 9m ./a", true},
		{"  cf-gotest: go test -timeout 9m ./a", true},
		{"+ /usr/local/go/bin/go test -timeout 9m ./a", true},
		{"\x1b[1mwrap: go test -timeout 9m ./a\x1b[0m", true},
		{"please run go test -timeout 9m ./a", false},
		{"expected output: go test -timeout 9m ./a", false},
		{"    a_test.go:42: go test -timeout 9m ./a", false},
		{"the command /usr/local/go/bin/go test -timeout 9m ./a failed", false},
	} {
		t.Run(tc.line, func(t *testing.T) {
			got := goRerunCommand([]string{"make", "test"}, tc.line+"\n", names, pkgs)
			var want []string
			if tc.echoed {
				want = []string{"go", "test", "-timeout", "9m", "-p", "2", "-count=3", "-run", "^(TestFlaky)$", "example.invalid/m/a"}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("rerun=%q, want %q", got, want)
			}
		})
	}
	t.Run("unreadable load falls back to probe", func(t *testing.T) {
		for _, value := range []string{"unreadable", "", "-1", "NaN", "+Inf", "1e9999"} {
			t.Setenv(FlakyLoadEnv, value)
			if load, ok := flakyLoadEnv(); ok {
				t.Errorf("override %q accepted as %v; want probe fallback", value, load)
			}
		}
		t.Setenv(FlakyLoadEnv, " 2.5 ")
		if load, ok := flakyLoadEnv(); !ok || load != 2.5 {
			t.Errorf("valid override=(%v,%v), want (2.5,true)", load, ok)
		}
	})
}
