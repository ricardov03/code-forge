package main

// Multi-step parity with the retired Node scripts (B574): every case the Node tests
// (test/release*.test.mjs, test/changelog.test.mjs, test/docs-block.test.mjs) checked and the
// single-step goldens did not, recorded from the Node scripts on the same fixture before they were
// deleted. Each step runs the tool once (after its own prep) and the golden holds every step's exit
// code, stdout, stderr and state, plus the files a step can leave beside them (the failing-test log,
// what each check saw, package-lock.json, the release commit's file list and the site checkout).
// Record with a checkout that still has the Node scripts (B570, 5c2510a7):
//
//	CF_RELEASE_NODE_GOLDENS=<repo root> go test ./cmd/cf-release -run TestNodeFlowParity -count=1
//
// B580 re-recorded the release goldens from Go (see golden_test.go: CF_RELEASE_UPDATE_GOLDENS).

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type flowStep struct {
	prep func(f *fixture)
	env  map[string]string
	args []string
}

type flowCase struct {
	name  string
	opts  fixtureOpts
	prep  func(f *fixture)
	steps []flowStep
}

func flowOne(args ...string) []flowStep { return []flowStep{{args: args}} }

func flowEnvStep(env map[string]string, args ...string) flowStep {
	return flowStep{env: env, args: args}
}

func flowCommitAll(f *fixture, msg string) {
	f.git(f.repo, "add", "-A")
	f.git(f.repo, "commit", "-q", "-m", msg)
}

// flowRestamp commits a docs stamp that covers HEAD and names tag.
func flowRestamp(f *fixture, tag string) {
	through := strings.TrimSpace(f.git(f.repo, "rev-parse", "HEAD"))
	f.write(filepath.Join(f.repo, "docs/release-docs.json"), "{\"through\":\""+through+"\",\"tag\":\""+tag+"\"}\n")
	flowCommitAll(f, "docs stamp")
}

// flowCommitFile commits one file with the given text.
func flowCommitFile(f *fixture, file, text string) {
	f.write(filepath.Join(f.repo, file), text)
	f.git(f.repo, "add", "--", file)
	f.git(f.repo, "commit", "-q", "-m", "change "+file)
}

func flowRemove(f *fixture, p string) {
	if err := os.RemoveAll(p); err != nil {
		f.t.Fatal(err)
	}
}

// flowCaseFile writes a file the fakes read from the case folder (npm-test.out, npm-test.err).
func flowCaseFile(f *fixture, name, body string) { f.write(filepath.Join(f.dir, name), body) }

func flowSeenDir(f *fixture) { f.mkdir(filepath.Join(f.dir, "seen")) }

func flowOtherClone(f *fixture) string {
	other := filepath.Join(f.dir, "other")
	f.git(f.dir, "clone", "-q", f.origin, other)
	return other
}

const (
	flowReadmeIntro  = "# pkg\n\nA useful tool.\n\n"
	flowReadmeLegacy = "\n## What's new in 0.21\n\nIn the next release:\n\n- New.\n  Continued.\n\nIn 0.21.0:\n\n- Old.\n"
	flowClHead       = "# Changelog\n\nIntro line.\n\n"
	flowClReleased   = "## [0.1.0] — first release\n\n### Added\n\n- First.\n"
	flowClBase       = flowClHead + "## [Unreleased]\n\n### Added\n\n- **One.** An entry that wraps onto a second line because it is long enough to pass the column\n" +
		"  the file uses.\n\n### Fixed\n\n- A fix.\n\n" + flowClReleased
	flowEmptyUnreleased = "# Changelog\n\n## [Unreleased]\n\n### Added\n\n## [0.21.0] — 2026-11-01\n\n- First.\n"
	flowCanonicalLock   = "{\n  \"name\": \"@example/pkg\",\n  \"version\": \"0.21.0\",\n  \"lockfileVersion\": 3,\n  \"requires\": true,\n" +
		"  \"packages\": {\n    \"\": {\n      \"name\": \"@example/pkg\",\n      \"version\": \"0.21.0\"\n    }\n  }\n}\n"
)

func flowReadmeWith(text string) func(f *fixture) {
	return func(f *fixture) { flowCommitFile(f, "README.md", text) }
}

func flowChangelogWith(text string) func(f *fixture) {
	return func(f *fixture) { f.write(filepath.Join(f.repo, "CHANGELOG.md"), text) }
}

func flowStampText(text func(f *fixture) string) func(f *fixture) {
	return func(f *fixture) { flowCommitFile(f, "docs/release-docs.json", text(f)) }
}

func flowHeadSha(f *fixture) string { return strings.TrimSpace(f.git(f.repo, "rev-parse", "HEAD")) }

func flowBothModes(args ...string) []flowStep {
	return []flowStep{{args: args}, {args: append(append([]string{}, args...), "--dry-run")}}
}

var flowCases = []flowCase{
	// B580: the release reads HEAD's CI run instead of running the Node checks.
	{name: "release-ci-empty", steps: []flowStep{flowEnvStep(map[string]string{"FAKE_GH": "empty"}, "release", "patch", "--date", date)}},
	{name: "release-ci-pending", steps: []flowStep{flowEnvStep(map[string]string{"FAKE_GH": "pending"}, "release", "patch", "--date", date)}},
	{name: "release-ci-foreign", steps: []flowStep{flowEnvStep(map[string]string{"FAKE_GH": "foreign"}, "release", "patch", "--date", date)}},

	// B263: docs pins and their rollback.
	{name: "release-minor-dry-then-real", steps: []flowStep{{args: []string{"release", "minor", "--dry-run", "--date", date}},
		{args: []string{"release", "minor", "--date", date}}}},
	{name: "release-docs-crlf", prep: func(f *fixture) {
		unchanged := "history 0.21.0\r\n@codedology/code-forge 0.21.0 extra\r\n@codedology/code-forge 0.20.9\r\n@codedology/code-forge@0.21.00\r\n" +
			"@codedology/code-forge@0.21.0-beta\r\n@codedology/code-forge@0x21x0\r\n"
		flowCommitFile(f, "docs/step-by-step.md", "npm install --save-dev --save-exact @codedology/code-forge@0.21.0\r\n@codedology/code-forge 0.21.0\r\n"+unchanged)
	}, steps: []flowStep{{args: []string{"release", "minor", "--date", date}},
		{prep: func(f *fixture) {
			text, _ := os.ReadFile(filepath.Join(f.repo, "CHANGELOG.md"))
			flowCommitFile(f, "CHANGELOG.md", strings.Replace(string(text), "## [Unreleased]\n", "## [Unreleased]\n\n### Fixed\n\n- Next fix.\n", 1))
			flowRestamp(f, "v0.22.0")
		}, env: map[string]string{"FAKE_NPM_PACK_EXIT": "1"}, args: []string{"release", "patch", "--date", date}}}},

	// Preflight refusals.
	{name: "release-explicit-not-greater", steps: []flowStep{{args: []string{"release", "0.21.0"}}, {args: []string{"release", "0.20.9"}}}},
	{name: "release-behind-origin", prep: func(f *fixture) {
		other := flowOtherClone(f)
		f.write(filepath.Join(other, "new.txt"), "x")
		f.git(other, "add", "new.txt")
		f.git(other, "commit", "-q", "-m", "upstream")
		f.git(other, "push", "-q", "origin", "main")
	}, steps: flowOne("release", "patch")},
	{name: "release-tag-local", prep: func(f *fixture) { f.git(f.repo, "tag", "v0.21.1") }, steps: flowOne("release", "patch")},
	{name: "release-tag-origin", prep: func(f *fixture) {
		other := flowOtherClone(f)
		f.git(other, "tag", "v0.21.1")
		f.git(other, "push", "-q", "origin", "v0.21.1")
	}, steps: flowOne("release", "patch")},
	{name: "release-two-pins", prep: func(f *fixture) {
		text, _ := os.ReadFile(filepath.Join(f.repo, "skill/scripts/forge"))
		flowCommitFile(f, "skill/scripts/forge", string(text)+"PINNED_VERSION=\"0.21.0\"\n")
		flowRestamp(f, "v0.21.0")
	}, steps: flowOne("release", "patch")},
	{name: "release-wrong-branch-real", prep: func(f *fixture) { f.git(f.repo, "switch", "-q", "-c", "feat") }, steps: flowOne("release", "minor")},

	// B108 and B313: failing checks roll back every rewritten file.
	{name: "release-lock-checks-fail", prep: func(f *fixture) {
		flowCommitFile(f, "package-lock.json", flowCanonicalLock)
		flowRestamp(f, "v0.21.0")
		flowSeenDir(f)
	}, steps: []flowStep{flowEnvStep(map[string]string{"FAKE_GH": "red"}, "release", "minor", "--date", date),
		flowEnvStep(map[string]string{"FAKE_NPM_PACK_EXIT": "1"}, "release", "minor", "--date", date)}},

	// B312: the docs stamp preflight, in real and dry-run mode.
	{name: "release-stamp-missing", prep: func(f *fixture) {
		flowRemove(f, filepath.Join(f.repo, "docs/release-docs.json"))
		flowCommitAll(f, "no stamp")
	}, steps: flowBothModes("release", "patch")},
	{name: "release-stamp-invalid-json", prep: flowStampText(func(*fixture) string { return "{broken" }), steps: flowBothModes("release", "patch")},
	{name: "release-stamp-null", prep: flowStampText(func(*fixture) string { return "null" }), steps: flowBothModes("release", "patch")},
	{name: "release-stamp-missing-fields", prep: flowStampText(func(*fixture) string { return "{}" }), steps: flowBothModes("release", "patch")},
	{name: "release-stamp-wrong-type", prep: flowStampText(func(*fixture) string { return `{"through":42,"tag":"v0.21.0"}` }),
		steps: flowBothModes("release", "patch")},
	{name: "release-stamp-older-than-release", prep: func(f *fixture) {
		f.git(f.repo, "tag", "-d", "v0.21.0")
		f.git(f.repo, "tag", "v0.20.0")
		through := flowHeadSha(f)
		flowCommitFile(f, "README.md", "# pkg\n\nLatest: 0.21.0 - see [CHANGELOG](CHANGELOG.md)\n\nReleased update.\n")
		f.git(f.repo, "tag", "-a", "v0.21.0", "-m", "latest release")
		flowCommitFile(f, "docs/release-docs.json", `{"through":"`+through+`","tag":"v0.20.0"}`)
	}, steps: flowBothModes("release", "patch")},
	{name: "release-stamp-unknown-commit", prep: flowStampText(func(*fixture) string {
		return `{"through":"` + strings.Repeat("0", 40) + `","tag":"v0.21.0"}`
	}), steps: flowBothModes("release", "patch")},
	{name: "release-stamp-blob", prep: flowStampText(func(f *fixture) string {
		return `{"through":"` + strings.TrimSpace(f.git(f.repo, "rev-parse", "HEAD:README.md")) + `","tag":"v0.21.0"}`
	}), steps: flowBothModes("release", "patch")},
	{name: "release-stamp-future-commit", prep: flowStampText(func(f *fixture) string {
		f.git(f.repo, "switch", "-q", "-c", "future")
		f.git(f.repo, "commit", "--allow-empty", "-q", "-m", "future")
		through := flowHeadSha(f)
		f.git(f.repo, "switch", "-q", "main")
		return `{"through":"` + through + `","tag":"v0.21.0"}`
	}), steps: flowBothModes("release", "patch")},
	{name: "release-stamp-wrong-tag", prep: flowStampText(func(f *fixture) string {
		return `{"through":"` + flowHeadSha(f) + `","tag":"v0.20.0"}`
	}), steps: flowBothModes("release", "patch")},
	{name: "release-code-after-stamp", prep: func(f *fixture) { flowCommitFile(f, "src/feature.mjs", "export const feature = true;\n") },
		steps: []flowStep{{args: []string{"release", "patch", "--dry-run"}}, {args: []string{"release", "patch"}}}},
	{name: "release-docs-after-stamp", prep: func(f *fixture) {
		flowCommitFile(f, "docs/reference/guide.md", "# New guide\n")
		flowCommitFile(f, "README.md", "# pkg\n\nLatest: 0.21.0 - see [CHANGELOG](CHANGELOG.md)\n\nNew usage.\n")
		flowCommitFile(f, "CHANGELOG.md", strings.Replace(fixtureChangelog, "- A new verb.", "- A documented new verb.", 1))
	}, steps: []flowStep{{args: []string{"release", "patch", "--dry-run"}}, {args: []string{"release", "patch"}}}},
	{name: "release-code-renamed-into-docs", prep: func(f *fixture) {
		flowCommitFile(f, "src/feature.mjs", "export const feature = true;\n")
		flowRestamp(f, "v0.21.0")
		f.git(f.repo, "config", "diff.renames", "true")
		f.git(f.repo, "mv", "src/feature.mjs", "docs/feature.mjs")
		f.git(f.repo, "commit", "-q", "-m", "move code into docs")
	}, steps: flowBothModes("release", "patch")},
	{name: "release-six-disallowed-paths", prep: func(f *fixture) {
		for _, file := range []string{"README.md.bak", "docs-other.mjs", "src/a.mjs", "src/b.mjs", "src/c.mjs", "src/d.mjs"} {
			f.write(filepath.Join(f.repo, file), "changed\n")
		}
		flowCommitAll(f, "six changes")
	}, steps: flowOne("release", "patch")},
	{name: "release-no-earlier-tag", prep: func(f *fixture) { f.git(f.repo, "tag", "-d", "v0.21.0") },
		steps: []flowStep{{args: []string{"release", "patch", "--dry-run"}}, {args: []string{"release", "patch"}}}},
	{name: "release-no-checks-skips-docs-block", prep: func(f *fixture) {
		flowRemove(f, filepath.Join(f.repo, "docs/release-docs.json"))
		flowCommitAll(f, "no stamp")
	}, steps: []flowStep{{args: []string{"release", "patch", "--no-checks", "--dry-run"}}, {args: []string{"release", "patch", "--no-checks"}}}},
	{name: "release-tags-outside-ancestry", prep: func(f *fixture) {
		f.git(f.repo, "tag", "-d", "v0.21.0")
		f.git(f.repo, "switch", "-q", "-c", "other")
		f.git(f.repo, "commit", "--allow-empty", "-q", "-m", "unmerged release")
		f.git(f.repo, "tag", "v9.0.0")
		f.git(f.repo, "switch", "-q", "main")
		f.git(f.repo, "tag", "preview")
	}, steps: []flowStep{{args: []string{"release", "patch", "--dry-run"}},
		{prep: func(f *fixture) {
			f.git(f.repo, "tag", "-a", "v0.21.0", "-m", "reachable release")
			flowRestamp(f, "v0.21.0")
		}, args: []string{"release", "patch", "--dry-run"}}}},

	// test/release-readme.test.mjs: only the Latest line changes; legacy lists stay.
	{name: "readme-latest-patch", prep: flowReadmeWith(flowReadmeLatest()), steps: flowDryThenReal("patch")},
	{name: "readme-latest-minor", prep: flowReadmeWith(flowReadmeLatest()), steps: flowDryThenReal("minor")},
	{name: "readme-latest-major", prep: flowReadmeWith(flowReadmeLatest()), steps: flowDryThenReal("major")},
	{name: "readme-latest-explicit-patch", prep: flowReadmeWith(flowReadmeLatest()), steps: flowDryThenReal("0.21.3")},
	{name: "readme-latest-explicit-minor", prep: flowReadmeWith(flowReadmeLatest()), steps: flowDryThenReal("0.22.2")},
	{name: "readme-no-latest-intro", prep: flowReadmeWith(flowReadmeIntro), steps: flowOne("release", "patch")},
	{name: "readme-no-latest-legacy", prep: flowReadmeWith(flowReadmeIntro + flowReadmeLegacy), steps: flowOne("release", "patch")},
	{name: "readme-no-latest-two-anchors", prep: flowReadmeWith(flowReadmeIntro + strings.Replace(flowReadmeLegacy, "In the next release:",
		"In the next release:\nIn the next release:", 1)), steps: flowOne("release", "patch")},
	{name: "readme-no-latest-usage", prep: flowReadmeWith(flowReadmeIntro + strings.Replace(flowReadmeLegacy, "## What's new in 0.21", "## Usage", 1)),
		steps: flowOne("release", "patch")},
	{name: "readme-no-latest-old-heading", prep: flowReadmeWith(flowReadmeIntro + strings.Replace(flowReadmeLegacy, "## What's new in 0.21",
		"## What's new in 0.20", 1)), steps: flowOne("release", "patch")},
	{name: "readme-no-latest-prose", prep: flowReadmeWith(flowReadmeIntro + strings.Replace(flowReadmeLegacy, "- New.", "Some prose.", 1)),
		steps: flowOne("release", "patch")},
	{name: "readme-no-latest-no-bullets", prep: flowReadmeWith(flowReadmeIntro + strings.Replace(flowReadmeLegacy, "- New.\n  Continued.\n", "", 1)),
		steps: flowOne("release", "patch")},
	{name: "readme-missing", prep: func(f *fixture) {
		flowRemove(f, filepath.Join(f.repo, "README.md"))
		flowCommitAll(f, "no readme")
	}, steps: flowBothModes("release", "patch")},
	{name: "readme-empty-unreleased", prep: func(f *fixture) {
		flowCommitFile(f, "README.md", flowReadmeIntro)
		flowCommitFile(f, "CHANGELOG.md", flowEmptyUnreleased)
	}, steps: flowBothModes("release", "patch")},
	{name: "readme-legacy-empty-unreleased", prep: func(f *fixture) {
		flowCommitFile(f, "README.md", flowReadmeIntro+flowReadmeLegacy)
		flowCommitFile(f, "CHANGELOG.md", flowEmptyUnreleased)
	}, steps: flowBothModes("release", "patch")},

	// test/release-site.test.mjs.
	{name: "site-custom-gate-fails", opts: fixtureOpts{site: true}, prep: func(f *fixture) {
		f.write(filepath.Join(f.site, ".code-forge.yml"), strings.Replace(siteConfig, `"echo lint $LINT_MODE CI=$CI yes=$npm_config_yes"`,
			`"echo lint failed; exit 3"`, 1))
		f.git(f.site, "commit", "-q", "-am", "red lint")
	}, steps: flowOne("release-site")},
	{name: "site-test-gate-fails", opts: fixtureOpts{site: true},
		steps: []flowStep{flowEnvStep(map[string]string{"FAKE_NPM_TEST_EXIT": "1", "FAKE_NPM_ALONE_EXIT": "1"}, "release-site")}},
	{name: "site-dirty-main", opts: fixtureOpts{site: true}, prep: func(f *fixture) {
		f.write(filepath.Join(f.site, "src/pages/index.astro"), "staged open work\n")
		f.git(f.site, "add", "src/pages/index.astro")
		f.write(filepath.Join(f.site, "src/pages/index.astro"), "unstaged open work\n")
		f.write(filepath.Join(f.site, "src/data/site-data.json"), "unfinished data\n")
		f.write(filepath.Join(f.site, "open.bin"), "\x00untracked\xff\n")
	}, steps: flowOne("release-site")},
	{name: "site-nested-linked-dry", opts: fixtureOpts{site: true}, prep: func(f *fixture) {
		f.write(filepath.Join(f.site, "docs-site/site/node_modules/dep.txt"), "existing docs dependencies\n")
	}, steps: flowOne("release-site", "--dry-run")},
	{name: "site-fresh-install-dry", opts: fixtureOpts{site: true}, prep: func(f *fixture) { flowRemove(f, filepath.Join(f.site, "node_modules")) },
		steps: flowOne("release-site", "--dry-run")},
	{name: "site-deploy-dist", opts: fixtureOpts{site: true}, steps: []flowStep{
		flowEnvStep(map[string]string{"FAKE_BUILD_DIST": "1"}, "release-site", "--deploy-dir", "dist")}},
	{name: "site-deploy-dist-dry", opts: fixtureOpts{site: true}, steps: []flowStep{
		flowEnvStep(map[string]string{"FAKE_BUILD_DIST": "1"}, "release-site", "--deploy-dir", "dist", "--dry-run")}},
	{name: "site-merged-none", opts: fixtureOpts{site: true}, steps: []flowStep{
		flowEnvStep(map[string]string{"FAKE_BUILD_MERGED": "none"}, "release-site"),
		flowEnvStep(map[string]string{"FAKE_BUILD_MERGED": "none"}, "release-site", "--dry-run")}},
	{name: "site-merged-noindex", opts: fixtureOpts{site: true}, steps: []flowStep{
		flowEnvStep(map[string]string{"FAKE_BUILD_MERGED": "noindex"}, "release-site"),
		flowEnvStep(map[string]string{"FAKE_BUILD_MERGED": "noindex"}, "release-site", "--dry-run")}},
	{name: "site-deploy-dir-absolute", steps: flowOne("release-site", "--deploy-dir", "/tmp/site")},
	{name: "site-deploy-dir-dotdot-inside", steps: flowOne("release-site", "--deploy-dir", "docs-site/../../outside")},
	{name: "site-deploy-dir-root", steps: flowOne("release-site", "--deploy-dir", ".")},

	// test/changelog.test.mjs.
	{name: "changelog-base-add", prep: flowChangelogWith(flowClBase), steps: flowOne("changelog", "added", "Two.")},
	{name: "changelog-base-changed-then-security", prep: flowChangelogWith(flowClBase),
		steps: []flowStep{{args: []string{"changelog", "changed", "Changed one."}}, {args: []string{"changelog", "SECURITY", "Secured."}}}},
	{name: "changelog-no-unreleased", prep: flowChangelogWith(flowClHead + flowClReleased), steps: flowOne("changelog", "removed", "Gone.")},
	{name: "changelog-wrap", prep: flowChangelogWith(flowClBase),
		steps: flowOne("changelog", "fixed", "**Bold lead.** "+strings.TrimSpace(strings.Repeat("word ", 30)))},
	{name: "changelog-base-refusals", prep: flowChangelogWith(flowClBase), steps: []flowStep{
		{args: []string{"changelog", "fixed", "A fix."}},
		{args: []string{"changelog", "added", "**One.** An entry that wraps onto a second line because it is long enough to pass the column the file uses."}},
		{args: []string{"changelog", "added", "   "}},
		{args: []string{"changelog", "improved", "x"}}}},
	{name: "changelog-base-list", prep: flowChangelogWith(flowClBase), steps: flowOne("changelog", "list")},
	{name: "changelog-list-empty", prep: flowChangelogWith(flowClHead + flowClReleased), steps: flowOne("changelog", "list")},

	// test/docs-block.test.mjs.
	{name: "docs-block-brief-custom-site", prep: func(f *fixture) {
		flowDocsBlockPrep(f)
		for _, page := range []string{"index.astro", "guide.astro", "pricing.astro", "ignore.md", "docs/x.astro", "docs/ignore.md"} {
			f.write(filepath.Join(f.dir, "custom site/src/pages", page), "page\n")
		}
	}, steps: []flowStep{flowEnvStep(map[string]string{"CODE_FORGE_SITE_DIR": "../custom site"}, "docs-block", "brief")}},
	{name: "docs-block-brief-no-tag", prep: func(f *fixture) {
		flowDocsBlockPrep(f)
		f.git(f.repo, "tag", "-d", "v0.21.0")
	}, steps: []flowStep{flowEnvStep(map[string]string{"CODE_FORGE_SITE_DIR": "../absent"}, "docs-block", "brief")}},
	{name: "docs-block-stamp-tracked", prep: func(f *fixture) { f.write(filepath.Join(f.repo, "README.md"), "dirty\n") },
		steps: flowOne("docs-block", "stamp")},
	{name: "docs-block-stamp-staged", prep: func(f *fixture) {
		f.write(filepath.Join(f.repo, "README.md"), "dirty\n")
		f.git(f.repo, "add", "README.md")
	}, steps: flowOne("docs-block", "stamp")},
	{name: "docs-block-stamp-then-release", prep: func(f *fixture) {
		flowCommitFile(f, "src/x.js", "export {};\n")
	}, steps: []flowStep{{args: []string{"docs-block", "stamp"}},
		{prep: func(f *fixture) {
			f.git(f.repo, "add", "docs/release-docs.json")
			f.git(f.repo, "commit", "-q", "-m", "docs stamp")
			f.git(f.repo, "push", "-q", "origin", "main")
		}, args: []string{"release", "patch", "--date", date}}}},
}

func flowReadmeLatest() string {
	return flowReadmeIntro + "Latest: 0.21.0 - see [CHANGELOG.md](CHANGELOG.md)\n" + flowReadmeLegacy
}

func flowDryThenReal(bump string) []flowStep {
	return []flowStep{{args: []string{"release", bump, "--dry-run", "--date", date}}, {args: []string{"release", bump, "--date", date}}}
}

// flowDocsBlockPrep gives the repository a release delta like test/docs-block.test.mjs: a feature
// commit, a docs commit, nested docs and a non-Markdown file under docs/.
func flowDocsBlockPrep(f *fixture) {
	f.write(filepath.Join(f.repo, "docs/reference/nested.md"), "# Nested\n")
	f.write(filepath.Join(f.repo, "docs/ignore.txt"), "not markdown\n")
	f.write(filepath.Join(f.repo, "feature.mjs"), "export const feature = true;\n")
	flowCommitAll(f, "add feature")
	flowCommitFile(f, "docs/guide/start.md", "# Updated start\n")
}

// leftovers is what a step can leave outside f.state(): the failing-test logs, what each check
// saw, package-lock.json, HEAD's subject and files, and every file of the site checkout with its
// index.
func (f *fixture) leftovers() string {
	var b strings.Builder
	section := func(title, body string) { b.WriteString("--- " + title + "\n" + body) }
	files := func(root string, skip func(rel string) bool) string {
		var out []string
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			if d.IsDir() {
				if skip != nil && skip(rel) {
					return filepath.SkipDir
				}
				return nil
			}
			data, _ := os.ReadFile(p)
			out = append(out, "# "+rel+"\n"+strconv.Quote(string(data))+"\n")
			return nil
		})
		sort.Strings(out)
		return strings.Join(out, "")
	}
	section("repo .code-forge-tmp", files(filepath.Join(f.repo, ".code-forge-tmp"), nil))
	section("seen", files(filepath.Join(f.dir, "seen"), nil))
	if data, err := os.ReadFile(filepath.Join(f.repo, "package-lock.json")); err == nil {
		section("file package-lock.json", string(data))
	}
	section("repo HEAD", f.git(f.repo, "show", "--name-only", "--format=%s", "HEAD"))
	if _, err := os.Stat(f.site); err == nil {
		section("site files", files(f.site, func(rel string) bool { return rel == ".git" }))
		section("site index", f.git(f.site, "ls-files", "-s"))
	}
	return f.mask(b.String())
}

func TestNodeFlowParity(t *testing.T) {
	nodeRoot := os.Getenv("CF_RELEASE_NODE_GOLDENS")
	update := os.Getenv("CF_RELEASE_UPDATE_GOLDENS") != ""
	for _, c := range flowCases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.opts)
			if c.prep != nil {
				c.prep(f)
			}
			var got strings.Builder
			for i, s := range c.steps {
				if s.prep != nil {
					s.prep(f)
				}
				pc := parityCase{name: c.name, env: s.env, args: s.args}
				got.WriteString("=== step " + strconv.Itoa(i+1) + ": " + strings.Join(s.args, " ") + "\n")
				var r runResult
				if nodeRoot != "" {
					r = f.runNode(nodeRoot, pc)
				} else {
					r = f.runGo(pc) // f.use drops the FAKE_* and CODE_FORGE_SITE_DIR keys of earlier steps
				}
				got.WriteString(f.report(r))
				got.WriteString(f.leftovers())
			}
			golden := filepath.Join("testdata", "golden", "flow", c.name+".txt")
			if nodeRoot != "" || update {
				f.mkdir(filepath.Dir(golden))
				if err := os.WriteFile(golden, []byte(got.String()), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden %s (record it from Node): %v", golden, err)
			}
			if got.String() != string(want) {
				t.Errorf("Go output differs from the Node golden %s\n%s", golden, firstDiff(string(want), got.String()))
			}
		})
	}
}

// test/release-site.test.mjs "waits for the exact npm version": npm does not serve the version on
// the first poll; the tool waits one poll interval (injected clock, no real sleep), then generates,
// builds, commits and deploys exactly once. Node's own run of this case sleeps 30 s, so it has no
// golden; the order of the fakes' calls is the Node test's assertion.
func TestReleaseSiteWaitsForNpmThenDeploysOnce(t *testing.T) {
	f := newFixture(t, fixtureOpts{site: true})
	f.use(map[string]string{"FAKE_NPM_VIEW_AFTER": "1"})
	var out, errb bytes.Buffer
	a := newApp(&out, &errb)
	clock := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	var slept []time.Duration
	a.now = func() time.Time { return clock }
	a.sleep = func(d time.Duration) { slept = append(slept, d); clock = clock.Add(d) }
	if code := run(a, []string{"release-site"}, f.repo); code != 0 {
		t.Fatalf("exit %d, want 0\nstdout %s\nstderr %s", code, out.String(), errb.String())
	}
	if len(slept) != 1 || slept[0] != 30*time.Second {
		t.Fatalf("slept %v, want exactly one 30 s wait", slept)
	}
	stdout := out.String()
	wait := strings.Index(stdout, "Waiting for npm to serve @example/pkg@0.21.0 (poll every 30 s)\n")
	served := strings.Index(stdout, "npm serves @example/pkg@0.21.0\n")
	if wait < 0 || served < wait || strings.Count(stdout, "Waiting for npm") != 1 {
		t.Fatalf("want one wait line before the served line; stdout %q", stdout)
	}
	var calls []string
	for _, line := range strings.Split(strings.TrimSpace(string(f.readLog())), "\n") {
		if _, rest, ok := strings.Cut(line, " npm "); ok && !strings.Contains(line, " git ") {
			calls = append(calls, "npm "+rest)
		} else if _, rest, ok := strings.Cut(line, " npx "); ok {
			calls = append(calls, "npx "+rest)
		}
	}
	views, deploys := 0, 0
	firstOther := -1
	for i, c := range calls {
		switch {
		case strings.HasPrefix(c, "npm view "):
			views++
			if firstOther >= 0 {
				t.Fatalf("npm view after other work: %v", calls)
			}
		case strings.HasPrefix(c, "npx wrangler pages deploy"):
			deploys++
			fallthrough
		default:
			if firstOther < 0 {
				firstOther = i
			}
		}
	}
	if views != 2 || deploys != 1 {
		t.Fatalf("calls %v: want 2 npm view polls before anything else and exactly 1 deploy", calls)
	}
	site, err := os.ReadFile(filepath.Join(f.dir, "polls"))
	if err != nil || strings.TrimSpace(string(site)) != "2" {
		t.Fatalf("polls file %q (%v), want 2", site, err)
	}
	if data := f.git(f.site, "show", "release-site/v0.21.0:src/data/site-data.json"); !strings.Contains(data, `"tag":"v0.21.0"`) {
		t.Fatalf("release branch data %q does not come from the one generation", data)
	}
}
