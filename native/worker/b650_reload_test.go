package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B650: the worker reads each ticket's config from the run record's current
// snapshot (configFor), so a run reload that changes review.on_unavailable
// reaches the next ticket of a live worker without a restart: the boot
// config here has no chain, and only the reloaded snapshot names m-c.

const b650NoChain = `{"levels":{"L2":{"model":"m-a","provider":"anthropic"}},"review":{"adaptive":{"enabled":false}}}`
const b650Chain = `{"levels":{"L2":{"model":"m-a","provider":"anthropic"}},"review":{"adaptive":{"enabled":false},"on_unavailable":[{"model":"m-c"}],"on_unavailable_backoff_s":[]}}`

func b650Hash(t *testing.T, text string) string {
	t.Helper()
	obj, err := ledger.DecodeObject([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	data, err := canon.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// b650Record writes the run record with both snapshots and current as the
// config hash, the way run reload leaves it (no pending ticket pinned).
func b650Record(t *testing.T, e *b629Env, current string) {
	t.Helper()
	base := "e64a56881e75eab80bd5dcda472ab5182c0b9604"
	a, b := b650Hash(t, b650NoChain), b650Hash(t, b650Chain)
	e.fx.record(t, `{"run_id":"r1","project":"unit","workspace":"`+e.fx.root+`","status":"active",`+
		`"blocks":{"B1":{"base_sha":"`+base+`","owned_files":["a.txt"]},"B2":{"base_sha":"`+base+`","owned_files":["b.txt"]}},`+
		`"config":{"hash":"`+current+`","snapshots":{"`+a+`":`+b650NoChain+`,"`+b+`":`+b650Chain+`},"pins":{}}}`)
}

func TestB650ReloadedOnUnavailableReachesNextTicket(t *testing.T) {
	e := b629Setup(t, b650NoChain) // the boot config: no chain
	b650Record(t, e, b650Hash(t, b650NoChain))
	if calls, _ := e.next(t, "m-a"); strings.Contains(strings.Join(calls, ","), "m-c") {
		t.Fatalf("ticket 1 calls %v: the snapshot before the reload has no chain", calls)
	}
	if got := e.rows(t, "review.unavailable_fallback"); len(got) != 0 {
		t.Fatalf("unavailable_fallback rows before the reload: %v", got)
	}
	// run reload moves the record's hash to the new snapshot; the same
	// worker (same boot config) takes it on its next ticket
	b650Record(t, e, b650Hash(t, b650Chain))
	calls, res := e.next(t, "m-a")
	if strings.Join(calls, ",") != "m-a,m-a,m-c" || !strings.Contains(res, `"approved":true`) || !strings.Contains(res, `"review.on_unavailable"`) {
		t.Fatalf("ticket 2 calls %v (want m-a, its timeout retry, then the reloaded chain's m-c) result %s", calls, res)
	}
	steps := e.rows(t, "review.unavailable_fallback")
	if len(steps) != 1 || steps[0]["to_model"] != "m-c" || steps[0]["from_model"] != "m-a" || steps[0]["waited_ms"] != 0.0 {
		t.Fatalf("unavailable_fallback rows %v", steps)
	}
}
