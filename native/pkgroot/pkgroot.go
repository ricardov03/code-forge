// Package pkgroot finds the installed package root and the Go binary (B606).
//
// Each platform's binary ships as its own npm package, @codedology/code-forge-<target>, an
// optional dependency of @codedology/code-forge. npm puts it either under the main package
// (<root>/node_modules/@codedology/code-forge-<target>) or beside it (a hoisted or global install:
// <root>/../code-forge-<target>). A git checkout or dev build keeps it at
// <root>/native/bin/<target>/code-forge-native. bin/code-forge, scripts/install.sh,
// scripts/setup.mjs and src/util/native.mjs use the same lookup.
package pkgroot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// MainName is the main package; PlatformPrefix + target names each platform package.
const (
	MainName       = "@codedology/code-forge"
	PlatformPrefix = MainName + "-"
	BinaryName     = "code-forge-native"
)

// Targets are the four release targets.
var Targets = []string{"darwin-arm64", "darwin-x64", "linux-x64", "linux-arm64"}

func isTarget(t string) bool {
	for _, x := range Targets {
		if x == t {
			return true
		}
	}
	return false
}

// SourceCheckout reports a git checkout or dev build: <root>/native/go.mod exists. It uses its
// own build only, so a published binary that npm ci put in node_modules never shadows it.
func SourceCheckout(root string) bool {
	_, err := os.Stat(filepath.Join(root, "native", "go.mod"))
	return err == nil
}

// Candidates are the binary paths for target, in lookup order. The first one is the path a
// missing-binary message names.
func Candidates(root, target string) []string {
	dev := filepath.Join(root, "native", "bin", target, BinaryName)
	if SourceCheckout(root) {
		return []string{dev}
	}
	return []string{
		filepath.Join(root, "node_modules", "@codedology", "code-forge-"+target, "bin", BinaryName),
		filepath.Join(filepath.Dir(root), "code-forge-"+target, "bin", BinaryName),
		dev,
	}
}

// Find returns the first candidate that is a regular file, else the first candidate and false.
func Find(root, target string) (string, bool) {
	all := Candidates(root, target)
	for _, c := range all {
		if st, err := os.Stat(c); err == nil && st.Mode().IsRegular() {
			return c, true
		}
	}
	return all[0], false
}

func isMain(dir string) bool {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var info struct {
		Name string `json:"name"`
	}
	return json.Unmarshal(data, &info) == nil && info.Name == MainName
}

// FromExecutable maps the binary's real path (symlinks resolved) to the package root. A binary in
// a platform package maps to the main package it belongs to (nested first, then the sibling);
// any other binary is at <root>/native/bin/<target>/code-forge-native.
func FromExecutable(exe string) string {
	dir := filepath.Dir(exe)
	if filepath.Base(dir) == "bin" {
		platform := filepath.Dir(dir)
		scope := filepath.Dir(platform)
		if t, ok := strings.CutPrefix(filepath.Base(platform), "code-forge-"); ok && isTarget(t) && filepath.Base(scope) == "@codedology" {
			modules := filepath.Dir(scope)
			if filepath.Base(modules) == "node_modules" && isMain(filepath.Dir(modules)) {
				return filepath.Dir(modules)
			}
			if sibling := filepath.Join(scope, "code-forge"); isMain(sibling) {
				return sibling
			}
		}
	}
	return filepath.Clean(filepath.Join(dir, "..", "..", ".."))
}

// Root is FromExecutable for this process.
func Root() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return FromExecutable(exe), nil
}
