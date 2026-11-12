package worker

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
	"github.com/ricardov03/code-forge/native/session"
)

// B497: testdata/b497_node_review.golden is what Node's createWorker
// (src/worker/loop.mjs processTicket, real engine and fix loop, default
// ledger) wrote for the same fixture: a git repo whose block B1 changes a.txt
// (the fake reviewer passes it) and block B2 changes b.txt (one warning
// finding), with b497FakeClaude as the `claude` CLI, a temp HOME and a fake
// key store. Normalized like B488, plus the state rows' state_sha (it hashes
// a body that holds a time).

const b497FakeClaude = `#!/bin/sh
in=$(cat)
heads=$(printf '%s\n' "$in" | grep -E '^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@' | sed -E 's/^(@@ -[0-9,]+ \+[0-9,]+ @@).*/\1/' | awk '!s[$0]++' | sed 's/.*/"&"/' | paste -sd, -)
case "$in" in
*WARNME*) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":false,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[{"id":"F1","file":"b.txt","line_start":2,"line_end":2,"severity":"warning","category":"style","claim":"a warning","evidence":"the line says WARNME","fix":"remove it"}]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
*) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
esac
`

var b497StateSHA = regexp.MustCompile(`"state_sha":"[0-9a-f]{64}"`)

func b497Normalize(s string) string {
	return b497StateSHA.ReplaceAllString(b488Normalize(s), `"state_sha":"S"`)
}

// b497Repo makes the e2e repo (fixed git identity and dates: Node's base sha).
func b497Repo(t *testing.T, root string) string {
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
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("a.txt", "hello\n")
	write("b.txt", "one\n")
	git("add", "a.txt", "b.txt")
	git("commit", "-q", "-m", "base")
	write("a.txt", "hello\nworld\n")
	write("b.txt", "one\nWARNME\n")
	return git("rev-parse", "HEAD")
}

func b497Schema(t *testing.T) {
	t.Helper()
	schema, err := filepath.Abs(filepath.Join("..", "..", "src", "review", "finding.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	lenses, err := filepath.Abs(filepath.Join("..", "..", "src", "review", "lenses"))
	if err != nil {
		t.Fatal(err)
	}
	oldSchema, oldLenses := session.FindingSchemaPath, review.LensDir
	session.FindingSchemaPath = func() (string, error) { return schema, nil }
	review.LensDir = func() (string, error) { return lenses, nil }
	t.Cleanup(func() { session.FindingSchemaPath, review.LensDir = oldSchema, oldLenses })
}

// b497Fixture: the e2e repo, the run record with blocks B1 (a.txt) and B2
// (b.txt), the fake CLI and a runner wired to them.
func b497Fixture(t *testing.T, clock *time.Time) (*b488Fix, *TicketRunner, *bytes.Buffer) {
	t.Helper()
	b497Schema(t)
	fx := newB488Fix(t)
	base := b497Repo(t, fx.root)
	if base != "e64a56881e75eab80bd5dcda472ab5182c0b9604" {
		t.Fatalf("base sha %s is not the Node fixture's", base)
	}
	fx.blocksRecord(t, `,"blocks":{"B1":{"base_sha":"`+base+`","owned_files":["a.txt"]},"B2":{"base_sha":"`+base+`","owned_files":["b.txt"]}}`)
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte(b497FakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	stderr := &bytes.Buffer{}
	w := fx.runner(clock)
	w.Cfg = b492Cfg(t, `{"levels":{"L2":{"model":"claude-x","provider":"anthropic"}}}`)
	w.RunRootDir = filepath.Join(fx.home, "rr")
	procRoot := t.TempDir() // Node registers the child in the process's own lazy root, not the run root
	w.SessionDeps = session.Deps{Environ: []string{"PATH=/usr/bin:/bin"}, Bins: map[string]string{"claude": bin}, Stderr: stderr,
		PidsRoot: func() (string, error) { return procRoot, nil }}
	return fx, w, stderr
}

// b497Files is the golden's text for the fixture (scratch mkgolden.sh's order).
func b497Files(t *testing.T, fx *b488Fix, ids []string) string {
	t.Helper()
	var out strings.Builder
	h := filepath.Join(fx.home, ".code-forge")
	out.WriteString("== ledger\n" + readFile(t, filepath.Join(h, "ledger", "unit.jsonl")))
	out.WriteString(b497TicketFiles(t, fx, ids))
	stateRoot := filepath.Join(h, "runs", "r1")
	var states []string
	if err := filepath.WalkDir(filepath.Join(stateRoot, "review-state"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(stateRoot, p)
			states = append(states, rel)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(states)
	for _, rel := range states {
		out.WriteString("== " + rel + "\n" + readFile(t, filepath.Join(stateRoot, rel)) + "\n")
	}
	out.WriteString("== run root\n")
	var tree []string
	rr := filepath.Join(fx.home, "rr")
	if err := filepath.WalkDir(rr, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			rel, _ := filepath.Rel(rr, p)
			if rel == "." {
				tree = append(tree, ".")
			} else {
				tree = append(tree, "./"+rel)
			}
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	sort.Strings(tree)
	out.WriteString(strings.Join(tree, "\n") + "\n")
	return out.String()
}

func b497TicketFiles(t *testing.T, fx *b488Fix, ids []string) string {
	t.Helper()
	var out strings.Builder
	for _, id := range ids {
		out.WriteString("== result " + id + "\n" + readFile(t, filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json")) + "\n")
		out.WriteString("== done " + id + "\n" + readFile(t, filepath.Join(QueueDir(fx.root), id+".done")) + "\n")
		out.WriteString("== queue " + id + "\n" + readFile(t, filepath.Join(QueueDir(fx.root), id+".json")))
	}
	return out.String()
}

func b497SameLines(t *testing.T, got, want string) {
	t.Helper()
	if got == want {
		return
	}
	gl, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(gl) || i < len(wl); i++ {
		var g, n string
		if i < len(gl) {
			g = gl[i]
		}
		if i < len(wl) {
			n = wl[i]
		}
		if g != n {
			t.Fatalf("line %d differs\ngo   %s\nnode %s", i+1, g, n)
		}
	}
}

// TestB497ReviewMatchesNodeBytes: Go reviews both tickets end to end (route,
// budget, plan, session, triage, round, approved/state and result rows, the
// result file, done marker, queue timings and review-state files) byte-equal
// to Node, normalized.
func TestB497ReviewMatchesNodeBytes(t *testing.T) {
	clock := fixedNow
	fx, w, stderr := b497Fixture(t, &clock)
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
	want := b497Normalize(readFile(t, filepath.Join("testdata", "b497_node_review.golden")))
	b497SameLines(t, got, want)
	line := "level=L2 provider=anthropic model=claude-x effort=none fallback_step=0\n"
	if stderr.String() != line+line {
		t.Errorf("stderr = %q, Node prints %q twice", stderr.String(), line)
	}
}

// A ticket the dry pass cannot finish like Node (here: the block's file list
// cannot be read because the root is not a git repo) stays queued with
// nothing written, and the fake CLI never runs.
func TestB497DryPassUnsupportedWritesNothing(t *testing.T) {
	clock := fixedNow
	fx, w, stderr := b497Fixture(t, &clock)
	if err := os.Rename(filepath.Join(fx.root, ".git"), filepath.Join(fx.root, "git.off")); err != nil {
		t.Fatal(err)
	}
	id := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\nworld\n"))
	w.WriteRow = func(r ledger.Object) error { t.Fatalf("a row was written: %v", r); return nil }
	_, _, err := w.RunTicket(id)
	if !IsUnsupported(err) {
		t.Fatalf("err = %v, want unsupported", err)
	}
	for _, p := range []string{
		filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json"),
		filepath.Join(QueueDir(fx.root), id+".done"),
		filepath.Join(fx.home, ".code-forge", "runs", "r1", "review-state"),
		filepath.Join(fx.home, "rr"),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s was written", p)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("a session ran: %q", stderr.String())
	}
}

// TestB497ContextUnreadableMatchesNode: rules Node's reviewDigest cannot
// read end the ticket `unavailable` / `context-unreadable` after the route
// row, with no session (testdata/b497_node_context.golden, Node on the same
// fixture with context.rules ["nope.md"]).
func TestB497ContextUnreadableMatchesNode(t *testing.T) {
	clock := fixedNow
	fx, w, stderr := b497Fixture(t, &clock)
	w.Cfg = b492Cfg(t, `{"levels":{"L2":{"model":"claude-x","provider":"anthropic"}},"context":{"rules":["nope.md"]}}`)
	id := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\nworld\n"))
	if done, poison, err := w.RunTicket(id); err != nil || done != 1 || poison {
		t.Fatalf("RunTicket = %d, %v, %v", done, poison, err)
	}
	got := "== ledger\n" + readFile(t, filepath.Join(fx.home, ".code-forge", "ledger", "unit.jsonl")) + b497TicketFiles(t, fx, []string{id})
	b497SameLines(t, b497Normalize(got), b497Normalize(readFile(t, filepath.Join("testdata", "b497_node_context.golden"))))
	if stderr.Len() != 0 {
		t.Fatalf("a session ran: %q", stderr.String())
	}
}
