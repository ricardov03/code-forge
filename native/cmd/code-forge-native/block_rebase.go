package main

import (
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
	"github.com/ricardov03/code-forge/native/rebase"
	"github.com/ricardov03/code-forge/native/state"
)

// B408: block rebase stays in Node, but Go runs it as a child instead of
// replacing itself, so that a rebase that brings native changes into a
// code-forge worktree rebuilds that worktree's native binary afterwards.
// B431: the plain rebase of an open worktree block runs in Go (rebase.RunPlain):
// owned files main changed are replayed instead of refused up front.
func init() { blockSubs["rebase"] = healWrap([]string{"block", "rebase"}, runBlockRebase) }

// blockRebaseNative runs `block rebase <id> --run <r> [--json]` of an open
// worktree block natively. handled is false when anything else is asked (a
// closed block, --head, --accept-changed, --accept-landed, a guarded run
// state) and Node keeps that path.
func blockRebaseNative(args []string) (code int, handled bool) {
	flags, positionals, message := blockFlags(args)
	if message != "" || len(positionals) != 1 || positionals[0] == "" {
		return 0, false
	}
	for name := range flags {
		if name != "run" && name != "json" {
			return 0, false
		}
	}
	run, id := blockString(flags["run"]), positionals[0]
	home := os.Getenv("HOME")
	if run == "" || home == "" {
		return 0, false
	}
	record, err := state.ReadRun(home, run)
	if err != nil {
		return 0, false
	}
	block := blockObject(blockObject(record.Get("blocks")).Get(id))
	if blockString(block.Get("status")) != "open" || blockObject(block.Get("worktree")) == nil || blockString(block.Get("base_branch")) == "" {
		return 0, false
	}
	if !blockGrantEndSafe(record) || !blockStorageSafe(home, run, record) {
		return 0, false
	}
	blockGrantExpiry(home, run, os.Stderr)
	result, err := rebase.RunPlain(home, run, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "block rebase: %s\n", strings.Join(strings.Fields(err.Error()), " "))
		return 1, true
	}
	blockBreadcrumb("rebase")
	if flags["json"] == true {
		out := ledger.Object{{Key: "block", Value: id}, {Key: "event", Value: "rebased"}, {Key: "old_base", Value: result.OldBase}, {Key: "new_base", Value: result.NewBase}}
		if len(result.Upstream) > 0 {
			files := []any{}
			for _, file := range result.Upstream {
				files = append(files, file)
			}
			out.Set("upstream_owned", files)
		}
		data, err := ledger.Stringify(out, false)
		if err != nil {
			return 1, true
		}
		fmt.Fprintln(os.Stdout, string(data))
		return 0, true
	}
	fmt.Fprintf(os.Stdout, "block %s rebased %s → %s\n", id, short12(result.OldBase), short12(result.NewBase))
	if len(result.Upstream) > 0 {
		fmt.Fprintf(os.Stdout, "upstream_owned: %s (main changed these owned files; queued for re-review)\n", rebase.FileList(result.Upstream))
	}
	return 0, true
}

func short12(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func runBlockRebase(args []string) int {
	full := append([]string{"rebase"}, args...)
	tree, before := blockRebaseTree(args)
	if code, handled := blockRebaseNative(args); handled {
		if code != 0 || tree == "" {
			return code
		}
		return blockRebaseRebuild(args[0], tree, before)
	}
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
