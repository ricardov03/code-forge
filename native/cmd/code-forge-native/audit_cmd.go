package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/releaseaudit"
)

// B802: `code-forge audit commands` runs every command of the Go help table
// in a throwaway HOME and git repository, with stubs for the paid and
// credential tools first on PATH and the network blocked, and writes the
// command audit report a major release needs (docs/audits/audit-<version>.md,
// checked by `npm run release -- major` and `code-forge release check`).
// B801 adds more checks per command: append them to auditChecks.

const auditUsage = "usage: code-forge audit commands [--json] [--out <file>]\n"

func init() {
	routes["audit"] = smallVerb{name: "audit", usage: auditUsage, subs: []string{"commands"},
		subUsage: map[string]string{"commands": auditUsage}, prepare: prepareAudit}.run
}

// auditStubs are the tools every audited command finds first on PATH: each
// one logs its call and exits 1, so no paid API, keychain or 1Password is used.
var auditStubs = []string{"op", "security", "claude", "codex", "grok", "gemini", "gh", "curl", "npm", "npx", "node"}

// auditTimeout is how long one audited command may run before it is stopped.
var auditTimeout = 20 * time.Second

// auditSelf is the binary under audit (a test can point it at a built binary).
var auditSelf = os.Executable

// auditNow is the report date.
var auditNow = func() time.Time { return time.Now().UTC() }

// auditResult is what one audited command did.
type auditResult struct {
	code           int
	stdout, stderr string
	timedOut       bool
	handover       bool
	startErr       error
}

// auditCheck is one check run against every audited command. args are the
// arguments after the command words; judge turns the result into a row
// (Result ok, or problem with a severity and detail).
type auditCheck struct {
	name  string
	args  func(entry helpEntry) []string
	judge func(entry helpEntry, r auditResult) (severity, detail string)
}

// auditBadFlag is a flag no command has.
const auditBadFlag = "--code-forge-audit-no-such-flag"

// auditCommon is the problem every check shares: a crash, a hand-over to the
// removed Node engine, a hang, or a binary that did not start.
func auditCommon(r auditResult) (string, string) {
	switch {
	case r.startErr != nil:
		return releaseaudit.High, "did not start: " + r.startErr.Error()
	case strings.Contains(r.stderr, "panic:") || strings.Contains(r.stderr, "goroutine "):
		detail := auditFirstLine(r.stderr)
		if !strings.HasPrefix(detail, "panic") {
			detail = "panic: " + detail
		}
		return releaseaudit.High, detail
	case r.handover:
		return releaseaudit.High, "handed over: " + auditFirstLine(r.stderr)
	}
	return "", ""
}

var auditChecks = []auditCheck{
	{
		name: "help",
		args: func(helpEntry) []string { return []string{"--help"} },
		judge: func(entry helpEntry, r auditResult) (string, string) {
			if sev, detail := auditCommon(r); sev != "" {
				return sev, detail
			}
			switch {
			case r.timedOut:
				return releaseaudit.High, "--help did not end in " + auditTimeout.String()
			case r.code != 0 && !strings.Contains(strings.ToLower(r.stdout+r.stderr), "usage:"):
				return releaseaudit.High, fmt.Sprintf("--help exited %d with no usage: %s", r.code, auditFirstLine(r.stderr+r.stdout))
			case r.code != 0:
				return releaseaudit.Medium, fmt.Sprintf("--help printed usage but exited %d: %s", r.code, auditFirstLine(r.stderr+r.stdout))
			case !strings.Contains(r.stdout, "code-forge "+strings.Fields(entry.name)[0]):
				return releaseaudit.Medium, "--help printed no usage line: " + auditFirstLine(r.stdout)
			}
			return "", ""
		},
	},
	{
		name: "bad-flag",
		args: func(helpEntry) []string { return []string{auditBadFlag} },
		judge: func(_ helpEntry, r auditResult) (string, string) {
			if sev, detail := auditCommon(r); sev != "" {
				return sev, detail
			}
			switch {
			case r.timedOut:
				return releaseaudit.Medium, "an unknown flag did not end in " + auditTimeout.String()
			case r.code == 0:
				return releaseaudit.Medium, "an unknown flag exited 0"
			case strings.TrimSpace(r.stderr) == "":
				return releaseaudit.Low, fmt.Sprintf("an unknown flag exited %d with nothing on stderr", r.code)
			}
			return "", ""
		},
	},
}

func auditFirstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			if len(t) > 160 {
				t = t[:160] + "…"
			}
			return t
		}
	}
	return "(no output)"
}

// auditCommands are the commands of the help table an audit runs: every
// command that is not hidden, namespaces included, sorted by name.
func auditCommands() []helpEntry {
	var out []helpEntry
	for _, entry := range helpCommands {
		if !entry.hidden {
			out = append(out, entry)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// auditSandbox is the throwaway world the commands run in.
type auditSandbox struct {
	dir, home, repo, bin, tmp, stubLog string
}

func auditMakeSandbox() (*auditSandbox, error) {
	dir, err := os.MkdirTemp("", "code-forge-audit-")
	if err != nil {
		return nil, err
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	s := &auditSandbox{dir: dir, home: filepath.Join(dir, "home"), repo: filepath.Join(dir, "repo"),
		bin: filepath.Join(dir, "bin"), tmp: filepath.Join(dir, "tmp"), stubLog: filepath.Join(dir, "stubs.log")}
	for _, d := range []string{s.home, s.repo, s.bin, s.tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return s, err
		}
	}
	for _, name := range auditStubs {
		body := "#!/bin/sh\necho \"" + name + " $*\" >> \"" + s.stubLog + "\"\necho \"" + name + ": stub (code-forge audit)\" >&2\nexit 1\n"
		if err := os.WriteFile(filepath.Join(s.bin, name), []byte(body), 0o755); err != nil {
			return s, err
		}
	}
	if git, err := exec.LookPath("git"); err == nil {
		if err := os.Symlink(git, filepath.Join(s.bin, "git")); err != nil {
			return s, err
		}
	}
	if err := os.WriteFile(filepath.Join(s.repo, "README.md"), []byte("# audit\n"), 0o644); err != nil {
		return s, err
	}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, {"commit", "-q", "-m", "initial"}} {
		cmd := exec.Command(filepath.Join(s.bin, "git"), args...)
		cmd.Dir, cmd.Env = s.repo, s.env("")
		if out, err := cmd.CombinedOutput(); err != nil {
			return s, fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
	}
	return s, nil
}

// env is the whole environment of an audited command: nothing of the
// caller's environment but the fixed keys below (home "" = the shared home).
func (s *auditSandbox) env(home string) []string {
	if home == "" {
		home = s.home
	}
	proxy := "http://127.0.0.1:9"
	return []string{
		"HOME=" + home, "TMPDIR=" + s.tmp, "PATH=" + s.bin + ":/usr/bin:/bin",
		"CODE_FORGE_KEY_BACKEND=file", "CODE_FORGE_NO_ERROR_LOG=1", "CODE_FORGE_HANDOVER_LOG=1",
		"CODE_FORGE_NODE=/nonexistent/code-forge-audit-node", "NO_COLOR=1", "TERM=dumb", "LC_ALL=C", "TZ=UTC",
		"HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy, "http_proxy=" + proxy, "https_proxy=" + proxy, "ALL_PROXY=" + proxy,
		"GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=code-forge audit", "GIT_AUTHOR_EMAIL=audit@example.invalid",
		"GIT_COMMITTER_NAME=code-forge audit", "GIT_COMMITTER_EMAIL=audit@example.invalid",
	}
}

// run runs the binary with words+args in the repository, with its own HOME,
// stdin empty, and stops its process group at auditTimeout.
func (s *auditSandbox) run(self string, n int, words, args []string) auditResult {
	home := filepath.Join(s.dir, "homes", fmt.Sprint(n))
	if err := os.MkdirAll(home, 0o700); err != nil {
		return auditResult{startErr: err}
	}
	ctx, cancel := context.WithTimeout(context.Background(), auditTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, append(append([]string{}, words...), args...)...)
	cmd.Dir, cmd.Env = s.repo, s.env(home)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := auditResult{stdout: stdout.String(), stderr: stderr.String()}
	if ctx.Err() != nil {
		r.timedOut = true
	}
	if exit, ok := err.(*exec.ExitError); ok {
		r.code = exit.ExitCode()
	} else if err != nil && !r.timedOut {
		r.startErr = err
	}
	if data, err := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl")); err == nil && len(bytes.TrimSpace(data)) > 0 {
		r.handover = true
	}
	if strings.Contains(r.stderr, "is not supported in this case yet") {
		r.handover = true
	}
	return r
}

// auditRun is the audit of every command with every check.
func auditRun(self string) (releaseaudit.Report, error) {
	report := releaseaudit.Report{Version: version, Date: auditNow().Format(time.RFC3339), Commit: "(not a git repository)"}
	if out, err := exec.Command("git", "-C", physicalCwd(), "rev-parse", "HEAD").Output(); err == nil {
		report.Commit = strings.TrimSpace(string(out))
	}
	s, err := auditMakeSandbox()
	if s != nil {
		defer os.RemoveAll(s.dir)
	}
	if err != nil {
		return report, err
	}
	n := 0
	for _, entry := range auditCommands() {
		for _, check := range auditChecks {
			n++
			r := s.run(self, n, strings.Fields(entry.name), check.args(entry))
			row := releaseaudit.Row{Command: entry.name, Check: check.name, Result: releaseaudit.OK}
			if sev, detail := check.judge(entry, r); sev != "" {
				row.Result, row.Severity, row.Detail = releaseaudit.Problem, sev, detail
			}
			report.Rows = append(report.Rows, row)
		}
	}
	return report, nil
}

// auditJSON is the --json line.
type auditJSON struct {
	Version  string             `json:"version"`
	Commit   string             `json:"commit"`
	Date     string             `json:"date"`
	Commands int                `json:"commands"`
	Checks   int                `json:"checks"`
	Problems map[string]int     `json:"problems"`
	Out      string             `json:"out"`
	Rows     []releaseaudit.Row `json:"rows"`
}

func prepareAudit(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	usage := func(msg string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
		return func(_, stderr io.Writer) int {
			if msg != "" {
				fmt.Fprintln(stderr, "code-forge audit: "+msg)
			}
			fmt.Fprint(stderr, auditUsage)
			return 2
		}, 0, true
	}
	if len(args) == 0 || args[0] != "commands" {
		if len(args) == 0 {
			return usage("")
		}
		return usage("unknown audit " + args[0])
	}
	asJSON, out := false, ""
	for i := 1; i < len(args); i++ {
		switch {
		case args[i] == "--json":
			asJSON = true
		case args[i] == "--out":
			if i+1 >= len(args) || args[i+1] == "" || strings.HasPrefix(args[i+1], "-") {
				return usage("--out needs a file")
			}
			out = args[i+1]
			i++
		default:
			return usage("unknown option " + args[i])
		}
	}
	return func(stdout, stderr io.Writer) int {
		self, err := auditSelf()
		if err != nil {
			fmt.Fprintln(stderr, "code-forge audit: cannot find this binary: "+err.Error())
			return 1
		}
		report, err := auditRun(self)
		if err != nil {
			fmt.Fprintln(stderr, "code-forge audit: cannot make the throwaway HOME and repository: "+err.Error())
			return 1
		}
		text := releaseaudit.Render(report)
		path := ""
		if out != "" {
			path = out
			if !filepath.IsAbs(path) {
				path = filepath.Join(physicalCwd(), path)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err == nil {
				err = os.WriteFile(path, []byte(text), 0o644)
			}
			if err != nil {
				fmt.Fprintln(stderr, "code-forge audit: cannot write "+out+": "+err.Error())
				return 1
			}
		}
		counts := report.Counts()
		commands := map[string]bool{}
		for _, row := range report.Rows {
			commands[row.Command] = true
		}
		switch {
		case asJSON:
			line, _ := json.Marshal(auditJSON{Version: report.Version, Commit: report.Commit, Date: report.Date,
				Commands: len(commands), Checks: len(report.Rows), Problems: counts, Out: out, Rows: report.Rows})
			fmt.Fprintln(stdout, string(line))
		case out != "":
			fmt.Fprintf(stdout, "audited %d commands (%d checks): high %d, medium %d, low %d\nwrote %s\n",
				len(commands), len(report.Rows), counts[releaseaudit.High], counts[releaseaudit.Medium], counts[releaseaudit.Low], out)
		default:
			fmt.Fprint(stdout, text)
		}
		if counts[releaseaudit.High] > 0 {
			fmt.Fprintf(stderr, "code-forge audit: %d high problem(s): give each a block id in the Block column and land it before a major release\n", counts[releaseaudit.High])
			return 1
		}
		return 0
	}, 0, true
}
