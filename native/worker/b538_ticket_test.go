package worker

import (
	"os"
	"path/filepath"
	"testing"
)

// B538: testdata/b538_node_ladder*.golden are what Node's createWorker wrote
// on the B497 fixture with a config that has a levels.L2 fallback ladder and
// a review.second_levels entry (scratch node_e2e.mjs + mkgolden.sh), with the
// B497 fake reviewer (b538_node_ladder.golden) and with
// testdata/b538_fake_ladder.sh, which answers claude-x off the schema
// (b538_node_ladder_miss.golden). Before B538 Go refused such a ticket.
const b538LadderCfg = `{"levels":{"L2":{"model":"claude-x","provider":"anthropic","fallback":[{"model":"claude-y","provider":"anthropic"}]}},"review":{"second_levels":{"L2":{"model":"claude-z","provider":"anthropic"}}}}`

func b538LadderRun(t *testing.T, fake, golden string) {
	t.Helper()
	clock := fixedNow
	fx, w, stderr := b497Fixture(t, &clock)
	w.Cfg = b492Cfg(t, b538LadderCfg)
	if fake != "" {
		bin, err := filepath.Abs(filepath.Join("testdata", fake))
		if err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(bin); err != nil || info.Mode().Perm()&0o100 == 0 {
			t.Fatalf("%s is not executable: %v", bin, err)
		}
		w.SessionDeps.Bins = map[string]string{"claude": bin}
	}
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
	got := b497Normalize(b497Files(t, fx, ids))
	want := b497Normalize(readFile(t, filepath.Join("testdata", golden)))
	b497SameLines(t, got, want)
	line := "level=L2 provider=anthropic model=claude-x effort=none fallback_step=0\n"
	if stderr.String() != line+line {
		t.Errorf("stderr = %q, Node prints %q twice", stderr.String(), line)
	}
}

// TestB538LadderTicketMatchesNode: the first session passes; Go reviews both
// tickets end to end, byte-equal to Node.
func TestB538LadderTicketMatchesNode(t *testing.T) {
	b538LadderRun(t, "", "b538_node_ladder.golden")
}

// TestB538LadderTicketSchemaMissMatchesNode: the session answers off the
// schema (the session layer's invalid-output); Go ends both tickets
// unavailable/schema with Node's session, schema_invalid, unavailable, state
// and result rows.
func TestB538LadderTicketSchemaMissMatchesNode(t *testing.T) {
	b538LadderRun(t, "b538_fake_ladder.sh", "b538_node_ladder_miss.golden")
}
