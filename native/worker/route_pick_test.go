package worker

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
)

// testdata/b495_pick_dates.golden is Node's real pickReviewer (src/review/
// model-stats.mjs) over fixed stats and ticket ids, and Date.parse over
// fixed strings (scratch tool b495tools/pick_golden.mjs).
type pickGolden struct {
	Picks []struct {
		Name, TicketID, Model, Reason string
		Explore                       float64
	}
	Dates []struct {
		S  string
		Ms *float64
	}
}

func loadPickGolden(t *testing.T) pickGolden {
	t.Helper()
	data, err := os.ReadFile("testdata/b495_pick_dates.golden")
	if err != nil {
		t.Fatal(err)
	}
	var g pickGolden
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

func stat(n int, med *float64, av, w float64) *modelStat {
	return &modelStat{sessions: n, median: med, availability: av, waived: w}
}

func f(v float64) *float64 { return &v }

func TestPickReviewerExploreMatchesNode(t *testing.T) {
	g := loadPickGolden(t)
	cands := []candidate{{model: "a", provider: "p"}, {model: "b", provider: "p"}, {model: "c", provider: "q"}}
	sets := map[string]map[string]*modelStat{
		"c-unmeasured":     {"a": stat(12, f(30), 1, 0), "b": stat(15, f(10), 0.9, 0.1), "c": stat(2, f(5), 1, 0)},
		"all-equal":        {"a": stat(12, f(30), 1, 0), "b": stat(12, f(10), 0.9, 0.1), "c": stat(12, f(5), 1, 0)},
		"two-least":        {"a": stat(20, f(30), 1, 0), "b": stat(3, nil, 1, 0), "c": stat(3, f(5), 1, 0)},
		"first-unmeasured": {"a": stat(1, nil, 1, 0), "b": stat(15, f(10), 0.9, 0.1), "c": stat(30, f(5), 1, 0)},
		"none-measured":    {"a": stat(1, f(3), 1, 0)},
	}
	if len(g.Picks) != 240 {
		t.Fatalf("golden has %d picks", len(g.Picks))
	}
	explored := 0
	for _, p := range g.Picks {
		got, reason := pickReviewer(cands, sets[p.Name], adaptiveOpts{minSessions: 10, explore: p.Explore, enabled: true}, p.TicketID)
		if got.model != p.Model || reason != p.Reason {
			t.Fatalf("%s explore %v %s: go %s/%s, node %s/%s", p.Name, p.Explore, p.TicketID, got.model, reason, p.Model, p.Reason)
		}
		if reason == "explore" {
			explored++
		}
	}
	if explored == 0 {
		t.Fatal("no explore pick in the golden")
	}
	if c, r := pickReviewer(cands, sets["all-equal"], adaptiveOpts{enabled: false}, "x"); c.model != "a" || r != "disabled" {
		t.Fatalf("disabled: %v %s", c, r)
	}
}

// dateParse is strict: where it answers, it is Node's Date.parse; strings it
// is not sure of (Node's lenient forms) are errRouteUnsure, never a guess.
func TestDateParseStrictAgainstNode(t *testing.T) {
	g := loadPickGolden(t)
	answered, unsure := 0, 0
	for _, d := range g.Dates {
		ms, ok, err := dateParse(d.S)
		switch {
		case errors.Is(err, errRouteUnsure):
			unsure++
		case err != nil:
			t.Fatalf("%q: %v", d.S, err)
		case ok:
			answered++
			if d.Ms == nil || *d.Ms != ms {
				t.Fatalf("%q: go %v, node %v", d.S, ms, d.Ms)
			}
		default:
			if d.Ms != nil {
				t.Fatalf("%q: go NaN, node %v", d.S, *d.Ms)
			}
		}
	}
	// Node takes 2026-02-30, 24:00 and non-Z forms; Go must refuse, not answer.
	for _, s := range []string{"2026-02-30T00:00:00Z", "2026-04-31T00:00:00Z", "2026-11-11T24:00:00Z", "2026-11-11", "2026-11-11T09:00:00+01:00", "Nov 11 2026", "2026-13-01T00:00:00Z", "2026-11-11T23:60:00Z"} {
		if _, _, err := dateParse(s); !errors.Is(err, errRouteUnsure) {
			t.Fatalf("%q: want unsure, got %v", s, err)
		}
	}
	for _, v := range []any{nil, true, map[string]any{}, ""} {
		if _, ok, err := dateParse(v); ok || err != nil {
			t.Fatalf("%v: want NaN", v)
		}
	}
	if _, _, err := dateParse(1.5); !errors.Is(err, errRouteUnsure) {
		t.Fatalf("number: %v", err)
	}
	if answered < 6 || unsure < 8 {
		t.Fatalf("answered %d unsure %d", answered, unsure)
	}
}
