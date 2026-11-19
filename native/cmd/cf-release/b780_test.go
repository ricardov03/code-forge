package main

// B780: release:site builds the site once and checks that the built site names the release; it
// no longer runs the site's own gates (gates.test, gates.custom). All runs use the throwaway
// fixture site with fake npm and npx; nothing is deployed.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// b780RedGates gives the fixture site gates that would all fail if anything ran them.
func b780RedGates(f *fixture) {
	f.write(filepath.Join(f.site, ".code-forge.yml"), "gates:\n  test: [npm, test]\n  custom:\n"+
		"    - name: fidelity\n      argv: [sh, -c, \"echo fidelity ran >> \\\"$FAKE_LOG\\\"; exit 3\"]\n  timeout_s: 1\n")
	f.git(f.site, "commit", "-q", "-am", "red gates")
}

func b780Calls(f *fixture, what string) int {
	return strings.Count(string(f.readLog()), what)
}

// A site whose tests and design checks would fail still releases when the build and the smoke
// check pass: one build, no gate run, one commit, one deploy, and the skipped line is printed.
func TestB780RedSiteGatesStillRelease(t *testing.T) {
	f := newFixture(t, fixtureOpts{site: true})
	b780RedGates(f)
	r := b580Run(f, map[string]string{"FAKE_NPM_TEST_EXIT": "1"}, 0, "release-site")
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
	if n := b780Calls(f, " npm run build:site"); n != 1 {
		t.Errorf("build:site ran %d times, want 1", n)
	}
	if b780Calls(f, " npm test") != 0 || b780Calls(f, "fidelity ran") != 0 {
		t.Errorf("a site gate ran:\n%s", f.readLog())
	}
	if n := b780Calls(f, " npx wrangler pages deploy docs-site/site/.pages-out "); n != 1 {
		t.Errorf("deploys %d, want 1:\n%s", n, f.readLog())
	}
	for _, line := range []string{
		"site tests and design checks run during development (site gates), not at release\n",
		"Smoke check: docs-site/site/.pages-out/llms-full.txt names @example/pkg@0.21.0\n",
	} {
		if strings.Count(r.stdout, line) != 1 {
			t.Errorf("want one %q in stdout:\n%s", line, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "Running gate") {
		t.Errorf("stdout still names gate runs:\n%s", r.stdout)
	}
	if subject := strings.TrimSpace(f.git(f.site, "log", "-1", "--format=%s", "release-site/v0.21.0")); subject != "Release site v0.21.0" {
		t.Fatalf("release commit subject %q", subject)
	}
}

// A built site that does not name <package>@<version> stops before the commit and the deploy;
// a near miss (@example/pkg@0.21.01) does not count. Dry run and real run both refuse.
func TestB780BuildWithoutVersionFailsBeforeCommit(t *testing.T) {
	for _, args := range [][]string{{"release-site"}, {"release-site", "--dry-run"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			f := newFixture(t, fixtureOpts{site: true})
			r := b580Run(f, map[string]string{"FAKE_BUILD_NOVERSION": "1"}, 0, args...)
			want := "release:site: Smoke check failed: no .html, .md or .txt file in deploy folder docs-site/site/.pages-out names @example/pkg@0.21.0 after the build (is the docs pin on the release tag?); no commit or deploy; the old site remains live\n"
			if r.code != 1 || !strings.HasSuffix(r.stderr, want) {
				t.Fatalf("exit %d, want 1\nstderr %q\nwant suffix %q", r.code, r.stderr, want)
			}
			if strings.Contains(r.stdout, "Would commit") || strings.Contains(r.stdout, "Owner next steps") {
				t.Errorf("went on after the failed smoke check:\n%s", r.stdout)
			}
			if refs := f.git(f.site, "for-each-ref", "refs/heads/release-site"); refs != "" {
				t.Fatalf("a release branch was made: %q", refs)
			}
			if b780Calls(f, "wrangler") != 0 {
				t.Fatalf("deployed:\n%s", f.readLog())
			}
			if _, err := os.Stat(filepath.Join(f.site, ".code-forge-tmp")); !os.IsNotExist(err) {
				t.Fatalf("temporary worktree folder left behind (%v)", err)
			}
		})
	}
}

// A site with no .code-forge.yml releases: release:site reads no site gate config.
func TestB780SiteWithoutConfigReleases(t *testing.T) {
	f := newFixture(t, fixtureOpts{site: true})
	if err := os.Remove(filepath.Join(f.site, ".code-forge.yml")); err != nil {
		t.Fatal(err)
	}
	f.git(f.site, "commit", "-q", "-am", "no config")
	r := b580Run(f, nil, 0, "release-site", "--dry-run")
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "Would deploy: npx wrangler pages deploy docs-site/site/.pages-out") {
		t.Errorf("no dry-run deploy line:\n%s", r.stdout)
	}
}

// A failed build stops with one line naming the build and its exit code.
func TestB780BuildFailureNamesBuild(t *testing.T) {
	f := newFixture(t, fixtureOpts{site: true})
	r := b580Run(f, map[string]string{"FAKE_BUILD_EXIT": "3"}, 0, "release-site")
	want := "release:site: Site build failed: npm run build:site (exit 3); no commit or deploy; the old site remains live\n"
	if r.code != 1 || !strings.HasSuffix(r.stderr, want) {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	if b780Calls(f, "wrangler") != 0 || f.git(f.site, "for-each-ref", "refs/heads/release-site") != "" {
		t.Fatalf("committed or deployed after a failed build")
	}
}
