package main

// The test fixture: a throwaway package repository with a local bare origin, a site repository
// beside it, and fake npm, gh, npx, claude and node on PATH that log their argv and never publish,
// push or deploy. The same fixture runs the Go port (in-process) and, to record the goldens, the
// Node scripts (CF_RELEASE_NODE_GOLDENS=<repo root>).

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

const fakeNpm = `#!/bin/sh
echo "$(pwd) npm $*" >> "$FAKE_LOG"
case "$1" in
view)
  if [ "$FAKE_NPM_VIEW" = E404 ]; then echo "npm error code E404" >&2; echo "npm error 404 Not Found" >&2; exit 1; fi
  echo "${FAKE_NPM_VIEW:-0.21.0}"; exit 0 ;;
test)
  if [ $# -gt 1 ]; then echo "alone $*"; exit "${FAKE_NPM_ALONE_EXIT:-0}"; fi
  if [ "${FAKE_NPM_TEST_EXIT:-0}" != 0 ]; then
    echo "TAP version 13"
    echo "not ok 1 - flaky thing"
    echo "  ---"
    echo "  location: 'test/a.test.mjs:3:1'"
    echo "  ..."
    echo "fake npm test failed" >&2
    exit "$FAKE_NPM_TEST_EXIT"
  fi
  echo "fake npm test"; exit 0 ;;
run)
  case "$2" in
  typecheck) [ "${FAKE_NPM_TYPECHECK_EXIT:-0}" = 0 ] || echo "type error in src/x.js" >&2; exit "${FAKE_NPM_TYPECHECK_EXIT:-0}" ;;
  build:site)
    mkdir -p docs-site/site/.pages-out && echo '<h1>site</h1>' > docs-site/site/.pages-out/index.html
    echo "built site with $(cat src/data/site-data.json)"; exit "${FAKE_BUILD_EXIT:-0}" ;;
  esac ;;
pack)
  name=$(sed -n 's/^  "name": "\(.*\)",$/\1/p' package.json | head -n 1)
  version=$(sed -n 's/^  "version": "\(.*\)",$/\1/p' package.json | head -n 1)
  files='{"path":"package.json"}'
  if [ -d native ]; then
    for t in darwin-arm64 darwin-x64 linux-x64 linux-arm64; do files="$files,{\"path\":\"native/bin/$t/code-forge-native\"}"; done
  fi
  printf '[{"name":"%s","version":"%s","size":1234567,"entryCount":7,"files":[%s]}]\n' "$name" "$version" "$files"
  exit 0 ;;
ls) echo '{"name":"x","dependencies":{"a":{"version":"1.0.0","dependencies":{"b":{"version":"2.0.0"}}}}}'; exit 0 ;;
ci) echo "fake npm ci"; exit 0 ;;
esac
echo "fake npm: unexpected $*" >&2
exit 9
`

const fakeGh = `#!/bin/sh
echo "$(pwd) gh $*" >> "$FAKE_LOG"
head=$(git rev-parse HEAD)
case "$FAKE_GH" in
green) printf '[{"headSha":"%s","status":"completed","conclusion":"success"}]\n' "$head" ;;
red) printf '[{"headSha":"%s","status":"completed","conclusion":"failure"}]\n' "$head" ;;
*) echo "fake gh: no runs" >&2; exit 1 ;;
esac
`

const fakeNpx = `#!/bin/sh
echo "$(pwd) npx $*" >> "$FAKE_LOG"
echo "Uploading... (3/3)"
echo "Deployment complete! Take a peek over at https://abc123.code-forge-ai.pages.dev"
`

const fakeClaude = `#!/bin/sh
echo "$(pwd) claude $*" >> "$FAKE_LOG"
if [ "$1" = plugin ] && [ "$2" = validate ]; then echo "Validation passed"; exit 0; fi
exit 9
`

// fakeNode runs the case scripts' shell twin: <script>.mjs runs <script>.mjs.sh in both modes.
const fakeNode = `#!/bin/sh
script=$1
shift
exec sh "$script.sh" "$@"
`

// caseScript is a case repository's .mjs: it runs its .sh twin, so the real node (Node goldens)
// and the fake node (Go port) do the same thing.
const caseScript = `import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
const r = spawnSync('sh', [fileURLToPath(import.meta.url) + '.sh', ...process.argv.slice(2)], { stdio: 'inherit' });
process.exit(r.status ?? 1);
`

const fixtureChangelog = `# Changelog

## [Unreleased]

### Added

- A new verb.

### Fixed

- A fixed bug.

## [0.21.0] — 2026-11-01

### Added

- The first verb.

[Unreleased]: https://github.com/example/pkg/compare/v0.21.0...HEAD
[0.21.0]: https://github.com/example/pkg/compare/v0.20.0...v0.21.0
`

const fixturePackage = `{
  "name": "@example/pkg",
  "version": "0.21.0",
  "type": "module",
  "repository": {
    "type": "git",
    "url": "git+https://github.com/example/pkg.git"
  }
}
`

const fixtureShrinkwrap = `{
  "name": "@example/pkg",
  "version": "0.21.0",
  "lockfileVersion": 3,
  "requires": true,
  "packages": {
    "": {
      "name": "@example/pkg",
      "version": "0.21.0"
    }
  }
}
`

const fixtureStepByStep = "# Step by step\n\nInstall:\n\n    npx @codedology/code-forge@0.21.0 init\n\n```\n" +
	"@codedology/code-forge 0.21.0\n```\n\nNot a pin: @codedology/code-forge@0.21.0x stays.\n"

const siteConfig = `gates:
  test: [npm, test, "{test_files}"]
  custom:
    - name: lint
      argv: [sh, -c, "echo lint $LINT_MODE CI=$CI yes=$npm_config_yes"]
      env:
        LINT_MODE: strict
  timeout_s: 60
`

type fixture struct {
	t                            *testing.T
	dir, repo, origin, site, bin string
	env                          map[string]string
}

type fixtureOpts struct {
	native  bool // native/go.mod and scripts/build-native.mjs
	site    bool // the site repository at ../code-forge-site
	noClaud bool // no claude on PATH
}

var fixedEnv = map[string]string{
	"GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": os.DevNull,
	"GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@example.invalid",
	"GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@example.invalid",
	"GIT_AUTHOR_DATE": "2026-11-12T10:00:00+00:00", "GIT_COMMITTER_DATE": "2026-11-12T10:00:00+00:00",
	"TZ": "UTC", "LC_ALL": "C",
}

func newFixture(t *testing.T, o fixtureOpts) *fixture {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, dir: dir, repo: filepath.Join(dir, "repo"), origin: filepath.Join(dir, "origin.git"),
		site: filepath.Join(dir, "code-forge-site"), bin: filepath.Join(dir, "bin"), env: map[string]string{}}
	for k, v := range fixedEnv {
		f.env[k] = v
	}
	f.env["HOME"] = filepath.Join(dir, "home")
	f.env["TMPDIR"] = filepath.Join(dir, "tmp")
	f.env["FAKE_LOG"] = filepath.Join(dir, "fake.log")
	f.env["PATH"] = f.bin + string(os.PathListSeparator) + "/usr/bin" + string(os.PathListSeparator) + "/bin"
	for _, d := range []string{f.bin, f.env["HOME"], f.env["TMPDIR"]} {
		f.mkdir(d)
	}
	f.write(f.env["FAKE_LOG"], "")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	if err := os.Symlink(gitPath, filepath.Join(f.bin, "git")); err != nil {
		t.Fatal(err)
	}
	fakes := map[string]string{"npm": fakeNpm, "gh": fakeGh, "npx": fakeNpx, "node": fakeNode}
	if !o.noClaud {
		fakes["claude"] = fakeClaude
	}
	for name, body := range fakes {
		f.write(filepath.Join(f.bin, name), body)
		if err := os.Chmod(filepath.Join(f.bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	f.mkdir(f.repo)
	files := map[string]string{
		"package.json":               fixturePackage,
		"npm-shrinkwrap.json":        fixtureShrinkwrap,
		".claude-plugin/plugin.json": "{\n  \"name\": \"pkg\",\n  \"version\": \"0.21.0\"\n}\n",
		"skill/scripts/forge":        "#!/bin/sh\n# shim: PINNED_VERSION=\"0.21.0\" is set below\nPINNED_VERSION=\"0.21.0\"\nset -eu\n",
		"CHANGELOG.md":               fixtureChangelog,
		"README.md":                  "# pkg\n\nLatest: 0.21.0 - see [CHANGELOG](CHANGELOG.md)\n",
		"docs/step-by-step.md":       fixtureStepByStep,
		"docs/guide/start.md":        "# Start\n",
		"test/a.test.mjs":            "// a test\n",
		".gitignore":                 ".code-forge-tmp/\n",
		"scripts/gen-config-doc.mjs.sh": "if [ -n \"$FAKE_DOC_FAIL\" ]; then echo \"docs/configuration.md is stale\" >&2; exit 1; fi\n" +
			"echo \"config doc ok\"\n",
		"scripts/gen-commands-doc.mjs.sh": "echo \"commands doc ok\"\n",
		"scripts/gen-site-data.mjs.sh":    "printf '{\"version\":\"0.21.0\",\"tag\":\"%s\"}\\n' \"$2\"\n",
	}
	for _, s := range []string{"gen-config-doc", "gen-commands-doc", "gen-site-data"} {
		files["scripts/"+s+".mjs"] = caseScript
	}
	if o.native {
		files["native/go.mod"] = "module example.invalid/native\n\ngo 1.22\n"
		files["scripts/build-native.mjs"] = caseScript
		files["scripts/build-native.mjs.sh"] = "echo \"built darwin-arm64 darwin-x64 linux-x64 linux-arm64\"\n"
	}
	for name, body := range files {
		f.write(filepath.Join(f.repo, name), body)
	}
	f.git(f.repo, "init", "-q", "-b", "main")
	f.git(f.repo, "add", "-A")
	f.git(f.repo, "commit", "-q", "-m", "initial")
	f.git(f.repo, "tag", "-a", "v0.21.0", "-m", "Release v0.21.0")
	through := strings.TrimSpace(f.git(f.repo, "rev-parse", "HEAD"))
	f.write(filepath.Join(f.repo, "docs/release-docs.json"), "{\n  \"through\": \""+through+"\",\n  \"tag\": \"v0.21.0\"\n}\n")
	f.git(f.repo, "add", "-A")
	f.git(f.repo, "commit", "-q", "-m", "docs: stamp")
	f.git(dir, "init", "-q", "--bare", "-b", "main", f.origin)
	f.git(f.repo, "remote", "add", "origin", f.origin)
	f.git(f.repo, "push", "-q", "origin", "main", "v0.21.0")

	if o.site {
		f.mkdir(f.site)
		site := map[string]string{
			"package.json":                    "{\n  \"name\": \"site\",\n  \"private\": true\n}\n",
			"docs-site/site/package.json":     "{\n  \"name\": \"docs\",\n  \"private\": true\n}\n",
			".code-forge.yml":                 siteConfig,
			".gitignore":                      "node_modules/\n",
			"src/data/site-data.json":         "{\"version\":\"0.20.0\"}\n",
			"src/pages/index.astro":           "<h1>home</h1>\n",
			"src/pages/docs/start.astro":      "<h1>start</h1>\n",
			"node_modules/left-pad/index.js":  "module.exports = 1;\n",
			"node_modules/@scope/x/index.js":  "module.exports = 2;\n",
			"node_modules/.cache/state":       "local\n",
			"node_modules/.bin/tool":          "#!/bin/sh\n",
			"node_modules/.package-lock.json": "{}\n",
		}
		for name, body := range site {
			f.write(filepath.Join(f.site, name), body)
		}
		f.git(f.site, "init", "-q", "-b", "main")
		f.git(f.site, "add", "-A")
		f.git(f.site, "commit", "-q", "-m", "site")
	}
	return f
}

func (f *fixture) mkdir(p string) {
	if err := os.MkdirAll(p, 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) write(p, body string) {
	f.mkdir(filepath.Dir(p))
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) environ(extra map[string]string) []string {
	var out []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(k, "GIT_") || strings.HasPrefix(k, "FAKE_") || strings.HasPrefix(k, "npm_") ||
			k == "CODE_FORGE_SITE_DIR" || f.env[k] != "" || extra[k] != "" {
			continue
		}
		out = append(out, e)
	}
	for _, m := range []map[string]string{f.env, extra} {
		for k, v := range m {
			out = append(out, k+"="+v)
		}
	}
	sort.Strings(out)
	return out
}

func (f *fixture) git(cwd string, args ...string) string {
	f.t.Helper()
	cmd := exec.Command(filepath.Join(f.bin, "git"), args...)
	if _, err := os.Stat(cmd.Path); err != nil {
		cmd = exec.Command("git", args...)
	}
	cmd.Dir = cwd
	cmd.Env = f.environ(nil)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		f.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, errb.String())
	}
	return out.String()
}

// use applies the fixture environment to this process for the in-process Go run.
func (f *fixture) use(extra map[string]string) {
	t := f.t
	for _, e := range os.Environ() {
		k, v, _ := strings.Cut(e, "=")
		if strings.HasPrefix(k, "GIT_") || strings.HasPrefix(k, "FAKE_") || strings.HasPrefix(k, "npm_") || k == "CODE_FORGE_SITE_DIR" {
			k, v := k, v
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, v) })
		}
	}
	for _, m := range []map[string]string{f.env, extra} {
		for k, v := range m {
			t.Setenv(k, v)
		}
	}
}

var (
	releaseTmpRe = regexp.MustCompile(`code-forge-release-[^/\s']+`)
	testLogRe    = regexp.MustCompile(`release-npm-test-\d+-\d+\.log`)
)

func (f *fixture) mask(s string) string {
	s = strings.ReplaceAll(s, f.dir, "<CASE>")
	s = releaseTmpRe.ReplaceAllString(s, "code-forge-release-<RAND>")
	return testLogRe.ReplaceAllString(s, "release-npm-test-<MS>-<PID>.log")
}

// state is what a run left behind: refs, the tag message, the work tree and the files a release
// edits, the release temp files, the site repository and the fake commands' argv log.
func (f *fixture) state() string {
	var b strings.Builder
	section := func(title, body string) { b.WriteString("--- " + title + "\n" + body) }
	section("repo refs", f.git(f.repo, "for-each-ref", "--format=%(refname) %(objectname) %(subject)"))
	section("repo status", f.git(f.repo, "status", "--porcelain=v1", "-uall"))
	tags := strings.Fields(f.git(f.repo, "tag", "--list"))
	for _, tag := range tags {
		section("tag "+tag, f.git(f.repo, "cat-file", "-p", "refs/tags/"+tag))
	}
	for _, name := range []string{"package.json", "npm-shrinkwrap.json", ".claude-plugin/plugin.json", "skill/scripts/forge",
		"CHANGELOG.md", "README.md", "docs/step-by-step.md", "docs/release-docs.json"} {
		data, err := os.ReadFile(filepath.Join(f.repo, name))
		if err != nil {
			section("file "+name, "(missing)\n")
			continue
		}
		section("file "+name, string(data))
	}
	section("origin refs", f.git(f.origin, "for-each-ref", "--format=%(refname) %(objectname)"))
	var tmp []string
	_ = filepath.WalkDir(f.env["TMPDIR"], func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			data, _ := os.ReadFile(p)
			tmp = append(tmp, "# "+f.mask(p)+"\n"+string(data))
		}
		return nil
	})
	sort.Strings(tmp)
	section("release temp files", strings.Join(tmp, ""))
	if _, err := os.Stat(f.site); err == nil {
		section("site refs", f.git(f.site, "for-each-ref", "--format=%(refname) %(objectname) %(subject)"))
		section("site status", f.git(f.site, "status", "--porcelain=v1", "-uall"))
		section("site worktrees", f.git(f.site, "worktree", "list", "--porcelain"))
		for _, ref := range strings.Fields(f.git(f.site, "for-each-ref", "--format=%(refname)", "refs/heads/release-site")) {
			section("site "+ref+" site-data", f.git(f.site, "show", ref+":src/data/site-data.json"))
		}
		_, err := os.Stat(filepath.Join(f.site, ".code-forge-tmp"))
		section("site .code-forge-tmp exists", strings.ToLower(strings.TrimSpace(boolWord(err == nil)))+"\n")
	}
	log, _ := os.ReadFile(f.env["FAKE_LOG"])
	section("fake log", string(log))
	return f.mask(b.String())
}

func boolWord(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
