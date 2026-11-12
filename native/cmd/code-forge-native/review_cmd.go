package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/reviewq"
)

// B460 (phase 1): `code-forge review` in Go (src/cli/review.mjs). Go answers
// --help, every usage error, "not inside a git repository", a base that does
// not resolve, a bad --files path, and the whole run when nothing is left to
// review (text or --json, with the skipped-file notes). B553: when a file is
// left to review, review_flow.go runs it (config, run start, block, ticket,
// wait, block stop, run end); the cases it cannot serve like Node hand over
// with a named reason before Go writes anything.
func init() { routes["review"] = runReviewVerb }

const reviewUsage = "usage: code-forge review [--base <ref>] [--files <path…>] [--acceptance <file> | --intent \"<text>\"] [--run <id>] [--max <seconds>] [--json] [--keep-run]\n"

var (
	reviewMaxRe   = regexp.MustCompile(`^[1-9]\d{0,5}s?$`)
	reviewRunIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	reviewShaRe   = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
	reviewSecrets = []*regexp.Regexp{
		regexp.MustCompile(`(?i)(^|/)\.env[^/]*$`), regexp.MustCompile(`(?i)\.pem$`), regexp.MustCompile(`(?i)\.key$`),
		regexp.MustCompile(`(?i)\.p12$`), regexp.MustCompile(`(?i)(^|/)id_(rsa|ed25519|ecdsa)[^/]*$`), regexp.MustCompile(`(?i)(^|/)credentials[^/]*$`),
	}
)

func reviewHandover(args []string, reason handoverReason) int {
	code, err := handover(append([]string{"review"}, args...), reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}

func reviewQuote(s string) string {
	quoted, _ := ledger.Stringify(s, false)
	return string(quoted)
}

// reviewParse is parseReviewArgs: the parsed flags or Node's usage message.
func reviewParse(args []string) (cliParsed, string) {
	p, bad := cliParseFlags(args, cliSpec{values: []string{"base", "acceptance", "intent", "run", "max"}, multi: []string{"files"}, booleans: []string{"json", "keep-run", "help"}})
	if bad != "" {
		return p, bad
	}
	if len(p.positionals) > 0 {
		return p, "unexpected argument " + reviewQuote(p.positionals[0])
	}
	_, hasAcceptance := p.values["acceptance"]
	intent, hasIntent := p.values["intent"]
	if hasAcceptance && hasIntent {
		return p, "give --acceptance or --intent, not both"
	}
	if hasIntent && strings.TrimSpace(intent) == "" {
		return p, "--intent needs text"
	}
	if max, ok := p.values["max"]; ok && !reviewMaxRe.MatchString(max) {
		return p, "--max must be whole seconds, like 900 or 900s"
	}
	if run, ok := p.values["run"]; ok && !reviewRunIDRe.MatchString(run) {
		return p, "--run must match ^[a-z0-9][a-z0-9-]{0,63}$"
	}
	return p, ""
}

// reviewCommitOf is commitOf: the commit sha of a ref, or "".
func reviewCommitOf(ref, root string) string {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return ""
	}
	out, err := gitx.Run(root, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	sha := strings.TrimSpace(out)
	if err != nil || !reviewShaRe.MatchString(sha) {
		return ""
	}
	return sha
}

// reviewResolveBase is resolveBase: the base sha and how it was chosen, or a
// usage message.
func reviewResolveBase(root string, given *string) (sha, from, bad string) {
	if given != nil {
		if sha = reviewCommitOf(*given, root); sha == "" {
			return "", "", "--base " + reviewQuote(*given) + " is not a commit of this repository"
		}
		return sha, *given, ""
	}
	head := reviewCommitOf("HEAD", root)
	if head == "" {
		return "", "", "the repository has no commit yet — commit once, or pass --base"
	}
	candidates := []string{}
	if out, err := gitx.Run(root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil && strings.TrimSpace(out) != "" {
		candidates = append(candidates, strings.TrimSpace(out))
	}
	candidates = append(candidates, "main", "master")
	for _, name := range candidates {
		tip := reviewCommitOf(name, root)
		if tip == "" {
			continue
		}
		out, err := gitx.Run(root, "merge-base", "HEAD", tip)
		base := strings.TrimSpace(out)
		if err != nil || base == "" {
			return "", "", "HEAD and " + name + " share no history — pass --base <ref>"
		}
		if base == head {
			return head, "HEAD (uncommitted changes)", ""
		}
		return base, "merge-base HEAD " + name, ""
	}
	return head, "HEAD (uncommitted changes; no main, master or origin/HEAD)", ""
}

// reviewScopeEnv is the scope module's gitEnv: no inherited GIT_*, no system
// or global git config.
func reviewScopeEnv() []string {
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "GIT_") {
			env = append(env, v)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
}

func reviewSplitNul(text string) []string {
	out := []string{}
	for _, entry := range strings.Split(text, "\x00") {
		if entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// reviewFileSet is computeFileSet's `all`: tracked changes since base union
// untracked files, de-duplicated and sorted. ok is false when git fails or a
// name is not UTF-8 (Node would decode it to U+FFFD): Node serves those.
func reviewFileSet(root, base string) ([]string, bool) {
	if strings.HasPrefix(base, "-") {
		return nil, false
	}
	env := reviewScopeEnv()
	diff, err := gitx.WithEnv(root, env, "diff", "--name-only", "-z", base)
	if err != nil {
		return nil, false
	}
	status, err := gitx.WithEnv(root, env, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return nil, false
	}
	if !utf8.ValidString(diff) || !utf8.ValidString(status) {
		return nil, false
	}
	seen := map[string]bool{}
	all := []string{}
	add := func(file string) {
		if !seen[file] {
			seen[file] = true
			all = append(all, file)
		}
	}
	for _, file := range reviewSplitNul(diff) {
		add(file)
	}
	for _, entry := range reviewSplitNul(status) {
		if strings.HasPrefix(entry, "?? ") {
			add(entry[3:])
		}
	}
	gitx.SortStrings(all)
	return all, true
}

// reviewSkipReason is skipReason: why a file is not sent for review, or "".
func reviewSkipReason(root, rel string, discovered bool) string {
	top := strings.ToLower(strings.SplitN(rel, "/", 2)[0])
	if top == ".code-forge" || top == ".git" {
		return "code-forge state"
	}
	if discovered && rel == ".code-forge.yml" {
		return "code-forge config"
	}
	if strings.HasPrefix(rel, "-") || strings.Contains(rel, "\\") {
		return "unsupported file name"
	}
	for _, re := range reviewSecrets {
		if re.MatchString(rel) {
			return "secret-like name, never sent to a reviewer"
		}
	}
	full := filepath.Join(root, rel)
	info, err := os.Lstat(full)
	if err != nil {
		return "deleted"
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "symlink"
	}
	if !info.Mode().IsRegular() {
		return "not a regular file"
	}
	f, err := os.Open(full)
	if err != nil {
		return "unreadable"
	}
	defer f.Close()
	head := make([]byte, 8000)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return "unreadable"
	}
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return "binary"
	}
	return ""
}

type reviewSkip struct{ file, reason string }

// reviewPlan decides the invocation without writing anything. A nil plan
// means Node serves it, for the returned reason.
func reviewPlan(args []string) (func(o *runOut) int, handoverReason) {
	usage := func(text string) (func(o *runOut) int, handoverReason) {
		return func(o *runOut) int { o.err("review: " + text + "\n"); return 2 }, 0
	}
	parsed, bad := reviewParse(args)
	if bad != "" {
		return func(o *runOut) int { o.err("review: " + bad + "\n" + reviewUsage); return 2 }, 0
	}
	if parsed.booleans["help"] {
		return func(o *runOut) int { o.out(reviewUsage); return 0 }, 0
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, hReviewGit
	}
	root, err := gitx.RepoTop(cwd)
	if err != nil {
		return usage("not inside a git repository")
	}
	var given *string
	if b, ok := parsed.values["base"]; ok {
		given = &b
	}
	sha, from, bad := reviewResolveBase(root, given)
	if bad != "" {
		return usage(bad)
	}
	var candidates []string
	raws, explicit := parsed.multi["files"]
	if explicit {
		seen := map[string]bool{}
		for _, raw := range raws {
			file, err := reviewq.Normalize(raw, cwd, root)
			if err != nil {
				var pathErr *gitx.Error
				if !errors.As(err, &pathErr) {
					return nil, hReviewGit
				}
				return usage("--files " + reviewQuote(raw) + ": " + pathErr.Message)
			}
			if !seen[file] {
				seen[file] = true
				candidates = append(candidates, file)
			}
		}
	} else {
		var ok bool
		if candidates, ok = reviewFileSet(root, sha); !ok {
			return nil, hReviewGit
		}
	}
	skipped := []reviewSkip{}
	files := []string{}
	for _, file := range candidates {
		why := reviewSkipReason(root, file, !explicit)
		if why == "" {
			files = append(files, file)
			continue
		}
		skipped = append(skipped, reviewSkip{file, why})
	}
	if len(files) > 0 {
		// B553: a file to review runs the whole flow in Go
		rvfSortJS(files)
		return rvfPrepare(args, parsed, root, sha, from, files, skipped)
	}
	asJSON := parsed.booleans["json"]
	return func(o *runOut) int {
		for _, s := range skipped {
			o.err("review: skipped " + s.file + " (" + s.reason + ")\n")
		}
		if !asJSON {
			o.out("nothing to review\n")
			return 0
		}
		list := []any{}
		for _, s := range skipped {
			list = append(list, ledger.Object{{Key: "file", Value: s.file}, {Key: "reason", Value: s.reason}})
		}
		totals := ledger.Object{}
		for _, key := range []string{"files", "approved", "unchanged", "with_findings", "findings", "stopped", "unavailable"} {
			totals = append(totals, ledger.Field{Key: key, Value: 0})
		}
		doc := ledger.Object{
			{Key: "ok", Value: true}, {Key: "base", Value: sha}, {Key: "base_from", Value: from},
			{Key: "run", Value: nil}, {Key: "block", Value: nil}, {Key: "files", Value: []any{}},
			{Key: "skipped", Value: list}, {Key: "totals", Value: totals}, {Key: "nothing_to_review", Value: true},
		}
		text, _ := ledger.Stringify(doc, false)
		o.out(string(text) + "\n")
		return 0
	}, 0
}

func runReviewVerb(args []string) int {
	// The Node router answers help before breadcrumbs and handlers.
	if len(args) > 0 && cliHelp(args[0]) {
		fmt.Fprint(os.Stdout, reviewUsage)
		return 0
	}
	home := os.Getenv("HOME")
	if home == "" || !filepath.IsAbs(home) {
		return reviewHandover(args, hReviewHome)
	}
	plan, reason := reviewPlan(args)
	if plan == nil {
		return reviewHandover(args, reason)
	}
	before := gatesBreadcrumbName("review")
	o := &runOut{}
	code := plan(o)
	if code != 0 {
		var message any
		if o.last != "" {
			message = strings.TrimSpace(o.last)
		}
		nativeVerbFailure("review", "", args, code, message, before)
	}
	return code
}
