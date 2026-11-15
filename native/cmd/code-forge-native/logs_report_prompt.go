package main

// B677: the terminal questions `logs report` asks (src/cli/logs.mjs askYes
// and choose: "Report anyway?", the send menu with "Edit in my editor",
// "Send without the AI check?" and "What now?") and --with-doctor's setup
// check (runSetupCheck), in Go like Node.

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/humanask"
)

const (
	lrConfirmMessage = "This will be public on GitHub. Send it?"
	// DOCTOR_TIMEOUT_MS and DOCTOR_DETAIL_MAX_BYTES
	lrDoctorTimeout   = 60 * time.Second
	lrDoctorDetailMax = 120
)

var (
	// logsSelect is clack's select (cancel and an input failure are "cancel").
	logsSelect = humanask.Select
	// logsPromptFits: clack draws these menus with every option on its own
	// row (no "..." cut, no wrapped option) on a terminal at least 40 columns
	// wide and 12 rows tall; a smaller or unknown one stays in Node.
	logsPromptFits = func() bool {
		cols, rows := humanask.Size()
		return cols >= 40 && rows >= 12
	}
	// lrSelf is the code-forge binary the setup check runs (`doctor --json`,
	// Node's `node bin/code-forge.mjs doctor --json`): this one.
	lrSelf = func() (string, error) {
		exe, err := os.Executable()
		if err != nil {
			return "", err
		}
		return filepath.EvalSymlinks(exe)
	}
	// lrRunEditor is runEditorOnTerminal: the exit code (nil after a signal)
	// or the spawn error's code.
	lrRunEditor = lrEditorOnTerminal
)

var (
	lrDoctorID     = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	lrEditorPlain  = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)
	lrDoctorStatus = map[string]bool{"OK": true, "WARN": true, "FAIL": true, "INFO": true}
)

// lrSetup is SetupCheck: rows, or why the check could not run.
type lrSetup struct {
	why  string
	rows [][3]string
}

// lrAskYes is askYes with initialValue false: only a submitted Yes is yes.
func lrAskYes(message string) bool {
	answer, err := logsConfirm(message)
	return err == nil && answer == humanask.Confirmed
}

// lrChoose is choose: the picked value; a cancelled prompt is "cancel".
func lrChoose(message string, values, labels []string, initial string) string {
	start := 0
	for i, v := range values {
		if v == initial {
			start = i
		}
	}
	index, answer, err := logsSelect(message, labels, start)
	if err != nil || answer != humanask.Confirmed || index < 0 || index >= len(values) {
		return "cancel"
	}
	return values[index]
}

// lrRunSetupCheck is runSetupCheck: `code-forge doctor --json` as a child
// (60 s, error logging off) keeping each valid row's id, status and the
// cleaned first line of its detail.
func lrRunSetupCheck(scrub func(string) string) lrSetup {
	self, err := lrSelf()
	if err != nil {
		return lrSetup{why: "doctor did not run"}
	}
	saved, had := os.LookupEnv("CODE_FORGE_NO_ERROR_LOG")
	os.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	res := lrExec([]string{self, "doctor", "--json"}, lrDoctorTimeout)
	if had {
		os.Setenv("CODE_FORGE_NO_ERROR_LOG", saved)
	} else {
		os.Unsetenv("CODE_FORGE_NO_ERROR_LOG")
	}
	if res.timedOut {
		return lrSetup{why: "timed out"}
	}
	var doc any
	if json.Unmarshal([]byte(res.stdout), &doc) != nil {
		doc = nil
	}
	object, _ := doc.(map[string]any)
	list, isList := object["rows"].([]any)
	if !isList {
		switch {
		case res.code == nil:
			return lrSetup{why: "doctor did not run"}
		case *res.code == 0:
			return lrSetup{why: "doctor gave no result"}
		}
		return lrSetup{why: "doctor exit " + strconv.Itoa(*res.code)}
	}
	setup := lrSetup{rows: [][3]string{}}
	for _, item := range list {
		row, _ := item.(map[string]any)
		id, _ := row["id"].(string)
		status, _ := row["status"].(string)
		if !lrDoctorID.MatchString(id) || !lrDoctorStatus[status] {
			continue
		}
		detail, _ := row["detail"].(string)
		first := ""
		for _, line := range strings.Split(detail, "\n") {
			if logsTrim(line) != "" {
				first = logsTrim(line)
				break
			}
		}
		setup.rows = append(setup.rows, [3]string{id, status, lrCapBytes(scrub(first), lrDoctorDetailMax)})
	}
	return setup
}

// lrSetupLines is buildReport's "### Setup check" section.
func lrSetupLines(setup *lrSetup) []string {
	if setup == nil {
		return nil
	}
	lines := []string{"", "### Setup check", ""}
	if setup.rows == nil {
		return append(lines, "(the setup check could not run: "+setup.why+")")
	}
	lines = append(lines, "| check | status | detail |", "|---|---|---|")
	for _, r := range setup.rows {
		lines = append(lines, "| "+lrCell(r[0])+" | "+lrCell(r[1])+" | "+lrCell(r[2])+" |")
	}
	return lines
}

// lrEditorCommand is editorCommand: $VISUAL, else $EDITOR, when it is a
// plain command name or an absolute path with no whitespace.
func lrEditorCommand() string {
	for _, raw := range []string{os.Getenv("VISUAL"), os.Getenv("EDITOR")} {
		if raw == "" || strings.ContainsFunc(raw, errorLogSpace) {
			continue
		}
		if filepath.IsAbs(raw) || lrEditorPlain.MatchString(raw) {
			return raw
		}
	}
	return ""
}

// lrEditorOnTerminal is runEditorOnTerminal: argv only, the terminal's
// stdio, never a shell.
func lrEditorOnTerminal(argv []string) (*int, string) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, lrSpawnCode(argv[0], err)
	}
	err := cmd.Wait()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return nil, "error"
	}
	if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return nil, ""
	}
	code := cmd.ProcessState.ExitCode()
	return &code, ""
}

// lrSpawnCode is the errno name a spawn error carries in Node (libuv's
// execvp: a name found on PATH but not runnable is EACCES).
func lrSpawnCode(name string, err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		if !strings.Contains(name, "/") {
			for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
				if dir == "" {
					dir = "."
				}
				if st, statErr := os.Stat(filepath.Join(dir, name)); statErr == nil && !st.IsDir() {
					return "EACCES"
				}
			}
		}
		return "ENOENT"
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		for code, name := range map[syscall.Errno]string{syscall.ENOENT: "ENOENT", syscall.EACCES: "EACCES", syscall.ENOEXEC: "ENOEXEC",
			syscall.ENOTDIR: "ENOTDIR", syscall.EPERM: "EPERM", syscall.ELOOP: "ELOOP", syscall.ENAMETOOLONG: "ENAMETOOLONG", syscall.E2BIG: "E2BIG"} {
			if errno == code {
				return name
			}
		}
	}
	return "error"
}

// lrEditBody is editBody: the edited text, or false when the editor could
// not run (the reason is printed).
func lrEditBody(body string, out func(string)) (string, bool) {
	editor := lrEditorCommand()
	if editor == "" {
		out("Cannot open an editor: set VISUAL or EDITOR to a plain command (a name such as vim, or an absolute path, with no spaces or flags).\n")
		return "", false
	}
	failed := func() (string, bool) {
		out("Could not edit the report; the text is unchanged.\n")
		return "", false
	}
	root, err := lrProcRoot()
	if err != nil || os.MkdirAll(root, 0o777) != nil {
		return failed()
	}
	dir, err := lrMkdtemp(filepath.Join(root, "report-edit-"))
	if err != nil {
		return failed()
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "report.md")
	if os.WriteFile(file, []byte(body), 0o600) != nil {
		return failed()
	}
	code, why := lrRunEditor([]string{editor, file})
	if code == nil || *code != 0 {
		if why == "" {
			why = "exit null"
			if code != nil {
				why = "exit " + strconv.Itoa(*code)
			}
		}
		out("The editor did not finish (" + why + "); the text is unchanged.\n")
		return "", false
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return failed()
	}
	return nodeUTF8(string(data)), true
}
