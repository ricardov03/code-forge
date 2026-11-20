package main

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/refguard"
	"github.com/ricardov03/code-forge/native/state"
)

// B959b: land.regenerate is a list of shell commands a batch land runs once
// on the combined tip, in the main checkout, after the merge and before the
// push (so CI sees the result): the generated docs and recorded test pins a
// wave changed (`sh scripts/cf-release.sh regen-docs`). What they change is
// one commit on main; nothing changed is no commit. A failing command stops
// the land at step regenerate before the push: main keeps the landed merge
// and the main checkout keeps what the commands wrote, for the owner to see.

// landRegenerateCommands is the run's land.regenerate list.
func landRegenerateCommands(cfg map[string]any) []string {
	list, _ := blockMap(cfg["land"])["regenerate"].([]any)
	out := []string{}
	for _, item := range list {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// landBatchRegenerate runs land.regenerate after a batch landed ids. It
// returns the first landed block (for the stop line) and an error.
func landBatchRegenerate(home, run string, ids []string, json bool) (string, error) {
	record, err := state.ReadRun(home, run)
	if err != nil {
		return "", err
	}
	cfg, _ := blockSnapshot(record)
	commands := landRegenerateCommands(cfg)
	if len(commands) == 0 {
		return "", nil
	}
	blocks := blockObject(record.Get("blocks"))
	landed := []string{}
	for _, id := range ids {
		if entry := blockObject(blocks.Get(id)); entry != nil && entry.Get("landed") != nil {
			landed = append(landed, id)
		}
	}
	if len(landed) == 0 {
		return "", nil
	}
	main, err := gitx.RepoTop(blockString(record.Get("workspace")))
	if err != nil {
		return landed[0], err
	}
	if err := landRegenerateAt(main, commands, landed, json); err != nil {
		return landed[0], err
	}
	return "", nil
}

// Shared by block land and lane combine: regenerate exactly once at their
// combined checkout, then record the generated changes in one commit.
func landRegenerateAt(main string, commands, landed []string, json bool) error {
	if dirty, err := gitx.Run(main, "status", "--porcelain"); err != nil || strings.TrimSpace(dirty) != "" {
		return fmt.Errorf("land.regenerate: the main checkout has uncommitted changes; no command ran")
	}
	for _, command := range commands {
		cmd := exec.Command("sh", "-c", command)
		cmd.Dir = main
		cmd.Env = gitx.ChildEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("land.regenerate: `%s` failed (%v); checkout keeps the generated changes, not pushed\n%s", command, err, rdTailLines(string(out), 20))
		}
	}
	changed, err := gitx.Run(main, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(changed) == "" {
		landRegenerateNote(json, "", 0)
		return nil
	}
	if _, err := gitx.Run(main, "add", "-A"); err != nil {
		return err
	}
	message := "regenerate after landing " + strings.Join(landed, " ") + "\n\nland.regenerate (B959b):\n" + strings.Join(commands, "\n") + "\n"
	refguard.Authorize(main)
	if _, err := gitx.WithEnv(main, refguard.LandEnv(landed...), "-c", "commit.gpgsign=false", "commit", "-q", "-m", message); err != nil {
		return err
	}
	head, err := gitx.Run(main, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	landRegenerateNote(json, strings.TrimSpace(head), len(strings.Split(strings.TrimSpace(changed), "\n")))
	return nil
}

func landRegenerateNote(json bool, commit string, files int) {
	if json {
		fmt.Fprintf(safeStdout, "{\"event\":\"regenerate\",\"commit\":%q,\"files\":%d}\n", commit, files)
		return
	}
	if commit == "" {
		fmt.Fprintln(safeStdout, "regenerate: nothing changed")
		return
	}
	fmt.Fprintf(safeStdout, "regenerate: committed %d files as %s\n", files, commit[:12])
}

// rdTailLines keeps the last n lines of a command's output.
func rdTailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
