package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const resolveFixture = `provider: anthropic
review: {allow_open_book_codex: false}
levels:
  L0: {model: claude-haiku-4-5-20251001}
  L1: {model: claude-sonnet-5, effort: {z: 1, a: [2, "x"], "3": n}}
  L2: {model: claude-opus-5-5, effort: high, fallback: [{provider: openai, model: gpt-6-sol, effort: high}, {model: grok-4.7, provider: xai, closed_book: pre}]}
  L3: {model: gpt-6-sol, provider: " OpenAI "}
`

const refusal = "[code-forge:warn] codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author\n"

// Expected bytes are the Node CLI's output on the same fixture.
func TestB455ResolveMatchesNode(t *testing.T) {
	home, _ := smallProject(t, resolveFixture)
	checkSmallCases(t, []smallCase{
		{[]string{"resolve", "--help"}, 0, "usage: code-forge resolve <L0|L1|L2|L3>\n", ""},
		{[]string{"resolve", "-h", "x"}, 0, "usage: code-forge resolve <L0|L1|L2|L3>\n", ""},
		{[]string{"resolve", "L0"}, 0, `{"provider":"anthropic","model":"claude-haiku-4-5-20251001","effort":null,"fallback":[],"cli":"claude"}` + "\n", ""},
		{[]string{"resolve", "L1"}, 0, `{"provider":"anthropic","model":"claude-sonnet-5","effort":{"3":"n","z":1,"a":[2,"x"]},"fallback":[],"cli":"claude"}` + "\n", ""},
		{[]string{"resolve", "L2"}, 0, `{"provider":"anthropic","model":"claude-opus-5-5","effort":"high","fallback":[{"provider":"openai","model":"gpt-6-sol","effort":"high","closed_book":"refused"},{"model":"grok-4.7","provider":"xai","closed_book":"pre"}],"cli":"claude"}` + "\n", refusal},
		{[]string{"resolve", "L3"}, 1, "", "[code-forge:error] resolve: provider \" OpenAI \" (from levels.L3) has no known CLI mapping\n"},
		{[]string{"resolve", "L9"}, 2, "", "[code-forge:error] unknown level \"L9\" — expected one of L0, L1, L2, L3\n"},
		{[]string{"resolve"}, 2, "", "[code-forge:error] usage: resolve <L0|L1|L2|L3>\n"},
		{[]string{"resolve", "L1", "--help"}, 2, "", "[code-forge:error] usage: resolve <L0|L1|L2|L3>\n"},
	})
	crumbs, _ := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "breadcrumbs.json"))
	if !strings.Contains(string(crumbs), `"resolve"`) {
		t.Fatalf("breadcrumbs = %s", crumbs)
	}
	errorsLog, _ := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	if !strings.Contains(string(errorsLog), `"verb":"resolve","sub":null,"flags":[],"exit":2,"kind":"usage"`) {
		t.Fatalf("errors.jsonl = %s", errorsLog)
	}
}

func TestB455ResolveOpenBookAndConfigErrors(t *testing.T) {
	smallProject(t, "provider: openai\nreview: {allow_open_book_codex: true}\nlevels:\n  L2: {model: gpt-6-sol}\n  L1: {model: \"\"}\n  L0: {model: m}\n")
	checkSmallCases(t, []smallCase{
		{[]string{"resolve", "L2"}, 0, `{"provider":"openai","model":"gpt-6-sol","effort":null,"fallback":[],"cli":"codex","closed_book":"open-book (allowed)"}` + "\n", "[code-forge:warn] codex reviewer is not closed-book: it can read files on this machine (review.allow_open_book_codex)\n"},
		{[]string{"resolve", "L1"}, 1, "", "[code-forge:error] resolveLevel: levels.L1 is missing or has no model\n"},
		{[]string{"resolve", "L3"}, 1, "", "[code-forge:error] resolveLevel: levels.L3 is missing or has no model\n"},
	})
	_, repo := smallProject(t, "levels: {L0: {model: m}}\n")
	checkSmallCases(t, []smallCase{{[]string{"resolve", "L0"}, 1, "", "[code-forge:error] resolveLevel: levels.L0 has no provider (neither per-level nor top-level)\n"}})
	if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte("- a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkSmallCases(t, []smallCase{{[]string{"resolve", "L0"}, 1, "", "[code-forge:error] parse-error: config root must be a mapping, got a list\n"}})
	if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte("version: 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	checkSmallCases(t, []smallCase{{[]string{"resolve", "L0"}, 1, "", "[code-forge:error] migrate-error: migrateConfig: config version 2 is newer than this code-forge understands (1) — upgrade the package\n"}})
	_ = os.Remove(filepath.Join(repo, ".code-forge.yml"))
	checkSmallCases(t, []smallCase{{[]string{"resolve", "L0"}, 1, "", "[code-forge:error] not-found: no config file at " + filepath.Join(repo, ".code-forge.yml") + "\n"}})
}

// JS-specific failures stay with Node: a YAML syntax error's library text and
// a fallback that is not a list of mappings.
func TestB455ResolveHandsOverJSOnlyCases(t *testing.T) {
	_, repo := smallProject(t, "provider: anthropic\nlevels:\n  L0: {model: m, fallback: oops}\n  L1: {model: m, fallback: [a]}\n")
	expectHandover(t, "resolve-fallback-shape", "resolve", "L0")
	expectHandover(t, "resolve-fallback-shape", "resolve", "L1")
	if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte("a: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectHandover(t, "config-parse-error", "resolve", "L0")
}
