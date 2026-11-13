package autopilot

import (
	"errors"
	"math"
	"time"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B616: checkExpiry (src/autopilot/grant.mjs) for a caller that only needs
// its write, as a worker drain runs it (expiryChecks drops the answer). It
// reads the grant the way Node does, not the start dialect Read checks:
// grantOf is any object under `autopilot`, a run that is not active writes
// nothing, a grant marked stopped or expired writes nothing, and an active
// grant writes ONE signed autopilot.expire row once now >= Date.parse(until).

// expireDue is checkExpiry's decision on one record: whether the expire row
// is due. ErrUnsupported: an until whose Date.parse Go cannot prove equal
// to V8's (a number, an array, a legacy text form Go does not read).
func expireDue(record ledger.Object, now time.Time) (ledger.Object, bool, error) {
	var grant ledger.Object
	switch g := record.Get("autopilot").(type) {
	case ledger.Object:
		grant = g
	case []any:
		return nil, false, nil // an array grant has no status: never active
	default:
		return nil, false, nil // null, text, number, bool: no grant
	}
	if record.Get("status") != "active" {
		return nil, false, nil // run-ended
	}
	if s := grant.Get("status"); s == "stopped" || s == "expired" {
		return nil, false, nil
	}
	var until any
	present := false
	for _, f := range grant {
		if f.Key == "until" {
			until, present = f.Value, true
		}
	}
	var ms float64
	switch until.(type) {
	case ledger.Object:
		ms = math.NaN() // String(object) is "[object Object]"
	default:
		parsed, ok, known := gatecheck.JSDateParse(until, present)
		if !known {
			return nil, false, ErrUnsupported
		}
		if !ok {
			ms = math.NaN()
		} else {
			ms = parsed
		}
	}
	// now.getTime() >= NaN is false: a grant without a readable until stays active
	if !(float64(now.UnixMilli()) >= ms) || grant.Get("status") != "active" {
		return nil, false, nil
	}
	return grant, true, nil
}

// ExpireSupported reports whether Go decides checkExpiry on this record the
// way Node does (no write happens here).
func ExpireSupported(record ledger.Object, now time.Time) bool {
	_, _, err := expireDue(record, now)
	return err == nil
}

// ExpireAny is checkExpiry's write for any grant shape Node reads: under the
// run lock the record is read again and, when the expire row is still due,
// the signed row is written before the grant is saved as expired. A missing
// grant_id or until is left out of the row, as JSON.stringify does.
func ExpireAny(home, run string, now time.Time) error {
	record, err := state.ReadRun(home, run)
	if err != nil {
		return err
	}
	if _, due, err := expireDue(record, now); err != nil || !due {
		return err
	}
	return state.UpdateRun(home, run, func(record *ledger.Object) error {
		grant, due, err := expireDue(*record, now)
		if err != nil {
			return err
		}
		if !due {
			return state.ErrUnchanged
		}
		at := now.UTC().Format("2006-01-02T15:04:05.000Z")
		row := ledger.Object{{Key: "event", Value: "autopilot.expire"}}
		for _, name := range []string{"grant_id", "until"} {
			for _, f := range grant {
				if f.Key == name {
					row = append(row, ledger.Field{Key: name, Value: f.Value})
				}
			}
		}
		row = append(row, ledger.Field{Key: "ts", Value: at})
		if _, err := state.WriteSigned(home, run, row); err != nil {
			return err
		}
		grant = grant.Clone()
		grant.Set("status", "expired")
		grant.Set("expired_at", at)
		record.Set("autopilot", grant)
		return nil
	})
}

// IsNoRun is checkExpiry's caller rule: no-run and bad-run-id answer nothing.
func IsNoRun(err error) bool {
	var se *state.Error
	return errors.As(err, &se) && (se.Code == "no-run" || se.Code == "bad-run-id")
}
