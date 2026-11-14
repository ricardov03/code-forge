package worker

import (
	"sync"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
	"github.com/ricardov03/code-forge/native/session"
	"github.com/ricardov03/code-forge/native/sign"
)

// B321 (folded into B629): a reviewer route (one level's provider and model)
// that timed out on 2 tickets in a row in this run is benched: later tickets
// of the run leave it out of the level's ladder and of review.on_unavailable,
// until it succeeds on another ticket or the worker is reattached (run start
// --reattach writes worker.reattach). The state is the run's signed ledger
// rows, so a worker restart keeps it:
//   review.route_timeout  {ticket, level, provider, model, streak}  one more timeout
//   review.route_benched  {ticket, level, provider, model, timeouts} the streak reached 2
//   review.route_reset    {ticket, level, provider, model, reason, was_benched} a success
// worker.reattach clears every streak. Unsigned or forged rows are never read.

// routeBenchAfter is the timeouts in a row that bench a route.
const routeBenchAfter = 2

type benchRoute struct{ level, provider, model string }

// routeBenchStreaks replays this run's signed bench rows into each route's
// timeouts in a row.
func routeBenchStreaks(rows []any, key []byte, runID string) map[benchRoute]int {
	out := map[benchRoute]int{}
	for _, raw := range rows {
		r, ok := raw.(map[string]any)
		if !ok || r["run"] != runID {
			continue
		}
		ev, _ := r["event"].(string)
		if ev != "review.route_timeout" && ev != "review.route_reset" && ev != "worker.reattach" {
			continue
		}
		if res, err := sign.VerifyRow(r, key); err != nil || !res.OK {
			continue
		}
		if ev == "worker.reattach" {
			out = map[benchRoute]int{}
			continue
		}
		k := benchRoute{}
		k.level, _ = r["level"].(string)
		k.provider, _ = r["provider"].(string)
		k.model, _ = r["model"].(string)
		if ev == "review.route_timeout" {
			out[k]++
		} else {
			delete(out, k)
		}
	}
	return out
}

// routeBenchLevels are the levels whose routes can be benched.
var routeBenchLevels = []string{"L2", "L3"}

// routeBenchConfig is cfg with the benched routes left out: a level's benched
// primary model gives way to its first fallback that is not benched (else to
// the level's first review.on_unavailable route that is not benched), and
// benched routes leave the fallback ladder and review.on_unavailable. A level
// with nothing left keeps its config (a benched route is the last resort).
// cfg itself is returned when nothing is benched.
func routeBenchConfig(cfg map[string]any, streaks map[benchRoute]int) map[string]any {
	benched := func(level string, provider, model any) bool {
		p, _ := provider.(string)
		m, _ := model.(string)
		return streaks[benchRoute{level, p, m}] >= routeBenchAfter
	}
	anyBenched := false
	for _, n := range streaks {
		if n >= routeBenchAfter {
			anyBenched = true
		}
	}
	if !anyBenched {
		return cfg
	}
	levels, _ := obj(cfg["levels"])
	newLevels := map[string]any{}
	for k, v := range levels {
		newLevels[k] = v
	}
	changed := false
	for _, level := range routeBenchLevels {
		l, _ := obj(levels[level])
		if l == nil {
			continue
		}
		provider := l["provider"]
		if provider == nil {
			provider = cfg["provider"]
		}
		type entry struct {
			provider, model, effort any
			hasEffort               bool
		}
		all := []entry{{provider: provider, model: l["model"]}}
		all[0].effort, all[0].hasEffort = l["effort"]
		list, isList := l["fallback"].([]any)
		if l["fallback"] != nil && !isList {
			continue
		}
		for _, raw := range list {
			f, _ := raw.(map[string]any)
			if f == nil {
				all = nil
				break
			}
			p := f["provider"]
			if p == nil {
				p = provider
			}
			e := entry{provider: p, model: f["model"]}
			e.effort, e.hasEffort = f["effort"]
			all = append(all, e)
		}
		if all == nil {
			continue
		}
		kept := []entry{}
		for _, e := range all {
			if !benched(level, e.provider, e.model) {
				kept = append(kept, e)
			}
		}
		if len(kept) == len(all) {
			continue
		}
		if len(kept) == 0 {
			for _, r := range review.OnUnavailableRoutes(cfg, level) {
				if !benched(level, r["provider"], r["model"]) {
					e := entry{provider: r["provider"], model: r["model"]}
					e.effort, e.hasEffort = r["effort"]
					kept = append(kept, e)
					break
				}
			}
		}
		if len(kept) == 0 {
			continue
		}
		next := map[string]any{}
		for k, v := range l {
			next[k] = v
		}
		next["provider"], next["model"] = kept[0].provider, kept[0].model
		delete(next, "effort")
		if kept[0].hasEffort {
			next["effort"] = kept[0].effort
		}
		fb := []any{}
		for _, e := range kept[1:] {
			m := map[string]any{"provider": e.provider, "model": e.model}
			if e.hasEffort {
				m["effort"] = e.effort
			}
			fb = append(fb, m)
		}
		next["fallback"] = fb
		newLevels[level] = next
		changed = true
	}
	rev, _ := obj(cfg["review"])
	var newChain []any
	chainChanged := false
	if list, ok := rev["on_unavailable"].([]any); ok {
		for _, raw := range list {
			m, _ := raw.(map[string]any)
			if m != nil {
				level, _ := m["level"].(string)
				if level == "" {
					level = "L2"
				}
				if benched(level, review.OnUnavailableProvider(cfg, m, level), m["model"]) {
					chainChanged = true
					continue
				}
			}
			newChain = append(newChain, raw)
		}
	}
	if !changed && !chainChanged {
		return cfg
	}
	out := map[string]any{}
	for k, v := range cfg {
		out[k] = v
	}
	if changed {
		out["levels"] = newLevels
	}
	if chainChanged {
		r := map[string]any{}
		for k, v := range rev {
			r[k] = v
		}
		if newChain == nil {
			newChain = []any{}
		}
		r["on_unavailable"] = newChain
		out["review"] = r
	}
	return out
}

// routeWatch records, per route, whether one ticket's review sessions timed
// out or answered.
type routeWatch struct {
	mu    sync.Mutex
	order []benchRoute
	seen  map[benchRoute]*routeSeen
}

type routeSeen struct{ timedOut, ok bool }

func newRouteWatch() *routeWatch { return &routeWatch{seen: map[benchRoute]*routeSeen{}} }

// spawn wraps a review spawn: each result's provider and model (else the
// spawned level's) is noted as timed out or answered.
func (rw *routeWatch) spawn(cfg map[string]any, inner func(review.SpawnOpts) (ledger.Object, error)) func(review.SpawnOpts) (ledger.Object, error) {
	return func(o review.SpawnOpts) (ledger.Object, error) {
		res, err := inner(o)
		if err != nil || res == nil {
			return res, err
		}
		status, _ := res.Get("status").(string)
		if status != "timeout" && status != "ok" {
			return res, err
		}
		c := o.Cfg
		if c == nil {
			c = cfg
		}
		k := benchRoute{level: o.Level}
		k.provider, _ = res.Get("provider").(string)
		k.model, _ = res.Get("model").(string)
		if k.provider == "" || k.model == "" {
			levels, _ := obj(c["levels"])
			l, _ := obj(levels[o.Level])
			if k.model == "" {
				k.model, _ = l["model"].(string)
			}
			if k.provider == "" {
				p := l["provider"]
				if p == nil {
					p = c["provider"]
				}
				k.provider, _ = p.(string)
			}
		}
		if k.model == "" {
			return res, err
		}
		rw.mu.Lock()
		s := rw.seen[k]
		if s == nil {
			s = &routeSeen{}
			rw.seen[k] = s
			rw.order = append(rw.order, k)
		}
		if status == "ok" {
			s.ok = true
		} else {
			s.timedOut = true
		}
		rw.mu.Unlock()
		return res, err
	}
}

// writeOutcomes writes this ticket's bench rows through write (the ticket's
// signed row writer): a route that only timed out gets review.route_timeout
// (and review.route_benched when its streak reaches 2); a route that
// answered after an earlier timeout gets review.route_reset. A ledger that
// cannot be read writes nothing.
func (rw *routeWatch) writeOutcomes(ticket string, streaks map[benchRoute]int, write func(ledger.Object) error) {
	rw.mu.Lock()
	defer rw.mu.Unlock()
	for _, k := range rw.order {
		s := rw.seen[k]
		head := ledger.Object{{Key: "ticket", Value: ticket}, {Key: "level", Value: k.level}, {Key: "provider", Value: k.provider}, {Key: "model", Value: k.model}}
		with := func(event string, more ...ledger.Field) ledger.Object {
			row := ledger.Object{{Key: "event", Value: event}}
			row = append(row, head...)
			return append(row, more...)
		}
		prior := streaks[k]
		switch {
		case s.ok:
			if prior > 0 {
				_ = write(with("review.route_reset", ledger.Field{Key: "reason", Value: "success"}, ledger.Field{Key: "was_benched", Value: prior >= routeBenchAfter}))
			}
		case s.timedOut:
			n := prior + 1
			_ = write(with("review.route_timeout", ledger.Field{Key: "streak", Value: float64(n)}))
			if n == routeBenchAfter {
				_ = write(with("review.route_benched", ledger.Field{Key: "timeouts", Value: float64(n)}))
			}
		}
	}
}

// routeBenched is cfg with this run's benched routes left out (a ledger
// that cannot be read benches nothing).
func (w *TicketRunner) routeBenched(cfg map[string]any) map[string]any {
	rows, err := w.readRows()
	if err != nil {
		return cfg
	}
	return routeBenchConfig(cfg, routeBenchStreaks(rows, w.Key, w.RunID))
}

// writeRouteOutcomes writes the ticket's bench rows against the streaks the
// ledger holds now.
func (w *TicketRunner) writeRouteOutcomes(id string, watch *routeWatch, write func(ledger.Object) error) {
	rows, err := w.readRows()
	if err != nil {
		return
	}
	watch.writeOutcomes(id, routeBenchStreaks(rows, w.Key, w.RunID), write)
}

// reviewSpawn is the review session runner: the ReviewSession seam, else
// session.ReviewSpawn.
func (w *TicketRunner) reviewSpawn(o session.Opts, d session.Deps) func(review.SpawnOpts) (ledger.Object, error) {
	if w.ReviewSession != nil {
		return w.ReviewSession
	}
	return session.ReviewSpawn(o, d)
}
