package main

// B799: `code-forge release check` in a throwaway repository with a local bare origin and fake gh
// and npm on PATH (they log their argv and never reach GitHub or the registry).

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const b799FakeGh = `#!/bin/sh
echo "gh $*" >> "$B799_LOG"
sha=$4
case "$B799_GH" in
''|green) printf '[{"headSha":"%s","status":"completed","conclusion":"success","name":"CI","workflowName":"CI"},{"headSha":"%s","status":"completed","conclusion":"skipped","name":"Pages","workflowName":"Pages"}]\n' "$sha" "$sha" ;;
red) printf '[{"headSha":"%s","status":"completed","conclusion":"failure","name":"CI","workflowName":"CI"}]\n' "$sha" ;;
pending) printf '[{"headSha":"%s","status":"in_progress","conclusion":"","name":"CI","workflowName":"CI"}]\n' "$sha" ;;
empty) echo '[]' ;;
*) echo "HTTP 401: Bad credentials" >&2; exit 1 ;;
esac
`

const b799FakeNpm = `#!/bin/sh
echo "npm $*" >> "$B799_LOG"
if [ "$1" = ls ]; then
  if [ -n "$B799_NPM_LS" ]; then cat "$B799_NPM_LS"; else echo '{"name":"@x/pkg","version":"1.4.0","dependencies":{"a":{"version":"1.0.0"}}}'; fi
  exit "${B799_NPM_LS_EXIT:-0}"
fi
echo "fake npm: unexpected $*" >&2
exit 9
`

const b799Package = `{
  "name": "@x/pkg",
  "version": "1.4.0",
  "scripts": {
    "release": "sh scripts/release.sh",
    "release:site": "sh scripts/site.sh"
  },
  "optionalDependencies": {
    "@x/pkg-darwin-arm64": "1.4.0",
    "@x/pkg-darwin-x64": "1.4.0",
    "@x/pkg-linux-arm64": "1.4.0",
    "@x/pkg-linux-x64": "1.4.0"
  }
}
`

const b799Changelog = "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- a new verb (B1)\n\n### Fixed\n\n- a bug (B2)\n\n## [1.4.0] — 2026-11-01\n\n### Fixed\n\n- old fix\n"

type b799Fixture struct {
	t                 *testing.T
	dir, repo, origin string
	log               string
}

func newB799Fixture(t *testing.T) *b799Fixture {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &b799Fixture{t: t, dir: dir, repo: filepath.Join(dir, "repo"), origin: filepath.Join(dir, "origin.git"), log: filepath.Join(dir, "fake.log")}
	bin := filepath.Join(dir, "bin")
	for _, d := range []string{bin, f.repo, filepath.Join(dir, "home")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(gitPath, filepath.Join(bin, "git")); err != nil {
		t.Fatal(err)
	}
	f.write(filepath.Join(bin, "gh"), b799FakeGh)
	f.write(filepath.Join(bin, "npm"), b799FakeNpm)
	for _, n := range []string{"gh", "npm"} {
		if err := os.Chmod(filepath.Join(bin, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range map[string]string{
		"PATH": bin + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin",
		"HOME": filepath.Join(dir, "home"), "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull,
		"GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "t@example.invalid", "GIT_COMMITTER_NAME": "Test",
		"GIT_COMMITTER_EMAIL": "t@example.invalid", "B799_LOG": f.log, "B799_GH": "", "B799_NPM_LS": "", "B799_NPM_LS_EXIT": "",
		"CODE_FORGE_NO_ERROR_LOG": "1", "CODE_FORGE_HANDOVER_LOG": "0",
	} {
		t.Setenv(k, v)
	}
	f.write(filepath.Join(f.repo, "package.json"), b799Package)
	f.write(filepath.Join(f.repo, ".gitignore"), ".code-forge/\nnode_modules/\n")
	f.write(filepath.Join(f.repo, "CHANGELOG.md"), "# Changelog\n\n## [Unreleased]\n\n## [1.4.0] — 2026-11-01\n\n### Fixed\n\n- old fix\n")
	f.git("init", "-q", "-b", "main")
	f.git("add", "-A")
	f.git("commit", "-q", "-m", "release 1.4.0")
	f.git("tag", "-a", "v1.4.0", "-m", "Release v1.4.0")
	f.write(filepath.Join(f.repo, "CHANGELOG.md"), b799Changelog)
	f.git("commit", "-qam", "B1 B2")
	f.gitIn(dir, "init", "-q", "--bare", "-b", "main", f.origin)
	f.git("remote", "add", "origin", f.origin)
	f.git("push", "-q", "origin", "main", "v1.4.0")
	return f
}

func (f *b799Fixture) write(p, body string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *b799Fixture) git(args ...string) string { return f.gitIn(f.repo, args...) }

func (f *b799Fixture) gitIn(dir string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func (f *b799Fixture) head() string { return strings.TrimSpace(f.git("rev-parse", "HEAD")) }

// call runs the CLI in-process from the repository folder.
func (f *b799Fixture) call(args ...string) (int, string, string) {
	f.t.Helper()
	b460Chdir(f.t, f.repo)
	return b799Call(f.t, args...)
}

func b799Call(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	stdout, _ := os.Create(filepath.Join(dir, "stdout"))
	stderr, _ := os.Create(filepath.Join(dir, "stderr"))
	beforeOut, beforeErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	code, runErr := run(args)
	os.Stdout, os.Stderr = beforeOut, beforeErr
	_ = stdout.Close()
	_ = stderr.Close()
	out, _ := os.ReadFile(stdout.Name())
	errText, _ := os.ReadFile(stderr.Name())
	if runErr != nil {
		errText = append(errText, []byte("run error: "+runErr.Error())...)
	}
	return code, string(out), string(errText)
}

func b799Has(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Fatalf("output lacks %q:\n%s", w, out)
		}
	}
}

const b799Steps = "  1. npm run release -- minor\n" +
	"  2. npm publish dist/npm/darwin-arm64\n  3. npm publish dist/npm/darwin-x64\n" +
	"  4. npm publish dist/npm/linux-arm64\n  5. npm publish dist/npm/linux-x64\n" +
	"  6. npm publish\n  7. npm run release:site\n  8. git push origin main --follow-tags\n"

func TestB799ReleaseCheckReady(t *testing.T) {
	f := newB799Fixture(t)
	code, out, errText := f.call("release", "check")
	if code != 0 || errText != "" {
		t.Fatalf("exit %d, stderr %q\n%s", code, errText, out)
	}
	head := f.head()
	b799Has(t, out,
		"release check: "+f.repo+" (branch main, HEAD "+head[:12]+")\n",
		"ok    tree      working tree is clean\n",
		"ok    pushed    HEAD "+head[:12]+" is on origin/main\n",
		"ok    ci        CI for HEAD "+head[:12]+" is green: CI, Pages\n",
		"ok    changelog ## [Unreleased] has entries, each under one ### heading\n",
		"ok    npm-ls    npm ls: 0 invalid, 0 missing, 0 extraneous\n",
		"bump: minor → 1.5.0 (now 1.4.0); ### Added has entries (Added 1, Fixed 1)\n",
		"steps, in order:\n"+b799Steps+"READY: every check passed\n")
	log, _ := os.ReadFile(f.log)
	if !strings.Contains(string(log), "gh run list --commit "+head+" ") || !strings.Contains(string(log), "npm ls --json") {
		t.Fatalf("fake log: %s", log)
	}
}

func TestB799ReleaseCheckJSON(t *testing.T) {
	f := newB799Fixture(t)
	code, out, _ := f.call("release", "check", "--json", "--cwd", f.repo)
	var r struct {
		Ready   bool     `json:"ready"`
		Bump    string   `json:"bump"`
		Next    string   `json:"next"`
		Current string   `json:"current"`
		Head    string   `json:"head"`
		Failed  []string `json:"failed"`
		Steps   []string `json:"steps"`
		Checks  []struct {
			Name, Status, Detail, Fix string
		} `json:"checks"`
	}
	if code != 0 || strings.Count(out, "\n") != 1 || json.Unmarshal([]byte(out), &r) != nil {
		t.Fatalf("exit %d: %q", code, out)
	}
	if !r.Ready || r.Bump != "minor" || r.Next != "1.5.0" || r.Current != "1.4.0" || r.Head != f.head() || len(r.Failed) != 0 ||
		len(r.Steps) != 8 || r.Steps[7] != "git push origin main --follow-tags" || len(r.Checks) != 5 {
		t.Fatalf("json report: %+v", r)
	}
}

func TestB799ReleaseCheckNotReady(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *b799Fixture)
		want  []string
	}{
		{"dirty tree", func(f *b799Fixture) { f.write(filepath.Join(f.repo, "notes.txt"), "x\n") }, []string{
			"FAIL  tree      the working tree is not clean (1 changed): ?? notes.txt\n", "fix: commit or remove the changes, then check again\n",
			"NOT READY: 1 check(s) failed: tree\n"}},
		{"not pushed", func(f *b799Fixture) {
			f.write(filepath.Join(f.repo, "a.txt"), "a\n")
			f.git("add", "a.txt")
			f.git("commit", "-qm", "local")
			t.Setenv("B799_GH", "empty")
		}, []string{"FAIL  pushed    HEAD ", "is not pushed (main is ahead of origin/main)\n", "fix: git push origin main\n",
			"FAIL  ci        no CI run for HEAD ", "fix: push HEAD, then wait for CI: code-forge ci watch --sha ",
			"NOT READY: 2 check(s) failed: pushed, ci\n"}},
		{"ci red", func(*b799Fixture) { t.Setenv("B799_GH", "red") }, []string{"failed: CI (failure)\n",
			"names the failed tests)\n", "NOT READY: 1 check(s) failed: ci\n"}},
		{"ci running", func(*b799Fixture) { t.Setenv("B799_GH", "pending") }, []string{"is still running: CI (in_progress)\n",
			"fix: wait for it: code-forge ci watch --sha "}},
		{"gh fails", func(*b799Fixture) { t.Setenv("B799_GH", "fail") }, []string{
			"FAIL  ci        gh run list failed (exit 1): HTTP 401: Bad credentials\n", "fix: check gh auth status"}},
		{"npm extraneous only", func(f *b799Fixture) {
			p := filepath.Join(f.dir, "ls.json")
			f.write(p, `{"name":"@x/pkg","dependencies":{"left-pad":{"version":"1.3.0","extraneous":true}},"problems":["extraneous: left-pad@1.3.0 /x/node_modules/left-pad"]}`)
			t.Setenv("B799_NPM_LS", p)
			t.Setenv("B799_NPM_LS_EXIT", "1")
		}, []string{"FAIL  npm-ls    npm ls: 1 extraneous: left-pad@1.3.0\n", "fix: npm prune (removes the extraneous packages)\n",
			"NOT READY: 1 check(s) failed: npm-ls\n"}},
		{"npm missing and extraneous", func(f *b799Fixture) {
			p := filepath.Join(f.dir, "ls.json")
			f.write(p, `{"dependencies":{"a":{"required":"^1","missing":true},"b":{"version":"2.0.0","extraneous":true}},"problems":["missing: a@^1, required by @x/pkg@1.4.0","extraneous: b@2.0.0 /x"]}`)
			t.Setenv("B799_NPM_LS", p)
			t.Setenv("B799_NPM_LS_EXIT", "1")
		}, []string{"npm ls: 1 missing: a; 1 extraneous: b@2.0.0\n", "fix: npm install, then npm prune (removes the extraneous packages)\n"}},
		{"changelog entry before heading and repeat", func(f *b799Fixture) {
			f.write(filepath.Join(f.repo, "CHANGELOG.md"), "# Changelog\n\n## [Unreleased]\n\n- loose (B3)\n\n### Fixed\n\n- a\n\n### fixed\n\n- b\n\n## [1.4.0] — 2026-11-01\n\n### Fixed\n\n- old fix\n")
			f.git("commit", "-qam", "bad changelog")
			f.git("push", "-q", "origin", "main")
		}, []string{"FAIL  changelog CHANGELOG.md line 5: an entry of \"## [Unreleased]\" comes before its first ### heading",
			"CHANGELOG.md line 11: \"### fixed\" repeats in \"## [Unreleased]\" (first at line 7): merge them into one ### fixed heading",
			"bump: patch → 1.4.1 (now 1.4.0); no ### Added entries (Fixed 1, fixed 1)\n", "  1. npm run release -- patch\n"}},
		{"changelog line under a released version", func(f *b799Fixture) {
			f.write(filepath.Join(f.repo, "CHANGELOG.md"), strings.Replace(b799Changelog, "- old fix\n", "- old fix\n- late entry (B4)\n", 1))
			f.git("commit", "-qam", "late")
			f.git("push", "-q", "origin", "main")
		}, []string{"CHANGELOG.md line 18 was added under a released version, not [Unreleased] (since v1.4.0): - late entry (B4)"}},
		{"changelog empty", func(f *b799Fixture) {
			f.write(filepath.Join(f.repo, "CHANGELOG.md"), "# Changelog\n\n## [Unreleased]\n\n### Added\n\n## [1.4.0] — 2026-11-01\n\n### Fixed\n\n- old fix\n")
			f.git("commit", "-qam", "empty")
			f.git("push", "-q", "origin", "main")
		}, []string{"\"## [Unreleased]\" has no entries: there is nothing to release", "steps, in order, once every check passes:\n"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newB799Fixture(t)
			c.setup(f)
			code, out, errText := f.call("release", "check")
			if code != 1 || errText != "" {
				t.Fatalf("exit %d, stderr %q; want 1\n%s", code, errText, out)
			}
			b799Has(t, out, c.want...)
		})
	}
}

// B778: the own platform packages at the previous version (the release bumps the pins first) do
// not fail npm ls; the same package at the pinned version but invalid does.
func TestB799ReleaseCheckOwnPlatformPackages(t *testing.T) {
	f := newB799Fixture(t)
	p := filepath.Join(f.dir, "ls.json")
	f.write(p, `{"name":"@x/pkg","dependencies":{"@x/pkg-darwin-arm64":{"version":"1.3.0","invalid":"\"1.4.0\" from the root project"}},"problems":["invalid: @x/pkg-darwin-arm64@1.3.0 /x"]}`)
	t.Setenv("B799_NPM_LS", p)
	t.Setenv("B799_NPM_LS_EXIT", "1")
	code, out, _ := f.call("release", "check")
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	b799Has(t, out, "ok    npm-ls    npm ls: 0 invalid, 0 missing, 0 extraneous (ignored the own platform packages at the previous version: @x/pkg-darwin-arm64@1.3.0)\n")

	f.write(p, `{"name":"@x/pkg","dependencies":{"@x/pkg-darwin-arm64":{"version":"1.9.0","invalid":"\"1.4.0\" from the root project"}}}`)
	code, out, _ = f.call("release", "check")
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	b799Has(t, out, "FAIL  npm-ls    npm ls: 1 invalid: @x/pkg-darwin-arm64@1.9.0\n", "fix: npm install\n")
}

func TestB799ReleaseCheckUsage(t *testing.T) {
	newB799Fixture(t)
	for _, args := range [][]string{{"release"}, {"release", "ship"}, {"release", "check", "--bogus"}, {"release", "check", "--json", "--json"}} {
		code, out, errText := b799Call(t, args...)
		if code != 2 || out != "" || !strings.HasSuffix(errText, releaseUsage+"\n") {
			t.Fatalf("%v = %d %q %q", args, code, out, errText)
		}
	}
	code, out, _ := b799Call(t, "release", "check", "--help")
	if code != 0 || !strings.Contains(out, releaseUsage) || !strings.Contains(out, "--cwd <dir>") {
		t.Fatalf("--help = %d %q", code, out)
	}
	code, _, errText := b799Call(t, "release", "check", "--cwd", filepath.Join(t.TempDir(), "nope"))
	if code != 2 || !strings.Contains(errText, "is not a folder") {
		t.Fatalf("--cwd missing = %d %q", code, errText)
	}
}

// A folder that is not a git repository fails tree and skips what needs git; no package.json
// and no CHANGELOG.md skip their checks; the steps fall back to a git tag.
func TestB799ReleaseCheckPlainFolder(t *testing.T) {
	newB799Fixture(t)
	dir := t.TempDir()
	code, out, _ := b799Call(t, "release", "check", "--cwd", dir)
	if code != 1 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	b799Has(t, out, "is not a git work tree", "skip  pushed    not a git work tree\n", "skip  changelog no CHANGELOG.md",
		"skip  npm-ls    no package.json\n", "  1. git tag -a v<x.y.z> -m \"Release v<x.y.z>\"\n  2. git push origin main --follow-tags\n",
		"NOT READY: 1 check(s) failed: tree\n")
}

// The autopilot ends with the release check: `autopilot stop` prints it as the final report when
// the run's workspace is a release-shaped repository, and keeps exit 0 when it is not ready.
func TestB799AutopilotStopPrintsReleaseCheck(t *testing.T) {
	rf := newB799Fixture(t)
	t.Setenv("B799_GH", "pending")
	f := newAutopilotFixture(t)
	f.workspace = rf.repo
	f.record(t, "r-link", "active", grantJSON("active", "2026-11-11T12:30:00.000Z", "null", `"https://example.com/doc"`))
	code, out, errText := autopilotCall(t, "stop", "--run", "r-link")
	if code != 0 || errText != "" {
		t.Fatalf("stop = %d %q %q", code, out, errText)
	}
	want := "autopilot run r-link: grant ap-0000abcd stopped\n  link      https://example.com/doc\n\nfinal report (code-forge release check):\nrelease check: " + rf.repo
	if !strings.HasPrefix(out, want) {
		t.Fatalf("stop output:\n%s\nwant prefix:\n%s", out, want)
	}
	b799Has(t, out, "FAIL  ci        CI for HEAD ", "NOT READY: 1 check(s) failed: ci\n")
}
