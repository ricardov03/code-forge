package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b791Old is a config an older release (before 0.22.0) accepted: comments,
// gates.extra.invariants next to a kept gates.extra key.
const b791Old = `# project config
version: 1 # schema
project:
  name: Audit # name
  slug: audit
provider: anthropic
# levels
levels:
  L0:
    model: claude-haiku-4-5-20251001 # cheap
  L1:
    model: claude-sonnet-5
  L2:
    model: claude-opus-5-5
  L3:
    model: claude-fable-5-2
gates:
  # tests
  test: ["sh", "-c", "exit 0"]
  lint: null
  types: null
  format: null
  extra:
    secret_scan: true # keep
    invariants: scripts/inv.sh # old
review:
  close_wait_s: 3 # short
`

// b791Fixed is b791Old with only the invariants line gone.
var b791Fixed = strings.Replace(b791Old, "    invariants: scripts/inv.sh # old\n", "", 1)

const b791Hint = "  to run \"scripts/inv.sh\" as a gate, add it to gates.custom as {name, argv}\n"

const b791Fix = "fixed gates-extra-invariants (" + cfxInvariantsReason + ")\n" + b791Hint

func b791Write(t *testing.T, file, text string) {
	t.Helper()
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func b791Unchanged(t *testing.T, file, want string) {
	t.Helper()
	if got := readText(t, file); got != want {
		t.Fatalf("%s changed:\n%s", file, got)
	}
}

// An old shape is fixed in place: comments, blank-free layout and key order
// stay, only the refused key goes, and the result validates.
func TestB791ConfigFixRemovesOldShapeKeepsComments(t *testing.T) {
	_, repo := smallProject(t, b791Old)
	b471ValidatePath(t)
	file := filepath.Join(repo, ".code-forge.yml")
	checkSmallCases(t, []smallCase{
		{[]string{"config", "fix"}, 0, b791Fix + file + ": fixed (1 rule); run code-forge validate to check it\n", ""},
	})
	b791Unchanged(t, file, b791Fixed)
	checkSmallCases(t, []smallCase{
		{[]string{"validate"}, 0, "[code-forge:info] " + file + ": valid (0 warnings)\n", ""},
		{[]string{"config", "fix"}, 0, file + ": config ok\n", ""},
	})
}

// gates.extra left empty goes with the key; gates keeps its other keys.
func TestB791ConfigFixPrunesEmptyExtra(t *testing.T) {
	old := strings.Replace(b791Old, "    secret_scan: true # keep\n", "", 1)
	_, repo := smallProject(t, old)
	b471ValidatePath(t)
	file := filepath.Join(repo, ".code-forge.yml")
	code, _, errText := runCaptured(t, "config", "fix")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d stderr=%q", code, errText)
	}
	want := strings.Replace(old, "  extra:\n    invariants: scripts/inv.sh # old\n", "", 1)
	b791Unchanged(t, file, want)
	checkSmallCases(t, []smallCase{{[]string{"validate"}, 0, "[code-forge:info] " + file + ": valid (0 warnings)\n", ""}})
}

// --dry-run prints the diff and the rule, writes nothing.
func TestB791ConfigFixDryRunWritesNothing(t *testing.T) {
	_, repo := smallProject(t, b791Old)
	file := filepath.Join(repo, ".code-forge.yml")
	info, _ := os.Stat(file)
	diff := "--- " + file + "\n+++ " + file + " (fixed)\n@@ -25,1 +24,0 @@\n-    invariants: scripts/inv.sh # old\n"
	checkSmallCases(t, []smallCase{
		{[]string{"config", "fix", "--dry-run"}, 0, diff + "would fix gates-extra-invariants (" + cfxInvariantsReason + ")\n" + b791Hint + "dry run: nothing was written\n", ""},
	})
	b791Unchanged(t, file, b791Old)
	if after, _ := os.Stat(file); !after.ModTime().Equal(info.ModTime()) {
		t.Fatal("dry run touched the file")
	}
}

// --file names another config; a missing file and bad flags are refused.
func TestB791ConfigFixFileAndUsage(t *testing.T) {
	_, repo := smallProject(t, "")
	other := filepath.Join(repo, "other.yml")
	b791Write(t, other, b791Old)
	missing := filepath.Join(repo, ".code-forge.yml")
	checkSmallCases(t, []smallCase{
		{[]string{"config"}, 2, "", configUsage},
		{[]string{"config", "fix", "--bogus"}, 2, "", configUsage},
		{[]string{"config", "fix", "--file"}, 2, "", configUsage},
		{[]string{"config", "fix", "--dry-run", "--dry-run"}, 2, "", configUsage},
		{[]string{"config", "fix"}, 1, "", "config fix: no config at " + missing + "\n"},
		{[]string{"config", "fix", "--file", "other.yml", "--dry-run"}, 0, "--- " + other + "\n+++ " + other + " (fixed)\n@@ -25,1 +24,0 @@\n-    invariants: scripts/inv.sh # old\nwould fix gates-extra-invariants (" + cfxInvariantsReason + ")\n" + b791Hint + "dry run: nothing was written\n", ""},
	})
	b791Unchanged(t, other, b791Old)
}

// Unknown keys are reported and kept; the known old shape is still fixed.
func TestB791ConfigFixReportsUnknownKeys(t *testing.T) {
	old := strings.Replace(b791Old, "  close_wait_s: 3 # short\n", "  close_wait_s: 3 # short\n  judge: L3\n", 1)
	_, repo := smallProject(t, old)
	file := filepath.Join(repo, ".code-forge.yml")
	unknown := "unknown key review.judge: left in place; remove or rename it by hand\n"
	checkSmallCases(t, []smallCase{
		{[]string{"config", "fix"}, 1, b791Fix + unknown + file + ": fixed (1 rule); run code-forge validate to check it\n", ""},
	})
	b791Unchanged(t, file, strings.Replace(old, "    invariants: scripts/inv.sh # old\n", "", 1))
	checkSmallCases(t, []smallCase{{[]string{"config", "fix"}, 1, unknown, ""}})
}

// Anchors, aliases and tags are never edited: nothing changes and the
// exact by-hand step and command are printed.
func TestB791ConfigFixUnsafeShapesChangeNothing(t *testing.T) {
	cases := map[string]struct{ text, why string }{
		"anchor": {strings.Replace(b791Old, "    invariants: scripts/inv.sh # old\n", "    invariants: &inv scripts/inv.sh\nx-inv: *inv\n", 1), "a YAML anchor or alias"},
		"tag":    {strings.Replace(b791Old, "  slug: audit\n", "  slug: !!str audit\n", 1), "a YAML tag (!!...)"},
		"merge":  {strings.Replace(b791Old, "review:\n", "base: &b {close_wait_s: 3}\nreview:\n  <<: *b\n", 1), "a YAML anchor or alias"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, repo := smallProject(t, c.text)
			file := filepath.Join(repo, ".code-forge.yml")
			want := "config fix: " + file + " holds " + c.why + ", so it cannot be edited safely; nothing was changed\n" +
				"  gates-extra-invariants: " + cfxInvariantsByHand + "\n  then run: code-forge validate\n"
			checkSmallCases(t, []smallCase{
				{[]string{"config", "fix"}, 1, "", want},
				{[]string{"config", "fix", "--dry-run"}, 1, "", want},
			})
			b791Unchanged(t, file, c.text)
		})
	}
}

// A file that does not load is left alone.
func TestB791ConfigFixBrokenYAML(t *testing.T) {
	_, repo := smallProject(t, "gates: [\n")
	file := filepath.Join(repo, ".code-forge.yml")
	code, out, errText := runCaptured(t, "config", "fix")
	if code != 1 || out != "" || !strings.HasPrefix(errText, "config fix: "+file+" does not load") || !strings.Contains(errText, "nothing was changed; fix it by hand, then run code-forge validate") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out, errText)
	}
	b791Unchanged(t, file, "gates: [\n")
}

// Every audit config from B781 (49 shapes teams use): one with no old shape
// is never touched; none makes the fixer fail or write an unloadable file.
func TestB791ConfigFixAuditConfigs(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "gatecheck", "testdata", "b781", "*.yml"))
	if err != nil || len(files) < 40 {
		t.Fatalf("audit configs: %d %v", len(files), err)
	}
	texts := map[string]string{}
	for _, f := range files {
		abs, _ := filepath.Abs(f)
		texts[filepath.Base(f)] = readText(t, abs)
	}
	_, repo := smallProject(t, "")
	file := filepath.Join(repo, ".code-forge.yml")
	for name, text := range texts {
		b791Write(t, file, text)
		code, out, errText := runCaptured(t, "config", "fix")
		if code != 0 && code != 1 {
			t.Fatalf("%s: code=%d stdout=%q stderr=%q", name, code, out, errText)
		}
		if got := readText(t, file); got != text {
			t.Errorf("%s: no old shape, but the file changed:\n%s", name, got)
		}
		if code == 0 && out != file+": config ok\n" {
			t.Errorf("%s: code 0 stdout=%q", name, out)
		}
	}
}

// upgrade runs the fixer on the cwd's .code-forge.yml after the relink and
// always exits 0: fixed, ok, unsafe and broken configs.
func TestB791UpgradeRunsConfigFix(t *testing.T) {
	_, repo, _ := upgradeFixture(t)
	file := filepath.Join(repo, ".code-forge.yml")
	base := "nothing to upgrade\ndocs: kept packed 0.23.0\n"
	b791Write(t, file, b791Old)
	checkSmallCases(t, []smallCase{
		{[]string{"upgrade", "--plain"}, 0, base + "config: fixed gates-extra-invariants (" + cfxInvariantsReason + ")\nconfig: " + strings.TrimPrefix(b791Hint, "  ") + ".gitignore: added .code-forge/\n", ""},
	})
	b791Unchanged(t, file, b791Fixed)
	checkSmallCases(t, []smallCase{{[]string{"upgrade", "--plain"}, 0, base + "config ok\n", ""}})

	anchored := strings.Replace(b791Old, "    invariants: scripts/inv.sh # old\n", "    invariants: &inv scripts/inv.sh\nx-inv: *inv\n", 1)
	b791Write(t, file, anchored)
	checkSmallCases(t, []smallCase{
		{[]string{"upgrade", "--plain"}, 0, base + "config: not changed (" + file + " holds a YAML anchor or alias); see what to change with: code-forge config fix --dry-run\n", ""},
	})
	b791Unchanged(t, file, anchored)

	b791Write(t, file, "gates: [\n")
	checkSmallCases(t, []smallCase{
		{[]string{"upgrade", "--plain"}, 0, base + "config: not changed (" + file + " does not load); check it with: code-forge validate\n", ""},
	})
	b791Unchanged(t, file, "gates: [\n")
}
