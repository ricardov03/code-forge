package main

import (
	"path/filepath"
	"testing"
)

// B655: doctor judges a review.on_unavailable route model with the same
// seen-in-cache list validate reads (~/.code-forge/cache/models.json), so a
// chain validate passes is not a doctor config FAIL. Only that rule reads the
// cache: a level model known only from the cache stays a doctor warning, as
// Node's checkConfig gives it.

const b655Chain = "provider: anthropic\n" + b474Levels + "review:\n  on_unavailable:\n    - {model: claude-new-9}\n"

func TestB655DoctorAgreesWithValidateOnSeenOnUnavailableModel(t *testing.T) {
	home, repo := b474Project(t, b655Chain, 0)
	file := filepath.Join(repo, ".code-forge.yml")
	unknown := "review.on_unavailable[0].model is not a known \"anthropic\" id, not in known_extra, and not seen-in-cache"

	// no cache: both refuse the chain
	b471ValidatePath(t)
	checkSmallCases(t, []smallCase{
		{[]string{"validate"}, 1, "[code-forge:info] " + file + ": invalid (1 error)\n", "[code-forge:error] [review-on-unavailable-invalid] " + unknown + "\n"},
		{[]string{"doctor", "--quick"}, 1, "FAIL config: 1 error(s); first: " + unknown + "\n" + b474NoLinks + b474NoKey, b474Summary(0, 2, 1)},
	})

	// the model is seen in the cache: both pass it
	writeHomeFile(t, home, ".code-forge/cache/models.json", `{"status":"seen-in-cache","seen":{"anthropic":["claude-new-9"]}}`)
	checkSmallCases(t, []smallCase{
		{[]string{"validate"}, 0, "[code-forge:info] " + file + ": valid (0 warnings)\n", ""},
		{[]string{"doctor", "--quick"}, 0, "OK   config: valid\n" + b474NoLinks + b474NoKey, b474Summary(1, 2, 0)},
	})

	// seen under another provider only: both still refuse it
	writeHomeFile(t, home, ".code-forge/cache/models.json", `{"status":"seen-in-cache","seen":{"xai":["claude-new-9"]}}`)
	checkSmallCases(t, []smallCase{
		{[]string{"validate"}, 1, "[code-forge:info] " + file + ": invalid (1 error)\n", "[code-forge:error] [review-on-unavailable-invalid] " + unknown + "\n"},
		{[]string{"doctor", "--quick"}, 1, "FAIL config: 1 error(s); first: " + unknown + "\n" + b474NoLinks + b474NoKey, b474Summary(0, 2, 1)},
	})
}

// A level model known only from the cache keeps Node's doctor warning
// (checkConfig runs validateConfig without seenInCache).
func TestB655DoctorLevelSeenModelStillWarns(t *testing.T) {
	home, _ := b474Project(t, "provider: anthropic\nlevels:\n  L0: {model: claude-haiku-4-5-20251001}\n  L1: {model: claude-new-9}\n  L2: {model: claude-opus-5-5}\n  L3: {model: claude-fable-5-1}\n", 0)
	writeHomeFile(t, home, ".code-forge/cache/models.json", `{"status":"seen-in-cache","seen":{"anthropic":["claude-new-9"]}}`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0,
		"WARN config: valid with 1 warning(s); first: levels.L1.model is not a known \"anthropic\" id, not in known_extra, and not seen-in-cache\n" + b474NoLinks + b474NoKey, b474Summary(0, 3, 0)}})
}

// init cannot carry a review.on_unavailable list in Go yet: a project
// config holding one hands init to Node (init-config-shape) before Go
// validates anything, seen in the cache or not. Node's init has no chain
// rule, so it keeps the chain. (Go's init validator reads the same cache for
// the day it serves such a config.)
func TestB655InitHandsOverAChain(t *testing.T) {
	saved := initStdinTTY
	initStdinTTY = func() bool { return false }
	t.Cleanup(func() { initStdinTTY = saved })
	for _, seen := range []bool{false, true} {
		home, _ := smallProject(t, b655Chain)
		if seen {
			writeHomeFile(t, home, ".code-forge/cache/models.json", `{"status":"seen-in-cache","seen":{"anthropic":["claude-new-9"]}}`)
		}
		expectHandover(t, "init-config-shape", "init", "--no-interaction", "--no-jev")
	}
}
