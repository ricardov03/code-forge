package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// B575: the native rebuild runs go build in Go, the way
// scripts/build-native.mjs --host does, and needs no Node.

// b575WriteFakeGo writes a fake go into dir. `go version` answers (or fails
// with CF_B575_VERSION_FAIL); any other call appends "build <cwd> <argv> |
// <env>" to $CF_B408_LOG, with the -o temp name's pid shown as PID, then fails
// with CF_B408_BUILD_FAIL or writes the raw -o path into the -o file.
func b575WriteFakeGo(t *testing.T, dir string) {
	t.Helper()
	script := `#!/bin/sh
if [ "$1" = version ]; then
  if [ -n "$CF_B575_VERSION_FAIL" ]; then echo 'go: version broke' >&2; exit 2; fi
  echo 'go version go0.0 fake'; exit 0
fi
line="build $(pwd -P)"
out=""
next=""
for a in "$@"; do
  if [ "$next" = o ]; then
    out="$a"; next=""
    base="${a%.*.tmp}"; mid="${a#"$base".}"; mid="${mid%.tmp}"
    case "$mid" in ''|*[!0-9]*) line="$line $a" ;; *) line="$line $base.PID.tmp" ;; esac
    continue
  fi
  [ "$a" = -o ] && next=o
  line="$line $a"
done
echo "$line | CGO_ENABLED=$CGO_ENABLED GOOS=$GOOS GOARCH=$GOARCH GOTOOLCHAIN=$GOTOOLCHAIN GOPROXY=$GOPROXY" >> "$CF_B408_LOG"
if [ -n "$CF_B408_BUILD_FAIL" ]; then echo 'go: build broke' >&2; exit 1; fi
if [ -n "$CF_B408_BUILD_SLEEP" ]; then /bin/sleep "$CF_B408_BUILD_SLEEP"; fi
printf '%s\n' "$out" > "$out"
exit 0
`
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// b575BuildLine is the fake go log line the script's build call makes.
func b575BuildLine(cwd, binary, version, node string) string {
	ldflags := "-s -w -X main.version=" + version
	if node != "" {
		ldflags += " -X main.reviewWaitNodeVersion=" + node
	}
	return "build " + cwd + " build -trimpath -ldflags " + ldflags + " -o " + binary + ".PID.tmp ./cmd/code-forge-native" +
		" | CGO_ENABLED=0 GOOS=" + runtime.GOOS + " GOARCH=" + runtime.GOARCH + " GOTOOLCHAIN=local GOPROXY=off\n"
}

type b575Root struct {
	root, real, goDir, log, node, binary, target string
}

// newB575Root is a checkout with package.json and native/, a fake go dir, and
// a fake node that answers --version.
func newB575Root(t *testing.T) b575Root {
	t.Helper()
	base := t.TempDir()
	r := b575Root{root: filepath.Join(base, "repo"), goDir: filepath.Join(base, "gobin"), log: filepath.Join(base, "calls.log"), node: filepath.Join(base, "fake-node")}
	if err := os.MkdirAll(filepath.Join(r.root, "native"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.root, "package.json"), []byte(`{"name":"x","version":"9.8.7"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.node, []byte("#!/bin/sh\n[ \"$1\" = --version ] && echo v24.22.1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	b575WriteFakeGo(t, r.goDir)
	real, err := filepath.EvalSymlinks(r.root)
	if err != nil {
		t.Fatal(err)
	}
	r.real = real
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	r.target = runtime.GOOS + "-" + arch
	r.binary = filepath.Join(r.root, "native", "bin", r.target, "code-forge-native")
	t.Setenv("CF_B408_LOG", r.log)
	t.Setenv("CF_B408_BUILD_FAIL", "")
	t.Setenv("CF_B408_BUILD_SLEEP", "")
	t.Setenv("CF_B575_VERSION_FAIL", "")
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("CODE_FORGE_TMP_ROOT", filepath.Join(base, "tmp"))
	return r
}

func (r b575Root) calls(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(r.log)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func (r b575Root) leftovers(t *testing.T) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(filepath.Dir(r.binary), "*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestB575NativeBuildRunsGoBuild(t *testing.T) {
	for _, tc := range []struct {
		name, nodeEnv, node string
	}{
		{name: "node on CODE_FORGE_NODE", node: "v24.22.1"},
		{name: "no node anywhere", nodeEnv: "/nonexistent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newB575Root(t)
			nodeEnv := r.node
			if tc.nodeEnv != "" {
				nodeEnv = tc.nodeEnv
			}
			t.Setenv("CODE_FORGE_NODE", nodeEnv)
			// PATH holds only the fake go: no node, so only CODE_FORGE_NODE can name one.
			t.Setenv("PATH", r.goDir)
			// The script's overrides win over the caller's values.
			t.Setenv("GOOS", "plan9")
			t.Setenv("CGO_ENABLED", "1")
			t.Setenv("GOPROXY", "direct")
			t.Setenv("GOTOOLCHAIN", "auto")
			line, err := nativeBuildHost(r.root)
			if err != nil {
				t.Fatalf("nativeBuildHost: %v", err)
			}
			if want := b575BuildLine(filepath.Join(r.real, "native"), r.binary, "9.8.7", tc.node); r.calls(t) != want {
				t.Fatalf("go argv/env:\n got %q\nwant %q", r.calls(t), want)
			}
			data, err := os.ReadFile(r.binary)
			if err != nil {
				t.Fatalf("binary not renamed into place: %v", err)
			}
			if want := fmt.Sprintf("%s.%d.tmp\n", r.binary, os.Getpid()); string(data) != want {
				t.Fatalf("binary was built as %q, want temp name %q", data, want)
			}
			st, err := os.Stat(r.binary)
			if err != nil {
				t.Fatal(err)
			}
			if st.Mode().Perm() != 0o755 {
				t.Fatalf("binary mode %v, want 0755", st.Mode().Perm())
			}
			if left := r.leftovers(t); len(left) != 0 {
				t.Fatalf("temp files left: %v", left)
			}
			if want := fmt.Sprintf("native %s: %d bytes (v9.8.7)", r.target, len(data)); line != want {
				t.Fatalf("line %q, want %q", line, want)
			}
			if err := nativeBuild(r.root); err != nil {
				t.Fatalf("nativeBuild: %v", err)
			}
		})
	}
}

func TestB575NativeBuildErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, r b575Root)
		want  string
		built bool
	}{
		{"go missing", func(t *testing.T, r b575Root) { t.Setenv("PATH", t.TempDir()) },
			"build-native: Go is required to build native release binaries: install Go and put go on PATH (npm users do not need Go).", false},
		{"go version fails", func(t *testing.T, r b575Root) { t.Setenv("CF_B575_VERSION_FAIL", "1") },
			"build-native: go version failed: go: version broke", false},
		{"build fails", func(t *testing.T, r b575Root) { t.Setenv("CF_B408_BUILD_FAIL", "1") },
			"build-native: %s build failed: go: build broke", true},
		{"package.json missing", func(t *testing.T, r b575Root) {
			if err := os.Remove(filepath.Join(r.root, "package.json")); err != nil {
				t.Fatal(err)
			}
		}, "build-native: ENOENT: no such file or directory, open '%r/package.json'", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newB575Root(t)
			t.Setenv("CODE_FORGE_NODE", "/nonexistent")
			t.Setenv("PATH", r.goDir)
			tc.setup(t, r)
			err := nativeBuild(r.root)
			want := strings.ReplaceAll(strings.ReplaceAll(tc.want, "%s", r.target), "%r", r.root)
			want = "native-rebuild-failed: node scripts/build-native.mjs --host in " + r.root + ": " + want
			if err == nil || err.Error() != want {
				t.Fatalf("error:\n got %v\nwant %s", err, want)
			}
			if _, statErr := os.Stat(r.binary); !os.IsNotExist(statErr) {
				t.Fatalf("no binary expected after a failed build, stat err %v", statErr)
			}
			if tc.built != (r.calls(t) != "") {
				t.Fatalf("build call made = %v, want %v (calls %q)", r.calls(t) != "", tc.built, r.calls(t))
			}
			if _, statErr := os.Stat(filepath.Dir(r.binary)); statErr == nil {
				if left := r.leftovers(t); len(left) != 0 {
					t.Fatalf("temp files left: %v", left)
				}
			}
		})
	}
}

// The gates-run stale rebuild works with no node anywhere: PATH holds only
// the fake go and git, and CODE_FORGE_NODE names nothing.
func TestB575GatesNativeRebuildWithoutNode(t *testing.T) {
	f := newB408Fixture(t)
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(git, filepath.Join(f.goDir, "git")); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := f.call([]string{"PATH=" + f.goDir, "CODE_FORGE_NODE=/nonexistent"}, "gates", "run", "--run", "fixture", "--block", "B1")
	want := b575BuildLine(filepath.Join(f.realTree(), "native"), f.bin, "9.8.7", "") + "test\n"
	if code != 0 || f.calls() != want {
		t.Fatalf("want exit 0, one go build with no node version, then the test command (%q); got exit %d calls %q stderr %s", want, code, f.calls(), stderr)
	}
	var result struct {
		AllOK         bool  `json:"allOk"`
		NativeRebuilt *bool `json:"native_rebuilt"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("gate JSON: %v: %s", err, stdout)
	}
	if !result.AllOK || result.NativeRebuilt == nil || !*result.NativeRebuilt {
		t.Fatalf("want allOk and native_rebuilt true, got %s", stdout)
	}
	if st, err := os.Stat(f.bin); err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("want the rebuilt binary at mode 0755, got %v %v", st, err)
	}
}
