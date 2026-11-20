package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestB1022RegenFixedPoint(t *testing.T) {
	root := b959Root(t)
	saved := rdSpawn
	t.Cleanup(func() { rdSpawn = saved })
	pin, doc := "old example", "old example"
	writes := 0
	rdSpawn = func(_ string, argv, env []string) result {
		check := false
		record := false
		for _, a := range argv {
			if a == "--check" {
				check = true
			}
		}
		for _, e := range env {
			if e == "CODE_FORGE_B801_RECORD=1" {
				record = true
			}
		}
		if strings.Contains(strings.Join(argv, " "), "gen-commands-doc") {
			if check {
				if doc != pin {
					return result{code: 1, stderr: "docs use stale examples"}
				}
			} else {
				doc = pin
				writes++
			}
		} else {
			if record {
				pin = "fresh example"
				writes++
			} else if pin != "fresh example" {
				return result{code: 1, stderr: "stale examples"}
			}
		}
		return result{code: 0}
	}
	if code, _, stderr := b959Run(t, root); code != 0 {
		t.Fatalf("write exit %d: %s", code, stderr)
	}
	before, _ := os.ReadFile(filepath.Join(root, "docs", "reference", "config.md"))
	if code, _, stderr := b959Run(t, root, "--check"); code != 0 {
		t.Fatalf("one write must reach fixed point: exit %d: %s (doc=%q pin=%q)", code, stderr, doc, pin)
	}
	after, _ := os.ReadFile(filepath.Join(root, "docs", "reference", "config.md"))
	if string(before) != string(after) || writes != 2 {
		t.Fatalf("check rewrote output: writes=%d", writes)
	}
}
