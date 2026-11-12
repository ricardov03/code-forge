package main

import (
	"encoding/json"
	"reflect"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
)

// closeOrderedRows keeps JSON.parse's key order for the autopilot.decision
// rows whose decision is an object or array holding an object (B498), and the
// agent waiver and reopen rows whose fields hold one (B583): Node prints that
// value with JSON.stringify, in the order the ledger line has.
// It reads the ledger again only when such a row exists; a row whose second
// reading differs (a concurrent rotation) is left out, and the gate then
// reports the value unknown, as before.
func closeOrderedRows(ledgerDir, project string, all []any) map[uintptr]ledger.Object {
	need := map[int]map[string]any{}
	for i, v := range all {
		r, ok := v.(map[string]any)
		if !ok {
			continue
		}
		if r["event"] == "autopilot.decision" && closeHoldsObject(r["decision"]) {
			need[i] = r
		}
		// B583: an agent waiver or reopen prints its fields in the --json
		// close audit with JSON.stringify, so an object keeps the line's order.
		if (r["event"] == "review.waived" || r["event"] == "review.reopened") && r["by"] == "agent" {
			for _, k := range []string{"file", "finding", "reason", "verdict"} {
				if closeHoldsObject(r[k]) {
					need[i] = r
				}
			}
		}
	}
	if len(need) == 0 {
		return nil
	}
	ordered, err := ledger.ReadAllOrdered(ledgerDir, project)
	if err != nil {
		return nil
	}
	out := map[uintptr]ledger.Object{}
	for i, r := range need {
		if i >= len(ordered) {
			continue
		}
		o, ok := ordered[i].(ledger.Object)
		if !ok {
			continue
		}
		data, err := ledger.Stringify(o, false)
		if err != nil {
			continue
		}
		var back any
		if json.Unmarshal(data, &back) != nil || !reflect.DeepEqual(back, any(r)) {
			continue
		}
		out[gatecheck.OrderedKey(r)] = o
	}
	return out
}

func closeHoldsObject(v any) bool {
	switch x := v.(type) {
	case map[string]any:
		return true
	case []any:
		for _, item := range x {
			if closeHoldsObject(item) {
				return true
			}
		}
	}
	return false
}
