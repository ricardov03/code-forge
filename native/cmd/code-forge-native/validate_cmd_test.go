package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b471ValidateCases hold Node's validate output on the same files, with the
// project directory written as "@". PATH holds only a claude stub.
var b471ValidateCases = []struct {
	name, config, pipeline, stdout, stderr string
	code                                   int
}{
	{name: "ok", code: 0,
		config: `provider: anthropic
levels:
  L0: {model: claude-haiku-4-5-20251001}
  L1: {model: claude-sonnet-5}
  L2: {model: claude-opus-5-5}
  L3: {model: claude-fable-5-1}
`,
		pipeline: ``,
		stdout: `[code-forge:info] @/.code-forge.yml: valid (0 warnings)
`,
		stderr: ``},
	{name: "warn1", code: 0,
		config: `provider: anthropic
orchestrator: L3
caps: {coders: 3}
levels:
  L0: {model: claude-haiku-4-5-20251001, effort: low}
  L1: {model: claude-new-9}
  L2: {model: claude-opus-5-5, fallback: [{provider: xai, model: grok-9}, {provider: anthropic, model: claude-sonnet-5}]}
  L3: {model: claude-fable-5-1}
`,
		pipeline: ``,
		stdout: `[code-forge:info] @/.code-forge.yml: valid (4 warnings)
`,
		stderr: `[code-forge:warn] [unknown-model-id] levels.L1.model is not a known "anthropic" id, not in known_extra, and not seen-in-cache
[code-forge:warn] [orchestrator-l3-multimodel-off] orchestrator is L3 but review.multimodel is off
[code-forge:warn] [fallback-unknown-or-cli-absent] levels.L2.fallback[0] (provider "xai") has an unknown model id and has no CLI on PATH
[code-forge:warn] [caps-coders-exceeds-cap] caps.coders is above the R10 cap of 2 concurrent coders — allowed, but recorded as a deviation
`},
	{name: "errs2", code: 1,
		config: `provider: openai
engine: subprocess
review: {multimodel: true, second_provider: openai}
levels:
  L0: {model: gpt-6-luna, effort: max}
  L1: {model: gpt-6-luna, effort: max}
  L2: {model: gpt-6-sol, fallback: [{provider: openai, model: gpt-6-astra, effort: xhigh}, {provider: xai, model: grok-4.7, effort: ""}]}
  L3: {model: gpt-6-astra}
`,
		pipeline: ``,
		stdout: `[code-forge:info] @/.code-forge.yml: invalid (8 errors)
`,
		stderr: `[code-forge:error] [duplicate-level-tuple] levels.L0 and levels.L1 resolve to the same (provider, model, effort)
[code-forge:error] [multimodel-effective-l2-collision] the effective L2 provider ("openai") equals the second reviewer's effective L2 provider (review.second_levels.L2.provider, else review.second_provider) — multimodel needs two different providers
[code-forge:error] [judge-family-collision] the L3 judge's provider ("openai") equals both reviewers's provider — the judge can't independently check a reviewer it shares a family with
[code-forge:error] [engine-subprocess-no-cli] engine is "subprocess" but no CLI was found on PATH for provider(s): "openai"
[code-forge:error] [effort-not-valid-for-provider] levels.L0.effort "max" is not valid for provider openai; use one of: minimal, low, medium, high
[code-forge:error] [effort-not-valid-for-provider] levels.L1.effort "max" is not valid for provider openai; use one of: minimal, low, medium, high
[code-forge:error] [effort-not-valid-for-provider] levels.L2.fallback[0].effort "xhigh" is not valid for provider openai; use one of: minimal, low, medium, high
[code-forge:error] [effort-not-valid-for-provider] levels.L2.fallback[1].effort is empty; provider xai needs a non-empty effort or none at all
[code-forge:warn] [fallback-unknown-or-cli-absent] levels.L2.fallback[0] (provider "openai") has no CLI on PATH
[code-forge:warn] [fallback-unknown-or-cli-absent] levels.L2.fallback[1] (provider "xai") has no CLI on PATH
[code-forge:warn] [closed-book-on-openai] levels.L2 resolves to openai — codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author
[code-forge:warn] [closed-book-on-openai] levels.L2.fallback[0] resolves to openai — codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author
[code-forge:warn] [closed-book-on-openai] levels.L3 resolves to openai — codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author
[code-forge:warn] [closed-book-on-openai] review.second_levels.L2 (else review.second_provider) resolves to openai — codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author
`},
	{name: "errs3", code: 1,
		config: `provider: anthropic
review: {multimodel: true, allow_open_book_codex: true}
orchestrator: L3
levels:
  L0: {model: claude-haiku-4-5-20251001}
  L1: {model: claude-sonnet-5}
  L2: {model: claude-opus-5-5, provider: openai}
  L3: {model: claude-fable-5-1, provider: openai, fallback: [{provider: openai, model: gpt-6-sol}]}
`,
		pipeline: ``,
		stdout: `[code-forge:info] @/.code-forge.yml: invalid (2 errors)
`,
		stderr: `[code-forge:error] [multimodel-missing-second-provider] review.multimodel is true but review.second_provider is empty
[code-forge:error] [judge-family-collision] the L3 judge's provider ("openai") equals reviewer 1 (L2)'s provider — the judge can't independently check a reviewer it shares a family with
[code-forge:warn] [unknown-model-id] levels.L2.model is not a known "openai" id, not in known_extra, and not seen-in-cache
[code-forge:warn] [unknown-model-id] levels.L3.model is not a known "openai" id, not in known_extra, and not seen-in-cache
[code-forge:warn] [fallback-unknown-or-cli-absent] levels.L3.fallback[0] (provider "openai") has no CLI on PATH
[code-forge:warn] [closed-book-on-openai] levels.L2 resolves to openai — codex reviewer is not closed-book: it can read files on this machine (review.allow_open_book_codex)
[code-forge:warn] [closed-book-on-openai] levels.L3 resolves to openai — codex reviewer is not closed-book: it can read files on this machine (review.allow_open_book_codex)
[code-forge:warn] [closed-book-on-openai] levels.L3.fallback[0] resolves to openai — codex reviewer is not closed-book: it can read files on this machine (review.allow_open_book_codex)
`},
	{name: "gates2", code: 1,
		config: `provider: anthropic
levels:
  L0: {model: claude-haiku-4-5-20251001}
  L1: {model: claude-sonnet-5}
  L2: {model: claude-opus-5-5}
  L3: {model: claude-fable-5-1}
gates:
  test: ["{test_files}"]
  lint: ["{changed_files}", x]
  types: null
  custom:
    - {name: e2e, argv: [make, e2e]}
    - {name: e2e, argv: [make]}
  kinds:
    code: [test, types, nope, e2e]
`,
		pipeline: ``,
		stdout: `[code-forge:info] @/.code-forge.yml: invalid (5 errors)
`,
		stderr: `[code-forge:error] [gates-not-argv-array] gates.test: '{test_files}' cannot be the only item (the full suite would run an empty command)
[code-forge:error] [gates-not-argv-array] gates.lint: '{changed_files}' cannot be the command (argv[0])
[code-forge:error] [gates-not-argv-array] gates.custom[1] (e2e).name must be unique
[code-forge:error] [gates-not-argv-array] gates.kinds.code: types is a null fixed gate
[code-forge:error] [gates-not-argv-array] gates.kinds.code: nope is an unknown gate name
`},
	{name: "runners2", code: 1,
		config: `provider: anthropic
levels:
  L0: {model: claude-haiku-4-5-20251001}
  L1: {model: claude-sonnet-5}
  L2: {model: claude-opus-5-5}
  L3: {model: claude-fable-5-1}
proof:
  runners:
    - {match: ["**/*.java"], adapter: junit, argv: [mvn, "{test_file}", "x{report}", "{scheme}"]}
    - {match: ["**/*.swift"], adapter: xcodebuild, argv: [xcodebuild, "{scheme}", "{workspace}"], xcode: {scheme: App, destination: "a", test_target: T, project: P}}
    - {match: ["**/*.swift"], adapter: swift, argv: [swift, test]}
`,
		pipeline: ``,
		stdout: `[code-forge:info] @/.code-forge.yml: invalid (10 errors)
`,
		stderr: `[code-forge:error] [proof-runners-invalid] proof.runners[0].argv[2]: a token must be a whole argv item (give it as its own item)
[code-forge:error] [proof-runners-invalid] proof.runners[0].argv[3] is not a token of the junit adapter ({test_file}, {test_id}, {test_filter}, {report})
[code-forge:error] [proof-runners-invalid] proof.runners[0].argv needs a {report} item (the junit adapter reads the report code-forge places there)
[code-forge:error] [proof-runners-invalid] proof.runners[1].argv needs a {destination} item (the xcodebuild adapter fills it from xcode.destination, the one the precheck checks)
[code-forge:error] [proof-runners-invalid] proof.runners[1].argv needs a {only_testing} item (a proof must name its test)
[code-forge:error] [proof-runners-invalid] proof.runners[1].argv needs a {result_bundle} item (the xcodebuild adapter reads the result bundle code-forge places there)
[code-forge:error] [proof-runners-invalid] proof.runners[1].argv has {workspace} but proof.runners[1].xcode.workspace is not set
[code-forge:error] [proof-runners-invalid] proof.runners[1].xcode.project is set but proof.runners[1].argv has no {project} item
[code-forge:error] [proof-runners-invalid] proof.runners[2].argv needs a {report} item (the swift adapter reads the report code-forge places there)
[code-forge:error] [proof-runners-invalid] proof.runners[2].argv needs a test token ({test_file}, {test_id}, {test_filter}): a proof must name its test
`},
	{name: "routes", code: 1,
		config: `provider: anthropic
levels:
  L0: {model: claude-haiku-4-5-20251001}
  L1: {model: claude-sonnet-5}
  L2: {model: claude-opus-5-5}
  L3: {model: claude-fable-5-1}
review:
  multimodel: true
  second_provider: xai
  routes:
    - {min_lines: 400, model: grok-4.7, provider: xai}
    - {min_lines: 400, model: gpt-6-sol, provider: openai, fallback: [{provider: openai, model: gpt-6-luna}]}
    - {level: L3, min_lines: 100, model: claude-opus-5-5, second: {provider: openai, model: gpt-6-sol}}
`,
		pipeline: ``,
		stdout: `[code-forge:info] @/.code-forge.yml: invalid (3 errors)
`,
		stderr: `[code-forge:error] [judge-family-collision] the L3 judge's provider ("anthropic") equals reviewer 1 (L2)'s provider — the judge can't independently check a reviewer it shares a family with
[code-forge:error] [review-routes-invalid] review.routes[0].provider (else provider) equals the second reviewer's effective L2 provider (review.second_levels.L2.provider, else review.second_provider) — multimodel needs two different providers
[code-forge:error] [review-routes-invalid] review.routes[1] has the same level and min_lines as review.routes[0]
[code-forge:warn] [closed-book-on-openai] review.routes[1] resolves to openai — codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author
[code-forge:warn] [closed-book-on-openai] review.routes[1].fallback[0] resolves to openai — codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author
[code-forge:warn] [closed-book-on-openai] review.routes[2].second resolves to openai — codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author
`},
	{name: "pipe2", code: 1,
		config: `provider: anthropic
levels:
  L0: {model: claude-haiku-4-5-20251001}
  L1: {model: claude-sonnet-5}
  L2: {model: claude-opus-5-5}
  L3: {model: claude-fable-5-1}
`,
		pipeline: `root: .
steps:
  - {id: a, skill: s, outcome: a.json, approval: none, needs: [b, zz]}
  - {id: b, skill: s, outcome: b.json, approval: none, needs: [a]}
  - {id: a, skill: s, outcome: c.json, approval: none}
`,
		stdout: ``,
		stderr: `[code-forge:error] duplicate step id "a"
[code-forge:error] step "a": unknown need "zz"
`},
	{name: "pipe4", code: 1,
		config: `provider: anthropic
levels:
  L0: {model: claude-haiku-4-5-20251001}
  L1: {model: claude-sonnet-5}
  L2: {model: claude-opus-5-5}
  L3: {model: claude-fable-5-1}
`,
		pipeline: `DIR`,
		stdout:   ``,
		stderr: `[code-forge:error] Cannot read pipeline (EISDIR)
`},
}

func b471ValidatePath(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

// Every domain rule's message, order, severity and exit code match Node.
func TestB471ValidateMatchesNode(t *testing.T) {
	for _, c := range b471ValidateCases {
		t.Run(c.name, func(t *testing.T) {
			_, repo := smallProject(t, c.config)
			b471ValidatePath(t)
			switch c.pipeline {
			case "":
			case "DIR":
				if err := os.Mkdir(filepath.Join(repo, "code-forge.pipeline.yml"), 0o700); err != nil {
					t.Fatal(err)
				}
			default:
				writeHomeFile(t, repo, "code-forge.pipeline.yml", c.pipeline)
			}
			code, out, errText := runCaptured(t, "validate")
			stdout, stderr := strings.ReplaceAll(c.stdout, "@", repo), strings.ReplaceAll(c.stderr, "@", repo)
			if code != c.code || out != stdout || errText != stderr {
				t.Fatalf("code=%d\nstdout=%q\nstderr=%q\nwant code=%d\nstdout=%q\nstderr=%q", code, out, errText, c.code, stdout, stderr)
			}
		})
	}
}

// A model id a refresh has seen lifts the unknown-model-id warning.
func TestB471ValidateSeenInCacheLiftsUnknownModel(t *testing.T) {
	home, repo := smallProject(t, "provider: anthropic\nlevels:\n  L0: {model: claude-haiku-4-5-20251001}\n  L1: {model: claude-new-9}\n  L2: {model: claude-opus-5-5}\n  L3: {model: claude-fable-5-1}\n")
	b471ValidatePath(t)
	file := filepath.Join(repo, ".code-forge.yml")
	checkSmallCases(t, []smallCase{{[]string{"validate"}, 0, "[code-forge:info] " + file + ": valid (1 warning)\n",
		"[code-forge:warn] [unknown-model-id] levels.L1.model is not a known \"anthropic\" id, not in known_extra, and not seen-in-cache\n"}})
	writeHomeFile(t, home, ".code-forge/cache/models.json", `{"status":"seen-in-cache","seen":{"anthropic":["claude-new-9"]}}`)
	checkSmallCases(t, []smallCase{{[]string{"validate"}, 0, "[code-forge:info] " + file + ": valid (0 warnings)\n", ""}})
}

// Flag misuse exits 2; --file resolves against the working directory; a
// missing file is a load error with exit 1.
func TestB471ValidateArgsAndLoadErrors(t *testing.T) {
	_, repo := smallProject(t, "")
	b471ValidatePath(t)
	writeHomeFile(t, repo, "sub/c.yml", b471ValidateCases[0].config)
	checkSmallCases(t, []smallCase{
		{[]string{"validate", "--file"}, 2, "", "[code-forge:error] --file requires a path argument\n"},
		{[]string{"validate", "--file", "--x"}, 2, "", "[code-forge:error] --file requires a path argument\n"},
		{[]string{"validate", "--bogus"}, 2, "", "[code-forge:error] unknown argument: --bogus\n"},
		{[]string{"validate"}, 1, "", "[code-forge:error] not-found: no config file at " + filepath.Join(repo, ".code-forge.yml") + "\n"},
		{[]string{"validate", "--file", "sub/../sub/c.yml"}, 0, "[code-forge:info] " + filepath.Join(repo, "sub", "c.yml") + ": valid (0 warnings)\n", ""},
	})
}

// The cases Go does not port go to Node with their own reasons.
func TestB471ValidateHandsOverUnportedCases(t *testing.T) {
	levels := "levels:\n  L0: {model: claude-haiku-4-5-20251001}\n  L1: {model: claude-sonnet-5}\n  L2: {model: claude-opus-5-5}\n  L3: {model: claude-fable-5-1}\n"
	for _, c := range []struct{ config, reason string }{
		{"provider: anthropic\nbogus: 1\n" + levels, "validate-schema-issues"},
		{"provider: anthropic\ngates: {test: [npm, sk-abcdefghijklmnopqrstu]}\n" + levels, "validate-secret-values"},
		{"provider: anthropic\ncoder: {sandbox: {add_dirs: [\"/\"]}}\n" + levels, "validate-sandbox-dirs"},
		{"provider: [\n", "config-parse-error"},
	} {
		smallProject(t, c.config)
		expectHandover(t, c.reason, "validate")
	}
}
