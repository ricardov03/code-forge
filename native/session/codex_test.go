package session

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B551: Codex's usage-limit text (`unexpected status 429 ...` in its error
// event) is rate-limited: two waits through the Sleep seam, then the ladder
// ends. The result is what Node printed for the same fake codex
// (CODE_FORGE_NATIVE=0 ./bin/code-forge spawn --level L2 --role reviewer
// --schema finding.schema.json), duration_ms masked.
func TestCodexUsageLimitIsRateLimited(t *testing.T) {
	dir := t.TempDir()
	prompt := filepath.Join(dir, "brief.md")
	if err := os.WriteFile(prompt, []byte("review this please, the packet line here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "codex")
	script := "#!/bin/sh\ncat > /dev/null\necho '{\"type\":\"error\",\"message\":\"unexpected status 429 Too Many Requests: usage limit reached\"}'\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	codexTestSchema(t)
	schema, err := LoadFindingSchema()
	if err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"review": map[string]any{"allow_open_book_codex": true},
		"levels": map[string]any{"L2": map[string]any{"provider": "openai", "model": "gpt-y"}}}
	o := Opts{Cfg: cfg, Level: "L2", Role: "reviewer", PromptPath: prompt, RunRoot: filepath.Join(dir, "root"), Schema: schema}
	var slept []float64
	var errText strings.Builder
	var rows []ledger.Object
	res, err := Spawn(o, Deps{Stderr: &errText, Bins: map[string]string{"codex": bin}, Environ: []string{"PATH=/usr/bin:/bin"},
		Sleep: func(ms float64) { slept = append(slept, ms) }, Random: func() float64 { return 0.5 },
		WriteRow: func(r ledger.Object) error { rows = append(rows, r); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	res.Delete("text") // the spawn CLI prints the result without its text, as Node's does
	data, _ := ledger.Stringify(res, false)
	got := regexp.MustCompile(`"duration_ms":\d+`).ReplaceAllString(string(data), `"duration_ms":N`)
	want := `{"status":"unavailable","reason":"rate-limited","answer":null,"usage":{"tokens_in":11,"tokens_out":23,"tokens_source":"estimated"},"duration_ms":N,"exit_code":1,"stderr":"","provider":"openai","model":"gpt-y","effort":null,"fallback_step":0,"row":{"event":"session","role":"reviewer","level":"L2","provider":"openai","model":"gpt-y","effort":null,"fallback_step":0,"run":null,"block":null,"status":"unavailable","reason":"rate-limited","tokens_in":11,"tokens_out":23,"tokens_source":"estimated","usd":0.0004,"cost_source":"estimated","duration_ms":N},"attempts":[{"fallback_step":0,"provider":"openai","model":"gpt-y","status":"unavailable","reason":"rate-limited"},{"fallback_step":0,"provider":"openai","model":"gpt-y","status":"unavailable","reason":"rate-limited"},{"fallback_step":0,"provider":"openai","model":"gpt-y","status":"unavailable","reason":"rate-limited"}]}`
	if got != want {
		t.Fatalf("result\n%s\nwant\n%s", got, want)
	}
	line := "level=L2 provider=openai model=gpt-y effort=none fallback_step=0\n"
	if errText.String() != line+"rate-limited: provider=openai model=gpt-y retry in 2.0 s\n"+line+"rate-limited: provider=openai model=gpt-y retry in 6.0 s\n"+line {
		t.Fatalf("stderr = %q", errText.String())
	}
	if len(slept) != 2 || slept[0] != 2000 || slept[1] != 6000 || len(rows) != 3 {
		t.Fatalf("slept = %v rows = %d", slept, len(rows))
	}
	if left, _ := os.ReadDir(filepath.Join(o.RunRoot, "sessions")); len(left) != 0 {
		t.Fatalf("session dirs left: %d", len(left))
	}
}

// A codex step with an effort Node's builder throws on, or with a coder role,
// stays a narrow hand-over decided before anything runs; the open-book
// reviewer passes Check and CheckReview.
func TestCodexCheckServesOpenBookOnly(t *testing.T) {
	codexTestSchema(t)
	o, _ := testSetup(t)
	open := map[string]any{"review": map[string]any{"allow_open_book_codex": true},
		"levels": map[string]any{"L1": map[string]any{"provider": "openai", "model": "gpt-x", "effort": "high"}}}
	o.Cfg = open
	if err := Check(o); err != nil {
		t.Fatalf("Check open-book reviewer = %v", err)
	}
	if err := CheckReview(o, "L1", "reviewer"); err != nil {
		t.Fatalf("CheckReview = %v", err)
	}
	o.Cfg = map[string]any{"review": map[string]any{"allow_open_book_codex": true},
		"levels": map[string]any{"L1": map[string]any{"provider": "openai", "model": "gpt-x", "effort": "turbo"}}}
	if err := Check(o); !IsUnsupported(err) {
		t.Fatalf("Check bad effort = %v, want unsupported", err)
	}
	// B551r: a Codex coder step runs in Go; B618: so does one under a
	// coder.sandbox mapping with a known profile; any other coder.sandbox
	// (Node's TypeError) stays with Node, named as the sandbox case
	o.Cfg = open
	o.Role = "coder"
	if err := Check(o); err != nil {
		t.Fatalf("Check coder = %v, want nil", err)
	}
	levels := map[string]any{"L1": map[string]any{"provider": "openai", "model": "gpt-x", "effort": "high"}}
	for _, sandbox := range []any{map[string]any{"profile": "workspace"}, map[string]any{"profile": "full"}, map[string]any{"profile": nil, "add_dirs": []any{"/x"}}, map[string]any{}} {
		o.Cfg = map[string]any{"coder": map[string]any{"sandbox": sandbox}, "levels": levels}
		if err := Check(o); err != nil {
			t.Fatalf("Check sandboxed coder %v = %v, want nil", sandbox, err)
		}
	}
	for _, sandbox := range []any{"workspace", []any{}, map[string]any{"profile": "open"}, map[string]any{"profile": 3.0}} {
		o.Cfg = map[string]any{"coder": map[string]any{"sandbox": sandbox}, "levels": levels}
		if err := Check(o); !IsUnsupported(err) || !IsCodexSandbox(err) {
			t.Fatalf("Check sandboxed coder %v = %v, want the codex sandbox case", sandbox, err)
		}
	}
}

// codexTestSchema points FindingSchemaPath at the repo's finding schema.
func codexTestSchema(t *testing.T) {
	t.Helper()
	here, _ := os.Getwd()
	before := FindingSchemaPath
	FindingSchemaPath = func() (string, error) {
		return filepath.Join(here, "..", "..", "src", "review", "finding.schema.json"), nil
	}
	t.Cleanup(func() { FindingSchemaPath = before })
}
