package worker

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
	"github.com/ricardov03/code-forge/native/sign"
)

// B630 (issue #37 C3/C4): when a file's review stays unavailable after the
// built-in recovery and the review.on_unavailable chain, an active autopilot
// grant with scope review:substitute lets the worker run one substitute
// review on a provider none of the failed sessions used. One signed
// autopilot.decision row records each substitute (scope, block, file,
// provider, model, reason) before it runs. An active grant without the scope
// writes one signed autopilot.decision row to the owner instead (acted
// false), which names the owner's commands. With no active grant nothing
// changes.

// substTracker notes the provider of every review session spawned for one
// ticket.
type substTracker struct {
	mu        sync.Mutex
	providers []string
}

func (st *substTracker) note(p string) {
	if p == "" {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, q := range st.providers {
		if strings.EqualFold(strings.TrimSpace(q), strings.TrimSpace(p)) {
			return
		}
	}
	st.providers = append(st.providers, p)
}

// spawn wraps a review spawn: the result's provider, else the spawned
// level's, else the config's top-level provider, is noted whatever the
// session's outcome.
func (st *substTracker) spawn(cfg map[string]any, inner func(review.SpawnOpts) (ledger.Object, error)) func(review.SpawnOpts) (ledger.Object, error) {
	return func(o review.SpawnOpts) (ledger.Object, error) {
		c := o.Cfg
		if c == nil {
			c = cfg
		}
		levels, _ := obj(c["levels"])
		l, _ := obj(levels[o.Level])
		p, _ := l["provider"].(string)
		if p == "" {
			p, _ = c["provider"].(string)
		}
		st.note(p)
		res, err := inner(o)
		if res != nil {
			if rp, _ := res.Get("provider").(string); rp != "" {
				st.note(rp)
			}
		}
		return res, err
	}
}

// failed is every provider noted, plus each session of res that names one.
func (st *substTracker) failed(res ledger.Object) []string {
	sessions, _ := res.Get("sessions").([]any)
	for _, s := range sessions {
		if o, ok := s.(ledger.Object); ok {
			p, _ := o.Get("provider").(string)
			st.note(p)
		}
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]string{}, st.providers...)
}

func substDecisionID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "ad-unknown"
	}
	return "ad-" + hex.EncodeToString(b)
}

func substStrings(list []string) []any {
	out := []any{}
	for _, s := range list {
		out = append(out, s)
	}
	return out
}

// substOwnerCommand is the owner's way out: grant the scope. Without it the
// review stays unavailable and retries once its provider answers again.
func substOwnerCommand(run string) string {
	return "code-forge autopilot start --run " + run + " --until <time> --delegate L2 --allow " + autopilot.SubstituteScope
}

// substAskedBefore: a signed autopilot.decision row to the owner already
// asks the same file at the same content.
func (w *TicketRunner) substAskedBefore(block, file, hash string) bool {
	rows, err := w.readRows()
	if err != nil {
		return false
	}
	for _, v := range rows {
		r, ok := v.(map[string]any)
		if !ok || r["run"] != w.RunID || r["event"] != "autopilot.decision" || r["scope"] != autopilot.SubstituteScope ||
			r["block"] != block || r["file"] != file || r["content_hash"] != hash || r["acted"] != false {
			continue
		}
		if res, err := sign.VerifyRow(r, w.Key); err == nil && res.OK {
			return true
		}
	}
	return false
}

// substitute is the step after review.File left res unavailable: res itself
// when no substitute runs, else the substitute review's result.
func (w *TicketRunner) substitute(in review.Input, deps review.Deps, res ledger.Object, failed []string, block, file, hash string, write func(ledger.Object) error) (ledger.Object, error) {
	rec, _, err := w.readRecord()
	if err != nil || rec == nil {
		return res, nil
	}
	now := w.now()
	grant, err := autopilot.ReadGrant(rec, now)
	if err != nil || grant.State != "active" {
		return res, nil // no autopilot run: the review stays unavailable, as before
	}
	reason, _ := res.Get("reason").(string)
	ts := now.UTC().Format("2006-01-02T15:04:05.000Z")
	on := "no provider recorded"
	if len(failed) > 0 {
		on = strings.Join(failed, ", ")
	}
	row := func(acted bool) ledger.Object {
		return ledger.Object{{Key: "event", Value: "autopilot.decision"}, {Key: "decision_id", Value: substDecisionID()},
			{Key: "grant_id", Value: grant.Grant.Get("grant_id")}, {Key: "scope", Value: autopilot.SubstituteScope},
			{Key: "subject", Value: ledger.Object{{Key: "block", Value: block}, {Key: "file", Value: file}}},
			{Key: "question", Value: "substitute review of " + file + " in block " + block + "? its review stayed unavailable (" + reason + ") on " + on},
			{Key: "options", Value: []any{"substitute", "owner"}}, {Key: "acted", Value: acted}, {Key: "to_owner", Value: !acted},
			{Key: "level", Value: "orchestrator"}, {Key: "failed_providers", Value: substStrings(failed)}, {Key: "unavailable_reason", Value: reason}}
	}
	if why := autopilot.ScopeRefusal(grant, autopilot.SubstituteScope); why != "" {
		if !w.substAskedBefore(block, file, hash) {
			r := row(false)
			r.Set("decision", nil)
			r.Set("owner_reason", "scope "+autopilot.SubstituteScope+" "+why+"; the owner grants it with "+substOwnerCommand(w.RunID)+", else the review retries when its provider answers again")
			r.Set("owner_command", substOwnerCommand(w.RunID))
			r.Set("ts", ts)
			_ = write(r) // a failed write leaves the review unavailable, as before
		}
		return res, nil
	}
	route, ok := review.PickSubstitute(in.Cfg, failed, deps.CheckSpawn)
	if !ok {
		if !w.substAskedBefore(block, file, hash) {
			r := row(false)
			r.Set("decision", nil)
			r.Set("owner_reason", "no runnable closed-book reviewer on a provider other than "+on+"; the review retries when its provider answers again")
			r.Set("owner_command", nil)
			r.Set("ts", ts)
			_ = write(r)
		}
		return res, nil
	}
	r := row(true)
	r.Set("decision", "substitute")
	r.Set("within_scope", true)
	r.Set("escalate", false)
	r.Set("reason", "review "+reason+" on "+on+"; substitute on "+route.Provider+"/"+route.Model)
	r.Set("provider", route.Provider)
	r.Set("model", route.Model)
	r.Set("ts", ts)
	if err := write(r); err != nil {
		return res, nil // no signed decision row, no substitute
	}
	return review.Substitute(in, deps, route)
}
