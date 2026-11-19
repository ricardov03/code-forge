package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B786: run status read budget.usd through an unchecked map assertion, so
// every config without a budget: block panicked (a go-panic hand-over). It
// must print the status and exit 0 with or without the block.
func TestB786RunStatusBudget(t *testing.T) {
	base := "provider: anthropic\nlevels:\n  L0: {model: claude-haiku-4-5-20251001}\n  L1: {model: claude-sonnet-5}\n  L2: {model: claude-opus-5-5}\n  L3: {model: claude-fable-5-1}\n"
	cases := []struct {
		name, cfg, budget string
	}{
		{"no budget", base, `"budget_usd":null`},
		{"budget without usd", base + "budget:\n  block_lines: 400\n", `"budget_usd":null`},
		{"budget.usd 5", base + "budget:\n  usd: 5\n", `"budget_usd":5`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := b473Setup(t, "start-snapshot.json", "", nil)
			if err := os.WriteFile(filepath.Join(f.ws, ".code-forge.yml"), []byte(tc.cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
			var handover error
			code, stdout, stderr := helpCapture(true, func() int {
				c, err := runGuarded([]string{"run", "status", "--run", "r-fix"})
				handover = err
				return c
			})
			if handover != nil || code != 0 {
				t.Fatalf("code=%d handover=%v stdout=%q stderr=%q", code, handover, stdout, stderr)
			}
			for _, want := range []string{`"run_id":"r-fix"`, `"status":"active"`, `"spent_usd":0`, tc.budget} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("stdout lacks %s:\n%s", want, stdout)
				}
			}
			if stderr != "" {
				t.Fatalf("stderr=%q", stderr)
			}
		})
	}
}
