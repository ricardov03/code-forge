package main

import (
	"fmt"
	"io"

	"github.com/ricardov03/code-forge/native/humanask"
)

// Seams for the install question (B682). Node asks only when stdin is a
// terminal (toolsStdinTTY); clack draws on stdout. Both ends terminals
// (humanask.Available) ask with humanask.Confirm; a terminal stdin with a
// piped stdout (B698) asks with humanask.ConfirmPiped, as clack still draws
// its frames on the pipe at 80 columns.
var (
	toolsAskTTY       = humanask.Available
	toolsConfirm      = humanask.Confirm
	toolsConfirmPiped = humanask.ConfirmPiped
)

// toolsAskInstall is Node's question in src/cli/tools.mjs runTools: one clack
// confirm, default no. Anything but yes (no, Escape, Ctrl-C, a closed or
// failed input) prints "cancelled — nothing installed" and exits 1; ok is
// true only on yes.
func toolsAskInstall(stdout io.Writer, pending int) (code int, ok bool) {
	confirm := toolsConfirm
	if !toolsAskTTY() {
		confirm = toolsConfirmPiped // stdout is a file or a pipe: clack writes its frames there
	}
	answer, err := confirm(fmt.Sprintf("Run %d install command(s)?", pending))
	if err != nil || answer != humanask.Confirmed {
		fmt.Fprint(stdout, "cancelled — nothing installed\n")
		return 1, false
	}
	return 0, true
}
