package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// B462: proof tier with a project .code-forge.yml runs in Go (configAt's
// load and validate refusals, proof.tiers.high.paths). Before B462 every
// such call handed over to Node (proof-config).
var b462TierConfigs = map[string]string{
	"c2": `version: 1
`,
	"c3": `version: 1
provider: anthropic
levels:
  L0: {model: a, effort: bogus}
  L1: {model: a, effort: bogus}
  L2: {model: b, provider: xai, effort: ""}
escalation: {stop_at: L2}
review: {multimodel: true, second_provider: xai, routes: "x"}
calibration: {shadow_rate: 0.9}
gates:
  test: ["{test_files}"]
  lint: {argv: [], env: {"1x": 2, OK: 3}, extra: 1}
  repeat_new_tests: 1
  timeout_s: 10
  custom:
    - {name: test, argv: [x, "{changed_files}"], evidence: ["/abs", "ok", ".."]}
    - {argv: [x]}
    - {argv: [y]}
  kinds: {code: [nope, lint], docs: [test]}
secretz: sk-abcdefghijklmnop
`,
	"c4": `version: 1
provider: anthropic
levels:
  L0: {model: a}
  L1: {model: b}
  L2: {model: c}
  L3: {model: d}
proof:
  tiers: {high: {paths: ["src/**", "lib/x.go"]}}
`,
	"c5": `version: 1
foo: [
`,
	"c6": `- a
`,
	"c7": `version: 3
`,
}

func b462TierDir(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".code-forge.yml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestB462ProofTierConfigGo(t *testing.T) {
	cases := []struct {
		config, file, risk string
		code               int
		stdout, stderr     string
	}{
		{"c2", "a", "1", 1, "", "proof tier: .code-forge.yml is invalid (6 error(s) at (root), level-missing) — run code-forge validate\n"},
		{"c3", "a", "1", 1, "", "proof tier: .code-forge.yml is invalid (45 error(s) at (root), /levels, /escalation/stop_at, /review/routes, /gates/lint, /gates/lint/argv, /gates/lint/env, /gates/lint/env/OK, /gates/custom/0/name, /gates/custom/0/argv, /gates/custom/0/evidence/0, /gates/custom/0/evidence/2, /gates/custom/1, /gates/custom/2, /gates/repeat_new_tests, /gates/timeout_s, /calibration/shadow_rate, level-missing, duplicate-level-tuple, stop-at-not-l3, multimodel-effective-l2-collision, gates-not-argv-array, secret-looking-value, shadow-rate-range, effort-not-valid-for-provider, review-routes-invalid) — run code-forge validate\n"},
		{"c4", "src/a.go", "1", 0, "{\"file\":\"src/a.go\",\"tier\":\"high\",\"reason\":\"path\"}\n", ""},
		{"c4", "lib/x.go", "0", 0, "{\"file\":\"lib/x.go\",\"tier\":\"high\",\"reason\":\"path\"}\n", ""},
		{"c4", "other", "0", 0, "{\"file\":\"other\",\"tier\":\"light\",\"reason\":\"default\"}\n", ""},
		{"c4", "other", "0", 0, "{\"file\":\"other\",\"tier\":\"light\",\"reason\":\"default\"}\n", ""},
		{"c4", "", "0", 1, "", "proof tier: tierFor: file must be a non-empty repo-relative path\n"},
		{"c5", "a", "1", 1, "", "proof tier: .code-forge.yml does not load (parse-error) — run code-forge validate\n"},
		{"c6", "a", "1", 1, "", "proof tier: .code-forge.yml does not load (parse-error) — run code-forge validate\n"},
		{"c7", "a", "1", 1, "", "proof tier: .code-forge.yml does not load (migrate-error) — run code-forge validate\n"},
	}
	for _, c := range cases {
		dir := b462TierDir(t, b462TierConfigs[c.config])
		got := b458Go(t, []string{"proof", "tier", "--file", c.file, "--risk", c.risk, "--cwd", dir})
		if got.code != c.code || got.stdout != c.stdout || got.stderr != c.stderr {
			t.Fatalf("%s %s: got %+v", c.config, c.file, got)
		}
	}
}

func TestB462ProofTierConfigMatchesNode(t *testing.T) {
	for name, body := range b462TierConfigs {
		for _, file := range []string{"src/a.go", "lib/x.go", ""} {
			dir := b462TierDir(t, body)
			args := []string{"proof", "tier", "--file", file, "--risk", "0", "--cwd", dir}
			got, want := b458Go(t, args), b458Node(t, args)
			if got.code != want.code || got.stdout != want.stdout || got.stderr != want.stderr || got.crumbs != want.crumbs || !reflect.DeepEqual(got.errors, want.errors) {
				t.Fatalf("%s %q:\ngo   %+v\nnode %+v", name, file, got, want)
			}
		}
	}
}
