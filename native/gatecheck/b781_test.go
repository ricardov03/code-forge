package gatecheck

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// B781: the customer's config shapes (issue 2026-11-18 block-close
// close-high-paths): comment lines, keys with an op:// reference,
// review.routes as a block list of mappings, review.second_provider and
// review.second_levels. HighPaths read none of them before 0.25.1; close
// handed over (close-high-paths), a dead end without Node.
const b781Head = `# code-forge config for the iOS app
version: 1
provider: anthropic

# Levels: who does what
levels:
  L0: { model: claude-haiku-4-5 }
  L1: { model: claude-sonnet-4-6, effort: medium }
  L2: { model: claude-opus-4-7, effort: high }
  L3: { model: claude-opus-4-7, effort: max }
`

const b781Keys = `
# stored key: a 1Password reference, not a secret
keys:
  jev: op://vault/item/field
`

const b781Routes = `
review:
  # files over 400 lines go to xai L2
  routes:
    - min_lines: 401
      level: L2
      provider: xai
      model: grok-4.7
`

const b781Second = `
review:
  # second opinion
  second_provider: xai
  second_levels:
    L2:
      provider: xai
      model: grok-4.7
`

const b781All = `
keys:
  jev: op://vault/item/field

review:
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
`

const b781Tiers = `
proof:
  tiers:
    high:
      paths:
        - Sources/Payments/**
`

// b781Repo is a temp git repo whose base commit and work tree both hold cfg.
func b781Repo(t *testing.T, base, current string) (string, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s %v", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, ".code-forge.yml"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=b781", "-c", "user.email=b781@example.invalid", "commit", "-qm", "base")
	if err := os.WriteFile(filepath.Join(dir, ".code-forge.yml"), []byte(current), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, git("rev-parse", "HEAD")
}

func TestB781HighPathsCustomerConfigs(t *testing.T) {
	for name, body := range map[string]string{"keys op://": b781Keys, "review.routes": b781Routes, "review.second_provider+second_levels": b781Second, "all": b781All} {
		t.Run(name, func(t *testing.T) {
			cfg := b781Head + body + b781Tiers
			dir, base := b781Repo(t, cfg, cfg)
			paths, err := b781HighPaths(dir, base)
			if err != nil {
				t.Fatalf("HighPaths refused a normal config: %v", err)
			}
			if !slices.Equal(paths, []any{"Sources/Payments/**"}) {
				t.Fatalf("paths = %v; want [Sources/Payments/**]", paths)
			}
		})
	}
}

// Both sides are read and merged without duplicates.
func TestB781HighPathsMergesBothSides(t *testing.T) {
	base := b781Head + b781All + b781Tiers
	current := b781Head + b781All + "\nproof:\n  tiers:\n    high:\n      paths: [Sources/Payments/**, Sources/Auth/**]\n"
	dir, sha := b781Repo(t, base, current)
	paths, err := b781HighPaths(dir, sha)
	if err != nil || !slices.Equal(paths, []any{"Sources/Payments/**", "Sources/Auth/**"}) {
		t.Fatalf("paths = %v err = %v", paths, err)
	}
}

func TestB781HighPathsRawSecretRefused(t *testing.T) {
	raw := "sk-ant-api03-" + strings.Repeat("Q", 30)
	clean := b781Head + b781All + b781Tiers
	secret := b781Head + "\nkeys:\n  jev: " + raw + "\n" + b781Tiers
	for name, sides := range map[string][2]string{"work tree": {clean, secret}, "base": {secret, clean}} {
		t.Run(name, func(t *testing.T) {
			dir, base := b781Repo(t, sides[0], sides[1])
			paths, err := b781HighPaths(dir, base)
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			if paths != nil || !strings.Contains(msg, "raw secret at keys.jev") || !strings.Contains(msg, "code-forge keys set") || !strings.Contains(msg, "op://") || strings.Contains(msg, raw) || strings.Contains(msg, "sk-") {
				t.Fatalf("paths = %v, refusal %q; want a refusal naming keys.jev and code-forge keys, without the secret", paths, msg)
			}
			if name == "base" && !strings.Contains(msg, "rebase") {
				t.Fatalf("a base-side secret must say to rebase: %q", msg)
			}
		})
	}
}

// Words that only contain a secret prefix (disk-guard has "sk-") are not
// secrets; the old crude scan refused them.
func TestB781HighPathsNoFalseSecret(t *testing.T) {
	cfg := b781Head + "\nkeys:\n  jev: op://vault/disk-guard/field\n  note: env:XAI_KEY\n" + b781Tiers
	dir, base := b781Repo(t, cfg, cfg)
	if _, err := b781HighPaths(dir, base); err != nil {
		t.Fatalf("refused: %v", err)
	}
}

func TestB781HighPathsSchemaRefusal(t *testing.T) {
	cfg := b781Head + "\nreview:\n  routes:\n    - level: L2\n" + b781Tiers
	dir, base := b781Repo(t, cfg, cfg)
	_, err := b781HighPaths(dir, base)
	if err == nil || !strings.Contains(err.Error(), "fails the config schema") || !strings.Contains(err.Error(), "code-forge validate") {
		t.Fatalf("err = %v; want a schema refusal", err)
	}
}

// b781HighPaths is the call under test.
func b781HighPaths(dir, base string) ([]any, error) { return HighPaths(dir, base, "0.25.1") }

// BenchmarkB781HighPaths times the customer config (perf clause: no slower
// than before B781).
func BenchmarkB781HighPaths(b *testing.B) {
	b.Setenv("HOME", b.TempDir())
	dir := b.TempDir()
	for _, args := range [][]string{{"init", "-q"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			b.Fatalf("%s %v", out, err)
		}
	}
	cfg := b781Head + "\nreview:\n  close_wait_s: 1\n" + b781Tiers
	if err := os.WriteFile(filepath.Join(dir, ".code-forge.yml"), []byte(cfg), 0o644); err != nil {
		b.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=b", "-c", "user.email=b@example.invalid", "commit", "-qm", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			b.Fatalf("%s %v", out, err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := b781HighPaths(dir, "HEAD"); err != nil {
			b.Fatal(err)
		}
	}
}

// b781SchemaInvalid are the B783 audit configs with a key the schema does
// not have (07 review.judge, 09 review.mode, 14 levels.*.no_tools, 23 an
// x-model anchor key) or a duplicate key (45): a refusal, never a hand-over.
var b781SchemaInvalid = map[string]bool{"07-review-judge-undoc.yml": true, "09-mode-consensus-undoc.yml": true, "14-level-no-tools-undoc.yml": true, "23-yaml-anchors.yml": true, "45-duplicate-key.yml": true}

// The 49 configs of the B783 audit (testdata/b781: 01-47, one feature each over a
// plain config, plus two real-team configs). 24 of them handed close over
// in 0.25.0 (close-high-paths), among them this repo's own gates.types +
// timeout_s (42) and project.name task-runner (43, "sk-" inside "task-").
func TestB781HighPathsAuditConfigs(t *testing.T) {
	files, err := filepath.Glob("testdata/b781/*.yml")
	if err != nil || len(files) != 49 {
		t.Fatalf("testdata/b781 holds %d configs, want 49 (%v)", len(files), err)
	}
	for _, file := range files {
		name := filepath.Base(file)
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			dir, base := b781Repo(t, string(data), string(data))
			paths, err := b781HighPaths(dir, base)
			if b781SchemaInvalid[name] {
				var refusal *ConfigRefusal
				if !errors.As(err, &refusal) {
					t.Fatalf("err = %v; want a config refusal", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("HighPaths refused a normal config: %v", err)
			}
			want := []any{}
			if name == "11-proof-high-paths.yml" {
				want = paths // its own list, checked below
				if len(paths) == 0 {
					t.Fatal("11 lists high paths; got none")
				}
			}
			if len(paths) != len(want) {
				t.Fatalf("paths = %v", paths)
			}
		})
	}
}

// A secret is a real key shape, not a substring: names holding "sk-",
// "ask-" or "risk-" are not secrets; a real-looking key is.
func TestB781SecretNeedsKeyShape(t *testing.T) {
	for _, tc := range []struct {
		value  string
		secret bool
	}{
		{"task-runner", false}, {"risk-review", false}, {"ask-jev", false}, {"disk-guard", false},
		{"xai-ops", false}, {"eyJ", false},
		{"sk-ant-api03-" + strings.Repeat("Q", 30), true}, {"xai-" + strings.Repeat("Z", 30), true},
	} {
		cfg := b781Head + "\nproject:\n  name: " + tc.value + "\n" + b781Tiers
		dir, base := b781Repo(t, cfg, cfg)
		_, err := b781HighPaths(dir, base)
		if got := err != nil && strings.Contains(err.Error(), "raw secret at project.name"); got != tc.secret {
			t.Errorf("project.name %q: err = %v; want secret=%v", tc.value, err, tc.secret)
		}
	}
}
