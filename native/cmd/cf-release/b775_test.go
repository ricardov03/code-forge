package main

// B775: release-site moves the site docs pin (docs-site/site/docs-source.json "commit") to the
// release tag and commits it with site-data.json. All runs use the throwaway fixture site.

import (
	"strings"
	"testing"
)

const b775Pin = "{\n  \"commit\": \"v0.20.0\",\n  \"pages\": [\n    \"step-by-step\",\n    \"reference/commands\"\n  ],\n  \"note\": \"kept\"\n}\n"

// b775SitePin adds the pin file to the fixture site's main branch.
func b775SitePin(f *fixture, body string) {
	f.write(f.site+"/docs-site/site/docs-source.json", body)
	f.git(f.site, "add", "-A")
	f.git(f.site, "commit", "-q", "-m", "site: docs pin")
}

// The pin moves to the tag, every other key and the layout stay, the build sees the new pin,
// and the release commit holds both files.
func TestB775ReleaseSiteMovesDocsPin(t *testing.T) {
	f := newFixture(t, fixtureOpts{site: true})
	b775SitePin(f, b775Pin)
	r := b580Run(f, nil, 0, "release-site")
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
	want := strings.Replace(b775Pin, `"v0.20.0"`, `"v0.21.0"`, 1)
	if got := f.git(f.site, "show", "release-site/v0.21.0:docs-site/site/docs-source.json"); got != want {
		t.Fatalf("pin on the release branch:\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(r.stdout, "Docs pin docs-site/site/docs-source.json: v0.20.0 -> v0.21.0\n") {
		t.Errorf("no pin line in stdout:\n%s", r.stdout)
	}
	files := strings.Fields(f.git(f.site, "show", "--name-only", "--format=", "release-site/v0.21.0"))
	if strings.Join(files, " ") != "docs-site/site/docs-source.json src/data/site-data.json" {
		t.Fatalf("release commit files %v, want the pin and the site data", files)
	}
	if subject := strings.TrimSpace(f.git(f.site, "log", "-1", "--format=%s", "release-site/v0.21.0")); subject != "Release site v0.21.0" {
		t.Fatalf("release commit subject %q", subject)
	}
	if parent := strings.TrimSpace(f.git(f.site, "rev-parse", "release-site/v0.21.0^")); parent != strings.TrimSpace(f.git(f.site, "rev-parse", "main")) {
		t.Fatalf("release commit is not one commit on main")
	}
	if status := f.git(f.site, "status", "--porcelain=v1", "-uall"); status != "" {
		t.Fatalf("site tree not clean: %q", status)
	}
}

// --dry-run writes the pin into the temporary tree (the gates build with it) and names both files.
func TestB775ReleaseSiteDryRunNamesPin(t *testing.T) {
	f := newFixture(t, fixtureOpts{site: true})
	b775SitePin(f, b775Pin)
	r := b580Run(f, nil, 0, "release-site", "--dry-run")
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "Would commit src/data/site-data.json and docs-site/site/docs-source.json on release-site/v0.21.0\n") {
		t.Errorf("dry-run commit line does not name the pin:\n%s", r.stdout)
	}
	if got := f.git(f.site, "show", "main:docs-site/site/docs-source.json"); got != b775Pin {
		t.Fatalf("dry run changed main's pin: %q", got)
	}
}

// A pin already at the tag is rewritten unchanged and still committed cleanly (site data only changes).
func TestB775ReleaseSitePinAlreadyAtTag(t *testing.T) {
	f := newFixture(t, fixtureOpts{site: true})
	at := strings.Replace(b775Pin, `"v0.20.0"`, `"v0.21.0"`, 1)
	b775SitePin(f, at)
	r := b580Run(f, nil, 0, "release-site")
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
	if got := f.git(f.site, "show", "release-site/v0.21.0:docs-site/site/docs-source.json"); got != at {
		t.Fatalf("pin changed: %q", got)
	}
	if !strings.Contains(r.stdout, "Docs pin docs-site/site/docs-source.json: v0.21.0 -> v0.21.0\n") {
		t.Errorf("no pin line:\n%s", r.stdout)
	}
}

// No pin file: the old behaviour (exit 0, only site-data.json committed) plus one not-found line.
func TestB775ReleaseSiteMissingPinKeepsOldBehaviour(t *testing.T) {
	f := newFixture(t, fixtureOpts{site: true})
	r := b580Run(f, nil, 0, "release-site")
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
	if strings.Count(r.stdout, "Docs pin docs-site/site/docs-source.json not found; the site docs pin was not moved\n") != 1 {
		t.Errorf("want one not-found line:\n%s", r.stdout)
	}
	if r.stderr != "" {
		t.Errorf("stderr %q", r.stderr)
	}
	files := strings.Fields(f.git(f.site, "show", "--name-only", "--format=", "release-site/v0.21.0"))
	if strings.Join(files, " ") != "src/data/site-data.json" {
		t.Fatalf("release commit files %v, want only the site data", files)
	}
}

// A pin that is not a JSON object stops the release before any commit or deploy.
func TestB775ReleaseSiteBadPinRefused(t *testing.T) {
	f := newFixture(t, fixtureOpts{site: true})
	b775SitePin(f, "[1]\n")
	r := b580Run(f, nil, 0, "release-site")
	if r.code != 1 || !strings.Contains(r.stderr, "release:site: docs-site/site/docs-source.json must be a JSON object") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	if refs := f.git(f.site, "for-each-ref", "refs/heads/release-site"); refs != "" {
		t.Fatalf("a release branch was made: %q", refs)
	}
	if strings.Contains(string(f.readLog()), "wrangler") {
		t.Fatalf("deployed with a bad pin")
	}
}
