package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// B606: a binary installed as the platform package (nested under the main package, or
// hoisted beside it) maps back to the main package: setup reads its package.json and
// names the binary it found.
func TestB606SetupFromPlatformPackage(t *testing.T) {
	host := setupHost(runtime.GOOS, runtime.GOARCH)
	for _, layout := range []string{"nested", "sibling"} {
		f := newSetupFixture(t, "9.8.7")
		lib := filepath.Join(filepath.Dir(f.pkg), "lib")
		mainPkg := filepath.Join(lib, "node_modules", "@codedology", "code-forge")
		platform := filepath.Join(mainPkg, "node_modules", "@codedology", "code-forge-"+host)
		if layout == "sibling" {
			platform = filepath.Join(lib, "node_modules", "@codedology", "code-forge-"+host)
		}
		exe := filepath.Join(platform, "bin", "code-forge-native")
		for _, dir := range []string{filepath.Dir(exe), mainPkg} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.Rename(filepath.Join(f.pkg, "package.json"), filepath.Join(mainPkg, "package.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(f.exe, exe); err != nil {
			t.Fatal(err)
		}
		f.pkg, f.exe = mainPkg, exe
		code, out, errOut := f.call("9.8.7")
		want := "code-forge setup: OK Go binary " + host + " v9.8.7 (" + exe + ")\n"
		if code != 0 || out != want || errOut != "" {
			t.Fatalf("%s: setup = %d %q %q; want 0 %q", layout, code, out, errOut, want)
		}
		if code, _, errOut = f.call("1.0.0"); code != 1 || errOut != "code-forge setup: Go binary reports v1.0.0, package is v9.8.7: "+exe+"\nfix: npm install -g @codedology/code-forge@9.8.7\n" {
			t.Fatalf("%s: mismatch = %d %q", layout, code, errOut)
		}
	}
}
