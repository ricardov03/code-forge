package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/healq"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B430: main first. `block land --push` (or config land.push) pushes main
// after the merge; a rejected push becomes a heal item instead of a silent
// local-only main. `--watch-ci` starts a detached `ci watch` and returns.

// healQueue queues one open item through healq. Inside a heal retry
// (healq.RetryEnv set) it queues nothing: the runner updates its own item, so
// a retry that fails again never adds a second one. It returns the item id.
func healQueue(home, run, block, kind string, argv []string, cwd, detail string) (string, error) {
	if id := os.Getenv(healq.RetryEnv); id != "" {
		return id, nil
	}
	if run == "" {
		return "", fmt.Errorf("heal: no run id (pass --run)")
	}
	item, err := healq.Enqueue(home, run, block, kind, argv, cwd, detail, time.Now())
	return item.ID, err
}

// landRetryArgv is the code-forge verb argv a heal retry runs for a land
// whose push failed: land again with --push; already landed blocks only push.
func landRetryArgv(run string, ids []string) []string {
	argv := append([]string{"block", "land"}, ids...)
	return append(argv, "--run", run, "--merge", "--push")
}

// landPushWanted: the --push flag, else land.push from the run-start config.
func landPushWanted(flags map[string]any, cfg map[string]any) bool {
	return flags["push"] == true || blockMap(cfg["land"])["push"] == true
}

// landPushCheck refuses flag combinations before anything runs; "" is fine.
func landPushCheck(flags map[string]any, record ledger.Object, batch bool) string {
	push, watch := flags["push"] == true, flags["watch-ci"] == true
	if !batch && (push || watch) && flags["merge"] != true {
		return "--push and --watch-ci need --merge"
	}
	cfg, _ := blockSnapshot(record)
	if watch && !landPushWanted(flags, cfg) {
		return "--watch-ci needs --push or land.push: true"
	}
	return ""
}

type landPushResult struct {
	sha, log string
}

// landPush pushes base from main to origin. A failed push appends a heal item
// of kind land (argv: the push to retry) and returns an error naming its id.
func landPush(home, run string, ids []string, main, base string, watch bool, print io.Writer) (landPushResult, error) {
	result := landPushResult{}
	sha, err := gitx.Run(main, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return result, fmt.Errorf("cannot resolve main HEAD before push")
	}
	result.sha = strings.TrimSpace(sha)
	if output, err := gitx.Run(main, "push", "origin", base); err != nil {
		detail := strings.TrimSpace(landFirst(landGitError(err, ""), output, err.Error()))
		id, herr := healQueue(home, run, strings.Join(ids, ","), healq.KindLand, landRetryArgv(run, ids), main, "push of "+base+" at "+result.sha+" rejected: "+detail)
		if herr != nil {
			return result, fmt.Errorf("merged into local %s but the push failed: %s; heal item not written: %v", base, detail, herr)
		}
		return result, fmt.Errorf("merged into local %s but the push failed: %s\nheal: queued %s", base, detail, id)
	}
	fmt.Fprintf(print, "pushed %s to origin/%s\n", result.sha, base)
	if watch {
		log, err := ciWatchSpawn(home, run, result.sha, main)
		if err != nil {
			return result, fmt.Errorf("pushed %s but ci watch did not start: %v", result.sha, err)
		}
		result.log = log
		fmt.Fprintf(print, "ci watch: started for %s; log %s\n", result.sha, log)
	}
	return result, nil
}

// landSelf is the binary that serves `ci watch`; tests replace it.
var landSelf = os.Executable

// ciWatchSpawn starts `ci watch` as a detached child (own session) logging to
// <home>/.code-forge/heal/ci-<sha>.log and returns without waiting for CI.
func ciWatchSpawn(home, run, sha, cwd string) (string, error) {
	if err := os.MkdirAll(healq.Dir(home), 0o700); err != nil {
		return "", err
	}
	log := filepath.Join(healq.Dir(home), "ci-"+sha+".log")
	out, err := os.OpenFile(log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	defer out.Close()
	self, err := landSelf()
	if err != nil {
		return "", err
	}
	args := []string{"ci", "watch", "--sha", sha, "--cwd", cwd}
	if run != "" {
		args = append(args, "--run", run)
	}
	cmd := exec.Command(self, args...)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	// Reap the child if this process outlives it; land itself never waits.
	go func() { _ = cmd.Wait() }()
	return log, nil
}

// landBatchPush runs after a batch released its block and run locks: when push is wanted
// and at least one of ids landed, it pushes the base branch once.
func landBatchPush(flags map[string]any, home, run string, ids []string) (string, error) {
	record, err := state.ReadRun(home, run)
	if err != nil {
		return "", err
	}
	cfg, _ := blockSnapshot(record)
	if !landPushWanted(flags, cfg) {
		return "", nil
	}
	blocks := blockObject(record.Get("blocks"))
	landed, base := []string{}, ""
	for _, id := range ids {
		entry := blockObject(blocks.Get(id))
		if entry != nil && entry.Get("landed") != nil {
			landed = append(landed, id)
			base = blockString(entry.Get("base_branch"))
		}
	}
	if len(landed) == 0 {
		return "", nil
	}
	main, err := gitx.RepoTop(blockString(record.Get("workspace")))
	if err != nil {
		return landed[0], err
	}
	_, err = landPush(home, run, landed, main, base, flags["watch-ci"] == true, os.Stderr)
	return landed[0], err
}

// landPushOnly is the heal retry path: push is wanted and every named block
// has already landed, so land only pushes main (0 pushed, 1 failed).
func landPushOnly(flags map[string]any, home, run string, record ledger.Object, ids []string) (bool, int) {
	cfg, _ := blockSnapshot(record)
	if !landPushWanted(flags, cfg) {
		return false, 0
	}
	blocks := blockObject(record.Get("blocks"))
	for _, id := range ids {
		entry := blockObject(blocks.Get(id))
		if entry == nil || entry.Get("landed") == nil {
			return false, 0
		}
	}
	fmt.Fprintf(os.Stderr, "block land: %s already landed; pushing main only\n", strings.Join(ids, ","))
	if _, err := landBatchPush(flags, home, run, ids); err != nil {
		fmt.Fprintln(os.Stderr, "block land: "+err.Error())
		return true, 1
	}
	return true, 0
}
