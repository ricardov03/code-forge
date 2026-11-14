package worker

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// B630 fix1: the order of the recovery steps in one run. The built-in
// recovery runs first, then the B629 review.on_unavailable chain, and only a
// chain that is used up leads to the B630 substitute review.

// b630Events is the event name of every ledger row, in ledger order.
func b630Events(t *testing.T, e *b629Env) []string {
	t.Helper()
	out := []string{}
	for _, l := range strings.Split(readFile(t, filepath.Join(e.fx.home, ".code-forge", "ledger", "unit.jsonl")), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatal(err)
		}
		ev, _ := m["event"].(string)
		out = append(out, ev)
	}
	return out
}

func b630IndexOf(list []string, ev string) int {
	for i, s := range list {
		if s == ev {
			return i
		}
	}
	return -1
}

// TestB630ChainUsedUpThenSubstitute: m-a times out (and its same-model
// retry), the chain's m-c times out too; the signed review.unavailable_fallback
// row for the chain step is written before the autopilot.decision row, the
// decision names both failed sessions' provider, and the substitute runs last.
func TestB630ChainUsedUpThenSubstitute(t *testing.T) {
	e := b629Setup(t, b630Chain)
	b630Grant(t, e, "review:substitute")
	calls, res := e.next(t, "m-a", "m-c")
	if strings.Join(calls, ",") != "m-a,m-a,m-c,m-x" {
		t.Fatalf("calls %v, want built-in recovery, then the chain, then the substitute", calls)
	}
	if !strings.Contains(res, `"source":"substitute"`) {
		t.Fatalf("result %s, want a substitute review", res)
	}
	fb := e.rows(t, "review.unavailable_fallback")
	if len(fb) == 0 || fb[0]["to_model"] != "m-c" {
		t.Fatalf("unavailable_fallback rows %v, want the chain step to m-c", fb)
	}
	d := b630Decisions(t, e)
	if len(d) != 1 || d[0]["decision"] != "substitute" {
		t.Fatalf("decision rows %v", d)
	}
	if got, _ := json.Marshal(d[0]["failed_providers"]); string(got) != `["anthropic"]` {
		t.Fatalf("failed_providers %s, want the chain's provider too", got)
	}
	events := b630Events(t, e)
	chain, decision := b630IndexOf(events, "review.unavailable_fallback"), b630IndexOf(events, "autopilot.decision")
	if chain < 0 || decision < 0 || chain > decision {
		t.Fatalf("ledger order %v: the chain row must come before the substitute decision", events)
	}
}

// TestB630ChainAnswersNoSubstitute: under the same grant, the chain's m-c
// answers, so the chain ends the walk with a valid review and no substitute
// runs: no autopilot.decision row, no source substitute, no third provider.
func TestB630ChainAnswersNoSubstitute(t *testing.T) {
	e := b629Setup(t, b630Chain)
	b630Grant(t, e, "review:substitute")
	calls, res := e.next(t, "m-a")
	if strings.Join(calls, ",") != "m-a,m-a,m-c" {
		t.Fatalf("calls %v, want the chain's m-c to end the walk", calls)
	}
	if strings.Contains(res, `"source":"substitute"`) || !strings.Contains(res, `"approved":true`) {
		t.Fatalf("result %s, want a normal approval from the chain", res)
	}
	if d := b630Decisions(t, e); len(d) != 0 {
		t.Fatalf("decision rows %v, want none when the chain answers", d)
	}
	if fb := e.rows(t, "review.unavailable_fallback"); len(fb) == 0 {
		t.Fatalf("no unavailable_fallback row for the chain step")
	}
	for _, r := range e.rows(t, "review.approved") {
		if r["source"] != nil {
			t.Fatalf("approved row %v, want no source", r)
		}
	}
}
