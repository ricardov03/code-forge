package main

// B690d: `cf-release site-data` is scripts/gen-site-data.mjs in Go, and release-site makes the
// site data without starting node. The cases are test/gen-site-data.test.mjs's.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const b690dChangelog = "# Changelog\n\n## [Unreleased]\n\n### Added\n\n- Future secret.\n\n" +
	"## [1.9.0] — 2026-10-01\n\n### Fixed\n\n- Older *fix*.\n\n" +
	"## [1.10.0] — 2026-11-06\n\n### Added\n\n- A **feature** with [docs](https://example.invalid).\n" +
	"  Continued `Markdown`.\n  - Nested item.\n\n- Second entry.\n\n### Fixed\n\n- A fix.\n\n" +
	"[Unreleased]: https://example.invalid/compare/v1.10.0...HEAD\n"

const b690dCommands = "# Command reference\n\n- [sample](#sample)\n\n## sample\n\nDo **sample** work.\n\n" +
	"```text\nusage: code-forge sample [--flag]\n```\n\n### sample list\n\n```text\nusage: code-forge sample list\n```\n"

type b690dSite struct{ dir, repo, ledger string }

func b690dGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %s: %v", args, out, err)
	}
}

// b690dSiteFixture is a repository tagged v1.10.0 with the changelog and command reference, an
// empty ledger directory and a HOME of its own.
func b690dSiteFixture(t *testing.T) b690dSite {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range fixedEnv {
		t.Setenv(k, v)
	}
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(k, "GIT_") && !strings.HasPrefix(k, "GIT_CONFIG_") && !strings.HasPrefix(k, "GIT_AUTHOR_") && !strings.HasPrefix(k, "GIT_COMMITTER_") {
			t.Setenv(k, "")
			os.Unsetenv(k)
		}
	}
	t.Setenv("HOME", filepath.Join(dir, "home"))
	s := b690dSite{dir: dir, repo: filepath.Join(dir, "repo"), ledger: filepath.Join(dir, "ledger")}
	for _, d := range []string{filepath.Join(s.repo, "docs", "reference"), s.ledger, filepath.Join(dir, "home")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b690dWrite(t, filepath.Join(s.repo, "CHANGELOG.md"), b690dChangelog)
	b690dWrite(t, filepath.Join(s.repo, "docs", "reference", "commands.md"), b690dCommands)
	b690dGit(t, s.repo, "init", "-q")
	b690dGit(t, s.repo, "add", ".")
	b690dGit(t, s.repo, "commit", "-qm", "tagged docs")
	b690dGit(t, s.repo, "tag", "-a", "v1.10.0", "-m", "release")
	return s
}

func b690dWrite(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func b690dGenerate(s b690dSite, args ...string) runResult {
	var out, errb bytes.Buffer
	code := run(newApp(&out, &errb), append([]string{"site-data"}, args...), s.repo)
	return runResult{code, out.String(), errb.String()}
}

// b690dDocument runs site-data for v1.10.0 on the fixture's ledger; it must succeed silently.
func b690dDocument(t *testing.T, s b690dSite, extra ...string) (map[string]any, string) {
	t.Helper()
	r := b690dGenerate(s, append([]string{"--tag", "v1.10.0", "--ledger-dir", s.ledger}, extra...)...)
	if r.code != 0 || r.stderr != "" {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &data); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, r.stdout)
	}
	return data, r.stdout
}

func b690dJSON(t *testing.T, text string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestB690dSiteDataTaggedDocs(t *testing.T) {
	s := b690dSiteFixture(t)
	b690dWrite(t, filepath.Join(s.repo, "CHANGELOG.md"), "# Later working tree changelog\n")
	b690dWrite(t, filepath.Join(s.repo, "docs", "reference", "commands.md"), "# Later working tree commands\n")
	data, _ := b690dDocument(t, s)
	if data["version"] != "1.10.0" || data["date"] != "2026-11-06" {
		t.Errorf("version %v date %v", data["version"], data["date"])
	}
	wantChangelog := b690dJSON(t, `[
		{"version": "1.10.0", "date": "2026-11-06", "sections": [
			{"name": "Added", "entries": ["A **feature** with [docs](https://example.invalid).\n  Continued `+"`Markdown`"+`.\n  - Nested item.", "Second entry."]},
			{"name": "Fixed", "entries": ["A fix."]}]},
		{"version": "1.9.0", "date": "2026-10-01", "sections": [{"name": "Fixed", "entries": ["Older *fix*."]}]}]`)
	if !reflect.DeepEqual(data["changelog"], wantChangelog) {
		t.Errorf("changelog %#v\nwant %#v", data["changelog"], wantChangelog)
	}
	wantCommands := b690dJSON(t, `[{"name": "sample", "description": "Do **sample** work.",
		"usage": "usage: code-forge sample [--flag]", "subcommands": [{"name": "list", "usage": "usage: code-forge sample list"}]}]`)
	if !reflect.DeepEqual(data["commands"], wantCommands) {
		t.Errorf("commands %#v\nwant %#v", data["commands"], wantCommands)
	}
}

func TestB690dSiteDataOlderTag(t *testing.T) {
	s := b690dSiteFixture(t)
	b690dGit(t, s.repo, "tag", "v1.9.0")
	r := b690dGenerate(s, "--tag", "v1.9.0", "--ledger-dir", s.ledger)
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	data := b690dJSON(t, r.stdout).(map[string]any)
	var versions []any
	for _, entry := range data["changelog"].([]any) {
		versions = append(versions, entry.(map[string]any)["version"])
	}
	if data["version"] != "1.9.0" || data["date"] != "2026-10-01" || !reflect.DeepEqual(versions, []any{"1.10.0", "1.9.0"}) {
		t.Errorf("got %v %v %v", data["version"], data["date"], versions)
	}
}

func TestB690dSiteDataBadInputs(t *testing.T) {
	s := b690dSiteFixture(t)
	missing := filepath.Join(s.dir, "missing-ledger")
	file := filepath.Join(s.dir, "not-a-directory")
	b690dWrite(t, file, "not a ledger directory")
	out := filepath.Join(s.dir, "site-data.json")
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown tag", []string{"--tag", "v9.9.9", "--ledger-dir", s.ledger}, "site-data: cannot read CHANGELOG.md at tag v9.9.9 (unknown tag or missing tagged file)\n"},
		{"missing ledger", []string{"--tag", "v1.10.0", "--ledger-dir", missing}, "site-data: cannot read ledger directory " + missing + "\n"},
		{"ledger is a file", []string{"--tag", "v1.10.0", "--ledger-dir", file, "--out", out}, "site-data: cannot read ledger directory " + file + "\n"},
		{"no tag", nil, "site-data: invalid or missing tag \"\"\n" + siteDataUsage + "\n"},
		{"bad tag", []string{"--tag", "1.10.0"}, "site-data: invalid or missing tag \"1.10.0\"\n" + siteDataUsage + "\n"},
		{"no value", []string{"--tag", "--out"}, "site-data: --tag needs a value\n" + siteDataUsage + "\n"},
		{"unknown flag", []string{"--fast"}, "site-data: " + siteDataUsage + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := b690dGenerate(s, tc.args...)
			if r.code != 1 || r.stdout != "" || r.stderr != tc.want {
				t.Errorf("exit %d stdout %q stderr %q, want 1 and %q", r.code, r.stdout, r.stderr, tc.want)
			}
		})
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("a failed run wrote %s: %v", out, err)
	}
}

func TestB690dSiteDataRotatedLedgerProof(t *testing.T) {
	s := b690dSiteFixture(t)
	private := `"slug":"private-slug","path":"/private/customer/path","prompt":"private-prompt","model":"private-model"`
	current := []string{
		`{"event":"run.start","run":"private-run-a",` + private + `}`,
		`{"event":"block.close","run":"private-run-a","block":"private-block","status":"complete"}`,
		`{"event":"block.close","run":"private-run-a","block":"private-block","status":"complete"}`,
		`{"event":"block.close","run":"private-run-b","block":"private-block","status":"complete"}`,
		`{"event":"block.close","run":"private-run-b","block":"failed-block","status":"failed"}`,
		`{"event":"block.close","run":"private-run-b","block":"missing-status"}`,
		`{"event":"block.close","block":"missing-run","status":"complete"}`,
		`{"event":"session","run":"private-run-a","usd":1.2345}`,
		`{"event":"session","run":"private-run-a","usd":null,"usd_unknown":true,"tokens_in":1000000}`,
		`{"event":"session","run":"private-run-b","usd":90,"source":"shadow"}`,
		`{"event":"session.background","run":"private-run-b","tokens_in":1000000}`,
	}
	rotated := []string{
		`{"event":"run.start","run":"private-run-a"}`,
		`{"event":"session","run":"private-run-b","provider":"openai","level":"L1","tokens_in":1000,"tokens_out":1000}`,
		`{"event":"block.close","run":"private-run-a","block":"rotated-block","status":"complete"}`,
		`{"event":"session","cost_usd":2.3456}`,
	}
	b690dWrite(t, filepath.Join(s.ledger, "code-forge.jsonl"), strings.Join(current, "\n")+"\n")
	b690dWrite(t, filepath.Join(s.ledger, "code-forge.1.jsonl"), strings.Join(rotated, "\n")+"\n")
	b690dWrite(t, filepath.Join(s.ledger, "code-forge.12.jsonl"), `{"event":"run.start","run":"private-run-c"}`+"\n")
	for _, name := range []string{"other.jsonl", "code-forge.extra.jsonl", "code-forge.1.jsonl.bak"} {
		b690dWrite(t, filepath.Join(s.ledger, name), `{"event":"session","run":"excluded-run","usd":999}`+"\n")
	}
	data, text := b690dDocument(t, s)
	want := map[string]any{"runs": 3.0, "blocks": 3.0, "spend": 3.59, "date": "2026-11-06"}
	if !reflect.DeepEqual(data["proof"], want) {
		t.Errorf("proof %v, want %v", data["proof"], want)
	}
	for _, value := range []string{"private-slug", "/private/customer/path", "private-prompt", "private-model", "private-run-a",
		"private-run-b", "private-run-c", "private-block", "rotated-block", s.ledger, "openai", "L1", "excluded-run"} {
		if strings.Contains(text, value) {
			t.Errorf("the site data holds private value %q", value)
		}
	}
}

func TestB690dSiteDataBadLedgerRow(t *testing.T) {
	s := b690dSiteFixture(t)
	for _, tc := range []struct{ body, want string }{
		{"{\"event\":\"x\"}\n\n{oops\n", "site-data: invalid JSON in ledger file code-forge.jsonl, line 3\n"},
		{"[1]\n", "site-data: invalid row in ledger file code-forge.jsonl, line 1\n"},
		{"null\n", "site-data: invalid row in ledger file code-forge.jsonl, line 1\n"},
	} {
		b690dWrite(t, filepath.Join(s.ledger, "code-forge.jsonl"), tc.body)
		r := b690dGenerate(s, "--tag", "v1.10.0", "--ledger-dir", s.ledger)
		if r.code != 1 || r.stdout != "" || r.stderr != tc.want {
			t.Errorf("%q: exit %d stdout %q stderr %q, want %q", tc.body, r.code, r.stdout, r.stderr, tc.want)
		}
	}
}

func TestB690dSiteDataStableBytes(t *testing.T) {
	s := b690dSiteFixture(t)
	data, first := b690dDocument(t, s)
	if !reflect.DeepEqual(data["proof"], map[string]any{"runs": 0.0, "blocks": 0.0, "spend": 0.0, "date": "2026-11-06"}) {
		t.Errorf("empty ledger proof %v", data["proof"])
	}
	_, second := b690dDocument(t, s)
	if first != second {
		t.Error("two runs gave different bytes")
	}
	wantStart := "{\n  \"version\": \"1.10.0\",\n  \"date\": \"2026-11-06\",\n  \"changelog\": [\n    {\n"
	wantEnd := "  \"proof\": {\n    \"runs\": 0,\n    \"blocks\": 0,\n    \"spend\": 0,\n    \"date\": \"2026-11-06\"\n  }\n}\n"
	if !strings.HasPrefix(first, wantStart) || !strings.HasSuffix(first, wantEnd) || strings.HasSuffix(first, "\n\n") {
		t.Errorf("not JSON.stringify(data, null, 2) + newline:\n%s", first)
	}
	if !strings.Contains(first, "\n  ],\n  \"commands\": [\n") {
		t.Errorf("key order is not version, date, changelog, commands, proof:\n%s", first)
	}
	out := filepath.Join(s.dir, "site-data.json")
	r := b690dGenerate(s, "--tag", "v1.10.0", "--ledger-dir", s.ledger, "--out", out)
	if r.code != 0 || r.stdout != "" || r.stderr != "" {
		t.Fatalf("--out: exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	if got, err := os.ReadFile(out); err != nil || string(got) != first {
		t.Errorf("--out file differs from stdout: %v", err)
	}
}

// With no --ledger-dir the ledger is $HOME/.code-forge/ledger, as release-site reads it.
func TestB690dSiteDataDefaultLedgerIsHome(t *testing.T) {
	s := b690dSiteFixture(t)
	r := b690dGenerate(s, "--tag", "v1.10.0")
	want := "site-data: cannot read ledger directory " + filepath.Join(os.Getenv("HOME"), ".code-forge", "ledger") + "\n"
	if r.code != 1 || r.stderr != want {
		t.Fatalf("exit %d stderr %q, want %q", r.code, r.stderr, want)
	}
	ledger := filepath.Join(os.Getenv("HOME"), ".code-forge", "ledger")
	if err := os.MkdirAll(ledger, 0o755); err != nil {
		t.Fatal(err)
	}
	b690dWrite(t, filepath.Join(ledger, "code-forge.jsonl"), `{"event":"run.start","run":"r1"}`+"\n")
	r = b690dGenerate(s, "--tag", "v1.10.0")
	if r.code != 0 || !strings.Contains(r.stdout, "\"runs\": 1,") {
		t.Fatalf("exit %d stderr %q stdout %s", r.code, r.stderr, r.stdout)
	}
}

// release-site writes the Go site data into the site and starts no node (it ran
// node scripts/gen-site-data.mjs before B690d).
func TestB690dReleaseSiteStartsNoNode(t *testing.T) {
	f := newFixture(t, fixtureOpts{site: true})
	marker := b580NodeMarker(f)
	r := b580Run(f, nil, 0, "release-site", "--dry-run")
	if r.code != 0 {
		t.Fatalf("exit %d\nstdout %s\nstderr %s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, `built site with {`) || !strings.Contains(r.stdout, `"version": "0.21.0"`) ||
		!strings.Contains(r.stdout, `"name": "sample"`) || !strings.Contains(r.stdout, `"runs": 0`) {
		t.Errorf("the site was not built with the Go site data:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "Would commit src/data/site-data.json") {
		t.Errorf("no dry-run commit line:\n%s", r.stdout)
	}
	// The site build (fake npm) runs; node must not.
	if calls, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(calls)) != "" {
		t.Fatalf("release-site ran node: %q", calls)
	} else if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
