package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// B564 end to end: `code-forge worker --run r1 --once` serves the fix ticket
// of a .js file whose block also changed (and took code from) a peer file:
// the recheck packet carries the peer's cross-file evidence and the
// moved-code hint, the round approves in Go and no hand-over row is written.
// The reviewer is a fake `claude` on PATH that saves every packet it reads;
// HOME and CODE_FORGE_TMP_ROOT are temp dirs (newB489Fix).

const b564FakeClaude = `#!/bin/sh
in=$(cat)
n=$(ls "%[1]s" | wc -l | tr -d ' ')
printf '%%s' "$in" > "%[1]s/packet-$n.md"
heads=$(printf '%%s\n' "$in" | grep -E '^- @@ ' | sed -E 's/^- (@@ -[0-9,]+ \+[0-9,]+ @@).*/\1/' | awk '!s[$0]++' | sed 's/.*/"&"/' | paste -sd, -)
if [ -z "$heads" ]; then
heads=$(printf '%%s\n' "$in" | grep -E '^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@' | sed -E 's/^(@@ -[0-9,]+ \+[0-9,]+ @@).*/\1/' | awk '!s[$0]++' | sed 's/.*/"&"/' | paste -sd, -)
fi
case "$in" in
*"## open findings"*) printf '%%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"fake recheck","reviewed_hunks":['"$heads"'],"findings":[],"resolved":[{"id":"F1","resolved":true,"why":"the marker is gone"}]},"usage":{"input_tokens":900,"output_tokens":200}}' ;;
*BUGGY*) printf '%%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":false,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[{"id":"F1","file":"src/use.js","line_start":4,"line_end":4,"severity":"warning","category":"logic","claim":"scale in lib.js changed; the result is off","evidence":"the line says BUGGY","fix":"use the new scale"}]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
*) printf '%%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
esac
`

const b564Average = "export function average(items) {\n  const total = items.reduce((a, b) => a + b, 0);\n  const count = items.length;\n" +
	"  const mean = total / count;\n  return Math.round(mean * 100) / 100;\n}\n"

func TestB564WorkerRecheckWithPeersServesInGo(t *testing.T) {
	f := newB489Fix(t)
	packets := t.TempDir()
	if err := os.WriteFile(b533Which(t), []byte(fmt.Sprintf(b564FakeClaude, packets)), 0o755); err != nil {
		t.Fatal(err)
	}
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
	lib, use := filepath.Join(f.root, "src", "lib.js"), filepath.Join(f.root, "src", "use.js")
	b460Write(t, lib, "export function scale(v) {\n  return v * 2;\n}\n"+b564Average)
	b460Write(t, use, "import { scale } from './lib.js';\n\nexport function go(x) {\n  return scale(x);\n}\n")
	git("add", "src")
	git("commit", "-q", "-m", "js base")
	base := git("rev-parse", "HEAD")
	cfg := `{"levels":{"L2":{"model":"claude-x","provider":"anthropic"}},"native":{"daemon":false},"version":1}`
	h := b489Sha(cfg)
	f.record(t, "r1", "active", `,"blocks":{"B3":{"base_sha":"`+base+`","owned_files":["src/use.js","src/lib.js"]}}`+
		`,"config":{"hash":"`+h+`","snapshots":{"`+h+`":`+cfg+`}}`)
	// the block: scale changes in lib.js, average moves from lib.js to use.js
	b460Write(t, lib, "export function scale(v) {\n  return v * 3;\n}\n")
	first := "import { scale } from './lib.js';\n\nexport function go(x) {\n  const y = scale(x); // BUGGY\n  return y + 1;\n}\n" + b564Average
	b460Write(t, use, first)

	f.ticket(t, "B3", "src/use.js", first)
	b564Drain(t, f)
	fixed := strings.Replace(first, " // BUGGY", "", 1)
	b460Write(t, use, fixed)
	f.ticket(t, "B3", "src/use.js", fixed)
	b564Drain(t, f)

	ledgerText := string(b564Read(t, filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl")))
	for _, want := range []string{
		`"event":"review.round","file":"src/use.js","round":1,"level":"L2","kind":"full"`,
		`"event":"review.round","file":"src/use.js","round":2,"level":"L2","kind":"recheck","open_before":1,"closed":1`,
		`"extra_files":["src/lib.js"],"extra_files_omitted":0,"extra_files_trimmed":[]`,
		`"event":"review.approved","file":"src/use.js"`,
	} {
		if !strings.Contains(ledgerText, want) {
			t.Fatalf("ledger lacks %s:\n%s", want, ledgerText)
		}
	}
	entries, err := os.ReadDir(packets)
	if err != nil || len(entries) != 2 {
		t.Fatalf("packets %v %v", entries, err)
	}
	recheck := string(b564Read(t, filepath.Join(packets, "packet-1.md")))
	for _, want := range []string{"# Lens: recheck", "## moved code", "## cross-file evidence", "src/lib.js", "-  return v * 2;", "+  return v * 3;", "## open findings"} {
		if !strings.Contains(recheck, want) {
			t.Fatalf("recheck packet lacks %q:\n%s", want, recheck)
		}
	}
}

func b564Drain(t *testing.T, f *b489Fix) {
	t.Helper()
	code, out, errText := b460Call(t, "worker", "--run", "r1", "--once")
	if code != 0 || out != "" || !strings.HasSuffix(errText, "worker: drained 1 ticket(s)\n") {
		t.Fatalf("worker --once = %d %q %q", code, out, errText)
	}
	if rows := b460Handovers(t, f.home); len(rows) != 0 {
		t.Fatalf("hand-overs %v", rows)
	}
}

func b564Read(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
