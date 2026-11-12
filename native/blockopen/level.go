package blockopen

import (
	"errors"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/sign"
)

// ErrLevelLedger: the ledger could not be read for the recorded level; Node
// prints V8's JSON.parse message, which Go does not reproduce (B581).
var ErrLevelLedger = errors.New("block open: the ledger cannot be read for the recorded level")

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
		// Node's message is V8's JSON.parse text for a torn middle line.
		return "", "", nil, ErrLevelLedger
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
			var err error
			if key, err = loadKey(home, run); err != nil {
				return "", "", nil, err
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
	return level, "autopilot", nil, nil
}
