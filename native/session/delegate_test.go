package session

import (
	"slices"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B536: the delegate session (askDelegate): a schema other than
// finding.schema.json is served when the caller gives its validator; the
// lens goes in as claude --system-prompt (after --no-session-persistence,
// builders/claude.mjs) and grok --system-prompt-override (last,
// builders/grok.mjs); an answer the validator refuses is invalid-output.
func TestDelegateSessionSystemPromptAndValidate(t *testing.T) {
	lens := "the delegate lens"
	run := func(t *testing.T, provider, stdout string) (ledger.Object, []string) {
		t.Helper()
		o, _ := testSetup(t)
		o.Cfg = map[string]any{"levels": map[string]any{"L2": map[string]any{"provider": provider, "model": "m-x"}}}
		o.Level, o.Role, o.Schema, o.SystemPrompt, o.Validate = "L2", "delegate", DelegateSchema(), &lens, MatchesDelegateSchema
		var argv []string
		deps := Deps{Stderr: &strings.Builder{}, WriteRow: func(ledger.Object) error { return nil },
			Exec: func(e execOpts) execResult {
				argv = e.Argv
				return execResult{OK: true, Code: 0.0, Stdout: stdout}
			}}
		res, err := Spawn(o, deps)
		if err != nil {
			t.Fatal(err)
		}
		return res, argv
	}
	good := `{"decision":"proceed","within_scope":true,"confidence":1,"reason":"ok","escalate":false}`
	res, argv := run(t, "anthropic", `{"is_error":false,"structured_output":`+good+`}`)
	at := slices.Index(argv, "--system-prompt")
	if at < 1 || argv[at-1] != "--no-session-persistence" || argv[at+1] != lens || argv[at+2] != "--output-format" {
		t.Fatalf("claude argv = %q", argv)
	}
	if res.Get("status") != "ok" || !MatchesDelegateSchema(res.Get("answer")) {
		t.Fatalf("ok answer: %v", res)
	}
	res, _ = run(t, "anthropic", `{"is_error":false,"structured_output":{"decision":"proceed"}}`)
	if res.Get("status") != "invalid-output" || res.Get("reason") != "answer does not match the schema" || res.Get("answer") != nil {
		t.Fatalf("bad answer: %v", res)
	}
	_, argv = run(t, "xai", good)
	if n := len(argv); n < 2 || argv[0] != "grok" || argv[n-2] != "--system-prompt-override" || argv[n-1] != lens {
		t.Fatalf("grok argv = %q", argv)
	}
}
