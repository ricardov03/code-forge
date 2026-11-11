package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B430: main first. `block land --push` (or config land.push) pushes main
// after the merge; a rejected push becomes a heal item instead of a silent
// local-only main. `--watch-ci` starts a detached `ci watch` and returns.

// healItem is one line of <home>/.code-forge/heal/<run>.jsonl, the shape the
// B429 heal queue reads.
type healItem struct {
	ID     string            `json:"id"`
	Run    string            `json:"run"`
	Block  string            `json:"block"`
	Kind   string            `json:"kind"`
	Argv   []string          `json:"argv"`
	Detail string            `json:"detail"`
	Tries  int               `json:"tries"`
	Status string            `json:"status"`
	Times  map[string]string `json:"times"`
}

var healRunName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func healDir(home string) string { return filepath.Join(home, ".code-forge", "heal") }

// healFile names the queue file of run; an empty run uses no-run.jsonl.
func healFile(home, run string) (string, error) {
	name := "no-run"
	if run != "" {
		if !healRunName.MatchString(run) {
			return "", fmt.Errorf("heal: bad run id %q", run)
		}
		name = run
	}
	return filepath.Join(healDir(home), name+".jsonl"), nil
}

// healAppend appends one open item and returns its id. The line is written
// with one O_APPEND write, so concurrent appenders never interleave a line.
func healAppend(home, run, block, kind string, argv []string, detail string) (string, error) {
	file, err := healFile(home, run)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return "", err
	}
	random := make([]byte, 3)
	_, _ = rand.Read(random)
	now := time.Now().UTC()
	item := healItem{
		ID: "h-" + now.Format("20060102-150405") + "-" + hex.EncodeToString(random), Run: run, Block: block, Kind: kind,
		Argv: argv, Detail: detail, Tries: 0, Status: "open", Times: map[string]string{"created": now.Format(time.RFC3339)},
	}
	line, err := json.Marshal(item)
	if err != nil {
		return "", err
	}
	out, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", err
	}
	_, werr := out.Write(append(line, '\n'))
	cerr := out.Close()
	if werr != nil {
		return "", werr
	}
	return item.ID, cerr
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
func landPush(home, run, block, main, base string, watch bool, print io.Writer) (landPushResult, error) {
	result := landPushResult{}
	sha, err := gitx.Run(main, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return result, fmt.Errorf("cannot resolve main HEAD before push")
	}
	result.sha = strings.TrimSpace(sha)
	argv := []string{"git", "-C", main, "push", "origin", base}
	if output, err := gitx.Run(main, "push", "origin", base); err != nil {
		detail := strings.TrimSpace(landFirst(landGitError(err, ""), output, err.Error()))
		id, herr := healAppend(home, run, block, "land", argv, "push of "+base+" at "+result.sha+" rejected: "+detail)
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
	if err := os.MkdirAll(healDir(home), 0o700); err != nil {
		return "", err
	}
	log := filepath.Join(healDir(home), "ci-"+sha+".log")
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
	_, err = landPush(home, run, strings.Join(landed, ","), main, base, flags["watch-ci"] == true, os.Stderr)
	return landed[0], err
}
