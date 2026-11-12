package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// B589 (issue #36, report 3): a batch land's merge gate gets
// gates.merge_gate_links (and --copy-links) like a single land. The suite
// fails in the trial tree unless the git-ignored secret.local is there.
func TestBlockLandBatchMergeGateLinks(t *testing.T) {
	for _, mode := range []string{"link", "copy"} {
		t.Run(mode, func(t *testing.T) {
			f := newLandBatchFixture(t, 0)
			command := `case "$PWD" in *.merge-gate-*) ;; *) exit 0 ;; esac
if [ "$(cat secret.local 2>/dev/null)" != secret ]; then printf 'not ok 1 - secret.local missing\n'; exit 1; fi
if [ -L secret.local ]; then echo link >> "$HOME/secret-kind"; else echo copy >> "$HOME/secret-kind"; fi
printf 'ok 1 - secret.local present\n'`
			cfg := landTestConfig([]string{"/bin/sh", "-c", command, "gate", "{test_files}"})
			gates := cfg["gates"].(map[string]any)
			gates["rerun_failed"] = 0
			gates["merge_gate_links"] = []string{"secret.local"}
			data, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			f.write(f.repo, ".code-forge.yml", string(data))
			f.write(f.repo, ".gitignore", ".code-forge/\nsecret.local\n")
			f.git(f.repo, "commit", "-qam", "links config")
			f.base = f.git(f.repo, "rev-parse", "HEAD")
			f.write(f.repo, "secret.local", "secret")
			extra := []string{}
			if mode == "copy" {
				extra = append(extra, "--copy-links")
			}
			code, output := f.landWith(extra, "B1", "B2")
			if code != 0 {
				t.Fatalf("batch exit=%d, want 0 (merge gate must see secret.local): %s", code, output)
			}
			f.checkMain([]string{"B1", "B2"})
			if kinds := f.lines("secret-kind"); len(kinds) == 0 || strings.Join(kinds, " ") != strings.TrimSpace(strings.Repeat(mode+" ", len(kinds))) {
				t.Fatalf("secret.local kinds in the merge gate=%v, want only %s", kinds, mode)
			}
		})
	}
}
