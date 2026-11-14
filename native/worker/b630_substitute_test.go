package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
	"github.com/ricardov03/code-forge/native/state"
)

// B630 (issue #37 C3/C4): a review left unavailable after the built-in
// recovery and the review.on_unavailable chain runs one substitute review on
// another provider when the active autopilot grant has scope
// review:substitute; without the scope it asks the owner instead.

// b630Chain: L2 m-a (anthropic) with the chain's m-c (anthropic), then L3 on
// two other providers: openai (closed-book refused for a reviewer) and xai.
const b630Chain = `{"levels":{"L2":{"model":"m-a","provider":"anthropic"},"L3":{"model":"m-o","provider":"openai","fallback":[{"model":"m-x","provider":"xai"}]}},` +
	`"review":{"adaptive":{"enabled":false},"on_unavailable_backoff_s":[],"on_unavailable":[{"model":"m-c","provider":"anthropic"}]}}`

const b630Base = "e64a56881e75eab80bd5dcda472ab5182c0b9604"

// b630Grant writes the run record with an autopilot grant of scopes (no
// grant when scopes is nil).
func b630Grant(t *testing.T, e *b629Env, scopes ...string) {
	t.Helper()
	ap := ""
	if scopes != nil {
		list, _ := json.Marshal(scopes)
		ap = `,"autopilot":{"grant_id":"g-1","status":"active","scopes":` + string(list) + `,"deny":[],"delegate":"L2","until":"2999-01-01T00:00:00.000Z","caps":{},"stop_at":0.8,"started_at":"2019-01-01T00:00:00.000Z"}`
	}
	e.fx.blocksRecord(t, `,"blocks":{"B1":{"base_sha":"`+b630Base+`","owned_files":["a.txt"]}}`+ap)
}

// b630Critical makes model answer with one critical finding.
func b630Critical(e *b629Env, model string) {
	inner := e.w.ReviewSession
	e.w.ReviewSession = func(o review.SpawnOpts) (ledger.Object, error) {
		res, err := inner(o)
		if err != nil || res == nil || res.Get("model") != model || res.Get("status") != "ok" {
			return res, err
		}
		answer := res.Get("answer").(ledger.Object).Clone()
		answer.Set("passed", false)
		answer.Set("findings", []any{ledger.Object{{Key: "id", Value: "F1"}, {Key: "file", Value: "a.txt"}, {Key: "line_start", Value: 1.0}, {Key: "line_end", Value: 1.0},
			{Key: "severity", Value: "critical"}, {Key: "category", Value: "correctness"}, {Key: "claim", Value: "a critical bug"}, {Key: "evidence", Value: "line 1"}, {Key: "fix", Value: "fix it"}}})
		res = res.Clone()
		res.Set("answer", answer)
		return res, nil
	}
}

func b630Decisions(t *testing.T, e *b629Env) []map[string]any {
	t.Helper()
	return e.rows(t, "autopilot.decision")
}

// TestB630SubstituteUnderScope: m-a and the chain's m-c (both anthropic) time
// out; the grant has review:substitute, so one substitute review runs on xai
// (never anthropic, never the closed-book-refused openai). The stored result,
// the review.result, review.round and review.approved rows say source
// substitute, and one signed acted autopilot.decision row names scope, block,
// file, provider, model and reason.
func TestB630SubstituteUnderScope(t *testing.T) {
	e := b629Setup(t, b630Chain)
	b630Grant(t, e, "review:substitute")
	calls, res := e.next(t, "m-a", "m-c")
	if strings.Join(calls, ",") != "m-a,m-a,m-c,m-x" {
		t.Fatalf("calls %v, want the chain then the substitute on m-x", calls)
	}
	if !strings.Contains(res, `"approved":true`) || !strings.Contains(res, `"source":"substitute"`) || !strings.Contains(res, `"provider":"xai"`) {
		t.Fatalf("result %s", res)
	}
	d := b630Decisions(t, e)
	if len(d) != 1 {
		t.Fatalf("decision rows %v", d)
	}
	r := d[0]
	subject, _ := r["subject"].(map[string]any)
	if r["scope"] != "review:substitute" || r["block"] != "B1" || r["file"] != "a.txt" || r["acted"] != true || r["to_owner"] != false ||
		r["decision"] != "substitute" || r["provider"] != "xai" || r["model"] != "m-x" || r["grant_id"] != "g-1" || r["run"] != "r1" ||
		r["content_hash"] == nil || subject["file"] != "a.txt" || !strings.Contains(r["reason"].(string), "anthropic") || !strings.Contains(r["reason"].(string), "xai/m-x") {
		t.Fatalf("decision row %v", r)
	}
	for _, event := range []string{"review.result", "review.round", "review.approved"} {
		rows := e.rows(t, event)
		if len(rows) != 1 || rows[0]["source"] != "substitute" {
			t.Fatalf("%s rows %v, want one with source substitute", event, rows)
		}
	}
	// the next ticket with the provider back is a normal review
	calls, res = e.next(t)
	if strings.Join(calls, ",") != "m-a" || strings.Contains(res, `"source":"substitute"`) || !strings.Contains(res, `"approved":true`) {
		t.Fatalf("ticket 2 calls %v result %s", calls, res)
	}
	if got := e.rows(t, "review.approved"); len(got) != 2 || got[1]["source"] != nil {
		t.Fatalf("approved rows %v", got)
	}
	if got := b630Decisions(t, e); len(got) != 1 {
		t.Fatalf("a normal review wrote decision rows %v", got)
	}
}

// TestB630SubstituteCriticalNotApproved: a substitute that finds a critical
// writes no review.approved row; the file stays open with the finding.
func TestB630SubstituteCriticalNotApproved(t *testing.T) {
	e := b629Setup(t, b630Chain)
	b630Grant(t, e, "review:substitute")
	b630Critical(e, "m-x")
	calls, res := e.next(t, "m-a", "m-c")
	if calls[len(calls)-1] != "m-x" || strings.Contains(res, `"approved":true`) || !strings.Contains(res, `"critical"`) {
		t.Fatalf("calls %v result %s", calls, res)
	}
	if got := e.rows(t, "review.approved"); len(got) != 0 {
		t.Fatalf("approved rows %v after a critical", got)
	}
	if got := b630Decisions(t, e); len(got) != 1 || got[0]["acted"] != true {
		t.Fatalf("decision rows %v", got)
	}
}

// TestB630NoScopeAsksOwner: an active grant without review:substitute runs
// no substitute; one signed autopilot.decision row asks the owner and names
// the owner command, once per file content.
func TestB630NoScopeAsksOwner(t *testing.T) {
	e := b629Setup(t, b630Chain)
	b630Grant(t, e, "close:routine")
	calls, res := e.next(t, "m-a", "m-c")
	if strings.Join(calls, ",") != "m-a,m-a,m-c" || strings.Contains(res, `"source":"substitute"`) || strings.Contains(res, `"approved":true`) {
		t.Fatalf("calls %v result %s", calls, res)
	}
	d := b630Decisions(t, e)
	if len(d) != 1 {
		t.Fatalf("decision rows %v", d)
	}
	r := d[0]
	cmd, _ := r["owner_command"].(string)
	if r["acted"] != false || r["to_owner"] != true || r["decision"] != nil || r["scope"] != "review:substitute" || r["file"] != "a.txt" ||
		!strings.Contains(cmd, "code-forge autopilot start --run r1 --until <time> --delegate L2 --allow review:substitute") ||
		!strings.Contains(r["owner_reason"].(string), "not-allowed") || !strings.Contains(r["owner_reason"].(string), "--allow review:substitute") {
		t.Fatalf("owner row %v", r)
	}
	if got := e.rows(t, "review.approved"); len(got) != 0 {
		t.Fatalf("approved rows %v", got)
	}
}

// TestB630NoGrantUnchanged: with no autopilot grant nothing changes: no
// substitute, no decision row.
func TestB630NoGrantUnchanged(t *testing.T) {
	e := b629Setup(t, b630Chain)
	b630Grant(t, e)
	calls, _ := e.next(t, "m-a", "m-c")
	if strings.Join(calls, ",") != "m-a,m-a,m-c" {
		t.Fatalf("calls %v", calls)
	}
	if got := b630Decisions(t, e); len(got) != 0 {
		t.Fatalf("decision rows %v", got)
	}
}

// TestB630NoOtherProvider: every route is on the failed provider (or a
// closed-book-refused one): no substitute runs, and the owner is asked.
func TestB630NoOtherProvider(t *testing.T) {
	e := b629Setup(t, `{"levels":{"L2":{"model":"m-a","provider":"anthropic"},"L3":{"model":"m-o","provider":"openai","fallback":[{"model":"m-z","provider":" Anthropic "}]}},`+
		`"review":{"adaptive":{"enabled":false},"on_unavailable_backoff_s":[],"on_unavailable":[{"model":"m-c","provider":"anthropic"}]}}`)
	b630Grant(t, e, "review:substitute")
	calls, _ := e.next(t, "m-a", "m-c")
	if strings.Join(calls, ",") != "m-a,m-a,m-c" {
		t.Fatalf("calls %v: a substitute ran on the failed or a closed-book provider", calls)
	}
	d := b630Decisions(t, e)
	if len(d) != 1 || d[0]["acted"] != false || !strings.Contains(d[0]["owner_reason"].(string), "no runnable closed-book reviewer") {
		t.Fatalf("decision rows %v", d)
	}
}

// TestB630PickSubstitute: the pick skips failed providers (any case and
// spacing), openai for a closed-book reviewer (unless open book is allowed),
// and routes the spawn check refuses.
func TestB630PickSubstitute(t *testing.T) {
	cfg := b492Cfg(t, b630Chain)
	if r, ok := review.PickSubstitute(cfg, []string{" ANTHROPIC"}, nil); !ok || r.Provider != "xai" || r.Model != "m-x" {
		t.Fatalf("pick %v %v", r, ok)
	}
	if _, ok := review.PickSubstitute(cfg, []string{"anthropic", "xai"}, nil); ok {
		t.Fatal("picked a failed provider")
	}
	refuse := func(c map[string]any, level, role string) error {
		l, _ := c["levels"].(map[string]any)[level].(map[string]any)
		if l["provider"] == "xai" {
			return errB630
		}
		return nil
	}
	if _, ok := review.PickSubstitute(cfg, []string{"anthropic"}, refuse); ok {
		t.Fatal("picked a route the spawn check refuses")
	}
	open := b492Cfg(t, strings.Replace(b630Chain, `"adaptive"`, `"allow_open_book_codex":true,"adaptive"`, 1))
	if r, ok := review.PickSubstitute(open, []string{"anthropic"}, nil); !ok || r.Provider != "openai" {
		t.Fatalf("open book pick %v %v", r, ok)
	}
}

var errB630 = &b630Err{}

type b630Err struct{}

func (*b630Err) Error() string { return "refused" }

// TestB630ReopenAfterSubstitute: block reopen --file on a file whose
// substitute review found a critical still asks a normal review: the
// signed review.reopened row sends the file's next ticket down the reopen
// path (since B623 a recheck round served in Go, as for any reopened file)
// with no substitute and no new decision row, even with the provider still
// down and the scope granted.
func TestB630ReopenAfterSubstitute(t *testing.T) {
	e := b629Setup(t, b630Chain)
	b630Grant(t, e, "review:substitute")
	b630Critical(e, "m-x")
	e.next(t, "m-a", "m-c")
	if _, err := state.WriteSigned(e.fx.home, "r1", ledger.Object{{Key: "event", Value: "review.reopened"}, {Key: "block", Value: "B1"}, {Key: "file", Value: "a.txt"},
		{Key: "from", Value: "open"}, {Key: "reason", Value: "a.txt:1 is fine"}, {Key: "by", Value: "human"}}); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	e.slow = map[string]bool{"m-a": true, "m-c": true}
	before := len(e.calls)
	e.mu.Unlock()
	content := "hello\nworld\nfixed\n"
	if err := os.WriteFile(filepath.Join(e.fx.root, "a.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	id := e.fx.ticket(t, "r1", "B1", "a.txt", sha(content))
	n, poison, err := e.w.RunTicket(id)
	if err != nil || n != 1 || poison {
		t.Fatalf("RunTicket = %d, %v, %v; want the reopened ticket served in Go (B623)", n, poison, err)
	}
	res := readFile(t, filepath.Join(e.fx.root, ".code-forge", "reviews", "r1", id+".json"))
	if !strings.Contains(res, `"kind":"recheck"`) || strings.Contains(res, `"source":"substitute"`) {
		t.Fatalf("result %s, want the normal recheck round of the reopened file, not a substitute", res)
	}
	e.mu.Lock()
	after := append([]string{}, e.calls[before:]...)
	e.mu.Unlock()
	if len(after) != 1 || after[0] == "m-x" {
		t.Fatalf("calls %v after the reopen, want one recheck session and no substitute", after)
	}
	if got := b630Decisions(t, e); len(got) != 1 {
		t.Fatalf("decision rows %v, want only the first substitute's", got)
	}
}
