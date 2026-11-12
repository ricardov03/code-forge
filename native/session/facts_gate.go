package session

import "os"

// GatePreflight (B568) reads what budgetGate reads for a run-pinned session
// before anything is written and reports (IsUnsupported) a Date.parse case Go
// may read differently from Node: an autopilot grant's until or started_at,
// or a counted ledger row's ts. A caller hands over here, before its first
// write, instead of in the middle of the run.
func GatePreflight(o Opts, deps Deps) error {
	if o.Run == nil || *o.Run == "" {
		return nil
	}
	home := deps.Home
	if home == "" {
		home = os.Getenv("HOME")
	}
	g := gateEnv{o: o, deps: deps, home: home}
	p, _, err := readCategoryPlan(home, *o.Run, o.Role, g.nowTime())
	if err != nil || p == nil {
		return err
	}
	slug := o.Slug
	if slug == "" {
		slug, _ = p.project.(string)
	}
	rows, err := g.readRows(slug)
	if err != nil || rows == nil {
		return nil
	}
	_, err = categorySpend(rows, *o.Run, p.category, p)
	return err
}
