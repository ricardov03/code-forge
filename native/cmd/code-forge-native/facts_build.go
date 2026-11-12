package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/docsync"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/session"
)

// B556: buildFacts (src/session/facts.mjs) and the rest of runFactsVerb in Go
// for the anthropic and xai providers: the read-only snapshot (git archive +
// seal), git check-ignore marking, packet.md, the L0 facts session, the
// answer validation, the sheet and the facts.built ledger row. Every case Go
// does not serve byte-equal is a named hand-over decided before any side
// effect (fcPlanFacts).

// fcSchemaPath names schema/facts.schema.json; tests point it at the repository copy.
var fcSchemaPath = func() (string, error) {
	root, err := docsync.PackageRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "schema", "facts.schema.json"), nil
}

// fcMaxBudgetUsd is FACTS_MAX_BUDGET_USD.
const fcMaxBudgetUsd = 0.5

// fcGitTimeout is GIT_TIMEOUT_MS.
const fcGitTimeout = 120 * time.Second

type fcPlan struct {
	cwd, project, real string // project: projectRootFor(cwd); real: its realpath
	briefPath, outPath string
	sources            []string
	brief              []byte
	mtime              string
	claims             []fcClaim
	lines              int
	run                *string
	opts               session.Opts
	home               string
}

// fcPortable is portable(): the schema without annotations and string-length keywords.
func fcPortable(v any) any {
	switch node := v.(type) {
	case []any:
		out := make([]any, len(node))
		for i, item := range node {
			out[i] = fcPortable(item)
		}
		return out
	case ledger.Object:
		out := ledger.Object{}
		for _, f := range node {
			switch f.Key {
			case "$schema", "$id", "title", "description", "maxLength", "minLength", "pattern":
				continue
			}
			out = append(out, ledger.Field{Key: f.Key, Value: fcPortable(f.Value)})
		}
		return out
	}
	return v
}

// fcRelative is path.relative for absolute paths ("" for the same path).
func fcRelative(from, to string) string {
	rel, err := filepath.Rel(from, to)
	if err != nil || rel == "." {
		return ""
	}
	return rel
}

// fcBelowRoot is belowRoot.
func fcBelowRoot(root, file string) bool {
	rel := fcRelative(root, file)
	return rel == "" || (!strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." && !filepath.IsAbs(rel))
}

// fcExtname is path.extname for a base name.
func fcExtname(base string) string {
	i := strings.LastIndex(base, ".")
	if i <= 0 || strings.Trim(base[:i], ".") == "" {
		return ""
	}
	return base[i:]
}

// fcISO is Date.prototype.toISOString.
func fcISO(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// fcPlanFacts reads everything buildFacts reads before its first write and
// decides every hand-over; nil and a reason = hand over to Node.
func fcPlanFacts(flags map[string]any, brief string) (*fcPlan, handoverReason) {
	p := &fcPlan{}
	var err error
	if p.cwd, err = os.Getwd(); err != nil {
		return nil, hFactsSession
	}
	p.project = config.ProjectRootFor(p.cwd)
	cfg, err := config.Load(p.cwd)
	if err != nil {
		return nil, hFactsSession
	}
	plans := "plans"
	if project, ok := cfg["project"].(map[string]any); ok {
		if value, present := project["plans_dir"]; present && value != nil {
			text, isString := value.(string)
			if !isString {
				return nil, hFactsSession
			}
			plans = text
		}
	} else if cfg["project"] != nil {
		return nil, hFactsSession
	}
	plansDir := spawnResolve(p.project, plans)
	p.briefPath = spawnResolve(p.cwd, brief)
	if out, ok := flags["out"].(string); ok {
		p.outPath = spawnResolve(p.cwd, out)
	} else {
		base := filepath.Base(p.briefPath)
		p.outPath = filepath.Join(plansDir, strings.TrimSuffix(base, fcExtname(base))+".facts.md")
	}
	given, _ := flags["sources"].([]string)
	for _, s := range given {
		p.sources = append(p.sources, spawnResolve(p.cwd, s))
	}
	if run, ok := flags["run"].(string); ok {
		p.run = &run
	}
	p.home = os.Getenv("HOME")
	if p.home == "" {
		return nil, hFactsSession
	}
	// readBriefOnce: bytes and mtime from one open file
	file, err := os.Open(p.briefPath)
	if err != nil {
		return nil, hFactsSession
	}
	p.brief, err = io.ReadAll(file)
	st, statErr := file.Stat()
	_ = file.Close()
	if err != nil || statErr != nil {
		return nil, hFactsSession
	}
	p.mtime = fcISO(st.ModTime())
	// Buffer#toString('utf8') replaces bad bytes; the claims would differ
	if !utf8.Valid(p.brief) {
		return nil, hFactsBriefEncoding
	}
	text := string(p.brief)
	bins := []string{}
	if data, err := os.ReadFile(filepath.Join(p.project, "package.json")); err == nil {
		bins = fcProjectBins(data)
	}
	entries, err := os.ReadDir(p.project)
	if err != nil {
		return nil, hFactsSession
	}
	topLevel := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() {
			topLevel[entry.Name()] = true
		}
	}
	if p.real, err = filepath.EvalSymlinks(p.project); err != nil {
		return nil, hFactsSession
	}
	// sourceRoots: a source outside the project is mounted under _sources
	for _, source := range p.sources {
		real, err := filepath.EvalSymlinks(source)
		if err != nil {
			return nil, hFactsSession
		}
		if !fcBelowRoot(p.real, real) {
			return nil, hFactsOutsideSource
		}
	}
	root := &fcRoot{dir: p.real, label: filepath.Base(p.real)}
	for _, claim := range fcExtractClaims(text, bins, topLevel) {
		if claim.kind == "path" && fcHasGlob(claim.token) {
			return nil, hFactsGlobClaim
		}
		// mapClaims with the project as the one root
		if claim.kind == "path" && filepath.IsAbs(claim.token) {
			if claim.token == "/dev" || strings.HasPrefix(claim.token, "/dev/") {
				continue
			}
			if fcBelowRoot(root.dir, claim.token) {
				claim.root = root
				claim.checkPath, claim.hasCheckPath = filepath.ToSlash(fcRelative(root.dir, claim.token)), true
				if claim.checkPath == "" {
					claim.checkPath = "."
				}
			} else if !fcBelowAny(fcToolRoots, claim.token) {
				if !fcIsDir("/" + strings.Split(claim.token, "/")[1]) {
					continue
				}
				claim.unavailable = "outside every --sources root and the snapshot"
			}
		}
		p.claims = append(p.claims, claim)
	}
	p.lines = strings.Count(text, "\n") + 1
	if strings.HasSuffix(text, "\n") {
		p.lines--
	}
	// the run root: --run's (made at run time), else the process's lazy root
	contextApplyTmpRoot()
	base := session.TmpBase()
	var runDir string
	if p.run != nil {
		var tmpRoot *string
		if tmp, ok := cfg["tmp"].(map[string]any); ok {
			if value, present := tmp["root"]; present && value != nil {
				s, isString := value.(string)
				if !isString || !filepath.IsAbs(s) {
					return nil, hFactsSession
				}
				tmpRoot = &s
			}
		}
		if tmpRoot != nil {
			base = *tmpRoot
		}
		runDir = filepath.Join(base, *p.run)
	} else {
		runDir = filepath.Join(base, "proc-"+strconv.Itoa(os.Getpid()))
	}
	// a run root inside a git work tree moves the snapshot to factsTmpRoot
	if session.GitWorkTreeAncestor(session.RealOrResolved(runDir)) != "" {
		return nil, hFactsRootInGit
	}
	schemaPath, err := fcSchemaPath()
	if err != nil {
		return nil, hFactsSession
	}
	data, err := os.ReadFile(schemaPath)
	if err != nil {
		return nil, hFactsSession
	}
	schema, err := ledger.DecodeObject(data)
	if err != nil {
		return nil, hFactsSession
	}
	p.opts = session.Opts{Cfg: cfg, Level: "L0", Role: "facts", Schema: fcPortable(schema).(ledger.Object), Run: p.run,
		Slug: session.SlugFor(cfg, p.project), Validate: fcValidAnswer, MaxBudgetUsd: fcMaxBudgetUsd}
	if raw, ok := flags["timeout"].(string); ok {
		n, _ := cliIntFlag(raw, "timeout")
		p.opts.TimeoutMs = n * 1000
	}
	// the packet path is set at run time; Check only needs an absolute one
	probe := p.opts
	probe.PromptPath = filepath.Join(p.project, "facts.packet.md")
	if session.HasCodexStep(probe) {
		return nil, hFactsCodexStep
	}
	if err := session.Check(probe); session.IsUnsupported(err) {
		return nil, hFactsSession
	}
	return p, 0
}

func fcBelowAny(roots []string, file string) bool {
	for _, r := range roots {
		if fcBelowRoot(r, file) {
			return true
		}
	}
	return false
}

// fcExecResult is exec.mjs's result: ok when the exit code is one of ok.
type fcExecResult struct {
	ok             bool
	code           int
	stdout, stderr string
}

// fcGitEnv is gitEnv(): the env minus every GIT_*, with no system/global git config.
func fcGitEnv() []string {
	env := []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return env
}

func fcExec(argv []string, dir string, env []string, input string, okCodes ...int) fcExecResult {
	if len(okCodes) == 0 {
		okCodes = []int{0}
	}
	ctx, cancel := context.WithTimeout(context.Background(), fcGitTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	res := fcExecResult{code: -1, stdout: out.String(), stderr: errOut.String()}
	var exitErr *exec.ExitError
	if err == nil {
		res.code = 0
	} else if errors.As(err, &exitErr) && ctx.Err() == nil {
		res.code = exitErr.ExitCode()
	}
	for _, c := range okCodes {
		if res.code >= 0 && res.code == c {
			res.ok = true
		}
	}
	return res
}

// fcSealTree is sealTree.
func fcSealTree(dir, rel string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		full := filepath.Join(dir, entry.Name())
		relPath := entry.Name()
		if rel != "" {
			relPath = rel + "/" + entry.Name()
		}
		st, err := os.Lstat(full)
		if err != nil {
			return err
		}
		switch {
		case st.Mode()&os.ModeSymlink != 0 || fcSecretPath(relPath):
			if err := os.RemoveAll(full); err != nil {
				return err
			}
		case st.IsDir():
			if err := fcSealTree(full, relPath); err != nil {
				return err
			}
		case st.Mode().IsRegular():
			if err := os.Chmod(full, 0o444); err != nil {
				return err
			}
		default:
			if err := syscall.Unlink(full); err != nil {
				return err
			}
		}
	}
	return os.Chmod(dir, 0o555)
}

// fcRemoveSnapshot is removeSnapshot.
func fcRemoveSnapshot(dir string) {
	var unseal func(d string) error
	unseal = func(d string) error {
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			full := filepath.Join(d, entry.Name())
			if st, err := os.Lstat(full); err == nil && st.IsDir() {
				if err := unseal(full); err != nil {
					return err
				}
			}
		}
		return nil
	}
	_ = unseal(dir)
	_ = os.RemoveAll(dir)
}

// fcDropLinks is dropLinks.
func fcDropLinks(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		full := filepath.Join(dir, entry.Name())
		st, err := os.Lstat(full)
		if err != nil {
			return err
		}
		if st.IsDir() {
			if err := fcDropLinks(full); err != nil {
				return err
			}
		} else if !st.Mode().IsRegular() {
			if err := syscall.Unlink(full); err != nil {
				return err
			}
		}
	}
	return nil
}

// fcAssertNoLinkSegments is assertNoLinkSegments.
func fcAssertNoLinkSegments(snapDir, dir string) error {
	rel := fcRelative(snapDir, dir)
	if rel == "" {
		return nil
	}
	cur := snapDir
	for _, seg := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, seg)
		st, err := os.Lstat(cur)
		if err != nil {
			return nil
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return &fcRefused{"the project snapshot refused a copy across a symlink (" + fcRelative(snapDir, cur) + ")"}
		}
		if !st.IsDir() {
			return &fcRefused{"the project snapshot has a file where a directory is needed (" + fcRelative(snapDir, cur) + ")"}
		}
	}
	return nil
}

// fcGitSegment is /(^|\/)\.git(\/|$)/.
func fcGitSegment(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if seg == ".git" {
			return true
		}
	}
	return false
}

// fcCopyNoFollow is copyNoFollow.
func fcCopyNoFollow(src, dst, snapDir string) error {
	rel := filepath.ToSlash(fcRelative(snapDir, dst))
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if st.Mode()&os.ModeSymlink != 0 || fcGitSegment(rel) || fcSecretPath(rel) {
		return nil
	}
	if err := fcAssertNoLinkSegments(snapDir, filepath.Dir(dst)); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	at, atErr := os.Lstat(dst)
	if st.IsDir() {
		if atErr != nil {
			if err := os.Mkdir(dst, 0o700); err != nil {
				return err
			}
		} else if !at.IsDir() {
			return &fcRefused{"the project snapshot has a file where a directory is needed (" + rel + ")"}
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := fcCopyNoFollow(filepath.Join(src, entry.Name()), filepath.Join(dst, entry.Name()), snapDir); err != nil {
				return err
			}
		}
		return nil
	}
	if !st.Mode().IsRegular() {
		return nil
	}
	if atErr == nil {
		if !at.Mode().IsRegular() {
			return &fcRefused{"the project snapshot has a directory where a file is needed (" + rel + ")"}
		}
		if err := syscall.Unlink(dst); err != nil {
			return err
		}
	}
	fd, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err == nil {
		_, err = fd.Write(data)
	}
	if cerr := fd.Close(); err == nil {
		err = cerr
	}
	return err
}

// fcBuildSnapshot is buildSnapshot for sources inside the project (an
// outside source hands over in fcPlanFacts).
func fcBuildSnapshot(p *fcPlan, snapDir, workDir string) (string, error) {
	if err := os.MkdirAll(snapDir, 0o700); err != nil {
		return "", err
	}
	project := p.real
	root := project
	env := fcGitEnv()
	top := fcExec([]string{"git", "rev-parse", "--show-toplevel"}, project, env, "")
	if top.ok && jsTrim(top.stdout) != "" {
		real, err := filepath.EvalSymlinks(jsTrim(top.stdout))
		if err != nil {
			return "", err
		}
		root = real
		head := fcExec([]string{"git", "rev-parse", "--verify", "-q", "HEAD"}, root, env, "")
		if head.ok && jsTrim(head.stdout) != "" {
			tarPath := filepath.Join(workDir, "head.tar")
			err := func() error {
				defer os.Remove(tarPath)
				archived := fcExec([]string{"git", "archive", "--format=tar", "-o", tarPath, "HEAD"}, root, env, "")
				if !archived.ok {
					return &fcRefused{"the project snapshot could not be archived (git archive exit " + fcExitText(archived.code) + ")"}
				}
				untar := fcExec([]string{"tar", "-xf", tarPath, "-C", snapDir}, "", os.Environ(), "")
				if !untar.ok {
					return &fcRefused{"the project snapshot could not be extracted (tar exit " + fcExitText(untar.code) + ")"}
				}
				return nil
			}()
			if err != nil {
				return "", err
			}
			if err := fcDropLinks(snapDir); err != nil {
				return "", err
			}
		}
	}
	sessionCwd := filepath.Join(snapDir, fcRelative(root, project))
	if err := fcAssertNoLinkSegments(snapDir, sessionCwd); err != nil {
		return "", err
	}
	if err := os.MkdirAll(sessionCwd, 0o700); err != nil {
		return "", err
	}
	for _, source := range p.sources {
		real, err := filepath.EvalSymlinks(source)
		if err != nil {
			return "", err
		}
		inProject := fcRelative(project, real)
		if inProject == "" || strings.HasPrefix(inProject, "..") || filepath.IsAbs(inProject) {
			continue
		}
		if err := fcCopyNoFollow(real, filepath.Join(sessionCwd, inProject), snapDir); err != nil {
			return "", err
		}
	}
	if err := fcSealTree(snapDir, ""); err != nil {
		return "", err
	}
	return sessionCwd, nil
}

// fcExitText is a Node exit code in a template literal (null when none).
func fcExitText(code int) string {
	if code < 0 {
		return "null"
	}
	return strconv.Itoa(code)
}

// fcMarkUnavailable is markUnavailable with the project as the one root.
func fcMarkUnavailable(p *fcPlan, cwd string) error {
	label := filepath.Base(p.real)
	idx := []int{}
	relatives := []string{}
	for i, c := range p.claims {
		if c.kind != "path" || c.unavailable != "" {
			continue
		}
		if !(c.root != nil || !filepath.IsAbs(c.token)) {
			continue
		}
		check := c.token
		if c.hasCheckPath {
			check = c.checkPath
		}
		if fcPathViolation(check) != "" {
			continue
		}
		rel := c.token
		if filepath.IsAbs(c.token) {
			if rel = fcRelative(p.real, c.token); rel == "" {
				rel = "."
			}
		}
		idx = append(idx, i)
		relatives = append(relatives, rel)
	}
	input := ""
	for _, r := range relatives {
		input += r + "\x00"
	}
	checked := fcExec([]string{"git", "check-ignore", "-z", "--stdin"}, p.real, fcGitEnv(), input, 0, 1)
	if !checked.ok && !(checked.code == 128 && strings.Contains(strings.ToLower(checked.stderr), "not a git repository")) {
		return &fcRefused{"git check-ignore could not check the source paths"}
	}
	ignored := map[string]bool{}
	for _, name := range strings.Split(checked.stdout, "\x00") {
		if name != "" {
			ignored[name] = true
		}
	}
	for n, i := range idx {
		c := &p.claims[i]
		check := c.token
		if c.hasCheckPath {
			check = c.checkPath
		}
		if ignored[relatives[n]] {
			c.unavailable = "git-ignored in " + label + ": the snapshot leaves ignored paths out"
		} else if _, err := os.Lstat(filepath.Join(p.real, relatives[n])); err == nil {
			if _, err := os.Lstat(filepath.Join(cwd, check)); err != nil {
				c.unavailable = "not at HEAD in " + label + ": outside the snapshot"
			}
		}
	}
	return nil
}

// fcPacket is buildFactsPacket for claims without a glob or a source.
func fcPacket(claims []fcClaim) string {
	lines := []string{}
	for i, c := range claims {
		if c.unavailable != "" {
			continue
		}
		line := "- F" + strconv.Itoa(i+1) + " " + c.kind + ": " + c.token
		if c.hasCheckPath && c.checkPath != "" {
			line += " (check at " + c.checkPath + ")"
		}
		lines = append(lines, line)
	}
	return fcRule + strings.Join(lines, "\n") + "\n"
}

// fcRunError is runFactsVerb's catch around buildFacts for an error that is
// not a FactsError or a SessionError: a file that vanished, else a crash.
func fcRunError(p *fcPlan, err error, stderr io.Writer) int {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		code := ""
		switch {
		case errors.Is(pathErr.Err, syscall.ENOENT):
			code = "ENOENT"
		case errors.Is(pathErr.Err, syscall.EACCES):
			code = "EACCES"
		case errors.Is(pathErr.Err, syscall.EISDIR):
			code = "EISDIR"
		}
		if code != "" {
			kind := "a working file"
			if pathErr.Path == p.briefPath {
				kind = "the brief"
			} else {
				for _, s := range p.sources {
					if s == pathErr.Path {
						kind = "a source"
					}
				}
			}
			fmt.Fprintf(stderr, "facts: %s could not be read while building the sheet (%s)\n", kind, code)
			return 1
		}
	}
	fmt.Fprintf(stderr, "code-forge: verb \"facts\" failed: Error: %s\n", err.Error())
	return 1
}

// fcRunFacts is buildFacts plus runFactsVerb's result handling; handover =
// the session refused an option only after the run began (the run-pinned
// gate's Date.parse cases): everything made here is removed first.
func fcRunFacts(p *fcPlan, stdout, stderr io.Writer) (code int, handover bool) {
	var procRoot string
	lazyRoot := func() (string, error) {
		if procRoot != "" {
			return procRoot, nil
		}
		dir, err := contextRunRoot("proc-"+strconv.Itoa(os.Getpid())+"-"+strconv.FormatInt(time.Now().UnixMilli(), 10), nil)
		procRoot = dir
		return dir, err
	}
	defer func() {
		if procRoot != "" {
			spawnReleaseRoot(procRoot)
		}
	}()
	var root string
	if p.run != nil {
		dir, ok := spawnRunRoot(p.cwd, *p.run)
		if !ok {
			return 0, true
		}
		root = dir
	}
	if p.lines > 40 && len(p.claims) < 5 {
		fmt.Fprintf(stderr, "forge facts: the brief has %d lines but only %d claims; the sheet checks almost nothing\n", p.lines, len(p.claims))
	}
	if root == "" {
		dir, err := lazyRoot()
		if err != nil {
			return fcRunError(p, err, stderr), false
		}
		root = dir
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	dir := filepath.Join(root, "facts", strconv.FormatInt(time.Now().UnixMilli(), 10)+"-"+hex.EncodeToString(suffix))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fcRunError(p, err, stderr), false
	}
	snapDir := filepath.Join(dir, "snapshot")
	cleanup := func() {
		fcRemoveSnapshot(snapDir)
		_ = os.RemoveAll(dir)
	}
	code, handover, after := fcSessionAndSheet(p, root, dir, snapDir, stderr, lazyRoot)
	cleanup()
	if handover || code != 0 {
		return code, handover
	}
	return after(stdout, stderr), false
}

// fcSessionAndSheet runs the steps between the snapshot and the ledger row;
// after prints the result once the work folder is gone.
func fcSessionAndSheet(p *fcPlan, root, dir, snapDir string, stderr io.Writer, lazyRoot func() (string, error)) (int, bool, func(stdout, stderr io.Writer) int) {
	var refused *fcRefused
	fail := func(err error) (int, bool, func(io.Writer, io.Writer) int) {
		if errors.As(err, &refused) {
			fmt.Fprintf(stderr, "facts: %s\n", refused.message)
			return 1, false, nil
		}
		return fcRunError(p, err, stderr), false, nil
	}
	cwd, err := fcBuildSnapshot(p, snapDir, dir)
	if err != nil {
		return fail(err)
	}
	if err := fcMarkUnavailable(p, cwd); err != nil {
		return fail(err)
	}
	packetPath := filepath.Join(dir, "packet.md")
	if err := os.WriteFile(packetPath, []byte(fcPacket(p.claims)), 0o600); err != nil {
		return fail(err)
	}
	opts := p.opts
	opts.PromptPath, opts.RunRoot, opts.Cwd = packetPath, root, cwd
	runID, hasRunID := os.LookupEnv("CODE_FORGE_RUN_ID")
	var sessionErr bytes.Buffer
	deps := session.Deps{Stderr: &sessionErr, RunID: runID, Warn: spawnLogWarning, RunRoot: lazyRoot}
	if p.run != nil {
		if !hasRunID {
			deps.RunID = *p.run
		}
		deps.PidsRoot = lazyRoot
	}
	result, err := session.Spawn(opts, deps)
	if session.IsUnsupported(err) {
		return 0, true, nil
	}
	_, _ = stderr.Write(sessionErr.Bytes())
	var sErr *session.Error
	if errors.As(err, &sErr) {
		fmt.Fprintf(stderr, "facts: %s\n", sErr.Message)
		if sErr.Code == "unavailable" {
			return 3, false, nil
		}
		return 1, false, nil
	}
	if err != nil {
		return fail(err)
	}
	if status, _ := result.Get("status").(string); status != "ok" {
		line := "facts: " + status
		if reason, _ := result.Get("reason").(string); reason != "" {
			line += " (" + reason + ")"
		}
		fmt.Fprint(stderr, line+"\n")
		switch status {
		case "started":
			return 0, false, func(io.Writer, io.Writer) int { return 0 }
		case "unavailable":
			return 3, false, nil
		}
		return 1, false, nil
	}
	briefRel := filepath.ToSlash(fcRelative(session.RealOrResolved(p.project), session.RealOrResolved(p.briefPath)))
	facts, downgraded, err := fcValidateFacts(result.Get("answer"), p.claims, fcCtx{briefRel: &briefRel, cwd: cwd})
	if err != nil {
		return fail(err)
	}
	sum := sha256.Sum256(p.brief)
	text := fcRenderSheet(filepath.ToSlash(fcRelative(filepath.Dir(p.outPath), p.briefPath)), hex.EncodeToString(sum[:]), p.mtime, fcISO(time.Now()), facts)
	if err := os.MkdirAll(filepath.Dir(p.outPath), 0o777); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(p.outPath, []byte(text), 0o666); err != nil {
		return fail(err)
	}
	verified := 0
	for _, f := range facts {
		if f.tag == "VERIFIED" {
			verified++
		}
	}
	var tokensIn, tokensOut any
	if row, ok := result.Get("row").(ledger.Object); ok {
		tokensIn, tokensOut = row.Get("tokens_in"), row.Get("tokens_out")
	}
	row := ledger.Object{{Key: "event", Value: "facts.built"}, {Key: "role", Value: "facts"}, {Key: "run", Value: strPtrAny(p.run)},
		{Key: "claims", Value: float64(len(p.claims))}, {Key: "verified", Value: float64(verified)}, {Key: "tokens_in", Value: tokensIn}, {Key: "tokens_out", Value: tokensOut}}
	if _, err := ledger.AppendObject(filepath.Join(p.home, ".code-forge", "ledger"), p.opts.Slug, row, ledger.DefaultRotateAtBytes); err != nil {
		return fail(err)
	}
	return 0, false, func(stdout, stderr io.Writer) int {
		for _, d := range downgraded {
			fmt.Fprint(stderr, fcDowngradeLine(d))
		}
		line, err := ledger.Stringify(ledger.Object{{Key: "out", Value: p.outPath}, {Key: "claims", Value: float64(len(p.claims))}, {Key: "verified", Value: float64(verified)}}, false)
		if err != nil {
			return fcRunError(p, err, stderr)
		}
		fmt.Fprint(stdout, string(line)+"\n")
		return 0
	}
}

func strPtrAny(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// fcRestoreCrumbs puts breadcrumbs.json back to the list before this call
// (a hand-over after the run began: Node writes its own crumb).
func fcRestoreCrumbs(before []byte, existed bool) {
	if !reviewWaitLogging() || os.Getenv("HOME") == "" {
		return
	}
	file := filepath.Join(os.Getenv("HOME"), ".code-forge", "logs", "breadcrumbs.json")
	if !existed {
		_ = os.Remove(file)
		return
	}
	tmp := file + ".tmp-" + strconv.Itoa(os.Getpid())
	defer os.Remove(tmp)
	if os.WriteFile(tmp, before, 0o600) == nil {
		_ = os.Rename(tmp, file)
	}
}
