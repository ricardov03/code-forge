package blockopen

import (
	"errors"
	"testing"

	"github.com/ricardov03/code-forge/native/state"
)

// B631: a human_check clause may name what the human waits for. The value
// must be a non-blank string and only human_check clauses may carry it.
func TestB631AcceptanceWaitsFor(t *testing.T) {
	const waitsMessage = "acceptance: waits_for must be a non-empty string on a human_check clause (what the human must check)"
	cases := []struct {
		name, yaml, refusal string
	}{
		{"human clause with waits_for", "- clause: c1\n  tests: [t1]\n- clause: looks right\n  kind: human_check\n  waits_for: device test on iPhone\n", ""},
		{"old file without waits_for", "- clause: c1\n  tests: [t1]\n- clause: looks right\n  kind: human_check\n", ""},
		{"empty waits_for", "- clause: looks right\n  kind: human_check\n  waits_for: \"\"\n", waitsMessage},
		{"blank waits_for", "- clause: looks right\n  kind: human_check\n  waits_for: \"   \"\n", waitsMessage},
		{"number waits_for", "- clause: looks right\n  kind: human_check\n  waits_for: 3\n", waitsMessage},
		{"null waits_for", "- clause: looks right\n  kind: human_check\n  waits_for:\n", waitsMessage},
		{"list waits_for", "- clause: looks right\n  kind: human_check\n  waits_for: [a]\n", waitsMessage},
		{"waits_for on a test clause", "- clause: c1\n  tests: [t1]\n  waits_for: device test\n", "acceptance: waits_for is allowed only on a human_check clause"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			value, err := ParseAcceptance([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			clauses, err := Acceptance(value)
			if tc.refusal == "" {
				if err != nil || len(clauses) == 0 {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var se *state.Error
			if !errors.As(err, &se) || se.Code != "bad-acceptance" || se.Message != tc.refusal {
				t.Fatalf("got %v; want bad-acceptance %q", err, tc.refusal)
			}
		})
	}
	// The general shape refusal still wins over waits_for.
	value, _ := ParseAcceptance([]byte("- kind: human_check\n  waits_for: \"\"\n"))
	if _, err := Acceptance(value); err == nil || err.Error() == "" || err.(*state.Error).Message != "acceptance: a non-empty list of {clause, tests: [test ids…]} or {clause, kind: human_check} without tests is required" {
		t.Fatalf("shape refusal: %v", err)
	}
}
