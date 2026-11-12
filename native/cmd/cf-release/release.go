package main

// `npm run release -- <patch|minor|major|x.y.z>` and `release notes <vX.Y.Z>` (B23): bump every
// version location, move `[Unreleased]` under the new version, run the checks, commit and tag.
// It never pushes and never publishes. Port of scripts/release.mjs (B570).

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const releaseUsage = "usage: npm run release -- <patch|minor|major|x.y.z> [--dry-run] [--date YYYY-MM-DD] [--no-checks] [--allow-branch <name>]"

// refusal stops the release with exit 1; usageErr with exit 2. crash is an unexpected error
// (Node would throw it): exit 1.
type refusal struct{ msg string }
type usageErr struct{ msg string }

func (e *refusal) Error() string  { return e.msg }
func (e *usageErr) Error() string { return e.msg }

type releaseOpts struct {
	bump        string
	dryRun      bool
	date        string
	checks      bool
	allowBranch string
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func parseReleaseArgs(argv []string) (releaseOpts, error) {
	o := releaseOpts{checks: true}
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--dry-run":
			o.dryRun = true
		case a == "--no-checks":
			o.checks = false
		case a == "--date" || a == "--allow-branch":
			v := ""
			if i+1 < len(argv) {
				v = argv[i+1]
			}
			if v == "" || strings.HasPrefix(v, "-") {
				return o, &usageErr{a + " needs a value"}
			}
			if a == "--date" {
				o.date = v
			} else {
				o.allowBranch = v
			}
			i++
		case strings.HasPrefix(a, "-"):
			return o, &usageErr{"unknown option " + a}
		case o.bump != "":
			return o, &usageErr{"one version argument only (got " + o.bump + " and " + a + ")"}
		default:
			o.bump = a
		}
	}
	if o.bump == "" {
		return o, &usageErr{"missing <patch|minor|major|x.y.z>"}
	}
	if o.date != "" && !dateRe.MatchString(o.date) {
		return o, &usageErr{"--date must be YYYY-MM-DD"}
	}
	return o, nil
}

var versionRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

func parseVersion(v string) ([3]float64, bool) {
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return [3]float64{}, false
	}
	var out [3]float64
	for i := 0; i < 3; i++ {
		out[i], _ = strconv.ParseFloat(m[i+1], 64)
	}
	return out, true
}

func compareVersions(a, b string) int {
	x, _ := parseVersion(a)
	y, _ := parseVersion(b)
	for i := 0; i < 3; i++ {
		if x[i] != y[i] {
			if x[i] < y[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func vnum(f float64) string { return jsNumberString(f) }

func nextVersion(current, bump string) (string, error) {
	c, ok := parseVersion(current)
	if !ok {
		return "", &refusal{"package.json version \"" + current + "\" is not x.y.z"}
	}
	switch bump {
	case "major":
		return vnum(c[0]+1) + ".0.0", nil
	case "minor":
		return vnum(c[0]) + "." + vnum(c[1]+1) + ".0", nil
	case "patch":
		return vnum(c[0]) + "." + vnum(c[1]) + "." + vnum(c[2]+1), nil
	}
	explicit := strings.TrimPrefix(bump, "v")
	if _, ok := parseVersion(explicit); !ok {
		return "", &usageErr{"\"" + bump + "\" is not patch, minor, major or x.y.z"}
	}
	return explicit, nil
}

// ── field-exact file edits ──

type jsonEdit struct {
	keys  []string
	value string
}

var indentRe = regexp.MustCompile(`\n([ \t]+)"`)

func setJSONFields(text string, edits []jsonEdit) (string, error) {
	data, err := jsParse(text)
	if err != nil {
		return "", err
	}
	for _, e := range edits {
		parent, ok := jsField(data, e.keys[:len(e.keys)-1])
		last := e.keys[len(e.keys)-1]
		obj, isObj := parent.(*jsObject)
		if !ok || !isObj {
			return "", &refusal{"no string field " + strings.Join(e.keys, ".")}
		}
		if v, has := obj.get(last); !has {
			return "", &refusal{"no string field " + strings.Join(e.keys, ".")}
		} else if _, isStr := v.(string); !isStr {
			return "", &refusal{"no string field " + strings.Join(e.keys, ".")}
		}
		obj.set(last, e.value)
	}
	indent, shown := "  ", "2"
	if m := indentRe.FindStringSubmatch(text); m != nil {
		indent, shown = m[1], jsQuote(m[1])
	}
	newline := ""
	if strings.HasSuffix(text, "\n") {
		newline = "\n"
	}
	orig, _ := jsParse(text)
	if jsStringify(orig, indent)+newline != text {
		return "", &refusal{"a JSON file is not in canonical JSON.stringify layout (indent " + shown + "); reformat it first so the release only changes the version"}
	}
	return jsStringify(data, indent) + newline, nil
}

func setPinnedVersion(text, current, next, file string) (string, error) {
	lines := strings.Split(text, "\n")
	var at []int
	for i, l := range lines {
		if strings.HasPrefix(l, `PINNED_VERSION="`) {
			at = append(at, i)
		}
	}
	if len(at) != 1 {
		return "", &refusal{fmt.Sprintf(`%s: expected exactly 1 PINNED_VERSION="…" line, found %d`, file, len(at))}
	}
	if lines[at[0]] != `PINNED_VERSION="`+current+`"` {
		return "", &refusal{file + ": the PINNED_VERSION line is " + lines[at[0]] + ", package.json says " + current}
	}
	lines[at[0]] = `PINNED_VERSION="` + next + `"`
	return strings.Join(lines, "\n"), nil
}

type location struct {
	file     string
	required bool
	json     [][]string
	shim     bool
}

var locations = []location{
	{file: "package.json", required: true, json: [][]string{{"version"}}},
	{file: "npm-shrinkwrap.json", json: [][]string{{"version"}, {"packages", "", "version"}}},
	{file: "package-lock.json", json: [][]string{{"version"}, {"packages", "", "version"}}},
	{file: ".claude-plugin/plugin.json", json: [][]string{{"version"}}},
	{file: "skill/scripts/forge", shim: true},
}

func fieldLabel(keys []string) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		if k == "" {
			parts[i] = `[""]`
		} else {
			parts[i] = k
		}
	}
	return strings.Replace(strings.Join(parts, "."), ".[", "[", 1)
}

type foundLoc struct {
	file, label, before string
	bump                func(string) (string, error)
}

func readLocations(root, current string) ([]foundLoc, error) {
	var found []foundLoc
	for _, loc := range locations {
		abs := filepath.Join(root, loc.file)
		if !exists(abs) {
			if loc.required {
				return nil, &refusal{loc.file + " not found in " + root}
			}
			continue
		}
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, err
		}
		before := string(data)
		loc := loc
		if loc.shim {
			if _, err := setPinnedVersion(before, current, current, loc.file); err != nil {
				return nil, err
			}
			found = append(found, foundLoc{file: loc.file, label: "PINNED_VERSION", before: before,
				bump: func(v string) (string, error) { return setPinnedVersion(before, current, v, loc.file) }})
			continue
		}
		parsed, err := jsParse(before)
		if err != nil {
			return nil, err
		}
		var labels []string
		for _, keys := range loc.json {
			got, ok := jsField(parsed, keys)
			if s, isStr := got.(string); !ok || !isStr || s != current {
				shown := "undefined"
				if ok {
					shown = jsStringify(got, "")
				}
				return nil, &refusal{loc.file + ": " + fieldLabel(keys) + " is " + shown + ", package.json says " + current}
			}
			labels = append(labels, fieldLabel(keys))
		}
		found = append(found, foundLoc{file: loc.file, label: strings.Join(labels, ", "), before: before,
			bump: func(v string) (string, error) {
				edits := make([]jsonEdit, len(loc.json))
				for i, keys := range loc.json {
					edits[i] = jsonEdit{keys, v}
				}
				return setJSONFields(before, edits)
			}})
	}
	return found, nil
}

// ── CHANGELOG ──

var (
	unreleasedLinkRe  = regexp.MustCompile(`(?i)^\[Unreleased\]:\s*\S+/compare/\S+\.\.\.HEAD\s*$`)
	unreleasedLinkCap = regexp.MustCompile(`(?i)^\[Unreleased\]:\s*(\S+)/compare/(\S+)\.\.\.HEAD\s*$`)
)

func releaseChangelog(text, version, date string) (string, string, error) {
	lines := strings.Split(text, "\n")
	sec := findUnreleased(lines)
	if sec == nil {
		return "", "", &refusal{`CHANGELOG.md has no "## [Unreleased]" heading: add an entry with npm run changelog -- added "…"`}
	}
	body := append([]string{}, lines[sec.start+1:sec.end]...)
	hasEntries := false
	for _, s := range parseSubsections(lines, sec.start+1, sec.end) {
		if len(s.bullets) > 0 {
			hasEntries = true
		}
	}
	if !hasEntries {
		return "", "", &refusal{`CHANGELOG.md: "## [Unreleased]" has no entries: add one with npm run changelog -- added "…"`}
	}
	notes := jsTrim(strings.Join(body, "\n"))
	out := append([]string{}, lines[:sec.start]...)
	out = append(out, "## [Unreleased]", "", "## ["+version+"] — "+date)
	out = append(out, body...)
	out = append(out, lines[sec.end:]...)
	for i, l := range out {
		if unreleasedLinkRe.MatchString(l) {
			m := unreleasedLinkCap.FindStringSubmatch(l)
			out = splice(out, i, 1, "[Unreleased]: "+m[1]+"/compare/v"+version+"...HEAD", "["+version+"]: "+m[1]+"/compare/"+m[2]+"...v"+version)
			break
		}
	}
	return strings.Join(out, "\n"), notes, nil
}

var latestLineRe = regexp.MustCompile(`(?m)^(Latest: )\d+\.\d+\.\d+( - see )`)

func releaseReadme(text, next string) string {
	return latestLineRe.ReplaceAllString(text, "${1}"+next+"${2}")
}

// replacePins updates `@codedology/code-forge@<current>` (followed by whitespace or the end) and
// whole `@codedology/code-forge <current>` lines in the step-by-step doc.
func replacePins(text, current, next string) string {
	needle := "@codedology/code-forge@" + current
	var b strings.Builder
	rest := text
	for {
		i := strings.Index(rest, needle)
		if i < 0 {
			b.WriteString(rest)
			break
		}
		after := rest[i+len(needle):]
		r := []rune(after)
		if len(r) == 0 || jsSpace(r[0]) {
			b.WriteString(rest[:i] + "@codedology/code-forge@" + next)
		} else {
			b.WriteString(rest[:i+len(needle)])
		}
		rest = after
	}
	lineRe := regexp.MustCompile(`(?m)^(@codedology/code-forge )` + regexp.QuoteMeta(current) + `(\r?)$`)
	return lineRe.ReplaceAllString(b.String(), "${1}"+next+"${2}")
}

func lineDiff(a, b string, context int) []string {
	x := strings.Split(a, "\n")
	y := strings.Split(b, "\n")
	var out []string
	if len(x) == len(y) {
		for i, line := range x {
			if line == y[i] {
				continue
			}
			if i > 0 {
				out = append(out, "  "+x[i-1])
			}
			out = append(out, "- "+line, "+ "+y[i])
		}
		return out
	}
	p := 0
	for p < len(x) && p < len(y) && x[p] == y[p] {
		p++
	}
	s := 0
	for s < len(x)-p && s < len(y)-p && x[len(x)-1-s] == y[len(y)-1-s] {
		s++
	}
	for i := max(0, p-context); i < p; i++ {
		out = append(out, "  "+x[i])
	}
	for i := p; i < len(x)-s; i++ {
		out = append(out, "- "+x[i])
	}
	for i := p; i < len(y)-s; i++ {
		out = append(out, "+ "+y[i])
	}
	for i := len(x) - s; i < min(len(x), len(x)-s+context); i++ {
		out = append(out, "  "+x[i])
	}
	return out
}

// ── processes ──

func (a *app) git(root string, args []string, timeout time.Duration) result {
	return spawn(root, append([]string{"git"}, args...), timeout, gitChildEnv())
}

func (a *app) gitOk(root string, args ...string) (string, error) {
	r := a.git(root, args, 60*time.Second)
	if r.code != 0 {
		return "", &refusal{"git " + strings.Join(args, " ") + " failed: " + jsTrim(or(r.stderr, r.stdout))}
	}
	return r.stdout, nil
}

func runCmd(root string, timeout time.Duration, argv ...string) result {
	return spawn(root, argv, timeout, nil)
}

func onPath(name string) bool {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(dir, name)); err == nil && st.Mode().IsRegular() {
			return true
		}
	}
	return false
}

func (a *app) warn(msg string) { a.out("WARN  " + msg + "\n") }
func (a *app) ok(msg string)   { a.out("ok    " + msg + "\n") }

// ── preflight ──

var shaRe = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

func (a *app) docsBlockFailure(root string) (string, error) {
	tag, err := latestReleaseTag(root)
	if err != nil {
		return "", err
	}
	if tag == "" {
		a.warn("docs block check skipped: no earlier v* tag is an ancestor of HEAD")
		return "", nil
	}
	file := "docs/release-docs.json"
	again := "run the docs block again: npm run docs-block -- brief"
	data, err := os.ReadFile(filepath.Join(root, file))
	var stamp any
	if err == nil {
		stamp, err = jsParse(string(data))
	}
	if err != nil {
		return file + " is missing or invalid; " + again, nil
	}
	obj, _ := stamp.(*jsObject)
	var through, stampTag string
	valid := obj != nil
	if valid {
		t, ok := obj.get("through")
		through, valid = t.(string)
		valid = valid && ok && shaRe.MatchString(through)
	}
	if valid {
		t, _ := obj.get("tag")
		stampTag, valid = t.(string)
		valid = valid && strings.HasPrefix(stampTag, "v")
	}
	if !valid {
		return file + " is invalid (expected through commit SHA and release tag); " + again, nil
	}
	if jsTrim(a.git(root, []string{"cat-file", "-t", through}, 0).stdout) != "commit" ||
		a.git(root, []string{"merge-base", "--is-ancestor", through, "HEAD"}, 0).code != 0 {
		return file + ": through is not a commit that is an ancestor of HEAD; " + again, nil
	}
	if a.git(root, []string{"merge-base", "--is-ancestor", "refs/tags/" + tag, through}, 0).code != 0 {
		return file + " is older than the latest release " + tag + "; " + again, nil
	}
	if stampTag != tag {
		return file + ": tag must be the latest release " + tag + "; " + again, nil
	}
	out, err := a.gitOk(root, "diff", "--no-renames", "--name-only", "-z", through, "HEAD")
	if err != nil {
		return "", err
	}
	var code []string
	for _, name := range splitNul(out) {
		if !strings.HasPrefix(name, "docs/") && name != "README.md" && name != "CHANGELOG.md" {
			code = append(code, name)
		}
	}
	if len(code) > 0 {
		return file + ": files changed after the stamp: " + strings.Join(code[:min(5, len(code))], ", ") + "; " + again, nil
	}
	a.ok("docs block covers " + through + " since " + tag)
	return "", nil
}

type preflightArgs struct {
	branch, allowBranch, tag, current, next, name string
	checks                                        bool
}

func (a *app) preflight(root string, p preflightArgs, fail func(string) error) error {
	inside := a.git(root, []string{"rev-parse", "--is-inside-work-tree"}, 0)
	if inside.code != 0 || jsTrim(inside.stdout) != "true" {
		return &refusal{root + " is not a git work tree"}
	}
	status, err := a.gitOk(root, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return err
	}
	if jsTrim(status) != "" {
		if err := fail("the working tree is not clean:\n" + jsTrimEnd(status)); err != nil {
			return err
		}
	} else {
		a.ok("working tree is clean")
	}
	wanted := or(p.allowBranch, "main")
	if p.branch != wanted {
		if err := fail("on branch \"" + p.branch + "\", expected \"" + wanted + "\" (use --allow-branch " + p.branch + " to release from it)"); err != nil {
			return err
		}
	} else {
		a.ok("on branch " + p.branch)
	}

	remote := a.git(root, []string{"ls-remote", "--heads", "origin", "refs/heads/" + p.branch}, 60*time.Second)
	remoteSha := ""
	if remote.code == 0 {
		remoteSha = jsSplitWS(remote.stdout)[0]
	}
	headOut, err := a.gitOk(root, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	head := jsTrim(headOut)
	var msg string
	switch {
	case remote.code != 0:
		msg = "cannot read origin: " + jsTrim(remote.stderr)
	case remoteSha == "":
		msg = "origin has no branch " + p.branch + ": push it first"
	case remoteSha == head:
		a.ok("up to date with origin/" + p.branch)
	case a.git(root, []string{"cat-file", "-e", remoteSha + "^{commit}"}, 0).code != 0:
		msg = p.branch + " is behind origin/" + p.branch + " (origin has commits you do not have): pull first"
	case a.git(root, []string{"merge-base", "--is-ancestor", head, remoteSha}, 0).code == 0:
		msg = p.branch + " is behind origin/" + p.branch + ": pull first"
	case a.git(root, []string{"merge-base", "--is-ancestor", remoteSha, head}, 0).code != 0:
		msg = p.branch + " and origin/" + p.branch + " have diverged: reconcile them first"
	default:
		a.warn(p.branch + " is ahead of origin/" + p.branch + ": the push will also send those commits")
	}
	if msg != "" {
		if err := fail(msg); err != nil {
			return err
		}
	}

	view := runCmd(root, 30*time.Second, "npm", "view", p.name, "version")
	published := jsTrim(view.stdout)
	_, publishedOK := parseVersion(published)
	msg = ""
	switch {
	case compareVersions(p.next, p.current) <= 0:
		msg = "the new version " + p.next + " is not greater than the current " + p.current
	case view.code == 0 && publishedOK:
		if compareVersions(p.next, published) <= 0 {
			msg = "the new version " + p.next + " is not greater than the latest on npm (" + published + ")"
		} else {
			a.ok(p.next + " > " + p.current + " (package.json) and > " + published + " (npm)")
		}
	case strings.Contains(view.stderr, "E404"):
		a.ok(p.next + " > " + p.current + "; " + p.name + " is not on npm yet")
	default:
		a.warn("could not read the latest version from npm (" + firstLine(jsTrim(or(view.stderr, view.errMsg, "no output"))) + "); compared with package.json only")
	}
	if msg != "" {
		if err := fail(msg); err != nil {
			return err
		}
	}

	remoteTag := a.git(root, []string{"ls-remote", "--tags", "origin", "refs/tags/" + p.tag}, 60*time.Second)
	msg = ""
	switch {
	case a.git(root, []string{"rev-parse", "-q", "--verify", "refs/tags/" + p.tag}, 0).code == 0:
		msg = "the tag " + p.tag + " already exists locally"
	case remoteTag.code != 0:
		msg = "cannot read origin's tags: " + jsTrim(remoteTag.stderr)
	case jsTrim(remoteTag.stdout) != "":
		msg = "the tag " + p.tag + " already exists on origin"
	default:
		a.ok("tag " + p.tag + " is free (local and origin)")
	}
	if msg != "" {
		if err := fail(msg); err != nil {
			return err
		}
	}

	if p.checks {
		failure, err := a.docsBlockFailure(root)
		if err != nil {
			return err
		}
		if failure != "" {
			if err := fail(failure); err != nil {
				return err
			}
		}
	} else {
		a.warn("!!! --no-checks: docs block preflight check was NOT run !!!")
	}
	return nil
}

// ── checks ──

func hasNative(root string) bool { return exists(filepath.Join(root, "native", "go.mod")) }

func (a *app) buildNative(root string) string {
	if !hasNative(root) {
		return ""
	}
	r := runCmd(root, 0, "node", filepath.Join(root, "scripts", "build-native.mjs"))
	if r.code != 0 {
		return fmt.Sprintf("build-native failed (exit %d): %s", r.code, jsTrim(or(r.stderr, r.stdout)))
	}
	a.out(r.stdout)
	a.ok("build-native: all four release targets")
	return ""
}

func (a *app) checkPack(root, name, version string) string {
	r := runCmd(root, 0, "npm", "pack", "--dry-run", "--json")
	if r.code != 0 {
		return fmt.Sprintf("npm pack --dry-run failed (exit %d): %s", r.code, jsTrim(or(r.stderr, r.stdout)))
	}
	parsed, err := jsParse(r.stdout)
	if err != nil || parsed == nil {
		return "npm pack --dry-run --json printed no JSON"
	}
	var pack any
	packOK := false
	if arr, isArr := parsed.([]any); isArr && len(arr) > 0 {
		pack, packOK = arr[0], true
	} else if obj, isObj := parsed.(*jsObject); isObj {
		pack, packOK = obj.vals["0"]
	}
	field := func(k string) (any, bool) {
		if !packOK {
			return nil, false
		}
		obj, isObj := pack.(*jsObject)
		if !isObj {
			return nil, false
		}
		return obj.get(k)
	}
	pn, pnOK := field("name")
	pv, pvOK := field("version")
	if s, isStr := pn.(string); !pnOK || !isStr || s != name {
		return "npm pack --dry-run: the tarball is " + jsTemplate(pn, pnOK) + "@" + jsTemplate(pv, pvOK) + ", expected " + name + "@" + version
	}
	if s, isStr := pv.(string); !pvOK || !isStr || s != version {
		return "npm pack --dry-run: the tarball is " + jsTemplate(pn, pnOK) + "@" + jsTemplate(pv, pvOK) + ", expected " + name + "@" + version
	}
	native := hasNative(root)
	sizeV, _ := field("size")
	size, sizeOK := sizeV.(jsNum)
	finite := sizeOK && !isNaNInf(float64(size))
	if native && (!finite || size < 0) {
		return "npm pack --dry-run: missing package size"
	}
	if finite {
		a.ok(fmt.Sprintf("package size: %s bytes (%s MB; limit <20 MB)", jsNumberString(float64(size)), toFixed2(float64(size)/1_000_000)))
		if size >= 20_000_000 {
			return "published package must be under 20 MB"
		}
	}
	filesV, filesOK := field("files")
	files, _ := filesV.([]any)
	if native {
		for _, target := range []string{"darwin-arm64", "darwin-x64", "linux-x64", "linux-arm64"} {
			found := false
			for _, f := range files {
				if o, isObj := f.(*jsObject); isObj {
					if p, _ := o.get("path"); p == "native/bin/"+target+"/code-forge-native" {
						found = true
					}
				}
			}
			if !found {
				return "npm pack --dry-run: missing native binary " + target
			}
		}
	}
	count, countOK := field("entryCount")
	shown := ""
	if countOK && count != nil {
		shown = jsTemplate(count, true)
	} else if arr, isArr := filesV.([]any); filesOK && isArr {
		shown = strconv.Itoa(len(arr))
	} else {
		shown = "undefined"
	}
	a.ok("npm pack --dry-run: " + name + "@" + version + ", " + shown + " files")
	return ""
}

func isNaNInf(f float64) bool {
	return f != f || f > 1.7976931348623157e308 || f < -1.7976931348623157e308
}

// toFixed2 is Number#toFixed(2) for a size: an exact tie rounds up, as in JavaScript (Go's
// formatting rounds a tie to even).
func toFixed2(f float64) string {
	exact := strconv.FormatFloat(f, 'f', 1100, 64)
	if dot := strings.IndexByte(exact, '.'); dot >= 0 && len(exact) > dot+3 &&
		exact[dot+3] == '5' && strings.Trim(exact[dot+4:], "0") == "" && f >= 0 {
		return strconv.FormatFloat(f+0.001, 'f', 2, 64)
	}
	return strconv.FormatFloat(f, 'f', 2, 64)
}

var (
	locationRe = regexp.MustCompile(`^\s*location: ['"](.+?)['"]\s*$`)
	testAtRe   = regexp.MustCompile(`^test at (.+?)\s*$`)
	lineColRe  = regexp.MustCompile(`:\d+(?::\d+)?$`)
	jsFileRe   = regexp.MustCompile(`\.(?:mjs|cjs|js)$`)
	failingRe  = regexp.MustCompile(`^\s*(?:[ℹ✖]\s+)?failing tests:\s*$`)
	tapNotOkRe = regexp.MustCompile(`^\s*not ok \d+ - (.+?)\s*$`)
	specFailRe = regexp.MustCompile(`^\s*✖ (.+?)\s*$`)
	specMsRe   = regexp.MustCompile(` \(\d+(?:\.\d+)?ms\)$`)
)

// failingTestFiles reads the file locations Node's TAP diagnostics or spec summary print; any
// unknown or outside path gives none.
func failingTestFiles(root, output string) []string {
	var files []string
	seen := map[string]bool{}
	for _, line := range strings.Split(stripVT(output), "\n") {
		m := locationRe.FindStringSubmatch(line)
		if m == nil {
			m = testAtRe.FindStringSubmatch(line)
		}
		if m == nil {
			continue
		}
		loc := lineColRe.ReplaceAllString(m[1], "")
		var absolute string
		if strings.HasPrefix(loc, "file:") {
			u, err := url.Parse(loc)
			if err != nil || u.Scheme != "file" || (u.Host != "" && u.Host != "localhost") || !strings.HasPrefix(u.Path, "/") {
				return nil
			}
			absolute = filepath.Clean(u.Path)
		} else {
			absolute = resolvePath(root, loc)
		}
		rel, err := filepath.Rel(root, absolute)
		if err != nil || rel == "" || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) || !jsFileRe.MatchString(rel) {
			return nil
		}
		st, err := os.Stat(absolute)
		if err != nil || !st.Mode().IsRegular() {
			return nil
		}
		if !seen[rel] {
			seen[rel] = true
			files = append(files, rel)
		}
	}
	return files
}

func tail30(output string) string {
	lines := strings.Split(jsTrimEnd(output), "\n")
	if len(lines) > 30 {
		lines = lines[len(lines)-30:]
	}
	return strings.Join(lines, "\n")
}

func (a *app) failed(root, label string, r result) string {
	output := r.stdout + r.stderr
	tail := tail30(output)
	if label == "npm test" {
		var names []string
		seen := map[string]bool{}
		summary := false
		for _, line := range strings.Split(stripVT(output), "\n") {
			if failingRe.MatchString(line) {
				summary = true
				continue
			}
			name := ""
			if m := tapNotOkRe.FindStringSubmatch(line); m != nil {
				name = m[1]
			} else if summary {
				if m := specFailRe.FindStringSubmatch(line); m != nil {
					name = specMsRe.ReplaceAllString(m[1], "")
				}
			}
			if name != "" && !seen[name] {
				seen[name] = true
				names = append(names, name)
			}
		}
		temp := filepath.Join(root, ".code-forge-tmp")
		_ = os.MkdirAll(temp, 0o755)
		log := filepath.Join(temp, fmt.Sprintf("release-npm-test-%d-%d.log", a.now().UnixMilli(), a.pid))
		_ = os.WriteFile(log, []byte(output), 0o644)
		failures := ""
		if len(names) > 0 {
			failures = "\nfailing tests:\n" + strings.Join(prefixAll("  - ", names), "\n")
		}
		msg := fmt.Sprintf("%s failed (exit %d)%s\nFull output: %s", label, r.code, failures, log)
		if tail != "" {
			msg += "\nLast 30 lines:\n" + tail
		}
		return msg
	}
	msg := fmt.Sprintf("%s failed (exit %d)", label, r.code)
	if tail != "" {
		msg += ":\n" + tail
	}
	return msg
}

func prefixAll(p string, items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = p + s
	}
	return out
}

func (a *app) runChecks(root, name, version string) (string, error) {
	genDoc := filepath.Join(root, "scripts", "gen-config-doc.mjs")
	if !exists(genDoc) {
		return "gen-config-doc --check failed: scripts/gen-config-doc.mjs not found", nil
	}
	r := runCmd(root, 120*time.Second, "node", genDoc, "--check")
	if r.code != 0 {
		return a.failed(root, "gen-config-doc --check", r), nil
	}
	a.ok("gen-config-doc --check")

	commandsDoc := filepath.Join(root, "scripts", "gen-commands-doc.mjs")
	if !exists(commandsDoc) {
		return "gen-commands-doc --check failed: scripts/gen-commands-doc.mjs not found", nil
	}
	r = runCmd(root, 120*time.Second, "node", commandsDoc, "--check")
	if r.code != 0 {
		return a.failed(root, "gen-commands-doc --check", r), nil
	}
	a.ok("gen-commands-doc --check")

	a.out("check npm test …\n")
	r = runCmd(root, 0, "npm", "test")
	if r.code != 0 {
		failure := a.failed(root, "npm test", r)
		files := failingTestFiles(root, r.stdout+r.stderr)
		if len(files) == 0 || r.failed() {
			return failure, nil
		}
		a.out(failure + "\n")
		passed := true
		for _, file := range files {
			a.out("check npm test (alone: " + file + ") …\n")
			alone := runCmd(root, 0, "npm", "test", "--", "--test-concurrency=1", file)
			a.out(alone.stdout)
			a.err(alone.stderr)
			a.out(fmt.Sprintf("npm test (alone: %s) exit %d\n", file, alone.code))
			if alone.code != 0 {
				passed = false
			}
		}
		if !passed {
			return failure + "\nisolated rerun failed", nil
		}
		headOut, err := a.gitOk(root, "rev-parse", "HEAD")
		if err != nil {
			return "", err
		}
		head := jsTrim(headOut)
		ci := runCmd(root, 60*time.Second, "gh", "run", "list", "--commit", head, "--workflow", "CI", "--limit", "1",
			"--json", "headSha,status,conclusion")
		green := false
		if runs, err := jsParse(ci.stdout); err == nil && ci.code == 0 {
			if arr, isArr := runs.([]any); isArr && len(arr) == 1 {
				if o, isObj := arr[0].(*jsObject); isObj {
					sha, _ := o.get("headSha")
					st, _ := o.get("status")
					concl, _ := o.get("conclusion")
					green = sha == head && st == "completed" && concl == "success"
				}
			}
		}
		if !green {
			extra := ""
			if e := jsTrim(ci.stderr); e != "" {
				extra = ": " + e
			}
			return failure + "\nCI for HEAD " + head + " is not green" + extra, nil
		}
		a.out("local flaky (passed alone, CI green): " + strings.Join(files, ", ") + "\n")
	} else {
		a.ok("npm test")
	}

	r = runCmd(root, 0, "npm", "run", "typecheck")
	if r.code != 0 {
		return a.failed(root, "npm run typecheck", r), nil
	}
	a.ok("npm run typecheck")

	if f := a.checkPack(root, name, version); f != "" {
		return f, nil
	}

	r = runCmd(root, 0, "npm", "ls", "--json")
	tree, err := jsParse(r.stdout)
	if err != nil {
		return a.failed(root, "npm ls --json (no JSON)", r), nil
	}
	var bad []string
	var walk func(node any, at string)
	walk = func(node any, at string) {
		obj, isObj := node.(*jsObject)
		if !isObj {
			return
		}
		deps, _ := obj.get("dependencies")
		d, isObj := deps.(*jsObject)
		if !isObj {
			return
		}
		for _, dep := range d.keys {
			info := d.vals[dep]
			if io, isObj := info.(*jsObject); isObj {
				if jsTruthy(io.vals["missing"]) {
					bad = append(bad, at+dep+" (missing)")
				}
				if jsTruthy(io.vals["invalid"]) {
					bad = append(bad, at+dep+" (invalid)")
				}
			}
			walk(info, at+dep+" > ")
		}
	}
	walk(tree, "")
	var problems []any
	if obj, isObj := tree.(*jsObject); isObj {
		problems, _ = obj.vals["problems"].([]any)
	}
	if len(bad) > 0 || len(problems) > 0 {
		detail := strings.Join(bad, ", ")
		if detail == "" {
			parts := make([]string, len(problems))
			for i, p := range problems {
				if p != nil {
					parts[i] = jsTemplate(p, true)
				}
			}
			detail = strings.Join(parts, "; ")
		}
		return fmt.Sprintf("npm ls: %d invalid or missing: %s", len(bad), detail), nil
	}
	a.ok("npm ls: 0 invalid, 0 missing")

	if onPath("claude") {
		r = runCmd(root, 120*time.Second, "claude", "plugin", "validate", ".")
		if r.code != 0 {
			return a.failed(root, "claude plugin validate .", r), nil
		}
		a.ok("claude plugin validate .")
	} else {
		a.warn(`the claude CLI is not on PATH: "claude plugin validate ." was skipped; install it and run "claude plugin validate ." before you publish`)
	}
	return "", nil
}

func jsTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case jsNum:
		return t != 0 && t == t
	case string:
		return t != ""
	}
	return true
}

// ── release notes ──

var (
	ghURLRe   = regexp.MustCompile(`github\.com[/:]([^/\s]+)/([^/\s]+?)(?:\.git)?/?$`)
	ghShortRe = regexp.MustCompile(`^(?:github:)?([\w.-]+)/([\w.-]+)$`)
)

func githubRepo(repository any) string {
	var u string
	switch t := repository.(type) {
	case string:
		u = t
	case *jsObject:
		if v, ok := t.get("url"); ok && v != nil {
			u = jsTemplate(v, true)
		}
	}
	if m := ghURLRe.FindStringSubmatch(u); m != nil {
		return m[1] + "/" + m[2]
	}
	if m := ghShortRe.FindStringSubmatch(u); m != nil {
		return m[1] + "/" + m[2]
	}
	return ""
}

func releaseNotes(changelog string, pkg *jsObject, version string) (string, bool) {
	lines := strings.Split(changelog, "\n")
	sec := findSection(lines, version)
	if sec == nil {
		return "", false
	}
	body := jsTrim(strings.Join(lines[sec.start+1:sec.end], "\n"))
	name, nameOK := pkg.get("name")
	links := []string{"npm: https://www.npmjs.com/package/" + jsTemplate(name, nameOK) + "/v/" + version}
	repoV, _ := pkg.get("repository")
	if repo := githubRepo(repoV); repo != "" {
		links = append(links, "Full changelog: https://github.com/"+repo+"/blob/v"+version+"/CHANGELOG.md")
	}
	out := ""
	if body != "" {
		out = body + "\n\n"
	}
	return out + strings.Join(links, "\n") + "\n", true
}

func readPackage(root string) (*jsObject, string, error) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil, "", err
	}
	v, err := jsParse(string(data))
	if err != nil {
		return nil, "", err
	}
	obj, ok := v.(*jsObject)
	if !ok {
		obj = &jsObject{vals: map[string]any{}}
	}
	return obj, string(data), nil
}

func (a *app) notesMain(argv []string, root string) int {
	version, out := "", ""
	usage := "usage: node scripts/release.mjs notes <vX.Y.Z> [--out <file>]\n"
	for i := 0; i < len(argv); i++ {
		switch {
		case argv[i] == "--out" && i+1 < len(argv) && argv[i+1] != "":
			out = argv[i+1]
			i++
		case version == "" && !strings.HasPrefix(argv[i], "-"):
			version = strings.TrimPrefix(argv[i], "v")
		default:
			a.err(usage)
			return 2
		}
	}
	if _, ok := parseVersion(version); !ok {
		a.err(usage)
		return 2
	}
	pkg, _, err := readPackage(root)
	if err != nil {
		a.err(err.Error() + "\n")
		return 1
	}
	changelog, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		a.err(err.Error() + "\n")
		return 1
	}
	notes, found := releaseNotes(string(changelog), pkg, version)
	if !found {
		a.err("REFUSED: CHANGELOG.md has no \"## [" + version + "]\" section\n")
		return 1
	}
	if out != "" {
		if err := os.WriteFile(resolvePath(root, out), []byte(notes), 0o644); err != nil {
			a.err(err.Error() + "\n")
			return 1
		}
		a.out("release notes for v" + version + " written to " + out + "\n")
	} else {
		a.out(notes)
	}
	return 0
}

// ── main ──

type planFile struct {
	file, label, before, after string
}

func (a *app) localToday() string { return a.now().Local().Format("2006-01-02") }

func releaseMain(a *app, argv []string, root string) int {
	if len(argv) > 0 && argv[0] == "notes" {
		return a.notesMain(argv[1:], root)
	}
	opts, err := parseReleaseArgs(argv)
	if err != nil {
		a.err(err.Error() + "\n" + releaseUsage + "\n")
		return 2
	}
	var written []planFile
	restore := func() {
		for _, w := range written {
			_ = os.WriteFile(filepath.Join(root, w.file), []byte(w.before), 0o644)
		}
	}
	code, err := a.release(root, opts, &written, restore)
	if err == nil {
		return code
	}
	if len(written) > 0 {
		restore()
	}
	var ref *refusal
	var use *usageErr
	switch {
	case errors.As(err, &use):
		a.err("\nREFUSED: " + use.msg + "\n")
		return 2
	case errors.As(err, &ref):
		a.err("\nREFUSED: " + ref.msg + "\n")
		return 1
	}
	a.err(err.Error() + "\n")
	return 1
}

func (a *app) release(root string, opts releaseOpts, written *[]planFile, restore func()) (int, error) {
	pkg, _, err := readPackage(root)
	if err != nil {
		return 0, err
	}
	nameV, nameOK := pkg.get("name")
	name := jsTemplate(nameV, nameOK)
	curV, curOK := pkg.get("version")
	current := jsTemplate(curV, curOK)
	next, err := nextVersion(current, opts.bump)
	if err != nil {
		return 0, err
	}
	tag := "v" + next
	date := or(opts.date, a.localToday())
	branchOut, err := a.gitOk(root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return 0, err
	}
	branch := jsTrim(branchOut)

	dry := ""
	if opts.dryRun {
		dry = " (dry run: nothing is written)"
	}
	a.out("release " + name + ": " + current + " → " + next + dry + "\n\n== 1. preflight ==\n")
	locs, err := readLocations(root, current)
	if err != nil {
		return 0, err
	}
	changelogPath := filepath.Join(root, "CHANGELOG.md")
	if !exists(changelogPath) {
		return 0, &refusal{"CHANGELOG.md not found"}
	}
	clData, err := os.ReadFile(changelogPath)
	if err != nil {
		return 0, err
	}
	changelogBefore := string(clData)
	readmeData, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		return 0, &refusal{"README.md is missing or unreadable"}
	}
	readmeBefore := string(readmeData)
	changelogAfter, notes, err := releaseChangelog(changelogBefore, next, date)
	if err != nil {
		return 0, err
	}
	a.ok("CHANGELOG.md [Unreleased] has entries")
	readmeAfter := releaseReadme(readmeBefore, next)
	docsFile := "docs/step-by-step.md"
	docsBefore := ""
	if d, err := os.ReadFile(filepath.Join(root, docsFile)); err == nil {
		docsBefore = string(d)
	}
	docsAfter := replacePins(docsBefore, current, next)

	var refusals []string
	err = a.preflight(root, preflightArgs{branch: branch, allowBranch: opts.allowBranch, tag: tag, current: current,
		next: next, name: name, checks: opts.checks}, func(msg string) error {
		if !opts.dryRun {
			return &refusal{msg}
		}
		refusals = append(refusals, msg)
		a.out("FAIL  " + msg + "\n")
		return nil
	})
	if err != nil {
		return 0, err
	}

	var plan []planFile
	for _, l := range locs {
		after, err := l.bump(next)
		if err != nil {
			return 0, err
		}
		plan = append(plan, planFile{l.file, l.label, l.before, after})
	}
	plan = append(plan, planFile{"CHANGELOG.md", "[Unreleased] → [" + next + "] — " + date, changelogBefore, changelogAfter})
	if readmeAfter != readmeBefore {
		plan = append(plan, planFile{"README.md", "Latest: " + current + " → " + next, readmeBefore, readmeAfter})
	}
	if docsAfter != docsBefore {
		plan = append(plan, planFile{docsFile, "version pins: " + current + " → " + next, docsBefore, docsAfter})
	}
	textFiles := map[string]bool{"CHANGELOG.md": true, "README.md": true, docsFile: true}

	a.out("\n== 2. bump + 3. CHANGELOG and README ==\n")
	for _, f := range plan {
		suffix := ""
		if !textFiles[f.file] {
			suffix = "  " + current + " → " + next
		}
		a.out(f.file + ": " + f.label + suffix + "\n")
		if opts.dryRun || textFiles[f.file] {
			for _, l := range lineDiff(f.before, f.after, 3) {
				a.out("    " + l + "\n")
			}
		}
	}

	if opts.dryRun {
		a.out("\n== plan (dry run) ==\n")
		if hasNative(root) {
			a.out("would build-native: four release targets; package size must be under 20 MB\n")
		}
		checks := "none (--no-checks)"
		if opts.checks {
			checks = "gen-config-doc --check, gen-commands-doc --check, npm test, npm run typecheck, npm pack --dry-run, npm ls --json, claude plugin validate ."
		}
		a.out("would run the checks: " + checks + "\n")
		a.out(fmt.Sprintf("would commit \"Release %s\" (%d files) and create the annotated tag %s\n", tag, len(plan), tag))
		a.out("would print: git push origin " + branch + " --follow-tags\n")
		a.out("dry run: 0 files changed, 0 commits, 0 tags\n")
		if len(refusals) > 0 {
			lines := make([]string, len(refusals))
			for i, r := range refusals {
				lines[i] = "  - " + firstLine(r)
			}
			a.err(fmt.Sprintf("\nREFUSED: a real run would stop at %d preflight failure(s):\n%s\n", len(refusals), strings.Join(lines, "\n")))
			return 1, nil
		}
		return 0, nil
	}

	for _, f := range plan {
		if err := os.WriteFile(filepath.Join(root, f.file), []byte(f.after), 0o644); err != nil {
			return 0, err
		}
		*written = append(*written, f)
	}
	rollback := func(failure string) (int, error) {
		restore()
		a.err(fmt.Sprintf("\nFAILED: %s\nrolled back %d files; nothing was committed or tagged\n", failure, len(*written)))
		return 1, nil
	}

	a.out("\n== 4. checks ==\n")
	if f := a.buildNative(root); f != "" {
		return rollback(f)
	}
	if opts.checks {
		f, err := a.runChecks(root, name, next)
		if err != nil {
			return 0, err
		}
		if f != "" {
			return rollback(f)
		}
	} else {
		if hasNative(root) {
			if f := a.checkPack(root, name, next); f != "" {
				return rollback(f)
			}
			a.warn("!!! --no-checks: gen-config-doc --check, gen-commands-doc --check, npm test, typecheck, npm ls and plugin validate were NOT run; native build and package size checked !!!")
		} else {
			a.warn("!!! --no-checks: gen-config-doc --check, gen-commands-doc --check, npm test, typecheck, npm pack, npm ls and plugin validate were NOT run !!!")
		}
	}

	a.out("\n== 5. commit + tag ==\n")
	files := make([]string, len(plan))
	for i, f := range plan {
		files[i] = f.file
	}
	add := a.git(root, append([]string{"add", "--"}, files...), 0)
	commit := add
	if add.code == 0 {
		commit = a.git(root, append([]string{"commit", "-m", "Release " + tag, "--"}, files...), 0)
	}
	if commit.code != 0 {
		restore()
		a.git(root, append([]string{"add", "--"}, files...), 0)
		a.err(fmt.Sprintf("\nFAILED: git commit: %s\nrolled back %d files\n", jsTrim(or(commit.stderr, commit.stdout)), len(*written)))
		return 1, nil
	}
	*written = nil
	a.ok(fmt.Sprintf("committed \"Release %s\" (%d files)", tag, len(files)))

	tmp, err := os.MkdirTemp(filepath.Clean(os.TempDir()), "code-forge-release-")
	if err != nil {
		return 0, err
	}
	notesFile := filepath.Join(tmp, "release-notes-"+tag+".md")
	body, found := releaseNotes(changelogAfter, pkg, next)
	if !found {
		body = notes + "\n"
	}
	if err := os.WriteFile(notesFile, []byte(body), 0o644); err != nil {
		return 0, err
	}
	tagMsgFile := filepath.Join(tmp, "tag-message.txt")
	if err := os.WriteFile(tagMsgFile, []byte("Release "+tag+"\n\n"+notes+"\n"), 0o644); err != nil {
		return 0, err
	}
	tagged := a.git(root, []string{"tag", "-a", tag, "--cleanup=verbatim", "-F", tagMsgFile}, 0)
	if tagged.code != 0 {
		a.err("\nFAILED: git tag: " + jsTrim(tagged.stderr) + "\nthe commit stays; tag it by hand: git tag -a " + tag + " --cleanup=verbatim -F " + tagMsgFile + "\n")
		return 1, nil
	}
	a.ok("annotated tag " + tag)

	a.out("\n== 6. next steps (nothing was pushed or published) ==\n")
	a.out("  publish to npm first, then push (the tag push only creates the GitHub release; CI never publishes):\n")
	a.out("      npm publish\n")
	a.out("      npm run release:site\n")
	a.out("      git push origin " + branch + " --follow-tags\n")
	a.out("  if the tag run cannot create the GitHub release, create it by hand (notes already written to " + notesFile + "):\n")
	a.out("      gh release create " + tag + " --title " + tag + " --notes-file " + notesFile + " --verify-tag\n")
	return 0, nil
}
