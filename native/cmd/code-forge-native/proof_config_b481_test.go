package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// B481: proof configs with proof.runners, review.routes under multimodel,
// coder.sandbox.add_dirs or merge-gate settings ran in Node before B481
// (proof-config). Go now counts those rules as validateConfig does.

const b481Levels = `version: 1
provider: anthropic
levels:
  L0: {model: a}
  L1: {model: b}
  L2: {model: c}
  L3: {model: d}
`

// b481ConfigBodies builds the configs; sandbox entries name real paths of
// this machine (the run temp base, a symlink to /, a symlink loop).
func b481ConfigBodies(t *testing.T) map[string]string {
	t.Helper()
	links := t.TempDir()
	if err := os.Symlink("/", filepath.Join(links, "rootlink")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(links, "loop"), filepath.Join(links, "loop")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(links, "plain"), 0o700); err != nil {
		t.Fatal(err)
	}
	tmpBase := filepath.Join(proofOSTmpdir(), "code-forge")
	quote := func(s string) string { return `"` + s + `"` }
	return map[string]string{
		"runners-ok": b481Levels + `proof:
  runners:
    - {match: ["**/*.java"], adapter: junit, argv: [mvn, test, "{test_filter}", "{report}"], env: {A: b}, timeout_s: 600}
    - {match: ["*.swift"], adapter: xcodebuild, argv: [xcodebuild, "{scheme}", "{destination}", "{only_testing}", "{result_bundle}", "{project}"], xcode: {scheme: S, destination: D, test_target: T, project: P}}
`,
		"runners-empty": b481Levels + "proof:\n  runners: []\n",
		"runners-null":  b481Levels + "proof:\n  runners:\n",
		"runners-bad": b481Levels + `proof:
  runners:
    - 5
    - {match: [], adapter: nope, argv: ["{report}", "x{test_id}", "{nope}", "{scheme}"], env: {"1x": a, OK: 3}, timeout_s: 30, extra: 1}
    - {match: [a, ""], adapter: swift, argv: [swift, test], xcode: {scheme: S}}
    - {match: [a], adapter: xcodebuild, argv: [x, "{workspace}", "{test_file}"], xcode: {scheme: "-s", destination: "", test_target: "a/b", workspace: W, project: P, other: 1}}
    - {match: [a], adapter: xcodebuild, argv: [x]}
    - {match: [a], adapter: xcodebuild, argv: [x, "{project}"], xcode: 7}
    - {match: [a], adapter: junit, argv: [], timeout_s: 60.5}
`,
		"routes-mm": b481Levels + `review:
  multimodel: true
  second_provider: openai
  routes:
    - {min_lines: 100, model: m1}
    - {min_lines: 200, model: m2, provider: openai}
    - {min_lines: 300, model: m3, provider: xai, second: {model: s, provider: xai}}
    - {min_lines: 400, model: m4, level: L3, provider: openai}
    - {min_lines: 500, model: m5, fallback: x, provider: openai}
    - {min_lines: 600, model: m6, provider: openai, second: {model: s, provider: anthropic}}
`,
		"routes-mm-ok": strings.Replace(b481Levels, "L3: {model: d}", "L3: {model: d, provider: xai}", 1) + `review:
  multimodel: true
  second_provider: openai
  routes:
    - {min_lines: 100, model: m1, provider: xai}
`,
		"routes-mm-second-levels": strings.Replace(b481Levels, "L3: {model: d}", "L3: {model: d, provider: xai}", 1) + `review:
  multimodel: true
  second_provider: anthropic
  second_levels: {L2: {model: z, provider: openai}}
  routes:
    - {min_lines: 100, model: m1, provider: xai}
    - {min_lines: 200, model: m2, provider: anthropic}
    - {min_lines: 300, model: m3, provider: openai}
`,
		"sandbox": b481Levels + `coder:
  sandbox:
    profile: toolchain
    add_dirs:
      - /
      - relative
      - ""
      - "~"
      - "~/.."
      - "~/.ssh"
      - "~/.config/x"
      - "~/work"
      - "~/Library/Keychains/k"
      - ` + quote(filepath.Join(tmpBase, "run")) + `
      - ` + quote(filepath.Join(links, "rootlink")) + `
      - ` + quote(filepath.Join(links, "loop", "x")) + `
      - ` + quote(filepath.Join(links, "plain", "..", "plain", "deep")) + `
      - /usr/local/b481-missing
      - 5
`,
		"sandbox-ok": b481Levels + `coder:
  sandbox:
    profile: toolchain
    add_dirs: ["~/work", ` + quote(filepath.Join(links, "plain")) + `]
`,
		"sandbox-tmp-root": b481Levels + `tmp: {root: ` + quote(filepath.Join(links, "plain")) + `}
coder:
  sandbox:
    add_dirs: [` + quote(filepath.Join(links, "plain", "x")) + `, ` + quote(filepath.Join(tmpBase, "y")) + `]
`,
		"merge-gate-links": b481Levels + "gates:\n  merge_gate_links: [node_modules]\n",
		"merge-gate-setup": b481Levels + "gates:\n  merge_gate_setup: []\n",
	}
}

func TestB481ProofConfigGo(t *testing.T) {
	bodies := b481ConfigBodies(t)
	want := map[string]string{
		"runners-ok":              "",
		"runners-empty":           "proof tier: .code-forge.yml is invalid (2 error(s) at /proof/runners, proof-runners-invalid) — run code-forge validate\n",
		"runners-null":            "proof tier: .code-forge.yml is invalid (2 error(s) at /proof/runners, proof-runners-invalid) — run code-forge validate\n",
		"routes-mm-ok":            "",
		"routes-mm-second-levels": "proof tier: .code-forge.yml is invalid (1 error(s) at review-routes-invalid) — run code-forge validate\n",
		"sandbox-ok":              "",
	}
	for name, stderr := range want {
		got := b458Go(t, []string{"proof", "tier", "--file", "src/a.go", "--risk", "0", "--cwd", b462TierDir(t, bodies[name])})
		code, stdout := 1, ""
		if stderr == "" {
			code, stdout = 0, "{\"file\":\"src/a.go\",\"tier\":\"light\",\"reason\":\"default\"}\n"
		}
		if got.code != code || got.stdout != stdout || got.stderr != stderr {
			t.Errorf("%s: got %+v", name, got)
		}
	}
	// the rest refuse in Go, never hand over (exit 127)
	for name, body := range bodies {
		got := b458Go(t, []string{"proof", "tier", "--file", "src/a.go", "--risk", "0", "--cwd", b462TierDir(t, body)})
		if got.code == 127 || strings.Contains(got.stderr, "nonexistent-b458-node") {
			t.Errorf("%s handed over: %+v", name, got)
		}
	}
}

func TestB481ProofConfigMatchesNode(t *testing.T) {
	for name, body := range b481ConfigBodies(t) {
		args := []string{"proof", "tier", "--file", "src/a.go", "--risk", "0", "--cwd", b462TierDir(t, body)}
		got, want := b458Go(t, args), b458Node(t, args)
		if got.code != want.code || got.stdout != want.stdout || got.stderr != want.stderr || got.crumbs != want.crumbs || !reflect.DeepEqual(got.errors, want.errors) {
			t.Errorf("%s:\ngo   %+v\nnode %+v", name, got, want)
		}
		t.Logf("%s: exit %d %s%s", name, got.code, got.stdout, got.stderr)
	}
}

func TestB481ProofConfigRuleCounts(t *testing.T) {
	bodies := b481ConfigBodies(t)
	for name, want := range map[string]map[string]int{
		"runners-bad": {"proof-runners-invalid": 0},
		"routes-mm":   {"review-routes-invalid": 0},
		"sandbox":     {"sandbox-dir-refused": 0},
	} {
		dir := b462TierDir(t, bodies[name])
		cfg, msg, reason := proofConfigAt(dir)
		if cfg != nil || reason != 0 || !strings.Contains(msg, "is invalid") {
			t.Fatalf("%s: %v %q %v", name, cfg, msg, reason)
		}
		for id := range want {
			if !strings.Contains(msg, id) {
				t.Errorf("%s: %q does not name %s", name, msg, id)
			}
		}
	}
}
