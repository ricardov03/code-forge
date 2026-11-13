package worker

import (
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
)

func b496Anchor(t *testing.T, block, file string, rung any) ledger.Object {
	t.Helper()
	row := ledger.Object{{Key: "run", Value: "r1"}, {Key: "event", Value: StateEvent}, {Key: "block", Value: block}, {Key: "file", Value: file},
		{Key: "seq", Value: 1.0}, {Key: "state_sha", Value: "x"}, {Key: "l3_rung_used", Value: rung}}
	signed, err := signAt(row, b496Key, b496Clock())
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// fileRungUsed: only this file's verified anchors with l3_rung_used === true
// count; any bad anchor of the run makes the block tampered (used).
func TestRungTaken(t *testing.T) {
	rows := []ledger.Object{b496Anchor(t, "B1", "a.go", true), b496Anchor(t, "B1", "b.go", false), b496Anchor(t, "B2", "c.go", true), b496Anchor(t, "B1", "d.go", "yes")}
	for file, want := range map[string]bool{"a.go": true, "b.go": false, "c.go": false, "d.go": false} {
		if status, used := RungTaken("r1", "B1", file, b496Key, rows); status != "ok" || used != want {
			t.Fatalf("%s: %s %v", file, status, used)
		}
	}
	bad := b496Anchor(t, "B9", "z.go", false)
	bad.Set("block", "B8")
	if status, used := RungTaken("r1", "B1", "b.go", b496Key, append(rows, bad)); status != "tampered" || !used {
		t.Fatalf("tampered: %s %v", status, used)
	}
	other := b496Anchor(t, "B9", "z.go", false)
	other.Set("run", "r2")
	if status, used := RungTaken("r1", "B1", "b.go", b496Key, append(rows, other)); status != "ok" || used {
		t.Fatalf("other run: %s %v", status, used)
	}
}

// mayTakeRung: never with the rung taken or for a patch_check; never for a
// first full round while the cap and the ladder are both above 1 (a value
// that is not an integer >= 0 is the default 4 / 2); else yes.
func TestMayTakeRung(t *testing.T) {
	fresh := review.NewFileState("a.go", "L2", false)
	second := review.NewFileState("a.go", "L2", false)
	second.Set("round", 1.0)
	second.Set("rounds_at_level", 1.0)
	cfg := func(section, key string, v any) map[string]any {
		return map[string]any{section: map[string]any{key: v}}
	}
	cases := []struct {
		name  string
		state ledger.Object
		kind  string
		cfg   map[string]any
		rung  bool
		want  bool
	}{
		{"rung taken", second, "recheck", nil, true, false},
		{"patch_check", second, "patch_check", nil, false, false},
		{"first full round", fresh, "full", nil, false, false},
		{"first full, cap 1", fresh, "full", cfg("review", "max_rounds_per_file", 1.0), false, true},
		{"first full, ladder 1", fresh, "full", cfg("escalation", "review_rounds_per_level", 1.0), false, true},
		{"first full, cap 2.5 is the default", fresh, "full", cfg("review", "max_rounds_per_file", 2.5), false, false},
		{"first full, cap -1 is the default", fresh, "full", cfg("review", "max_rounds_per_file", -1.0), false, false},
		{"recheck", second, "recheck", nil, false, true},
		{"retried full after a round", second, "full", nil, false, true},
	}
	for _, c := range cases {
		if got := MayTakeRung(c.state, c.kind, c.cfg, c.rung); got != c.want {
			t.Fatalf("%s: %v", c.name, got)
		}
	}
}

type b496Ledger struct {
	t    *testing.T
	rows []ledger.Object
	tk   FixLoopTicket
}

func (l *b496Ledger) write(row ledger.Object) error {
	l.rows = append(l.rows, b496Stamp(l.t, row, l.tk.Block, l.tk.File, l.tk.ContentHash))
	return nil
}

func (l *b496Ledger) events() []string {
	out := []string{}
	for _, r := range l.rows {
		out = append(out, r.Get("event").(string))
	}
	return out
}

func b496Request(t *testing.T, home, repo string, l *b496Ledger, review func(func(ledger.Object) error) (ledger.Object, error)) FixLoopRequest {
	return FixLoopRequest{Home: home, RunID: "r1", RunRootDir: home + "/rr", ContentRoot: repo, Level: "L2", Cfg: map[string]any{}, Key: b496Key,
		ReadRows: func() ([]ledger.Object, error) { return append([]ledger.Object{}, l.rows...), nil }, WriteRow: l.write, Review: review, Now: b496Clock}
}

func b496Clean() ledger.Object {
	return ledger.Object{{Key: "status", Value: "reviewed"}, {Key: "approved", Value: true}, {Key: "findings", Value: []any{}},
		{Key: "sessions", Value: []any{}}, {Key: "engine", Value: "adaptive"}}
}

// A clean round whose file moved since the ticket: the approval row is
// refused ('hash-moved', swallowed by the round) so it is not written; the
// round row and the state anchor are.
func TestPlayRoundHashMoved(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	b496Repo(t, repo)
	l := &b496Ledger{t: t, tk: FixLoopTicket{Block: "B1", File: "a.go", ContentHash: strings.Repeat("f", 64)}}
	r := b496Request(t, home, repo, l, func(func(ledger.Object) error) (ledger.Object, error) { return b496Clean(), nil })
	got, err := playRound(l.tk, r, review.NewFileState("a.go", "L2", false), "full", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got.ApprovalWritten || got.Crashed != nil || got.State.Get("status") != "complete" {
		t.Fatalf("played %+v", got)
	}
	if ev := strings.Join(l.events(), ","); ev != "review.round,review.state" {
		t.Fatalf("rows %s", ev)
	}
	// the real hash: the approval is written
	l2 := &b496Ledger{t: t, tk: FixLoopTicket{Block: "B1", File: "b.go", ContentHash: b496Hash(t, repo, "b.go")}}
	r2 := b496Request(t, home, repo, l2, func(func(ledger.Object) error) (ledger.Object, error) { return b496Clean(), nil })
	got, err = playRound(l2.tk, r2, review.NewFileState("b.go", "L2", false), "full", 0, 0)
	if err != nil || !got.ApprovalWritten {
		t.Fatalf("played %+v %v", got, err)
	}
	if ev := strings.Join(l2.events(), ","); ev != "review.round,review.approved,review.state" {
		t.Fatalf("rows %s", ev)
	}
}

// A throw mid-round: the state is saved as next: retry with the pending
// kind; the reason is the code, else the masked message, else round-failed.
func TestPlayRoundCrashSavesRetry(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&RoundError{Code: "ETIMEDOUT", Message: "slow"}, "ETIMEDOUT"},
		{&RoundError{Message: "key sk-123 died"}, "key [REDACTED] died"},
		{&RoundError{Message: ""}, "round-failed"},
	}
	for _, c := range cases {
		home, repo := t.TempDir(), t.TempDir()
		b496Repo(t, repo)
		l := &b496Ledger{t: t, tk: FixLoopTicket{Block: "B1", File: "a.go", ContentHash: b496Hash(t, repo, "a.go")}}
		r := b496Request(t, home, repo, l, func(func(ledger.Object) error) (ledger.Object, error) { return nil, c.err })
		r.Secrets = []string{"", "sk-123"}
		got, err := playRound(l.tk, r, review.NewFileState("a.go", "L2", false), "full", 2, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got.Crashed == nil || *got.Crashed != c.want || got.Engine != nil {
			t.Fatalf("crashed %v", got.Crashed)
		}
		next, _ := got.State.Get("next").(ledger.Object)
		if next.Get("action") != "retry" || next.Get("reason") != c.want || got.State.Get("pending_kind") != "full" || got.State.Get("round") != 0.0 {
			t.Fatalf("state %s", mustJSON(t, got.State))
		}
		if ev := strings.Join(l.events(), ","); ev != "review.state" || l.rows[0].Get("seq") != 3.0 {
			t.Fatalf("rows %s", mustJSON(t, l.rows))
		}
		loaded, err := LoadState(StateWhere{Home: home, RunID: "r1", Block: "B1", File: "a.go", ContentRoot: repo, Key: b496Key, Rows: l.rows}, false)
		if err != nil || loaded.Status != "ok" || loaded.Seq != 3 {
			t.Fatalf("load %+v %v", loaded, err)
		}
	}
}

// An ErrUnsupported from the review session is not a crash: it is passed up
// and no state is saved.
func TestPlayRoundPassesUnsupported(t *testing.T) {
	home, repo := t.TempDir(), t.TempDir()
	b496Repo(t, repo)
	l := &b496Ledger{t: t, tk: FixLoopTicket{Block: "B1", File: "a.go", ContentHash: b496Hash(t, repo, "a.go")}}
	r := b496Request(t, home, repo, l, func(func(ledger.Object) error) (ledger.Object, error) {
		return nil, unsupported("the rules risk is not ported")
	})
	if _, err := playRound(l.tk, r, review.NewFileState("a.go", "L2", false), "full", 0, 0); !IsUnsupported(err) || len(l.rows) != 0 {
		t.Fatalf("%v %d rows", err, len(l.rows))
	}
}
