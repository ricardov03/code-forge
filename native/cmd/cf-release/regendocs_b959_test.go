package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type b959Call struct {
	argv string
	env  map[string]string
}

// b959Root is a repository root with the real schema, so the config doc step runs for real.
func b959Root(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	schema, err := os.ReadFile(filepath.Join("..", "..", "..", "schema", "code-forge.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "schema"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "docs", "reference"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "schema", "code-forge.schema.json"), schema, 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// b959Fake replaces the child runner; fail names argv substrings that exit 1.
func b959Fake(t *testing.T, fail ...string) *[]b959Call {
	t.Helper()
	calls := &[]b959Call{}
	saved := rdSpawn
	t.Cleanup(func() { rdSpawn = saved })
	rdSpawn = func(root string, argv []string, env []string) result {
		c := b959Call{argv: strings.Join(argv, " "), env: map[string]string{}}
		for _, kv := range env {
			if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "CODE_FORGE_") {
				c.env[k] = v
			}
		}
		*calls = append(*calls, c)
		for _, f := range fail {
			if strings.Contains(c.argv, f) {
				return result{code: 1, stdout: "--- FAIL: TestX\n"}
			}
		}
		return result{code: 0}
	}
	return calls
}

// b959Records lists the record switches a child got.
func b959Records(c b959Call) string {
	var on []string
	for _, k := range []string{"CODE_FORGE_WRITE_SUMMARIES", "CODE_FORGE_B801_RECORD", "CODE_FORGE_INIT_GOLDEN_RECORD"} {
		if v, ok := c.env[k]; ok {
			on = append(on, k+"="+v)
		}
	}
	return strings.Join(on, " ")
}

func b959Run(t *testing.T, root string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(newApp(&out, &errb), append([]string{"regen-docs"}, args...), root)
	return code, out.String(), errb.String()
}

func TestB959RegenRunsEveryStepOnce(t *testing.T) {
	calls := b959Fake(t)
	root := b959Root(t)
	code, stdout, stderr := b959Run(t, root)
	if code != 0 {
		t.Fatalf("exit %d; stderr %q", code, stderr)
	}
	if len(*calls) != 2 {
		t.Fatalf("children %v, want the commands doc and one go test", *calls)
	}
	if (*calls)[0].argv != "sh scripts/gen-commands-doc.sh" || b959Records((*calls)[0]) != "" {
		t.Errorf("step 1 %+v", (*calls)[0])
	}
	g := (*calls)[1]
	for _, name := range []string{"TestB613SummariesFile", "TestB801DocExamplesRun", "TestB528InitWizardMatchesNode", "TestB611InitWizardJevAndDoctorMatchNode", "TestB703InitWizardMatchesNode", "TestB726InitWizardMatchesNode", "TestB762.*Golden"} {
		if !strings.Contains(g.argv, name) {
			t.Errorf("go test run misses %s: %s", name, g.argv)
		}
	}
	if !strings.HasPrefix(g.argv, "go -C native test ./cmd/code-forge-native -run ") {
		t.Errorf("go test argv %q", g.argv)
	}
	for _, k := range []string{"CODE_FORGE_WRITE_SUMMARIES", "CODE_FORGE_B801_RECORD", "CODE_FORGE_INIT_GOLDEN_RECORD"} {
		if g.env[k] != "1" {
			t.Errorf("go test env %s=%q, want 1", k, g.env[k])
		}
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "reference", "config.md")); err != nil {
		t.Errorf("config doc step did not write config.md: %v", err)
	}
	if strings.Count(stdout, "regen-docs: ok: ") != 3 || !strings.Contains(stdout, "regen-docs: done") {
		t.Errorf("stdout %q", stdout)
	}
}

func TestB959RegenStopsAtFailure(t *testing.T) {
	calls := b959Fake(t, "gen-commands-doc")
	root := b959Root(t)
	code, stdout, stderr := b959Run(t, root)
	if code != 1 || len(*calls) != 1 {
		t.Fatalf("exit %d after %d children, want 1 after 1", code, len(*calls))
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "reference", "config.md")); err == nil || strings.Contains(stdout, "ok: docs/reference/config.md") {
		t.Errorf("the config doc step ran after the failed first step; stdout %q", stdout)
	}
	if !strings.Contains(stderr, "regen-docs: failed: docs/reference/commands.md and commands.json\n") || !strings.Contains(stderr, "--- FAIL: TestX") {
		t.Errorf("stderr %q", stderr)
	}
}

func TestB959CheckListsStaleSteps(t *testing.T) {
	// record switches left in the shell must not reach a check's go test
	t.Setenv("CODE_FORGE_B801_RECORD", "1")
	t.Setenv("CODE_FORGE_INIT_GOLDEN_RECORD", "1")
	t.Setenv("CODE_FORGE_WRITE_SUMMARIES", "1")
	root := b959Root(t)
	calls := b959Fake(t, "go -C native test")
	// config.md missing: stale too; every step still runs
	code, stdout, stderr := b959Run(t, root, "--check")
	if code != 1 || len(*calls) != 2 {
		t.Fatalf("exit %d, children %d; stderr %q", code, len(*calls), stderr)
	}
	if (*calls)[0].argv != "sh scripts/gen-commands-doc.sh --check" {
		t.Errorf("check step 1 %q", (*calls)[0].argv)
	}
	if r := b959Records((*calls)[1]); r != "" {
		t.Errorf("check mode go test has a record switch: %s", r)
	}
	if !strings.Contains(stdout, "regen-docs --check: ok: docs/reference/commands.md") ||
		!strings.Contains(stderr, "regen-docs --check: stale: docs/reference/config.md\n") ||
		!strings.Contains(stderr, "regen-docs --check: stale: test pins") ||
		!strings.Contains(stderr, "regen-docs --check: 2 stale;") {
		t.Errorf("stdout %q stderr %q", stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "reference", "config.md")); err == nil {
		t.Error("--check wrote config.md")
	}

	b959Fake(t)
	if code, _, stderr := b959Run(t, root); code != 0 {
		t.Fatalf("regen exit %d: %s", code, stderr)
	}
	code, stdout, stderr = b959Run(t, root, "--check")
	if code != 0 || !strings.Contains(stdout, "every generated doc and pin is up to date") {
		t.Fatalf("fresh check exit %d stdout %q stderr %q", code, stdout, stderr)
	}
}

func TestB959RegenWritesConfigDoc(t *testing.T) {
	b959Fake(t)
	root := b959Root(t)
	file := filepath.Join(root, "docs", "reference", "config.md")
	if err := os.WriteFile(file, []byte("stale\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := b959Run(t, root, "--check"); code != 1 {
		t.Fatalf("stale config.md: check exit %d, want 1", code)
	}
	if code, _, stderr := b959Run(t, root); code != 0 {
		t.Fatalf("regen exit %d: %s", code, stderr)
	}
	want, err := cdRender(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(file); string(got) != want {
		t.Fatal("config.md not regenerated")
	}
	if code, _, stderr := b959Run(t, root, "--check"); code != 0 {
		t.Fatalf("check after regen exit %d: %s", code, stderr)
	}
}

func TestB959Usage(t *testing.T) {
	calls := b959Fake(t)
	for _, args := range [][]string{{"--x"}, {"--check", "now"}} {
		code, stdout, stderr := b959Run(t, b959Root(t), args...)
		if code != 2 || stdout != "" || stderr != rdUsage+"\n" {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, code, stdout, stderr)
		}
	}
	if len(*calls) != 0 {
		t.Errorf("usage error ran children: %v", *calls)
	}
}
