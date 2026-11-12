package main

import "github.com/ricardov03/code-forge/native/config"

// B487/B504/B525: which full `doctor` run (no --quick) Go makes. With a valid
// config Go runs the live checks (doctor_run.go, B525). A config that loads
// but fails validation hands over: its tmp.root, daemon setting and worktree
// folder feed the sweep, daemon and indexing rows. A config Go cannot judge
// (schema, secret-shaped values, sandbox dirs, YAML syntax) keeps the general
// doctor-full-run reason. Without a loadable config no live check runs and Go
// makes every row (B504, doctor_local.go).

// doctorFullRunReason judges the config the full run would load at cwd: 0
// means Go runs it, with the config row(s) in rows and cfg the valid config
// (nil when none loads).
func doctorFullRunReason(cwd string) ([]doctorRow, map[string]any, handoverReason) {
	rows, cfg, reason := doctorConfigRow(cwd)
	switch {
	case reason != 0:
		return nil, nil, hDoctorFullRun
	case cfg != nil:
		return rows, cfg, 0
	}
	if _, _, err := config.LoadOrdered(cwd); err == nil {
		return nil, nil, hDoctorFullInvalidConfig
	}
	return rows, nil, 0
}
