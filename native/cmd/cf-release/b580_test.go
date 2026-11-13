package main

// B580: the release builds the native targets with go build in Go and accepts a green CI run of
// HEAD in place of the local Node checks, so it runs no Node on the laptop.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// b580NodeMarker replaces the fixture's node with one that appends its argv to a marker file and
// answers --version.
func b580NodeMarker(f *fixture) string {
	marker := filepath.Join(f.dir, "node-calls")
	f.write(filepath.Join(f.bin, "node"), "#!/bin/sh\necho \"$*\" >> '"+marker+"'\n"+
		"if [ \"$1\" = --version ]; then echo v24.0.0; exit 0; fi\nexit 9\n")
	if err := os.Chmod(filepath.Join(f.bin, "node"), 0o755); err != nil {
		f.t.Fatal(err)
	}
	return marker
}

func b580Run(f *fixture, env map[string]string, pid int, args ...string) runResult {
	f.use(env)
	var out, errb bytes.Buffer
	a := newApp(&out, &errb)
	if pid != 0 {
		a.pid = pid
	}
	code := run(a, args, f.repo)
	return runResult{code, out.String(), errb.String()}
}

// b580NoNodeSuite fails the test when the release ran the local Node suite or a Node check.
func b580NoNodeSuite(t *testing.T, f *fixture, marker string) {
	t.Helper()
	for _, line := range strings.Split(string(f.readLog()), "\n") {
		for _, banned := range []string{" npm test", " npm run typecheck", "gen-config-doc", "gen-commands-doc", "build-native.mjs"} {
			if strings.Contains(line, banned) {
				t.Fatalf("the release ran %q: %s", banned, line)
			}
		}
	}
	if marker == "" {
		return
	}
	calls, err := os.ReadFile(marker)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
		if line != "" && line != "--version" {
			t.Fatalf("the release ran node %q; only node --version is allowed", line)
		}
	}
}

func TestB580NativeBuildRunsGoForFourTargets(t *testing.T) {
	f := newFixture(t, fixtureOpts{native: true})
	marker := b580NodeMarker(f)
	raw := filepath.Join(f.dir, "go-raw-out")
	r := b580Run(f, map[string]string{"FAKE_GO_RAW_OUT": raw}, 4242, "release", "minor", "--date", date)
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
	targets := []struct{ name, goos, goarch string }{
		{"darwin-arm64", "darwin", "arm64"}, {"darwin-x64", "darwin", "amd64"}, {"linux-x64", "linux", "amd64"}, {"linux-arm64", "linux", "arm64"},
	}
	native := filepath.Join(f.repo, "native")
	want := []string{f.repo + " go version"}
	var lines, rawNames []string
	for _, tg := range targets {
		binary := filepath.Join(native, "bin", tg.name, "code-forge-native")
		want = append(want, native+" go build -trimpath -ldflags -s -w -X main.version=0.22.0 -X main.reviewWaitNodeVersion=v24.0.0 -o "+
			binary+".<PID>.tmp ./cmd/code-forge-native | CGO_ENABLED=0 GOOS="+tg.goos+" GOARCH="+tg.goarch+" GOTOOLCHAIN=local GOPROXY=off")
		body := "binary " + tg.goos + "/" + tg.goarch + "\n"
		lines = append(lines, "native "+tg.name+": "+strconv.Itoa(len(body))+" bytes (v0.22.0)")
		rawNames = append(rawNames, binary+".4242.tmp")
		data, err := os.ReadFile(binary)
		if err != nil || string(data) != body {
			t.Fatalf("%s: %q, %v; want %q", binary, data, err, body)
		}
		st, _ := os.Stat(binary)
		if st.Mode().Perm() != 0o755 {
			t.Fatalf("%s mode %v, want 0755", binary, st.Mode().Perm())
		}
		left, _ := filepath.Glob(filepath.Join(native, "bin", tg.name, "*.tmp"))
		if len(left) != 0 {
			t.Fatalf("temp files left: %v", left)
		}
	}
	var goCalls []string
	for _, line := range strings.Split(string(f.readLog()), "\n") {
		if strings.Contains(line, " go ") {
			goCalls = append(goCalls, line)
		}
	}
	if strings.Join(goCalls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("go calls:\n%s\nwant:\n%s", strings.Join(goCalls, "\n"), strings.Join(want, "\n"))
	}
	if got, _ := os.ReadFile(raw); strings.TrimSpace(string(got)) != strings.Join(rawNames, "\n") {
		t.Fatalf("go -o names %q, want %q", got, rawNames)
	}
	block := strings.Join(lines, "\n") + "\nok    build-native: all four release targets\n"
	if !strings.Contains(r.stdout, "== 4. checks ==\n") || !strings.Contains(r.stdout, block) {
		t.Fatalf("stdout lacks the build lines %q:\n%s", block, r.stdout)
	}
	b580NoNodeSuite(t, f, marker)
}

func TestB580NativeBuildWithoutNode(t *testing.T) {
	f := newFixture(t, fixtureOpts{native: true})
	if err := os.Remove(filepath.Join(f.bin, "node")); err != nil {
		t.Fatal(err)
	}
	f.use(nil)
	if p, err := exec.LookPath("node"); err == nil {
		t.Skipf("a node outside the fixture is on its PATH: %s", p)
	}
	r := b580Run(f, nil, 0, "release", "minor", "--date", date)
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
	builds := 0
	for _, line := range strings.Split(string(f.readLog()), "\n") {
		if strings.Contains(line, " go build ") {
			builds++
			if strings.Contains(line, "reviewWaitNodeVersion") || !strings.Contains(line, "-ldflags -s -w -X main.version=0.22.0 -o ") {
				t.Fatalf("with no node the build must leave reviewWaitNodeVersion unset: %s", line)
			}
		}
	}
	if builds != 4 {
		t.Fatalf("%d go builds, want 4", builds)
	}
	if !strings.Contains(r.stdout, "ok    annotated tag v0.22.0") {
		t.Fatalf("no tag:\n%s", r.stdout)
	}
}

func TestB580NativeBuildWithoutGoRollsBack(t *testing.T) {
	f := newFixture(t, fixtureOpts{native: true})
	if err := os.Remove(filepath.Join(f.bin, "go")); err != nil {
		t.Fatal(err)
	}
	f.use(nil)
	if p, err := exec.LookPath("go"); err == nil {
		t.Skipf("a go outside the fixture is on its PATH: %s", p) // B600: Ubuntu CI has /usr/bin/go
	}
	before := f.state()
	r := b580Run(f, nil, 0, "release", "minor", "--date", date)
	want := "\nFAILED: build-native failed (exit 1): build-native: Go is required to build native release binaries: install Go and put go on PATH (npm users do not need Go).\nrolled back 7 files; nothing was committed or tagged\n"
	if r.code != 1 || r.stderr != want {
		t.Fatalf("exit %d stderr %q, want 1 and %q", r.code, r.stderr, want)
	}
	files := func(s string) string { return s[:strings.Index(s, "--- fake log\n")] }
	if after := f.state(); files(after) != files(before) {
		t.Fatalf("state changed\n%s", firstDiff(files(before), files(after)))
	}
}

func TestB580ReleaseNeedsGreenCI(t *testing.T) {
	cases := []struct{ gh, state string }{
		{"pending", "run status in_progress"},
		{"red", "run completed with conclusion failure"},
		{"empty", "no CI run"},
		{"foreign", "no CI run (the latest run is for wrong)"},
		{"fail", "gh run list failed (exit 1): fake gh: no runs"},
	}
	for _, c := range cases {
		t.Run(c.gh, func(t *testing.T) {
			f := newFixture(t, fixtureOpts{native: true})
			marker := b580NodeMarker(f)
			head := strings.TrimSpace(f.git(f.repo, "rev-parse", "HEAD"))
			before := f.state()
			r := b580Run(f, map[string]string{"FAKE_GH": c.gh}, 0, "release", "patch", "--date", date)
			want := "\nFAILED: CI for HEAD " + head + " is not green: " + c.state + "; push HEAD and wait for a green CI run\n" +
				"rolled back 0 files; nothing was committed or tagged\n"
			if r.code != 1 || r.stderr != want {
				t.Fatalf("exit %d stderr %q, want 1 and %q", r.code, r.stderr, want)
			}
			files := func(s string) string { return s[:strings.Index(s, "--- fake log\n")] }
			if after := f.state(); files(after) != files(before) {
				t.Fatalf("state changed\n%s", firstDiff(files(before), files(after)))
			}
			if _, err := os.Stat(filepath.Join(f.repo, "native", "bin")); !os.IsNotExist(err) {
				t.Fatalf("native/bin was written before the CI check: %v", err)
			}
			log := string(f.readLog())
			if !strings.Contains(log, " gh run list --commit "+head+" --workflow CI --limit 1 --json headSha,status,conclusion\n") {
				t.Fatalf("no gh run list for HEAD in:\n%s", log)
			}
			for _, banned := range []string{" go ", " npm pack", " npm ls", " claude "} {
				if strings.Contains(log, banned) {
					t.Fatalf("ran %q before CI was green:\n%s", banned, log)
				}
			}
			b580NoNodeSuite(t, f, marker)
		})
	}
	t.Run("green", func(t *testing.T) {
		f := newFixture(t, fixtureOpts{native: true})
		marker := b580NodeMarker(f)
		head := strings.TrimSpace(f.git(f.repo, "rev-parse", "HEAD"))
		r := b580Run(f, map[string]string{"FAKE_GH": "green"}, 0, "release", "patch", "--date", date)
		if r.code != 0 {
			t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
		}
		ok := "ok    CI for HEAD " + head + ": completed, success (npm test, npm run typecheck, gen-config-doc and gen-commands-doc --check)\n"
		if !strings.Contains(r.stdout, "== 4. checks ==\n"+ok) {
			t.Fatalf("stdout lacks %q:\n%s", ok, r.stdout)
		}
		if tags := f.git(f.repo, "tag", "--list", "v0.21.1"); strings.TrimSpace(tags) != "v0.21.1" {
			t.Fatalf("no tag v0.21.1: %q", tags)
		}
		b580NoNodeSuite(t, f, marker)
	})
}

func TestB580NoChecksSkipsCI(t *testing.T) {
	f := newFixture(t, fixtureOpts{})
	r := b580Run(f, map[string]string{"FAKE_GH": "red"}, 0, "release", "patch", "--no-checks", "--date", date)
	if r.code != 0 {
		t.Fatalf("exit %d\nstderr %s", r.code, r.stderr)
	}
	warn := "WARN  !!! --no-checks: the CI run for HEAD (npm test, typecheck, gen-config-doc and gen-commands-doc --check), npm pack, npm ls and plugin validate were NOT checked !!!\n"
	if !strings.Contains(r.stdout, warn) {
		t.Fatalf("stdout lacks %q:\n%s", warn, r.stdout)
	}
	if strings.Contains(string(f.readLog()), " gh ") {
		t.Fatalf("--no-checks read CI:\n%s", f.readLog())
	}
}
