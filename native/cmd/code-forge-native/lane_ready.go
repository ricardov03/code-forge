package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/proof"
	"github.com/ricardov03/code-forge/native/state"
)

type laneVerifiedBlock struct {
	id, last string
	files    []string
	entry    ledger.Object
}
type laneVerified struct {
	run, workspace, base, head, branch string
	blocks                             []*laneVerifiedBlock
}

var laneCommitID = regexp.MustCompile(`^(B[0-9]+[a-z]?)[: ]`)

// Verify committed bytes, not the current review queue or an unsigned report.
// The signed close supplies the ordinary acceptance/gate checks; the lane
// checks additionally bind every commit to its block and latest review.
// Coders close before committing (the normal block workflow), so close time
// is not an upper bound on commit time. Committed content must still match
// its approved review hash, even when the commit follows the signed close.
func laneVerify(home, run string) (*laneVerified, error) {
	record, err := state.ReadRun(home, run)
	if err != nil {
		return nil, err
	}
	policy := blockObject(record.Get("lane"))
	lane := &laneVerified{run: run, workspace: blockString(record.Get("workspace")), base: blockString(policy.Get("base")), branch: blockString(policy.Get("branch"))}
	if lane.base == "" || lane.branch == "" {
		return nil, fmt.Errorf("run %s is not a pinned lane", run)
	}
	top, err := gitx.RepoTop(lane.workspace)
	if err != nil {
		return nil, err
	}
	if proof.Real(top) != proof.Real(lane.workspace) {
		return nil, fmt.Errorf("lane workspace must be the repository root")
	}
	branch, err := gitx.Run(top, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil || strings.TrimSpace(branch) != lane.branch {
		return nil, fmt.Errorf("lane branch changed")
	}
	status, err := gitx.Run(top, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(status) != "" {
		return nil, fmt.Errorf("lane worktree is dirty")
	}
	head, err := gitx.Run(top, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	lane.head = strings.TrimSpace(head)
	if _, err := gitx.Run(top, "merge-base", "--is-ancestor", lane.base, lane.head); err != nil {
		return nil, fmt.Errorf("lane no longer descends from its pinned base")
	}
	big, err := landBigFiles(top, landBigFileDefaultMB*1000000, lane.head, lane.base)
	if err != nil {
		return nil, err
	}
	if len(big) > 0 {
		return nil, fmt.Errorf("lane size exceeds %d MB: %s", landBigFileDefaultMB, big[0].path)
	}
	blocks := blockObject(record.Get("blocks"))
	if len(blocks) < 1 || len(blocks) > 2 {
		return nil, fmt.Errorf("lane must contain one or two closed blocks")
	}
	values, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), blockString(record.Get("project")))
	if err != nil {
		return nil, err
	}
	key, err := blockclose.ReadKey(home, run)
	if err != nil {
		return nil, err
	}
	rows := []gatecheck.Row{}
	for _, value := range values {
		r, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid lane ledger row")
		}
		if r["run"] == run {
			rows = append(rows, r)
		}
	}
	commits, err := gitx.Run(top, "log", "--reverse", "--format=%H%x09%cI%x09%s", lane.base+".."+lane.head)
	if err != nil {
		return nil, err
	}
	byID := map[string]*laneVerifiedBlock{}
	for _, line := range strings.Split(strings.TrimSpace(commits), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) != 3 {
			return nil, fmt.Errorf("lane has no block commits")
		}
		match := laneCommitID.FindStringSubmatch(fields[2])
		if match == nil {
			return nil, fmt.Errorf("commit %s has no block id", fields[0])
		}
		id := match[1]
		entry := blockObject(blocks.Get(id))
		if entry.Get("status") != "closed" {
			return nil, fmt.Errorf("block %s must be closed", id)
		}
		opened, e1 := time.Parse(time.RFC3339Nano, blockString(entry.Get("opened_at")))
		committed, e2 := time.Parse(time.RFC3339, fields[1])
		// Git commit dates have second precision; accept the open in that second.
		if e1 != nil || e2 != nil || opened.Unix() > committed.Unix() {
			return nil, fmt.Errorf("block %s must open before its first commit", id)
		}
		b := byID[id]
		if b == nil {
			b = &laneVerifiedBlock{id: id, entry: entry}
			byID[id] = b
			lane.blocks = append(lane.blocks, b)
		}
		b.last = fields[0]
		parents, err := gitx.Run(top, "rev-list", "--parents", "-n", "1", b.last)
		if err != nil {
			return nil, err
		}
		if len(strings.Fields(parents)) != 2 {
			return nil, fmt.Errorf("lane block commits cannot be merges")
		}
		changed, err := gitx.Run(top, "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", b.last)
		if err != nil {
			return nil, err
		}
		for _, file := range strings.Split(changed, "\x00") {
			if file == "" {
				continue
			}
			if !gatecheck.Owns("", entry, file) {
				return nil, fmt.Errorf("block %s changed %s which is not owned", id, file)
			}
			if !gates.Has(b.files, file) {
				b.files = append(b.files, file)
			}
		}
	}
	if len(byID) != len(blocks) {
		return nil, fmt.Errorf("every lane block must have a commit")
	}
	for _, b := range lane.blocks {
		dispatched, closed := false, false
		for _, row := range rows {
			if row["block"] != b.id {
				continue
			}
			if row["event"] == "dispatch" && gatecheck.Verified(row, key) {
				dispatched = true
			}
			if row["event"] == "block.close" && gatecheck.Verified(row, key) {
				closed = true
			}
		}
		if !dispatched || !closed {
			return nil, fmt.Errorf("block %s needs signed dispatch and close evidence", b.id)
		}
		for _, file := range b.files {
			exists, err := gitx.Run(top, "ls-tree", "--name-only", b.last, "--", file)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(exists) == "" {
				continue
			}
			body, err := gitx.Run(top, "show", b.last+":"+file)
			if err != nil {
				return nil, err
			}
			hash := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
			approved, result := false, false
			for _, row := range rows {
				if row["block"] != b.id || row["file"] != file || row["content_hash"] != hash {
					continue
				}
				switch row["event"] {
				case "review.approved":
					approved = gatecheck.Verified(row, key)
				case "review.result":
					result = gatecheck.Verified(row, key) && row["approved"] == true
				}
			}
			checked := gatecheck.Check(run, b.id, []gatecheck.File{{File: file, Hash: hash}}, rows, key, nil, nil, false)
			if !approved || !result || checked.Unknown || len(checked.Blockers) > 0 {
				return nil, fmt.Errorf("block %s: %s needs approved signed review of committed content", b.id, file)
			}
		}
	}
	return lane, nil
}

func runLaneIntegration(args []string) int {
	sub := args[0]
	usage := laneReadyUsage
	if sub == "combine" {
		usage = laneCombineUsage
	}
	fail := func(err error, code int) int { fmt.Fprintf(safeStderr, "lane %s: %v\n", sub, err); return code }
	run, json, ids := "", false, []string{}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--help", "-h":
			fmt.Fprint(safeStdout, usage)
			return 0
		case "--run":
			if run != "" || i+1 == len(args) {
				return fail(fmt.Errorf("%s", strings.TrimSpace(usage)), 2)
			}
			i++
			run = args[i]
		case "--json":
			if sub != "ready" || json {
				return fail(fmt.Errorf("%s", strings.TrimSpace(usage)), 2)
			}
			json = true
		default:
			if strings.HasPrefix(args[i], "-") || sub != "combine" {
				return fail(fmt.Errorf("unknown argument %s", args[i]), 2)
			}
			ids = append(ids, args[i])
		}
	}
	if run == "" || sub == "combine" && len(ids) == 0 {
		return fail(fmt.Errorf("%s", strings.TrimSpace(usage)), 2)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fail(err, 1)
	}
	if sub == "combine" {
		if err := laneCombine(home, run, ids); err != nil {
			return fail(err, 1)
		}
		return 0
	}
	lane, err := laneVerify(home, run)
	if json {
		row := ledger.Object{{Key: "run", Value: run}, {Key: "ready", Value: err == nil}}
		if err == nil {
			row.Set("head", lane.head)
			row.Set("base", lane.base)
		} else {
			row.Set("reason", err.Error())
		}
		data, _ := ledger.Stringify(row, false)
		fmt.Fprintln(safeStdout, string(data))
	}
	if err != nil {
		return fail(err, 1)
	}
	if !json {
		fmt.Fprintf(safeStdout, "lane %s ready at %s (%d blocks)\n", run, lane.head, len(lane.blocks))
	}
	return 0
}
