package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestB1011Reload(t *testing.T) {
	f := b473Setup(t, "start-snapshot.json", "", nil)
	cfg := b473Data(t, "c1.yml") + "\nhuman_check:\n  autopilot:\n    enabled: true\n    proof_dirs: [/proof]\n"
	if err := os.WriteFile(filepath.Join(f.ws, ".code-forge.yml"), []byte(cfg), 0600); err != nil {
		t.Fatal(err)
	}
	before := f.record(t)
	code, out, errText := runCaptured(t, "run", "reload", "--run", "r-fix")
	if code != 1 || !strings.Contains(errText, "human_check.autopilot cannot change mid-run") {
		t.Fatalf("%d %s %s", code, out, errText)
	}
	if before != f.record(t) {
		t.Fatal("refusal changed run")
	}
}

func TestB1011InitAndFix(t *testing.T) {
	t.Setenv("CODE_FORGE_RUN_ID", "")
	side := b527Go(t, []string{"--no-interaction", "--skip-doctor", "--no-jev"}, nil)
	cfg := b939Config(t, side)
	for _, s := range []string{"human_check:", "    enabled: false", "    require_proof: true", "    proof_dirs: []", "    exclude_waits_for: []", "    require_separate_prover: true"} {
		if !strings.Contains(cfg, s) {
			t.Fatalf("missing %s in %s", s, cfg)
		}
	}
	keys := "human_check:\n  autopilot:\n    enabled: true\n    proof_dirs: [/proof]\n"
	r := cfxRun("example.yml", b791Old+keys)
	if r.unsafe != "" || len(r.left) != 0 || !strings.Contains(r.after, keys) {
		t.Fatalf("fix changed or rejected current config: %+v", r)
	}
}
