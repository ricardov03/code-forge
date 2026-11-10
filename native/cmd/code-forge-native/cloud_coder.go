package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/state"
)

// B342: `code-forge cloud-coder --run <r> --block <id>` hands a block to a
// cloud coder through the coder.cloud submit/fetch argv templates, then lands
// the returned patch in the block worktree as one commit. Reviews and spend go
// through the existing Node verbs (review-file, ledger add coder), started the
// same way as a handover (CODE_FORGE_NODE and the package's bin/code-forge.mjs).
func init() { routes["cloud-coder"] = runCloudCoder }

const cloudCoderUsage = "usage: code-forge cloud-coder --run <r> --block <id>\n"

var (
	cloudTaskID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,199}$`)
	cloudUSD    = regexp.MustCompile(`^usd[=:]\s*([0-9]+(?:\.[0-9]+)?)\s*$`)
)

func runCloudCoder(args []string) int {
	flags := map[string]string{}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			fmt.Fprint(os.Stdout, cloudCoderUsage)
			return 0
		case "--run", "--block":
			name := strings.TrimPrefix(args[i], "--")
			if _, seen := flags[name]; seen {
				return cloudUsage("--" + name + " given more than once")
			}
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				return cloudUsage("--" + name + " needs a value")
			}
			flags[name] = args[i+1]
			i++
		default:
			return cloudUsage("unknown argument " + quoteArg(args[i]))
		}
	}
	run, block := flags["run"], flags["block"]
	if run == "" || block == "" {
		return cloudUsage("--run and --block are required")
	}
	if !reviewBlockID.MatchString(block) {
		return cloudUsage("block id must match /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/")
	}
	if !reviewBlockRun.MatchString(run) {
		return cloudUsage("run id must match /^[a-z0-9][a-z0-9-]{0,63}$/")
	}
	return cloudCoder(run, block)
}

func cloudUsage(message string) int {
	fmt.Fprint(os.Stderr, "cloud-coder: "+message+"\n"+cloudCoderUsage)
	return 2
}

func quoteArg(s string) string {
	if len(s) > 40 {
		s = s[:40] + "..."
	}
	return fmt.Sprintf("%q", s)
}

func cloudSay(out ledger.Object, code int) int {
	data, err := ledger.Stringify(out, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cloud-coder:", err)
		return 1
	}
	fmt.Fprintln(os.Stdout, string(data))
	if message, ok := out.Get("message").(string); ok && code != 0 {
		fmt.Fprintln(os.Stderr, "cloud-coder: "+message)
	}
	return code
}

func cloudFail(block, status, reason, message string) int {
	return cloudSay(ledger.Object{{Key: "block", Value: block}, {Key: "status", Value: status}, {Key: "reason", Value: reason}, {Key: "message", Value: message}}, 1)
}

func cloudCoder(run, block string) int {
	started := time.Now()
	home := os.Getenv("HOME")
	record, err := state.ReadRun(home, run)
	if err != nil {
		return cloudFail(block, "refused", "no-run", err.Error())
	}
	blocks, _ := record.Get("blocks").(ledger.Object)
	entry, _ := blocks.Get(block).(ledger.Object)
	if entry == nil {
		return cloudFail(block, "refused", "no-block", fmt.Sprintf("run %s has no block %s", run, block))
	}
	worktree, _ := entry.Get("worktree").(ledger.Object)
	tree := blockString(worktree.Get("path"))
	if tree == "" {
		return cloudFail(block, "refused", "no-worktree", fmt.Sprintf("block %s has no worktree; open it with a worktree first", block))
	}
	workspace := blockString(record.Get("workspace"))
	cfg, err := config.Load(workspace)
	if err != nil {
		return cloudFail(block, "refused", "config", err.Error())
	}
	cloud, err := config.CloudCoderFrom(cfg)
	if err != nil {
		return cloudFail(block, "refused", "config", err.Error())
	}
	if cloud == nil {
		return cloudFail(block, "refused", "no-cloud-config", "set coder.cloud.submit and coder.cloud.fetch in .code-forge.yml")
	}
	if dirty, err := gitx.Run(tree, "status", "--porcelain"); err != nil || strings.TrimSpace(dirty) != "" {
		return cloudFail(block, "refused", "dirty-worktree", "the block worktree has uncommitted changes; commit or remove them first")
	}
	brief := ""
	if b, ok := entry.Get("brief").(ledger.Object); ok {
		brief = blockString(b.Get("path"))
		if brief != "" && !filepath.IsAbs(brief) {
			brief = filepath.Join(workspace, brief)
		}
	}
	scratch, err := os.MkdirTemp("", "code-forge-cloud-")
	if err != nil {
		return cloudFail(block, "failed", "temp", err.Error())
	}
	defer os.RemoveAll(scratch)
	values := map[string]string{"block": block, "base": blockString(entry.Get("base_sha")), "brief": brief, "patch_out": filepath.Join(scratch, "cloud.patch")}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cloud.TimeoutS*float64(time.Second)))
	defer cancel()
	out, code, err := cloudExec(ctx, tree, cloud.Submit, values)
	if ctx.Err() != nil {
		return cloudFail(block, "timeout", "submit-timeout", fmt.Sprintf("submit did not finish within %gs", cloud.TimeoutS))
	}
	if err != nil || code != 0 {
		return cloudFail(block, "failed", "submit-failed", fmt.Sprintf("submit exited %d", code))
	}
	task := ""
	for _, line := range strings.Split(out, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			task = line
			break
		}
	}
	if !cloudTaskID.MatchString(task) {
		return cloudFail(block, "failed", "no-task-id", "submit must print a task id (letters, digits, . _ : / -) on its first line")
	}
	values["task"] = task

	usd, fetched := "", false
	for {
		out, code, err = cloudExec(ctx, tree, cloud.Fetch, values)
		if ctx.Err() != nil {
			break
		}
		if err != nil || code != 0 {
			return cloudFail(block, "failed", "fetch-failed", fmt.Sprintf("fetch for task %s exited %d", task, code))
		}
		if info, statErr := os.Stat(values["patch_out"]); statErr == nil && info.Size() > 0 {
			for _, line := range strings.Split(out, "\n") {
				if m := cloudUSD.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
					usd = m[1]
				}
			}
			fetched = true
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(cloud.PollS * float64(time.Second))):
		}
		if ctx.Err() != nil {
			break
		}
	}
	if !fetched {
		return cloudFail(block, "timeout", "fetch-timeout", fmt.Sprintf("no patch for task %s within %gs (coder.cloud.timeout_s)", task, cloud.TimeoutS))
	}

	files, err := cloudPatchFiles(tree, values["patch_out"])
	if err != nil {
		return cloudFail(block, "refused", "bad-patch", err.Error())
	}
	paths, _ := entry.Get("owned_files").([]any)
	kinds, _ := entry.Get("owned_kinds").(ledger.Object)
	var unowned []string
	for _, file := range files {
		if !owned.OwnsFile(paths, kinds, file) {
			unowned = append(unowned, file)
		}
	}
	if len(unowned) > 0 {
		out := ledger.Object{{Key: "block", Value: block}, {Key: "status", Value: "refused"}, {Key: "reason", Value: "unowned-file"}, {Key: "task", Value: task}, {Key: "unowned", Value: stringsAny(unowned)},
			{Key: "message", Value: fmt.Sprintf("the patch touches %d file(s) block %s does not own (%s); nothing was applied", len(unowned), block, strings.Join(unowned, ", "))}}
		return cloudSay(out, 1)
	}
	if _, err := gitx.Run(tree, "apply", "--3way", values["patch_out"]); err != nil {
		_, _ = gitx.Run(tree, "reset", "-q", "--hard", "HEAD")
		return cloudFail(block, "refused", "apply-failed", fmt.Sprintf("git apply --3way failed for task %s; the worktree was reset", task))
	}
	if _, err := gitx.Run(tree, append([]string{"commit", "-q", "-m", block + ": cloud coder patch", "--"}, files...)...); err != nil {
		_, _ = gitx.Run(tree, "reset", "-q", "--hard", "HEAD")
		return cloudFail(block, "failed", "commit-failed", "git commit failed; the worktree was reset")
	}
	head, _ := gitx.Run(tree, "rev-parse", "HEAD")
	elapsed := time.Since(started)

	reviews := []any{}
	failed := 0
	for _, file := range files {
		if _, err := os.Lstat(filepath.Join(tree, file)); err != nil {
			continue // a deleted file has nothing to review
		}
		stdout, code := cloudNode(tree, "review-file", file, "--block", block, "--run", run)
		row := ledger.Object{{Key: "file", Value: file}, {Key: "exit", Value: float64(code)}}
		if result, err := ledger.DecodeObject([]byte(strings.TrimSpace(stdout))); err == nil {
			if ticket, ok := result.Get("ticket").(string); ok {
				row.Set("ticket", ticket)
			}
		}
		if code != 0 {
			failed++
		}
		reviews = append(reviews, row)
	}
	result := ledger.Object{{Key: "block", Value: block}, {Key: "status", Value: "landed"}, {Key: "task", Value: task}, {Key: "commit", Value: strings.TrimSpace(head)}, {Key: "files", Value: stringsAny(files)}, {Key: "reviews", Value: reviews}, {Key: "elapsed_ms", Value: float64(elapsed.Milliseconds())}}
	if usd != "" {
		_, code := cloudNode(tree, "ledger", "add", "coder", "--run", run, "--block", block, "--usd", usd, "--note", "cloud coder task "+task)
		result.Set("usd", usd)
		result.Set("spend_recorded", code == 0)
		if code != 0 {
			failed++
		}
	}
	if failed > 0 {
		result.Set("message", fmt.Sprintf("the patch landed, but %d review/spend step(s) failed; rerun them by hand", failed))
		return cloudSay(result, 1)
	}
	return cloudSay(result, 0)
}

// cloudExec runs one argv template with its placeholders filled in. It runs in
// the block worktree with no stdin, and is killed when ctx ends.
func cloudExec(ctx context.Context, dir string, template []string, values map[string]string) (string, int, error) {
	// One pass, so a value that holds a placeholder is never rewritten.
	var pairs []string
	for _, key := range []string{"block", "base", "brief", "task", "patch_out"} {
		if value, ok := values[key]; ok {
			pairs = append(pairs, "{"+key+"}", value)
		}
	}
	replacer := strings.NewReplacer(pairs...)
	argv := make([]string, len(template))
	for i, arg := range template {
		argv[i] = replacer.Replace(arg)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.WaitDelay = time.Second
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.String(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return stdout.String(), -1, err
	}
	return stdout.String(), 0, nil
}

// cloudPatchFiles lists every path a patch touches (both sides of a rename),
// as git itself reads the patch.
func cloudPatchFiles(tree, patch string) ([]string, error) {
	out, err := gitx.Run(tree, "apply", "--numstat", "-z", patch)
	if err != nil {
		return nil, fmt.Errorf("git cannot read the patch")
	}
	seen := map[string]bool{}
	tokens := strings.Split(out, "\x00")
	for i := 0; i < len(tokens); i++ {
		fields := strings.SplitN(tokens[i], "\t", 3)
		if len(fields) != 3 {
			continue
		}
		names := []string{fields[2]}
		if fields[2] == "" && i+2 < len(tokens) {
			names = []string{tokens[i+1], tokens[i+2]}
			i += 2
		}
		for _, name := range names {
			if name == "" || filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "/../") {
				return nil, fmt.Errorf("the patch names a path outside the worktree")
			}
			seen[name] = true
		}
	}
	if len(seen) == 0 {
		return nil, fmt.Errorf("the patch changes no file")
	}
	files := make([]string, 0, len(seen))
	for name := range seen {
		files = append(files, name)
	}
	sort.Strings(files)
	return files, nil
}

// cloudNode runs a Node-only verb the way a handover does, and returns its
// stdout and exit code (-1 when it could not start).
func cloudNode(dir string, args ...string) (string, int) {
	node := os.Getenv("CODE_FORGE_NODE")
	if node == "" {
		node = "node"
	}
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.EvalSymlinks(executable)
	}
	if err != nil {
		return "", -1
	}
	script := filepath.Join(filepath.Dir(executable), "..", "..", "..", "bin", "code-forge.mjs")
	cmd := exec.Command(node, append([]string{script}, args...)...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.String(), exitErr.ExitCode()
	}
	if err != nil {
		return stdout.String(), -1
	}
	return stdout.String(), 0
}

func stringsAny(list []string) []any {
	out := make([]any, len(list))
	for i, s := range list {
		out[i] = s
	}
	return out
}
