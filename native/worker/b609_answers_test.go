package worker

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/session"
	"github.com/ricardov03/code-forge/native/ticket"
)

// B609: testdata/b609_node_answers.golden is what Node's createWorker
// (src/worker/loop.mjs processTicket, engine.mjs reviewTicket, answers.mjs
// reviewAnswers) wrote for this fixture: block B2 owns b.txt, c.txt (new) and
// d.txt (tracked); a review of b.txt leaves F1 open, then answers tickets
// (enqueueAnswers) run: d.txt (its diff; a repeated verdict id is no valid
// verdict), c.txt (a new file; F1 resolved, the review.answered row), a
// not-open id, the same file, a file B2 does not own, an answering file
// changed, an answering file gone and a source changed after the enqueue,
// and five tickets readTicket refuses. testdata/b609_fake_answers.sh is the
// fake `claude`; it appends each answers packet to answers.log next to
// itself (testdata/b609_node_answers_packets.golden is Node's log).

const b609Base = "a1b8e4d8025c4f89891c7f7b0d18a45768d4f5eb"

// b609Enqueued is the enqueueAnswers time of the Node fixture.
var b609Enqueued = time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)

func b609Repo(t *testing.T, root string) string {
	t.Helper()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_AUTHOR_DATE=2026-11-11T09:00:00Z", "GIT_COMMITTER_DATE=2026-11-11T09:00:00Z")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for name, text := range map[string]string{"a.txt": "hello\n", "b.txt": "one\n", "d.txt": "x\n"} {
		b609Write(t, root, name, text)
	}
	git("init", "-q", "-b", "main")
	git("add", "a.txt", "b.txt", "d.txt")
	git("commit", "-q", "-m", "base")
	for name, text := range map[string]string{"a.txt": "hello\nworld\n", "b.txt": "one\nWARNME\n", "d.txt": "x\ny\n", "c.txt": "fixed\n"} {
		b609Write(t, root, name, text)
	}
	return git("rev-parse", "HEAD")
}

func b609Write(t *testing.T, root, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// b609Fixture: the repo, the run record (B1 a.txt; B2 b.txt, c.txt, d.txt),
// the fake CLI (its directory holds answers.log) and a runner wired to them.
func b609Fixture(t *testing.T, clock *time.Time) (*b488Fix, *TicketRunner, *bytes.Buffer, string) {
	t.Helper()
	b497Schema(t)
	fx := newB488Fix(t)
	if base := b609Repo(t, fx.root); base != b609Base {
		t.Fatalf("base sha %s is not the Node fixture's", base)
	}
	fx.blocksRecord(t, `,"blocks":{"B1":{"base_sha":"`+b609Base+`","owned_files":["a.txt"]},"B2":{"base_sha":"`+b609Base+`","owned_files":["b.txt","c.txt","d.txt"]}}`)
	binDir := t.TempDir()
	bin := filepath.Join(binDir, "claude")
	if err := os.WriteFile(bin, []byte(readFile(t, filepath.Join("testdata", "b609_fake_answers.sh"))), 0o755); err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	w := fx.runner(clock)
	w.Cfg = b492Cfg(t, `{"levels":{"L2":{"model":"claude-x","provider":"anthropic"}}}`)
	w.RunRootDir = filepath.Join(fx.home, "rr")
	procRoot := t.TempDir()
	w.SessionDeps = session.Deps{Environ: []string{"PATH=/usr/bin:/bin"}, Bins: map[string]string{"claude": bin}, Stderr: stderr,
		PidsRoot: func() (string, error) { return procRoot, nil }}
	return fx, w, stderr, binDir
}

// b609Answers enqueues an answers ticket of B2's b.txt like enqueueAnswers.
func b609Answers(t *testing.T, fx *b488Fix, byFile string, answers ...string) string {
	t.Helper()
	p := ticket.Parts{Run: "r1", Block: "B2", File: "b.txt", ContentHash: sha(readFile(t, filepath.Join(fx.root, "b.txt")))}
	byHash := sha(readFile(t, filepath.Join(fx.root, byFile)))
	ids := ansUnique(answers)
	id := queue.AnswersID(p, byFile, byHash, ids)
	if err := queue.PublishAnswers(QueueDir(fx.root), p, id, byFile, byHash, ids, b609Enqueued); err != nil {
		t.Fatal(err)
	}
	return id
}

func b609Run(t *testing.T, w *TicketRunner, id string) {
	t.Helper()
	if done, poison, err := w.RunTicket(id); err != nil || done != 1 || poison {
		t.Fatalf("RunTicket(%s) = %d, %v, %v", id, done, poison, err)
	}
}

// TestB609AnswersMatchNodeBytes: Go serves every answers ticket of the Node
// fixture end to end (no hand-over): the route rows, the answers session and
// its session row, the signed review.answered row, the refusals, the result
// files, done markers and queue timings, and the packets the reviewer read,
// byte-equal to Node, normalized.
func TestB609AnswersMatchNodeBytes(t *testing.T) {
	clock := fixedNow
	fx, w, stderr, binDir := b609Fixture(t, &clock)
	first := fx.ticket(t, "r1", "B2", "b.txt", sha("one\nWARNME\n"))
	ids := []string{first}
	b609Run(t, w, first)
	for _, c := range [][]string{{"d.txt", "F1"}, {"c.txt", "F1"}, {"d.txt", "F2", "F1"}, {"b.txt", "F1"}, {"a.txt", "F1"}} {
		id := b609Answers(t, fx, c[0], c[1:]...)
		ids = append(ids, id)
		b609Run(t, w, id)
	}
	later := []struct {
		by      string
		answers []string
		change  func()
	}{
		{"d.txt", []string{"F3", "F1"}, func() { b609Write(t, fx.root, "d.txt", "x\ny\nz\n") }},
		{"c.txt", []string{"F9"}, func() {
			if err := os.Remove(filepath.Join(fx.root, "c.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{"d.txt", []string{"F8"}, func() { b609Write(t, fx.root, "b.txt", "one\nfixed\n") }},
	}
	for _, c := range later {
		id := b609Answers(t, fx, c.by, c.answers...)
		c.change()
		ids = append(ids, id)
		b609Run(t, w, id)
	}
	hash := sha("one\nfixed\n")
	for _, c := range []struct{ id, extra string }{
		{"e", `"kind":"answers","by_file":"c.txt","by_hash":"x","answers":["F1"],`},
		{"f", `"kind":"other",`},
		{"a", `"kind":"answers","by_file":"../c.txt","by_hash":"x","answers":["F1"],`},
		{"b", `"kind":"answers","by_file":"c.txt","by_hash":"x","answers":["F1","F1"],`},
		{"d", `"kind":"answers","by_file":"c.txt","by_hash":"x","answers":[],`},
	} {
		id := strings.Repeat(c.id, 24)
		writeJSON(t, filepath.Join(QueueDir(fx.root), id+".json"), `{"ticket":"`+id+`","run":"r1","block":"B2","file":"b.txt","content_hash":"`+hash+`",`+c.extra+`"enqueued_at":"2026-11-11T09:00:00.000Z"}`+"\n")
		ids = append(ids, id)
		b609Run(t, w, id)
	}
	got := b497Normalize(b497Files(t, fx, ids))
	want := b497Normalize(readFile(t, filepath.Join("testdata", "b609_node_answers.golden")))
	b497SameLines(t, got, want)
	b497SameLines(t, readFile(t, filepath.Join(binDir, "answers.log")), readFile(t, filepath.Join("testdata", "b609_node_answers_packets.golden")))
	line := "level=L2 provider=anthropic model=claude-x effort=none fallback_step=0\n"
	if stderr.String() != strings.Repeat(line, 3) {
		t.Errorf("stderr = %q, Node prints %q three times", stderr.String(), line)
	}
}

// An answers ticket whose dry pass finds a part Go does not reproduce (here:
// an answering file that is not UTF-8) stays queued with nothing written, no
// route row and no session.
func TestB609AnswersUnsupportedWritesNothing(t *testing.T) {
	clock := fixedNow
	fx, w, stderr, binDir := b609Fixture(t, &clock)
	first := fx.ticket(t, "r1", "B2", "b.txt", sha("one\nWARNME\n"))
	b609Run(t, w, first)
	b609Write(t, fx.root, "c.txt", "fixed\n\xff\n") // an answering file that is not UTF-8
	id := b609Answers(t, fx, "c.txt", "F1")
	before := fx.snapshotFiles(t)
	stderr.Reset()
	w.WriteRow = func(r ledger.Object) error { t.Fatalf("a row was written: %v", r); return nil }
	if _, _, err := w.RunTicket(id); !IsUnsupported(err) {
		t.Fatalf("err = %v, want unsupported", err)
	}
	assertSameFiles(t, before, fx.snapshotFiles(t))
	if stderr.Len() != 0 || exists(filepath.Join(binDir, "answers.log")) {
		t.Fatalf("a session ran: %q", stderr.String())
	}
	if exists(filepath.Join(fx.home, "rr", "packets", id)) {
		t.Fatal("the packet directory was left")
	}
}

// ansCutUTF16 is String.slice(0, n) on UTF-16 units for text without a
// split surrogate pair.
func TestB609CutUTF16(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{{"abc", 2, "ab"}, {"abc", 5, "abc"}, {"aé😀b", 4, "aé😀"}, {"aé😀b", 3, "aé�"}} {
		if got := ansCutUTF16(c.in, c.n); got != c.want {
			t.Errorf("ansCutUTF16(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

// The verdict schema check is matchesSchema(ANSWERS_SCHEMA).
func TestB609ValidAnswer(t *testing.T) {
	for text, want := range map[string]bool{
		`{"verdicts":[]}`: true,
		`{"verdicts":[{"id":"F1","verdict":"resolved","reason":"r"}]}`:              true,
		`{"verdicts":[{"reason":"r","verdict":"not_resolved","id":"F1"}]}`:          true,
		`{"verdicts":[{"id":"F1","verdict":"maybe","reason":"r"}]}`:                 false,
		`{"verdicts":[{"id":"F1","verdict":"resolved"}]}`:                           false,
		`{"verdicts":[{"id":"F1","verdict":"resolved","reason":"r","extra":true}]}`: false,
		`{"verdicts":[7]}`:         false,
		`{"verdicts":{}}`:          false,
		`{"verdicts":[],"more":1}`: false,
		`{}`:                       false,
		`{"verdicts":[{"id":1,"verdict":"resolved","reason":"r"}]}`: false,
	} {
		v, err := ledger.DecodeObject([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		if got := ansValid(v); got != want {
			t.Errorf("ansValid(%s) = %v, want %v", text, got, want)
		}
	}
}
