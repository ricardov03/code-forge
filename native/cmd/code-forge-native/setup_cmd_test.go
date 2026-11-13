package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSetupProcess runs the public router in a child process whose executable
// sits at native/bin/<host>/code-forge-native inside a fake package.
func TestSetupProcess(t *testing.T) {
	if os.Getenv("CF_SETUP_PROCESS") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("CF_SETUP_ARGS")), &args); err != nil {
		os.Exit(99)
	}
	version = os.Getenv("CF_SETUP_VERSION")
	code, _ := run(args)
	os.Exit(code)
}

type setupFixture struct {
	t        *testing.T
	pkg, exe string
}

func newSetupFixture(t *testing.T, pkgVersion string) setupFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(root, "pkg")
	binDir := filepath.Join(pkg, "native", "bin", setupHost(runtime.GOOS, runtime.GOARCH))
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@codedology/code-forge","version":"`+pkgVersion+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "code-forge-native")
	if os.Link(self, exe) != nil {
		data, err := os.ReadFile(self)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(exe, data, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return setupFixture{t: t, pkg: pkg, exe: exe}
}

// call runs `code-forge setup <args>` through the router. CODE_FORGE_NODE
// points nowhere, so a hand-over to Node exits 127 instead of running Node.
func (f setupFixture) call(binaryVersion string, args ...string) (int, string, string) {
	f.t.Helper()
	encoded, _ := json.Marshal(append([]string{"setup"}, args...))
	home := f.t.TempDir()
	cmd := exec.Command(f.exe, "-test.run=^TestSetupProcess$")
	cmd.Dir = home
	cmd.Env = append(os.Environ(), "HOME="+home, "CODE_FORGE_HANDOVER_LOG=0", "CODE_FORGE_NO_ERROR_LOG=1",
		"CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_SETUP_PROCESS=1", "CF_SETUP_ARGS="+string(encoded),
		"CF_SETUP_VERSION="+binaryVersion)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		e, ok := err.(*exec.ExitError)
		if !ok {
			f.t.Fatal(err)
		}
		code = e.ExitCode()
	}
	return code, stdout.String(), stderr.String()
}

func TestSetupRouteOK(t *testing.T) {
	f := newSetupFixture(t, "9.8.7")
	code, out, errOut := f.call("9.8.7")
	host := setupHost(runtime.GOOS, runtime.GOARCH)
	want := "code-forge setup: OK Go binary " + host + " v9.8.7 (" + f.exe + ")\n"
	if code != 0 || out != want || errOut != "" {
		t.Fatalf("setup = %d %q %q; want 0 %q and no stderr", code, out, errOut, want)
	}
}

func TestSetupRouteVersionMismatch(t *testing.T) {
	f := newSetupFixture(t, "9.8.7")
	code, out, errOut := f.call("0.0.1")
	want := "code-forge setup: Go binary reports v0.0.1, package is v9.8.7: " + f.exe + "\nfix: npm install -g @codedology/code-forge@9.8.7\n"
	if code != 1 || out != "" || errOut != want {
		t.Fatalf("setup = %d %q %q; want 1 and %q", code, out, errOut, want)
	}
}

func TestSetupRouteSourceCheckoutFix(t *testing.T) {
	f := newSetupFixture(t, "9.8.7")
	if err := os.WriteFile(filepath.Join(f.pkg, "native", "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := f.call("1.0.0")
	want := "code-forge setup: Go binary reports v1.0.0, package is v9.8.7: " + f.exe + "\nfix: node scripts/build-native.mjs --host\n"
	if code != 1 || errOut != want {
		t.Fatalf("setup = %d %q; want 1 and %q", code, errOut, want)
	}
}

func TestSetupRouteUsageAndHelp(t *testing.T) {
	f := newSetupFixture(t, "9.8.7")
	for _, args := range [][]string{{"--help"}, {"-h"}, {"x", "--help"}} {
		if code, out, errOut := f.call("9.8.7", args...); code != 0 || out != "usage: code-forge setup\n" || errOut != "" {
			t.Fatalf("setup %v = %d %q %q; want usage on stdout, exit 0", args, code, out, errOut)
		}
	}
	for _, args := range [][]string{{"--postinstall"}, {"--nope"}, {"a", "b"}} {
		if code, out, errOut := f.call("9.8.7", args...); code != 2 || out != "" || errOut != "usage: code-forge setup\n" {
			t.Fatalf("setup %v = %d %q %q; want usage on stderr, exit 2", args, code, out, errOut)
		}
	}
}

func TestSetupCheckMissingUnsupportedAndExecBits(t *testing.T) {
	pkg := t.TempDir()
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@codedology/code-forge","version":"2.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	check := func(target, binaryVersion string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := setupCheck(nil, pkg, target, binaryVersion, &out, &errOut)
		return code, out.String(), errOut.String()
	}
	binary := filepath.Join(pkg, "native", "bin", "linux-arm64", "code-forge-native")
	// B606: with no binary anywhere the message names the platform package path.
	nested := filepath.Join(pkg, "node_modules", "@codedology", "code-forge-linux-arm64", "bin", "code-forge-native")
	code, out, errOut := check("linux-arm64", "2.0.0")
	if want := "code-forge setup: Go binary is missing: " + nested + "\nfix: npm install -g @codedology/code-forge@2.0.0\n"; code != 1 || out != "" || errOut != want {
		t.Fatalf("missing = %d %q %q; want 1 and %q", code, out, errOut, want)
	}
	code, _, errOut = check("freebsd-x64", "2.0.0")
	if want := "code-forge setup: no Go binary is built for freebsd-x64 (built: darwin-arm64, darwin-x64, linux-x64, linux-arm64); code-forge runs on Node here\nfix: use macOS or Linux on x64 or arm64 for the Go binary\n"; code != 1 || errOut != want {
		t.Fatalf("unsupported = %d %q; want 1 and %q", code, errOut, want)
	}
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = check("linux-arm64", "2.0.0")
	if want := "code-forge setup: OK Go binary linux-arm64 v2.0.0 (" + binary + ")\n"; code != 0 || out != want || errOut != "" {
		t.Fatalf("exec bits = %d %q %q; want 0 and %q", code, out, errOut, want)
	}
	if st, err := os.Stat(binary); err != nil || st.Mode().Perm() != 0o755 {
		t.Fatalf("binary mode = %v, %v; want 0755 restored", st.Mode().Perm(), err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut = check("linux-arm64", "2.0.0"); code != 1 || !bytes.HasSuffix([]byte(errOut), []byte("\nfix: npm install -g @codedology/code-forge\n")) {
		t.Fatalf("bad package.json = %d %q; want 1 and a reinstall fix line", code, errOut)
	}
}

// B463 owner pick B: npm runs nothing at install time; customers run
// `code-forge setup` themselves. Loads the shipped manifest as JSON.
func TestPackageManifestHasNoInstallScripts(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, hook := range []string{"preinstall", "install", "postinstall"} {
		if script, ok := manifest.Scripts[hook]; ok {
			t.Errorf("package.json declares %q = %q; want no install-time script", hook, script)
		}
	}
	if manifest.Scripts["setup"] != "node scripts/setup.mjs" {
		t.Errorf("package.json setup script = %q; want the manual check kept", manifest.Scripts["setup"])
	}
}
