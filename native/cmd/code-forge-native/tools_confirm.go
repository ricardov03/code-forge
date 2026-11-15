package main

import (
	"fmt"
	"io"

	"github.com/ricardov03/code-forge/native/humanask"
)

// Seams for the install question (B682). Node asks only when stdin is a
// terminal (toolsStdinTTY); clack draws on stdout, so Go draws the prompt
// only when both ends are terminals (humanask.Available). A terminal stdin
// with a piped stdout stays with Node (hToolsConfirm).
var (
	toolsAskTTY  = humanask.Available
	toolsConfirm = humanask.Confirm
)

// toolsNeedsNodePrompt is the one confirm case Go cannot draw like clack:
// installs are pending, nothing skips the question, stdin is a terminal and
// stdout is not.
func toolsNeedsNodePrompt(pending int, yes, dryRun bool) bool {
	return pending > 0 && !yes && !dryRun && toolsStdinTTY() && !toolsAskTTY()
}

// toolsAskInstall is Node's question in src/cli/tools.mjs runTools: one clack
// confirm, default no. Anything but yes (no, Escape, Ctrl-C, a closed or
// failed input) prints "cancelled — nothing installed" and exits 1; ok is
// true only on yes.
func toolsAskInstall(stdout io.Writer, pending int) (code int, ok bool) {
	answer, err := toolsConfirm(fmt.Sprintf("Run %d install command(s)?", pending))
	if err != nil || answer != humanask.Confirmed {
		fmt.Fprint(stdout, "cancelled — nothing installed\n")
		return 1, false
	}
	return 0, true
}
