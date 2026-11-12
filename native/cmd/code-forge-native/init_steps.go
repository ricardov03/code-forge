package main

// B548: the init steps after the write in Go (src/install/wizard/run.mjs):
// the Jev key found in the environment or the file key store (step 5), the
// recommended tools (step 1), the model-id refresh (step 3) and the quick
// doctor (step 9). Every hand-over here is decided before init writes.

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

// initInstallTool is step 1's installer: exec(argv, {timeoutMs: 600_000}).
// Tests stand in for it.
var initInstallTool = func(argv []string) childResult { return runChild(argv, 600*time.Second) }

// initJevAuto is what resolveKey('jev', {store, env}) gives a run with no
// Jev flag and no keys.jev: the env variable, else the file store
// (CODE_FORGE_KEY_BACKEND=file) read with the 24 h cap. pre is the store's
// own side effect (an expired entry dropped, a longer expiry cut), run before
// anything else, like Node's read. The default chain reads the OS keychain:
// that stays with Node (init-jev-keychain), as does a store file or index Go
// cannot read back like Node (init-jev-key).
type initJevAuto struct {
	found       bool
	ref, source string
	secrets     []string
	pre         func()
}

func initJevAutoPlan(home string) (initJevAuto, handoverReason) {
	if value := os.Getenv("CODE_FORGE_KEY_JEV"); value != "" {
		return initJevAuto{found: true, ref: "env:CODE_FORGE_KEY_JEV", source: "env", secrets: []string{value}}, 0
	}
	if os.Getenv("CODE_FORGE_KEY_BACKEND") != "file" {
		return initJevAuto{}, hInitJevKeychain
	}
	keyFile := filepath.Join(home, ".code-forge", "store", "jev.key")
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		// missing, or a read error the store reports and passes over
		return initJevAuto{}, 0
	}
	if !utf8.Valid(raw) {
		return initJevAuto{}, hInitJevKey
	}
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" {
		return initJevAuto{}, 0
	}
	value, exp, ok := decodeKeyEntry(text)
	if !ok {
		return initJevAuto{}, hInitJevKey
	}
	now := doctorNow()
	latest := now + 24*3_600_000
	found := initJevAuto{found: true, ref: "user", source: "file", secrets: []string{value}}
	switch {
	case exp != nil && *exp <= now:
		if !keyIndexReadable(home, "jev", false) {
			return initJevAuto{}, hInitJevKey
		}
		return initJevAuto{secrets: []string{value}, pre: func() {
			_ = os.Remove(keyFile)
			_ = updateKeyIndex(home, func(index *ledger.Object) { index.Delete("jev") })
		}}, 0
	case exp != nil && *exp > latest:
		if !keyIndexReadable(home, "jev", true) {
			return initJevAuto{}, hInitJevKey
		}
		found.pre = func() {
			if writePrivateFile(keyFile, encodeKeyEntry(value, latest)) == nil {
				_ = updateKeyIndex(home, func(index *ledger.Object) {
					if row, ok := index.Get("jev").(ledger.Object); ok {
						row = row.Clone()
						row.Set("backend", "file")
						row.Set("exp", latest)
						index.Set("jev", row)
					}
				})
			}
		}
	}
	return found, 0
}

// initYesTools is values.yes_tools: every --yes-tool, once, in flag order.
func initYesTools(parsed initParsed) []string {
	var out []string
	for _, g := range parsed.given {
		if id, ok := g.value.(string); ok && g.flag == "--yes-tool" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// initFailureReason is failureReason in src/install/tools.mjs.
func initFailureReason(res childResult) string {
	switch {
	case res.timedOut:
		return "timed out"
	case res.signal != "":
		return "signal " + res.signal
	case res.code != nil:
		return "exit " + strconv.Itoa(*res.code)
	}
	return "did not start"
}

// initToolLines is step 1: each recommended tool present, installed after
// its --yes-tool, or missing with its install command or hint.
func initToolLines(solo bool, yes []string) []string {
	var lines []string
	for _, tool := range toolRows {
		if (tool.id == "solo" && solo) || detectTool(tool) {
			lines = append(lines, "tool "+tool.id+": present")
			continue
		}
		argv, hint := toolInstallPlan(tool)
		switch {
		case argv != nil && slices.Contains(yes, tool.id):
			res := initInstallTool(argv)
			result := "installed"
			if !res.ok {
				result = "install failed (" + initFailureReason(res) + ")"
			}
			lines = append(lines, "tool "+tool.id+": "+result+" ("+strings.Join(argv, " ")+")")
		case argv != nil:
			lines = append(lines, "tool "+tool.id+": missing — install with: "+strings.Join(argv, " ")+" (or: code-forge tools install)")
		default:
			lines = append(lines, "tool "+tool.id+": missing — "+hint+" (or: code-forge tools install)")
		}
	}
	return lines
}

// initModelsLine is step 3: refreshFromCliCaches({home}). A cache Go cannot
// write like Node keeps the hand-over (init-models, decided in the plan).
func initModelsLine(home string) (string, error) {
	res, err := config.RefreshFromCLICaches(home)
	if err != nil {
		return "", err
	}
	return "models: " + strconv.Itoa(len(res.SourcesRead)) + " CLI cache(s) read", nil
}

// initDoctorPlan is step 9 decided before init writes: runDoctor({cwd,
// quick: true}) on the config init leaves in cwd. Written, it is the text
// init writes (judged as checkConfig judges the file); unchanged, the file
// on disk. The doctor's own hand-over reasons hand init over before it
// writes anything.
func initDoctorPlan(cwd, home string, written bool, cfg map[string]any, order config.Ordered) (*doctorQuick, handoverReason) {
	var rows []doctorRow
	var loaded map[string]any
	var reason handoverReason
	if written {
		rows, loaded, reason = doctorConfigJudge(cfg, order, nil)
	} else {
		rows, loaded, reason = doctorConfigRow(cwd)
	}
	if reason != 0 {
		return nil, reason
	}
	return doctorQuickPlan(rows, loaded, home, doctorNow())
}

// initDoctorCounts is init's doctor summary {ok, counts}.
func initDoctorCounts(rows []doctorRow) (ledger.Object, bool) {
	c := doctorCounts(rows)
	counts := ledger.Object{}
	for _, s := range []string{"OK", "WARN", "FAIL", "INFO"} {
		counts = append(counts, ledger.Field{Key: s, Value: float64(c[s])})
	}
	return ledger.Object{{Key: "ok", Value: c["FAIL"] == 0}, {Key: "counts", Value: counts}}, c["FAIL"] == 0
}
