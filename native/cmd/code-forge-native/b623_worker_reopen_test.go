package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/ticket"
	"github.com/ricardov03/code-forge/native/worker"
)

// B623 end to end: `code-forge worker --run r1 --once` serves a reopened
// ticket. The first round leaves src/use.js open with finding F1; an agent's
// signed review.reopened row (from open, verdict agree, to_hash the same
// content, at the state's seq) and a ticket with reopened_at for that content
// start a second full round in Go, and no hand-over row is written. Before
// B623 the second ticket went to Node (a review.reopened row for the file).
// The reviewer is the fake `claude` of B564 on PATH.
func TestB623WorkerServesReopenedTicketInGo(t *testing.T) {
	f := newB489Fix(t)
	packets := t.TempDir()
	if err := os.WriteFile(b533Which(t), []byte(fmt.Sprintf(b564FakeClaude, packets)), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := `{"levels":{"L2":{"model":"claude-x","provider":"anthropic"}},"native":{"daemon":false},"version":1}`
	h := b489Sha(cfg)
	use := filepath.Join(f.root, "src", "use.js")
	b460Write(t, use, "export function go(x) {\n  return x;\n}\n")
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = f.root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
			"GIT_AUTHOR_DATE=2026-11-11T09:00:00Z", "GIT_COMMITTER_DATE=2026-11-11T09:00:00Z")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("add", "src")
	git("commit", "-q", "-m", "base")
	f.record(t, "r1", "active", `,"blocks":{"B3":{"base_sha":"`+git("rev-parse", "HEAD")+`","owned_files":["src/use.js"]}}`+
		`,"config":{"hash":"`+h+`","snapshots":{"`+h+`":`+cfg+`}}`)
	text := "export function go(x) {\n  return x * 2; // BUGGY\n}\n"
	b460Write(t, use, text)
	f.ticket(t, "B3", "src/use.js", text)
	b564Drain(t, f)

	ledgerPath := filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl")
	seq := -1.0
	for _, l := range strings.Split(string(b564Read(t, ledgerPath)), "\n") {
		var row map[string]any
		if json.Unmarshal([]byte(l), &row) == nil && row["event"] == "review.state" && row["file"] == "src/use.js" {
			seq, _ = row["seq"].(float64)
		}
	}
	if seq < 0 {
		t.Fatalf("no review.state row:\n%s", b564Read(t, ledgerPath))
	}
	key := b564Read(t, filepath.Join(f.home, ".code-forge", "runs", "r1.key"))
	hash := b489Sha(text)
	row, err := sign.SignRow(map[string]any{"run": "r1", "ts": "2026-11-11T09:10:00.000Z", "event": "review.reopened", "block": "B3", "file": "src/use.js",
		"from": "open", "stop_reason": nil, "at_seq": seq, "from_hash": hash, "to_hash": hash, "reason": "the finding is wrong", "by": "agent",
		"verdict": map[string]any{"verdict": "agree", "reason": "F1 misreads the line"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(row)
	lf, err := os.OpenFile(ledgerPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lf.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	lf.Close()
	at := "2026-11-11T09:10:00.000Z"
	p := ticket.Parts{Run: "r1", Block: "B3", File: "src/use.js", ContentHash: hash, ReopenedAt: &at}
	id := ticket.ReviewID(p)
	b460Write(t, filepath.Join(worker.QueueDir(f.root), id+".json"),
		`{"ticket":"`+id+`","run":"r1","block":"B3","file":"src/use.js","content_hash":"`+hash+`","enqueued_at":"2026-11-11T09:10:00.001Z","queued_at":"2026-11-11T09:10:00.001Z","started_at":null,"finished_at":null,"duration_ms":null,"reopened_at":"`+at+`"}`+"\n")
	b564Drain(t, f)

	// As Node's fixLoopRound (worker testdata/b623_fixloop_golden.json,
	// reopen-agent-open): the reopen restarts the loop, a full round 1 at
	// state seq 2, and the reopened ticket gets its result.
	after := strings.SplitN(string(b564Read(t, ledgerPath)), `"event":"review.reopened"`, 2)[1]
	for _, want := range []string{`"event":"review.round","file":"src/use.js","round":1,"level":"L2","kind":"full"`,
		`"event":"review.state","block":"B3","file":"src/use.js","seq":2,`, `"event":"review.result","ticket":"` + id + `"`} {
		if !strings.Contains(after, want) {
			t.Fatalf("after the reopen the ledger lacks %s:\n%s", want, after)
		}
	}
	if entries, err := os.ReadDir(packets); err != nil || len(entries) != 2 {
		t.Fatalf("packets %v %v", entries, err)
	}
}
