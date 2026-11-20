package config

import (
	"errors"
	"fmt"
	"strings"
)

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
	human, _ := cfg["human_check"].(map[string]any)
	ap, _ := human["autopilot"].(map[string]any)
	if v, ok := ap["require_proof"]; ok && v != true {
		return fmt.Errorf("human_check.autopilot.require_proof must be true in v1")
	}
	if ap["enabled"] == true {
		dirs, _ := ap["proof_dirs"].([]any)
		if len(dirs) == 0 {
			return fmt.Errorf("human_check.autopilot.proof_dirs must be non-empty when enabled")
		}
		for _, v := range dirs {
			if s, ok := v.(string); !ok || strings.TrimSpace(s) == "" {
				return fmt.Errorf("human_check.autopilot.proof_dirs must contain non-blank paths")
			}
		}
	}
	return nil
}
