package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B408: block rebase stays in Node, but Go runs it as a child instead of
// replacing itself, so that a rebase that brings native changes into a
// code-forge worktree rebuilds that worktree's native binary afterwards.
// B431: the plain rebase of an open worktree block runs in Go (rebase.RunPlain):
// owned files main changed are replayed instead of refused up front.
func init() { blockSubs["rebase"] = healWrap([]string{"block", "rebase"}, runBlockRebase) }

func short12(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func runBlockRebase(args []string) int {
	full := append([]string{"rebase"}, args...)
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fmt.Fprint(os.Stdout, blockVerbUsage("rebase", rebaseForm))
			return 0
		}
	}
	tree, before := blockRebaseTree(args)
	var stdout bytes.Buffer
	stderr := &closeErrorBuffer{}
	code := rebaseStep(args, &stdout, stderr)
	if code != rebaseUnported {
		crumb := gatesBreadcrumbName("block rebase")
		fmt.Fprint(os.Stdout, stdout.String())
		fmt.Fprint(os.Stderr, stderr.String())
		if code != 0 {
			var message any
			if stderr.last != "" {
				message = stderr.last
			}
			nativeVerbFailure("block", "rebase", args, code, message, crumb)
			return code
		}
		if tree == "" {
			return 0
		}
		return blockRebaseRebuild(args[0], tree, before)
	}
	// B449: only the guarded run states rebaseStep names stay with Node.
	if tree == "" {
		return blockHandover(full, hBlockUnportedSubcommand)
	}
	node := os.Getenv("CODE_FORGE_NODE")
	if node == "" {
		node = "node"
	}
	nodePath, err := exec.LookPath(node)
	if err != nil {
		return blockHandover(full, hBlockUnportedSubcommand)
	}
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		return blockHandover(full, hBlockUnportedSubcommand)
	}
	recordHandover(append([]string{"block"}, full...), handoverReasons[hBlockUnportedSubcommand])
	script := filepath.Join(filepath.Dir(executable), "..", "..", "..", "bin", "code-forge.mjs")
	cmd := exec.Command(nodePath, append([]string{script, "block"}, full...)...)
	cmd.Args[0] = node
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	// The child shares the terminal; keep this process alive until it ends.
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native: exec node:", err)
		return 126
	}
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-signals:
				if s == syscall.SIGTERM || s == syscall.SIGHUP {
					_ = cmd.Process.Signal(s)
				}
			case <-done:
				return
			}
		}
	}()
	err = cmd.Wait()
	close(done)
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			if status, ok := exit.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				return 128 + int(status.Signal())
			}
			return exit.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "code-forge-native: node:", err)
		return 1
	}
	return blockRebaseRebuild(args[0], tree, before)
}

// blockRebaseRebuild rebuilds the worktree's native binary when the rebase
// brought native source changes (B408).
func blockRebaseRebuild(id, tree, before string) int {
	after, err := gitx.Run(tree, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return 0
	}
	rebuilt, err := nativeRebuildAfterMove(tree, before, strings.TrimSpace(after))
	if err != nil {
		fmt.Fprintf(os.Stderr, "block rebase: block %s: %s; the rebase stands; rebuild by hand: node scripts/build-native.mjs --host in %s\n", id, strings.ReplaceAll(err.Error(), "\n", " "), tree)
		return 1
	}
	if rebuilt {
		fmt.Fprintf(os.Stderr, "block rebase: block %s: rebuilt the native binary in %s (the rebase changed native sources)\n", id, tree)
	}
	return 0
}

// blockRebaseTree finds the worktree of `block rebase <id> --run <r>` in a
// code-forge checkout and its HEAD, or "" when any of that is unknown (the
// plain hand-over then reports every error as before).
func blockRebaseTree(args []string) (tree, head string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return "", ""
	}
	run, given, ok := gatesFlag(args, "--run")
	if !given || !ok || run == "" {
		return "", ""
	}
	home := os.Getenv("HOME")
	if home == "" {
		return "", ""
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return "", ""
	}
	worktree, _ := gatesObj(gatesObj(record.Get("blocks")).Get(args[0])).Get("worktree").(ledger.Object)
	path, _ := worktree.Get("path").(string)
	if path == "" || !filepath.IsAbs(path) || !nativeRepo(path) {
		return "", ""
	}
	out, err := gitx.Run(path, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", ""
	}
	return path, strings.TrimSpace(out)
}
