package main

// B606: each target's Go binary ships as its own npm package. After the native build the release
// writes dist/npm/<target>/ (package.json, README.md, bin/code-forge-native), packs each one and
// the main package with npm pack --dry-run (platform <35 MB, main <5 MB and no native/bin), and
// prints the four platform publishes before the main publish. It never publishes.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var b606Targets = []struct{ name, os, cpu string }{
	{"darwin-arm64", "darwin", "arm64"}, {"darwin-x64", "darwin", "x64"}, {"linux-x64", "linux", "x64"}, {"linux-arm64", "linux", "arm64"},
}

func TestB606PlatformPackagesWritten(t *testing.T) {
	f := newFixture(t, fixtureOpts{native: true})
	r := b580Run(f, nil, 0, "release", "minor", "--date", date)
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
	for _, tg := range b606Targets {
		dir := filepath.Join(f.repo, "dist", "npm", tg.name)
		data, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			Name    string            `json:"name"`
			Version string            `json:"version"`
			OS      []string          `json:"os"`
			CPU     []string          `json:"cpu"`
			Files   []string          `json:"files"`
			Scripts map[string]string `json:"scripts"`
		}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("%s package.json: %v\n%s", tg.name, err, data)
		}
		if m.Name != "@example/pkg-"+tg.name || m.Version != "0.22.0" || strings.Join(m.OS, ",") != tg.os ||
			strings.Join(m.CPU, ",") != tg.cpu || strings.Join(m.Files, ",") != "bin" || m.Scripts != nil {
			t.Fatalf("%s package.json = %+v; want @example/pkg-%s 0.22.0 os [%s] cpu [%s] files [bin], no scripts", tg.name, m, tg.name, tg.os, tg.cpu)
		}
		bin := filepath.Join(dir, "bin", "code-forge-native")
		st, err := os.Stat(bin)
		if err != nil || st.Mode().Perm() != 0o755 {
			t.Fatalf("%s: %v %v; want mode 0755", bin, st, err)
		}
		built, _ := os.ReadFile(filepath.Join(f.repo, "native", "bin", tg.name, "code-forge-native"))
		if got, _ := os.ReadFile(bin); string(got) != string(built) || len(got) == 0 {
			t.Fatalf("%s holds %q; want the native build %q", bin, got, built)
		}
		if readme, err := os.ReadFile(filepath.Join(dir, "README.md")); err != nil || !strings.Contains(string(readme), "npm i -g @example/pkg") {
			t.Fatalf("%s README.md: %q %v", tg.name, readme, err)
		}
		if !strings.Contains(r.stdout, "ok    platform package @example/pkg-"+tg.name+"@0.22.0: dist/npm/"+tg.name+"\n") ||
			!strings.Contains(r.stdout, "ok    package size: @example/pkg-"+tg.name+" 7654321 bytes (7.65 MB; limit <35 MB)\n") {
			t.Fatalf("stdout lacks the %s package and pack lines:\n%s", tg.name, r.stdout)
		}
	}
	if !strings.Contains(r.stdout, "ok    package size: @example/pkg 1234567 bytes (1.23 MB; limit <5 MB)\n") {
		t.Fatalf("stdout lacks the main pack line:\n%s", r.stdout)
	}
	// The release bumps the platform pins with the version, and dist/npm stays out of git.
	pkg, _ := os.ReadFile(filepath.Join(f.repo, "package.json"))
	shrink, _ := os.ReadFile(filepath.Join(f.repo, "npm-shrinkwrap.json"))
	for _, tg := range b606Targets {
		if !strings.Contains(string(pkg), `"@example/pkg-`+tg.name+`": "0.22.0"`) || strings.Contains(string(pkg), "0.21.0") || strings.Contains(string(shrink), "0.21.0") {
			t.Fatalf("platform pins not bumped:\n%s\n%s", pkg, shrink)
		}
	}
	if status := f.git(f.repo, "status", "--porcelain=v1", "-uall"); status != "" {
		t.Fatalf("git status after the release: %q; want clean (dist/npm ignored)", status)
	}
}

func TestB606NextStepsPublishPlatformsBeforeMain(t *testing.T) {
	f := newFixture(t, fixtureOpts{native: true})
	r := b580Run(f, nil, 0, "release", "minor", "--no-checks", "--date", date)
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
	want := "      npm publish dist/npm/darwin-arm64\n      npm publish dist/npm/darwin-x64\n      npm publish dist/npm/linux-x64\n" +
		"      npm publish dist/npm/linux-arm64\n      npm publish\n      npm run release:site\n      git push origin main --follow-tags\n"
	if !strings.Contains(r.stdout, want) {
		t.Fatalf("next steps lack the platform-first order %q:\n%s", want, r.stdout)
	}
	for _, line := range strings.Split(string(f.readLog()), "\n") {
		if strings.Contains(line, "npm publish") {
			t.Fatalf("the release published: %s", line)
		}
	}
	// A package with no native build prints the main publish only.
	plain := newFixture(t, fixtureOpts{})
	r = b580Run(plain, nil, 0, "release", "patch", "--no-checks", "--date", date)
	if r.code != 0 || strings.Contains(r.stdout, "dist/npm") || !strings.Contains(r.stdout, "      npm publish\n") {
		t.Fatalf("plain release: exit %d\n%s", r.code, r.stdout)
	}
}

func b606Refused(t *testing.T, env map[string]string, want string) {
	t.Helper()
	f := newFixture(t, fixtureOpts{native: true})
	r := b580Run(f, env, 0, "release", "minor", "--date", date)
	if r.code != 1 || !strings.Contains(r.stderr, "\nFAILED: "+want+"\nrolled back ") {
		t.Fatalf("exit %d\nstderr %q\nwant FAILED: %s", r.code, r.stderr, want)
	}
	if tags := f.git(f.repo, "tag", "--list"); strings.TrimSpace(tags) != "v0.21.0" {
		t.Fatalf("tags after a refused release: %q", tags)
	}
}

func TestB606MainPackageOver5MBFails(t *testing.T) {
	b606Refused(t, map[string]string{"FAKE_NPM_PACK_SIZE": "5000000"}, "@example/pkg must be under 5 MB to publish")
}

func TestB606PlatformPackageOver35MBFails(t *testing.T) {
	b606Refused(t, map[string]string{"FAKE_NPM_PACK_BIG": "linux-x64"}, "@example/pkg-linux-x64 must be under 35 MB to publish")
}

func TestB606MainPackageWithNativeBinFails(t *testing.T) {
	b606Refused(t, map[string]string{"FAKE_NPM_PACK_NATIVE_BIN": "1"},
		"npm pack --dry-run: @example/pkg ships native/bin/linux-x64/code-forge-native; the Go binaries ship in the platform packages (dist/npm/<target>)")
}

func TestB606MissingPlatformPinRefused(t *testing.T) {
	f := newFixture(t, fixtureOpts{native: true})
	f.write(filepath.Join(f.repo, "package.json"), fixturePackage)
	f.git(f.repo, "commit", "-q", "-am", "drop pins")
	f.git(f.repo, "push", "-q", "origin", "main")
	r := b580Run(f, nil, 0, "release", "minor", "--dry-run", "--date", date)
	if r.code == 0 || !strings.Contains(r.stderr, "package.json: optionalDependencies.@example/pkg-darwin-arm64 is undefined, package.json says 0.21.0") {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
}
