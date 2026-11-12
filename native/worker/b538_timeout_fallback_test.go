package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/review"
)

// TestB538TimeoutFallbackForMatchesNode: with a levels.L2 fallback ladder,
// review.second_levels and review.routes in the config, the worker's
// timeout-fallback seam picks Node's model, provider, reason and next config
// (or no fallback where Node had none), instead of refusing the config.
func TestB538TimeoutFallbackForMatchesNode(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "b538_timeout_fallback.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []struct {
			Name string            `json:"name"`
			Cfg  map[string]any    `json:"cfg"`
			From map[string]string `json:"from"`
			Rows []any             `json:"rows"`
		} `json:"cases"`
		Golden map[string]map[string]any `json:"golden"`
	}
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			wantAll := fx.Golden[c.Name]
			got, err := timeoutFallbackFor(c.Cfg, review.TimeoutFallbackInput{FromProvider: c.From["provider"], FromModel: c.From["model"],
				Rows: c.Rows, TicketID: "T1", Level: "L2", Role: "reviewer"}, now)
			if e, ok := wantAll["error"]; ok {
				if err == nil {
					t.Fatalf("Node threw %v, Go returned %+v", e, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("timeoutFallbackFor: %v (Node chose %v)", err, wantAll["output"])
			}
			want, _ := wantAll["output"].(map[string]any)
			if want == nil {
				if got != nil {
					t.Fatalf("got %+v, Node had no fallback", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("no fallback, Node chose %v", want)
			}
			if got.Provider != want["provider"] || got.Model != want["model"] || got.Reason != want["reason"] {
				t.Errorf("got %s/%s %s, Node %v/%v %v", got.Provider, got.Model, got.Reason, want["provider"], want["model"], want["reason"])
			}
			g, _ := json.Marshal(got.Cfg)
			w, _ := json.Marshal(want["cfg"])
			if string(g) != string(w) {
				t.Errorf("cfg %s, Node %s", g, w)
			}
		})
	}
}
