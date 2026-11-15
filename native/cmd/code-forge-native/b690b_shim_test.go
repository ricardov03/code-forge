package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// B690b: skill/scripts/forge (the POSIX shim) never runs Node. These cases
// were test/skill/shim.test.mjs; PATH is private (<fake bin>:/usr/bin:/bin),
// so no code-forge, npx or node of this machine can answer.

// b690bShimArgs carry a space and a glob character: each must arrive intact.
var b690bShimArgs = []string{"doctor", "a b", "*"}

// b690bShimEnv is a private PATH with a fake bin directory; a fake `node`
// there writes its marker file, so a shim that runs node is seen.
func b690bShimEnv(t *testing.T) (fakeBin, nodeMarker string, env []string) {
	t.Helper()
	parent := t.TempDir()
	fakeBin = filepath.Join(parent, "fakebin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	nodeMarker = filepath.Join(parent, "node-ran")
	b690bShimFake(t, filepath.Join(fakeBin, "node"), "fake-node", nodeMarker)
	env = []string{"PATH=" + fakeBin + ":/usr/bin:/bin", "HOME=" + parent}
	return fakeBin, nodeMarker, env
}

// b690bShimFake writes an executable that prints prefix and its bracketed
// arguments; with a marker it also creates that file.
func b690bShimFake(t *testing.T, path, prefix, marker string) {
	t.Helper()
	body := "#!/bin/sh\n"
	if marker != "" {
		body += ": > '" + marker + "'\n"
	}
	body += "printf '%s' '" + prefix + "'\nfor a in \"$@\"; do printf '[%s]' \"$a\"; done\nprintf '\\n'\n"
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// b690bShimCopy copies the repo's shim to dir/skill/scripts/forge.
func b690bShimCopy(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b458Repo(t), "skill", "scripts", "forge"))
	if err != nil {
		t.Fatal(err)
	}
	shim := filepath.Join(dir, "skill", "scripts", "forge")
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return shim
}

func b690bShimRun(t *testing.T, shim string, env []string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(shim, args...)
	cmd.Env = env
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, out.String(), errb.String()
}

func b690bPackageVersion(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b458Repo(t), "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil || pkg.Version == "" {
		t.Fatalf("package.json version: %v", err)
	}
	return pkg.Version
}

func TestB690bShimPinIsThePackageVersion(t *testing.T) {
	shim := filepath.Join(b458Repo(t), "skill", "scripts", "forge")
	st, err := os.Stat(shim)
	if err != nil || st.Mode()&0o111 != 0o111 {
		t.Fatalf("shim mode %v %v", st, err)
	}
	// the npx fallback carries the pin: run it and read the spec it passes
	fakeBin, _, env := b690bShimEnv(t)
	b690bShimFake(t, filepath.Join(fakeBin, "npx"), "fake-npx", "")
	copied := b690bShimCopy(t, t.TempDir())
	code, out, _ := b690bShimRun(t, copied, env, "version")
	want := "fake-npx[--yes][@codedology/code-forge@" + b690bPackageVersion(t) + "][version]\n"
	if code != 0 || out != want {
		t.Fatalf("exit %d out %q want %q", code, out, want)
	}
}

func TestB690bShimResolution(t *testing.T) {
	t.Run("inside the package runs bin/code-forge", func(t *testing.T) {
		_, marker, env := b690bShimEnv(t)
		pkg := t.TempDir()
		shim := b690bShimCopy(t, pkg)
		b690bShimFake(t, filepath.Join(pkg, "bin", "code-forge"), "fake-launcher", "")
		code, out, _ := b690bShimRun(t, shim, env, b690bShimArgs...)
		if code != 0 || out != "fake-launcher[doctor][a b][*]\n" {
			t.Fatalf("exit %d out %q", code, out)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("node ran")
		}
	})
	t.Run("copied out falls back to code-forge on PATH", func(t *testing.T) {
		fakeBin, _, env := b690bShimEnv(t)
		b690bShimFake(t, filepath.Join(fakeBin, "code-forge"), "fake-code-forge", "")
		code, out, _ := b690bShimRun(t, b690bShimCopy(t, t.TempDir()), env, b690bShimArgs...)
		if code != 0 || out != "fake-code-forge[doctor][a b][*]\n" {
			t.Fatalf("exit %d out %q", code, out)
		}
	})
	t.Run("nothing available exits 127 with the install hint", func(t *testing.T) {
		_, _, env := b690bShimEnv(t)
		code, out, errText := b690bShimRun(t, b690bShimCopy(t, t.TempDir()), env, b690bShimArgs...)
		if code != 127 || out != "" || !strings.HasPrefix(errText, "forge: no code-forge found. Install it: npm install -g @codedology/code-forge@"+b690bPackageVersion(t)) {
			t.Fatalf("exit %d out %q err %q", code, out, errText)
		}
	})
}

// CODE_FORGE_BIN naming the old Node entry: with a launcher next to it the
// launcher runs; alone it stops with exit 127 and node never runs.
func TestB690bShimOldNodeEntryNeverRunsNode(t *testing.T) {
	_, marker, env := b690bShimEnv(t)
	pkg := t.TempDir()
	mjs := filepath.Join(pkg, "bin", "code-forge.mjs")
	b690bShimFake(t, mjs, "fake-mjs", "")
	shim := b690bShimCopy(t, t.TempDir())

	code, out, errText := b690bShimRun(t, shim, append(env, "CODE_FORGE_BIN="+mjs), b690bShimArgs...)
	if code != 127 || out != "" || !regexp.MustCompile(`^forge: CODE_FORGE_BIN=\S+code-forge\.mjs is the old Node entry and code-forge no longer runs Node; set CODE_FORGE_BIN=<path>/bin/code-forge`).MatchString(errText) {
		t.Fatalf("alone: exit %d out %q err %q", code, out, errText)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("node ran for the old Node entry")
	}

	b690bShimFake(t, filepath.Join(pkg, "bin", "code-forge"), "fake-launcher", "")
	code, out, _ = b690bShimRun(t, shim, append(env, "CODE_FORGE_BIN="+mjs), b690bShimArgs...)
	if code != 0 || out != "fake-launcher[doctor][a b][*]\n" {
		t.Fatalf("with launcher: exit %d out %q", code, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("node ran")
	}
}
