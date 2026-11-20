package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/releasecheck"
)

// B799: `code-forge release check` answers "is this project ready to release?" in one command:
// clean tree, HEAD pushed, GitHub CI green for the HEAD sha, CHANGELOG [Unreleased] shape and a
// clean `npm ls` (own platform packages at the previous version ignored; npm prune named when
// only extraneous packages fail). It suggests the bump and prints the release steps in order.
// Exit 0 ready, 1 not ready, 2 usage. It only reads; it never releases.

func init() { routes["release"] = runRelease }

const releaseUsage = "usage: code-forge release check [--cwd <dir>] [--json]"

// releaseRunner is the command seam (tests use fake gh/npm on PATH through the default runner).
var releaseRunner releasecheck.Runner = releasecheck.DefaultRunner

func runRelease(args []string) int { return releaseMain(args, safeStdout, safeStderr) }

func releaseMain(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, releaseUsage)
		return 0
	}
	if len(args) == 0 || args[0] != "check" {
		fmt.Fprintln(stderr, releaseUsage)
		return 2
	}
	if len(args) > 1 && (args[1] == "--help" || args[1] == "-h") {
		fmt.Fprintln(stdout, releaseUsage)
		return 0
	}
	cwd, asJSON := "", false
	for i := 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json" && !asJSON:
			asJSON = true
		case a == "--cwd" && cwd == "" && i+1 < len(args) && args[i+1] != "" && !strings.HasPrefix(args[i+1], "-"):
			cwd = args[i+1]
			i++
		case strings.HasPrefix(a, "--cwd=") && cwd == "" && len(a) > len("--cwd="):
			cwd = strings.TrimPrefix(a, "--cwd=")
		default:
			fmt.Fprintln(stderr, "release check: unknown or repeated option "+a)
			fmt.Fprintln(stderr, releaseUsage)
			return 2
		}
	}
	root, err := releaseRoot(cwd)
	if err != nil {
		fmt.Fprintln(stderr, "release check: "+err.Error())
		return 2
	}
	report := releasecheck.Run(root, releaseRunner)
	if asJSON {
		fmt.Fprint(stdout, report.JSON())
	} else {
		fmt.Fprint(stdout, report.Text())
	}
	if report.Ready {
		return 0
	}
	return 1
}

// releaseRoot is the git top level of dir (or of the current folder), else dir itself.
func releaseRoot(dir string) (string, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s is not a folder", abs)
	}
	top := releaseRunner(abs, 0, "git", "rev-parse", "--show-toplevel")
	if t := strings.TrimSpace(top.Stdout); top.Code == 0 && t != "" {
		return t, nil
	}
	return abs, nil
}

// autopilotReleaseReport is the release check the autopilot prints as its final report when the
// grant stops (B799): the run's workspace, or the current folder. It never changes the exit code.
func autopilotReleaseReport(workspace string) string {
	root, err := releaseRoot(workspace)
	if err != nil {
		return "release check: skipped (" + err.Error() + ")\n"
	}
	return releasecheck.Run(root, releaseRunner).Text()
}

// autopilotStopReleaseCheck is the release check `autopilot stop` prints (B799) when the run's
// workspace is a project a release applies to: the top of a git work tree with a CHANGELOG.md or
// a package.json. Any other workspace prints nothing.
func autopilotStopReleaseCheck(workspace string) string {
	if !releaseApplies(workspace) {
		return ""
	}
	return autopilotReleaseReport(workspace)
}

// releaseApplies: the workspace is the top of a git work tree with a
// CHANGELOG.md or a package.json, a project a release applies to.
func releaseApplies(workspace string) bool {
	if workspace == "" || !filepath.IsAbs(workspace) {
		return false
	}
	_, clErr := os.Stat(filepath.Join(workspace, "CHANGELOG.md"))
	_, pkgErr := os.Stat(filepath.Join(workspace, "package.json"))
	if clErr != nil && pkgErr != nil {
		return false
	}
	top := releaseRunner(workspace, 0, "git", "rev-parse", "--show-toplevel")
	return top.Code == 0 && releaseSameDir(strings.TrimSpace(top.Stdout), workspace)
}

// releaseSizeLine is the release size of the run's workspace (B951): the
// RELEASE NOW or near line, or "" when it does not apply.
func releaseSizeLine(workspace any) string {
	ws, _ := workspace.(string)
	if !releaseApplies(ws) {
		return ""
	}
	return releasecheck.SizeOf(ws, releaseRunner).Line()
}

func releaseSameDir(a, b string) bool {
	x, err1 := os.Stat(a)
	y, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(x, y)
}
