package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/humanask"
)

// B677: `logs report` asks its terminal questions and runs --with-doctor in
// Go. testdata/b677/node.jsonl is Node's runLogs (src/cli/logs.mjs) on the
// lrTestEnv fixture with these fake doctor and editor scripts, a scripted
// clack ui (each question asked, its first option index and the answer) and
// the home folder written as {{HOME}}, the temp root as {{TMP}}.

const lrTestFakeDoctor = `#!/bin/sh
printf 'doctor %s nolog=%s\n' "$*" "$CODE_FORGE_NO_ERROR_LOG" >> "$FAKELOG"
case "$DOCTOR_MODE" in
rows)
  long=$(printf 'é%.0s' 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 19 20 21 22 23 24 25 26 27 28 29 30 31 32 33 34 35 36 37 38 39 40 41 42 43 44 45 46 47 48 49 50 51 52 53 54 55 56 57 58 59 60 61 62 63 64 65 66 67 68 69 70)
  printf '{"ok":false,"rows":[{"id":"node","status":"OK","label":"Node.js","detail":"\\n   \\n  v24 at %s/bin | x  \\nsecond line"},{"id":"bad id","status":"OK","detail":"x"},{"id":"keys","status":"FAIL","detail":"FAIL %s"},{"id":"x","status":"MAYBE","detail":"y"},{"id":"cfg","status":"WARN"},{"id":"n.1:a-b_c","status":"INFO","detail":5},{"status":"OK"},"str",null]}\n' "$HOME" "$long"
  exit 1 ;;
empty) printf '{"rows":[]}\n' ;;
junk) echo "not json"; exit 3 ;;
silent) exit 0 ;;
killed) kill -9 $$ ;;
esac
`

const lrTestFakeEditor = `#!/bin/sh
printf 'editor %s %s\n' "$(ls -l "$1" | cut -c1-10)" "${1##*/}" >> "$FAKELOG"
case "$EDITMODE" in
rewrite) printf 'My own words at %s/notes\n' "$HOME" > "$1" ;;
keep) ;;
secret) printf 'key sk-abcdefghijklmnop\n' > "$1" ;;
fail) exit 2 ;;
killed) kill -9 $$ ;;
esac
`

// Tests never run the test binary as the setup check: a test that wants a
// doctor sets lrSelf to a fake one.
func init() {
	lrSelf = func() (string, error) { return "", errors.New("tests run no real doctor") }
}

type lrAsked struct {
	Kind    string   `json:"kind"`
	Message string   `json:"message"`
	Labels  []string `json:"labels"`
	Initial int      `json:"initial"`
	Picked  int      `json:"picked"`
}

type lrNodeCase struct {
	Name     string            `json:"name"`
	Args     []string          `json:"args"`
	Env      map[string]string `json:"env"`
	TTY      bool              `json:"tty"`
	Code     int               `json:"code"`
	Stdout   string            `json:"stdout"`
	Stderr   string            `json:"stderr"`
	Asked    []lrAsked         `json:"asked"`
	Children string            `json:"children"`
}

// lrTestTerminal fakes a terminal on stdin and stdout of a usable size that
// answers each question with Node's recorded pick; got collects what Go asked.
func lrTestTerminal(t *testing.T, answers []lrAsked, got *[]lrAsked) {
	t.Helper()
	oldStdin, oldAsk, oldConfirm, oldSelect, oldFits := logsStdinTTY, logsAskTTY, logsConfirm, logsSelect, logsPromptFits
	logsStdinTTY = func() bool { return true }
	logsAskTTY = func() bool { return true }
	logsPromptFits = func() bool { return true }
	next := func() lrAsked {
		if len(*got) >= len(answers) {
			return lrAsked{Picked: -1}
		}
		return answers[len(*got)]
	}
	logsConfirm = func(message string) (humanask.Result, error) {
		answer := next()
		*got = append(*got, lrAsked{Kind: "confirm", Message: message, Picked: answer.Picked})
		if answer.Picked == 1 {
			return humanask.Confirmed, nil
		}
		return humanask.Cancelled, nil
	}
	logsSelect = func(message string, labels []string, initial int) (int, humanask.Result, error) {
		answer := next()
		*got = append(*got, lrAsked{Kind: "select", Message: message, Labels: labels, Initial: initial, Picked: answer.Picked})
		if answer.Picked < 0 {
			return initial, humanask.Cancelled, nil
		}
		return answer.Picked, humanask.Confirmed, nil
	}
	t.Cleanup(func() {
		logsStdinTTY, logsAskTTY, logsConfirm, logsSelect, logsPromptFits = oldStdin, oldAsk, oldConfirm, oldSelect, oldFits
	})
}

// lrTestFakes adds the fake doctor (as this binary) and editor; it returns
// the editor's path.
func lrTestFakes(t *testing.T) string {
	t.Helper()
	bin := strings.Split(os.Getenv("PATH"), ":")[0]
	for name, text := range map[string]string{"doctor": lrTestFakeDoctor, "editor": lrTestFakeEditor} {
		b500Write(t, filepath.Join(bin, name), text)
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	old := lrSelf
	lrSelf = func() (string, error) { return filepath.Join(bin, "doctor"), nil }
	t.Cleanup(func() { lrSelf = old })
	for _, name := range []string{"DOCTOR_MODE", "EDITMODE", "VISUAL", "EDITOR"} {
		t.Setenv(name, "")
		_ = os.Unsetenv(name)
	}
	return filepath.Join(bin, "editor")
}

var lrTestBodyFile = regexp.MustCompile(`\{\{TMP\}\}/\S+`)

func lrTestNodeCases(t *testing.T) []lrNodeCase {
	t.Helper()
	file, err := os.Open(filepath.Join("testdata", "b677", "node.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	cases := []lrNodeCase{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	for scanner.Scan() {
		var tc lrNodeCase
		if err := json.Unmarshal(scanner.Bytes(), &tc); err != nil {
			t.Fatal(err)
		}
		cases = append(cases, tc)
	}
	if len(cases) != 25 {
		t.Fatalf("Node cases = %d, want 25", len(cases))
	}
	return cases
}

// Each question, answer, exit code, output, child and error-log row as Node.
func TestB677LogsReportPromptsAndDoctorMatchNode(t *testing.T) {
	for _, tc := range lrTestNodeCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			c := lrTestEnv(t, "", false)
			editor := lrTestFakes(t)
			for key, value := range tc.Env {
				t.Setenv(key, strings.ReplaceAll(value, "{{EDITOR}}", editor))
			}
			got := []lrAsked{}
			if tc.TTY {
				lrTestTerminal(t, tc.Asked, &got)
			}
			before := b500Read(t, c.errorLog())
			code, out, errText := b500Run(t, append([]string{"logs", "report"}, tc.Args...)...)
			home := func(s string) string { return strings.ReplaceAll(s, "{{HOME}}", c.home) }
			lrTestCheck(t, tc.Name, code, out, errText, tc.Code, home(tc.Stdout), home(tc.Stderr))
			gotAsked, _ := json.Marshal(got)
			wantAsked, _ := json.Marshal(append([]lrAsked{}, tc.Asked...))
			if string(gotAsked) != string(wantAsked) {
				t.Errorf("questions:\n%s\nwant Node's:\n%s", gotAsked, wantAsked)
			}
			children := lrTestBodyFile.ReplaceAllString(strings.ReplaceAll(c.fakeLog(t), c.tmpRoot, "{{TMP}}"), "<file>")
			if want := lrTestBodyFile.ReplaceAllString(home(tc.Children), "<file>"); children != want {
				t.Errorf("children:\n%s\nwant Node's:\n%s", children, want)
			}
			if reason := b500HandedOver(t, c.home); reason != "" {
				t.Errorf("handed over: %s", reason)
			}
			if left := c.rootLeft(t); len(left) != 0 {
				t.Errorf("run root or edit folder left behind: %v", left)
			}
			logged := strings.Contains(b500Read(t, c.errorLog()), `"verb":"logs","sub":"report"`)
			if logged != (tc.Code != 0) || tc.Code == 0 && b500Read(t, c.errorLog()) != before {
				t.Errorf("error log row = %v for exit %d (Node's router logs every failure)", logged, tc.Code)
			}
		})
	}
}

// The questions Go cannot draw like clack still go to Node before any output.
func TestB677LogsReportPromptHandsOverWhenItCannotDraw(t *testing.T) {
	for _, tc := range []struct {
		name       string
		both, fits bool
		args       []string
	}{
		{"stdout not a terminal", false, true, []string{"--no-ai"}},
		{"terminal too small", true, false, []string{"--no-ai"}},
		{"ai check may ask", false, true, []string{"--yes", "--allow-old"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := lrTestEnv(t, "", false)
			got := []lrAsked{}
			lrTestTerminal(t, nil, &got)
			logsAskTTY = func() bool { return tc.both }
			logsPromptFits = func() bool { return tc.fits }
			code, out, _ := b500Run(t, append([]string{"logs", "report"}, tc.args...)...)
			if code != 1 || out != "" || b500HandedOver(t, c.home) != "logs-report-prompt" || len(got) != 0 || c.fakeLog(t) != "" {
				t.Errorf("got (%d, %q) reason %q asked %v children %q", code, out, b500HandedOver(t, c.home), got, c.fakeLog(t))
			}
		})
	}
	// --no-ai --yes --allow-old and --dry-run never ask: no terminal checks.
	c := lrTestEnv(t, "", false)
	got := []lrAsked{}
	lrTestTerminal(t, nil, &got)
	logsAskTTY = func() bool { return false }
	code, out, errText := b500Run(t, "logs", "report", "--no-ai", "--dry-run")
	lrTestCheck(t, "dry run", code, out, errText, 0, c.golden(t, "dry.stdout"), "")
	code, out, _ = b500Run(t, "logs", "report", "--no-ai", "--yes", "--allow-old")
	if code != 0 || !strings.HasSuffix(out, "sent: https://github.com/x/y/issues/42\n") || len(got) != 0 {
		t.Errorf("--no-ai --yes --allow-old: %d %q asked %v", code, out, got)
	}
}

// The edited report goes to gh as shown: scrubbed, with the footer back.
func TestB677LogsReportEditedBodyIsSent(t *testing.T) {
	c := lrTestEnv(t, "", false)
	t.Setenv("EDITOR", lrTestFakes(t))
	t.Setenv("EDITMODE", "rewrite")
	got := []lrAsked{}
	lrTestTerminal(t, []lrAsked{{Picked: 1}, {Picked: 0}, {Picked: 1}}, &got)
	code, out, _ := b500Run(t, "logs", "report", "--no-ai")
	body := "My own words at ~/notes\n\n### Fingerprint\n\nFingerprint: 0123456789ab\n\n<!-- code-forge-fp: 0123456789ab -->\n"
	log := c.fakeLog(t)
	head := strings.TrimSuffix(log, "\n"+body)
	if code != 0 || head == log || !strings.HasPrefix(head[strings.LastIndex(head, "\n")+1:], "BODYFILE -rw------- "+c.tmpRoot+"/proc-") || strings.Contains(log, c.home) {
		t.Errorf("exit %d, gh body:\n%s\nstdout:\n%s", code, log, out)
	}
}

// A select input failure (a closed terminal) is a cancel, like clack's.
func TestB677LogsReportSelectFailureIsCancel(t *testing.T) {
	c := lrTestEnv(t, "", false)
	got := []lrAsked{}
	lrTestTerminal(t, nil, &got)
	logsSelect = func(message string, labels []string, initial int) (int, humanask.Result, error) {
		return initial, humanask.Cancelled, os.ErrClosed
	}
	code, out, errText := b500Run(t, "logs", "report", "--no-ai")
	if code != 1 || !strings.HasSuffix(out, "\nnot sent\n") || errText != "" || strings.Contains(c.fakeLog(t), "gh ") {
		t.Errorf("got %d %q %q children %q", code, out, errText, c.fakeLog(t))
	}
}
