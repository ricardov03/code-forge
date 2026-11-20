package landmerge

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gitx"
)

// B815: this repository's .code-forge.yml makes land re-generate
// docs/reference/commands.md, commands.json and config.md when they conflict,
// instead of merging them row by row or refusing.
func TestB815RepoConfigRegeneratesReferenceDocs(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadFile(filepath.Join(root, config.Filename))
	if err != nil {
		t.Fatalf("repo config: %v", err)
	}
	settings := FromConfig(cfg)
	docs := []string{"docs/reference/commands.md", "docs/reference/commands.json", "docs/reference/config.md"}
	for _, file := range docs {
		if kind := settings.Kind(file, false); kind != KindGenerated {
			t.Fatalf("%s kind = %s, want generated", file, kind)
		}
	}
	if kind := settings.Kind("docs/native-engine.md", false); kind != KindTable {
		t.Fatalf("docs/native-engine.md kind = %s, want table (default kept)", kind)
	}
	// Stand-in generators with the repository's script names: each writes
	// the files the real one writes.
	commands := "#!/bin/sh\nprintf 'commands regenerated\\n' > docs/reference/commands.md\nprintf '{\"regenerated\":true}\\n' > docs/reference/commands.json\n"
	release := "#!/bin/sh\n[ \"$1\" = config-doc ] || exit 9\nprintf 'config regenerated\\n' > docs/reference/config.md\n"
	files := map[string][3]string{
		"scripts/gen-commands-doc.sh": {commands, commands, commands},
		"scripts/cf-release.sh":       {release, release, release},
	}
	for _, file := range docs {
		files[file] = [3]string{"base\n", "main\n", "block\n"}
	}
	repo := lmRepo(t, files)
	resolved, err := Resolve(repo, gitx.ChildEnv(), settings)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := Describe(resolved); got != "docs/reference/commands.json (generated), docs/reference/commands.md (generated), docs/reference/config.md (generated)" {
		t.Fatalf("resolved = %s", got)
	}
	want := map[string]string{"docs/reference/commands.md": "commands regenerated\n", "docs/reference/commands.json": "{\"regenerated\":true}\n", "docs/reference/config.md": "config regenerated\n"}
	for file, body := range want {
		if got := lmGit(t, repo, "show", ":"+file); got != body {
			t.Fatalf("staged %s = %q, want %q", file, got, body)
		}
	}
	if out, err := exec.Command("git", "-C", repo, "diff", "--name-only", "--diff-filter=U").Output(); err != nil || len(out) != 0 {
		t.Fatalf("unmerged left: %q %v", out, err)
	}
}
