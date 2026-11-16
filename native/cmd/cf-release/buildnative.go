package main

// The release's native build in Go (B580): scripts/build-native.mjs without --host, run with no
// Node; since B690d it asks no node for a version either. Same four targets, environment, flags, temp name, mode, rename and printed lines.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// bnTargets are the script's release targets: Node's platform and arch, and Go's GOARCH.
var bnTargets = [][3]string{
	{"darwin", "arm64", "arm64"}, {"darwin", "x64", "amd64"}, {"linux", "x64", "amd64"}, {"linux", "arm64", "arm64"},
}

// bnBuild builds the four release targets of root. It returns the lines the script printed, or
// the script's error message (without its "build-native: " prefix).
func (a *app) bnBuild(root string) (string, string) {
	return a.bnBuildTargets(root, bnTargets)
}

// bnBuildTargets builds the given targets of root (bnBuild's steps).
func (a *app) bnBuildTargets(root string, targets [][3]string) (string, string) {
	check := spawn(root, []string{"go", "version"}, 0, nil)
	switch {
	case check.errMsg == "spawnSync go ENOENT":
		return "", "Go is required to build native release binaries: install Go and put go on PATH (npm users do not need Go)."
	case check.failed():
		return "", "go version failed: " + check.errMsg
	case check.code != 0:
		return "", "go version failed: " + check.stderr
	}
	pkg, _, err := readPackage(root)
	if err != nil {
		return "", err.Error()
	}
	versionV, versionOK := pkg.get("version")
	version := jsTemplate(versionV, versionOK)
	// B690d: no Node version stamp; the binary never starts Node.
	ldflags := "-s -w -X main.version=" + version
	var printed strings.Builder
	for _, t := range targets {
		target := t[0] + "-" + t[1]
		dir := filepath.Join(root, "native", "bin", target)
		if err := os.MkdirAll(dir, 0o777); err != nil {
			return "", err.Error()
		}
		binary := filepath.Join(dir, "code-forge-native")
		pending := fmt.Sprintf("%s.%d.tmp", binary, a.pid)
		failure := func() string {
			defer os.Remove(pending)
			env := envWith(nil, [2]string{"CGO_ENABLED", "0"}, [2]string{"GOOS", t[0]}, [2]string{"GOARCH", t[2]},
				[2]string{"GOTOOLCHAIN", "local"}, [2]string{"GOPROXY", "off"})
			built := spawn(filepath.Join(root, "native"), []string{"go", "build", "-trimpath", "-ldflags", ldflags, "-o", pending,
				"./cmd/code-forge-native"}, 0, env)
			switch {
			case built.failed():
				return target + " build failed: " + built.errMsg
			case built.code != 0:
				return target + " build failed: " + built.stderr
			}
			if err := os.Chmod(pending, 0o755); err != nil {
				return err.Error()
			}
			if err := os.Rename(pending, binary); err != nil {
				return err.Error()
			}
			st, err := os.Stat(binary)
			if err != nil {
				return err.Error()
			}
			printed.WriteString(fmt.Sprintf("native %s: %d bytes (v%s)\n", target, st.Size(), version))
			return ""
		}()
		if failure != "" {
			return "", failure
		}
	}
	return printed.String(), ""
}

// buildNativeMain is `cf-release build-native [--host]` (B760): the maintainer build that
// scripts/build-native.mjs did, with no Node. No argument builds the four release targets;
// --host builds only this machine's target into native/bin/<platform>-<arch>.
func buildNativeMain(a *app, args []string, root string) int {
	targets := bnTargets
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--host":
		targets = nil
		for _, t := range bnTargets {
			if t[0] == runtime.GOOS && t[2] == runtime.GOARCH {
				targets = append(targets, t)
			}
		}
		if len(targets) == 0 {
			a.err("build-native: unsupported native host: " + runtime.GOOS + "-" + runtime.GOARCH + "\n")
			return 1
		}
	default:
		a.err("build-native: usage: cf-release build-native [--host]\n")
		return 1
	}
	printed, failure := a.bnBuildTargets(root, targets)
	if failure != "" {
		a.err("build-native: " + failure + "\n")
		return 1
	}
	a.out(printed)
	return 0
}
