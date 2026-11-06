package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strings"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/ledger"
)

// Use the oracle's resolved gate configuration, not the Go execution plan, so
// green full-suite evidence can be exchanged between engines. Canonical JSON
// avoids depending on YAML map order or locale-specific environment sorting.
func gatesFullStamp(tree string, p gates.Plan, configured map[string]any, block ledger.Object) string {
	if tree == "" || reviewWaitNodeVersion == "" || reviewWaitNodeVersion == "unknown" {
		return ""
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
	data, err := canon.Marshal([]any{"gate-tree-v2", tree, planned, environment, reviewWaitNodeVersion})
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
