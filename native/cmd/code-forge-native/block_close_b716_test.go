package main

import (
	"math/rand"
	"testing"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
)

// TestB716DecisionRowsAlwaysSign: the owner's --decide rows (review.waived
// and rule_break.accepted) hold only text and finite line numbers. For any
// text (invalid UTF-8, lone surrogate bytes, controls, line separators) and
// any key, signing and the ledger round trip always give a row the gate
// verifies, so the old sign-error hand-over to Node had no reachable case.
func TestB716DecisionRowsAlwaysSign(t *testing.T) {
	texts := []string{"", "a", "\xff\xfe", "\xed\xa0\x80", "\x00\x1f\x7f", "  ", "😀", "\"\\/", "<script>&amp;", string(make([]byte, 4096))}
	for b := 0; b < 256; b++ {
		texts = append(texts, string([]byte{byte(b)}), "x"+string([]byte{byte(b), 'y'}))
	}
	rng := rand.New(rand.NewSource(716))
	for i := 0; i < 500; i++ {
		buf := make([]byte, rng.Intn(64))
		rng.Read(buf)
		texts = append(texts, string(buf))
	}
	keys := [][]byte{nil, {}, []byte("k"), make([]byte, 256)}
	lines := []float64{1, 2, 1000, 9007199254740991}
	checked := 0
	for i, text := range texts {
		key := keys[i%len(keys)]
		run := texts[(i+1)%len(texts)]
		waiver := ledger.Object{{Key: "event", Value: "review.waived"}, {Key: "block", Value: text}, {Key: "file", Value: text}, {Key: "finding", Value: text}, {Key: "reason", Value: text}, {Key: "by", Value: "human"}}
		hit := ledger.Object{{Key: "event", Value: "rule_break.accepted"}, {Key: "block", Value: text}, {Key: "rule", Value: text}, {Key: "line", Value: lines[i%len(lines)]}, {Key: "line_sha256", Value: blockSHA([]byte(text))}, {Key: "reason", Value: text}, {Key: "by", Value: "human"}}
		for _, row := range []ledger.Object{waiver, hit} {
			signed := closeDecideSigned(run, row, key)
			if !gatecheck.Verified(signed, key) {
				t.Fatalf("row %d %q: signed row does not verify", i, text)
			}
			if signed["event"] != row.Get("event") || signed["by"] != "human" {
				t.Fatalf("row %d: signed row lost its fields: %v", i, signed)
			}
			if line, ok := row.Get("line").(float64); ok && signed["line"] != line {
				t.Fatalf("row %d: line %v became %v", i, line, signed["line"])
			}
			checked++
		}
	}
	if checked < 2000 {
		t.Fatalf("checked only %d rows", checked)
	}
}
