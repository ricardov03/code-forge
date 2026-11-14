package gatecheck

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"testing"
)

// B649: an autopilot acceptance under an until-done grant (until null,
// until_done true) is inside the grant until the grant ends; a signed
// autopilot.extend row (B634) moves a grant's clock end. Without either the
// window is read as before.
func TestB649AutopilotAcceptanceUntilDoneGrant(t *testing.T) {
	key := []byte(strings.Repeat("k", 32))
	transcript := "git clean -fd\n"
	hits, ok := Transcript(transcript)
	if !ok || len(hits) != 1 {
		t.Fatalf("one transcript hit: %v %v", hits, ok)
	}
	sum := sha256.Sum256([]byte("git clean -fd"))
	accept := Row{"event": "rule_break.accepted", "rule": "git-clean", "line": 1, "line_sha256": hex.EncodeToString(sum[:]), "reason": "scratch dir", "by": "autopilot", "grant_id": "ag-1", "ts": "2026-11-11T13:00:00.000Z"}
	grant := func(extra Row) Row {
		g := Row{"event": "autopilot.grant", "grant_id": "ag-1", "scopes": []any{}, "deny": []any{}, "delegate": "L2", "ts": "2026-11-11T09:00:00.000Z"}
		for k, v := range extra {
			g[k] = v
		}
		return g
	}
	extend := func(ts, until string) Row {
		return Row{"event": "autopilot.extend", "grant_id": "ag-1", "from": "2026-11-11T12:00:00.000Z", "until": until, "ts": ts}
	}
	cases := []struct {
		name    string
		rows    []Row
		cleared bool
	}{
		{"until-done grant", []Row{grant(Row{"until": nil, "until_done": true})}, true},
		{"until-done grant past its deadline", []Row{grant(Row{"until": "2026-11-11T12:00:00.000Z", "until_done": true})}, false},
		{"null until without until_done", []Row{grant(Row{"until": nil})}, false},
		{"timed grant past its end", []Row{grant(Row{"until": "2026-11-11T12:00:00.000Z"})}, false},
		{"timed grant extended before the acceptance", []Row{grant(Row{"until": "2026-11-11T12:00:00.000Z"}), extend("2026-11-11T11:00:00.000Z", "2026-11-11T14:00:00.000Z")}, true},
		{"timed grant extended after the acceptance", []Row{grant(Row{"until": "2026-11-11T12:00:00.000Z"}), extend("2026-11-11T13:30:00.000Z", "2026-11-11T14:00:00.000Z")}, false},
	}
	for _, tc := range cases {
		rows := b380Rows(t, key, Row{"event": "review.approved", "file": "a.mjs", "content_hash": "h2"})
		rows = append(rows, b380Rows(t, key, append(slices.Clone(tc.rows), b649Clone(accept))...)...)
		got := b380Codes(b380Eval(rows, key, hits, transcript))
		if cleared := !slices.Contains(got, "rule_break"); cleared != tc.cleared {
			t.Errorf("%s: cleared %v, want %v (codes %v)", tc.name, cleared, tc.cleared, got)
		}
	}
	// an unsigned extend row moves nothing
	rows := b380Rows(t, key, Row{"event": "review.approved", "file": "a.mjs", "content_hash": "h2"}, grant(Row{"until": "2026-11-11T12:00:00.000Z"}), b649Clone(accept))
	rows = append(rows, extend("2026-11-11T11:00:00.000Z", "2026-11-11T14:00:00.000Z"))
	if got := b380Codes(b380Eval(rows, key, hits, transcript)); !slices.Contains(got, "rule_break") {
		t.Fatalf("an unsigned extend row must not move the end: %v", got)
	}
}

func b649Clone(r Row) Row {
	out := Row{}
	for k, v := range r {
		out[k] = v
	}
	return out
}
