package worker

import (
	"path/filepath"
	"testing"
)

// TestB537LightCapWithPeersMatchesNode: B545's cap run with b.txt changed
// too, so the third ticket's ordinary per-file round has a peer file (the
// related-files section of the packet). testdata/b537r_node_light_cap_peers.golden
// is what Node's createWorker wrote (scratch node_cap.mjs, b.txt "one\ntwo\n").
// Before B537 Go refused this round (a light round after the cap with peer
// files). Byte-equal to Node, packet sizes included.
func TestB537LightCapWithPeersMatchesNode(t *testing.T) {
	clock := fixedNow
	fx, w, stderr := b497Fixture(t, &clock)
	fx.blocksRecord(t, `,"blocks":{"L":{"base_sha":"e64a56881e75eab80bd5dcda472ab5182c0b9604","owned_files":["a.txt","b.txt"],"kind":"light"}}`)
	b545Write("b.txt", "one\ntwo\n")(fx.root)
	w.Cfg = b492Cfg(t, b545Cfg)
	bin, err := filepath.Abs(filepath.Join("testdata", "b545_fake_light.sh"))
	if err != nil {
		t.Fatal(err)
	}
	w.SessionDeps.Bins = map[string]string{"claude": bin}
	ids := []string{}
	for _, text := range []string{"hello\nworld\n", "hello\nworld2\n", "hello\nworld3\n"} {
		b545Write("a.txt", text)(fx.root)
		id := fx.ticket(t, "r1", "L", "a.txt", sha(text))
		ids = append(ids, id)
		done, poison, err := w.RunTicket(id)
		if err != nil || done != 1 || poison {
			t.Fatalf("RunTicket(%s) = %d, %v, %v", id, done, poison, err)
		}
	}
	got := b497Normalize(b497Files(t, fx, ids))
	want := b497Normalize(readFile(t, filepath.Join("testdata", "b537r_node_light_cap_peers.golden")))
	b497SameLines(t, got, want)
	line := "level=L2 provider=anthropic model=claude-x effort=none fallback_step=0\n"
	if stderr.String() != line+line+line {
		t.Errorf("stderr = %q, Node prints %q three times", stderr.String(), line)
	}
}
