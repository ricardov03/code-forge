package blockopen

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/blockops"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/sign"
)

// PickLevel reads only local state. Brief runs use rules; other runs use the last
// signed recorded level. No model, worker or daemon participates in this choice.
func PickLevel(home, run, id string, flags map[string]any, record ledger.Object, cfg map[string]any) (string, string, ledger.Object, error) {
	if level, ok := flags["level"].(string); ok {
		return level, "flag", nil, nil
	}
	if record.Get("mode") == "brief" {
		lines := int64(0)
		if raw, ok := flags["lines"].(string); ok {
			n, err := strconv.ParseInt(raw, 10, 64)
			if !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(raw) || err != nil || n > 9007199254740991 {
				return "", "", nil, refusal("usage", "--lines must be a positive integer")
			}
			lines = n
		}
		files, _ := flags["owned"].([]string)
		proof, _ := cfg["proof"].(map[string]any)
		tiers, _ := proof["tiers"].(map[string]any)
		highTier, _ := tiers["high"].(map[string]any)
		high, _ := highTier["paths"].([]any)
		migration, policy, floor := false, false, false
		paths := []any{}
		for _, file := range unique(files) {
			paths = append(paths, file)
			// JS non-Unicode /i does not fold non-ASCII letters into ASCII.
			lower := strings.Map(func(r rune) rune {
				if r >= 'A' && r <= 'Z' {
					return r + ('a' - 'A')
				}
				return r
			}, file)
			migration = migration || strings.Contains(lower, "migration")
			policy = policy || regexp.MustCompile(`polic(y|ies)|middleware`).MatchString(lower)
			floor = floor || owned.OwnsFile(high, nil, file)
		}
		for _, v := range high {
			if h, ok := v.(string); ok && h != "" {
				floor = floor || owned.OwnsFile(paths, nil, h)
			}
		}
		facts := ledger.Object{{Key: "filesChanged", Value: len(paths)}, {Key: "linesAdded", Value: lines}, {Key: "touchesMigration", Value: migration}, {Key: "touchesPolicyOrMiddleware", Value: policy}, {Key: "pathFloorHit", Value: floor}}
		lane := "L0"
		if migration || policy || floor {
			lane = "L2"
		} else if len(paths) > 4 || lines > 200 {
			lane = "L1"
		}
		return lane, "rules", ledger.Object{{Key: "question", Value: "lane"}, {Key: "answer", Value: lane}, {Key: "source", Value: "rules"}, {Key: "facts", Value: facts}}, nil
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), text(record.Get("project")))
	if err != nil {
		return "", "", nil, blockops.ErrHandover
	}
	level := ""
	found := false
	var key []byte
	for _, v := range rows {
		r, ok := v.(map[string]any)
		if !ok || r["event"] != "autopilot.level" || r["run"] != run || r["block"] != id {
			continue
		}
		candidate, ok := r["level"].(string)
		if !ok {
			continue
		}
		if key == nil {
			file := filepath.Join(home, ".code-forge/runs", run+".key")
			info, err := os.Stat(file)
			if err != nil || info.Mode().Perm()&0077 != 0 {
				return "", "", nil, blockops.ErrHandover
			}
			key, err = os.ReadFile(file)
			if err != nil || len(key) != 32 {
				return "", "", nil, blockops.ErrHandover
			}
		}
		verified, err := sign.VerifyRow(r, key)
		if err == nil && verified.OK {
			level, found = candidate, true
		}
	}
	if !found {
		return "", "", nil, refusal("usage", "no --level given and no autopilot level recorded for "+id)
	}
	// The CLI already delegates an explicit trailing-newline level because JS's
	// regexp end anchor differs from Go's. Recorded levels need the same guard.
	if strings.HasSuffix(level, "\n") {
		return "", "", nil, blockops.ErrHandover
	}
	return level, "autopilot", nil, nil
}
