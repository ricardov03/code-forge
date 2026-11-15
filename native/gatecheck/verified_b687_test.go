package gatecheck

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/jsonx"
)

// B687: rows Node's signer wrote (key 0x11 x 32) with a lone surrogate and,
// after signing, a number JSON.parse reads as Infinity (Node signed the
// stringified row, where it is null). Node's verifyRow accepts both; Go must
// too, and must still refuse a changed field, a changed mac or another key.
func TestB687VerifiedRowsWithLoneSurrogatesAndInfinity(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	for _, text := range []string{
		`{"run":"r1","block":"B1","event":"review.result","ticket":"t\ud800","file":"a.mjs","content_hash":"h1","status":"reviewed","approved":false,"open":[{"id":"F1","severity":"warning"}],"score":1e400,"mac":"8d44b4e507f2cbf6fb157a15730dd0c5c5a536c87481889bbcdda2efff2eb439"}`,
		`{"run":"r1","block":"B1","event":"review.waived","file":"a.mjs","finding":"F1","severity":"warning","verdict":"agree","by":"agent","reason":"r\ud83d","mac":"5982ace762ac9f917825e02d3ccaab7fe1990e4ffb339a47c345e2c2503efd80"}`,
	} {
		parse := func(s string) Row {
			v, err := jsonx.Parse(s)
			if err != nil {
				t.Fatal(err)
			}
			return v.(map[string]any)
		}
		if !Verified(parse(text), key) {
			t.Fatalf("Node-signed row refused: %s", text)
		}
		if Verified(parse(text), bytes.Repeat([]byte{0x12}, 32)) {
			t.Fatalf("row accepted under another key: %s", text)
		}
		changed := strings.Replace(text, `"run":"r1"`, `"run":"r2"`, 1)
		if Verified(parse(changed), key) {
			t.Fatalf("changed row accepted: %s", changed)
		}
		badMAC := strings.Replace(text, `"mac":"`, `"mac":"0`, 1)
		if Verified(parse(badMAC), key) {
			t.Fatalf("bad mac accepted: %s", badMAC)
		}
	}
}
