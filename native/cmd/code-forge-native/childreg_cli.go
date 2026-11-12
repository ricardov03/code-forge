package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/ricardov03/code-forge/native/childreg"
	"github.com/ricardov03/code-forge/native/filelock"
)

// childregGoOnly are the verbs Node hands to this binary whole (spawnSync of
// the native binary in src/cli/<verb>.mjs): they have no exec.mjs children.
var childregGoOnly = map[string]bool{"ci": true, "cloud-coder": true, "docs": true, "decisions": true, "heal": true}

// childregInstall is exec.mjs for the whole CLI process (B573): every child
// a verb spawns through gitx, runChild or the childreg helpers is recorded in
// this process's run root (pids/<pid>.json) and gets CODE_FORGE_OWNER_PID,
// its start time and CODE_FORGE_RUN_ID. Verbs Node runs as a whole Go binary
// and the internal accelerators (except the daemon, internal serve) keep the
// plain spawns.
func childregInstall(args []string) {
	if len(args) == 0 || childregGoOnly[args[0]] {
		return
	}
	if args[0] == "internal" && (len(args) < 2 || args[1] != "serve") {
		return
	}
	contextApplyTmpRoot() // bin/code-forge.mjs applyTmpRoot, before any verb runs
	childregArgs = args
	childreg.Started = lrRegisterChild
	childreg.Env = childregOwnerEnv
}

// childregArgs is process.argv after the verb (exec.mjs reads --run there).
var childregArgs []string

var childregStart struct {
	once  sync.Once
	value *string
}

// childregOwnerEnv is exec.mjs's child environment: the owner pid and start
// time (dropped when ps cannot tell it), and the run id from the
// environment, the command line (--run X / --run=X) or an r- run root.
func childregOwnerEnv(environ []string) []string {
	childregStart.once.Do(func() { childregStart.value = filelock.ProcessStartTime(os.Getpid()) })
	runID, hasRunID := "", false
	env := make([]string, 0, len(environ)+3)
	for _, kv := range environ {
		switch {
		case strings.HasPrefix(kv, "CODE_FORGE_OWNER_PID="), strings.HasPrefix(kv, "CODE_FORGE_OWNER_START_TIME="):
			continue
		case strings.HasPrefix(kv, "CODE_FORGE_RUN_ID="):
			runID, hasRunID = strings.TrimPrefix(kv, "CODE_FORGE_RUN_ID="), true
		}
		env = append(env, kv)
	}
	env = append(env, "CODE_FORGE_OWNER_PID="+strconv.Itoa(os.Getpid()))
	if childregStart.value != nil {
		env = append(env, "CODE_FORGE_OWNER_START_TIME="+*childregStart.value)
	}
	root, err := lrProcRoot() // pidsDir(): the root is made before the spawn
	if err != nil || hasRunID {
		return env
	}
	if value, ok := os.LookupEnv("CODE_FORGE_RUN_ID"); ok {
		runID, hasRunID = value, true
	} else if value, ok := childregRunArg(childregArgs); ok {
		runID, hasRunID = value, true
	} else if base := filepath.Base(root); childregRunRoot.MatchString(base) {
		runID, hasRunID = base, true
	}
	if hasRunID && runID != "" {
		env = append(env, "CODE_FORGE_RUN_ID="+runID)
	}
	return env
}

var childregRunRoot = regexp.MustCompile(`^r-`)

// childregRunArg is exec.mjs's `--run X` (first --run) else `--run=X`.
func childregRunArg(args []string) (string, bool) {
	for i, arg := range args {
		if arg == "--run" {
			if i+1 < len(args) {
				return args[i+1], true
			}
			return "", false
		}
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "--run=") {
			return strings.TrimPrefix(arg, "--run="), true
		}
	}
	return "", false
}

// childregRelease is tmp.mjs releaseLazyRoot at exit: each lazy root
// (proc-<pid>-<ms>) this process made goes when no child entry is left in
// it; a run root set by the verb (the worker's setRunRoot) stays.
func childregRelease() {
	lrMu.Lock()
	roots := append([]string(nil), lrLazyRoots...)
	lrMu.Unlock()
	for _, root := range roots {
		spawnReleaseRoot(root)
	}
}
