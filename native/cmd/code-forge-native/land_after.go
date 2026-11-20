package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/refguard"
	"github.com/ricardov03/code-forge/native/state"
)

// B960: land.after is the tail the maintainer scripts ran by hand after each
// land (build-native --host, docs-block stamp, a commit, a pipeline line).
// After the merge on main, and before the push, land runs each command in the
// main checkout, commits their changes to tracked files on main in one commit
// and writes one signed block.landed row per landed block (the binnacle row).

// landAfterCommands is land.after from the run-start config: the non-empty
// command strings, in order.
func landAfterCommands(cfg map[string]any) []string {
	list, _ := blockMap(cfg["land"])["after"].([]any)
	out := []string{}
	for _, v := range list {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// landAfterError is a land.after command that failed: main keeps the merge,
// nothing is pushed, and next is the command to run by hand.
type landAfterError struct{ reason, next string }

func (e *landAfterError) Error() string { return e.reason }

// landAfterEnv is the user's environment without git's own variables (the
// commands and the commit see the repository the way a shell in main does),
// with land's reflog action so main's ref-guard check counts the commit as land.
func landAfterEnv(ids []string) []string {
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			env = append(env, v)
		}
	}
	return append(env, "GIT_TERMINAL_PROMPT=0", "GIT_REFLOG_ACTION="+refguard.LandAction+" "+strings.Join(ids, " "))
}

// landAfterTail is the last lines of a command's output, for a refusal.
func landAfterTail(out []byte) string {
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	text := strings.TrimSpace(strings.Join(lines, "\n"))
	if text == "" {
		return "no output"
	}
	return text
}

// landAfter runs land.after in main for the blocks this land merged, commits
// the changes to tracked files, and writes one block.landed row per block.
func landAfter(home, run string, cfg map[string]any, main string, ids []string, print io.Writer) error {
	if len(ids) == 0 {
		return nil
	}
	commands := landAfterCommands(cfg)
	env := landAfterEnv(ids)
	afterCommit := any(nil)
	for _, command := range commands {
		cmd := exec.Command("/bin/sh", "-c", command)
		cmd.Dir, cmd.Env = main, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			return &landAfterError{
				reason: fmt.Sprintf("land.after command %q failed (%v): %s; main keeps the merge, nothing was pushed; fix it, run the command in %s, commit, then push", command, err, landAfterTail(out), main),
				next:   command,
			}
		}
		fmt.Fprintf(print, "land.after: ran %s\n", command)
	}
	if len(commands) > 0 {
		status, err := gitx.Run(main, "status", "--porcelain", "--untracked-files=no")
		if err != nil {
			return err
		}
		if strings.TrimSpace(status) != "" {
			message := fmt.Sprintf("land.after for %s: %s", strings.Join(ids, " "), strings.Join(commands, "; "))
			cmd := exec.Command("git", "-C", main, "commit", "-q", "-a", "-m", message)
			cmd.Env = env
			if out, err := cmd.CombinedOutput(); err != nil {
				return &landAfterError{
					reason: fmt.Sprintf("land.after: commit of the command changes failed (%v): %s; main keeps the merge, nothing was pushed", err, landAfterTail(out)),
					next:   closeCommand([]string{"git", "-C", main, "status"}),
				}
			}
			sha, err := gitx.Run(main, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			afterCommit = strings.TrimSpace(sha)
			fmt.Fprintf(print, "land.after: committed %.12s\n", afterCommit)
		}
	}
	head, err := gitx.Run(main, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	after := []any{}
	for _, command := range commands {
		after = append(after, command)
	}
	for _, id := range ids {
		row := ledger.Object{{Key: "event", Value: "block.landed"}, {Key: "block", Value: id}, {Key: "main", Value: strings.TrimSpace(head)}, {Key: "after", Value: after}, {Key: "after_commit", Value: afterCommit}}
		if _, err := state.WriteSigned(home, run, row); err != nil {
			return err
		}
	}
	return nil
}
