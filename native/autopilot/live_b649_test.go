package autopilot

import (
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B649: LiveUntilDone names the grants Node cannot read (a null until):
// an until-done grant the record still marks live.
func TestB649LiveUntilDone(t *testing.T) {
	record := func(grant string) ledger.Object {
		body := `{"run_id":"r1","status":"active","blocks":{}}`
		if grant != "" {
			body = `{"run_id":"r1","status":"active","blocks":{},"autopilot":` + grant + `}`
		}
		r, err := ledger.DecodeObject([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for _, tc := range []struct {
		name, grant string
		live        bool
	}{
		{"no grant", "", false},
		{"until-done, no deadline", `{"grant_id":"g","status":"active","until":null,"until_done":true}`, true},
		{"until-done with a deadline", `{"grant_id":"g","status":"active","until":"2026-01-01T13:00:00.000Z","until_done":true}`, true},
		{"until-done stopped", `{"grant_id":"g","status":"stopped","until":null,"until_done":true}`, false},
		{"until-done expired (done)", `{"grant_id":"g","status":"expired","until":null,"until_done":true}`, false},
		{"timed grant", `{"grant_id":"g","status":"active","until":"2026-01-01T13:00:00.000Z"}`, false},
		{"null until without until_done", `{"grant_id":"g","status":"active","until":null}`, true},
		{"not an object", `[1]`, false},
		{"no until key, odd status", `{"grant_id":"g","status":"weird"}`, false},
		{"no until key, active", `{"grant_id":"g","status":"active"}`, false},
		{"until-done, odd status", `{"grant_id":"g","status":"weird","until":null,"until_done":true}`, false},
	} {
		got := LiveUntilDone(record(tc.grant)) != nil
		if got != tc.live {
			t.Errorf("%s: live %v, want %v", tc.name, got, tc.live)
		}
	}
}
