package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestB1014ReloadFreezesRebaseSources(t *testing.T) {
	for _, name := range []string{"start-snapshot.json", "start-legacy.json"} {
		t.Run(name, func(t *testing.T) {
			f := b473Setup(t, name, "c1.yml", nil)
			if err := os.WriteFile(filepath.Join(f.ws, ".code-forge.yml"), []byte(b473Data(t, "c1.yml")+"rebase:\n  approved_binary_sources: [approved/**]\n"), 0600); err != nil {
				t.Fatal(err)
			}
			before, rows := f.record(t), f.ledgerRows(t)
			code, out, errText := runCaptured(t, "run", "reload", "--run", "r-fix")
			if code != 1 || out != "" || !strings.Contains(errText, "rebase.approved_binary_sources cannot change mid-run") {
				t.Fatalf("exit=%d out=%q err=%q", code, out, errText)
			}
			if before != f.record(t) || rows != f.ledgerRows(t) {
				t.Fatal("reload refusal wrote state")
			}
		})
	}
}
