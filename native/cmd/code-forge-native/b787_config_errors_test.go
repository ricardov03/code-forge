package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/humanask"
)

// B787: the diagnostic and reload verbs print the config error (file, line,
// key, why) and exit non-zero in Go; none of them hands over. The configs are
// the shapes the B783 audit ran on 0.25.0 (runs4/runs6 variants 07, 08, 19,
// 44 and 45).

// cpAuditBase is the audit's base config; each variant edits it.
const cpAuditBase = `version: 1
project:
  name: Audit
  slug: audit
provider: anthropic
levels:
  L0:
    model: claude-haiku-4-5-20251001
  L1:
    model: claude-sonnet-5
  L2:
    model: claude-opus-5-5
  L3:
    model: claude-fable-5-2
gates:
  test: ["sh", "-c", "exit 0"]
  lint: null
  types: null
  format: null
review:
  close_wait_s: 3
`

var cpAuditVariants = map[string]string{
	// 07: an unknown key under review
	"07": cpAuditBase + "  judge: L3\n",
	// 08: domain-invalid (no second provider; the judge shares a family)
	"08": strings.Replace(cpAuditBase, "review:\n", "review:\n  multimodel: true\n", 1),
	// 19: a toolchain sandbox with an extra dir
	"19": cpAuditBase + "coder:\n  sandbox:\n    profile: toolchain\n    add_dirs: [\"/private/var/empty\"]\n",
	// 44: "!!" in a comment
	"44": "# careful!! do not edit by hand\n" + cpAuditBase,
	// 45: a duplicate top-level key (line 22)
	"45": cpAuditBase + "review:\n  close_wait_s: 4\n",
}

const cpDuplicateKey = "DUPLICATE_KEY: Map keys must be unique (line 22, column 1)"

// cpRunNoHandover runs args in Go and fails the test when the run hands over.
func cpRunNoHandover(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	before, _ := os.ReadFile(handoverLogPath())
	code, out, errText := runCaptured(t, args...)
	after, _ := os.ReadFile(handoverLogPath())
	if len(after) != len(before) || b690aIsRefusal(errText) {
		t.Fatalf("%q handed over: code=%d stderr=%q log=%s", args, code, errText, after[len(before):])
	}
	return code, out, errText
}

// Items 2, 3 and 4: validate names the unknown key, the duplicate key's line,
// and checks coder.sandbox.add_dirs itself.
func TestB787ValidatePrintsConfigErrors(t *testing.T) {
	_, repo := smallProject(t, cpAuditVariants["07"])
	code, out, errText := cpRunNoHandover(t, "validate")
	file := filepath.Join(repo, ".code-forge.yml")
	if code != 1 || out != "[code-forge:info] "+file+": invalid (1 error)\n" ||
		!strings.HasPrefix(errText, `[code-forge:error] [schema] /review must NOT have additional property "judge" (unknown key; `) {
		t.Fatalf("07 = %d %q %q", code, out, errText)
	}

	_, repo = smallProject(t, cpAuditVariants["45"])
	code, out, errText = cpRunNoHandover(t, "validate")
	if want := "[code-forge:error] parse-error: " + filepath.Join(repo, ".code-forge.yml") + ":22: " + cpDuplicateKey + "\n"; code != 1 || out != "" || errText != want {
		t.Fatalf("45 = %d %q %q, want %q", code, out, errText, want)
	}

	_, repo = smallProject(t, cpAuditVariants["19"])
	code, out, errText = cpRunNoHandover(t, "validate")
	if code != 0 || out != "[code-forge:info] "+filepath.Join(repo, ".code-forge.yml")+": valid (0 warnings)\n" || errText != "" {
		t.Fatalf("19 = %d %q %q", code, out, errText)
	}

	// a refused dir and a profile that does not use the dirs
	smallProject(t, cpAuditBase+"coder:\n  sandbox:\n    profile: toolchain\n    add_dirs: [\"/\"]\n")
	code, _, errText = cpRunNoHandover(t, "validate")
	if code != 1 || errText != "[code-forge:error] [sandbox-dir-refused] coder.sandbox.add_dirs[0] is refused — it is the filesystem root\n" {
		t.Fatalf("root dir = %d %q", code, errText)
	}
	smallProject(t, cpAuditBase+"coder:\n  sandbox:\n    add_dirs: [\"/private/var/empty\"]\n")
	code, out, errText = cpRunNoHandover(t, "validate")
	if code != 0 || !strings.HasSuffix(out, ": valid (1 warning)\n") ||
		errText != "[code-forge:warn] [sandbox-dirs-unused] coder.sandbox.add_dirs is set but coder.sandbox.profile is workspace, which does not use it — only toolchain adds --add-dir\n" {
		t.Fatalf("unused dirs = %d %q %q", code, out, errText)
	}
}

// Item 1: doctor (full and quick) shows the first config error as its FAIL
// config row and exits 1.
func TestB787DoctorPrintsConfigErrors(t *testing.T) {
	for _, c := range []struct{ variant, row string }{
		{"08", "FAIL config: 2 error(s); first: review.multimodel is true but review.second_provider is empty\n"},
		{"07", `FAIL config: 1 schema error(s); first: /review must NOT have additional property "judge" (unknown key; `},
		{"45", "FAIL config: parse-error: .code-forge.yml:22: " + cpDuplicateKey + " (run code-forge validate)\n"},
	} {
		for _, args := range [][]string{{"doctor"}, {"doctor", "--quick"}} {
			b474Project(t, cpAuditVariants[c.variant], 0)
			code, out, errText := cpRunNoHandover(t, args...)
			if code != 1 || !strings.Contains(out, c.row) {
				t.Fatalf("%s %q = %d stdout=%q stderr=%q", c.variant, args, code, out, errText)
			}
		}
	}
}

// Item 3: plan check names the duplicate key's file and line.
func TestB787PlanCheckPrintsYAMLError(t *testing.T) {
	_, repo := planCheckProject(t, cpAuditVariants["45"])
	code, out, errText := cpRunNoHandover(t, "plan", "check", "plans/x.plan.md", "--slug", "lanes")
	if want := "plan: " + filepath.Join(repo, ".code-forge.yml") + ":22: " + cpDuplicateKey + "\n"; code != 2 || out != "" || errText != want {
		t.Fatalf("= %d %q %q, want %q", code, out, errText, want)
	}
}

// cpReload runs run reload on the b473 run with text as the config.
func cpReload(t *testing.T, text string) (int, string, string) {
	t.Helper()
	f := b473Setup(t, "start-snapshot.json", "c2.yml", nil)
	if err := os.WriteFile(filepath.Join(f.ws, ".code-forge.yml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return cpRunNoHandover(t, "run", "reload", "--run", "r-fix")
}

// Items 2, 4 and 5: run reload names schema issues and the line of a real
// YAML tag; "!!" in a comment or a quoted string and coder.sandbox.add_dirs
// reload.
func TestB787RunReloadPrintsConfigErrors(t *testing.T) {
	c2 := b473Data(t, "c2.yml")
	code, out, errText := cpReload(t, c2+"  judge: L3\n")
	if code != 1 || out != "" || !strings.HasPrefix(errText, "run reload: .code-forge.yml is invalid (1 error) — nothing changed; run code-forge validate\n  [schema] /review must NOT have additional property \"judge\" (unknown key; ") {
		t.Fatalf("schema = %d %q %q", code, out, errText)
	}
	code, out, errText = cpReload(t, strings.Replace(c2, "provider: anthropic\n", "provider: !!str anthropic\n", 1))
	if code != 1 || out != "" || errText != "run reload: .code-forge.yml:1 uses the YAML tag !!str, which code-forge does not reload; write the plain value — nothing changed\n" {
		t.Fatalf("tag = %d %q %q", code, out, errText)
	}
	code, out, errText = cpReload(t, c2+"review:\n  close_wait_s: 4\n")
	if code != 1 || out != "" || !strings.HasPrefix(errText, "run reload: the config could not be parsed (.code-forge.yml:10: DUPLICATE_KEY: Map keys must be unique (line 10, column 1)) — nothing changed\n") {
		t.Fatalf("duplicate = %d %q %q", code, out, errText)
	}
	for name, text := range map[string]string{
		"comment": "# careful!! do not edit by hand\n" + c2,
		"quoted":  c2 + "  exclude: [\"wip!!/**\"]\n",
	} {
		code, out, errText = cpReload(t, text)
		if code != 0 || !strings.HasPrefix(out, "run r-fix: config reloaded") || errText != "" {
			t.Fatalf("%s = %d %q %q", name, code, out, errText)
		}
	}
	// coder.sandbox.add_dirs is checked in Go: this run started without a
	// sandbox, so the frozen-key rule names it (a run started with the same
	// add_dirs reloads; B783 variant 19 by hand)
	code, out, errText = cpReload(t, c2+"coder:\n  sandbox:\n    profile: toolchain\n    add_dirs: [\"/private/var/empty\"]\n")
	if code != 1 || out != "" || !strings.HasPrefix(errText, "run reload: coder.sandbox cannot change mid-run ") {
		t.Fatalf("sandbox = %d %q %q", code, out, errText)
	}
}

// Item 6: autopilot approve reloads a config with "!!" in a comment, and
// refuses a config that does not check clean naming its first error, before
// the prompt and without a write.
func TestB787AutopilotApproveConfigErrors(t *testing.T) {
	f := b536Setup(t)
	start := b536Data(t, "start.yml")
	if err := os.WriteFile(filepath.Join(f.ws, ".code-forge.yml"), []byte("# careful!! do not edit by hand\n"+start), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errText := b536Approve(t, "review.max_rounds_per_file", "5", "2026-11-11T11:00:00.000Z")
	if code != 0 || !strings.HasPrefix(out, "autopilot approve run r-fix: review.max_rounds_per_file changed until ") || errText != "" {
		t.Fatalf("comment = %d %q %q", code, out, errText)
	}
	if got := f.config(t); !strings.HasPrefix(got, "# careful!! do not edit by hand\n") || !strings.Contains(got, "max_rounds_per_file: 5\n") {
		t.Fatalf("config = %q", got)
	}
	f.noHandover(t)

	for _, c := range []struct{ name, yml, value, want string }{
		{"invalid file", start + "bogus: 1\n", "5", `autopilot approve: fix .code-forge.yml first: it fails the config schema ((root) must NOT have additional property "bogus" (unknown key; `},
		{"invalid edit", start, `"abc"`, "autopilot approve: with that change .code-forge.yml fails the config schema (/review/max_rounds_per_file "},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := b536Setup(t)
			if err := os.WriteFile(filepath.Join(f.ws, ".code-forge.yml"), []byte(c.yml), 0o644); err != nil {
				t.Fatal(err)
			}
			before := f.state(t)
			asked := false
			autopilotConfirm = func(string) (humanask.Result, error) { asked = true; return humanask.Confirmed, nil }
			code, out, errText := b536Approve(t, "review.max_rounds_per_file", c.value, "2026-11-11T11:00:00.000Z")
			if code != 1 || out != "" || asked || !strings.HasPrefix(errText, c.want) || !strings.HasSuffix(errText, "; nothing was changed; run code-forge validate\n") {
				t.Fatalf("= %d %q %q asked %v", code, out, errText, asked)
			}
			if f.state(t) != before {
				t.Fatal("wrote")
			}
			f.noHandover(t)
		})
	}
}
