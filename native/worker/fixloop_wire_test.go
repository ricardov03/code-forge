package worker

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
)

func deepClone(t *testing.T, v ledger.Object) ledger.Object {
	t.Helper()
	data, err := ledger.Stringify(v, false)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ledger.DecodeObject(data)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Every golden scenario replayed through FixLoopRound: the outcome, the new
// ledger rows, the files under HOME and loadState after each step are Node's.
// The one ticket Go does not serve (cap-stop-then-stopped's second ticket: a
// patch_check) returns ErrUnsupported with nothing written.
func TestFixLoopRoundMatchesNode(t *testing.T) {
	for _, sc := range b496Scenarios(t) {
		name := sc.Get("name").(string)
		cfg, _ := plainMap(sc.Get("cfg").(ledger.Object))
		t.Run(name, func(t *testing.T) {
			home, repo := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			base := b496Repo(t, repo)
			l := &b496Ledger{t: t}
			for i, s := range sc.Get("steps").([]any) {
				st := s.(ledger.Object)
				step := st.Get("step").(ledger.Object)
				file, _ := step.Get("file").(string)
				mark := len(l.rows)
				switch step.Get("op") {
				case "row":
					l.rows = append(l.rows, objects(st.Get("rows"))...)
				case "unlink-state":
					if err := os.Remove(StatePath(home, "r1", "B1", file)); err != nil {
						t.Fatal(err)
					}
				case "junk-state":
					p := StatePath(home, "r1", "B1", file)
					if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte("{\"junk\":true}\n"), 0o600); err != nil {
						t.Fatal(err)
					}
				case "legacy":
					to := LegacyStatePath(filepath.Join(home, "rr"), "B1", file)
					if err := os.MkdirAll(filepath.Dir(to), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(StatePath(home, "r1", "B1", file), to); err != nil {
						t.Fatal(err)
					}
				case "ticket":
					hash := b496Hash(t, repo, file)
					if step.Get("hash") == "moved" {
						hash = strings.Repeat("f", 64)
					}
					l.tk = FixLoopTicket{Block: "B1", File: file, ContentHash: hash}
					failOn, _ := step.Get("failOn").(string)
					res := step.Get("review").(ledger.Object)
					r := b496Request(t, home, repo, l, func(func(ledger.Object) error) (ledger.Object, error) {
						if th, ok := res.Get("throw").(ledger.Object); ok {
							code, _ := th.Get("code").(string)
							return nil, &RoundError{Code: code, Message: th.Get("message").(string)}
						}
						return deepClone(t, res), nil
					})
					r.WriteRow = func(row ledger.Object) error {
						if row.Get("event") == failOn {
							return errors.New("boom")
						}
						return l.write(row)
					}
					r.Cfg = cfg
					r.Base = b496Base(step, base)
					r.Owned = step.Get("owned").([]any)
					filesBefore := mustJSON(t, b496Files(t, home))
					got, err := FixLoopRound(l.tk, r)
					// B517 ports the patch_check, but this config names no levels.L2:
					// Go's session guard hands the ticket over before any write.
					if name == "cap-stop-then-stopped" && i == 1 {
						if !IsUnsupported(err) || len(l.rows) != mark || mustJSON(t, b496Files(t, home)) != filesBefore {
							t.Fatalf("patch_check: %v, %d rows", err, len(l.rows)-mark)
						}
						return
					}
					if err != nil {
						t.Fatalf("step %d: %v", i, err)
					}
					sameJSON(t, "outcome", got, st.Get("outcome"))
				}
				sameJSON(t, "rows", append([]ledger.Object{}, l.rows[mark:]...), objects(st.Get("rows")))
				sameJSON(t, "files", b496Files(t, home), st.Get("files"))
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

// No ledger: Node's no-ledger outcome, nothing read.
func TestFixLoopRoundNoLedger(t *testing.T) {
	got, err := FixLoopRound(FixLoopTicket{Block: "B1", File: "a.go"}, FixLoopRequest{RunID: "r1"})
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "outcome", got, ledger.Object{{Key: "status", Value: "unavailable"}, {Key: "reason", Value: "no-ledger"},
		{Key: "approved", Value: false}, {Key: "engine", Value: "adaptive"}, {Key: "sessions", Value: []any{}}})
}

// A ledger read that throws: unavailable with its reason; an ErrUnsupported
// read is passed up.
func TestFixLoopRoundReadFails(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	b496Repo(t, repo)
	l := &b496Ledger{t: t, tk: FixLoopTicket{Block: "B1", File: "a.go", ContentHash: b496Hash(t, repo, "a.go")}}
	r := b496Request(t, home, repo, l, nil)
	r.ReadRows = func() ([]ledger.Object, error) { return nil, &RoundError{Code: "EACCES", Message: "denied"} }
	got, err := FixLoopRound(l.tk, r)
	if err != nil || got.Get("status") != "unavailable" || got.Get("reason") != "EACCES" {
		t.Fatalf("%v %v", mustJSON(t, got), err)
	}
	r.ReadRows = func() ([]ledger.Object, error) { return nil, unsupported("ledger bytes") }
	if _, err := FixLoopRound(l.tk, r); !IsUnsupported(err) || len(l.rows) != 0 {
		t.Fatalf("%v %d rows", err, len(l.rows))
	}
}

// Tickets that need a part Go does not port return ErrUnsupported before
// anything is written (no budget row, no restored state, no round): a
// `block reopen` row, an autopilot extra round, a recheck with no work dir,
// and a stop the raised cap reopens into such a recheck. (B537 ported the
// review.recheck_scope file and base-with-peers rechecks: see
// TestFixLoopRoundRecheckScopeAndPeersRun.)
func TestFixLoopRoundUnsupportedWritesNothing(t *testing.T) {
	sign := func(row ledger.Object) ledger.Object {
		signed, err := signAt(append(ledger.Object{{Key: "run", Value: "r1"}}, row...), b496Key, b496Clock())
		if err != nil {
			t.Fatal(err)
		}
		return signed
	}
	cases := map[string]func(t *testing.T, home, repo string, l *b496Ledger, r *FixLoopRequest){
		"reopen row": func(t *testing.T, home, repo string, l *b496Ledger, r *FixLoopRequest) {
			l.rows = append(l.rows, sign(ledger.Object{{Key: "event", Value: "review.reopened"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "a.go"}, {Key: "by", Value: "human"}}))
		},
		"extra round row": func(t *testing.T, home, repo string, l *b496Ledger, r *FixLoopRequest) {
			l.rows = append(l.rows, sign(ledger.Object{{Key: "event", Value: "autopilot.extra_round"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "a.go"}, {Key: "grant_id", Value: "g1"}}))
		},
		"recheck with no work dir": func(t *testing.T, home, repo string, l *b496Ledger, r *FixLoopRequest) { b517Warn(t, l, r) },
		"stop the cap reopens": func(t *testing.T, home, repo string, l *b496Ledger, r *FixLoopRequest) {
			state := ledger.Object{{Key: "file", Value: "a.go"}, {Key: "level", Value: "L2"}, {Key: "round", Value: 1.0}, {Key: "rounds_at_level", Value: 1.0},
				{Key: "warn_rounds_at_level", Value: 0.0}, {Key: "open", Value: []any{}}, {Key: "late", Value: []any{}}, {Key: "reviewed_content", Value: nil},
				{Key: "l3_rung_used", Value: false}, {Key: "status", Value: "stopped"}, {Key: "next", Value: ledger.Object{{Key: "action", Value: "stop"}, {Key: "reason", Value: "review_cap"}}},
				{Key: "last_packet", Value: nil}, {Key: "pending_kind", Value: nil}}
			if err := SaveState(SaveRequest{Home: home, RunID: "r1", Block: "B1", File: "a.go", Key: b496Key, State: state, Seq: 0, WriteRow: l.write, Now: b496Clock}); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			home, repo := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			b496Repo(t, repo)
			l := &b496Ledger{t: t, tk: FixLoopTicket{Block: "B1", File: "a.go", ContentHash: b496Hash(t, repo, "a.go")}}
			r := b496Request(t, home, repo, l, func(func(ledger.Object) error) (ledger.Object, error) { return b496Clean(), nil })
			setup(t, home, repo, l, &r)
			mark, before := len(l.rows), mustJSON(t, b496Files(t, home))
			r.Review = func(func(ledger.Object) error) (ledger.Object, error) { t.Fatal("review ran"); return nil, nil }
			_, err := FixLoopRound(l.tk, r)
			if !IsUnsupported(err) {
				t.Fatalf("got %v", err)
			}
			named := map[string]string{"recheck with no work dir": "a recheck with no work dir", "stop the cap reopens": "a recheck with no work dir"}
			if want, ok := named[name]; ok && !strings.Contains(err.Error(), want) {
				t.Fatalf("got %v, want %q", err, want)
			}
			if len(l.rows) != mark || mustJSON(t, b496Files(t, home)) != before {
				t.Fatalf("written: %d rows", len(l.rows)-mark)
			}
		})
	}
}

// B537: a recheck under review.recheck_scope file and a recheck with a base
// and peers (moved-code hints, cross-file evidence) now run in Go: the round
// is a recheck session (never a full review) and its rows are written.
func TestFixLoopRoundRecheckScopeAndPeersRun(t *testing.T) {
	b517Lenses(t)
	cases := map[string]func(home, repo, base string, r *FixLoopRequest){
		"recheck_scope file": func(home, repo, base string, r *FixLoopRequest) {
			r.Cfg = map[string]any{"review": map[string]any{"recheck_scope": "file"}, "levels": map[string]any{"L2": map[string]any{"provider": "p", "model": "m"}}}
			r.Input = review.Input{RepoRoot: repo, File: "a.go", Base: base, Cfg: r.Cfg, WorkDir: filepath.Join(home, "wd")}
		},
		"recheck with peers": func(home, repo, base string, r *FixLoopRequest) {
			r.Cfg = map[string]any{"levels": map[string]any{"L2": map[string]any{"provider": "p", "model": "m"}}}
			r.Input = review.Input{RepoRoot: repo, File: "a.go", Base: base, Peers: []string{"a.go", "b.go"}, Cfg: r.Cfg, WorkDir: filepath.Join(home, "wd")}
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			home, repo := t.TempDir(), t.TempDir()
			t.Setenv("HOME", home)
			base := b496Repo(t, repo)
			l := &b496Ledger{t: t, tk: FixLoopTicket{Block: "B1", File: "a.go", ContentHash: b496Hash(t, repo, "a.go")}}
			r := b496Request(t, home, repo, l, nil)
			b517Warn(t, l, &r)
			if err := os.WriteFile(filepath.Join(repo, "a.go"), []byte("package a\n\nfunc A() { _ = 1 }\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			l.tk.ContentHash = b496Hash(t, repo, "a.go")
			setup(home, repo, base, &r)
			r.Review = func(func(ledger.Object) error) (ledger.Object, error) { t.Fatal("full review ran"); return nil, nil }
			answer := ledger.Object{{Key: "passed", Value: true}, {Key: "summary", Value: "fine"}, {Key: "reviewed_hunks", Value: "$HUNKS"}, {Key: "findings", Value: []any{}},
				{Key: "resolved", Value: []any{ledger.Object{{Key: "id", Value: "f1"}, {Key: "resolved", Value: true}, {Key: "why", Value: "fixed"}}}}}
			sp := &b517Spawn{t: t, answers: []any{ledger.Object{{Key: "status", Value: "ok"}, {Key: "provider", Value: "p"}, {Key: "model", Value: "m"},
				{Key: "usage", Value: ledger.Object{{Key: "tokens_in", Value: 100.0}, {Key: "tokens_out", Value: 500.0}}}, {Key: "answer", Value: answer}}}}
			r.Session = review.Deps{Spawn: sp.spawn}
			mark := len(l.rows)
			if _, err := FixLoopRound(l.tk, r); err != nil {
				t.Fatalf("FixLoopRound: %v", err)
			}
			if len(sp.seen) != 1 || sp.seen[0].(ledger.Object).Get("row_extra").(ledger.Object).Get("lens") != "recheck" {
				t.Fatalf("sessions %v, want one recheck", sp.seen)
			}
			if ev := strings.Join(l.events()[mark:], ","); ev != "review.round,review.approved,review.state" {
				t.Fatalf("rows %s", ev)
			}
		})
	}
}

// b517Warn plays one full round that leaves f1 open, so the next ticket is a
// recheck.
func b517Warn(t *testing.T, l *b496Ledger, r *FixLoopRequest) {
	t.Helper()
	warn := ledger.Object{{Key: "status", Value: "reviewed"}, {Key: "approved", Value: false}, {Key: "findings", Value: []any{ledger.Object{
		{Key: "id", Value: "f1"}, {Key: "severity", Value: "warning"}, {Key: "line_start", Value: 1.0}, {Key: "line_end", Value: 2.0},
		{Key: "claim", Value: "c"}, {Key: "evidence", Value: "e"}, {Key: "fix", Value: "f"}}}}, {Key: "sessions", Value: []any{}}}
	r.Review = func(func(ledger.Object) error) (ledger.Object, error) { return warn, nil }
	if _, err := FixLoopRound(l.tk, *r); err != nil {
		t.Fatal(err)
	}
}

// B42: the block's files run at once; the budget row's check-then-write is
// under the block lock, so the block gets exactly one review.budget row.
// Run with -race -count=20.
func TestFixLoopRoundBudgetOncePerBlock(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	base := b496Repo(t, repo)
	var mu sync.Mutex
	rows := []ledger.Object{}
	files := []string{"a.go", "b.go", "db/migration.go"}
	var wg sync.WaitGroup
	errs := make([]error, len(files))
	outcomes := make([]ledger.Object, len(files))
	for i, f := range files {
		wg.Add(1)
		go func(i int, f string) {
			defer wg.Done()
			hash := b496Hash(t, repo, f)
			r := FixLoopRequest{Home: home, RunID: "r1", RunRootDir: filepath.Join(home, "rr"), ContentRoot: repo, Level: "L2", Cfg: map[string]any{}, Key: b496Key,
				Base: &base, Owned: []any{"a.go", "b.go", "db/**"}, Now: b496Clock,
				ReadRows: func() ([]ledger.Object, error) {
					mu.Lock()
					defer mu.Unlock()
					return append([]ledger.Object{}, rows...), nil
				},
				WriteRow: func(row ledger.Object) error {
					signed, err := signAt(append(append(ledger.Object{{Key: "run", Value: "r1"}}, row...), ledger.Field{Key: "block", Value: "B1"},
						ledger.Field{Key: "file", Value: f}, ledger.Field{Key: "content_hash", Value: hash}), b496Key, b496Clock())
					if err != nil {
						return err
					}
					mu.Lock()
					defer mu.Unlock()
					rows = append(rows, signed)
					return nil
				},
				Review: func(func(ledger.Object) error) (ledger.Object, error) { return b496Clean(), nil }}
			outcomes[i], errs[i] = FixLoopRound(FixLoopTicket{Block: "B1", File: f, ContentHash: hash}, r)
		}(i, f)
	}
	wg.Wait()
	budgets := 0
	for _, r := range rows {
		if r.Get("event") == "review.budget" {
			budgets++
		}
	}
	for i := range files {
		if errs[i] != nil || outcomes[i].Get("status") != "reviewed" || outcomes[i].Get("approved") != true {
			t.Fatalf("%s: %v %s", files[i], errs[i], mustJSON(t, outcomes[i]))
		}
	}
	if budgets != 1 {
		t.Fatalf("%d review.budget rows", budgets)
	}
}

// The open-state reasons: next.reason no-fix-hunks is named, any other open
// state is findings-open (engine.mjs playRound's last line).
func TestRoundOutcomeOpenReasons(t *testing.T) {
	f := ledger.Object{{Key: "id", Value: "f1"}}
	for reason, want := range map[string]string{"no-fix-hunks": "no-fix-hunks", "stall": "findings-open"} {
		state := ledger.Object{{Key: "round", Value: 2.0}, {Key: "level", Value: "L2"}, {Key: "open", Value: []any{f}}, {Key: "late", Value: []any{f}},
			{Key: "status", Value: "open"}, {Key: "next", Value: ledger.Object{{Key: "action", Value: "fix"}, {Key: "reason", Value: reason}, {Key: "trigger", Value: "t"}}}}
		got := roundOutcome(state, "recheck", played{State: state, Engine: ledger.Object{{Key: "engine", Value: "adaptive"}, {Key: "sessions", Value: []any{"s"}}}})
		sameJSON(t, reason, got, ledger.Object{{Key: "round", Value: 2.0}, {Key: "kind", Value: "recheck"}, {Key: "level", Value: "L2"}, {Key: "next", Value: state.Get("next")},
			{Key: "trigger", Value: "t"}, {Key: "late", Value: 1.0}, {Key: "engine", Value: "adaptive"}, {Key: "sessions", Value: []any{"s"}},
			{Key: "status", Value: "reviewed"}, {Key: "approved", Value: false}, {Key: "reason", Value: want}, {Key: "findings", Value: []any{f}}})
	}
}
