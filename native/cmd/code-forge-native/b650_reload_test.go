package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// B650: run reload's review-only path takes review.on_unavailable and
// review.on_unavailable_backoff_s too, so changing them together with a
// frozen key keeps the frozen key from the run snapshot instead of refusing
// the reload; the new snapshot holds the new chain and the signed run.reload
// row lists the changed and kept keys.

// b650Current is the run record's current config snapshot.
func b650Current(t *testing.T, f b473Fixture) (string, map[string]any) {
	t.Helper()
	var rec struct {
		Config struct {
			Hash      string                    `json:"hash"`
			Snapshots map[string]map[string]any `json:"snapshots"`
			Pins      map[string]string         `json:"pins"`
		} `json:"config"`
		Engine string `json:"engine"`
	}
	if err := json.Unmarshal([]byte(f.record(t)), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Engine != "auto" {
		t.Fatalf("record engine = %q; the frozen engine must stay auto", rec.Engine)
	}
	snap, ok := rec.Config.Snapshots[rec.Config.Hash]
	if !ok {
		t.Fatalf("no snapshot for the current hash %s", rec.Config.Hash)
	}
	return rec.Config.Hash, snap
}

func b650JSON(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func TestB650ReloadOnUnavailableWithFrozenKey(t *testing.T) {
	f := b473Setup(t, "start-snapshot.json", "c7-on-unavailable.yml", nil)
	code, out, errText := runCaptured(t, "run", "reload", "--run", "r-fix")
	b473Expect(t, code, out, errText, 0, "run r-fix: config reloaded · 2 keys changed\n  review.on_unavailable\n  review.on_unavailable_backoff_s\nkept frozen keys from the run snapshot: engine\n"+strings.Replace(b473Tail, "%d", "0", 1), "")
	hash, snap := b650Current(t, f)
	if _, has := snap["engine"]; has {
		t.Fatalf("snapshot took the frozen engine: %s", b650JSON(snap))
	}
	review, _ := snap["review"].(map[string]any)
	if got := b650JSON(review["on_unavailable"]); got != `[{"model":"claude-sonnet-5"},{"level":"L3","model":"claude-opus-5-5","provider":"anthropic"}]` {
		t.Fatalf("snapshot on_unavailable = %s", got)
	}
	if got := b650JSON(review["on_unavailable_backoff_s"]); got != `[0,5]` {
		t.Fatalf("snapshot on_unavailable_backoff_s = %s", got)
	}
	want := `{"run":"r-fix","event":"run.reload","changed_keys":["review.on_unavailable","review.on_unavailable_backoff_s"],"old_hash":"56bc3a37173fa0d2072c24a166edfcf75fcbf1f97570888ef12176639edbe464","new_hash":"` + hash + `","pinned_tickets":0,"kept_keys":["engine"],"tokens_source":null,"cost_source":null}` + "\n"
	if got := f.ledgerRows(t); got != want {
		t.Fatalf("ledger:\n%s\nwant:\n%s", got, want)
	}
	// the same file again has no review change left, so the frozen engine is
	// refused the way a repeated session_timeout_s reload is (Node's rule)
	code, out, errText = runCaptured(t, "run", "reload", "--run", "r-fix")
	b473Expect(t, code, out, errText, 1, "", "run reload: engine cannot change mid-run (the run engine is fixed at run start) — put it back, or end the run and start a new one\n")
	if got := f.ledgerRows(t); got != want {
		t.Fatalf("a refused reload wrote a row:\n%s", got)
	}
}

// Taking the chain away again (frozen engine still changed on disk) is a
// review-only reload too.
func TestB650ReloadRemovesOnUnavailableWithFrozenKey(t *testing.T) {
	f := b473Setup(t, "start-snapshot.json", "c7-on-unavailable.yml", nil)
	if code, _, errText := runCaptured(t, "run", "reload", "--run", "r-fix"); code != 0 {
		t.Fatalf("first reload exit %d: %s", code, errText)
	}
	b650WriteCfg(t, f, "c6-kept.yml")
	code, out, errText := runCaptured(t, "run", "reload", "--run", "r-fix")
	b473Expect(t, code, out, errText, 0, "run r-fix: config reloaded · 3 keys changed\n  review.on_unavailable\n  review.on_unavailable_backoff_s\n  review.session_timeout_s\nkept frozen keys from the run snapshot: engine\n"+strings.Replace(b473Tail, "%d", "0", 1), "")
	_, snap := b650Current(t, f)
	review, _ := snap["review"].(map[string]any)
	if _, has := review["on_unavailable"]; has {
		t.Fatalf("snapshot still holds on_unavailable: %s", b650JSON(snap))
	}
}

// Without a frozen change the ordinary reload takes the chain (it did
// before B650 too).
func TestB650ReloadOnUnavailableOnly(t *testing.T) {
	f := b473Setup(t, "start-snapshot.json", "c8-on-unavailable-only.yml", nil)
	code, out, errText := runCaptured(t, "run", "reload", "--run", "r-fix")
	b473Expect(t, code, out, errText, 0, "run r-fix: config reloaded · 1 key changed\n  review.on_unavailable\n"+strings.Replace(b473Tail, "%d", "0", 1), "")
	_, snap := b650Current(t, f)
	review, _ := snap["review"].(map[string]any)
	if got := b650JSON(review["on_unavailable"]); got != `[{"model":"claude-sonnet-5"}]` {
		t.Fatalf("snapshot on_unavailable = %s", got)
	}
}

func b650WriteCfg(t *testing.T, f b473Fixture, name string) {
	t.Helper()
	writeFileB650(t, f.ws+"/.code-forge.yml", b473Data(t, name))
}

func writeFileB650(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}
