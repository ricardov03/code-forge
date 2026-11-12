package worker

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestB545LadderNoProviderMatchesNode: an L2 fallback ladder entry with no
// provider key routes in Go like Node's routeTicket (route.mjs
// adaptiveTicket): it is dropped when Node's second provider is undefined,
// kept (its routed entry has no provider) when review.multimodel names a
// second provider or sets second_provider to null, and a ticket's earlier
// review.route row still picks its reviewer. Rows, timeout and routed config
// match Node (golden: Node's routeTicket on the same fixture). The one
// narrow case Go keeps with Node: the chosen reviewer itself has no
// provider; Go answers errRouteUnsure before any write.
func TestB545LadderNoProviderMatchesNode(t *testing.T) {
	read := func(name string, v any) {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(data, v); err != nil {
			t.Fatal(err)
		}
	}
	var fx struct {
		Cases []struct {
			Name string         `json:"name"`
			Cfg  map[string]any `json:"cfg"`
			Rows []any          `json:"rows"`
			Go   string         `json:"go"`
		} `json:"cases"`
	}
	var golden struct {
		Cases map[string]struct {
			TimeoutS float64  `json:"timeoutS"`
			Rows     []string `json:"rows"`
			Cfg      string   `json:"cfg"`
			Error    string   `json:"error"`
		} `json:"cases"`
	}
	read("b545_ladder_fixture.json", &fx)
	read("b545_ladder_golden.json", &golden)
	sorted := func(text string) string {
		var v any
		if err := json.Unmarshal([]byte(text), &v); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(v)
		return string(b)
	}
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			want := golden.Cases[c.Name]
			if want.Error != "" {
				t.Fatalf("Node threw %s", want.Error)
			}
			got, err := adaptiveRoutes(c.Cfg, 10, false, 300, c.Rows, "t1", "r1", now)
			if c.Go == "unsure" {
				if !errors.Is(err, errRouteUnsure) {
					t.Fatalf("a chosen reviewer with no provider: got %v, want errRouteUnsure (kept with Node)", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Go refused a route Node takes: %v", err)
			}
			if got.timeoutS != want.TimeoutS {
				t.Errorf("timeout %v, Node %v", got.timeoutS, want.TimeoutS)
			}
			if len(got.rows) != len(want.Rows) {
				t.Fatalf("rows %d, Node %d", len(got.rows), len(want.Rows))
			}
			for i, r := range got.rows {
				b, err := json.Marshal(r)
				if err != nil {
					t.Fatal(err)
				}
				if string(b) != want.Rows[i] {
					t.Errorf("row %d\ngot  %s\nNode %s", i, b, want.Rows[i])
				}
			}
			b, err := json.Marshal(got.cfg)
			if err != nil {
				t.Fatal(err)
			}
			if g, w := string(b), sorted(want.Cfg); g != w {
				t.Errorf("routed cfg\ngot  %s\nNode %s", g, w)
			}
		})
	}
}
