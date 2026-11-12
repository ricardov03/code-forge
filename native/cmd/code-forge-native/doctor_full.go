package main

import "github.com/ricardov03/code-forge/native/config"

// B487/B504: why the full `doctor` run (no --quick) still goes to Node. With a
// valid config the run makes the live checks (the Jev call, one model call
// per provider CLI role, isolation and path-deny probes, worker start and
// stop): they stay on Node until Go has a Jev client and the probe ports. A
// config that loads but fails validation hands over too: its tmp.root, daemon
// setting and worktree folder feed the sweep, daemon and indexing rows. A
// config Go cannot judge (schema, secret-shaped values, sandbox dirs, YAML
// syntax) keeps the general doctor-full-run reason. Without a loadable config
// no live check runs and Go makes every row (B504, doctor_local.go).

// doctorFullRunReason judges the config the full run would load at cwd: 0
// means Go runs it, with the config row in rows.
func doctorFullRunReason(cwd string) ([]doctorRow, handoverReason) {
	rows, cfg, reason := doctorConfigRow(cwd)
	switch {
	case reason != 0:
		return nil, hDoctorFullRun
	case cfg != nil:
		return nil, hDoctorLiveProbes
	}
	if _, _, err := config.LoadOrdered(cwd); err == nil {
		return nil, hDoctorFullInvalidConfig
	}
	return rows, 0
}
