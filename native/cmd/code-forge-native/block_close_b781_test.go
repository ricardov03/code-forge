package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/state"
)

// B781: the customer's config (issue 2026-11-18 block-close
// close-high-paths): comment lines, keys with an op:// reference,
// review.routes as a block list of mappings, review.second_provider and
// review.second_levels. 0.25.0 handed every close over (close-high-paths),
// a dead end without Node.
const b781CustomerConfig = `# code-forge config for the iOS app
version: 1
provider: anthropic

# Levels: who does what
levels:
  L0: { model: claude-haiku-4-5 }
  L1: { model: claude-sonnet-4-6, effort: medium }
  L2: { model: claude-opus-4-7, effort: high }
  L3: { model: claude-opus-4-7, effort: max }

# stored key: a 1Password reference, not a secret
keys:
  jev: op://vault/item/field

review:
  close_wait_s: 1
  # second opinion
  second_provider: xai
  second_levels:
    L2:
      provider: xai
      model: grok-4.7
  # files over 400 lines go to xai L2
  routes:
    - min_lines: 401
      level: L2
      provider: xai
      model: grok-4.7

proof:
  tiers:
    high:
      paths: []
`

func TestB781CloseClosesOnCustomerConfig(t *testing.T) {
	configs := map[string]string{"customer": b781CustomerConfig}
	// B783 audit: this repo's own gates.types + timeout_s, project.name
	// task-runner ("sk-" inside "task-") and the valid real-team config.
	for _, name := range []string{"42-gates-types-timeout.yml", "43-name-task-dash.yml", "98-real-team-valid.yml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "gatecheck", "testdata", "b781", name))
		if err != nil {
			t.Fatal(err)
		}
		configs[name] = string(data)
	}
	for name, cfg := range configs {
		t.Run(name, func(t *testing.T) {
			f := newCloseReviewFixture(t, 2, cfg)
			done := f.reviewer("approved")
			code, out, errs := f.close()
			if err := <-done; err != nil {
				t.Error(err)
			}
			if code != 0 || !strings.Contains(out, "block B1 closed") || strings.Contains(errs, "close-high-paths") {
				t.Fatalf("exit=%d out=%s err=%s; want a normal close", code, out, errs)
			}
		})
	}
}

func TestB781CloseRefusesRawSecretConfig(t *testing.T) {
	raw := "xai-" + strings.Repeat("Z", 30)
	cfg := strings.Replace(b781CustomerConfig, "jev: op://vault/item/field", "jev: "+raw, 1)
	f := newCloseReviewFixture(t, 2, cfg)
	code, out, errs := f.close()
	want := "block close: .code-forge.yml holds what looks like a raw secret at keys.jev; remove it and store the key with code-forge keys set <name> (the config may hold only an env: or op:// reference); fix the file, then close again; nothing was recorded\n"
	// f.close appends the hand-over reason; none means the zero reason.
	want += fmt.Sprint(handoverReason(0))
	if code != 1 || errs != want || strings.Contains(out+errs, raw) {
		t.Fatalf("exit=%d out=%q err=%q; want exit 1 and\n%q", code, out, errs, want)
	}
	record, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil || blockObject(blockObject(record.Get("blocks")).Get("B1")).Get("status") != "open" {
		t.Fatalf("refused block must remain open: %v %v", record, err)
	}
}

// block ready runs the same high-paths read: the customer config gives its
// normal blocker list, a raw secret its refusal; neither hands over.
func TestB781BlockReadyOnCustomerConfig(t *testing.T) {
	cfg := `{"gates":{"test":["node","--test"],"lint":null,"types":null,"format":null,"rerun_failed":0}}`
	cases := []struct{ name, text string }{
		{"customer", b781CustomerConfig},
		{"raw secret", strings.Replace(b781CustomerConfig, "jev: op://vault/item/field", "jev: xai-"+strings.Repeat("Z", 30), 1)},
	}
	for _, name := range []string{"42-gates-types-timeout.yml", "43-name-task-dash.yml", "98-real-team-valid.yml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "gatecheck", "testdata", "b781", name))
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, struct{ name, text string }{name, string(data)})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newReadyFixture(t)
			f.setSnapshot(cfg, nil)
			f.write(f.repo, ".code-forge.yml", tc.text)
			f.write(f.tree, ".code-forge.yml", tc.text)
			code, stdout, stderr := f.call("block", "ready", "B1", "--run", f.run, "--json")
			if tc.name != "raw secret" {
				if code != 1 || !strings.HasPrefix(stdout, `{"block":"B1","ready":false,`) || !strings.Contains(stdout, `"code":"no_green_gates"`) || stderr != "" {
					t.Fatalf("code=%d stdout=%q stderr=%q; want the normal blocker list", code, stdout, stderr)
				}
				return
			}
			if code != 1 || stdout != "" || !strings.Contains(stderr, "block ready: .code-forge.yml holds what looks like a raw secret at keys.jev; remove it and store the key with code-forge keys set <name>") {
				t.Fatalf("code=%d stdout=%q stderr=%q; want the raw-secret refusal", code, stdout, stderr)
			}
		})
	}
}

// The secret scan of validate, run reload and autopilot approve matches a key
// shape that does not start inside a word: "sk-" inside task-runner-ios
// (10 characters after it) is a name, not a key.
func TestB781SecretScanNeedsKeyShape(t *testing.T) {
	for value, secret := range map[string]bool{
		"task-runner-ios": false, "risk-assessment-tool": false, "op://vault/disk-guard-ios/field": false,
		"sk-ant-api03-" + strings.Repeat("Q", 30): true, "key " + "xai-" + strings.Repeat("Z", 30): true,
	} {
		cfg := map[string]any{"project": map[string]any{"name": value}}
		if got := configHoldsSecretShape(cfg); got != secret {
			t.Errorf("%q: secret = %v; want %v", value, got, secret)
		}
	}
}
