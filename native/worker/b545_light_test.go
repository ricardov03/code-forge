package worker

import (
	"os"
	"path/filepath"
	"testing"
)

// B545: testdata/b545_node_light*.golden are what Node's createWorker wrote
// on the B497 repo with one light block L owning a.txt and b.txt (scratch
// node_light.mjs + run.sh), with testdata/b545_fake_light.sh as `claude`
// (it echoes every hunk header, duplicates included, as the light lens
// asks). Before B545 Go handed every light ticket to Node.

func b545LightRun(t *testing.T, cfg, golden string, edit func(root string)) {
	t.Helper()
	clock := fixedNow
	fx, w, stderr := b497Fixture(t, &clock)
	fx.blocksRecord(t, `,"blocks":{"L":{"base_sha":"e64a56881e75eab80bd5dcda472ab5182c0b9604","owned_files":["a.txt","b.txt"],"kind":"light"}}`)
	if edit != nil {
		edit(fx.root)
	}
	w.Cfg = b492Cfg(t, cfg)
	bin, err := filepath.Abs(filepath.Join("testdata", "b545_fake_light.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(bin); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("%s is not executable: %v", bin, err)
	}
	w.SessionDeps.Bins = map[string]string{"claude": bin}
	ids := []string{
		fx.ticket(t, "r1", "L", "a.txt", sha(readFile(t, filepath.Join(fx.root, "a.txt")))),
		fx.ticket(t, "r1", "L", "b.txt", sha(readFile(t, filepath.Join(fx.root, "b.txt")))),
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
	if stderr.String() != line {
		t.Errorf("stderr = %q, Node prints %q once (one batch session)", stderr.String(), line)
	}
}

const b545Cfg = `{"levels":{"L2":{"model":"claude-x","provider":"anthropic"}}}`

// TestB545LightBatchMatchesNode: the first ticket runs the block's batch
// round (light_batch, one session, both files' round/approved/result rows,
// both signed results, light_result); the second ticket takes its cached
// outcome. Byte-equal to Node.
func TestB545LightBatchMatchesNode(t *testing.T) {
	b545LightRun(t, b545Cfg, "b545_node_light_warn.golden", nil)
}

func b545Write(name, text string) func(string) {
	return func(root string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			panic(err)
		}
	}
}

// TestB545LightBatchApprovedMatchesNode: a batch that passes approves both
// files (review.approved for each), byte-equal to Node.
func TestB545LightBatchApprovedMatchesNode(t *testing.T) {
	b545LightRun(t, b545Cfg, "b545_node_light_clean.golden", b545Write("b.txt", "one\ntwo\n"))
}

// TestB545LightBatchFindingFileMatchesNode: a finding on a file outside the
// batch ends both tickets unavailable/finding-file, no round rows, the
// second ticket from the cached outcome, byte-equal to Node.
func TestB545LightBatchFindingFileMatchesNode(t *testing.T) {
	b545LightRun(t, b545Cfg, "b545_node_light_other.golden", b545Write("b.txt", "one\nOTHERFILE\n"))
}

// TestB545LightCapMatchesNode: three a.txt contents in turn (scratch
// node_cap.mjs, b.txt back at its base content). The first two tickets each
// run a batch; the third finds two light_batch rows and enters the
// ordinary per-file loop (budget, plan, session, state). Byte-equal to Node.
func TestB545LightCapMatchesNode(t *testing.T) {
	clock := fixedNow
	fx, w, stderr := b497Fixture(t, &clock)
	fx.blocksRecord(t, `,"blocks":{"L":{"base_sha":"e64a56881e75eab80bd5dcda472ab5182c0b9604","owned_files":["a.txt","b.txt"],"kind":"light"}}`)
	b545Write("b.txt", "one\n")(fx.root) // only a.txt changed: the ordinary round has no peers
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
	want := b497Normalize(readFile(t, filepath.Join("testdata", "b545_node_light_cap.golden")))
	b497SameLines(t, got, want)
	line := "level=L2 provider=anthropic model=claude-x effort=none fallback_step=0\n"
	if stderr.String() != line+line+line {
		t.Errorf("stderr = %q, Node prints %q three times", stderr.String(), line)
	}
}
