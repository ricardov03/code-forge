package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B462: `proof export <block>` builds the export in Go. Before B462 it
// handed over to Node (proof-run). The cases join b462Cases, so
// TestB462ProofRunGo and TestB462ProofRunMatchesNode run them too.

// b462ExportRepo commits a base with export-ignore paths, then changes the
// tree: an owned file edited, one added, one deleted; an untracked .env; a
// node_modules dir (linked) and a Composer vendor dir (copied).
func b462ExportRepo(config string) func(t *testing.T, repo string) {
	return func(t *testing.T, repo string) {
		w := func(rel, body string, mode os.FileMode) { b462Write(t, filepath.Join(repo, rel), []byte(body), mode) }
		w(".code-forge.yml", config, 0o600)
		w(".gitignore", ".code-forge/\n.env\nnode_modules/\nvendor/\n", 0o644)
		w(".gitattributes", "docs/ export-ignore\nsecret.txt export-ignore\n", 0o644)
		w("docs/a.md", "doc\n", 0o644)
		w("secret.txt", "kept\n", 0o644)
		w("src/x.js", "base x\n", 0o644)
		w("src/y.js", "base y\n", 0o644)
		w("old.js", "old\n", 0o644)
		w("README.md", "readme\n", 0o644)
		b462Git(t, repo, "add", "-A")
		b462Git(t, repo, "-c", "user.name=Fixture", "-c", "user.email=f@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "export base")
		w("src/x.js", "tree x\n", 0o755)
		w("src/new.js", "new\n", 0o644)
		if err := os.Remove(filepath.Join(repo, "old.js")); err != nil {
			t.Fatal(err)
		}
		w("README.md", "changed but not owned\n", 0o644)
		w(".env", "APP=1\n", 0o600)
		w("node_modules/pkg/index.js", "module\n", 0o644)
		w("vendor/composer/autoload_real.php", "<?php\n", 0o644)
		w("vendor/pkg/tool", "#!/bin/sh\n", 0o755)
		w("packages/pkg/src.php", "<?php\n", 0o644)
		for link, target := range map[string]string{
			"vendor/bin/tool":    "../pkg/tool",
			"vendor/acme/pkg":    filepath.Join(repo, "packages", "pkg"),
			"vendor/acme/root":   repo,
			"vendor/acme/state":  filepath.Join(repo, ".code-forge", "runs"),
			"vendor/acme/beyond": "/usr",
		} {
			if err := os.MkdirAll(filepath.Join(repo, filepath.Dir(link)), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(repo, link)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

const b462Levels = "version: 1\nprovider: anthropic\nlevels:\n  L0: {model: a}\n  L1: {model: b}\n  L2: {model: c}\n  L3: {model: d}\n"

func b462ExportBlock(extra ...ledger.Field) ledger.Object {
	block := ledger.Object{{Key: "status", Value: "open"}, {Key: "base_sha", Value: "main"},
		{Key: "owned_files", Value: []any{"src/**", "old.js"}}, {Key: "owned_kinds", Value: ledger.Object{}}}
	for _, field := range extra {
		block.Set(field.Key, field.Value)
	}
	return block
}

func init() {
	exportArgs := []string{"proof", "export", "B1", "--run", b462Run}
	b462Cases = append(b462Cases,
		b462Case{name: "export-build", args: exportArgs, blocks: ledger.Object{{Key: "B1", Value: b462ExportBlock()}},
			setup: b462ExportRepo(b462Levels), stdout: "{\"dir\":\"<REPO>/.code-forge/export/B1\",\"base\":\"00ce280b2b598102bee22c112bb233fd24614df0\",\"restored\":[\"docs/a.md\",\"secret.txt\"],\"untracked\":[\".env\"],\"owned\":[\"src/new.js\",\"src/x.js\",\"src/y.js\"],\"removed\":[\"old.js\"],\"linked\":[\"node_modules\"],\"copied\":[\"vendor\"]}\n"},
		b462Case{name: "export-config-lists", args: exportArgs, blocks: ledger.Object{{Key: "B1", Value: b462ExportBlock()}},
			setup:  b462ExportRepo(b462Levels + "proof:\n  export:\n    link_dirs: [node_modules]\n    copy_untracked: [.env, cfg/local.ini]\n"),
			stdout: "{\"dir\":\"<REPO>/.code-forge/export/B1\",\"base\":\"c45267fa0712962b4d79f59962fd6b74c76dde0c\",\"restored\":[\"docs/a.md\",\"secret.txt\"],\"untracked\":[\".env\"],\"owned\":[\"src/new.js\",\"src/x.js\",\"src/y.js\"],\"removed\":[\"old.js\"],\"linked\":[\"node_modules\"],\"copied\":[]}\n"},
		b462Case{name: "export-isolation-lock", args: exportArgs, blocks: ledger.Object{{Key: "B1", Value: b462ExportBlock()}},
			setup: b462ExportRepo(b462Levels + "proof:\n  isolation: lock\n"),
			code:  1, stderr: "proof export: proof.isolation is lock — use proof lock instead of an export\n"},
		b462Case{name: "export-bad-base", args: exportArgs, blocks: ledger.Object{{Key: "B1", Value: b462ExportBlock(ledger.Field{Key: "base_sha", Value: "nope"})}},
			setup: b462ExportRepo(b462Levels),
			code:  1, stderr: "proof export: buildExport: the base is not a commit in this repository\n"},
		b462Case{name: "export-no-base", args: exportArgs, blocks: ledger.Object{{Key: "B1", Value: ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{}}}}},
			setup: b462ExportRepo(b462Levels),
			code:  1, stderr: "proof export: buildExport: the base is not a commit in this repository\n"},
		b462Case{name: "export-owned-missing", args: exportArgs, blocks: ledger.Object{{Key: "B1", Value: ledger.Object{{Key: "status", Value: "open"}, {Key: "base_sha", Value: "main"}}}},
			setup: b462ExportRepo(b462Levels),
			code:  1, stderr: "proof export: buildExport: owned must be an array\n"},
		b462Case{name: "export-copy-state", args: exportArgs, blocks: ledger.Object{{Key: "B1", Value: b462ExportBlock()}},
			setup: b462ExportRepo(b462Levels + "proof:\n  export:\n    copy_untracked: [.code-forge/x]\n"),
			code:  1, stderr: "proof export: proof.export.copy_untracked: nothing under .code-forge/ is ever exported\n"},
		b462Case{name: "export-link-dotdot", args: exportArgs, blocks: ledger.Object{{Key: "B1", Value: b462ExportBlock()}},
			setup: b462ExportRepo(b462Levels + "proof:\n  export:\n    link_dirs: [a/../b]\n"),
			code:  1, stderr: "proof export: proof.export.link_dirs: no \".\", \"..\" or empty segments\n"},
		b462Case{name: "export-config-invalid", args: exportArgs, blocks: ledger.Object{{Key: "B1", Value: b462ExportBlock()}},
			setup: b462ExportRepo("version: 1\nlevels: [\n"),
			code:  1, stderr: "proof export: .code-forge.yml does not load (parse-error) — run code-forge validate\n"},
		b462Case{name: "export-worktree-gone", args: exportArgs,
			blocks: ledger.Object{{Key: "B1", Value: b462ExportBlock(ledger.Field{Key: "worktree", Value: ledger.Object{
				{Key: "path", Value: "/nonexistent-b462-tree"}, {Key: "branch", Value: "b1"}, {Key: "base_sha", Value: strings.Repeat("a", 40)}}})}},
			setup: b462ExportRepo(b462Levels),
			code:  1, stderr: "proof export: block B1 of run " + b462Run + ": the worktree /nonexistent-b462-tree is gone (removed or moved); no proof was run\n"},
	)
}

// TestB462ProofExportTree checks the export Go built: export-ignore paths
// restored, owned files at tree content (mode kept), a deleted owned file
// gone, unowned edits left at base, node_modules linked, the Composer vendor
// copied with its main-tree links pointed at the export, and the signed row.
func TestB462ProofExportTree(t *testing.T) {
	c := b462Cases[0]
	for _, each := range b462Cases {
		if each.name == "export-build" {
			c = each
		}
	}
	home, repo := b462Fixture(t, c)
	if code, _, stderr := b462GoCall(t, home, repo, c.args); code != 0 {
		t.Fatalf("export failed: %s", stderr)
	}
	got := b462Collect(t, home, repo)
	exp := ".code-forge/export/B1/"
	want := map[string]string{
		exp + "docs/a.md":          "644 doc\n",
		exp + "secret.txt":         "644 kept\n",
		exp + "src/x.js":           "755 tree x\n",
		exp + "src/new.js":         "644 new\n",
		exp + "README.md":          "644 readme\n",
		exp + ".env":               "644 APP=1\n",
		exp + "node_modules":       "link <REPO>/node_modules",
		exp + "vendor/acme/pkg":    "link <REPO>/.code-forge/export/B1/packages/pkg",
		exp + "vendor/acme/root":   "link <REPO>/.code-forge/export/B1",
		exp + "vendor/acme/state":  "link <REPO>/.code-forge/runs",
		exp + "vendor/acme/beyond": "link /usr",
		exp + "vendor/bin/tool":    "link ../pkg/tool",
		exp + "vendor/pkg/tool":    "755 #!/bin/sh\n",
	}
	for rel, body := range want {
		if got.files[rel] != body {
			t.Errorf("%s = %q, want %q", rel, got.files[rel], body)
		}
	}
	if _, ok := got.files[exp+"old.js"]; ok {
		t.Errorf("old.js is still in the export")
	}
	if len(got.rows) != 1 || !strings.Contains(got.rows[0], `"step":"export","base_sha":"00ce280b2b598102bee22c112bb233fd24614df0","restored":["docs/a.md","secret.txt"],"untracked":[".env"],"event":"proof","block":"B1","isolation":"export"`) {
		t.Fatalf("rows %v", got.rows)
	}
}
