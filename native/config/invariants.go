package config

import "errors"

// ErrInvariants is the refusal for gates.extra.invariants (B402). Older
// schemas accepted the key, but neither engine ever ran its script, so a
// configured check was skipped silently. The schema now refuses the key, and
// verbs that read the config without the schema step refuse it with this text.
var ErrInvariants = errors.New("gates.extra.invariants is not supported: no engine runs it, so its script would be skipped; remove it from .code-forge.yml and run the script as a gates.custom entry ({name, argv})")

// CheckInvariants returns ErrInvariants when cfg sets gates.extra.invariants,
// with any value.
func CheckInvariants(cfg map[string]any) error {
	gates, _ := cfg["gates"].(map[string]any)
	extra, _ := gates["extra"].(map[string]any)
	if _, present := extra["invariants"]; present {
		return ErrInvariants
	}
	return nil
}
