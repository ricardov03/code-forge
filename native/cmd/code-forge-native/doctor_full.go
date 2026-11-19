package main

// B487/B504/B525: which full `doctor` run (no --quick) Go makes. With a valid
// config Go runs the live checks (doctor_run.go, B525). B787: a config that
// does not check clean (a YAML error, schema issues, domain errors) gets its
// FAIL config row and the no-config rows (B504, doctor_local.go), as a
// config that does not load does: the doctor shows what is wrong instead of
// handing over. A config Go cannot judge (secret-shaped values) keeps the
// general doctor-full-run reason.

// doctorFullRunReason judges the config the full run would load at cwd: 0
// means Go runs it, with the config row(s) in rows and cfg the valid config
// (nil when none loads or it is invalid).
func doctorFullRunReason(cwd string) ([]doctorRow, map[string]any, handoverReason) {
	rows, cfg, reason := doctorConfigRow(cwd)
	if reason != 0 {
		return nil, nil, hDoctorFullRun
	}
	return rows, cfg, 0
}
