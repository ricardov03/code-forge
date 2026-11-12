package main

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/contextpack"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/session"
	"github.com/ricardov03/code-forge/native/state"
)

// B549: the coder parts of `code-forge spawn` (src/cli/spawn.mjs
// checkRunSandbox, src/session/spawn.mjs coderPromptFor). Every case Go
// cannot answer byte-equal hands over before anything is written.

// spawnCodexLadder: the coder or facts session's ladder has a Codex (openai)
// step, which stays with Node (hSpawnCodexStep).
func spawnCodexLadder(cwd string, parsed cliParsed) bool {
	role := parsed.values["role"]
	if role != "coder" && role != "facts" {
		return false
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return false
	}
	placeholder := ""
	o := session.Opts{Cfg: cfg, Level: parsed.values["level"], Role: role, PromptPath: "/brief", Background: parsed.booleans["background"], CoderPrompt: &placeholder}
	return session.IsCodexStep(session.Check(o))
}

// spawnCheckRunSandbox is checkRunSandbox for `--role coder --run <id>`:
// (0, "", 0) = go on; a code = the refusal's exit and text; a reason = hand over.
func spawnCheckRunSandbox(cwd, runID string) (int, string, handoverReason) {
	record, err := state.ReadRun(os.Getenv("HOME"), runID)
	if err != nil {
		var stateErr *state.Error
		if errors.As(err, &stateErr) && stateErr.Code == "no-run" {
			return 0, "", 0
		}
		return 0, "", hSpawnCoderRun
	}
	snapshot, isSnapshot, valid := sweepConfigInForce(record)
	if !valid {
		if id, ok := record.Get("run_id").(string); ok {
			return 2, "spawn: run " + id + ": the config snapshot in force does not match its hash\n", 0
		}
		return 0, "", hSpawnCoderRun
	}
	if !isSnapshot {
		return 0, "", 0
	}
	var pinned any
	if coder, ok := snapshot.Get("coder").(ledger.Object); ok {
		pinned = runDeepMap(coder.Get("sandbox"))
	}
	var live any
	cfg, err := config.Load(cwd)
	if err != nil {
		return 0, "", hSpawnCoderRun
	}
	if coder, ok := cfg["coder"].(map[string]any); ok {
		live = coder["sandbox"]
	}
	a, errA := canon.Marshal(pinned)
	b, errB := canon.Marshal(live)
	if errA != nil || errB != nil {
		return 0, "", hSpawnCoderRun
	}
	if string(a) != string(b) {
		return 1, "spawn: sandbox-changed: coder.sandbox in the live config differs from the run " + runID + " snapshot — put it back, or end the run and start a new one\n", 0
	}
	return 0, "", 0
}

// spawnObjectProto: the Object.prototype members a block id can name.
var spawnObjectProto = []string{"constructor", "hasOwnProperty", "isPrototypeOf", "propertyIsEnumerable", "toLocaleString", "toString", "valueOf"}

// spawnCoderPrompt is coderPromptFor(opts) for a coder with --block: the
// prompt text, or its usage refusal (returned after the budget gate, as Node
// computes it there); a reason = a case Go leaves to Node.
func spawnCoderPrompt(brief []byte, block string, run *string) (*string, error, handoverReason) {
	if !reviewBlockID.MatchString(block) {
		return nil, &session.Error{Code: "usage", Message: "block id must match /^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$/"}, 0
	}
	original := contextpack.DecodeUTF8(brief)
	task := []string{}
	if run != nil {
		record, err := state.ReadRun(os.Getenv("HOME"), *run)
		if err != nil {
			var stateErr *state.Error
			if !errors.As(err, &stateErr) || stateErr.Code != "no-run" {
				return nil, nil, hSpawnCoderRun
			}
			record = nil
		}
		if record != nil {
			blocks, _ := record.Get("blocks").(ledger.Object)
			if blocks != nil && !runHas(blocks, block) && cliContains(spawnObjectProto, block) {
				return nil, nil, hSpawnCoderRun // an inherited Object.prototype member
			}
			raw := blocks.Get(block)
			if !runTruthy(raw) {
				if _, isObject := record.Get("blocks").(ledger.Object); !isObject && record.Get("blocks") != nil {
					return nil, nil, hSpawnCoderRun // a property read Node may answer otherwise
				}
				return nil, &session.Error{Code: "usage", Message: "run " + *run + " has no block " + block}, 0
			}
			entry, isObject := raw.(ledger.Object)
			if !isObject {
				return nil, nil, hSpawnCoderRun
			}
			workspace, isString := record.Get("workspace").(string)
			if !isString {
				return nil, nil, hSpawnCoderRun
			}
			root, refusal, ok := reviewMainRoot(context.Background(), workspace)
			if !ok || refusal != nil {
				return nil, nil, hSpawnCoderRun
			}
			lines, ok, err := contextpack.BlockTaskLines(root, entry, original)
			var packErr *contextpack.Error
			if errors.As(err, &packErr) {
				return nil, &session.Error{Code: "usage", Message: packErr.Message}, 0
			}
			if err != nil || !ok {
				return nil, nil, hSpawnCoderRun
			}
			task = lines
		}
	}
	text := contextpack.TrimEnd(original) + "\n\n"
	if len(task) > 0 {
		text += strings.Join(task, "\n") + "\n\n"
	}
	text += "Report the changed files and pass/fail counts. Print TESTS <command> exit=<n> on its own line before the sentinel. On success, print this exact sentinel as your last line:\n===BLOCK " + block + " COMPLETE===\n"
	return &text, nil, 0
}
