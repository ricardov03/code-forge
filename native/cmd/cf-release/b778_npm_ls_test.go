package main

// B778 (in B799): the release's npm ls check uses the same rules as `code-forge release check`:
// the own platform packages still installed at the previous version (the release bumps their
// pins first) are ignored, and a tree whose only fault is extraneous packages names npm prune.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func b778Release(t *testing.T, ls string) (runResult, string) {
	t.Helper()
	f := newFixture(t, fixtureOpts{native: true})
	p := filepath.Join(f.dir, "npm-ls.json")
	if err := os.WriteFile(p, []byte(ls), 0o644); err != nil {
		t.Fatal(err)
	}
	r := b580Run(f, map[string]string{"FAKE_NPM_LS": p}, 4242, "release", "minor", "--date", date)
	data, _ := os.ReadFile(filepath.Join(f.repo, "package.json"))
	return r, string(data)
}

func TestB778ReleaseIgnoresOwnPlatformPackagesAtPreviousVersion(t *testing.T) {
	r, pkg := b778Release(t, `{"name":"@example/pkg","dependencies":{"@example/pkg-darwin-arm64":{"version":"0.21.0","invalid":"\"0.22.0\" from the root project"},"@example/pkg-linux-x64":{"required":"0.22.0","missing":true}},"problems":["invalid: @example/pkg-darwin-arm64@0.21.0 /x","missing: @example/pkg-linux-x64@0.22.0, required by @example/pkg@0.22.0"]}`)
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
	want := "ok    npm ls: 0 invalid, 0 missing, 0 extraneous (ignored the own platform packages at the previous version: @example/pkg-darwin-arm64@0.21.0, @example/pkg-linux-x64@not installed)\n"
	if !strings.Contains(r.stdout, want) || !strings.Contains(pkg, `"version": "0.22.0"`) {
		t.Fatalf("stdout lacks %q:\n%s", want, r.stdout)
	}
}

func TestB778ReleaseRefusesExtraneousNamingPrune(t *testing.T) {
	for _, c := range []struct{ name, ls, want string }{
		{"extraneous only", `{"dependencies":{"left-pad":{"version":"1.3.0","extraneous":true}},"problems":["extraneous: left-pad@1.3.0 /x"]}`,
			"npm ls: 1 extraneous: left-pad@1.3.0\nfix: npm prune (removes the extraneous packages)"},
		{"a real invalid package", `{"dependencies":{"a":{"version":"9.0.0","invalid":"x"}}}`, "npm ls: 1 invalid: a@9.0.0\nfix: npm install"},
		{"own package at a newer version", `{"dependencies":{"@example/pkg-darwin-x64":{"version":"0.23.0","invalid":"x"}}}`,
			"npm ls: 1 invalid: @example/pkg-darwin-x64@0.23.0\nfix: npm install"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r, pkg := b778Release(t, c.ls)
			if r.code == 0 || !strings.Contains(r.stdout+r.stderr, c.want) {
				t.Fatalf("exit %d, want the refusal %q\nstdout %s\nstderr %s", r.code, c.want, r.stdout, r.stderr)
			}
			if !strings.Contains(pkg, `"version": "0.21.0"`) {
				t.Fatalf("the refused release left package.json changed:\n%s", pkg)
			}
		})
	}
}
