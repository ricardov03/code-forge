package main

// B487: why the full `doctor` run (no --quick) still goes to Node. With a
// valid config the run makes the live checks (the Jev call, one model call
// per provider CLI role, isolation and path-deny probes, worker start and
// stop): they stay on Node until Go has a Jev client and the probe ports.
// Without a valid config no live check runs, but the rows still need the
// process sweep scan (sweep and memory rows, stale workers) and the tmp-root
// and worker-registry readers, which Go has only on unmerged branches. A
// config Go cannot judge (schema, secret-shaped values, sandbox dirs, YAML
// syntax) keeps the general doctor-full-run reason.

// doctorFullRunReason judges the config the full run would load at cwd.
func doctorFullRunReason(cwd string) handoverReason {
	_, cfg, reason := doctorConfigRow(cwd)
	switch {
	case reason != 0:
		return hDoctorFullRun
	case cfg != nil:
		return hDoctorLiveProbes
	default:
		return hDoctorFullNoConfig
	}
}
