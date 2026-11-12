package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
)

// B408: a worktree of the code-forge repo itself carries its own native
// binary, which the Node compat tests run. A rebase or an edit that changes
// native sources leaves that binary stale, so rebase and gates rebuild it.

// nativeRepo reports whether root is a code-forge checkout that builds its own
// native binary (native/go.mod and scripts/build-native.mjs both present).
func nativeRepo(root string) bool {
	for _, file := range []string{filepath.Join("native", "go.mod"), filepath.Join("scripts", "build-native.mjs")} {
		if st, err := os.Stat(filepath.Join(root, file)); err != nil || !st.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// nativeHostBinary is the path scripts/build-native.mjs --host writes.
func nativeHostBinary(root string) string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return filepath.Join(root, "native", "bin", runtime.GOOS+"-"+arch, "code-forge-native")
}

// nativeStale reports whether the host binary is missing, or older than
// native/go.mod or any non-test native/**/*.go file (native/bin is skipped).
func nativeStale(root string) (bool, error) {
	st, err := os.Stat(nativeHostBinary(root))
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	built := st.ModTime()
	dir := filepath.Join(root, "native")
	bin := filepath.Join(dir, "bin")
	stale := errors.New("stale")
	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == bin {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if path != filepath.Join(dir, "go.mod") && (!strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go")) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(built) {
			return stale
		}
		return nil
	})
	if err == stale {
		return true, nil
	}
	return false, err
}

// nativeBuild rebuilds the host binary of root in Go, the way
// scripts/build-native.mjs --host does (B575: no Node needed). A failure keeps
// the text callers showed when Node ran the script.
func nativeBuild(root string) error {
	if _, err := nativeBuildHost(root); err != nil {
		text := strings.Join(strings.Fields("build-native: "+err.Error()), " ")
		return fmt.Errorf("native-rebuild-failed: node scripts/build-native.mjs --host in %s: %s", root, text)
	}
	return nil
}

// nativeBuildTargets are the release targets of scripts/build-native.mjs, in
// Node's platform-arch names.
var nativeBuildTargets = []string{"darwin-arm64", "darwin-x64", "linux-x64", "linux-arm64"}

var nbNodeVersionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+[0-9A-Za-z.+-]*$`)

// nbNodeVersion is the version the script stamped into
// main.reviewWaitNodeVersion (its own process.version): the Node a hand-over
// uses (contextNode), asked with --version. With no usable node it is "", and
// the build leaves the variable at its "unknown" default.
func nbNodeVersion() string {
	node, err := contextNode()
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, "--version").Output()
	if err != nil {
		return ""
	}
	v := strings.TrimSpace(string(out))
	if !nbNodeVersionPattern.MatchString(v) {
		return ""
	}
	return v
}

// nbPackageVersion reads package.json's version as the script's template
// string shows it.
func nbPackageVersion(root string) (string, error) {
	file := filepath.Join(root, "package.json")
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("ENOENT: no such file or directory, open '%s'", file)
	}
	if err != nil {
		return "", err
	}
	var pkg map[string]any
	if err := json.Unmarshal(data, &pkg); err != nil {
		return "", err
	}
	switch v := pkg["version"].(type) {
	case nil:
		return "undefined", nil
	case string:
		return v, nil
	default:
		return fmt.Sprint(v), nil
	}
}

// nbEnv is the environment plus the script's build overrides.
func nbEnv(goos, goarch string) []string {
	set := map[string]string{"CGO_ENABLED": "0", "GOOS": goos, "GOARCH": goarch, "GOTOOLCHAIN": "local", "GOPROXY": "off"}
	env := []string{}
	for _, kv := range os.Environ() {
		if key, _, _ := strings.Cut(kv, "="); set[key] != "" {
			continue
		}
		env = append(env, kv)
	}
	for _, key := range []string{"CGO_ENABLED", "GOOS", "GOARCH", "GOTOOLCHAIN", "GOPROXY"} {
		env = append(env, key+"="+set[key])
	}
	return env
}

// nativeBuildHost is scripts/build-native.mjs --host in Go: go version check,
// go build to <binary>.<pid>.tmp, chmod 755, rename, and the script's line.
func nativeBuildHost(root string) (string, error) {
	goarch := runtime.GOARCH
	arch := goarch
	if arch == "amd64" {
		arch = "x64"
	}
	target := runtime.GOOS + "-" + arch
	if !slices.Contains(nativeBuildTargets, target) {
		return "", fmt.Errorf("unsupported native host: %s", target)
	}
	var verErr bytes.Buffer
	check := exec.Command("go", "version")
	check.Stderr = &verErr
	if err := check.Run(); err != nil {
		var exit *exec.ExitError
		switch {
		case errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist):
			return "", errors.New("Go is required to build native release binaries: install Go and put go on PATH (npm users do not need Go).")
		case errors.As(err, &exit):
			return "", fmt.Errorf("go version failed: %s", verErr.String())
		default:
			return "", fmt.Errorf("go version failed: %s", err)
		}
	}
	version, err := nbPackageVersion(root)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, "native", "bin", target)
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return "", err
	}
	binary := filepath.Join(dir, "code-forge-native")
	pending := fmt.Sprintf("%s.%d.tmp", binary, os.Getpid())
	defer os.Remove(pending)
	ldflags := "-s -w -X main.version=" + version
	if node := nbNodeVersion(); node != "" {
		ldflags += " -X main.reviewWaitNodeVersion=" + node
	}
	var buildErr bytes.Buffer
	build := exec.Command("go", "build", "-trimpath", "-ldflags", ldflags, "-o", pending, "./cmd/code-forge-native")
	build.Dir = filepath.Join(root, "native")
	build.Env = nbEnv(runtime.GOOS, goarch)
	build.Stderr = &buildErr
	if err := build.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("%s build failed: %s", target, buildErr.String())
		}
		return "", fmt.Errorf("%s build failed: %s", target, err)
	}
	if err := os.Chmod(pending, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(pending, binary); err != nil {
		return "", err
	}
	st, err := os.Stat(binary)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("native %s: %d bytes (v%s)", target, st.Size(), version), nil
}

// nativeRebuildIfStale rebuilds the host binary of a code-forge checkout when
// nativeStale says so. Other projects are left alone.
func nativeRebuildIfStale(root string) (bool, error) {
	if !nativeRepo(root) {
		return false, nil
	}
	stale, err := nativeStale(root)
	if err != nil {
		return false, fmt.Errorf("native-rebuild-failed: %s", err)
	}
	if !stale {
		return false, nil
	}
	return true, nativeBuild(root)
}

// nativeChanged reports whether the commits from..to change native/** or
// scripts/build-native.mjs.
func nativeChanged(tree, from, to string) (bool, error) {
	out, err := gitx.Run(tree, "diff", "--no-renames", "--name-only", "-z", from, to, "--")
	if err != nil {
		return false, err
	}
	for _, file := range strings.Split(out, "\x00") {
		if strings.HasPrefix(file, "native/") || file == "scripts/build-native.mjs" {
			return true, nil
		}
	}
	return false, nil
}

// nativeRebuildAfterMove rebuilds the host binary when HEAD moved from..to
// across a native change in a code-forge checkout.
func nativeRebuildAfterMove(tree, from, to string) (bool, error) {
	if from == to || !nativeRepo(tree) {
		return false, nil
	}
	changed, err := nativeChanged(tree, from, to)
	if err != nil {
		return false, fmt.Errorf("native-rebuild-failed: %s", err)
	}
	if !changed {
		return false, nil
	}
	return true, nativeBuild(tree)
}
