package main

import (
	"os"
	"path/filepath"
	"testing"
)

// B606: each target's binary ships as its own npm package, @codedology/code-forge-<target>.
// bin/code-forge and scripts/install.sh look for it (1) under the main package
// (node_modules/@codedology/code-forge-<target>), (2) beside it (a hoisted or global
// install), (3) in the dev build native/bin/<target>. A git checkout uses (3) only. These
// tests lay out a real npm tree with fake binaries that print their own path.

// newB606Pkg is a B569 package at <lib>/node_modules/@codedology/code-forge with no binary.
func newB606Pkg(t *testing.T) (b569Pkg, string) {
	t.Helper()
	base := newB569Pkg(t)
	root := filepath.Dir(base.pkg)
	lib := filepath.Join(root, "lib dir")
	p := base
	p.pkg = filepath.Join(lib, "node_modules", "@codedology", "code-forge")
	for _, rel := range []string{"bin/code-forge", "scripts/install.sh", "package.json"} {
		data, err := os.ReadFile(filepath.Join(base.pkg, rel))
		if err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o755)
		if rel == "package.json" {
			mode = 0o644
		}
		b569Write(t, filepath.Join(p.pkg, rel), string(data), mode)
	}
	return p, lib
}

// b606FakeBinary answers --version and internal version like b569FakeNative and
// otherwise prints its own path, so a test sees which binary the launcher ran.
func b606FakeBinary(t *testing.T, path string) {
	t.Helper()
	b569Write(t, path, "#!/bin/sh\n"+
		"case \"$1 $2\" in\n"+
		"  '--version ') echo '@codedology/code-forge "+b569Version+"'; exit 0 ;;\n"+
		"  'internal version') printf '{\"version\":\"%s\"}\\n' '"+b569Version+"'; exit 0 ;;\n"+
		"esac\n"+
		"printf 'ran:%s %s\\n' '"+path+"' \"$*\"\n", 0o755)
}

func b606Paths(p b569Pkg, lib string) (nested, sibling, dev string) {
	nested = p.nested(p.target)
	sibling = filepath.Join(lib, "node_modules", "@codedology", "code-forge-"+p.target, "bin", "code-forge-native")
	dev = filepath.Join(p.pkg, "native", "bin", p.target, "code-forge-native")
	return
}

func b606InstallOK(p b569Pkg, binary string) string {
	return "code-forge install: OK Go binary " + p.target + " v" + b569Version + " (" + binary + ")\n"
}

func TestB606LauncherNestedPlatformPackage(t *testing.T) {
	p, lib := newB606Pkg(t)
	nested, _, _ := b606Paths(p, lib)
	b606FakeBinary(t, nested)
	b569Expect(t, "nested", p.launch(nil, "status", "x"), 0, "ran:"+nested+" status x\n", "")
	b569Expect(t, "nested install", p.install(), 0, b606InstallOK(p, nested), "")
}

func TestB606LauncherHoistedSiblingPlatformPackage(t *testing.T) {
	p, lib := newB606Pkg(t)
	_, sibling, _ := b606Paths(p, lib)
	b606FakeBinary(t, sibling)
	b569Expect(t, "sibling", p.launch(nil, "status"), 0, "ran:"+sibling+" status\n", "")
	b569Expect(t, "sibling install", p.install(), 0, b606InstallOK(p, sibling), "")
	// The launcher runs through a global bin symlink too.
	link := filepath.Join(lib, "bin", "code-forge")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "node_modules", "@codedology", "code-forge", "bin", "code-forge"), link); err != nil {
		t.Fatal(err)
	}
	b569Expect(t, "sibling via bin link", p.run(link, nil, "status"), 0, "ran:"+sibling+" status\n", "")
}

func TestB606LauncherDevBuild(t *testing.T) {
	p, lib := newB606Pkg(t)
	_, _, dev := b606Paths(p, lib)
	b606FakeBinary(t, dev)
	b569Expect(t, "dev", p.launch(nil, "status"), 0, "ran:"+dev+" status\n", "")
	b569Expect(t, "dev install", p.install(), 0, b606InstallOK(p, dev), "")
}

func TestB606LauncherLookupOrder(t *testing.T) {
	p, lib := newB606Pkg(t)
	nested, sibling, dev := b606Paths(p, lib)
	for _, path := range []string{nested, sibling, dev} {
		b606FakeBinary(t, path)
	}
	b569Expect(t, "nested first", p.launch(nil, "status"), 0, "ran:"+nested+" status\n", "")
	if err := os.Remove(nested); err != nil {
		t.Fatal(err)
	}
	b569Expect(t, "sibling second", p.launch(nil, "status"), 0, "ran:"+sibling+" status\n", "")
	// A git checkout (native/go.mod) runs its own build even when npm put a published one in node_modules.
	b606FakeBinary(t, nested)
	b569Write(t, filepath.Join(p.pkg, "native", "go.mod"), "module x\n", 0o644)
	b569Expect(t, "checkout uses dev", p.launch(nil, "status"), 0, "ran:"+dev+" status\n", "")
	b569Expect(t, "checkout install uses dev", p.install(), 0, b606InstallOK(p, dev), "")
	if err := os.Remove(dev); err != nil {
		t.Fatal(err)
	}
	p.binary = dev
	b569Expect(t, "checkout without build", p.launch(nil, "status"), 69, "", p.noEngine("the Go engine for "+p.target+" is missing"))
}

func TestB606LauncherNoBinaryExits69(t *testing.T) {
	p, lib := newB606Pkg(t)
	nested, _, _ := b606Paths(p, lib)
	p.binary = nested
	want := p.noEngine("the Go engine for " + p.target + " is missing")
	b569Expect(t, "none", p.launch(nil, "status"), 69, "", want)
	b569Expect(t, "none setup", p.launch(nil, "setup"), 69, "", want)
	b569Expect(t, "none install", p.install(), 1, "", "code-forge install: Go binary is missing: "+nested+"\nfix: npm i -g @codedology/code-forge@"+b569Version+"\n")
}
