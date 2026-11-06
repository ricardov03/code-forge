package autopilot

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
)

func grantFixture(t *testing.T) ledger.Object {
	t.Helper()
	g, err := ledger.DecodeObject([]byte(`{"grant_id":"ap-test","status":"active","until":"2026-01-02T00:00:00.000Z","scopes":["close:routine"],"deny":[],"delegate":"L2","caps":{},"stop_at":0.9,"started_at":"2026-01-01T00:00:00.000Z","stopped_at":null,"expired_at":null,"link":null}`))
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGrantState(t *testing.T) {
	until, _ := time.Parse(time.RFC3339, "2026-01-02T00:00:00Z")
	for _, tc := range []struct {
		status string
		now    time.Time
		want   string
	}{
		{"active", until.Add(-time.Millisecond), "active"},
		{"active", until, "expired"},
		{"active", until.Add(time.Millisecond), "expired"},
		{"stopped", until, "stopped"},
		{"expired", until.Add(-time.Hour), "expired"},
	} {
		g := grantFixture(t)
		g.Set("status", tc.status)
		got, err := GrantState(g, tc.now)
		if err != nil || got != tc.want {
			t.Fatalf("%s at %s: got %s, %v; want %s", tc.status, tc.now, got, err, tc.want)
		}
	}
	for _, mutate := range []func(*ledger.Object){
		func(g *ledger.Object) { g.Set("status", "future") },
		func(g *ledger.Object) { g.Set("until", "invalid") },
		func(g *ledger.Object) { g.Set("scopes", "close:routine") },
		func(g *ledger.Object) { g.Set("future", true) },
	} {
		g := grantFixture(t)
		mutate(&g)
		if _, err := GrantState(g, until); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unknown shape: got %v, want ErrUnsupported", err)
		}
	}
	if got := ActiveGrantMessage("r-test", "until"); got != "run r-test already has an active autopilot grant until until; stop it first with code-forge autopilot stop --run r-test" {
		t.Fatalf("active message: %q", got)
	}
	if got := ExpiredMessage("r-test", "until"); got != "run r-test's autopilot grant already expired at until" {
		t.Fatalf("expired message: %q", got)
	}
}

func TestReadAndRoutineRefusal(t *testing.T) {
	now, _ := time.Parse(time.RFC3339, "2026-01-01T12:00:00Z")
	r := ledger.Object{{Key: "status", Value: "active"}}
	result, err := Read(r, now)
	if err != nil || result.State != "none" || RoutineRefusal(result) != "no-grant" {
		t.Fatalf("no grant: %+v, %v", result, err)
	}
	g := grantFixture(t)
	r.Set("autopilot", g)
	for _, tc := range []struct{ status, want string }{{"active", ""}, {"ended", "run-ended"}} {
		r.Set("status", tc.status)
		result, err = Read(r, now)
		if err != nil || RoutineRefusal(result) != tc.want {
			t.Fatalf("run %s: %+v, %v; want %q", tc.status, result, err, tc.want)
		}
	}
	r.Set("status", "active")
	g.Set("scopes", []any{})
	r.Set("autopilot", g)
	result, _ = Read(r, now)
	if RoutineRefusal(result) != "not-allowed" {
		t.Fatal("empty scopes must refuse close:routine")
	}
	g.Set("deny", []any{"close:routine"})
	r.Set("autopilot", g)
	result, _ = Read(r, now)
	if RoutineRefusal(result) != "denied" {
		t.Fatal("deny must precede missing scope")
	}
	r.Set("autopilot_approvals", []any{ledger.Object{{Key: "status", Value: "active"}}})
	if _, err := Read(r, now); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("pending approval: got %v, want ErrUnsupported", err)
	}
}

func TestCheckExpiry(t *testing.T) {
	for _, status := range []string{"active", "ended"} {
		t.Run(status, func(t *testing.T) {
			home := t.TempDir()
			workspace := t.TempDir()
			run := "r-expiry"
			g := grantFixture(t)
			r := ledger.Object{{Key: "run_id", Value: run}, {Key: "workspace", Value: workspace}, {Key: "project", Value: "expiry"}, {Key: "status", Value: status}, {Key: "autopilot", Value: g}}
			if err := state.SaveRun(home, r); err != nil {
				t.Fatal(err)
			}
			key := make([]byte, 32)
			if err := os.WriteFile(filepath.Join(home, ".code-forge/runs", run+".key"), key, 0600); err != nil {
				t.Fatal(err)
			}
			now, _ := time.Parse(time.RFC3339, "2026-01-02T00:00:00Z")
			var wg sync.WaitGroup
			for i := 0; i < 4; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					result, err := CheckExpiry(home, run, now)
					want := "expired"
					if status == "ended" {
						want = "run-ended"
					}
					if err != nil || result.State != want {
						t.Errorf("expiry: %+v, %v; want %s", result, err, want)
					}
				}()
			}
			wg.Wait()
			rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), "expiry")
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if status == "ended" {
				want = 0
			}
			if len(rows) != want {
				t.Fatalf("expiry rows: got %d, want %d", len(rows), want)
			}
			if want == 0 {
				return
			}
			row := rows[0].(map[string]any)
			verified, err := sign.VerifyRow(row, key)
			if err != nil || !verified.OK {
				t.Fatalf("expiry signature: %+v, %v", verified, err)
			}
			if row["event"] != "autopilot.expire" || row["grant_id"] != "ap-test" || row["until"] != g.Get("until") || row["ts"] != g.Get("until") {
				t.Fatalf("expiry row: %+v", row)
			}
			saved, err := state.ReadRun(home, run)
			if err != nil {
				t.Fatal(err)
			}
			stored := saved.Get("autopilot").(ledger.Object)
			if stored.Get("status") != "expired" || stored.Get("expired_at") != g.Get("until") {
				t.Fatalf("saved expiry: %+v", stored)
			}
		})
	}
}
