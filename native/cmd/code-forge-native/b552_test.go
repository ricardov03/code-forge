package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b552SealApps points detectTool's machine-wide app folder at a folder that
// does not exist, so doctor and tools rows are the same on every machine.
func b552SealApps(t *testing.T) {
	t.Helper()
	before, beforeOS := toolSystemApps, toolOS
	toolSystemApps = filepath.Join(t.TempDir(), "no-apps")
	// B560: the goldens come from Node on a Mac; pin the platform too
	toolOS = "darwin"
	t.Cleanup(func() { toolSystemApps, toolOS = before, beforeOS })
}

// TestB552AppsSealedFromMachine: with the seal, an app bundle only counts
// when it is in the sealed folder or HOME/Applications.
func TestB552AppsSealedFromMachine(t *testing.T) {
	b552SealApps(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("HOME", t.TempDir())
	solo := toolRows[len(toolRows)-1]
	if solo.id != "solo" {
		t.Fatalf("last row = %s, want solo", solo.id)
	}
	if detectTool(solo) {
		t.Fatal("solo found with a sealed app folder and an empty HOME")
	}
}

// b552SkillRows folds each copied skill file (".../skills/code-forge/<rest>")
// into one "<rel> <mode> =skill/<rest>" row when the repo has skill/<rest>.
// With root set (Go's tree) the file must hold the repo's exact bytes; with
// no root (Node's golden) the row was the copy of that day's skill. So a
// skill text change (B463's shim line) no longer breaks the init golden,
// while a wrong copy in Go still does.
func b552SkillRows(t *testing.T, tree, root string) string {
	t.Helper()
	repo := b458Repo(t)
	out := []string{}
	skip := false
	for _, line := range strings.Split(tree, "\n") {
		if m := b528Row.FindStringSubmatch(line); m != nil {
			skip = false
			rel := m[1]
			i := strings.Index(rel, "skills/code-forge/")
			if i >= 0 && strings.HasPrefix(line[len(rel)+1:], "-") {
				rest := rel[i+len("skills/code-forge/"):]
				src, err := os.ReadFile(filepath.Join(repo, "skill", rest))
				same := err == nil
				if same && root != "" {
					data, err := os.ReadFile(filepath.Join(root, rel))
					same = err == nil && string(data) == string(src)
				}
				if same {
					out = append(out, rel+" "+line[len(rel)+1:len(rel)+11]+" =skill/"+rest)
					skip = true
					continue
				}
			}
		}
		if !skip {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
