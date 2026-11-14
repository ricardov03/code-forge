package main

import (
	"path/filepath"
	"testing"
)

// B651: validate checks each review.on_unavailable route the way a level is
// checked: a model that is not a known id of the route's provider (its own,
// else its level's, else the top-level one; known_extra and seen-in-cache
// count) is a review-on-unavailable-invalid error with exit 1, like a
// review.routes error; an openai route gets the closed-book warning that an
// openai review.routes rule gets.

const b651Levels = `provider: anthropic
levels:
  L0: {model: claude-haiku-4-5-20251001}
  L1: {model: claude-sonnet-5}
  L2: {model: claude-opus-5-5}
  L3: {model: claude-fable-5-1}
`

const b651ClosedBook = " resolves to openai — codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author\n"

func TestB651ValidateOnUnavailableModels(t *testing.T) {
	cases := []struct {
		name, review string
		code         int
		stdout       string
		stderr       string
	}{
		{"known", "review:\n  on_unavailable:\n    - {model: claude-sonnet-5}\n    - {level: L3, model: claude-opus-5-5, provider: anthropic}\n", 0,
			"valid (0 warnings)\n", ""},
		{"unknown-model", "review:\n  on_unavailable:\n    - {model: claude-sonnet-5}\n    - {model: bogus-model-x}\n", 1,
			"invalid (1 error)\n",
			"[code-forge:error] [review-on-unavailable-invalid] review.on_unavailable[1].model is not a known \"anthropic\" id, not in known_extra, and not seen-in-cache\n"},
		// the route's own provider decides which ids are known
		{"model-of-other-provider", "review:\n  on_unavailable:\n    - {model: claude-sonnet-5, provider: xai}\n", 1,
			"invalid (1 error)\n",
			"[code-forge:error] [review-on-unavailable-invalid] review.on_unavailable[0].model is not a known \"xai\" id, not in known_extra, and not seen-in-cache\n"},
		{"known-extra", "known_extra: {anthropic: [claude-new-9]}\nreview:\n  on_unavailable:\n    - {model: claude-new-9}\n", 0,
			"valid (0 warnings)\n", ""},
		{"openai-closed-book", "review:\n  on_unavailable:\n    - {model: gpt-6-sol, provider: openai}\n", 0,
			"valid (1 warning)\n",
			"[code-forge:warn] [closed-book-on-openai] review.on_unavailable[0]" + b651ClosedBook},
		{"openai-unknown", "review:\n  on_unavailable:\n    - {model: gpt-nope, provider: openai}\n", 1,
			"invalid (1 error)\n",
			"[code-forge:error] [review-on-unavailable-invalid] review.on_unavailable[0].model is not a known \"openai\" id, not in known_extra, and not seen-in-cache\n" +
				"[code-forge:warn] [closed-book-on-openai] review.on_unavailable[0]" + b651ClosedBook},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, repo := smallProject(t, b651Levels+c.review)
			b471ValidatePath(t)
			file := filepath.Join(repo, ".code-forge.yml")
			checkSmallCases(t, []smallCase{{[]string{"validate"}, c.code, "[code-forge:info] " + file + ": " + c.stdout, c.stderr}})
		})
	}
}

// A level's provider is the route's when the route names none, and a model
// a refresh has seen is known.
func TestB651ValidateOnUnavailableLevelProviderAndSeenCache(t *testing.T) {
	home, repo := smallProject(t, `provider: anthropic
levels:
  L0: {model: claude-haiku-4-5-20251001}
  L1: {model: claude-sonnet-5}
  L2: {model: claude-opus-5-5}
  L3: {model: grok-4.7, provider: xai}
review:
  on_unavailable:
    - {level: L3, model: claude-new-9}
`)
	b471ValidatePath(t)
	file := filepath.Join(repo, ".code-forge.yml")
	checkSmallCases(t, []smallCase{{[]string{"validate"}, 1, "[code-forge:info] " + file + ": invalid (1 error)\n",
		"[code-forge:error] [review-on-unavailable-invalid] review.on_unavailable[0].model is not a known \"xai\" id, not in known_extra, and not seen-in-cache\n"}})
	writeHomeFile(t, home, ".code-forge/cache/models.json", `{"status":"seen-in-cache","seen":{"xai":["claude-new-9"]}}`)
	checkSmallCases(t, []smallCase{{[]string{"validate"}, 0, "[code-forge:info] " + file + ": valid (0 warnings)\n", ""}})
}

// run reload refuses a config whose chain names an unknown model, the way
// it refuses any validate error; nothing changes.
func TestB651ReloadRefusesUnknownOnUnavailableModel(t *testing.T) {
	f := b473Setup(t, "start-snapshot.json", "", nil)
	writeFileB650(t, f.ws+"/.code-forge.yml", b651Levels+"review:\n  max_rounds_per_file: 3\n  on_unavailable:\n    - {model: bogus-model-x}\n")
	before := f.record(t)
	code, out, errText := runCaptured(t, "run", "reload", "--run", "r-fix")
	b473Expect(t, code, out, errText, 1, "", "run reload: .code-forge.yml is invalid (1 error) — nothing changed; run code-forge validate\n  [review-on-unavailable-invalid]\n")
	if f.record(t) != before {
		t.Fatal("a refused reload changed the run record")
	}
}
