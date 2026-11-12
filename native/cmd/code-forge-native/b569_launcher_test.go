package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// B569 (P4-0): the sh launcher bin/code-forge requires the Go binary and never
// falls back to Node; scripts/install.sh checks and repairs that binary. These
// tests run the real launcher and install script from the repo inside a temp
// package copy with a fake Go binary, a fake node and (when needed) a fake uname.

const b569Version = "9.8.7"

type b569Pkg struct {
	t                           *testing.T
	pkg, home, fakeBin, nodeLog string
	target, binary              string
}

func b569Target() string {
	return setupHost(runtime.GOOS, runtime.GOARCH)
}

func b569Write(t *testing.T, path, data string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// b569FakeNative answers --version like the Go verb (name and package
// version), internal version with the given build version, and records any
// other call. exitCode is what every other call exits with.
func b569FakeNative(buildVersion, exitCode string) string {
	return "#!/bin/sh\n" +
		"case \"$1 $2\" in\n" +
		"  '--version ') echo '@codedology/code-forge " + b569Version + "'; exit 0 ;;\n" +
		"  'internal version') printf '{\"version\":\"%s\",\"goVersion\":\"go1.0\"}\\n' '" + buildVersion + "'; exit 0 ;;\n" +
		"esac\n" +
		"printf 'go:%s\\n' \"$*\"\n" +
		"exit " + exitCode + "\n"
}

func newB569Pkg(t *testing.T) b569Pkg {
	t.Helper()
	if _, ok := map[string]bool{"darwin": true, "linux": true}[runtime.GOOS]; !ok {
		t.Skip("launcher targets macOS and Linux")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := b569Pkg{t: t, pkg: filepath.Join(root, "pkg with space"), home: filepath.Join(root, "home"), fakeBin: filepath.Join(root, "fakebin"), nodeLog: filepath.Join(root, "node-args"), target: b569Target()}
	p.binary = filepath.Join(p.pkg, "native", "bin", p.target, "code-forge-native")
	repo := filepath.Join("..", "..", "..")
	for _, rel := range []string{"bin/code-forge", "scripts/install.sh"} {
		data, err := os.ReadFile(filepath.Join(repo, rel))
		if err != nil {
			t.Fatal(err)
		}
		b569Write(t, filepath.Join(p.pkg, rel), string(data), 0o755)
	}
	b569Write(t, filepath.Join(p.pkg, "package.json"), "{\n  \"name\": \"@codedology/code-forge\",\n  \"version\": \""+b569Version+"\",\n  \"dependencies\": {\n    \"version\": \"0.0.1\"\n  }\n}\n", 0o644)
	b569Write(t, p.binary, b569FakeNative(b569Version, "0"), 0o755)
	if err := os.MkdirAll(p.home, 0o755); err != nil {
		t.Fatal(err)
	}
	b569Write(t, filepath.Join(p.fakeBin, "node"), "#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+p.nodeLog+"'\necho 'node served it'\nexit 0\n", 0o755)
	return p
}

// fakeUname makes `uname -s` / `uname -m` report sys and mach.
func (p b569Pkg) fakeUname(sys, mach string) {
	b569Write(p.t, filepath.Join(p.fakeBin, "uname"), "#!/bin/sh\ncase \"$1\" in\n  -s) echo '"+sys+"' ;;\n  -m) echo '"+mach+"' ;;\n  *) echo '"+sys+" "+mach+"' ;;\nesac\n", 0o755)
}

type b569Result struct {
	code           int
	stdout, stderr string
	nodeArgs       string
	nodeRan        bool
}

func (p b569Pkg) run(file string, env []string, args ...string) b569Result {
	p.t.Helper()
	_ = os.Remove(p.nodeLog)
	cmd := exec.Command(file, args...)
	cmd.Dir = p.home
	cmd.Env = append([]string{"HOME=" + p.home, "PATH=" + p.fakeBin + ":/usr/bin:/bin"}, env...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	res := b569Result{}
	if err := cmd.Run(); err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			p.t.Fatalf("%s %v: %v", file, args, err)
		}
		res.code = exit.ExitCode()
	}
	res.stdout, res.stderr = stdout.String(), stderr.String()
	if data, err := os.ReadFile(p.nodeLog); err == nil {
		res.nodeRan, res.nodeArgs = true, string(data)
	}
	return res
}

func (p b569Pkg) launch(env []string, args ...string) b569Result {
	p.t.Helper()
	return p.run(filepath.Join(p.pkg, "bin", "code-forge"), env, args...)
}

func (p b569Pkg) install(args ...string) b569Result {
	p.t.Helper()
	return p.run("sh", nil, append([]string{filepath.Join(p.pkg, "scripts", "install.sh")}, args...)...)
}

func (p b569Pkg) noEngine(problem string) string {
	return "code-forge: " + problem + "\n  expected: " + p.binary + "\n  fix: sh '" + p.pkg + "/scripts/install.sh'\n  if that fails, reinstall: npm i -g @codedology/code-forge\n"
}

func b569Expect(t *testing.T, name string, got b569Result, code int, stdout, stderr string) {
	t.Helper()
	if got.code != code || got.stdout != stdout || got.stderr != stderr {
		t.Errorf("%s: exit=%d stdout=%q stderr=%q\nwant exit=%d stdout=%q stderr=%q", name, got.code, got.stdout, got.stderr, code, stdout, stderr)
	}
	if got.nodeRan {
		t.Errorf("%s: Node ran with %q; want no Node", name, got.nodeArgs)
	}
}

func TestB569LauncherRunsGoBinary(t *testing.T) {
	p := newB569Pkg(t)
	got := p.launch(nil, "status", "two words")
	b569Expect(t, "binary present", got, 0, "go:status two words\n", "")
}

func TestB569LauncherMissingBinaryStops(t *testing.T) {
	p := newB569Pkg(t)
	if err := os.Remove(p.binary); err != nil {
		t.Fatal(err)
	}
	want := p.noEngine("the Go engine for " + p.target + " is missing")
	b569Expect(t, "missing", p.launch(nil, "status"), 69, "", want)
	b569Expect(t, "missing, no verb", p.launch(nil), 69, "", want)
	b569Expect(t, "missing setup", p.launch(nil, "setup"), 69, "", want)
}

func TestB569LauncherNotExecutableStops(t *testing.T) {
	p := newB569Pkg(t)
	if err := os.Chmod(p.binary, 0o644); err != nil {
		t.Fatal(err)
	}
	want := p.noEngine("the Go engine for " + p.target + " is not executable")
	b569Expect(t, "not executable", p.launch(nil, "block", "open", "B1"), 69, "", want)
	b569Expect(t, "not executable setup", p.launch(nil, "setup"), 69, "", want)
}

func TestB569LauncherUnsupportedHostStops(t *testing.T) {
	for _, host := range []struct{ sys, mach, target string }{
		{"Linux", "riscv64", "linux-riscv64"},
		{"FreeBSD", "amd64", "freebsd-x64"},
	} {
		p := newB569Pkg(t)
		p.fakeUname(host.sys, host.mach)
		p.binary = filepath.Join(p.pkg, "native", "bin", host.target, "code-forge-native")
		want := p.noEngine("no Go engine is built for " + host.target + " (built: darwin-arm64, darwin-x64, linux-arm64, linux-x64)")
		b569Expect(t, host.target, p.launch(nil, "status"), 69, "", want)
		b569Expect(t, host.target+" setup", p.launch(nil, "setup"), 69, "", want)
	}
}

func TestB569LauncherSetupExitCodes(t *testing.T) {
	for _, code := range []string{"0", "1", "2"} {
		p := newB569Pkg(t)
		b569Write(t, p.binary, b569FakeNative(b569Version, code), 0o755)
		got := p.launch(nil, "setup")
		b569Expect(t, "setup exit "+code, got, int(code[0]-'0'), "go:setup\n", "")
	}
	p := newB569Pkg(t)
	b569Write(t, p.binary, b569FakeNative(b569Version, "5"), 0o755)
	b569Expect(t, "setup crash", p.launch(nil, "setup"), 69, "go:setup\n", p.noEngine("the Go engine for "+p.target+" failed to run setup (exit 5)"))
}

func TestB569LauncherNativeZeroRunsNodeRouter(t *testing.T) {
	p := newB569Pkg(t)
	for _, missing := range []bool{false, true} {
		if missing {
			if err := os.Remove(p.binary); err != nil {
				t.Fatal(err)
			}
		}
		for _, args := range [][]string{{"status", "a b"}, {"setup"}} {
			got := p.launch([]string{"CODE_FORGE_NATIVE=0"}, args...)
			want := filepath.Join(p.pkg, "bin", "code-forge.mjs") + "\n" + strings.Join(args, "\n") + "\n"
			if got.code != 0 || got.stdout != "node served it\n" || got.stderr != "" || got.nodeArgs != want {
				t.Errorf("missing=%v %v: exit=%d stdout=%q stderr=%q node=%q; want the Node router", missing, args, got.code, got.stdout, got.stderr, got.nodeArgs)
			}
		}
	}
}

func TestB569InstallOK(t *testing.T) {
	p := newB569Pkg(t)
	b569Expect(t, "install ok", p.install(), 0, "code-forge install: OK Go binary "+p.target+" v"+b569Version+" ("+p.binary+")\n", "")
}

func TestB569InstallVersionMismatch(t *testing.T) {
	p := newB569Pkg(t)
	b569Write(t, p.binary, b569FakeNative("1.0.0", "0"), 0o755)
	b569Expect(t, "mismatch", p.install(), 1, "", "code-forge install: Go binary reports v1.0.0, package is v"+b569Version+": "+p.binary+"\nfix: npm i -g @codedology/code-forge@"+b569Version+"\n")
	// A source checkout (native/go.mod) names the maintainer build instead.
	b569Write(t, filepath.Join(p.pkg, "native", "go.mod"), "module x\n", 0o644)
	b569Expect(t, "mismatch source", p.install(), 1, "", "code-forge install: Go binary reports v1.0.0, package is v"+b569Version+": "+p.binary+"\nfix: node scripts/build-native.mjs --host\n")
}

func TestB569InstallRepairsExecBit(t *testing.T) {
	p := newB569Pkg(t)
	if err := os.Chmod(p.binary, 0o644); err != nil {
		t.Fatal(err)
	}
	b569Expect(t, "chmod repair", p.install(), 0, "code-forge install: OK Go binary "+p.target+" v"+b569Version+" ("+p.binary+")\n", "")
	st, err := os.Stat(p.binary)
	if err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Errorf("binary mode after install: %v %v; want executable", st, err)
	}
	// The launcher works again after the repair.
	b569Expect(t, "launch after repair", p.launch(nil, "status"), 0, "go:status\n", "")
}

func TestB569InstallMissingUnsupportedAndBroken(t *testing.T) {
	p := newB569Pkg(t)
	b569Write(t, p.binary, "#!/bin/sh\necho boom >&2\nexit 3\n", 0o755)
	b569Expect(t, "broken", p.install(), 1, "", "code-forge install: Go binary failed to run (--version: exit 3): "+p.binary+"\nfix: npm i -g @codedology/code-forge@"+b569Version+"\n")
	if err := os.Remove(p.binary); err != nil {
		t.Fatal(err)
	}
	b569Expect(t, "missing", p.install(), 1, "", "code-forge install: Go binary is missing: "+p.binary+"\nfix: npm i -g @codedology/code-forge@"+b569Version+"\n")
	p.fakeUname("Linux", "riscv64")
	b569Expect(t, "unsupported", p.install(), 1, "", "code-forge install: no Go binary is built for linux-riscv64 (built: darwin-arm64, darwin-x64, linux-arm64, linux-x64): "+filepath.Join(p.pkg, "native", "bin", "linux-riscv64", "code-forge-native")+"\nfix: use macOS or Linux on x64 or arm64\n")
}

func TestB569InstallUsage(t *testing.T) {
	p := newB569Pkg(t)
	b569Expect(t, "help", p.install("--help"), 0, "usage: sh scripts/install.sh\n", "")
	b569Expect(t, "extra arg", p.install("now"), 2, "", "usage: sh scripts/install.sh\n")
}

// The install script with the real Go binary: OK, and nothing is written to
// HOME (no hand-over or error log rows).
func TestB569InstallRealBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the Go binary")
	}
	p := newB569Pkg(t)
	if err := os.Remove(p.binary); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-ldflags", "-X main.version="+b569Version, "-o", p.binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	b569Expect(t, "real binary", p.install(), 0, "code-forge install: OK Go binary "+p.target+" v"+b569Version+" ("+p.binary+")\n", "")
	entries, err := os.ReadDir(p.home)
	if err != nil || len(entries) != 0 {
		t.Errorf("HOME after install: %v %v; want empty", entries, err)
	}
	got := p.launch(nil, "--version")
	b569Expect(t, "launcher --version", got, 0, "@codedology/code-forge "+b569Version+"\n", "")
}
