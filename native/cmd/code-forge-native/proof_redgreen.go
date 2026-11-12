package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/proof"
	"github.com/ricardov03/code-forge/native/state"
)

// B481: `proof red-green` runs in Go, as src/cli/proof.mjs with
// src/proof/red-green.mjs, red-parse.mjs and util/exec.mjs: the block file
// set, the export and its signed row, the journal, the revert from a
// temporary index or the assertion deletion, the filtered node:test command
// (RED, then GREEN), the restore, the signed red-green row and the JSON.
// A configured proof.runners list and the Pest default runner hand over
// (proof-runner).

var (
	proofTestPath     = regexp.MustCompile(`(?:^|/)(?:test|tests|__tests__|spec)/|\.(?:test|spec)\.[cm]?[jt]sx?$|Test\.php$`)
	proofNodeTestFile = regexp.MustCompile(`\.(?:mjs|cjs|js)$`)
	proofRegexMeta    = regexp.MustCompile(`[.*+?^${}()|[\]\\]`)
)

// proofTestTimeout is DEFAULT_TEST_TIMEOUT_MS (a seam for tests).
var proofTestTimeout = 600 * time.Second

const proofKillGrace = 2 * time.Second
const proofMaxBuffer = 10 * 1024 * 1024

// proofRG is one red-green call, checked up to its first side effect.
type proofRG struct {
	home, runID, blockID, file, label, mechanism, isolation string
	record, block                                           ledger.Object
	dir, base, tree                                         string
	baseValue                                               any
	argv, sources                                           []string
	setup                                                   [][]string
	linkDirs, copyUntracked                                 []string
	exp                                                     *proofExported
}

// proofRGFail is the refusal of an error Node words the same way; anything
// else hands over (nothing has been changed yet).
func proofRGFail(err error) (*proofRG, *proofOutcome, handoverReason) {
	var ge *gitx.Error
	if errors.As(err, &ge) {
		return nil, proofRefuse("red-green", ge.Message, 1), 0
	}
	out, reason := proofFail("red-green", err)
	if out == nil {
		reason = hProofRedGreen
	}
	return nil, out, reason
}

// proofRedGreenPlan does every check Node does before the export is built.
func proofRedGreenPlan(runID, blockID, file, testCase string, hasCase bool, mechanism string) (*proofRG, *proofOutcome, handoverReason) {
	const sub = "red-green"
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, nil, hProofRedGreen
	}
	record, err := state.ReadRun(home, runID)
	if err != nil {
		return proofRGFail(err)
	}
	block := proofBlock(record, blockID)
	if block == nil || block.Get("status") != "open" {
		return nil, proofRefuse(sub, "block "+blockID+" is not open in run "+runID, 1), 0
	}
	workspace, ok := record.Get("workspace").(string)
	if !ok {
		return nil, nil, hProofRedGreen
	}
	rg := &proofRG{home: home, runID: runID, blockID: blockID, file: file, mechanism: mechanism, record: record, block: block, dir: workspace}
	rg.baseValue = block.Get("base_sha")
	wt, err := proof.BlockTree(workspace, block)
	if err != nil {
		var gitErr *gitx.CommandError
		if errors.As(err, &gitErr) {
			return nil, nil, hProofRedGreen
		}
		return nil, proofRefuse(sub, fmt.Sprintf("block %s of run %s: %s; no proof was run", blockID, runID, err.Error()), 1), 0
	}
	if wt != nil {
		rg.dir, rg.tree = wt.Workspace, wt.Path
		rg.baseValue = nil
		if object, ok := block.Get("worktree").(ledger.Object); ok {
			rg.baseValue = object.Get("base_sha")
		}
	}
	rg.base, _ = rg.baseValue.(string)

	// proofRunners: the run snapshot's proof.runners.
	snapshot, ok := blockSnapshot(record)
	if !ok {
		return nil, nil, hProofSnapshot
	}
	if runners, present := jsObject(snapshot["proof"])["runners"]; present && runners != nil {
		list, isList := runners.([]any)
		if !isList {
			return nil, proofRefuse(sub, fmt.Sprintf("run %s: proof.runners in the run snapshot is not a list", proofJSText(record.Get("run_id"))), 1), 0
		}
		if len(list) == 0 {
			return nil, proofRefuse(sub, fmt.Sprintf("run %s: proof.runners in the run snapshot is an empty list", proofJSText(record.Get("run_id"))), 1), 0
		}
		return nil, nil, hProofRunner
	}
	if strings.HasSuffix(file, ".php") && proofHasPest(rg.dir) {
		return nil, nil, hProofRunner
	}
	if !proofNodeTestFile.MatchString(file) {
		return nil, proofRefuse(sub, "only node:test files (.mjs, .cjs, .js) have a filtered test command in this version — add a proof.runners entry for it (see proof.md §2)", 1), 0
	}
	node, err := contextNode()
	if err != nil {
		return nil, nil, hProofRedGreen
	}
	rg.argv = []string{node, "--test"}
	if hasCase {
		rg.argv = append(rg.argv, "--test-name-pattern", "^"+proofRegexMeta.ReplaceAllString(testCase, `\$0`)+"$")
	}
	rg.argv = append(rg.argv, file)
	rg.label = file
	if hasCase {
		rg.label = file + "::" + testCase
	}
	if refusal := proofAssertTestFile(rg.dir, file); refusal != nil {
		return nil, refusal, 0
	}
	cfg, refusal, handover := proofConfigAt(workspace)
	if handover != 0 {
		return nil, nil, handover
	}
	if refusal != "" {
		return nil, proofRefuse(sub, refusal, 1), 0
	}
	rg.isolation = "export"
	if rg.tree == "" && jsObject(cfg["proof"])["isolation"] == "lock" {
		rg.isolation = "lock"
	}
	if rg.isolation == "lock" {
		holder, _ := record.Get("proof_lock").(ledger.Object)
		if holder.Get("block") != blockID {
			return nil, proofRefuse(sub, "proof.isolation is lock — run code-forge proof lock "+blockID+" --run "+runID+" first", 1), 0
		}
	}

	// blockFileSet: the owned changed files, minus ones deleted after base
	// that base never had.
	ownedList, ok := block.Get("owned_files").([]any)
	kinds, okKinds := block.Get("owned_kinds").(ledger.Object)
	if !ok || (!okKinds && block.Get("owned_kinds") != nil) {
		return nil, nil, hProofRedGreen
	}
	changed, err := gitx.ChangedFiles(rg.dir, rg.base)
	if err != nil {
		return proofRGFail(err)
	}
	rg.sources = []string{}
	for _, f := range changed {
		if !owned.OwnsFile(ownedList, kinds, f) {
			continue
		}
		hash, err := gitx.ContentHash(rg.dir, f)
		if err != nil {
			return nil, nil, hProofRedGreen
		}
		if hash == gitx.DeletedHash {
			if listed, err := gitx.Run(rg.dir, "ls-tree", "--name-only", "-z", rg.base, "--", f); err == nil && listed == "" {
				if history, err := gitx.Run(rg.dir, "log", "--format=%H", rg.base+"..HEAD", "--", f); err == nil && strings.TrimSpace(history) != "" {
					continue
				}
			}
		}
		if mechanism != "assertion-deletion" && f != file && !proofTestPath.MatchString(f) {
			rg.sources = append(rg.sources, f)
		}
	}
	if mechanism == "revert" && len(rg.sources) == 0 {
		return nil, proofRefuse(sub, "block "+blockID+" changed no non-test source file to revert — use --mechanism assertion-deletion", 2), 0
	}
	if rg.isolation == "export" {
		exportCfg := jsObject(jsObject(cfg["proof"])["export"])
		var okLink, okCopy bool
		rg.linkDirs, okLink = proofStrings(exportCfg["link_dirs"], proofDefaultLinkDirs)
		rg.copyUntracked, okCopy = proofStrings(exportCfg["copy_untracked"], proofDefaultCopyUntracked)
		if !okLink || !okCopy {
			return nil, nil, hProofRedGreen
		}
		if value := jsObject(jsObject(cfg["proof"])["worktree"])["setup"]; value != nil {
			list, ok := value.([]any)
			if !ok {
				return nil, nil, hProofRedGreen
			}
			for _, item := range list {
				argv, ok := proofStrings(item, nil)
				if !ok || len(argv) == 0 {
					return nil, nil, hProofRedGreen
				}
				rg.setup = append(rg.setup, argv)
			}
		}
	}
	return rg, nil, 0
}

// proofHasPest is defaultPestRunner's detection: an artisan file, a
// vendor/bin/pest file, or pestphp/pest in composer.json.
func proofHasPest(dir string) bool {
	isFile := func(rel string) bool {
		info, err := os.Stat(filepath.Join(dir, rel))
		return err == nil && info.Mode().IsRegular()
	}
	if isFile("artisan") || isFile("vendor/bin/pest") {
		return true
	}
	data, err := os.ReadFile(filepath.Join(dir, "composer.json"))
	if err != nil {
		return false
	}
	var composer map[string]any
	if json.Unmarshal(data, &composer) != nil {
		return false
	}
	for _, key := range []string{"require", "require-dev"} {
		if section, ok := composer[key].(map[string]any); ok {
			if _, ok := section["pestphp/pest"].(string); ok {
				return true
			}
		}
	}
	return false
}

func proofAssertTestFile(dir, file string) *proofOutcome {
	if info, err := os.Lstat(filepath.Join(dir, file)); err != nil || !info.Mode().IsRegular() {
		return proofRefuse("red-green", "--test "+file+" is not a file in "+dir, 2)
	}
	return nil
}

// export builds the measurement export and signs its row (exportAndRecord).
// An error before the row hands over: Node rebuilds the export from scratch.
func (rg *proofRG) export() (*proofOutcome, handoverReason) {
	exportRoot := ""
	if rg.tree != "" {
		exportDir, err := proofExportDir(rg.dir, rg.blockID)
		if workspace, ok := rg.record.Get("workspace").(string); ok {
			exportDir, err = proofExportDir(workspace, rg.blockID)
		}
		if err != nil {
			_, out, reason := proofRGFail(err)
			return out, reason
		}
		exportRoot = filepath.Dir(exportDir)
	}
	exp, err := proofBuildExport(rg.dir, rg.blockID, rg.baseValue, rg.block.Get("owned_files"), rg.block.Get("owned_kinds"), rg.linkDirs, rg.copyUntracked, exportRoot)
	if err != nil {
		_, out, reason := proofRGFail(err)
		return out, reason
	}
	fields := ledger.Object{{Key: "step", Value: "export"}, {Key: "base_sha", Value: exp.base}, {Key: "restored", Value: proofList(exp.restored)}, {Key: "untracked", Value: proofList(exp.untracked)}}
	if rg.tree != "" {
		fields = append(fields, ledger.Field{Key: "worktree", Value: rg.tree})
	}
	if err := proofRecord(rg.home, rg.runID, rg.blockID, "export", fields); err != nil {
		var se *state.Error
		if errors.As(err, &se) {
			return proofRefuse("red-green", se.Message, 1), 0
		}
		return proofRefuse("red-green", err.Error(), 1), 0
	}
	rg.exp = exp
	return nil, 0
}

// proofRGResult is what runRedGreen returns.
type proofRGResult struct {
	mechanism  string
	red        *proofRedParse
	green      any
	proven     bool
	setup      bool
	outputTail string
}

// run is the rest of the verb after the export: the red→green proof, its
// signed row and the JSON. Errors here are refusals (exit 2 for usage).
func (rg *proofRG) run(stdout, stderr io.Writer) int {
	fail := func(err error) int {
		code := 1
		var pe *proofError
		if errors.As(err, &pe) && pe.code == "usage" {
			code = 2
		}
		message := err.Error()
		var se *state.Error
		if errors.As(err, &se) {
			message = se.Message
		}
		fmt.Fprint(stderr, "proof red-green: "+message+"\n")
		return code
	}
	workDir := rg.dir
	if rg.exp != nil {
		workDir = rg.exp.dir
		if refusal := proofAssertTestFile(workDir, rg.file); refusal != nil {
			fmt.Fprint(stderr, refusal.stderr)
			return refusal.code
		}
	}
	result, err := rg.redGreen(workDir, stderr)
	if err != nil {
		return fail(err)
	}
	coverList := []string{rg.file}
	if result.mechanism == "revert" {
		coverList = []string{}
		seen := map[string]bool{}
		for _, s := range rg.sources {
			p := posixNormalize(s)
			if !seen[p] {
				seen[p] = true
				coverList = append(coverList, p)
			}
		}
		gitx.SortStrings(coverList)
	}
	var redVerdict, redKind, failed any
	if result.red != nil {
		redVerdict, redKind, failed = result.red.verdict, result.red.kind, result.red.failed
	}
	proven := result.proven && result.red != nil
	fields := ledger.Object{
		{Key: "step", Value: "red-green"}, {Key: "test", Value: rg.label}, {Key: "mechanism", Value: result.mechanism},
		{Key: "red_kind", Value: redKind}, {Key: "red", Value: redVerdict}, {Key: "green", Value: result.green},
		{Key: "proven", Value: proven}, {Key: "failed", Value: failed}, {Key: "covers", Value: proofList(coverList)},
	}
	if result.setup {
		fields = append(fields, ledger.Field{Key: "reason", Value: "setup"}, ledger.Field{Key: "output_tail", Value: result.outputTail})
	}
	if err := proofRecord(rg.home, rg.runID, rg.blockID, rg.isolation, fields); err != nil {
		return fail(err)
	}
	out := ledger.Object{
		{Key: "block", Value: rg.blockID}, {Key: "test", Value: rg.label}, {Key: "mechanism", Value: result.mechanism},
		{Key: "isolation", Value: rg.isolation}, {Key: "red", Value: redVerdict}, {Key: "red_kind", Value: redKind},
		{Key: "green", Value: result.green}, {Key: "proven", Value: proven},
	}
	if result.setup {
		out = append(out, ledger.Field{Key: "reason", Value: "setup"}, ledger.Field{Key: "output_tail", Value: result.outputTail})
	}
	if rg.exp != nil {
		out = append(out, ledger.Field{Key: "export", Value: ledger.Object{
			{Key: "dir", Value: rg.exp.dir}, {Key: "restored", Value: proofList(rg.exp.restored)}, {Key: "untracked", Value: proofList(rg.exp.untracked)},
		}})
	}
	fmt.Fprint(stdout, proofJSON(out))
	if proven {
		return 0
	}
	return 1
}

// redGreen is runRedGreen without a record (the CLI writes the row).
func (rg *proofRG) redGreen(workDir string, stderr io.Writer) (*proofRGResult, error) {
	if err := proofRelPath(rg.file, "test.file"); err != nil {
		return nil, err
	}
	for _, s := range rg.sources {
		if err := proofRelPath(s, "sources"); err != nil {
			return nil, err
		}
	}
	if rg.base == "" || strings.HasPrefix(rg.base, "-") {
		return nil, &proofError{"bad-ref", "red-green: base must be a commit"}
	}
	label := rg.label
	mechanism := "revert"
	if rg.mechanism == "assertion-deletion" || len(rg.sources) == 0 {
		mechanism = "assertion-deletion"
	}
	journal, err := proofReadJournal(workDir)
	if err != nil {
		return nil, err
	}
	if journal != nil {
		return nil, &proofError{"journal-present", "a red→green journal exists in " + workDir + " — an earlier run was interrupted; run code-forge proof restore first"}
	}
	env := proofTestEnv()
	for _, argv := range rg.setup {
		res := proofExec(argv, workDir, env, rg.runID, proofTestTimeout)
		if res.ok() {
			continue
		}
		tail := jsTrim(res.stdout + "\n" + res.stderr + "\n" + res.err)
		if units := utf16.Encode([]rune(tail)); len(units) > 4000 {
			tail = string(utf16.Decode(units[len(units)-4000:]))
		}
		fmt.Fprint(stderr, "SETUP_FAILED "+label+"\n"+tail+"\n")
		return &proofRGResult{mechanism: mechanism, setup: true, outputTail: tail}, nil
	}

	type redFile struct {
		rel   string
		bytes []byte
	}
	redState := []redFile{}
	if mechanism == "revert" {
		files := []string{}
		seen := map[string]bool{}
		for _, s := range rg.sources {
			if !seen[s] {
				seen[s] = true
				files = append(files, s)
			}
		}
		gitx.SortStrings(files)
		base, err := proofReadBaseFiles(rg.dir, rg.base, files)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			redState = append(redState, redFile{f, base[f]})
		}
	} else {
		abs := filepath.Join(workDir, rg.file)
		if info := proofLstat(abs); info == nil || !info.Mode().IsRegular() {
			return nil, &proofError{"bad-path", "red-green: " + rg.file + " is not a regular file"}
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		deleted, err := proofDeleteAssertions(rg.file, data)
		if err != nil {
			return nil, err
		}
		redState = append(redState, redFile{rg.file, deleted})
	}
	paths := make([]string, len(redState))
	for i, f := range redState {
		paths[i] = f.rel
	}
	if err := proofWriteJournal(workDir, paths, mechanism, label); err != nil {
		return nil, err
	}
	red, err := func() (red *proofRedParse, err error) {
		defer func() {
			if _, restoreErr := proofRestoreJournal(workDir); restoreErr != nil {
				red, err = nil, restoreErr
			}
		}()
		for _, f := range redState {
			abs := filepath.Join(workDir, f.rel)
			if err := os.Remove(abs); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
			if f.bytes != nil {
				if err := os.MkdirAll(filepath.Dir(abs), 0o777); err != nil {
					return nil, err
				}
				if err := os.WriteFile(abs, f.bytes, 0o666); err != nil {
					return nil, err
				}
			}
		}
		parsed := proofParseRed(proofExec(rg.argv, workDir, env, rg.runID, proofTestTimeout))
		return &parsed, nil
	}()
	if err != nil {
		return nil, err
	}
	if red.verdict == "RED_INVALID" {
		fmt.Fprint(stderr, "RED_INVALID "+label+" ("+red.kind+")\n")
	} else {
		fmt.Fprint(stderr, red.verdict+" "+label+"\n")
	}
	var green any
	if red.verdict == "RED" {
		green = "NOT_GREEN"
		if proofExec(rg.argv, workDir, env, rg.runID, proofTestTimeout).ok() {
			green = "GREEN"
		}
		fmt.Fprint(stderr, green.(string)+" "+label+"\n")
	}
	return &proofRGResult{mechanism: mechanism, red: red, green: green, proven: red.verdict == "RED" && green == "GREEN"}, nil
}

// proofReadBaseFiles is readBaseFiles: each file's bytes at base, or nil
// when base does not have it, through a temporary index.
func proofReadBaseFiles(repoDir, base string, files []string) (map[string][]byte, error) {
	resolved, err := proofGit(repoDir, nil, "", "rev-parse", base+"^{commit}")
	if err != nil {
		var pe *proofError
		if errors.As(err, &pe) {
			return nil, &proofError{"bad-ref", "red-green: the base is not a commit in this repository"}
		}
		return nil, err
	}
	sha := strings.TrimSpace(resolved)
	listing, err := proofGit(repoDir, nil, "", "ls-tree", "-r", "-z", "--full-tree", "--name-only", sha)
	if err != nil {
		return nil, err
	}
	listed := map[string]bool{}
	for _, name := range strings.Split(listing, "\x00") {
		if name != "" {
			listed[name] = true
		}
	}
	out := map[string][]byte{}
	present := []string{}
	for _, f := range files {
		if listed[f] {
			present = append(present, f)
		}
	}
	if len(present) == 0 {
		return out, nil
	}
	scratchBase := os.TempDir()
	if root := os.Getenv("CODE_FORGE_TMP_ROOT"); filepath.IsAbs(root) {
		scratchBase = root
	}
	if err := os.MkdirAll(scratchBase, 0o700); err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp(scratchBase, "red-green-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "index")}
	into := filepath.Join(scratch, "tree")
	if _, err := proofGit(repoDir, env, "", "read-tree", sha); err != nil {
		return nil, err
	}
	if _, err := proofGit(repoDir, env, strings.Join(present, "\x00")+"\x00", "checkout-index", "-f", "-z", "--stdin", "--prefix="+into+string(filepath.Separator)); err != nil {
		return nil, err
	}
	for _, rel := range present {
		data, err := os.ReadFile(filepath.Join(into, rel))
		if err != nil {
			return nil, err
		}
		out[rel] = data
	}
	return out, nil
}

// proofWriteJournal is writeJournal: every file's bytes, hash and mode (or
// absent), written atomically before any change.
func proofWriteJournal(workDir string, files []string, mechanism, test string) error {
	entries := []any{}
	for _, rel := range files {
		abs := filepath.Join(workDir, rel)
		info := proofLstat(abs)
		if info == nil {
			entries = append(entries, ledger.Object{{Key: "path", Value: rel}, {Key: "existed", Value: false}})
			continue
		}
		if !info.Mode().IsRegular() {
			return &proofError{"bad-path", "red-green: " + rel + " is not a regular file"}
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return err
		}
		entries = append(entries, ledger.Object{
			{Key: "path", Value: rel}, {Key: "existed", Value: true}, {Key: "sha256", Value: proofSha(data)},
			{Key: "mode", Value: float64(info.Mode().Perm())}, {Key: "content", Value: base64.StdEncoding.EncodeToString(data)},
		})
	}
	journal := ledger.Object{
		{Key: "version", Value: float64(1)}, {Key: "pid", Value: float64(os.Getpid())},
		{Key: "created_at", Value: proofNow().UTC().Format("2006-01-02T15:04:05.000Z")},
		{Key: "mechanism", Value: mechanism}, {Key: "test", Value: test}, {Key: "files", Value: entries},
	}
	file := filepath.Join(workDir, proofJournalRel)
	if err := os.MkdirAll(filepath.Dir(file), 0o777); err != nil {
		return err
	}
	if err := os.WriteFile(file+".tmp", []byte(proofJSON(journal)), 0o600); err != nil {
		return err
	}
	return os.Rename(file+".tmp", file)
}

// proofTestEnv is testEnv(): the environment minus NODE_TEST_CONTEXT.
func proofTestEnv() map[string]string {
	env := map[string]string{}
	for _, s := range os.Environ() {
		k, v, _ := strings.Cut(s, "=")
		if k != "NODE_TEST_CONTEXT" {
			env[k] = v
		}
	}
	return env
}

// proofExecResult is util/exec.mjs's result.
type proofExecResult struct {
	code                *int
	signal              string
	stdout, stderr, err string
	timedOut, overflow  bool
}

func (r *proofExecResult) ok() bool {
	return r.err == "" && !r.timedOut && !r.overflow && r.code != nil && *r.code == 0
}

type proofCapped struct {
	mu       sync.Mutex
	data     []byte
	over     *bool
	overKill func()
}

func (c *proofCapped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if *c.over {
		return len(p), nil
	}
	if room := proofMaxBuffer - len(c.data); len(p) > room {
		c.data = append(c.data, p[:room]...)
		*c.over = true
		c.overKill()
		return len(p), nil
	}
	c.data = append(c.data, p...)
	return len(p), nil
}

var proofErrnoNames = map[syscall.Errno]string{
	syscall.ENOENT: "ENOENT", syscall.EACCES: "EACCES", syscall.ENOTDIR: "ENOTDIR", syscall.ENOEXEC: "ENOEXEC",
	syscall.EPERM: "EPERM", syscall.E2BIG: "E2BIG", syscall.ELOOP: "ELOOP", syscall.ENAMETOOLONG: "ENAMETOOLONG",
}

// proofExec is exec(): its own process group, the timeout (TERM, KILL after
// the grace), 10 MB per stream, SIGINT/SIGTERM/SIGHUP forwarded to the group
// and then re-raised, and the group's leftovers stopped after the leader.
func proofExec(argv []string, cwd string, env map[string]string, runID string, timeout time.Duration) *proofExecResult {
	res := &proofExecResult{}
	vars := map[string]string{}
	for k, v := range env {
		vars[k] = v
	}
	vars["CODE_FORGE_OWNER_PID"] = strconv.Itoa(os.Getpid())
	if start := filelock.ProcessStartTime(os.Getpid()); start != nil {
		vars["CODE_FORGE_OWNER_START_TIME"] = *start
	} else {
		delete(vars, "CODE_FORGE_OWNER_START_TIME")
	}
	if _, present := vars["CODE_FORGE_RUN_ID"]; !present {
		vars["CODE_FORGE_RUN_ID"] = runID
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	for k, v := range vars {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var mu sync.Mutex
	exited := false
	group := func(sig syscall.Signal) {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, sig)
		}
	}
	stdout := &proofCapped{over: &res.overflow, overKill: func() { group(syscall.SIGKILL) }}
	stderr := &proofCapped{over: &res.overflow, overKill: func() { group(syscall.SIGKILL) }}
	outRead, outWrite, err1 := os.Pipe()
	errRead, errWrite, err2 := os.Pipe()
	if err1 != nil || err2 != nil {
		res.err = "spawn " + argv[0] + " EMFILE"
		return res
	}
	defer outRead.Close()
	defer errRead.Close()
	cmd.Stdout, cmd.Stderr = outWrite, errWrite
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		outWrite.Close()
		errWrite.Close()
		name := "ENOENT"
		var errno syscall.Errno
		if errors.As(err, &errno) {
			if known, ok := proofErrnoNames[errno]; ok {
				name = known
			} else {
				name = errno.Error()
			}
		}
		res.err = "spawn " + argv[0] + " " + name
		return res
	}
	outWrite.Close()
	errWrite.Close()
	var drains sync.WaitGroup
	drains.Add(2)
	go func() { defer drains.Done(); _, _ = io.Copy(stdout, outRead) }()
	go func() { defer drains.Done(); _, _ = io.Copy(stderr, errRead) }()
	done := make(chan struct{})
	go func() {
		select {
		case sig := <-signals:
			s := sig.(syscall.Signal)
			group(s)
			signal.Reset(s)
			_ = syscall.Kill(os.Getpid(), s)
		case <-done:
		}
	}()
	var killTimer *time.Timer
	termTimer := time.AfterFunc(timeout, func() {
		mu.Lock()
		defer mu.Unlock()
		if exited {
			return
		}
		res.timedOut = true
		group(syscall.SIGTERM)
		killTimer = time.AfterFunc(proofKillGrace, func() { group(syscall.SIGKILL) })
	})
	waitErr := cmd.Wait()
	mu.Lock()
	exited = true
	termTimer.Stop()
	if killTimer != nil {
		killTimer.Stop()
	}
	mu.Unlock()
	close(done)
	gates.StopRemaining(cmd.Process.Pid)
	drains.Wait()
	if cmd.ProcessState == nil {
		res.err = waitErr.Error()
	} else if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		res.signal = proofSignalName(status.Signal())
	} else {
		code := cmd.ProcessState.ExitCode()
		res.code = &code
	}
	if res.overflow {
		res.err = "exec: maxBufferBytes exceeded"
	}
	res.stdout, res.stderr = jsDecode(stdout.data), jsDecode(stderr.data)
	return res
}

// proofSignalName is Node's signal name (SIGKILL), not Go's text ("killed").
func proofSignalName(sig syscall.Signal) string {
	for name, value := range map[string]syscall.Signal{"SIGKILL": syscall.SIGKILL, "SIGTERM": syscall.SIGTERM, "SIGINT": syscall.SIGINT,
		"SIGHUP": syscall.SIGHUP, "SIGSEGV": syscall.SIGSEGV, "SIGABRT": syscall.SIGABRT, "SIGBUS": syscall.SIGBUS, "SIGPIPE": syscall.SIGPIPE} {
		if sig == value {
			return name
		}
	}
	return "SIG" + strconv.Itoa(int(sig))
}
