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
// for the anthropic and xai providers (B582: and openai, the Codex CLI): the read-only snapshot (git archive +
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
	roots              []*fcRoot
	opts               session.Opts
	home               string
	tmpRoot            *string // --run's tmp.root (runRootFor)
	crash              *fcCrash
	levelCrash         string // resolveLevel's throw inside spawnSession
}

// fcCrash is an error Node does not catch: the CLI prints its stack and logs
// a crash row (B568).
type fcCrash struct{ name, message string }

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
	// Buffer#toString('utf8'): each bad sequence becomes one U+FFFD
	text := fcDecodeUTF8(p.brief)
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
	if p.roots, err = fcSourceRoots(p.real, p.sources); err != nil {
		return nil, hFactsSession
	}
	p.claims = fcMapClaims(fcExtractClaims(text, bins, topLevel), p.roots)
	for _, claim := range p.claims {
		check := claim.token
		if claim.hasCheckPath {
			check = claim.checkPath
		}
		// a glob path:matchesGlob does not model (fcGlobUnmodelled) hands over
		if claim.kind == "path" && fcHasGlob(claim.token) && !strings.HasPrefix(claim.token, "~") && !fcDotDotSegment(claim.token) &&
			fcGlobUnmodelled(fcRelPosix(check)) {
			return nil, hFactsGlobClaim
		}
	}
	p.lines = strings.Count(text, "\n") + 1
	if strings.HasSuffix(text, "\n") {
		p.lines--
	}
	// runRootFor(run, cfg.tmp.root): a tmp.root that is not an absolute
	// path is a TypeError Node does not catch, thrown before any write
	contextApplyTmpRoot()
	if p.run != nil {
		if tmp, ok := cfg["tmp"].(map[string]any); ok {
			if value, present := tmp["root"]; present && value != nil {
				text, isString := value.(string)
				if !isString || !filepath.IsAbs(text) {
					p.crash = &fcCrash{"TypeError", "tmp: tmp.root must be an absolute path"}
					return p, 0
				}
				p.tmpRoot = &text
			}
		}
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
	// B582: an openai (Codex) step runs the closed-book Codex session of B551
	// (read-only argv in the snapshot, strict schema file, -o answer); facts
	// is not a no-tools role, so review.allow_open_book_codex changes nothing
	err = session.Check(probe)
	if p.levelCrash = session.LevelCrash(err); p.levelCrash != "" {
		return p, 0
	}
	if session.IsUnsupported(err) {
		return nil, hFactsSession
	}
	// B568: the run-pinned gate's Date.parse cases are found here, before
	// any write, not in the middle of the run
	if session.IsUnsupported(session.GatePreflight(p.opts, session.Deps{})) {
		return nil, hFactsSession
	}
	return p, 0
}

// fcDecodeUTF8 is the WHATWG UTF-8 decoder Buffer#toString('utf8') uses:
// one U+FFFD per maximal invalid subpart.
func fcDecodeUTF8(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	var out strings.Builder
	needed, seen := 0, 0
	cp := rune(0)
	lower, upper := byte(0x80), byte(0xBF)
	for i := 0; i < len(data); i++ {
		b := data[i]
		if needed == 0 {
			switch {
			case b <= 0x7F:
				out.WriteByte(b)
			case b >= 0xC2 && b <= 0xDF:
				needed, cp = 1, rune(b&0x1F)
			case b >= 0xE0 && b <= 0xEF:
				if b == 0xE0 {
					lower = 0xA0
				} else if b == 0xED {
					upper = 0x9F
				}
				needed, cp = 2, rune(b&0x0F)
			case b >= 0xF0 && b <= 0xF4:
				if b == 0xF0 {
					lower = 0x90
				} else if b == 0xF4 {
					upper = 0x8F
				}
				needed, cp = 3, rune(b&0x07)
			default:
				out.WriteRune(utf8.RuneError)
			}
			continue
		}
		if b < lower || b > upper {
			needed, seen, cp, lower, upper = 0, 0, 0, 0x80, 0xBF
			out.WriteRune(utf8.RuneError)
			i--
			continue
		}
		lower, upper = 0x80, 0xBF
		cp = cp<<6 | rune(b&0x3F)
		if seen++; seen == needed {
			out.WriteRune(cp)
			needed, seen, cp = 0, 0, 0
		}
	}
	if needed != 0 {
		out.WriteRune(utf8.RuneError)
	}
	return out.String()
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

// fcBuildSnapshot is buildSnapshot.
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
	if err := fcSnapshotSources(p.roots, sessionCwd, snapDir, workDir); err != nil {
		return "", err
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

// fcMarkUnavailable is markUnavailable: one git check-ignore per root.
func fcMarkUnavailable(p *fcPlan, cwd string) error {
	directories := []*fcRoot{}
	for _, r := range p.roots {
		if r.file == "" {
			directories = append(directories, r)
		}
	}
	for _, root := range p.roots {
		idx := []int{}
		relatives := []string{}
		for i, c := range p.claims {
			if c.kind != "path" || c.unavailable != "" {
				continue
			}
			if !(c.root == root || c.root == nil && len(directories) == 1 && root == directories[0] && !filepath.IsAbs(c.token)) {
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
				if rel = fcRelative(root.dir, c.token); rel == "" {
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
		checked := fcExec([]string{"git", "check-ignore", "-z", "--stdin"}, root.dir, fcGitEnv(), input, 0, 1)
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
				c.unavailable = "git-ignored in " + root.label + ": the snapshot leaves ignored paths out"
			} else if _, err := os.Lstat(filepath.Join(root.dir, relatives[n])); err == nil {
				if _, err := os.Lstat(filepath.Join(cwd, check)); err != nil {
					c.unavailable = "not at HEAD in " + root.label + ": outside the snapshot"
				}
			}
		}
	}
	return nil
}

// fcPacket is buildFactsPacket.
func fcPacket(claims []fcClaim) string {
	lines := []string{}
	for i, c := range claims {
		if c.unavailable != "" {
			continue
		}
		line := "- F" + strconv.Itoa(i+1) + " " + c.kind + ": " + c.token
		if c.hasSource {
			line += " (source " + c.source + ", check at " + c.checkPath + ")"
		} else if c.hasCheckPath && c.checkPath != "" {
			line += " (check at " + c.checkPath + ")"
		}
		if c.kind == "path" && fcHasGlob(c.token) {
			token := c.token
			if c.hasCheckPath {
				token = c.checkPath
			}
			line += fcGlobHint(token)
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
	return fcCrashed(p, &fcCrash{"Error", err.Error()}, stderr)
}

// fcCrashed prints what bin/code-forge.mjs prints for an uncaught error
// (its first stack line; Go has no JS frames) and keeps it for the crash row.
func fcCrashed(p *fcPlan, c *fcCrash, stderr io.Writer) int {
	fmt.Fprintf(stderr, "code-forge: verb \"facts\" failed: %s: %s\n", c.name, c.message)
	p.crash = c
	return 1
}

// fcRunFacts is buildFacts plus runFactsVerb's result handling; handover =
// a run root Go could not make (an mkdir error) or, only if the gate's files
// change between the plan's GatePreflight and the session, a gate Go cannot
// read like Node: everything made here is removed first. B568: the gate's
// Date.parse cases are decided in fcPlanFacts, before any write.
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
	if p.crash != nil {
		return fcCrashed(p, p.crash, stderr), false
	}
	var root string
	if p.run != nil {
		dir, err := contextRunRoot(*p.run, p.tmpRoot)
		if err != nil {
			// tmp.mjs's trust refusals are Errors Node does not catch;
			// anything else (an mkdir failure) stays with Node
			if strings.HasPrefix(err.Error(), "tmp: refusing untrusted ") {
				return fcCrashed(p, &fcCrash{"Error", err.Error()}, stderr), false
			}
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
	// B9b.1: a run root inside a git work tree moves the facts area to the
	// private root factsTmpRoot makes (left in place, as Node leaves it)
	area := filepath.Join(root, "facts")
	if real := session.RealOrResolved(root); session.GitWorkTreeAncestor(real) != "" {
		sum := sha256.Sum256([]byte(real))
		factsRoot, err := contextMakeRunRoot("facts-"+hex.EncodeToString(sum[:])[:16], nil)
		if err != nil {
			return fcRunError(p, err, stderr), false
		}
		area = filepath.Join(factsRoot, "facts")
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	dir := filepath.Join(area, strconv.FormatInt(time.Now().UnixMilli(), 10)+"-"+hex.EncodeToString(suffix))
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
	if p.levelCrash != "" {
		// spawnSession: assertFactsCwd, then resolveLevel throws
		if err := session.CheckFactsCwd(cwd, root); err != nil {
			fmt.Fprintf(stderr, "facts: %s\n", err.Error())
			return 1, false, nil
		}
		return fcCrashed(p, &fcCrash{"Error", p.levelCrash}, stderr), false, nil
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
