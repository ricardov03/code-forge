package worker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/review"
)

// TestB514TimeoutFallbackForMatchesNode: for every case where Node's
// reviewFile called route.mjs timeoutFallbackFor (B514 goldens, same config
// and ledger rows), the worker's seam picks the same model, provider, reason
// and next config, or no fallback where Node had none.
func TestB514TimeoutFallbackForMatchesNode(t *testing.T) {
	load := func(name string) map[string]any {
		data, err := os.ReadFile(filepath.Join("..", "review", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(data, &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	cfgOf := map[string]map[string]any{}
	for _, raw := range load("b514_engine_fixture.json")["cases"].([]any) {
		c := raw.(map[string]any)
		cfg, _ := c["cfg"].(map[string]any)
		cfgOf[c["name"].(string)] = cfg
	}
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	seen := 0
	for name, raw := range load("b514_engine_golden.json")["cases"].(map[string]any) {
		fb, ok := raw.(map[string]any)["fallback"].(map[string]any)
		if !ok {
			continue
		}
		seen++
		t.Run(name, func(t *testing.T) {
			in := fb["input"].(map[string]any)
			from := in["from"].(map[string]any)
			rows, _ := in["rows"].([]any)
			cfg := cfgOf[name]
			if cfg == nil {
				t.Fatal("case has no config")
			}
			got, err := timeoutFallbackFor(cfg, review.TimeoutFallbackInput{FromProvider: from["provider"], FromModel: from["model"],
				Rows: rows, TicketID: in["ticketId"].(string), Level: "L2", Role: "reviewer"}, now)
			if err != nil {
				t.Fatalf("timeoutFallbackFor: %v (Node chose %v)", err, fb["output"])
			}
			want, _ := fb["output"].(map[string]any)
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
	if seen < 10 {
		t.Fatalf("only %d golden cases reached timeoutFallbackFor", seen)
	}
}
