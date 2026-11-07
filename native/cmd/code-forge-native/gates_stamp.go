package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

func gatesCleanTree(cwd string) string {
	status, err := gitx.Run(cwd, "status", "--porcelain", "--untracked-files=all")
	if err != nil || status != "" {
		return ""
	}
	tree, err := gitx.Run(cwd, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(tree)
}

// Match the existing gate.done full_stamp against this exact trial tree and its
// resolved commands. Legacy, partial and unsuccessful runs fail closed.
func gatesReusableFull(cwd string) bool {
	loaded, err := config.Load(cwd)
	if err != nil {
		return false
	}
	configured, _ := loaded["gates"].(map[string]any)
	p, safe := gates.Resolve(cwd, configured)
	if !safe || !gates.ResolveCustom(&p, configured) || p.Commands["test"] == nil {
		return false
	}
	block := ledger.Object{{Key: "worktree", Value: ledger.Object{{Key: "path", Value: cwd}}}}
	stamp := gatesFullStamp(gates.TreeStamp(cwd), p, configured, block)
	if stamp == "" {
		return false
	}
	project, _ := loaded["project"].(map[string]any)
	slug, _ := project["slug"].(string)
	if slug == "" {
		return false
	}
	rows, err := ledger.ReadAllRows(filepath.Join(os.Getenv("HOME"), ".code-forge", "ledger"), slug)
	if err != nil {
		return false
	}
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if !ok || row["event"] != "gate.done" || row["all_ok"] != true || row["scope"] != "full" || row["cancelled"] == true || row["flaky"] != nil {
			continue
		}
		results, _ := row["results"].([]any)
		tests, green := 0, true
		for _, value := range results {
			r, ok := value.(map[string]any)
			if !ok || r["ok"] != true || r["flaky"] != nil || r["timed_out"] == true || r["cancelled"] == true {
				green = false
				break
			}
			if r["gate"] == "test" {
				green = green && r["skipped"] == false && r["full_stamp"] == stamp
				tests++
			}
		}
		if green && tests == 1 {
			return true
		}
	}
	return false
}

// Keep the resolved configuration in the existing full_stamp record. Worktree
// stamps use version 3 to bind an exact clean Git tree, including docs and
// release versions. Legacy normalized stamps cannot authorize merge-gate reuse.
// Canonical JSON avoids YAML map order and environment sorting differences.
func gatesFullStamp(tree string, p gates.Plan, configured map[string]any, block ledger.Object) string {
	if tree == "" || reviewWaitNodeVersion == "" || reviewWaitNodeVersion == "unknown" {
		return ""
	}
	path := blockString(blockObject(block.Get("worktree")).Get("path"))
	exactTree := ""
	if path != "" {
		exactTree = gatesCleanTree(path)
		if exactTree == "" {
			return ""
		}
	}
	planned := map[string]any{"stack": p.Stack}
	for _, name := range gates.Order {
		if value, present := configured[name]; present {
			planned[name] = value
			continue
		}
		var value any
		if command := p.Commands[name]; command != nil {
			value = command.Argv
			if command.FailOnStdout {
				value = map[string]any{"argv": command.Argv, "failOnStdout": true}
			}
		}
		planned[name] = value
	}
	if configured != nil {
		planned["rerun_failed"] = 1
		for _, name := range []string{"custom", "rerun_failed"} {
			if value, present := configured[name]; present {
				planned[name] = value
			}
		}
	}
	if p.Restricted {
		planned["kindGates"] = map[string]any{"kind": p.Kind, "names": p.Names}
	}
	if p.Repeat != nil {
		repeat := map[string]any{"n": p.Repeat.N, "base": p.Repeat.Base, "owned": block.Get("owned_files")}
		if kinds := block.Get("owned_kinds"); kinds != nil {
			repeat["kinds"] = kinds
		}
		planned["repeatNewTests"] = repeat
	}
	environment := map[string]string{}
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if !gates.Has([]string{"CODE_FORGE_RUN_ID", "CODE_FORGE_OWNER_PID", "CODE_FORGE_OWNER_START_TIME", "CODE_FORGE_RUN", "CODE_FORGE_BLOCK"}, key) {
			environment[key] = value
		}
	}
	input := []any{"gate-tree-v2", tree, planned, environment, reviewWaitNodeVersion}
	if exactTree != "" {
		input = []any{"gate-tree-v3", exactTree, tree, planned, environment, reviewWaitNodeVersion}
	}
	data, err := canon.Marshal(input)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
