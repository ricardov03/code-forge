package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/humanask"
)

// B554: `logs report --no-ai` runs in Go. Expected texts in testdata/b554 are
// Node's (CODE_FORGE_NATIVE=0) for the same fixture, with the home folder
// written as {{HOME}} and the clock at 2026-11-12T16:00:00.000Z.

const lrTestFakeGh = `#!/bin/sh
printf '%s\n' "gh $*" >> "$FAKELOG"
case "$1 $2" in
  "auth status") [ "$GHAUTH" = fail ] && exit 1; exit 0 ;;
  "issue list") [ -n "$GHLISTEXIT" ] && exit "$GHLISTEXIT"; printf '%s\n' "${GHLIST:-[]}"; exit 0 ;;
  "issue comment"|"issue create")
    for a; do case "$prev" in --body-file) printf 'BODYFILE %s %s\n' "$(ls -l "$a" | cut -c1-10)" "$a" >> "$FAKELOG"; cat "$a" >> "$FAKELOG";; esac; prev=$a; done
    if [ "$2" = comment ]; then [ -n "$GHCOMMENTEXIT" ] && exit "$GHCOMMENTEXIT"; exit 0; fi
    case "$*" in *--label*) if [ "$GHLABEL" = fail ]; then echo "could not add label: 'kind:x' not found" >&2; exit 1; fi;; esac
    [ -n "$GHCREATEEXIT" ] && exit "$GHCREATEEXIT"
    echo "Creating issue"; echo "https://github.com/x/y/issues/42"; exit 0 ;;
esac
exit 0
`

const lrTestFakeNpm = `#!/bin/sh
printf '%s\n' "npm $*" >> "$FAKELOG"
if [ -n "$NPMEXIT" ]; then exit "$NPMEXIT"; fi
printf '%s\n' "${NPMOUT:-0.22.1}"
`

// lrTestData is resolved at start, before any test changes directory.
var lrTestData, _ = filepath.Abs(filepath.Join("testdata", "b554"))

type lrTestCase struct {
	data, home, work, log, tmpRoot, pkg string
}

// lrTestEnv makes a temp home with the three-row fixture, a project with a
// slug, fake npm and gh on PATH (none when noFakes) and Node's system facts.
func lrTestEnv(t *testing.T, message string, noFakes bool) lrTestCase {
	t.Helper()
	home, work := b500Env(t)
	home, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	c := lrTestCase{data: lrTestData, home: home, work: work, log: filepath.Join(t.TempDir(), "fake.log"), tmpRoot: t.TempDir(), pkg: filepath.Join(t.TempDir(), "package.json")}
	bin := t.TempDir()
	if !noFakes {
		b500Write(t, filepath.Join(bin, "npm"), lrTestFakeNpm)
		b500Write(t, filepath.Join(bin, "gh"), lrTestFakeGh)
	}
	for _, name := range []string{"npm", "gh"} {
		_ = os.Chmod(filepath.Join(bin, name), 0o755)
	}
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("FAKELOG", c.log)
	if c.tmpRoot, err = filepath.EvalSymlinks(c.tmpRoot); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODE_FORGE_TMP_ROOT", c.tmpRoot)
	for _, name := range []string{"NPMOUT", "NPMEXIT", "GHAUTH", "GHLIST", "GHLISTEXIT", "GHCOMMENTEXIT", "GHCREATEEXIT", "GHLABEL"} {
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}
	b500Write(t, filepath.Join(work, ".code-forge.yml"), "project:\n  slug: myslug\n")
	if message == "" {
		message = "failed at " + home + "/x and " + work + "/src/a.go for bob@example.com in myslug op://vault/item/field abcdefghijklmnopqrstuvwxyz"
	}
	rows := []map[string]any{
		{"ts": "2026-11-09T10:00:00.000Z", "version": "0.22.0", "node": "v24.1.0", "platform": "darwin", "arch": "arm64", "verb": "block", "sub": "close", "flags": []string{"--yes"}, "exit": 1, "kind": "error", "message": "older | pipe\nline", "fp": "0123456789ab"},
		{"ts": "2026-11-09T11:00:00.000Z", "version": "0.22.0", "node": "v24.1.0", "platform": "darwin", "arch": "arm64", "verb": "gates", "sub": nil, "exit": 0, "kind": "warning", "warning": "slow gate", "message": "slow", "fp": "bbbbbbbbbbbb"},
		{"ts": "2026-11-10T10:00:00.000Z", "version": "0.22.0", "node": "v24.1.0", "platform": "darwin", "arch": "arm64", "verb": "block", "sub": "close", "flags": []string{"--yes", "--json"}, "before": []string{"status", "gates run"}, "exit": 1, "kind": "error", "message": message, "stack": "Error: boom ``` x\n    at " + work + "/x.mjs:1", "fp": "0123456789ab", "cleaned": []map[string]any{{"rule": "home", "count": 2}, {"rule": "custom_thing", "count": 1}}},
	}
	var text strings.Builder
	for _, row := range rows {
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		text.Write(data)
		text.WriteByte('\n')
	}
	b500Write(t, c.errorLog(), text.String())
	b500Write(t, c.pkg, `{"name":"@codedology/code-forge","version":"0.22.1","repository":{"type":"git","url":"git+https://github.com/ricardov03/code-forge.git"},"bugs":{"url":"https://github.com/ricardov03/code-forge/issues"}}`)

	oldPkg, oldNow, oldHost, oldFixes, oldNode := lrPackageJSON, lrNow, lrHost, lrFixesEmpty, reviewWaitNodeVersion
	lrPackageJSON = func() string { return c.pkg }
	lrNow = func() time.Time { return time.Date(2026, 11, 12, 16, 0, 0, 0, time.UTC) }
	lrHost = func() (string, string, string) { return "darwin", "25.2.0", "arm64" }
	lrFixesEmpty = func() bool { return true }
	reviewWaitNodeVersion = "v24.22.1"
	t.Cleanup(func() {
		lrPackageJSON, lrNow, lrHost, lrFixesEmpty, reviewWaitNodeVersion = oldPkg, oldNow, oldHost, oldFixes, oldNode
	})
	asked := ""
	b500Terminal(t, false, false, humanask.Cancelled, &asked)
	return c
}

func (c lrTestCase) errorLog() string {
	return filepath.Join(c.home, ".code-forge", "logs", "errors.jsonl")
}

func (c lrTestCase) golden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(c.data, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "{{HOME}}", c.home)
}

func (c lrTestCase) fakeLog(t *testing.T) string {
	t.Helper()
	return b500Read(t, c.log)
}

// rootLeft lists what is left under the temp root (the run root is removed).
func (c lrTestCase) rootLeft(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(c.tmpRoot)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	left := []string{}
	for _, e := range entries {
		sub, _ := os.ReadDir(filepath.Join(c.tmpRoot, e.Name()))
		for _, s := range sub {
			left = append(left, e.Name()+"/"+s.Name())
		}
	}
	return left
}

const lrNpmView = "npm view @codedology/code-forge version\n"
const lrGhList = "gh issue list --repo ricardov03/code-forge --state all --search 0123456789ab in:body --json number,url,state --limit 5\n"

func lrTestCheck(t *testing.T, what string, code int, out, errText string, wantCode int, wantOut, wantErr string) {
	t.Helper()
	if code != wantCode || out != wantOut || errText != wantErr {
		t.Errorf("%s: got exit %d\nstdout:\n%s\nstderr:\n%s\nwant exit %d\nstdout:\n%s\nstderr:\n%s", what, code, out, errText, wantCode, wantOut, wantErr)
	}
}

func TestB554LogsReportDryRunMatchesNode(t *testing.T) {
	c := lrTestEnv(t, "", false)
	before := b500Read(t, c.errorLog())
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--dry-run")
	lrTestCheck(t, "dry run", code, out, errText, 0, c.golden(t, "dry.stdout"), "")
	if got := c.fakeLog(t); got != lrNpmView {
		t.Errorf("dry run children: %q", got)
	}
	for _, private := range []string{c.home, c.work, "bob@example.com", "myslug", "op://vault", "abcdefghijklmnopqrstuvwxyz"} {
		if strings.Contains(out, private) {
			t.Errorf("dry run output keeps %q", private)
		}
	}
	if after := b500Read(t, c.errorLog()); after != before {
		t.Errorf("a dry run changed the error log")
	}
	if b500HandedOver(t, c.home) != "" {
		t.Errorf("dry run handed over: %s", b500HandedOver(t, c.home))
	}
	if left := c.rootLeft(t); len(left) != 0 {
		t.Errorf("run root left behind: %v", left)
	}
}

func TestB554LogsReportNewerOnNpm(t *testing.T) {
	c := lrTestEnv(t, "", false)
	t.Setenv("NPMOUT", "0.23.0")
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--dry-run", "--include-warnings")
	lrTestCheck(t, "newer dry run", code, out, errText, 0, c.golden(t, "newer-dry.stdout"), "")

	upgrade := "You use 0.22.1. 0.23.0 is out. Many errors are fixed in newer versions: upgrade with npm install -g @codedology/code-forge@latest and try again first.\n"
	code, out, errText = b500Run(t, "logs", "report", "--no-ai", "--yes")
	lrTestCheck(t, "newer without --allow-old", code, out, errText, 2, upgrade,
		"logs: an older code-forge than the one on npm; not sent — upgrade first, or pass --allow-old to report from this version\n")
	if strings.Contains(c.fakeLog(t), "gh ") {
		t.Errorf("refused report ran gh: %q", c.fakeLog(t))
	}
	if !strings.Contains(b500Read(t, c.errorLog()), `"verb":"logs","sub":"report"`) {
		t.Errorf("refusal not logged like Node's failure: %s", b500Read(t, c.errorLog()))
	}

	code, out, _ = b500Run(t, "logs", "report", "--no-ai", "--yes", "--allow-old")
	if code != 0 || !strings.HasPrefix(out, upgrade) || !strings.HasSuffix(out, "sent: https://github.com/x/y/issues/42\n") {
		t.Errorf("--allow-old: got %d %q", code, out)
	}
}

func TestB554LogsReportNpmProblemsGoOn(t *testing.T) {
	for _, tc := range []struct{ name, key, value, why string }{
		{"exit", "NPMEXIT", "3", "npm exit 3"},
		{"junk", "NPMOUT", "not a version", "npm gave no version"},
		{"missing", "", "", "npm did not run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := lrTestEnv(t, "", false)
			if tc.key != "" {
				t.Setenv(tc.key, tc.value)
			} else {
				_ = os.Remove(filepath.Join(strings.Split(os.Getenv("PATH"), ":")[0], "npm"))
			}
			code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--dry-run")
			want := "Could not check npm for a newer code-forge (" + tc.why + "); going on.\n" +
				strings.Replace(c.golden(t, "dry.stdout"), "- latest on npm: 0.22.1", "- latest on npm: unknown", 1)
			lrTestCheck(t, tc.name, code, out, errText, 0, want, "")
		})
	}
}

func TestB554LogsReportLinkWithoutGh(t *testing.T) {
	c := lrTestEnv(t, "", true)
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--yes")
	lrTestCheck(t, "gh missing", code, out, errText, 0, c.golden(t, "link.stdout"), "")

	// npm and gh run; gh is not signed in.
	c = lrTestEnv(t, "", false)
	t.Setenv("GHAUTH", "fail")
	code, out, errText = b500Run(t, "logs", "report", "--no-ai", "--yes")
	want := strings.Replace(strings.TrimPrefix(c.golden(t, "link.stdout"), "Could not check npm for a newer code-forge (npm did not run); going on.\n"),
		"- latest on npm: unknown", "- latest on npm: 0.22.1", 1)
	want = strings.Replace(want, "latest%20on%20npm%3A%20unknown", "latest%20on%20npm%3A%200.22.1", 1)
	lrTestCheck(t, "gh not signed in", code, out, errText, 0, want, "")
	if got := c.fakeLog(t); got != lrNpmView+"gh auth status\n" {
		t.Errorf("gh not signed in children: %q", got)
	}
}

func TestB554LogsReportCreatesIssue(t *testing.T) {
	c := lrTestEnv(t, "", false)
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--yes")
	lrTestCheck(t, "create", code, out, errText, 0, c.golden(t, "sent.stdout"), "")
	log := c.fakeLog(t)
	head := lrNpmView + "gh auth status\n" + lrGhList + "gh issue create --repo ricardov03/code-forge --title [error report] block error --body-file "
	if !strings.HasPrefix(log, head) {
		t.Fatalf("create children:\n%s", log)
	}
	if !strings.Contains(log, " --label error-report --label kind:error\nBODYFILE -rw------- "+c.tmpRoot+"/proc-") {
		t.Errorf("labels or private body file under the run root missing:\n%s", log)
	}
	sent := c.golden(t, "sent.stdout")
	body := sent[strings.Index(sent, "## code-forge error report") : strings.LastIndex(sent, "\n\nsent: ")+1]
	if !strings.HasSuffix(log, body) {
		t.Errorf("issue body differs from the shown report:\n%s", log)
	}
	if left := c.rootLeft(t); len(left) != 0 {
		t.Errorf("run root or body file left behind: %v", left)
	}

	// A label gh does not know: sent again once without labels.
	c = lrTestEnv(t, "", false)
	t.Setenv("GHLABEL", "fail")
	code, out, errText = b500Run(t, "logs", "report", "--no-ai", "--yes")
	lrTestCheck(t, "label retry", code, out, errText, 0, c.golden(t, "sent.stdout"), "")
	if n := strings.Count(c.fakeLog(t), "gh issue create "); n != 2 || !strings.Contains(c.fakeLog(t), "--body-file "+c.tmpRoot) {
		t.Errorf("label retry: %d creates\n%s", n, c.fakeLog(t))
	}
}

func TestB554LogsReportCreateFailsGivesLink(t *testing.T) {
	c := lrTestEnv(t, "", false)
	t.Setenv("GHCREATEEXIT", "4")
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--yes")
	lrTestCheck(t, "create fails", code, out, errText, 0, c.golden(t, "createfail.stdout"), "")
	saved := filepath.Join(c.home, ".code-forge", "logs", "report-2026-11-12T16-00-00-000Z.md")
	info, err := os.Stat(saved)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("saved report: %v %v", info, err)
	}
	sent := c.golden(t, "sent.stdout")
	if body := sent[strings.Index(sent, "## code-forge error report") : strings.LastIndex(sent, "\n\nsent: ")+1]; b500Read(t, saved) != body {
		t.Errorf("saved report differs:\n%s", b500Read(t, saved))
	}
}

func TestB554LogsReportSearchProblems(t *testing.T) {
	for _, tc := range []struct{ name, key, value, why string }{
		{"exit", "GHLISTEXIT", "5", "gh exit 5"},
		{"junk", "GHLIST", "{not json", "gh gave no list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := lrTestEnv(t, "", false)
			t.Setenv(tc.key, tc.value)
			code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--yes")
			sent := c.golden(t, "sent.stdout")
			at := strings.LastIndex(sent, "sent: ")
			want := sent[:at] + "could not search for an existing report (" + tc.why + "); creating a new one\n" + sent[at:]
			lrTestCheck(t, tc.name, code, out, errText, 0, want, "")
		})
	}
}

func TestB554LogsReportCommentsOnDuplicate(t *testing.T) {
	c := lrTestEnv(t, "", false)
	t.Setenv("GHLIST", `[{"number":7,"url":"https://github.com/x/y/issues/7","state":"OPEN"}]`)
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--yes")
	lrTestCheck(t, "duplicate", code, out, errText, 0, c.golden(t, "dup.stdout"), "")
	comment := "This error happened again.\n\n- code-forge: 0.22.1\n- latest on npm: 0.22.1\n- Node: v24.22.1\n- OS: darwin 25.2.0 arm64\n- times in the local log: 2\n\n<!-- code-forge-fp: 0123456789ab -->\n"
	log := c.fakeLog(t)
	if !strings.Contains(log, "gh issue comment 7 --repo ricardov03/code-forge --body-file "+c.tmpRoot+"/proc-") || !strings.HasSuffix(log, comment) || strings.Contains(log, "issue create") {
		t.Errorf("duplicate children:\n%s", log)
	}

	c = lrTestEnv(t, "", false)
	t.Setenv("GHLIST", `[{"number":7,"url":"https://github.com/x/y/issues/7","state":"OPEN"}]`)
	t.Setenv("GHCOMMENTEXIT", "3")
	code, out, errText = b500Run(t, "logs", "report", "--no-ai", "--yes")
	wantOut := strings.TrimSuffix(c.golden(t, "dup.stdout"), "commented: https://github.com/x/y/issues/7\n")
	lrTestCheck(t, "comment fails", code, out, errText, 1, wantOut, "gh failed (exit 3); nothing was sent. Add the comment by hand: https://github.com/x/y/issues/7\n")
}

func TestB554LogsReportBlocksSecrets(t *testing.T) {
	c := lrTestEnv(t, "key sk-abcdefghijklmnop here", false)
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--dry-run")
	lrTestCheck(t, "secret", code, out, errText, 1, c.golden(t, "secret.stdout"), c.golden(t, "secret.stderr"))
	code, _, _ = b500Run(t, "logs", "report", "--no-ai", "--yes")
	if code != 1 || strings.Contains(c.fakeLog(t), "gh issue") {
		t.Errorf("secret with --yes: exit %d, children %q", code, c.fakeLog(t))
	}
}

func TestB554LogsReportHugeIsCut(t *testing.T) {
	words := make([]string, 3000)
	for i := range words {
		words[i] = "é€😀w" + itoa(i)
	}
	c := lrTestEnv(t, strings.Join(words, " "), true)
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--yes")
	lrTestCheck(t, "huge", code, out, errText, 0, c.golden(t, "huge.stdout"), "")
	if info, err := os.Stat(filepath.Join(c.home, ".code-forge", "logs", "report-2026-11-12T16-00-00-000Z.md")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("huge saved report: %v %v", info, err)
	}
}

func itoa(i int) string {
	data, _ := json.Marshal(i)
	return string(data)
}

func TestB554LogsReportNoMatchAndNoRepo(t *testing.T) {
	c := lrTestEnv(t, "", false)
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--dry-run", "--verb", "nosuch")
	lrTestCheck(t, "no match", code, out, errText, 0, "no matching errors to report\n", "")
	if c.fakeLog(t) != "" || b500HandedOver(t, c.home) != "" {
		t.Errorf("no match: children %q handover %q", c.fakeLog(t), b500HandedOver(t, c.home))
	}

	c = lrTestEnv(t, "", false)
	b500Write(t, c.pkg, `{"version":"0.22.1"}`)
	code, out, errText = b500Run(t, "logs", "report", "--no-ai", "--dry-run")
	lrTestCheck(t, "no repo", code, out, errText, 1, "", "logs: package.json names no GitHub repository to report to\n")
	if c.fakeLog(t) != "" {
		t.Errorf("no repo ran children: %q", c.fakeLog(t))
	}
}

func TestB554LogsReportHandsOverWhatStaysInNode(t *testing.T) {
	cases := []struct {
		name, reason string
		args         []string
		setup        func(t *testing.T, c lrTestCase)
	}{
		{"ai cleaning in a terminal send", "logs-report", []string{"--yes", "--allow-old"}, func(t *testing.T, c lrTestCase) {
			asked := ""
			b500Terminal(t, true, true, humanask.Cancelled, &asked)
		}},
		{"doctor", "logs-report-doctor", []string{"--no-ai", "--with-doctor", "--dry-run"}, nil},
		{"known fixes", "logs-report-known-fixes", []string{"--no-ai", "--dry-run"}, func(t *testing.T, c lrTestCase) { lrFixesEmpty = func() bool { return false } }},
		{"os release unknown", "logs-report-system", []string{"--no-ai", "--dry-run"}, func(t *testing.T, c lrTestCase) {
			lrHost = func() (string, string, string) { return "darwin", "", "arm64" }
		}},
		{"terminal prompt", "logs-report-prompt", []string{"--no-ai", "--yes"}, func(t *testing.T, c lrTestCase) {
			asked := ""
			b500Terminal(t, true, true, humanask.Cancelled, &asked)
		}},
		{"entry without fingerprint", "logs-format", []string{"--no-ai", "--dry-run"}, func(t *testing.T, c lrTestCase) {
			text := b500Read(t, c.errorLog())
			b500Write(t, c.errorLog(), strings.Replace(text, `"fp":"bbbbbbbbbbbb"`, `"fp":7`, 1))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := lrTestEnv(t, "", false)
			if tc.setup != nil {
				tc.setup(t, c)
			}
			before := b500Read(t, c.errorLog())
			// errText is only the launcher's note that no Node runs in tests.
			code, out, _ := b500Run(t, append([]string{"logs", "report"}, tc.args...)...)
			if code != 127 || out != "" || b500HandedOver(t, c.home) != tc.reason {
				t.Errorf("got (%d, %q) reason %q, want handover %q", code, out, b500HandedOver(t, c.home), tc.reason)
			}
			if c.fakeLog(t) != "" || b500Read(t, c.errorLog()) != before {
				t.Errorf("side effects before the handover: children %q", c.fakeLog(t))
			}
		})
	}

	// A terminal with --dry-run or with --yes --allow-old asks nothing: Go runs it.
	c := lrTestEnv(t, "", false)
	asked := ""
	b500Terminal(t, true, true, humanask.Cancelled, &asked)
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--dry-run")
	lrTestCheck(t, "terminal dry run", code, out, errText, 0, c.golden(t, "dry.stdout"), "")
}

// Node's compareVersions, findSecret and encodeURIComponent on the same input.
func TestB554LogsReportHelpersMatchNode(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want float64
		ok   bool
	}{
		{"1.2.3", "1.2.3", 0, true}, {"1.2.4", "1.2.3", 1, true}, {"1.2.3-rc.1", "1.2.3", -1, true},
		{"1.2.3-rc.2", "1.2.3-rc.10", -8, true}, {"1.2.3-alpha", "1.2.3-1", 1, true}, {"1.2.3-a.b", "1.2.3-a", 1, true},
		{"v2.0.0", "1.9.9", 1, true}, {"x", "1.0.0", 0, false}, {"1.0.0+b", "1.0.0", 0, true}, {"1.10.0", "1.9.0", 1, true},
	} {
		if got, ok := lrCompareVersions(tc.a, tc.b); ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("compareVersions(%q, %q) = %v %v, want %v %v", tc.a, tc.b, got, ok, tc.want, tc.ok)
		}
	}
	for _, tc := range []struct {
		text, rule string
		line       int
	}{
		{"nothing here", "", 0},
		{"x\nkey sk-abcdefghijk", "API key (sk-)", 2},
		{"xsk-abcdefghijklmno", "", 0},
		{"ghp_abcdefghijklmnopqrstuvwxyz", "GitHub token", 1},
		{"AKIAABCDEFGHIJKLMNOPQ", "secret-shaped token", 1},
		{"AKIAABCDEFGHIJKLMNOP", "AWS access key", 1},
		{"-----BEGIN RSA PRIVATE KEY-----", "private key", 1},
		{"xai-abcdefghijklmnopqrstu", "secret-shaped token", 1},
		{"AIzaabcdefghijklmnopqrstuvwx", "secret-shaped token", 1},
		{"0123456789abcdef0123456789abcdef0123", "", 0},
		{"Zx9QpL2mN8vR4tY7wK1jH5gF3dS6aB0cXeUiOo", "high-entropy string", 1},
		{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", 0},
		{"gho_abcdefghijklmnopqrstuvwx", "GitHub token", 1},
	} {
		rule, line, hit := lrFindSecret(tc.text)
		if rule != tc.rule || line != tc.line || hit != (tc.rule != "") {
			t.Errorf("findSecret(%q) = %q %d %v, want %q %d", tc.text, rule, line, hit, tc.rule, tc.line)
		}
	}
	sc := lrNewScrubber("/Users/bob/", "/Users/bob/proj", "acme")
	for _, tc := range [][2]string{
		{"/Users/bob/proj/a and /Users/bobby/x and /Users/bob", "<project>/a and /Users/bobby/x and ~"},
		{"acme acme-x xacme <acme> acme://h op://v/i/f mail a.b@c.io", "<slug> acme-x xacme <acme> acme://h op://<ref> mail <email>"},
		{"id abcdefghijklmnopqrstuvwxyz and abcdefghijklmnopqrstuvwxyz0", "id <item-id> and abcdefghijklmnopqrstuvwxyz0"},
	} {
		if got := sc.scrub(tc[0]); got != tc[1] {
			t.Errorf("scrub(%q) = %q, want %q", tc[0], got, tc[1])
		}
	}
	merged := lrMergeCounts([]any{sc.counts(), []any{map[string]any{"rule": "odd_rule", "count": 1.0}, map[string]any{"rule": "home", "count": 1.5}, map[string]any{"rule": "email", "count": 1.0}}})
	if got := lrDescribeCounts(merged); got != "Cleaned: 1 home path, 1 project path, 1 project name, 2 emails, 1 1Password reference, 1 1Password item ID, 1 odd rule." {
		t.Errorf("describeCounts = %q", got)
	}
	if got := lrEncode("a b/é?&=#~!*'()😀"); got != "a%20b%2F%C3%A9%3F%26%3D%23~!*'()%F0%9F%98%80" {
		t.Errorf("encodeURIComponent = %q", got)
	}
}
