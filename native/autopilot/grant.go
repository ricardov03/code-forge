// Package autopilot reads the grant dialect issued by Node and performs its
// lazy expiry. Unknown shapes are delegated before any state is changed.
package autopilot

import (
	"errors"
	"math"
	"slices"
	"time"

	"github.com/ricardov03/code-forge/native/blockops"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

var ErrUnsupported = errors.New("unsupported autopilot grant")

type Result struct {
	State      string
	Grant      ledger.Object
	ExpiredNow bool
}

func text(v any) string { s, _ := v.(string); return s }

func timestamp(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false
	}
	t, err := time.Parse("2006-01-02T15:04:05.000Z", s)
	return t, err == nil && t.Format("2006-01-02T15:04:05.000Z") == s
}

// GrantState is pure, matching Node's stopped-before-expired precedence.
func GrantState(grant ledger.Object, now time.Time) (string, error) {
	if !valid(grant) {
		return "", ErrUnsupported
	}
	status := text(grant.Get("status"))
	if status == "stopped" || status == "expired" {
		return status, nil
	}
	until, _ := timestamp(grant.Get("until"))
	if !now.Before(until) {
		return "expired", nil
	}
	return "active", nil
}

func valid(g ledger.Object) bool {
	if g == nil || text(g.Get("grant_id")) == "" || !slices.Contains([]string{"active", "stopped", "expired"}, text(g.Get("status"))) {
		return false
	}
	for _, f := range g {
		if !slices.Contains([]string{"grant_id", "status", "scopes", "deny", "delegate", "until", "caps", "stop_at", "started_at", "stopped_at", "expired_at", "link", "paused"}, f.Key) {
			return false
		}
	}
	for _, name := range []string{"until", "started_at"} {
		if _, ok := timestamp(g.Get(name)); !ok {
			return false
		}
	}
	for _, name := range []string{"stopped_at", "expired_at"} {
		if g.Get(name) != nil {
			if _, ok := timestamp(g.Get(name)); !ok {
				return false
			}
		}
	}
	for _, name := range []string{"scopes", "deny"} {
		values, ok := g.Get(name).([]any)
		if !ok {
			return false
		}
		for _, value := range values {
			if _, ok := value.(string); !ok {
				return false
			}
		}
	}
	if !slices.Contains([]string{"L2", "L3"}, text(g.Get("delegate"))) {
		return false
	}
	caps, ok := g.Get("caps").(ledger.Object)
	if !ok {
		return false
	}
	for _, f := range caps {
		if n, ok := f.Value.(float64); !ok || n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
			return false
		}
	}
	if n, ok := g.Get("stop_at").(float64); !ok || !(n > 0 && n <= 1) {
		return false
	}
	if g.Get("link") != nil {
		if _, ok := g.Get("link").(string); !ok {
			return false
		}
	}
	if g.Get("paused") != nil {
		if _, ok := g.Get("paused").(ledger.Object); !ok {
			return false
		}
	}
	return true
}

// Read also proves approvals are quiet: their restore path remains with Node.
func Read(record ledger.Object, now time.Time) (Result, error) {
	without := record.Clone()
	without.Delete("autopilot")
	if !blockops.QuietExpiry(without) {
		return Result{}, ErrUnsupported
	}
	return ReadGrant(record, now)
}

// ErrApprovalDue: an active approval whose window is over; checkApprovals
// (src/autopilot/limits.mjs) restores it, which stays with Node.
var ErrApprovalDue = errors.New("autopilot approval due for restore")

// Approvals is checkApprovals' read: nil when no approval is due at now
// (Node then writes nothing), ErrApprovalDue when one is, ErrUnsupported for
// an entry Node would read differently (a null entry throws in Node; an
// until Go does not date the way Date.parse does).
func Approvals(record ledger.Object, now time.Time) error {
	values, ok := record.Get("autopilot_approvals").([]any)
	if !ok {
		return nil // approvalsOf: not an array is no approvals
	}
	due := false
	for _, value := range values {
		if value == nil {
			return ErrUnsupported
		}
		a, ok := value.(ledger.Object)
		if !ok || a.Get("status") != "active" {
			continue
		}
		until := a.Get("until")
		if until == nil {
			continue // Date.parse(undefined) and Date.parse(null) are NaN: never due
		}
		t, ok := timestamp(until)
		if !ok {
			return ErrUnsupported
		}
		if !now.Before(t) {
			due = true
		}
	}
	if due {
		return ErrApprovalDue
	}
	return nil
}

// ApprovalDue is checkApprovals' due test for one entry: an active approval
// whose until is at or before now. ok is false for an until Go does not date
// the way Date.parse does (a missing or null until is never due).
func ApprovalDue(a ledger.Object, now time.Time) (due, ok bool) {
	if a.Get("status") != "active" || a.Get("until") == nil {
		return false, true
	}
	t, ok := timestamp(a.Get("until"))
	if !ok {
		return false, false
	}
	return !now.Before(t), true
}

// ReadGrant is Read without the approvals check: Node's checkExpiry never
// reads approvals (checkApprovals ran once, at the start of the command).
func ReadGrant(record ledger.Object, now time.Time) (Result, error) {
	if record.Get("autopilot") == nil {
		return Result{State: "none"}, nil
	}
	g, ok := record.Get("autopilot").(ledger.Object)
	if !ok {
		return Result{}, ErrUnsupported
	}
	s, err := GrantState(g, now)
	if err != nil {
		return Result{}, err
	}
	if record.Get("status") != "active" {
		s = "run-ended"
	}
	return Result{State: s, Grant: g}, nil
}

func Supported(record ledger.Object) bool {
	_, err := Read(record, time.Now())
	return err == nil
}

// CheckExpiry re-reads under Node's shared run lock. Publish the signed row
// before saving the expired state, with the original check time in both.
func CheckExpiry(home, run string, now time.Time) (Result, error) {
	return checkExpiry(home, run, now, Read)
}

// CheckGrantExpiry is CheckExpiry that reads the grant only (ReadGrant).
func CheckGrantExpiry(home, run string, now time.Time) (Result, error) {
	return checkExpiry(home, run, now, ReadGrant)
}

func checkExpiry(home, run string, now time.Time, reader func(ledger.Object, time.Time) (Result, error)) (Result, error) {
	read := func() (Result, error) {
		r, err := state.ReadRun(home, run)
		if err != nil {
			return Result{}, err
		}
		return reader(r, now)
	}
	result, err := read()
	if err != nil || result.State != "expired" || result.Grant.Get("status") != "active" {
		return result, err
	}
	err = state.UpdateRun(home, run, func(record *ledger.Object) error {
		var err error
		result, err = reader(*record, now)
		if err != nil {
			return err
		}
		if result.State != "expired" || result.Grant.Get("status") != "active" {
			return state.ErrUnchanged
		}
		at := now.UTC().Format("2006-01-02T15:04:05.000Z")
		_, err = state.WriteSigned(home, run, ledger.Object{{Key: "event", Value: "autopilot.expire"}, {Key: "grant_id", Value: result.Grant.Get("grant_id")}, {Key: "until", Value: result.Grant.Get("until")}, {Key: "ts", Value: at}})
		if err != nil {
			return err
		}
		result.Grant.Set("status", "expired")
		result.Grant.Set("expired_at", at)
		record.Set("autopilot", result.Grant)
		result.ExpiredNow = true
		return nil
	})
	if err != nil {
		result.ExpiredNow = false
	}
	return result, err
}

// RoutineRefusal is grantFor's close:routine subset (no budget category).
func RoutineRefusal(result Result) string {
	if result.State != "active" {
		if result.State == "none" {
			return "no-grant"
		}
		return result.State
	}
	deny, _ := result.Grant.Get("deny").([]any)
	allow, _ := result.Grant.Get("scopes").([]any)
	if slices.Contains(deny, any("close:routine")) {
		return "denied"
	}
	if !slices.Contains(allow, any("close:routine")) {
		return "not-allowed"
	}
	return ""
}

func ActiveGrantMessage(run, until string) string {
	return "run " + run + " already has an active autopilot grant until " + until + "; stop it first with code-forge autopilot stop --run " + run
}

func ExpiredMessage(run, until string) string {
	return "run " + run + "'s autopilot grant already expired at " + until
}
