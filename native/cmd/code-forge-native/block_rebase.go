package main

import (
	"bytes"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B408: block rebase stays in Node, but Go runs it as a child instead of
// replacing itself, so that a rebase that brings native changes into a
// code-forge worktree rebuilds that worktree's native binary afterwards.
// B431: the plain rebase of an open worktree block runs in Go (rebase.RunPlain):
// owned files main changed are replayed instead of refused up front.
// B578: no Node child any more; the guarded run states run in Go too.
func init() { blockSubs["rebase"] = healWrap([]string{"block", "rebase"}, runBlockRebase) }

// rebaseUserHome is the user database's home folder (a seam for tests).
var rebaseUserHome = func() string {
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

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
	// B578: Node's os.homedir() reads the user database when HOME is unset.
	if os.Getenv("HOME") == "" {
		if home := rebaseUserHome(); home != "" {
			_ = os.Setenv("HOME", home)
		}
	}
	tree, before := blockRebaseTree(args)
	var stdout bytes.Buffer
	stderr := &closeErrorBuffer{}
	code, reason := rebaseStep(args, &stdout, stderr)
	if reason != 0 {
		// B578: a narrow guarded state Go does not serve yet; nothing of the
		// rebase was kept, and Node runs the whole command. The expiry lines
		// Go already printed are not printed again by Node (its checks find
		// nothing left to do).
		fmt.Fprint(os.Stderr, stderr.String())
		return blockHandover(full, reason)
	}
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
