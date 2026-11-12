package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
)

// testdata/b517_recheck_golden.json is Node's real fixLoopRound
// (src/worker/engine.mjs, reviewFile replaced by a fixed result, ctx.spawn a
// scripted recheck session) over 16 scenarios: recheck rounds (resolved,
// mixed with late findings, no fix hunks, stale, coverage miss, hunk
// mismatch, too short, timeouts, a throwing spawn, late_findings block,
// repeated findings), patch_check rounds, a cap stop the raised cap reopens
// and a split stop a new full_in budget reopens. Same key, run, block, clock
// and stamping as the B496 goldens. Each ticket step also records every
// session's options and prompt, and the work dir left behind.

func b517Scenarios(t *testing.T) []ledger.Object {
	t.Helper()
	data, err := os.ReadFile("testdata/b517_recheck_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := ledger.DecodeObject(append(append([]byte(`{"s":`), data...), '}'))
	if err != nil {
		t.Fatal(err)
	}
	out := []ledger.Object{}
	for _, s := range wrapped.Get("s").([]any) {
		out = append(out, s.(ledger.Object))
	}
	if len(out) != 16 {
		t.Fatalf("golden has %d scenarios", len(out))
	}
	return out
}

// b517Spawn is golden.mjs ctx.spawn: it records each session and answers
// from the script; '$HUNKS' becomes the packet's "hunks:" list.
type b517Spawn struct {
	t       *testing.T
	answers []any
	seen    []any
	warns   int
}

func (s *b517Spawn) spawn(opts review.SpawnOpts) (ledger.Object, error) {
	data, err := os.ReadFile(opts.PromptPath)
	if err != nil {
		s.t.Fatal(err)
	}
	prompt := string(data)
	sum := sha256.Sum256(data)
	var cfg any
	if opts.Cfg != nil {
		cfg = opts.Cfg
	}
	s.seen = append(s.seen, ledger.Object{{Key: "level", Value: opts.Level}, {Key: "role", Value: opts.Role}, {Key: "row_extra", Value: opts.RowExtra},
		{Key: "cfg", Value: cfg}, {Key: "prompt_sha", Value: hex.EncodeToString(sum[:])}, {Key: "prompt", Value: prompt}})
	if len(s.answers) == 0 {
		return nil, errors.New("no scripted answer")
	}
	next := deepClone(s.t, s.answers[0].(ledger.Object))
	s.answers = s.answers[1:]
	if msg, ok := next.Get("throw").(string); ok {
		return nil, errors.New(msg)
	}
	if ans, ok := next.Get("answer").(ledger.Object); ok && ans.Get("reviewed_hunks") == "$HUNKS" {
		hunks := []any{}
		for _, line := range strings.Split(prompt, "\n") {
			if strings.HasPrefix(line, "- @@ ") {
				hunks = append(hunks, line[2:])
			}
		}
		ans.Set("reviewed_hunks", hunks)
	}
	return next, nil
}

// b517Request is one golden ticket: the B496 request plus the recheck
// session seams the worker passes (planEngineDry): the review input with its
// work dir and the ledger's schema history.
func b517Request(t *testing.T, home, repo, dir string, l *b496Ledger, cfg map[string]any, step ledger.Object, sp *b517Spawn) FixLoopRequest {
	res, _ := step.Get("review").(ledger.Object)
	r := b496Request(t, home, repo, l, func(func(ledger.Object) error) (ledger.Object, error) {
		if res == nil {
			return nil, errors.New("no review in this step")
		}
		return deepClone(t, res), nil
	})
	r.Cfg = cfg
	r.Owned = step.Get("owned").([]any)
	r.Input = review.Input{RepoRoot: repo, File: l.tk.File, Cfg: cfg, Kind: "code", WorkDir: filepath.Join(dir, "wd")}
	readRows := func() ([]any, error) {
		out := []any{}
		for _, row := range l.rows {
			m, err := plainMap(row)
			if err != nil {
				return nil, err
			}
			out = append(out, m)
		}
		return out, nil
	}
	r.Session = review.Deps{Spawn: sp.spawn, Warn: func(string, string) { sp.warns++ },
		SchemaHistory: schemaHistory(readRows, b496Key, "r1", l.tk.File, l.tk.ContentHash)}
	return r
}

// b517Lenses points the packet at the repository's lens files (the ones the
// Node goldens read).
func b517Lenses(t *testing.T) {
	t.Helper()
	lenses, err := filepath.Abs(filepath.Join("..", "..", "src", "review", "lenses"))
	if err != nil {
		t.Fatal(err)
	}
	old := review.LensDir
	review.LensDir = func() (string, error) { return lenses, nil }
	t.Cleanup(func() { review.LensDir = old })
}

func b517WorkDir(t *testing.T, dir string) any {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "wd"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	out := []any{}
	for _, n := range names {
		out = append(out, n)
	}
	return out
}

// B517: the worker plays Node's recheck and patch_check rounds, reopens a
// cap stop under a raised cap and a split stop under a new full_in budget,
// with the same outcome, signed rows, state files, loaded state, session
// options and prompt bytes as Node.
func TestFixLoopRecheckMatchesNode(t *testing.T) {
	b517Lenses(t)
	for _, sc := range b517Scenarios(t) {
		name := sc.Get("name").(string)
		cfg0, _ := plainMap(sc.Get("cfg").(ledger.Object))
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			home, repo := filepath.Join(root, "home"), filepath.Join(root, "repo")
			for _, d := range []string{home, repo} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("HOME", home)
			b496Repo(t, repo)
			l := &b496Ledger{t: t}
			for i, s := range sc.Get("steps").([]any) {
				st := s.(ledger.Object)
				step := st.Get("step").(ledger.Object)
				file := step.Get("file").(string)
				mark := len(l.rows)
				sp := &b517Spawn{t: t}
				switch step.Get("op") {
				case "write":
					if err := os.WriteFile(filepath.Join(repo, file), []byte(step.Get("content").(string)), 0o644); err != nil {
						t.Fatal(err)
					}
				case "ticket":
					cfg := cfg0
					if c, ok := step.Get("cfg").(ledger.Object); ok {
						cfg, _ = plainMap(c)
					}
					l.tk = FixLoopTicket{Block: "B1", File: file, ContentHash: b496Hash(t, repo, file)}
					sp.answers, _ = step.Get("answers").([]any)
					r := b517Request(t, home, repo, root, l, cfg, step, sp)
					got, err := FixLoopRound(l.tk, r)
					if err != nil {
						t.Fatalf("step %d: %v", i, err)
					}
					sameJSON(t, "outcome", got, st.Get("outcome"))
					if len(sp.answers) != 0 {
						t.Fatalf("step %d: %d answers unused", i, len(sp.answers))
					}
					want := objects(st.Get("spawns"))
					if len(sp.seen) != len(want) {
						t.Fatalf("step %d: %d sessions, node %d", i, len(sp.seen), len(want))
					}
					for k, w := range want {
						g := sp.seen[k].(ledger.Object)
						if g.Get("prompt") != w.Get("prompt") {
							t.Fatalf("step %d session %d prompt differs\n  go: %q\nnode: %q", i, k, g.Get("prompt"), w.Get("prompt"))
						}
						sameJSON(t, "session", g, w)
					}
					retried := 0
					for _, row := range l.rows[mark:] {
						if row.Get("event") == "review.session_timeout" && row.Get("retried") == true {
							retried++
						}
					}
					if sp.warns != retried {
						t.Fatalf("step %d: %d review_retry warnings, %d retried timeouts", i, sp.warns, retried)
					}
				}
				sameJSON(t, "rows", append([]ledger.Object{}, l.rows[mark:]...), objects(st.Get("rows")))
				sameJSON(t, "files", b496Files(t, home), st.Get("files"))
				sameJSON(t, "work dir", b517WorkDir(t, root), st.Get("wd"))
				loaded, err := LoadState(StateWhere{Home: home, RunRootDir: filepath.Join(home, "rr"), RunID: "r1", Block: "B1", File: "a.go",
					Key: b496Key, Rows: l.rows, ContentRoot: repo}, false)
				if err != nil {
					t.Fatal(err)
				}
				sameJSON(t, "load", loaded.Object(), st.Get("load"))
			}
		})
	}
}

// b517FakeClaude is b497FakeClaude plus a recheck answer: a packet with an
// open-findings list gets every listed id resolved.
const b517FakeClaude = `#!/bin/sh
in=$(cat)
heads=$(printf '%s\n' "$in" | grep -E '^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@' | sed -E 's/^(@@ -[0-9,]+ \+[0-9,]+ @@).*/\1/' | awk '!s[$0]++' | sed 's/.*/"&"/' | paste -sd, -)
case "$in" in
*"## open findings"*) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"fake recheck","reviewed_hunks":['"$heads"'],"findings":[],"resolved":[{"id":"F1","resolved":true,"why":"the line is gone"}]},"usage":{"input_tokens":900,"output_tokens":200}}' ;;
*WARNME*) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":false,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[{"id":"F1","file":"b.txt","line_start":2,"line_end":2,"severity":"warning","category":"style","claim":"a warning","evidence":"the line says WARNME","fix":"remove it"}]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
*) printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":true,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[]},"usage":{"input_tokens":1000,"output_tokens":300}}' ;;
esac
`

// B517 end to end: the worker serves the fix ticket of a file with an open
// finding in Go (no hand-over): the dry pass plans a recheck, the real
// session layer runs the fake CLI once at L2, and the round approves the
// fixed content with a recheck round row and a signed approval.
func TestB517WorkerRecheckRunsInGo(t *testing.T) {
	clock := fixedNow
	fx, w, stderr := b497Fixture(t, &clock)
	if err := os.WriteFile(w.SessionDeps.Bins["claude"], []byte(b517FakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	first := fx.ticket(t, "r1", "B2", "b.txt", sha("one\nWARNME\n"))
	if done, poison, err := w.RunTicket(first); err != nil || done != 1 || poison {
		t.Fatalf("RunTicket(%s) = %d, %v, %v", first, done, poison, err)
	}
	if err := os.WriteFile(filepath.Join(fx.root, "b.txt"), []byte("one\nfixed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := fx.ticket(t, "r1", "B2", "b.txt", sha("one\nfixed\n"))
	if done, poison, err := w.RunTicket(second); err != nil || done != 1 || poison {
		t.Fatalf("RunTicket(%s) = %d, %v, %v", second, done, poison, err)
	}
	result := readFile(t, filepath.Join(fx.root, ".code-forge", "reviews", "r1", second+".json"))
	for _, want := range []string{`"status":"reviewed","approved":true`, `"kind":"recheck"`, `"lens":"recheck"`} {
		if !strings.Contains(result, want) {
			t.Fatalf("result lacks %s:\n%s", want, result)
		}
	}
	ledgerText := readFile(t, filepath.Join(fx.home, ".code-forge", "ledger", "unit.jsonl"))
	if !strings.Contains(ledgerText, `"event":"review.round","file":"b.txt","round":2,"level":"L2","kind":"recheck","open_before":1,"closed":1`) ||
		!strings.Contains(ledgerText, `"event":"review.approved","file":"b.txt"`) {
		t.Fatalf("ledger:\n%s", ledgerText)
	}
	line := "level=L2 provider=anthropic model=claude-x effort=none fallback_step=0\n"
	if stderr.String() != line+line {
		t.Fatalf("stderr = %q, want one line per session", stderr.String())
	}
}
