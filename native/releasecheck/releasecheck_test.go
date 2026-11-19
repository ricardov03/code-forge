package releasecheck

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestChangelogFaultsShapes(t *testing.T) {
	cases := []struct {
		name, text string
		want       []string
	}{
		{"good", "## [Unreleased]\n\n### Added\n\n- x\n\n### Fixed\n\n* y\n\n## [1.0.0]\n\n- old\n", nil},
		{"no heading", "# Changelog\n\n## [1.0.0]\n\n- old\n", []string{`CHANGELOG.md has no "## [Unreleased]" heading`}},
		{"no entries", "## [Unreleased]\n\n### Added\n\n## [1.0.0]\n- old\n", []string{`"## [Unreleased]" has no entries: there is nothing to release`}},
		{"entry before heading", "## [Unreleased]\n- loose\n### Added\n- x\n", []string{
			`CHANGELOG.md line 2: an entry of "## [Unreleased]" comes before its first ### heading: put each entry under ### Added, Changed, Deprecated, Removed, Fixed, Security`}},
		{"repeated kind, any case", "## [Unreleased]\n### Fixed\n- a\n### Added\n- b\n### FIXED\n- c\n", []string{
			`CHANGELOG.md line 6: "### FIXED" repeats in "## [Unreleased]" (first at line 2): merge them into one ### FIXED heading`}},
		{"link definitions end the section", "## [Unreleased]\n### Added\n- a\n[Unreleased]: https://x\n- not counted\n", nil},
	}
	for _, c := range cases {
		if got := ChangelogFaults(c.text); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestBumpChoice(t *testing.T) {
	cases := []struct{ text, bump, reason string }{
		{"## [Unreleased]\n### Fixed\n- a\n### Added\n- b\n- c\n## [1.0.0]\n### Added\n- old\n", "minor", "### Added has entries (Fixed 1, Added 2)"},
		{"## [Unreleased]\n### Fixed\n- a\n### Changed\n- b\n## [1.0.0]\n### Added\n- old\n", "patch", "no ### Added entries (Fixed 1, Changed 1)"},
		{"## [Unreleased]\n### Added\n\n### Fixed\n- a\n", "patch", "no ### Added entries (Fixed 1)"},
		{"## [Unreleased]\n### added\n- a\n", "minor", "### Added has entries (added 1)"},
		{"no section\n", "patch", "no ### Added entries (no entries)"},
	}
	for _, c := range cases {
		if b, r := Bump(c.text); b != c.bump || r != c.reason {
			t.Errorf("Bump(%q) = %q %q, want %q %q", c.text, b, r, c.bump, c.reason)
		}
	}
	for _, c := range []struct{ cur, bump, want string }{
		{"0.25.1", "minor", "0.26.0"}, {"0.25.1", "patch", "0.25.2"}, {"1.9.9", "major", "2.0.0"}, {"", "minor", ""}, {"x", "patch", ""},
	} {
		if got := NextVersion(c.cur, c.bump); got != c.want {
			t.Errorf("NextVersion(%q, %q) = %q, want %q", c.cur, c.bump, got, c.want)
		}
	}
}

func TestNpmLsClassify(t *testing.T) {
	opt := map[string]string{"@x/p-darwin-arm64": "1.4.0", "@x/p-linux-x64": "1.4.0", "fsevents": "2.0.0"}
	cases := []struct {
		name, out      string
		ok             bool
		summary, fix   string
		ignored, other []string
	}{
		{"clean", `{"dependencies":{"a":{"version":"1.0.0"}}}`, true, "npm ls: 0 invalid, 0 missing, 0 extraneous", "", nil, nil},
		{"not json", "npm ERR! oops", false, "npm ls --json printed no JSON", "run npm install, then npm ls", nil, nil},
		{"own old and own missing ignored", `{"dependencies":{"@x/p-darwin-arm64":{"version":"1.3.0","invalid":"x"},"@x/p-linux-x64":{"required":"1.4.0","missing":true}},"problems":["invalid: @x/p-darwin-arm64@1.3.0 /p","missing: @x/p-linux-x64@1.4.0, required by @x/p@1.4.0"]}`,
			true, "npm ls: 0 invalid, 0 missing, 0 extraneous (ignored the own platform packages at the previous version: @x/p-darwin-arm64@1.3.0, @x/p-linux-x64@not installed)", "",
			[]string{"@x/p-darwin-arm64@1.3.0", "@x/p-linux-x64@not installed"}, nil},
		{"own newer still fails", `{"dependencies":{"@x/p-darwin-arm64":{"version":"1.5.0","invalid":"x"}}}`, false, "npm ls: 1 invalid: @x/p-darwin-arm64@1.5.0", "npm install", nil, nil},
		{"other optional not own", `{"dependencies":{"fsevents":{"version":"1.0.0","invalid":"x"}}}`, false, "npm ls: 1 invalid: fsevents@1.0.0", "npm install", nil, nil},
		{"own nested not ignored", `{"dependencies":{"a":{"version":"1.0.0","dependencies":{"@x/p-linux-x64":{"missing":true}}}}}`, false, "npm ls: 1 missing: a > @x/p-linux-x64", "npm install", nil, nil},
		{"extraneous only names prune", `{"dependencies":{"z":{"version":"1.0.0","extraneous":true},"a":{"version":"1.0.0","dependencies":{"y":{"version":"2.0.0","extraneous":true}}}},"problems":["extraneous: z@1.0.0 /z","extraneous: y@2.0.0 /y"]}`,
			false, "npm ls: 2 extraneous: a > y@2.0.0, z@1.0.0", "npm prune (removes the extraneous packages)", nil, nil},
		{"unexplained problem", `{"problems":["peer dep missing: react@^18, required by b@1.0.0"]}`, false,
			"npm ls: 1 other: peer dep missing: react@^18, required by b@1.0.0", "npm install", nil, []string{"peer dep missing: react@^18, required by b@1.0.0"}},
	}
	for _, c := range cases {
		r := NpmLs(c.out, "@x/p", opt)
		if r.OK() != c.ok || r.Summary() != c.summary || r.Fix() != c.fix {
			t.Errorf("%s: ok=%v summary=%q fix=%q", c.name, r.OK(), r.Summary(), r.Fix())
		}
		if c.ignored != nil && !reflect.DeepEqual(r.Ignored, c.ignored) {
			t.Errorf("%s: ignored %q", c.name, r.Ignored)
		}
		if c.other != nil && !reflect.DeepEqual(r.Other, c.other) {
			t.Errorf("%s: other %q", c.name, r.Other)
		}
	}
	if got := OwnPlatformPackages("@x/p", opt); strings.Join(got, ",") != "@x/p-darwin-arm64,@x/p-linux-x64" {
		t.Errorf("own = %q", got)
	}
}

// Run with an injected runner: no gh means a failing ci check that names the install.
func TestRunGhMissing(t *testing.T) {
	dir := t.TempDir()
	r := Run(dir, func(_ string, _ time.Duration, argv ...string) Exec {
		switch {
		case argv[0] == "gh":
			return Exec{Code: -1, Err: "gh is not on PATH"}
		case len(argv) > 2 && argv[1] == "rev-parse" && argv[2] == "--is-inside-work-tree":
			return Exec{Stdout: "true\n"}
		case len(argv) > 2 && argv[1] == "rev-parse" && argv[2] == "--abbrev-ref":
			return Exec{Stdout: "main\n"}
		case len(argv) > 1 && argv[1] == "rev-parse":
			return Exec{Stdout: "0123456789abcdef0123456789abcdef01234567\n"}
		case len(argv) > 1 && argv[1] == "ls-remote":
			return Exec{Stdout: "0123456789abcdef0123456789abcdef01234567\trefs/heads/main\n"}
		}
		return Exec{}
	})
	var ci Check
	for _, c := range r.Checks {
		if c.Name == "ci" {
			ci = c
		}
	}
	if r.Ready || ci.Status != Fail || ci.Fix != "install the GitHub CLI (gh) and run gh auth login" {
		t.Fatalf("report: %+v", r)
	}
}
