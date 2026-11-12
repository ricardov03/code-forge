package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/session"
)

// B477: the session part of `code-forge spawn` in Go (runSpawn after the
// usage checks): previewArgv for --print-argv, spawnSession otherwise, the
// result line without `text`, and exit 0 ok / 3 unavailable / 1 otherwise.

// spawnFindingSchema is the --schema value Go serves: JSON null (no schema) or
// finding.schema.json itself (nil, false = hand over).
func spawnFindingSchema(data []byte) (ledger.Object, bool) {
	if string(bytes.TrimSpace(data)) == "null" {
		return nil, true
	}
	given, err := ledger.DecodeObject(data)
	if err != nil {
		return nil, false
	}
	finding, err := session.LoadFindingSchema()
	if err != nil {
		return nil, false
	}
	a, err1 := ledger.Stringify(given, false)
	b, err2 := ledger.Stringify(finding, false)
	if err1 != nil || err2 != nil || !bytes.Equal(a, b) {
		return nil, false
	}
	return given, true
}

// spawnRunRoot is runRootFor(--run, cfg.tmp.root) for a run id that passed
// the id rule; false = a case Go leaves to Node (a tmp.root that is not a
// string, or a root runRoot refuses).
func spawnRunRoot(cwd, run string) (string, bool) {
	cfg, err := config.Load(cwd)
	if err != nil {
		return "", false
	}
	var root *string
	if tmp, ok := cfg["tmp"].(map[string]any); ok {
		if value, present := tmp["root"]; present && value != nil {
			text, isString := value.(string)
			if !isString {
				return "", false
			}
			root = &text
		}
	}
	dir, err := contextRunRoot(run, root)
	if err != nil {
		return "", false
	}
	return dir, true
}

// spawnSessionPlan plans the session; runRoot is the --run's root ("" = none).
func spawnSessionPlan(cwd string, parsed cliParsed, runRoot string) (func(o *runOut) int, handoverReason) {
	if parsed.booleans["background"] {
		return nil, hSpawnSession
	}
	if _, ok := parsed.values["run"]; ok && runRoot == "" {
		return nil, hSpawnSession
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return nil, hSpawnSession
	}
	brief := spawnResolve(cwd, parsed.values["brief"])
	if _, err := os.ReadFile(brief); err != nil {
		return nil, hSpawnSession // Node throws reading the packet (a stack trace)
	}
	opts := session.Opts{Cfg: cfg, Level: parsed.values["level"], Role: parsed.values["role"], PromptPath: brief,
		Slug: session.SlugFor(cfg, config.ProjectRootFor(cwd))}
	if raw, ok := parsed.values["timeout"]; ok {
		n, _ := cliIntFlag(raw, "timeout")
		opts.TimeoutMs = n * 1000
	}
	if block, ok := parsed.values["block"]; ok {
		opts.Block = &block
	}
	if run, ok := parsed.values["run"]; ok {
		opts.Run, opts.RunRoot = &run, runRoot
	}
	if file, ok := parsed.values["schema"]; ok {
		data, err := os.ReadFile(spawnResolve(cwd, file))
		if err != nil {
			return nil, hSpawnSession
		}
		schema, ok := spawnFindingSchema(data)
		if !ok {
			return nil, hSpawnSession
		}
		opts.Schema = schema
	}
	if err := session.Check(opts); session.IsUnsupported(err) {
		return nil, hSpawnSession
	}
	preview := parsed.booleans["print-argv"]
	return func(o *runOut) int { return spawnSessionRun(o, opts, preview) }, 0
}

func spawnSessionRun(o *runOut, opts session.Opts, preview bool) int {
	var procRoot string
	// currentRunRoot: the process's lazy root, made once
	lazyRoot := func() (string, error) {
		if procRoot != "" {
			return procRoot, nil
		}
		contextApplyTmpRoot()
		dir, err := contextRunRoot("proc-"+strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixMilli(), 10), nil)
		procRoot = dir
		return dir, err
	}
	runID, hasRunID := os.LookupEnv("CODE_FORGE_RUN_ID")
	deps := session.Deps{Stderr: spawnStderr{o}, RunID: runID, Warn: spawnLogWarning, RunRoot: lazyRoot}
	if opts.Run != nil {
		// exec.mjs: the child's CODE_FORGE_RUN_ID is the env's, else the --run
		// argument; its pid entry goes in the process's lazy root
		if !hasRunID {
			deps.RunID = *opts.Run
		}
		deps.PidsRoot = lazyRoot
	}
	defer func() {
		if procRoot != "" {
			spawnReleaseRoot(procRoot)
		}
	}()
	var res ledger.Object
	var err error
	if preview {
		res, err = session.Preview(opts, deps)
	} else {
		res, err = session.Spawn(opts, deps)
	}
	var sessionErr *session.Error
	if errors.As(err, &sessionErr) {
		o.err("spawn: " + sessionErr.Message + "\n")
		return 2
	}
	if err != nil {
		// a throw in Node: the launcher's crash line (Node adds the stack)
		o.err(fmt.Sprintf("code-forge: verb \"spawn\" failed: Error: %s\n", err.Error()))
		return 1
	}
	if !preview {
		res = res.Clone()
		res.Delete("text")
	}
	line, err := ledger.Stringify(res, false)
	if err != nil {
		o.err(fmt.Sprintf("code-forge: verb \"spawn\" failed: Error: %s\n", err.Error()))
		return 1
	}
	o.out(string(line) + "\n")
	if preview {
		return 0
	}
	switch res.Get("status") {
	case "ok", "started":
		return 0
	case "unavailable":
		return 3
	}
	return 1
}

// spawnReleaseRoot is tmp.mjs releaseLazyRoot: the lazy root goes when its
// pid registry holds no entry (every child this process started has exited
// and removed its entry).
func spawnReleaseRoot(dir string) {
	entries, err := os.ReadDir(filepath.Join(dir, "pids"))
	if err != nil && !os.IsNotExist(err) {
		return
	}
	if len(entries) > 0 {
		return
	}
	_ = os.RemoveAll(dir)
}

// spawnStderr sends the session's stderr lines through runOut, so the error
// log's message is the last line written, as Node's captureStderr records it.
type spawnStderr struct{ o *runOut }

func (w spawnStderr) Write(p []byte) (int, error) {
	w.o.err(string(p))
	return len(p), nil
}
