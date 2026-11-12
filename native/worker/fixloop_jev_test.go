package worker

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/jev"
	"github.com/ricardov03/code-forge/native/ledger"
)

// testdata/b533_jev_golden.json is Node's real fixLoopRound (the B517 tool,
// scratch b533tools/recheck_golden.mjs) with ctx.jev a scripted S1: each
// ticket step holds the Jev answers served and the {state, questions} Node
// asked. Scenarios: a full round triaged by Jev, Jev failing in a full round,
// Jev closing every open finding (no recheck session), Jev closing one of two
// (the packet holds only the other), Jev triaging the recheck's late
// findings, and `resolved` disabled.
func TestB533FixLoopJevMatchesNode(t *testing.T) {
	b517Lenses(t)
	data, err := os.ReadFile("testdata/b533_jev_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := ledger.DecodeObject(append(append([]byte(`{"s":`), data...), '}'))
	if err != nil {
		t.Fatal(err)
	}
	scenarios := wrapped.Get("s").([]any)
	if len(scenarios) != 6 {
		t.Fatalf("golden has %d scenarios", len(scenarios))
	}
	for _, s := range scenarios {
		sc := s.(ledger.Object)
		cfg0, _ := plainMap(sc.Get("cfg").(ledger.Object))
		t.Run(sc.Get("name").(string), func(t *testing.T) {
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
			for i, x := range sc.Get("steps").([]any) {
				st := x.(ledger.Object)
				step := st.Get("step").(ledger.Object)
				file := step.Get("file").(string)
				mark := len(l.rows)
				switch step.Get("op") {
				case "write":
					if err := os.WriteFile(filepath.Join(repo, file), []byte(step.Get("content").(string)), 0o644); err != nil {
						t.Fatal(err)
					}
				case "ticket":
					sp := &b517Spawn{t: t}
					sp.answers, _ = step.Get("answers").([]any)
					l.tk = FixLoopTicket{Block: "B1", File: file, ContentHash: b496Hash(t, repo, file)}
					r := b517Request(t, home, repo, root, l, cfg0, step, sp)
					asked := []any{}
					if script, ok := step.Get("jev").([]any); ok {
						r.Jev = func(state, questions any) (jev.Result, error) {
							asked = append(asked, ledger.Object{{Key: "state", Value: state}, {Key: "questions", Value: questions}})
							if len(script) == 0 {
								return jev.Result{}, errors.New("no scripted jev answer")
							}
							next := script[0].(ledger.Object)
							script = script[1:]
							res := jev.Result{OK: next.Get("ok") == true}
							res.Kind, _ = next.Get("kind").(string)
							if a, ok := next.Get("answers").(ledger.Object); ok {
								res.Answers, _ = plainMap(a)
							}
							return res, nil
						}
					}
					got, err := FixLoopRound(l.tk, r)
					if err != nil {
						t.Fatalf("step %d: %v", i, err)
					}
					sameJSON(t, "outcome", got, st.Get("outcome"))
					if len(sp.answers) != 0 {
						t.Fatalf("step %d: %d answers unused", i, len(sp.answers))
					}
					want, _ := step.Get("asked").([]any)
					sameJSON(t, "jev asks", asked, want)
					spawns := objects(st.Get("spawns"))
					if len(sp.seen) != len(spawns) {
						t.Fatalf("step %d: %d sessions, node %d", i, len(sp.seen), len(spawns))
					}
					for k, w := range spawns {
						if g := sp.seen[k].(ledger.Object); g.Get("prompt") != w.Get("prompt") {
							t.Fatalf("step %d session %d prompt differs\n  go: %q\nnode: %q", i, k, g.Get("prompt"), w.Get("prompt"))
						}
					}
				}
				sameJSON(t, "rows", append([]ledger.Object{}, l.rows[mark:]...), objects(st.Get("rows")))
				sameJSON(t, "files", b496Files(t, home), st.Get("files"))
			}
		})
	}
}
