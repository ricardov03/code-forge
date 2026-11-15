package worker

import (
	"path/filepath"
	"testing"
)

// TestB689BudgetUSDNoWarnSeamReviews: the dry pass no longer hands a
// budget.usd ticket to Node when Warn is nil (B689: worker --run always sets
// it, so that hand-over was dead). Under budget the rows match Node's B538
// golden, the same as with the seam wired.
func TestB689BudgetUSDNoWarnSeamReviews(t *testing.T) {
	clock := fixedNow
	fx, w, stderr := b497Fixture(t, &clock)
	w.Cfg = b492Cfg(t, `{"levels":{"L2":{"model":"claude-x","provider":"anthropic"}},"budget":{"usd":100}}`)
	w.Warn = nil
	ids := []string{
		fx.ticket(t, "r1", "B1", "a.txt", sha("hello\nworld\n")),
		fx.ticket(t, "r1", "B2", "b.txt", sha("one\nWARNME\n")),
	}
	for _, id := range ids {
		done, poison, err := w.RunTicket(id)
		if err != nil || done != 1 || poison {
			t.Fatalf("RunTicket(%s) = %d, %v, %v", id, done, poison, err)
		}
	}
	b497SameLines(t, b497Normalize(b497Files(t, fx, ids)), b497Normalize(readFile(t, filepath.Join("testdata", "b538_node_budget.golden"))))
	line := "level=L2 provider=anthropic model=claude-x effort=none fallback_step=0\n"
	if stderr.String() != line+line {
		t.Errorf("stderr = %q", stderr.String())
	}
}
