package blockopen

import "testing"

func TestB1011Approval(t *testing.T) {
	for _, c := range []struct {
		yaml  string
		valid bool
	}{
		{"- clause: screen\n  kind: human_check\n  approval: device\n", true},
		{"- clause: screen\n  kind: human_check\n  approval: design\n", true},
		{"- clause: screen\n  kind: human_check\n", true},
		{"- clause: screen\n  kind: human_check\n  approval: other\n", false},
		{"- clause: screen\n  kind: human_check\n  approval: null\n", false},
		{"- clause: screen\n  tests: [T1]\n  approval: device\n", false},
	} {
		v, err := ParseAcceptance([]byte(c.yaml))
		if err != nil {
			t.Fatal(err)
		}
		_, err = Acceptance(v)
		if (err == nil) != c.valid {
			t.Errorf("%s: %v", c.yaml, err)
		}
	}
}
