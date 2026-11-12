package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/review"
	"github.com/ricardov03/code-forge/native/session"
	"github.com/ricardov03/code-forge/native/ticket"
	"github.com/ricardov03/code-forge/native/worker"
)

// B489: `code-forge worker --run <id>` serves the queue in Go. The fixture is
// a git repo whose block B1 changes a.txt and B2 changes b.txt, a run record
// with a config snapshot, a fake `claude` reviewer on PATH (no model, no
// network), a temp HOME with the file key store and a temp run root.

// The pid entry is written just after the start (B552: on a slow CI box the
// child ran ls first), so the fake waits for it, up to 5 s.
const b489FakeClaude = `#!/bin/sh
d="%s"; i=0
while [ -z "$(ls "$d" 2>/dev/null)" ] && [ $i -lt 50 ]; do sleep 0.1; i=$((i+1)); done
ls "$d" >> "%s" 2>/dev/null
in=$(cat)
heads=$(printf '%%s\n' "$in" | grep -E '^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@' | sed -E 's/^(@@ -[0-9,]+ \+[0-9,]+ @@).*/\1/' | awk '!s[$0]++' | sed 's/.*/"&"/' | paste -sd, -)
printf '%%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[]},"usage":{"input_tokens":1000,"output_tokens":300}}'
`

type b489Fix struct {
	runFixture
	root, tmpRoot, pidsSeen string
	sweeps                  int
}

func b489Sha(s string) string { d := sha256.Sum256([]byte(s)); return hex.EncodeToString(d[:]) }

func newB489Fix(t *testing.T) *b489Fix {
	t.Helper()
	// Before the chdir below: the package directory holds the paths.
	schema, _ := filepath.Abs(filepath.Join("..", "..", "..", "src", "review", "finding.schema.json"))
	lenses, _ := filepath.Abs(filepath.Join("..", "..", "..", "src", "review", "lenses"))
	f := &b489Fix{runFixture: newRunFixture(t)}
	t.Setenv("CODE_FORGE_KEY_BACKEND", "file")
	t.Setenv("CODE_FORGE_KEY_JEV", "")
	f.tmpRoot = t.TempDir()
	t.Setenv("CODE_FORGE_TMP_ROOT", f.tmpRoot)
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = f.workspace
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_AUTHOR_DATE=2026-11-11T09:00:00Z", "GIT_COMMITTER_DATE=2026-11-11T09:00:00Z")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q", "-b", "main")
	b460Write(t, filepath.Join(f.workspace, "a.txt"), "hello\n")
	b460Write(t, filepath.Join(f.workspace, "b.txt"), "one\n")
	git("add", "a.txt", "b.txt")
	git("commit", "-q", "-m", "base")
	b460Write(t, filepath.Join(f.workspace, "a.txt"), "hello\nworld\n")
	b460Write(t, filepath.Join(f.workspace, "b.txt"), "one\ntwo\n")
	root, err := filepath.EvalSymlinks(f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	f.root = root
	b460Chdir(t, root)

	// The reviewer lists the run root's pid entries while it runs (step 3).
	bin := t.TempDir()
	f.pidsSeen = filepath.Join(t.TempDir(), "pids-seen")
	script := fmt.Sprintf(b489FakeClaude, filepath.Join(f.tmpRoot, "r1", "pids"), f.pidsSeen)
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	oldSchema, oldLenses := session.FindingSchemaPath, review.LensDir
	session.FindingSchemaPath = func() (string, error) { return schema, nil }
	review.LensDir = func() (string, error) { return lenses, nil }
	oldDaemon, oldOrphans, oldSignals := workerStartDaemon, workerOrphans, workerSignals
	workerStartDaemon = func(string, map[string]any) (func(), error) { return func() {}, nil }
	workerOrphans = func(string) func() { return func() { f.sweeps++ } }
	t.Cleanup(func() {
		session.FindingSchemaPath, review.LensDir = oldSchema, oldLenses
		workerStartDaemon, workerOrphans, workerSignals = oldDaemon, oldOrphans, oldSignals
	})
	base := git("rev-parse", "HEAD")
	f.withConfig(t, base, `{"levels":{"L2":{"model":"claude-x","provider":"anthropic"}},"native":{"daemon":false},"version":1}`)
	return f
}

// withConfig writes the run record with blocks B1/B2 and a snapshot of cfg
// (canonical JSON: sorted keys, no spaces).
func (f *b489Fix) withConfig(t *testing.T, base, cfg string) {
	t.Helper()
	extra := `,"blocks":{"B1":{"base_sha":"` + base + `","owned_files":["a.txt"]},"B2":{"base_sha":"` + base + `","owned_files":["b.txt"]}}`
	if cfg != "" {
		h := b489Sha(cfg)
		extra += `,"config":{"hash":"` + h + `","snapshots":{"` + h + `":` + cfg + `}}`
	}
	f.record(t, "r1", "active", extra)
}

func (f *b489Fix) ticket(t *testing.T, block, file, text string) string {
	t.Helper()
	p := ticket.Parts{Run: "r1", Block: block, File: file, ContentHash: b489Sha(text)}
	id := ticket.ReviewID(p)
	b460Write(t, filepath.Join(worker.QueueDir(f.root), id+".json"),
		`{"ticket":"`+id+`","run":"r1","block":"`+block+`","file":"`+file+`","content_hash":"`+p.ContentHash+`","enqueued_at":"2026-11-11T09:00:00.000Z"}`+"\n")
	return id
}

func (f *b489Fix) answers(t *testing.T) string {
	t.Helper()
	id := strings.Repeat("c", 24)
	b460Write(t, filepath.Join(worker.QueueDir(f.root), id+".json"), `{"ticket":"`+id+`","run":"r1","block":"B1","file":"a.txt","content_hash":"`+b489Sha("hello\nworld\n")+`","kind":"answers","by_file":"b.txt","by_hash":"x","answers":["F1"],"enqueued_at":"2026-11-11T09:00:00.000Z"}`+"\n")
	return id
}

// results counts the signed review.result rows of a ticket (session rows
// are unsigned in Node too, so the ledger is read line by line).
func (f *b489Fix) results(t *testing.T, id string) int {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"))
	n := 0
	for _, l := range strings.Split(string(data), "\n") {
		if strings.Contains(l, `"event":"review.result"`) && strings.Contains(l, `"ticket":"`+id+`"`) && strings.Contains(l, `"mac":"`) {
			n++
		}
	}
	return n
}

func b489Exists(p string) bool { _, err := os.Stat(p); return err == nil }

// --once drains the queue in Go: each ported ticket is reviewed once (done
// marker, one signed result row), the orphan clean-up runs after the drain,
// the reviewer's pid entry sits under the run root (step 3) and nothing goes
// to Node.
func TestB489WorkerOnceServesInGo(t *testing.T) {
	f := newB489Fix(t)
	ids := []string{f.ticket(t, "B1", "a.txt", "hello\nworld\n"), f.ticket(t, "B2", "b.txt", "one\ntwo\n")}
	code, out, errText := b460Call(t, "worker", "--run", "r1", "--once")
	if code != 0 || out != "" || !strings.HasSuffix(errText, "worker: drained 2 ticket(s)\n") {
		t.Fatalf("worker --once = %d %q %q", code, out, errText)
	}
	for _, id := range ids {
		if !b489Exists(filepath.Join(worker.QueueDir(f.root), id+".done")) || f.results(t, id) != 1 {
			t.Fatalf("ticket %s not reviewed once in Go", id)
		}
	}
	if rows := b460Handovers(t, f.home); len(rows) != 0 {
		t.Fatalf("hand-overs %v", rows)
	}
	if f.sweeps != 1 {
		t.Fatalf("orphan sweeps %d, want 1", f.sweeps)
	}
	seen, _ := os.ReadFile(f.pidsSeen)
	if strings.TrimSpace(string(seen)) == "" {
		t.Fatalf("no pid entry under the run root %s/r1/pids while the reviewer ran", f.tmpRoot)
	}
	owner, err := os.ReadFile(filepath.Join(f.tmpRoot, "r1", "owner.json"))
	if err != nil || !strings.Contains(string(owner), fmt.Sprintf(`"pid":%d`, os.Getpid())) {
		t.Fatalf("owner.json %s %v", owner, err)
	}
	// The announcement is withdrawn: a second worker boots.
	if code, _, errText := b460Call(t, "worker", "--run", "r1", "--once"); code != 0 || !strings.HasSuffix(errText, "worker: drained 0 ticket(s)\n") {
		t.Fatalf("second worker = %d %q", code, errText)
	}
}

// The split rule end to end: an unported ticket hands the whole worker to
// Node (worker-ticket-unported); the ticket stays queued, unclaimed, for
// Node to review once.
func TestB489WorkerUnportedTicketHandsOver(t *testing.T) {
	f := newB489Fix(t)
	id := f.answers(t)
	code, out, _ := b460Call(t, "worker", "--run", "r1", "--once")
	if code != 127 || out != "" {
		t.Fatalf("worker = %d %q, want the Node hand-over (127)", code, out)
	}
	if got := strings.Join(b460Handovers(t, f.home), ","); got != "worker worker-ticket-unported" {
		t.Fatalf("hand-overs %s", got)
	}
	if b489Exists(filepath.Join(worker.QueueDir(f.root), id+".done")) || !b489Exists(filepath.Join(worker.QueueDir(f.root), id+".json")) {
		t.Fatal("the unported ticket is not left pending")
	}
	if p := worker.Pending(f.root); len(p) != 1 || p[0] != id {
		t.Fatalf("pending %v", p)
	}
	if n := f.results(t, id); n != 0 {
		t.Fatalf("Go wrote %d result rows for the unported ticket", n)
	}
}

// The cases Go does not serve go to Node before anything is written; a bad
// snapshot is Node's own refusal, answered in Go.
func TestB489WorkerHandOverCases(t *testing.T) {
	f := newB489Fix(t)
	base := strings.Repeat("0", 40)
	store := filepath.Join(f.home, ".code-forge", "store", "jev.key")
	cases := []struct {
		name, cfg, backend, store string
		keychain                  bool
		code                      int
		want                      string
	}{
		{"no snapshot", "", "file", "", false, 127, "worker worker-config"},
		// B533: a resolvable Jev key serves in Go; only the sources Go
		// cannot read like Node keep a narrow reason.
		{"bad key reference", `{"system1":{"key":"keychain:BAD NAME"},"version":1}`, "file", "", false, 127, "worker worker-jev-key-ref"},
		{"undecodable store", `{"version":1}`, "file", "{bad", false, 127, "worker worker-jev-key-store"},
		{"keychain holds a key", `{"version":1}`, "", "", true, 127, "worker worker-jev-keychain"},
	}
	oldProbe := workerJevProbe
	t.Cleanup(func() { workerJevProbe = oldProbe })
	for _, c := range cases {
		f.withConfig(t, base, c.cfg)
		t.Setenv("CODE_FORGE_KEY_BACKEND", c.backend)
		found := c.keychain
		workerJevProbe = worker.JevProbe{Keychain: func(string, string) (bool, bool) { return found, true }}
		_ = os.Remove(store)
		if c.store != "" {
			b460Write(t, store, c.store)
		}
		before := len(b460Handovers(t, f.home))
		code, _, _ := b460Call(t, "worker", "--run", "r1", "--once")
		rows := b460Handovers(t, f.home)
		if code != c.code || len(rows) != before+1 || rows[before] != c.want {
			t.Errorf("%s: %d %v, want %d %s", c.name, code, rows, c.code, c.want)
		}
		if b489Exists(filepath.Join(f.tmpRoot, "r1")) {
			t.Errorf("%s: the run root was made before the hand-over", c.name)
		}
	}
	_ = os.Remove(store)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "file")
	f.record(t, "r1", "active", `,"config":{"hash":"`+strings.Repeat("f", 64)+`","snapshots":{}}`)
	code, _, errText := b460Call(t, "worker", "--run", "r1", "--once")
	if code != 1 || errText != "worker: run r1: the config snapshot in the run record is missing or altered\n" {
		t.Fatalf("bad snapshot = %d %q", code, errText)
	}
}

// Without --once the worker serves until SIGTERM/SIGINT, then stops cleanly:
// the signal arrives once the ticket is done.
func TestB489WorkerServesUntilSignal(t *testing.T) {
	f := newB489Fix(t)
	id := f.ticket(t, "B1", "a.txt", "hello\nworld\n")
	stopped := make(chan struct{})
	workerSignals = func(c chan<- os.Signal) func() {
		go func() {
			for !b489Exists(filepath.Join(worker.QueueDir(f.root), id+".done")) {
				select {
				case <-stopped:
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
			c <- syscall.SIGTERM
		}()
		return func() { close(stopped) }
	}
	code, _, errText := b460Call(t, "worker", "--run", "r1", "--poll-ms", "10")
	want := fmt.Sprintf("worker: pid %d serving run r1 · jev key absent (fallback)\n", os.Getpid())
	if code != 0 || !strings.Contains(errText, want) || !strings.HasSuffix(errText, "worker: stopped\n") {
		t.Fatalf("worker = %d %q", code, errText)
	}
	if f.results(t, id) != 1 {
		t.Fatal("ticket not reviewed once")
	}
}
