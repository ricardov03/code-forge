package main

// B505: checkJev (src/doctor/local.mjs) in Go: the one live Jev call of the
// full doctor run, through the native/jev client.

import (
	"fmt"

	"github.com/ricardov03/code-forge/native/jev"
	"github.com/ricardov03/code-forge/native/ledger"
)

// doctorJevOpts are the client seams; tests point URL at a local fake
// server and make Sleep a no-op.
var doctorJevOpts = jev.Opts{}

// doctorJevRow is checkJev(cfg, key). hasKey false is Node's null key. ok is
// false where Node's checkJev throws (the risk payload refuses the config,
// an empty key): the caller must not run the doctor in Go then.
func doctorJevRow(cfg map[string]any, key string, hasKey bool) (doctorRow, bool) {
	if !hasKey {
		return doctorRow{"jev", "WARN", "jev", "live call skipped (no key)"}, true
	}
	questions, ok := jev.RiskPayload(cfg)
	if !ok || key == "" {
		return doctorRow{}, false
	}
	o := doctorJevOpts
	o.State = ledger.Object{{Key: "paths", Value: []any{"README.md"}}, {Key: "diff_stat", Value: "+1 -0"}}
	o.Questions = questions
	o.Key = key
	res, err := jev.Ask(o)
	if err != nil {
		return doctorRow{}, false
	}
	if res.OK {
		return doctorRow{"jev", "OK", "jev", fmt.Sprintf("live call ok (%d attempt(s))", res.Attempts)}, true
	}
	status := "WARN"
	switch res.Kind {
	case "rate_limited", "unavailable", "network", "timeout":
	default:
		status = "FAIL"
	}
	detail := "live call failed (" + res.Kind
	if res.Status != 0 {
		detail += fmt.Sprintf(", status %d", res.Status)
	}
	return doctorRow{"jev", status, "jev", detail + ")"}, true
}
