package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"github.com/ricardov03/code-forge/native/pkgroot"
)

func init() { routes["setup"] = runSetupCommand }

const setupUsage = "usage: code-forge setup\n"

var setupTargets = []string{"darwin-arm64", "darwin-x64", "linux-x64", "linux-arm64"}

// setupHost is the Node-style <platform>-<arch> name of this binary's target.
func setupHost(goos, goarch string) string {
	arch := goarch
	if arch == "amd64" {
		arch = "x64"
	}
	return goos + "-" + arch
}

func runSetupCommand(args []string) int {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "code-forge setup: cannot locate the Go binary: %v\nfix: npm install -g @codedology/code-forge\n", err)
		return 1
	}
	pkg := pkgroot.FromExecutable(exe)
	return setupCheck(args, pkg, setupHost(runtime.GOOS, runtime.GOARCH), version, os.Stdout, os.Stderr)
}

// setupCheck is scripts/setup.mjs in Go for a binary that runs: the binary is
// this process, so its version is `version`. bin/code-forge runs the Node
// script instead when the binary is missing, not executable or crashes.
func setupCheck(args []string, pkg, target, binaryVersion string, stdout, stderr io.Writer) int {
	isHelp := func(i int) bool { return len(args) > i && (args[i] == "--help" || args[i] == "-h") }
	if isHelp(0) || isHelp(1) {
		fmt.Fprint(stdout, setupUsage)
		return 0
	}
	if len(args) > 0 {
		fmt.Fprint(stderr, setupUsage)
		return 2
	}
	fail := func(problem, fix string) int {
		fmt.Fprintf(stderr, "code-forge setup: %s\nfix: %s\n", problem, fix)
		return 1
	}
	data, err := os.ReadFile(filepath.Join(pkg, "package.json"))
	var info struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err == nil {
		err = json.Unmarshal(data, &info)
	}
	if err != nil {
		return fail(fmt.Sprintf("cannot read %s: %v", filepath.Join(pkg, "package.json"), err), "npm install -g @codedology/code-forge")
	}
	binary, _ := pkgroot.Find(pkg, target) // B606: platform package, then the dev build
	reinstall := fmt.Sprintf("npm install -g %s@%s", info.Name, info.Version)
	if _, err := os.Stat(filepath.Join(pkg, "native", "go.mod")); err == nil {
		reinstall = "node scripts/build-native.mjs --host"
	}
	supported := false
	for _, t := range setupTargets {
		supported = supported || t == target
	}
	if !supported {
		return fail(fmt.Sprintf("no Go binary is built for %s (built: %s); code-forge runs on Node here", target, strings.Join(setupTargets, ", ")), "use macOS or Linux on x64 or arm64 for the Go binary")
	}
	if st, err := os.Stat(binary); err != nil || !st.Mode().IsRegular() {
		return fail("Go binary is missing: "+binary, reinstall)
	}
	if syscall.Access(binary, 1) != nil {
		if os.Chmod(binary, 0o755) != nil || syscall.Access(binary, 1) != nil {
			return fail("Go binary is not executable and its mode could not be fixed: "+binary, fmt.Sprintf("chmod 755 '%s'", binary))
		}
	}
	if binaryVersion != info.Version {
		return fail(fmt.Sprintf("Go binary reports v%s, package is v%s: %s", binaryVersion, info.Version, binary), reinstall)
	}
	fmt.Fprintf(stdout, "code-forge setup: OK Go binary %s v%s (%s)\n", target, info.Version, binary)
	return 0
}
