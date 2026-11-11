package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeHomeFile(t *testing.T, home, rel, text string) {
	t.Helper()
	file := filepath.Join(home, rel)
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

const modelsCatalog = "[code-forge:info] anthropic:\n[code-forge:info]   claude-haiku-4-5-20251001\n[code-forge:info]   claude-sonnet-5\n[code-forge:info]   claude-opus-5-5\n[code-forge:info]   claude-fable-5-1\n[code-forge:info]   claude-fable-5-2\n"

// Expected bytes are the Node CLI's output on the same fixture.
func TestB455ModelsMatchesNode(t *testing.T) {
	home, repo := smallProject(t, "known_extra:\n  anthropic: [claude-new, claude-sonnet-5, 3]\n  zz: [q]\n  bad: nope\n")
	writeHomeFile(t, home, ".codex/models_cache.json", `{"models":[{"slug":"gpt-6-sol"},{"slug":"gpt-x-new"},{"slug":""}]}`)
	writeHomeFile(t, home, ".grok/models_cache.json", `{"models":{"a":{"info":{"id":"grok-z"}},"grok-4.7":{}}}`)
	writeHomeFile(t, home, ".code-forge/cache/models.json", `{"status":"seen-in-cache","seen":{"zeta":["z1"],"xai":["grok-old"],"10":["n"],"bad":[1]}}`)
	cache := filepath.Join(home, ".code-forge", "cache", "models.json")
	listing := "[code-forge:info] 10:\n[code-forge:info]   n (seen-in-cache)\n" + modelsCatalog + "[code-forge:info]   claude-new (known_extra)\n[code-forge:info] bad:\n" +
		"[code-forge:info] openai:\n[code-forge:info]   gpt-6-luna\n[code-forge:info]   gpt-6-sol\n[code-forge:info]   gpt-6-astra\n[code-forge:info]   gpt-x-new (seen-in-cache)\n" +
		"[code-forge:info] xai:\n[code-forge:info]   grok-4.7\n[code-forge:info]   grok-4.7-build-fast\n[code-forge:info]   grok-4.6\n[code-forge:info]   grok-4.5\n[code-forge:info]   grok-old (seen-in-cache)\n[code-forge:info]   grok-z (seen-in-cache)\n" +
		"[code-forge:info] zeta:\n[code-forge:info]   z1 (seen-in-cache)\n[code-forge:info] zz:\n[code-forge:info]   q (known_extra)\n"
	checkSmallCases(t, []smallCase{
		{[]string{"models", "-h"}, 0, modelsUsage, ""},
		{[]string{"models", "--refresh"}, 2, "", "[code-forge:error] models --refresh requires --from-cli-caches (the only refresh source in 0.1)\n"},
		{[]string{"models", "--from-cli-caches"}, 2, "", "[code-forge:error] --from-cli-caches requires --refresh\n"},
		{[]string{"models", "--refresh", "--bogus"}, 2, "", "[code-forge:error] unknown argument: --bogus\n"},
		{[]string{"models", "--refresh", "--from-cli-caches"}, 0, "[code-forge:info] refreshed from 2 CLI cache(s) -> " + cache + " (status: seen-in-cache)\n" + listing, ""},
	})
	data, _ := os.ReadFile(cache)
	want := "{\n  \"status\": \"seen-in-cache\",\n  \"updated_at\": \"X\",\n  \"seen\": {\n    \"10\": [\n      \"n\"\n    ],\n    \"zeta\": [\n      \"z1\"\n    ],\n    \"xai\": [\n      \"grok-4.7\",\n      \"grok-old\",\n      \"grok-z\"\n    ],\n    \"openai\": [\n      \"gpt-6-sol\",\n      \"gpt-x-new\"\n    ]\n  }\n}\n"
	got := string(data)
	if at := strings.Index(got, `"updated_at": "`); at > 0 {
		end := strings.Index(got[at+15:], `"`)
		got = got[:at+15] + "X" + got[at+15+end:]
	}
	if got != want {
		t.Fatalf("models.json =\n%s\nwant\n%s", data, want)
	}
	if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte("version: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errText := runCaptured(t, "models")
	if code != 0 || !strings.HasPrefix(out, "[code-forge:info] 10:\n") || errText != "[code-forge:warn] could not read known_extra from "+filepath.Join(repo, ".code-forge.yml")+": migrate-error: migrateConfig: version must be a positive integer, got a string\n" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errText)
	}
}

func TestB455ModelsEmptyRefreshAndHandovers(t *testing.T) {
	home, repo := smallProject(t, "")
	checkSmallCases(t, []smallCase{
		{[]string{"models", "--refresh", "--from-cli-caches"}, 0, "[code-forge:info] anthropic:\n" + strings.TrimPrefix(modelsCatalog, "[code-forge:info] anthropic:\n") +
			"[code-forge:info] openai:\n[code-forge:info]   gpt-6-luna\n[code-forge:info]   gpt-6-sol\n[code-forge:info]   gpt-6-astra\n[code-forge:info] xai:\n[code-forge:info]   grok-4.7\n[code-forge:info]   grok-4.7-build-fast\n[code-forge:info]   grok-4.6\n[code-forge:info]   grok-4.5\n",
			"[code-forge:warn] no CLI caches were found to refresh from (checked ~/.codex and ~/.grok)\n"},
	})
	writeHomeFile(t, home, ".codex/models_cache.json", `{"models":[1,2`)
	expectHandover(t, "models-refresh-invalid-json", "models", "--refresh", "--from-cli-caches")
	_ = os.Remove(filepath.Join(home, ".codex", "models_cache.json"))
	_ = os.Remove(filepath.Join(home, ".code-forge", "cache", "models.json"))
	if err := os.Mkdir(filepath.Join(home, ".code-forge", "cache", "models.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	expectHandover(t, "models-refresh-unwritable", "models", "--refresh", "--from-cli-caches")
	if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte("a: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	expectHandover(t, "config-parse-error", "models")
}
