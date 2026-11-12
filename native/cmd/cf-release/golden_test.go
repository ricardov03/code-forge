package main

// Byte-for-byte parity with the Node scripts (B570): each case runs the Go port in a fresh fixture
// and compares exit code, stdout, stderr and the state left behind with a golden recorded from the
// Node script on the same fixture. Record the goldens with a checkout that still has the Node
// scripts (B570, 5c2510a7; B574 deleted them):
//
//	CF_RELEASE_NODE_GOLDENS=<that checkout's root> go test ./cmd/cf-release -run TestNodeParity -count=1
//
// Masks: the fixture folder is <CASE>, the release temp folder's random suffix is <RAND>, and the
// npm test log's time and pid are <MS> and <PID>.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type parityCase struct {
	name  string
	opts  fixtureOpts
	prep  func(f *fixture)
	env   map[string]string
	args  []string // tool and its arguments
	cwdIn string   // run from this folder below the fixture (default repo)
}

var nodeScript = map[string]string{
	"release": "release.mjs", "release-site": "release-site.mjs", "docs-block": "docs-block.mjs", "changelog": "changelog.mjs",
}

const date = "2026-11-12"

func dirty(f *fixture) { f.write(filepath.Join(f.repo, "scratch.txt"), "not committed\n") }

var parityCases = []parityCase{
	{name: "release-patch-dry", args: []string{"release", "patch", "--dry-run", "--date", date}},
	{name: "release-minor-dry-e404", env: map[string]string{"FAKE_NPM_VIEW": "E404"},
		args: []string{"release", "minor", "--dry-run", "--date", date}},
	{name: "release-patch-real", args: []string{"release", "patch", "--date", date}},
	{name: "release-minor-real-native-no-checks", opts: fixtureOpts{native: true},
		args: []string{"release", "minor", "--no-checks", "--date", date}},
	{name: "release-explicit-real-no-claude", opts: fixtureOpts{noClaud: true},
		args: []string{"release", "v1.0.0", "--date", date}},
	{name: "release-dirty-dry", prep: dirty, args: []string{"release", "patch", "--dry-run", "--date", date}},
	{name: "release-dirty-real", prep: dirty, args: []string{"release", "patch", "--date", date}},
	{name: "release-ci-green", env: map[string]string{"FAKE_NPM_TEST_EXIT": "1", "FAKE_GH": "green"},
		args: []string{"release", "patch", "--date", date}},
	{name: "release-ci-red", env: map[string]string{"FAKE_NPM_TEST_EXIT": "1", "FAKE_GH": "red"},
		args: []string{"release", "patch", "--date", date}},
	{name: "release-ci-no-runs", env: map[string]string{"FAKE_NPM_TEST_EXIT": "1"},
		args: []string{"release", "patch", "--date", date}},
	{name: "release-alone-fails", env: map[string]string{"FAKE_NPM_TEST_EXIT": "1", "FAKE_NPM_ALONE_EXIT": "1", "FAKE_GH": "green"},
		args: []string{"release", "patch", "--date", date}},
	{name: "release-doc-check-fails", env: map[string]string{"FAKE_DOC_FAIL": "1"},
		args: []string{"release", "patch", "--date", date}},
	{name: "release-typecheck-fails", env: map[string]string{"FAKE_NPM_TYPECHECK_EXIT": "2"},
		args: []string{"release", "patch", "--date", date}},
	{name: "release-npm-newer", env: map[string]string{"FAKE_NPM_VIEW": "0.22.0"},
		args: []string{"release", "patch", "--date", date}},
	{name: "release-wrong-branch-dry", prep: func(f *fixture) { f.git(f.repo, "switch", "-q", "-c", "topic") },
		args: []string{"release", "patch", "--dry-run", "--date", date}},
	{name: "release-ahead-allow-branch", prep: func(f *fixture) {
		f.git(f.repo, "switch", "-q", "-c", "topic")
		f.git(f.repo, "push", "-q", "origin", "topic")
		f.git(f.repo, "commit", "-q", "--allow-empty", "-m", "docs: more")
	}, args: []string{"release", "patch", "--allow-branch", "topic", "--dry-run", "--date", date}},
	{name: "release-stale-stamp", prep: func(f *fixture) {
		f.write(filepath.Join(f.repo, "src/x.js"), "export {};\n")
		f.git(f.repo, "add", "-A")
		f.git(f.repo, "commit", "-q", "-m", "feat: x")
		f.git(f.repo, "push", "-q", "origin", "main")
	}, args: []string{"release", "patch", "--date", date}},
	{name: "release-empty-unreleased", prep: func(f *fixture) {
		f.write(filepath.Join(f.repo, "CHANGELOG.md"), "# Changelog\n\n## [Unreleased]\n\n## [0.21.0] — 2026-11-01\n\n- First.\n")
		f.git(f.repo, "commit", "-q", "-am", "docs: changelog")
		f.git(f.repo, "push", "-q", "origin", "main")
	}, args: []string{"release", "patch", "--date", date}},
	{name: "release-noncanonical-json", prep: func(f *fixture) {
		f.write(filepath.Join(f.repo, ".claude-plugin/plugin.json"), "{ \"name\": \"pkg\", \"version\": \"0.21.0\" }\n")
		f.git(f.repo, "commit", "-q", "-am", "docs: plugin")
		f.git(f.repo, "push", "-q", "origin", "main")
	}, args: []string{"release", "patch", "--no-checks", "--date", date}},
	{name: "release-usage-missing", args: []string{"release"}},
	{name: "release-usage-date", args: []string{"release", "patch", "--date", "12-11-2026"}},
	{name: "release-usage-bump", args: []string{"release", "huge"}},
	{name: "release-notes", args: []string{"release", "notes", "v0.21.0"}},
	{name: "release-notes-out", args: []string{"release", "notes", "v0.21.0", "--out", "notes.md"}},
	{name: "release-notes-missing", args: []string{"release", "notes", "v9.9.9"}},
	{name: "release-notes-usage", args: []string{"release", "notes"}},

	{name: "site-dry", opts: fixtureOpts{site: true}, args: []string{"release-site", "--dry-run"}},
	{name: "site-real", opts: fixtureOpts{site: true}, args: []string{"release-site"}},
	{name: "site-gate-fails", opts: fixtureOpts{site: true}, env: map[string]string{"FAKE_BUILD_EXIT": "3"},
		args: []string{"release-site"}},
	{name: "site-deploy-dir-missing", opts: fixtureOpts{site: true}, args: []string{"release-site", "--deploy-dir", "out/site"}},
	{name: "site-branch-exists", opts: fixtureOpts{site: true},
		prep: func(f *fixture) { f.git(f.site, "branch", "release-site/v0.21.0") }, args: []string{"release-site"}},
	{name: "site-missing", args: []string{"release-site", "--site", "../nowhere"}},
	{name: "site-no-tag", opts: fixtureOpts{site: true}, prep: func(f *fixture) { f.git(f.repo, "tag", "-d", "v0.21.0") },
		args: []string{"release-site"}},
	{name: "site-usage", args: []string{"release-site", "--wait-min", "0"}},
	{name: "site-unknown-option", args: []string{"release-site", "--fast"}},
	{name: "site-deploy-dir-escape", args: []string{"release-site", "--deploy-dir", "../out"}},

	{name: "docs-block-brief", opts: fixtureOpts{site: true}, args: []string{"docs-block", "brief"}},
	{name: "docs-block-brief-no-site", args: []string{"docs-block", "brief"}},
	{name: "docs-block-stamp", prep: func(f *fixture) {
		f.write(filepath.Join(f.repo, "src/x.js"), "export {};\n")
		f.git(f.repo, "add", "-A")
		f.git(f.repo, "commit", "-q", "-m", "feat: x")
	}, args: []string{"docs-block", "stamp"}},
	{name: "docs-block-stamp-dirty", prep: dirty, args: []string{"docs-block", "stamp"}},
	{name: "docs-block-usage", args: []string{"docs-block"}},

	{name: "changelog-add", args: []string{"changelog", "changed", "The release tool runs in Go now, with the same output, exit codes, file edits and git commands as before"}},
	{name: "changelog-add-new-type", args: []string{"changelog", "security", "Pinned a dependency."}},
	{name: "changelog-add-duplicate", args: []string{"changelog", "added", "A new verb."}},
	{name: "changelog-add-unknown", args: []string{"changelog", "tweaked", "x"}},
	{name: "changelog-add-empty", args: []string{"changelog", "added", "   "}},
	{name: "changelog-list", args: []string{"changelog", "list"}},
	{name: "changelog-usage", args: []string{"changelog"}},
}

type runResult struct {
	code           int
	stdout, stderr string
}

func (f *fixture) runNode(repoRoot string, c parityCase) runResult {
	script := filepath.Join(repoRoot, "scripts", nodeScript[c.args[0]])
	node, err := exec.LookPath("node")
	if err != nil {
		f.t.Fatal("CF_RELEASE_NODE_GOLDENS needs node on PATH")
	}
	cmd := exec.Command(node, append([]string{script}, c.args[1:]...)...)
	cmd.Dir = f.repo
	cmd.Env = f.environ(c.env)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err = cmd.Run()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			f.t.Fatal(err)
		}
		code = exit.ExitCode()
	}
	return runResult{code, out.String(), errb.String()}
}

func (f *fixture) runGo(c parityCase) runResult {
	f.use(c.env)
	var out, errb bytes.Buffer
	a := newApp(&out, &errb)
	code := run(a, c.args, f.repo)
	return runResult{code, out.String(), errb.String()}
}

func (f *fixture) report(r runResult) string {
	return "exit " + strconv.Itoa(r.code) + "\n--- stdout\n" + f.mask(r.stdout) + "--- stderr\n" + f.mask(r.stderr) + f.state()
}

func TestNodeParity(t *testing.T) {
	nodeRoot := os.Getenv("CF_RELEASE_NODE_GOLDENS")
	for _, c := range parityCases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, c.opts)
			if c.prep != nil {
				c.prep(f)
			}
			golden := filepath.Join("testdata", "golden", c.name+".txt")
			if nodeRoot != "" {
				got := f.report(f.runNode(nodeRoot, c))
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden %s (record it from Node): %v", golden, err)
			}
			got := f.report(f.runGo(c))
			if got != string(want) {
				t.Errorf("Go output differs from the Node golden %s\n%s", golden, firstDiff(string(want), got))
			}
		})
	}
}

// firstDiff shows the first differing line with a little context.
func firstDiff(want, got string) string {
	w := strings.Split(want, "\n")
	g := strings.Split(got, "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl || i >= len(w) || i >= len(g) {
			from := max(0, i-3)
			ctx := strings.Join(w[from:min(i, len(w))], "\n")
			return "line " + strconv.Itoa(i+1) + ":\n" + ctx + "\n- want: " + strconv.Quote(wl) + "\n+ got:  " + strconv.Quote(gl)
		}
	}
	return "(equal)"
}
