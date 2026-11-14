package worker

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
)

// B629 / B321: a route that timed out on 2 tickets in a row in this run is
// benched (skipped by later tickets) until it succeeds on another ticket or
// the worker is reattached; a review.on_unavailable chain runs end to end
// through the worker with signed rows.

var b629Hunk = regexp.MustCompile(`(?m)^(@@ -[0-9,]+ \+[0-9,]+ @@)`)

// b629Env drives tickets for a.txt through RunTicket with a fake review
// session (the ReviewSession seam): a model in slow times out at once, any
// other answers with a passing review. calls lists the models in order.
type b629Env struct {
	fx    *b488Fix
	w     *TicketRunner
	mu    sync.Mutex
	slow  map[string]bool
	calls []string
	n     int
}

func b629Setup(t *testing.T, cfg string) *b629Env {
	t.Helper()
	clock := fixedNow
	fx, w, _ := b497Fixture(t, &clock)
	w.Cfg = b492Cfg(t, cfg)
	e := &b629Env{fx: fx, w: w, slow: map[string]bool{}}
	w.Sleep = func(d time.Duration) { t.Errorf("slept %v", d) } // no backoff or retry wait here
	w.ReviewSession = func(o review.SpawnOpts) (ledger.Object, error) {
		levels, _ := o.Cfg["levels"].(map[string]any)
		l, _ := levels[o.Level].(map[string]any)
		model, _ := l["model"].(string)
		provider, _ := l["provider"].(string)
		e.mu.Lock()
		e.calls = append(e.calls, model)
		slow := e.slow[model]
		e.mu.Unlock()
		if slow {
			return ledger.Object{{Key: "status", Value: "timeout"}, {Key: "provider", Value: provider}, {Key: "model", Value: model},
				{Key: "reason", Value: "killed after 3000 ms"}, {Key: "answer", Value: nil}, {Key: "duration_ms", Value: 3000.0}}, nil
		}
		text, err := os.ReadFile(o.PromptPath)
		if err != nil {
			return nil, err
		}
		heads := []any{}
		seen := map[string]bool{}
		for _, m := range b629Hunk.FindAllStringSubmatch(string(text), -1) {
			if !seen[m[1]] {
				seen[m[1]] = true
				heads = append(heads, m[1])
			}
		}
		answer := ledger.Object{{Key: "passed", Value: true}, {Key: "summary", Value: "fine"}, {Key: "reviewed_hunks", Value: heads}, {Key: "findings", Value: []any{}}}
		return ledger.Object{{Key: "status", Value: "ok"}, {Key: "provider", Value: provider}, {Key: "model", Value: model}, {Key: "fallback_step", Value: 0.0},
			{Key: "exit_code", Value: 0.0}, {Key: "answer", Value: answer},
			{Key: "usage", Value: ledger.Object{{Key: "tokens_in", Value: 1000.0}, {Key: "tokens_out", Value: 300.0}}}}, nil
	}
	return e
}

// next runs one ticket for new a.txt content with the given slow models and
// returns the models it called and its result file.
func (e *b629Env) next(t *testing.T, slow ...string) ([]string, string) {
	t.Helper()
	e.mu.Lock()
	e.slow = map[string]bool{}
	for _, m := range slow {
		e.slow[m] = true
	}
	before := len(e.calls)
	e.mu.Unlock()
	e.n++
	content := "hello\nworld\n" + strings.Repeat("more\n", e.n)
	if err := os.WriteFile(filepath.Join(e.fx.root, "a.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	id := e.fx.ticket(t, "r1", "B1", "a.txt", sha(content))
	n, poison, err := e.w.RunTicket(id)
	if err != nil || n != 1 || poison {
		t.Fatalf("RunTicket = %d, %v, %v; want the ticket recorded in Go", n, poison, err)
	}
	e.mu.Lock()
	calls := append([]string{}, e.calls[before:]...)
	e.mu.Unlock()
	return calls, readFile(t, filepath.Join(e.fx.root, ".code-forge", "reviews", "r1", id+".json"))
}

// rows is every signed ledger row of one event (an unsigned one fails).
func (e *b629Env) rows(t *testing.T, event string) []map[string]any {
	t.Helper()
	out := (&b627Env{fx: e.fx}).rows(t, event)
	for _, r := range out {
		if res, err := sign.VerifyRow(r, e.fx.key); err != nil || !res.OK {
			t.Fatalf("%s row is not signed by the run key: %v", event, r)
		}
	}
	return out
}

const b629Ladder = `{"levels":{"L2":{"model":"m-a","provider":"anthropic","fallback":[{"model":"m-b","provider":"anthropic"}]}},"review":{"adaptive":{"enabled":false}}}`

// TestB629RouteBenchedAfterTwoTimeouts: m-a times out on 2 tickets in a row
// (the ladder's m-b answers each time); the second writes
// review.route_benched, and the third ticket never calls m-a.
func TestB629RouteBenchedAfterTwoTimeouts(t *testing.T) {
	e := b629Setup(t, b629Ladder)
	if calls, res := e.next(t, "m-a"); strings.Join(calls, ",") != "m-a,m-b" || !strings.Contains(res, `"approved":true`) {
		t.Fatalf("ticket 1 calls %v result %s", calls, res)
	}
	if got := e.rows(t, "review.route_benched"); len(got) != 0 {
		t.Fatalf("benched after one timeout: %v", got)
	}
	e.next(t, "m-a")
	timeouts := e.rows(t, "review.route_timeout")
	if len(timeouts) != 2 || timeouts[1]["streak"] != 2.0 || timeouts[1]["model"] != "m-a" || timeouts[1]["level"] != "L2" || timeouts[1]["run"] != "r1" {
		t.Fatalf("route_timeout rows %v", timeouts)
	}
	benched := e.rows(t, "review.route_benched")
	if len(benched) != 1 || benched[0]["provider"] != "anthropic" || benched[0]["model"] != "m-a" || benched[0]["timeouts"] != 2.0 ||
		benched[0]["ticket"] == nil || benched[0]["file"] != "a.txt" {
		t.Fatalf("route_benched rows %v", benched)
	}
	if calls, res := e.next(t); strings.Join(calls, ",") != "m-b" || !strings.Contains(res, `"approved":true`) {
		t.Fatalf("ticket 3 calls %v (want only m-b: m-a is benched) result %s", calls, res)
	}
	if calls, _ := e.next(t); strings.Join(calls, ",") != "m-b" {
		t.Fatalf("ticket 4 calls %v: the bench lasts the run", calls)
	}
	if got := e.rows(t, "review.route_reset"); len(got) != 0 {
		t.Fatalf("reset rows %v with no m-a success", got)
	}
}

// TestB629TimeoutsNotInARowDoNotBench: a success between two timeouts resets
// the streak (review.route_reset), so no bench.
func TestB629TimeoutsNotInARowDoNotBench(t *testing.T) {
	e := b629Setup(t, b629Ladder)
	e.next(t, "m-a")
	e.next(t)
	resets := e.rows(t, "review.route_reset")
	if len(resets) != 1 || resets[0]["model"] != "m-a" || resets[0]["reason"] != "success" || resets[0]["was_benched"] != false {
		t.Fatalf("route_reset rows %v", resets)
	}
	e.next(t, "m-a")
	if got := e.rows(t, "review.route_benched"); len(got) != 0 {
		t.Fatalf("benched with a success between the timeouts: %v", got)
	}
	if calls, _ := e.next(t); calls[0] != "m-a" {
		t.Fatalf("calls %v, want m-a still first", calls)
	}
}

// TestB629BenchClearedAfterSuccess: a benched route that is the level's only
// route still runs as the last resort; its success on another ticket clears
// the bench (review.route_reset was_benched true), so a later timeout starts
// a fresh streak.
func TestB629BenchClearedAfterSuccess(t *testing.T) {
	e := b629Setup(t, `{"levels":{"L2":{"model":"m-a","provider":"anthropic"}},"review":{"adaptive":{"enabled":false}}}`)
	e.next(t, "m-a")
	e.next(t, "m-a")
	if got := e.rows(t, "review.route_benched"); len(got) != 1 {
		t.Fatalf("benched rows %v", got)
	}
	if calls, res := e.next(t); len(calls) != 1 || calls[0] != "m-a" || !strings.Contains(res, `"approved":true`) {
		t.Fatalf("ticket 3 calls %v result %s", calls, res)
	}
	resets := e.rows(t, "review.route_reset")
	if len(resets) != 1 || resets[0]["was_benched"] != true {
		t.Fatalf("route_reset rows %v", resets)
	}
	e.next(t, "m-a")
	timeouts := e.rows(t, "review.route_timeout")
	if last := timeouts[len(timeouts)-1]; last["streak"] != 1.0 {
		t.Fatalf("streak after the reset %v, want 1", last["streak"])
	}
	if got := e.rows(t, "review.route_benched"); len(got) != 1 {
		t.Fatalf("benched again after one timeout: %v", got)
	}
}

// TestB629BenchClearedByReattach: run start --reattach's signed
// worker.reattach row clears every bench; a forged one does not.
func TestB629BenchClearedByReattach(t *testing.T) {
	e := b629Setup(t, b629Ladder)
	e.next(t, "m-a")
	e.next(t, "m-a")
	e.fx.appendLedger(t, `{"run":"r1","event":"worker.reattach","pid":1,"ts":"2026-11-11T10:00:00.000Z","mac":"forged"}`+"\n")
	if calls, _ := e.next(t); strings.Join(calls, ",") != "m-b" {
		t.Fatalf("calls %v: a forged reattach row cleared the bench", calls)
	}
	if _, err := state.WriteSigned(e.fx.home, "r1", ledger.Object{{Key: "event", Value: "worker.reattach"}, {Key: "pid", Value: 1.0}}); err != nil {
		t.Fatal(err)
	}
	if calls, _ := e.next(t); strings.Join(calls, ",") != "m-a" {
		t.Fatalf("calls %v, want m-a back after the reattach", calls)
	}
}

// TestB629BenchedRouteLeavesChain: a benched route is also left out of
// review.on_unavailable, and a level whose every model is benched starts
// from the first chain route that is not.
func TestB629BenchedRouteLeavesChain(t *testing.T) {
	e := b629Setup(t, `{"levels":{"L2":{"model":"m-a","provider":"anthropic"}},"review":{"adaptive":{"enabled":false},"on_unavailable_backoff_s":[],"on_unavailable":[{"model":"m-c","provider":"anthropic"},{"model":"m-d","provider":"anthropic"}]}}`)
	// m-a and m-c time out on two tickets; m-d answers each time
	for i := 0; i < 2; i++ {
		if calls, _ := e.next(t, "m-a", "m-c"); strings.Join(calls, ",") != "m-a,m-a,m-c,m-d" {
			t.Fatalf("ticket %d calls %v", i+1, calls)
		}
	}
	if got := e.rows(t, "review.route_benched"); len(got) != 2 {
		t.Fatalf("benched rows %v, want m-a and m-c", got)
	}
	if calls, res := e.next(t); strings.Join(calls, ",") != "m-d" || !strings.Contains(res, `"approved":true`) {
		t.Fatalf("ticket 3 calls %v (want m-d only) result %s", calls, res)
	}
}

// TestB629ChainThroughWorkerFakeCLI: the fake claude CLI fails for m-a
// (exit 1); the worker walks review.on_unavailable to m-c, writes one signed
// review.unavailable_fallback row with the ticket and file, and stores an
// approved review.
func TestB629ChainThroughWorkerFakeCLI(t *testing.T) {
	clock := fixedNow
	fx, w, _ := b497Fixture(t, &clock)
	w.Cfg = b492Cfg(t, `{"levels":{"L2":{"model":"m-a","provider":"anthropic"}},"review":{"adaptive":{"enabled":false},"on_unavailable":[{"model":"m-c"}]}}`)
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "claude")
	script := strings.Replace(b497FakeClaude, "in=$(cat)\n", `in=$(cat)
case " $* " in *" m-a "*) echo m-a >> `+calls+`; echo "provider down" >&2; exit 1 ;; esac
echo other >> `+calls+`
`, 1)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	w.SessionDeps.Bins = map[string]string{"claude": bin}
	w.Sleep = func(d time.Duration) { t.Errorf("the first chain step waited %v", d) }
	e := &b629Env{fx: fx, w: w}
	if err := os.WriteFile(filepath.Join(fx.root, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := fx.ticket(t, "r1", "B1", "a.txt", sha("hello\nworld\n"))
	if n, poison, err := w.RunTicket(id); err != nil || n != 1 || poison {
		t.Fatalf("RunTicket = %d, %v, %v", n, poison, err)
	}
	if got := readFile(t, calls); got != "m-a\nother\n" {
		t.Fatalf("CLI calls %q, want m-a then the chain's m-c; rows %v", got, e.rows(t, "review.unavailable_fallback"))
	}
	res := readFile(t, filepath.Join(fx.root, ".code-forge", "reviews", "r1", id+".json"))
	if !strings.Contains(res, `"approved":true`) || !strings.Contains(res, `"review.on_unavailable"`) {
		t.Fatalf("result %s", res)
	}
	steps := e.rows(t, "review.unavailable_fallback")
	if len(steps) != 1 {
		t.Fatalf("unavailable_fallback rows %v", steps)
	}
	s := steps[0]
	if s["ticket"] != id || s["file"] != "a.txt" || s["block"] != "B1" || s["from_model"] != "m-a" || s["to_model"] != "m-c" ||
		s["to_provider"] != "anthropic" || s["reason"] != "exit" || s["attempt"] != 1.0 || s["waited_ms"] != 0.0 {
		t.Fatalf("row %v", s)
	}
}
