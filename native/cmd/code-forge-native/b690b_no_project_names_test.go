package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// B690b: test/no-project-names.test.mjs in Go. code-forge never names the
// real project it must never touch, in any package content. The names are
// built from parts so this file never holds either one; docs/design is the
// maintainer's private note (root ruling 2026-09-25) and never ships.

var b690bNamePattern = regexp.MustCompile("(?i)" + strings.Join([]string{"condo" + "mera", "finance" + "360"}, "|"))

// b690bSweptRoots are the package and repo content swept for a name.
var b690bSweptRoots = []string{"bin", "schema", "skill", "evals", "docs", "examples", "test", "scripts", "native", "README.md", "CHANGELOG.md", "package.json"}

func b690bNameHits(text []byte) int { return len(b690bNamePattern.FindAllIndex(text, -1)) }

func TestB690bNameMatcherCatchesAControlLine(t *testing.T) {
	first := "condo" + "mera"
	if n := b690bNameHits([]byte("ref: " + strings.ToUpper(first) + " run")); n != 1 {
		t.Fatalf("control = %d", n)
	}
	if n := b690bNameHits([]byte("the " + strings.ToUpper(first) + " run, " + "finance" + "360 numbers")); n != 2 {
		t.Fatalf("two names = %d", n)
	}
	if n := b690bNameHits([]byte("a neutral example library")); n != 0 {
		t.Fatalf("clean = %d", n)
	}
}

func TestB690bNoProjectNamesInContent(t *testing.T) {
	root := b458Repo(t)
	var hits []string
	checked := 0
	for _, top := range b690bSweptRoots {
		_ = filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if d.Name() == "node_modules" || d.Name() == ".git" || rel == "docs/design" {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil // never follow a symlink out of the swept tree
			}
			data, err := os.ReadFile(path)
			if err != nil || bytes.IndexByte(data, 0) >= 0 {
				return nil // binaries never carry the name as readable text
			}
			checked++
			if b690bNameHits(data) > 0 {
				hits = append(hits, rel)
			}
			return nil
		})
	}
	if checked < 100 {
		t.Fatalf("checked only %d files", checked)
	}
	if len(hits) != 0 {
		t.Fatalf("excluded project names in: %v", hits)
	}
}
