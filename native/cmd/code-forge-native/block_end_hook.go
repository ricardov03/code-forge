package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/blockend"
	"github.com/ricardov03/code-forge/native/blockops"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// Land still uses the original predicate; claim/stop/close use blockEndSafe.
func blockSafe(record ledger.Object) bool {
	if !blockEndSafe(record) {
		return false
	}
	cfg, _ := blockSnapshot(record)
	return blockMap(cfg["hooks"])["on_block_end"] == nil
}

// Called only after publication releases the run lock. The hook itself runs
// last, even when a later output or worktree removal returns an error.
func blockRunEndHook(home, run, id, status, doneFile string, record ledger.Object, stderr io.Writer) {
	cfg, _ := blockSnapshot(record)
	hook := blockend.FromConfig(cfg)
	if hook == nil {
		return
	}
	cwd := blockString(record.Get("workspace"))
	if top, err := gitx.RepoTop(cwd); err == nil {
		cwd = top
	}
	env := []string{}
	for _, pair := range os.Environ() {
		key, _, _ := strings.Cut(pair, "=")
		if key != "CODE_FORGE_RUN" && key != "CODE_FORGE_BLOCK" && key != "CODE_FORGE_BLOCK_STATUS" && key != "CODE_FORGE_DONE_FILE" {
			env = append(env, pair)
		}
	}
	env = append(env, "CODE_FORGE_RUN="+run, "CODE_FORGE_BLOCK="+id, "CODE_FORGE_BLOCK_STATUS="+status, "CODE_FORGE_DONE_FILE="+doneFile)
	ran := blockend.Run(hook, cwd, env)
	if warning := ran.Warning(); warning != "" {
		fmt.Fprintln(stderr, "warning: on_block_end hook", warning)
	}
	row := ledger.Object{{Key: "event", Value: "hook.ran"}, {Key: "block", Value: id}, {Key: "hook", Value: "on_block_end"},
		{Key: "argv0", Value: filepath.Base(hook.Argv[0])}, {Key: "exit", Value: ran.Exit}, {Key: "signal", Value: ran.Signal},
		{Key: "timed_out", Value: ran.TimedOut}, {Key: "duration_ms", Value: ran.DurationMS}}
	err := state.WithRunLock(home, run, func() error {
		_, err := state.WriteSigned(home, run, row)
		return err
	})
	if err != nil {
		fmt.Fprintf(stderr, "warning: on_block_end hook row not written (%s)\n", blockend.ErrorCode(err))
	}
}

func blockHasEndHook(record ledger.Object) bool {
	cfg, _ := blockSnapshot(record)
	return blockend.FromConfig(cfg) != nil
}

func blockDoneJSON(out *ledger.Object, record ledger.Object, doneFile string) {
	if !blockHasEndHook(record) {
		return
	}
	var file any
	if doneFile != "" {
		file = doneFile
	}
	out.Set("done_file", file)
}

// External review waives only local approval checks. Scope, transcript, late
// findings and proof blockers remain, and no reviewed lines are emitted.
func closeExternalReviewer(cfg map[string]any) string {
	external := blockMap(blockMap(cfg["review"])["external"])
	if external["required"] != true {
		return ""
	}
	return strings.Trim(blockString(external["name"]), "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff")
}

// Close's automatic claims can admit the end hook without widening land's
// guard. Its read-only enqueue checks remain shared with landAutoClaim.
func closeAutoClaim(home, run, id, top string, record ledger.Object, files []string, print io.Writer, preflight bool) error {
	cfg, _ := blockSnapshot(record)
	if preflight || blockMap(cfg["hooks"])["on_block_end"] == nil {
		return landAutoClaim(home, run, id, top, record, files, print, preflight)
	}
	why := ""
	if closeExternalReviewer(cfg) != "" {
		why = "review.external.required is true"
	} else if blockMap(cfg["proof"])["runners"] != nil {
		why = "proof.runners is set"
	}
	guarded, err := blockops.Guarded(record, why)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := blockops.ClaimAuto(home, run, id, file, guarded, why, func(r ledger.Object) bool { return blockEndSafe(r) && blockStorageSafe(home, run, r) }, true); err != nil {
			return err
		}
		fmt.Fprintf(print, "block %s auto-claimed %s\n", id, file)
		result := landCapture(func(s *landStep) int {
			return reviewEnqueueStep([]string{file, "--block", id, "--run", run, "--cwd", top}, s)
		}, nil)
		if result.code != 0 {
			return fmt.Errorf("%s", landFirst(result.error, result.output, "auto-claim review failed"))
		}
	}
	return nil
}
