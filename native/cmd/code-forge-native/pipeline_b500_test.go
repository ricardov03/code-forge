package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// b500Env gives a test a temp HOME, no Node (a handover exits 127) and a
// working directory; it returns the real path of that directory.
func b500Env(t *testing.T) (home, work string) {
	t.Helper()
	home, work = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b500-node")
	for _, name := range []string{"CODE_FORGE_NO_ERROR_LOG", "CLAUDECODE", "CLAUDE_CODE", "CURSOR_AGENT", "AI_AGENT"} {
		t.Setenv(name, "")
	}
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(here) })
	real, err := syscall.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return home, real
}

// b500Run runs one command in process; a handover shows as exit 127.
func b500Run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	files := [2]*os.File{}
	for i := range files {
		file, err := os.CreateTemp(dir, "out-")
		if err != nil {
			t.Fatal(err)
		}
		files[i] = file
	}
	out, errOut := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = files[0], files[1]
	code, err := run(args)
	os.Stdout, os.Stderr = out, errOut
	if err != nil {
		code = 127
	}
	texts := [2]string{}
	for i, file := range files {
		_ = file.Close()
		data, _ := os.ReadFile(file.Name())
		texts[i] = string(data)
	}
	return code, texts[0], texts[1]
}

func b500Write(t *testing.T, file, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func b500Read(t *testing.T, file string) string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}

// b500HandedOver reports the reason of the last recorded handover ("" for none).
func b500HandedOver(t *testing.T, home string) string {
	t.Helper()
	text := strings.TrimSpace(b500Read(t, filepath.Join(home, ".code-forge", "native", "handovers.jsonl")))
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	last := lines[len(lines)-1]
	at := strings.Index(last, `"reason":"`)
	if at < 0 {
		return "?"
	}
	rest := last[at+len(`"reason":"`):]
	return rest[:strings.Index(rest, `"`)]
}

const b500Pipeline = `root: .
steps:
  - id: brief
    skill: client-intake
    outcome: out/brief.json
    approval: human
  - id: design
    skill: impeccable
    needs: [brief]
    outcome: out/design.json
    approval: human
  - id: logo
    skill: logo-designer
    needs: [brief]
    outcome: out/logo.json
    approval: none
  - id: build
    skill: website-kit
    needs: [design, logo]
    outcome: out/build.json
    approval: none
  - id: copy
    skill: seo-writer
    outcome: out/copy.json
    approval: none
  - id: legal
    skill: legal-writer
    outcome: out/legal.json
    approval: none
  - id: bad
    skill: x
    outcome: out/bad.json
    approval: none
`

// Expected texts are the Node outputs (CODE_FORGE_NATIVE=0) for the same files.
func TestB500PipelineStatusAndNextInGo(t *testing.T) {
	home, work := b500Env(t)
	b500Write(t, filepath.Join(work, "code-forge.pipeline.yml"), b500Pipeline)
	b500Write(t, filepath.Join(work, "files", "a.txt"), "hello\n")
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte("hello\n")))
	b500Write(t, filepath.Join(work, "out", "brief.json"), `{"skill":"client-intake","status":"approved","files":[{"path":"files/a.txt","sha256":"`+sum+`"}],"checks":[{"name":"c","result":"pass"}]}`)
	b500Write(t, filepath.Join(work, "out", "design.json"), `{"skill":"impeccable","status":"approved","files":[{"path":"files/a.txt","sha256":"00"}]}`)
	b500Write(t, filepath.Join(work, "out", "logo.json"), `{"skill":"logo","status":"draft","files":[]}`)
	b500Write(t, filepath.Join(work, "out", "copy.json"), `{"skill":"x","status":"approved","files":[],"checks":[{"name":"lint","result":"fail"}]}`)
	b500Write(t, filepath.Join(work, "out", "legal.json"), `{not json`)
	b500Write(t, filepath.Join(work, "out", "bad.json"), `{"skill":"x","status":"approved","files":[{"path":"../x","sha256":"1"}]}`)
	legal := filepath.Join(work, "out", "legal.json")
	for _, tc := range []struct {
		args []string
		out  string
	}{
		{[]string{"pipeline", "status"}, "brief  approved  client-intake\n" +
			"design  invalid  impeccable  — file \"files/a.txt\": sha256 does not match\n" +
			"logo  draft  logo-designer\n" +
			"build  waiting  website-kit\n" +
			"copy  invalid  seo-writer  — check \"lint\": result is fail\n" +
			"legal  invalid  legal-writer  — Outcome \"" + legal + "\" is not valid JSON\n" +
			"bad  invalid  x  — file \"../x\": path must be relative to root without ..\n"},
		{[]string{"pipeline", "status", "--json"}, `{"exists":true,"steps":[{"id":"brief","skill":"client-intake","state":"approved"},` +
			`{"id":"design","skill":"impeccable","state":"invalid","problem":"file \"files/a.txt\": sha256 does not match"},` +
			`{"id":"logo","skill":"logo-designer","state":"draft"},{"id":"build","skill":"website-kit","state":"waiting"},` +
			`{"id":"copy","skill":"seo-writer","state":"invalid","problem":"check \"lint\": result is fail"},` +
			`{"id":"legal","skill":"legal-writer","state":"invalid","problem":"Outcome \"` + legal + `\" is not valid JSON"},` +
			`{"id":"bad","skill":"x","state":"invalid","problem":"file \"../x\": path must be relative to root without .."}]}` + "\n"},
		{[]string{"pipeline", "next", "--json"}, `{"exists":true,"ready":[],"waiting":[{"id":"build","needs":["design","logo"]}],"done":false}` + "\n"},
		{[]string{"pipeline", "next"}, "nothing ready\nbuild  waiting for design, logo\n"},
	} {
		code, out, errText := b500Run(t, tc.args...)
		if code != 0 || out != tc.out || errText != "" {
			t.Errorf("%q: got (%d, %q, %q), want (0, %q, \"\")", tc.args, code, out, errText, tc.out)
		}
	}
	if got := b500Read(t, filepath.Join(home, ".code-forge", "logs", "breadcrumbs.json")); got != `["pipeline","pipeline","pipeline","pipeline"]`+"\n" {
		t.Errorf("breadcrumbs = %q", got)
	}
	if reason := b500HandedOver(t, home); reason != "" {
		t.Errorf("handed over: %s", reason)
	}

	// Remove the blockers: design approved, logo approved → build ready; then all approved.
	b500Write(t, filepath.Join(work, "out", "design.json"), `{"skill":"impeccable","status":"approved","files":[]}`)
	b500Write(t, filepath.Join(work, "out", "logo.json"), `{"skill":"logo","status":"approved","files":[]}`)
	if code, out, _ := b500Run(t, "pipeline", "next"); code != 0 || out != "build\n" {
		t.Errorf("next with build ready: got (%d, %q)", code, out)
	}
	for _, name := range []string{"build", "copy", "legal", "bad"} {
		b500Write(t, filepath.Join(work, "out", name+".json"), `{"skill":"s","status":"approved","files":[]}`)
	}
	if code, out, _ := b500Run(t, "pipeline", "next"); code != 0 || out != "all steps approved\n" {
		t.Errorf("next when done: got (%d, %q)", code, out)
	}
	if code, out, _ := b500Run(t, "pipeline", "next", "--json"); code != 0 || out != `{"exists":true,"ready":[],"waiting":[],"done":true}`+"\n" {
		t.Errorf("next --json when done: got (%d, %q)", code, out)
	}
}

func TestB500PipelineHelpUsageAndMissingFile(t *testing.T) {
	home, work := b500Env(t)
	usage := "usage: code-forge pipeline status|next [--json] [--root <dir>]\n"
	for _, tc := range []struct {
		args         []string
		code         int
		out, errText string
	}{
		{[]string{"pipeline", "--help"}, 0, usage, ""},
		{[]string{"pipeline", "-h"}, 0, usage, ""},
		{[]string{"pipeline", "status", "--help"}, 0, "usage: code-forge pipeline status [--json] [--root <dir>]\n", ""},
		{[]string{"pipeline", "next", "-h"}, 0, "usage: code-forge pipeline next [--json] [--root <dir>]\n", ""},
		{[]string{"pipeline"}, 2, "", usage},
		{[]string{"pipeline", "bogus", "--help"}, 2, "", usage},
		{[]string{"pipeline", "status", "--json", "--json"}, 2, "", usage},
		{[]string{"pipeline", "status", "--root"}, 2, "", usage},
		{[]string{"pipeline", "status", "--root", "-x"}, 2, "", usage},
		{[]string{"pipeline", "status", "--root", ""}, 2, "", usage},
		{[]string{"pipeline", "status", "--root", "a", "--root", "b"}, 2, "", usage},
		{[]string{"pipeline", "next", "extra"}, 2, "", usage},
		{[]string{"pipeline", "status"}, 0, "no code-forge.pipeline.yml in " + work + "\n", ""},
		{[]string{"pipeline", "next", "--root", "nowhere/../x/", "--json"}, 0, "{\"exists\":false}\n", ""},
		{[]string{"pipeline", "status", "--root", "sub"}, 0, "no code-forge.pipeline.yml in " + filepath.Join(work, "sub") + "\n", ""},
	} {
		code, out, errText := b500Run(t, tc.args...)
		tc.out = helpExpect(t, tc.args, tc.code, tc.out) // B613 help page
		if code != tc.code || out != tc.out || errText != tc.errText {
			t.Errorf("%q: got (%d, %q, %q), want (%d, %q, %q)", tc.args, code, out, errText, tc.code, tc.out, tc.errText)
		}
	}
	log := b500Read(t, filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	if n := strings.Count(log, `"kind":"usage"`); n != 8 {
		t.Errorf("usage entries = %d, want 8:\n%s", n, log)
	}
	if !strings.Contains(log, `"verb":"pipeline","sub":null,"flags":[],"exit":2,"kind":"usage","message":"usage: code-forge pipeline status|next [--json] [--root <dir>]","stack":null,"cleaned":[],"fp":"`) {
		t.Errorf("usage entry shape:\n%s", log)
	}
	if reason := b500HandedOver(t, home); reason != "" {
		t.Errorf("handed over: %s", reason)
	}
}

func TestB500PipelineProblemsAndReadErrors(t *testing.T) {
	home, work := b500Env(t)
	file := filepath.Join(work, "code-forge.pipeline.yml")
	b500Write(t, filepath.Join(work, ".code-forge.yml"), "version: 1\nproject:\n  slug: acme\n")
	for _, tc := range []struct {
		yaml, errText, message string
	}{
		{"root: .\nsteps:\n  - {id: a, skill: s, outcome: o/a.json, approval: none}\n  - {id: a, skill: s, outcome: o/b.json, approval: none, needs: [zz, \"q\\u00e9\"]}\n",
			"duplicate step id \"a\"\nstep \"a\": unknown need \"zz\"\nstep \"a\": unknown need \"qé\"\n", `"message":"step \"a\": unknown need \"qé\""`},
		{"root: .\nsteps:\n  - {id: a, skill: s, outcome: o/a.json, approval: none, needs: [b]}\n  - {id: b, skill: s, outcome: o/b.json, approval: none, needs: [a]}\n  - {id: c, skill: s, outcome: o/c.json, approval: none, needs: [c]}\n",
			"cycle at step \"a\"\ncycle at step \"c\"\n", `"message":"cycle at step \"c\""`},
		{"root: .\nsteps:\n  - {id: acme, skill: s, outcome: o/a.json, approval: none, needs: [acme-x, acme, \"x acme\"]}\n",
			"step \"acme\": unknown need \"acme-x\"\nstep \"acme\": unknown need \"x acme\"\n", `"message":"step \"<slug>\": unknown need \"x <slug>\"","stack":null,"cleaned":[{"rule":"project_name","count":2}]`},
	} {
		b500Write(t, file, tc.yaml)
		code, out, errText := b500Run(t, "pipeline", "status")
		if code != 1 || out != "" || errText != tc.errText {
			t.Errorf("%q: got (%d, %q, %q), want (1, \"\", %q)", tc.yaml, code, out, errText, tc.errText)
		}
		if log := b500Read(t, filepath.Join(home, ".code-forge", "logs", "errors.jsonl")); !strings.Contains(log, tc.message) {
			t.Errorf("error log lacks %s:\n%s", tc.message, log)
		}
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0o755); err != nil {
		t.Fatal(err)
	}
	if code, _, errText := b500Run(t, "pipeline", "next"); code != 1 || errText != "Cannot read pipeline (EISDIR)\n" {
		t.Errorf("directory pipeline: got (%d, %q)", code, errText)
	}
	if reason := b500HandedOver(t, home); reason != "" {
		t.Errorf("handed over: %s", reason)
	}
}

func TestB500PipelineOutcomeFileProblems(t *testing.T) {
	home, work := b500Env(t)
	outside := t.TempDir()
	b500Write(t, filepath.Join(outside, "s.txt"), "secret\n")
	if err := os.Symlink(outside, filepath.Join(work, "link")); err != nil {
		t.Fatal(err)
	}
	b500Write(t, filepath.Join(work, "code-forge.pipeline.yml"), "root: .\nsteps:\n"+
		"  - {id: a, skill: s, outcome: out/a.json, approval: none}\n"+
		"  - {id: b, skill: s, outcome: out/b.json, approval: none}\n"+
		"  - {id: c, skill: s, outcome: out/c.json, approval: none}\n"+
		"  - {id: d, skill: s, outcome: out/d.json, approval: none}\n"+
		"  - {id: e, skill: s, outcome: out/e.json, approval: none}\n"+
		"  - {id: f, skill: s, outcome: missing/f.json, approval: none, needs: [e]}\n")
	b500Write(t, filepath.Join(work, "out", "a.json"), `{"skill":"x","status":"approved","files":[{"path":"link/s.txt","sha256":"1"}]}`)
	b500Write(t, filepath.Join(work, "out", "b.json"), `{"skill":"x","status":"approved","files":[{"path":"inside/none.txt","sha256":"1"}]}`)
	if err := os.MkdirAll(filepath.Join(work, "out", "c.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	b500Write(t, filepath.Join(work, "out", "d.json"), `{"skill":"x","status":"approved","files":[{"path":"inside","sha256":"1"}]}`)
	b500Write(t, filepath.Join(work, "out", "e.json"), `{"skill":"x","status":"approved","files":[]}`)
	want := "a  invalid  s  — file \"link/s.txt\": path leaves root through a symlink\n" +
		"b  invalid  s  — file \"inside/none.txt\": missing file\n" +
		"c  invalid  s  — Cannot read outcome \"" + filepath.Join(work, "out", "c.json") + "\" (EISDIR)\n" +
		"d  invalid  s  — file \"inside\": cannot read (EISDIR)\n" +
		"e  approved  s\n" +
		"f  ready  s\n"
	if code, out, errText := b500Run(t, "pipeline", "status"); code != 0 || out != want || errText != "" {
		t.Errorf("got (%d, %q, %q), want %q", code, out, errText, want)
	}
	if code, out, _ := b500Run(t, "pipeline", "next"); code != 0 || out != "f\n" {
		t.Errorf("next: got (%d, %q)", code, out)
	}
	if reason := b500HandedOver(t, home); reason != "" {
		t.Errorf("handed over: %s", reason)
	}
}

// Node's YAML and Ajv wording is not ported: those files keep a named handover.
func TestB500PipelineKeepsNamedHandovers(t *testing.T) {
	home, work := b500Env(t)
	file := filepath.Join(work, "code-forge.pipeline.yml")
	for _, tc := range []struct{ yaml, outcome, reason string }{
		{"root: .\nsteps: [\n", "", "pipeline-unsupported"},
		{"root: .\nsteps:\n  - {id: A, skill: s, outcome: o/a.json, approval: none}\n", "", "pipeline-unsupported"},
		{"root: .\nsteps:\n  - {id: a, skill: s, outcome: ../a.json, approval: none}\n", "", "pipeline-unsupported"},
		{"steps:\n  - {id: a, skill: s, outcome: o/a.json, approval: none}\n", "", "pipeline-unsupported"},
		{"root: .\nsteps:\n  - {id: a, skill: s, outcome: o/a.json, approval: none}\n", `{"skill":1,"status":"draft","files":[]}`, "pipeline-unsupported"},
		{"root: .\nsteps:\n  - {id: a, skill: s, outcome: o/a.json, approval: none}\n", `[1]`, "pipeline-unsupported"},
	} {
		b500Write(t, file, tc.yaml)
		if tc.outcome != "" {
			b500Write(t, filepath.Join(work, "o", "a.json"), tc.outcome)
		}
		code, out, _ := b500Run(t, "pipeline", "status")
		if code != 127 || out != "" || b500HandedOver(t, home) != tc.reason {
			t.Errorf("%q %q: got (%d, %q, %q), want a %s handover", tc.yaml, tc.outcome, code, out, b500HandedOver(t, home), tc.reason)
		}
	}
}

// The router's one-line hint: on a terminal, not for agents or usage errors,
// once per fingerprint per 24 hours (src/util/error-hint.mjs).
func TestB500RouterHint(t *testing.T) {
	home, work := b500Env(t)
	b500Write(t, filepath.Join(work, "code-forge.pipeline.yml"), "root: .\nsteps:\n  - {id: a, skill: s, outcome: o/a.json, approval: none, needs: [b]}\n")
	tty := true
	now := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	oldTTY, oldNow := routerLogStderrTTY, routerLogNow
	routerLogStderrTTY = func() bool { return tty }
	routerLogNow = func() time.Time { return now }
	t.Cleanup(func() { routerLogStderrTTY, routerLogNow = oldTTY, oldNow })
	problem := "step \"a\": unknown need \"b\"\n"
	hints := filepath.Join(home, ".code-forge", "logs", "hints.json")
	b500Write(t, hints, `{"0123456789ab":"2026-11-10T09:00:00.000Z","ffffffffffff":"2026-11-11T09:00:00.000Z","bad":"x"}`+"\n")
	if code, _, errText := b500Run(t, "pipeline", "status"); code != 1 || errText != problem+routerHintLine {
		t.Fatalf("first failure: got (%d, %q)", code, errText)
	}
	log := b500Read(t, filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	at := strings.Index(log, `"fp":"`)
	fp := log[at+6 : at+18]
	if got, want := b500Read(t, hints), `{"ffffffffffff":"2026-11-11T09:00:00.000Z","`+fp+`":"2026-11-11T10:00:00.000Z"}`+"\n"; got != want {
		t.Errorf("hints = %q, want %q", got, want)
	}
	now = now.Add(23 * time.Hour)
	if _, _, errText := b500Run(t, "pipeline", "status"); errText != problem {
		t.Errorf("within 24 h: %q", errText)
	}
	now = now.Add(time.Hour)
	if _, _, errText := b500Run(t, "pipeline", "status"); errText != problem+routerHintLine {
		t.Errorf("after 24 h: %q", errText)
	}
	now = now.Add(48 * time.Hour)
	t.Setenv("CLAUDECODE", "1")
	if _, _, errText := b500Run(t, "pipeline", "status"); errText != problem {
		t.Errorf("agent mode: %q", errText)
	}
	t.Setenv("CLAUDECODE", "")
	if _, _, errText := b500Run(t, "pipeline", "bogus"); strings.Contains(errText, routerHintLine) {
		t.Errorf("usage error got a hint: %q", errText)
	}
	tty = false
	if _, _, errText := b500Run(t, "pipeline", "status"); errText != problem {
		t.Errorf("no terminal: %q", errText)
	}
}

// A hint time only Date.parse reads stays with Node before any output; the
// ledger takes the same hint as the small verbs (no ledger-error-hint
// hand-over for a readable state).
func TestB500RouterHintStateAndLedger(t *testing.T) {
	home, work := b500Env(t)
	b500Write(t, filepath.Join(work, "code-forge.pipeline.yml"), "root: .\nsteps:\n  - {id: a, skill: s, outcome: o/a.json, approval: none, needs: [b]}\n")
	oldTTY, oldNow := routerLogStderrTTY, routerLogNow
	routerLogStderrTTY = func() bool { return true }
	routerLogNow = func() time.Time { return time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { routerLogStderrTTY, routerLogNow = oldTTY, oldNow })
	hints := filepath.Join(home, ".code-forge", "logs", "hints.json")
	b500Write(t, hints, `{"0123456789ab":"Nov 11 2026"}`+"\n")
	if code, out, _ := b500Run(t, "pipeline", "status"); code != 127 || out != "" || b500HandedOver(t, home) != "router-hint-state" {
		t.Errorf("odd hint time: got (%d, %q, %s), want a router-hint-state handover", code, out, b500HandedOver(t, home))
	}
	if code, _, _ := b500Run(t, "ledger", "outcome", "--scan-git", "--slug", "s"); code != 127 || b500HandedOver(t, home) != "ledger-error-hint" {
		t.Errorf("ledger odd hint time: got (%d, %s)", code, b500HandedOver(t, home))
	}
	b500Write(t, hints, "not json\n")
	code, _, errText := b500Run(t, "ledger", "outcome", "--scan-git", "--slug", "s")
	if code != 1 || !strings.HasPrefix(errText, "ledger outcome: git rev-parse") || !strings.HasSuffix(errText, routerHintLine) {
		t.Errorf("ledger failure: got (%d, %q), want exit 1 with the hint", code, errText)
	}
	if got := b500Read(t, hints); !strings.HasSuffix(got, `":"2026-11-11T10:00:00.000Z"}`+"\n") || strings.Contains(got, "0123456789ab") {
		t.Errorf("hints after a broken state = %q", got)
	}
}
