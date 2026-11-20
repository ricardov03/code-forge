package main

import (
	"strings"
	"testing"
)

func TestB1023FlagReferenceSelection(t *testing.T) {
	const source = "cmd/code-forge-native/flags.go"
	const refs = "cmd/code-forge-native/help_reference_test.go"
	original := "package main\nconst usage = `usage: tool [--old]`\n"
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"add flag", strings.Replace(original, "--old", "--old --new", 1), true},
		{"remove flag", strings.Replace(original, "--old", "", 1), true},
		{"add existing flag to another command", original + "const other = `usage: other [--old]`\n", true},
		{"ordinary change", original + "var ordinary = 1\n", false},
		{"comment only", original + "// document --example\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := map[string]string{
				"go.mod": b1009Base["go.mod"], source: original,
				refs: "package main\nimport \"testing\"\nfunc TestB801EveryCommandHasAReference(t *testing.T) {}\nfunc TestB801CommandsJSONCoversTheTable(t *testing.T) {}\nfunc TestB801DocExamplesRun(t *testing.T) {}\nfunc TestUnrelated(t *testing.T) {}\n",
			}
			f := newB667GateFixtureOn(t, base, nil, map[string]*string{source: b667Body(tc.body)})
			code, out := b1009Gate(t, f)
			if code != 0 {
				t.Fatalf("gate exit %d: %s", code, out)
			}
			calls := strings.Join(b667Calls(t, f.log), "\n")
			for _, name := range []string{"TestB801EveryCommandHasAReference", "TestB801CommandsJSONCoversTheTable"} {
				if strings.Contains(calls, name) != tc.want {
					t.Errorf("%s selected=%v want=%v:\n%s", name, strings.Contains(calls, name), tc.want, calls)
				}
			}
			if strings.Contains(calls, "TestB801DocExamplesRun") || strings.Contains(calls, "TestUnrelated") {
				t.Fatalf("reference selection widened: %s", calls)
			}
		})
	}
}

func TestB1023LandedFlagMeaning(t *testing.T) {
	b613Env(t)
	meaning := helpRefFlagMeaning("block close", "--landed")
	if !strings.Contains(meaning, "original signed evidence") || !strings.Contains(meaning, "landed_sha") || !strings.Contains(meaning, "human checks") {
		t.Fatalf("block close --landed has no complete meaning: %q", meaning)
	}
}
