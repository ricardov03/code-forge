package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

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

// nativeBuild runs `node <root>/scripts/build-native.mjs --host` in root, with
// the same Node a hand-over uses (CODE_FORGE_NODE, else node on PATH), so the
// binary gets the script's output path and ldflags.
func nativeBuild(root string) error {
	node := os.Getenv("CODE_FORGE_NODE")
	if node == "" {
		node = "node"
	}
	cmd := exec.Command(node, filepath.Join(root, "scripts", "build-native.mjs"), "--host")
	cmd.Dir = root
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		text := strings.Join(strings.Fields(out.String()), " ")
		if text == "" {
			text = err.Error()
		}
		return fmt.Errorf("native-rebuild-failed: node scripts/build-native.mjs --host in %s: %s", root, text)
	}
	return nil
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
