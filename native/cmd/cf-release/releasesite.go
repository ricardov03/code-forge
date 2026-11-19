package main

// `npm run release:site`: wait for npm to serve the release, write the site data into a detached
// worktree of the site repository, move the docs pin, build the site once, check that the built
// site names the release, commit on release-site/vX.Y.Z and deploy. It never pushes. Port of
// scripts/release-site.mjs (B570). B780: the site's own gates (npm test, design fidelity) run
// during development as the site's code-forge gates, not at release.

import (
	"errors"
	"math"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	siteUsage     = "usage: npm run release:site -- [--site <dir>] [--wait-min <minutes>] [--deploy-dir <path>] [--dry-run]"
	siteData      = "src/data/site-data.json"
	siteDocsPin   = "docs-site/site/docs-source.json" // B775: the docs pages come from docs/ at its "commit"
	siteDeployDir = "docs-site/site/.pages-out"
)

func deployArgv(dir string) []string {
	return []string{"wrangler", "pages", "deploy", dir, "--project-name", "code-forge-ai", "--branch", "main"}
}

var (
	winAbsRe  = regexp.MustCompile(`^(?:[A-Za-z]:[\\/]|[\\/])`)
	slashesRe = regexp.MustCompile(`[\\/]+`)
)

func siteDeployDirOf(value string) (string, error) {
	if filepath.IsAbs(value) || winAbsRe.MatchString(value) {
		return "", errors.New("--deploy-dir must be relative to the site root: " + value)
	}
	parts := slashesRe.Split(value, -1)
	var kept []string
	for _, p := range parts {
		if p == ".." {
			return "", errors.New("--deploy-dir must stay inside the site root: " + value)
		}
		if p != "" {
			kept = append(kept, p)
		}
	}
	dir := path.Clean(strings.Join(kept, "/"))
	if dir == "." || dir == "" {
		return "", errors.New("--deploy-dir must name a folder below the site root")
	}
	return dir, nil
}

// fsErr words a file system failure the way Node does (`ENOENT: no such file or directory, <op> '<path>'`).
func fsErr(op, p string, err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return errors.New("ENOENT: no such file or directory, " + op + " '" + p + "'")
	case errors.Is(err, os.ErrExist):
		return errors.New("EEXIST: file already exists, " + op + " '" + p + "'")
	case strings.Contains(err.Error(), "directory not empty"):
		return errors.New("ENOTEMPTY: directory not empty, " + op + " '" + p + "'")
	}
	return err
}

func realpath(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Node's realpathSync lstats each component and names the first one that is missing.
		if errors.Is(err, os.ErrNotExist) {
			prefix := "/"
			for _, part := range strings.Split(strings.TrimPrefix(abs, "/"), "/") {
				prefix = filepath.Join(prefix, part)
				if _, lerr := os.Lstat(prefix); lerr != nil {
					return "", fsErr("lstat", prefix, lerr)
				}
			}
		}
		return "", fsErr("realpath", p, err)
	}
	return real, nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func checkDeployDir(tree, dir string) error {
	full := filepath.Join(tree, dir)
	index := filepath.Join(full, "index.html")
	missing := errors.New("Deploy folder " + dir + " is missing or has no index.html after the build; no commit or deploy; the old site remains live")
	if !isDir(full) {
		return missing
	}
	realTree, err := realpath(tree)
	if err != nil {
		return err
	}
	real, err := realpath(full)
	if err != nil {
		return err
	}
	if real != realTree && !strings.HasPrefix(real, realTree+string(filepath.Separator)) {
		return errors.New("Deploy folder " + dir + " resolves outside the site tree; no commit or deploy")
	}
	if !isFile(index) {
		return missing
	}
	return nil
}

var jsDecimalRe = regexp.MustCompile(`^[+-]?(?:Infinity|(?:\d+\.?\d*|\.\d+)(?:[eE][+-]?\d+)?)$`)

// jsNumber is Number(string): NaN when the text is not a number literal.
func jsNumber(s string) float64 {
	t := jsTrim(s)
	if t == "" {
		return 0
	}
	lower := strings.ToLower(t)
	for prefix, base := range map[string]int{"0x": 16, "0o": 8, "0b": 2} {
		if strings.HasPrefix(lower, prefix) {
			if strings.Contains(t, "_") {
				return math.NaN()
			}
			n, err := strconv.ParseUint(t[2:], base, 64)
			if err != nil {
				return math.NaN()
			}
			return float64(n)
		}
	}
	if !jsDecimalRe.MatchString(t) {
		return math.NaN()
	}
	switch strings.TrimLeft(t, "+") {
	case "Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	f, _ := strconv.ParseFloat(t, 64)
	return f
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

type siteOpts struct {
	site      string
	waitMin   float64
	dryRun    bool
	deployDir string
}

func siteOptions(args []string) (siteOpts, error) {
	o := siteOpts{site: "../code-forge-site", waitMin: 20, deployDir: siteDeployDir}
	for i := 0; i < len(args); i++ {
		flag := args[i]
		switch flag {
		case "--dry-run":
			o.dryRun = true
		case "--site", "--wait-min", "--deploy-dir":
			i++
			value := ""
			if i < len(args) {
				value = args[i]
			}
			if value == "" || strings.HasPrefix(value, "--") {
				return o, errors.New(flag + " needs a value\n" + siteUsage)
			}
			switch flag {
			case "--site":
				o.site = value
			case "--deploy-dir":
				dir, err := siteDeployDirOf(value)
				if err != nil {
					return o, err
				}
				o.deployDir = dir
			default:
				o.waitMin = jsNumber(value)
				if !finite(o.waitMin) || o.waitMin <= 0 {
					return o, errors.New("--wait-min must be a positive number")
				}
			}
		default:
			return o, errors.New("unknown option " + flag + "\n" + siteUsage)
		}
	}
	return o, nil
}

// siteRun is release-site's run(): every child without the repository-picking git variables.
func siteRun(cwd string, argv []string, timeout time.Duration, env []string) result {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	if env == nil {
		env = gitChildEnv()
	}
	return spawn(cwd, argv, timeout, env)
}

func checked(cwd string, argv []string, timeout time.Duration) (string, error) {
	r := siteRun(cwd, argv, timeout, nil)
	if r.failed() || r.code != 0 {
		return "", errors.New(strings.Join(argv, " ") + " failed: " + or(r.errMsg, r.stderr, r.stdout))
	}
	return r.stdout, nil
}

func (a *app) waitForNpm(root, name, version string, waitMin float64) error {
	deadline := a.now().Add(time.Duration(waitMin * float64(time.Minute)))
	for {
		remaining := deadline.Sub(a.now())
		if remaining <= 0 {
			break
		}
		limit := min(30*time.Second, remaining)
		limit = max(time.Millisecond, limit.Truncate(time.Millisecond))
		r := siteRun(root, []string{"npm", "view", name + "@" + version, "version"}, limit, nil)
		if !r.failed() && r.code == 0 && jsTrim(r.stdout) == version {
			a.out("npm serves " + name + "@" + version + "\n")
			return nil
		}
		left := deadline.Sub(a.now())
		if left <= 0 {
			break
		}
		a.out("Waiting for npm to serve " + name + "@" + version + " (poll every 30 s)\n")
		a.sleep(min(30*time.Second, left))
	}
	return errors.New("Timed out after " + jsNumberString(waitMin) + " min waiting for npm to serve " + name + "@" + version + "; nothing changed")
}

// B780: release:site builds once and runs no site gate; it says so in one line.
const (
	siteSkipped      = "site tests and design checks run during development (site gates), not at release"
	siteBuildTimeout = 600 * time.Second
)

// siteSmokeExts are the built files the smoke check reads for the release name.
var siteSmokeExts = map[string]bool{".html": true, ".md": true, ".txt": true}

func siteSmokeWordByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || strings.IndexByte("_+-", c) >= 0
}

// siteSmokeHas reports whether text names want as a whole word: "pkg@1.2.3" matches "pkg@1.2.3."
// at the end of a sentence but not "pkg@1.2.30", "pkg@1.2.3.4", "pkg@1.2.3-rc" or "mypkg@1.2.3".
func siteSmokeHas(text []byte, want string) bool {
	for from := 0; ; {
		i := strings.Index(string(text[from:]), want)
		if i < 0 {
			return false
		}
		start, end := from+i, from+i+len(want)
		from = start + 1
		if start > 0 && (siteSmokeWordByte(text[start-1]) || text[start-1] == '.' || text[start-1] == '/') {
			continue
		}
		if end < len(text) {
			c := text[end]
			if siteSmokeWordByte(c) || c == '.' && end+1 < len(text) && text[end+1] >= '0' && text[end+1] <= '9' {
				continue
			}
		}
		return true
	}
}

// siteNamesRelease is the release smoke check (B780): some built .html, .md or .txt file in the
// deploy folder names <package>@<version> (the docs pages pinned to the release tag do, through
// docs/step-by-step.md). It returns that file, relative to the site root.
func siteNamesRelease(tree, dir, want string) (string, error) {
	root := filepath.Join(tree, dir)
	found := ""
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() || !siteSmokeExts[strings.ToLower(filepath.Ext(p))] {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if siteSmokeHas(data, want) {
			rel, _ := filepath.Rel(tree, p)
			found = filepath.ToSlash(rel)
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", errors.New("Smoke check failed: no .html, .md or .txt file in deploy folder " + dir + " names " + want +
			" after the build (is the docs pin on the release tag?); no commit or deploy; the old site remains live")
	}
	return found, nil
}

func shQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'" }

func linkModules(source, target string) error {
	if err := os.Mkdir(target, 0o755); err != nil {
		return fsErr("mkdir", target, err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		return fsErr("scandir", source, err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") && e.Name() != ".bin" {
			continue
		}
		from := filepath.Join(source, e.Name())
		to := filepath.Join(target, e.Name())
		if strings.HasPrefix(e.Name(), "@") && e.IsDir() {
			if err := linkModules(from, to); err != nil {
				return err
			}
		} else if err := os.Symlink(from, to); err != nil {
			return fsErr("symlink", from+"' -> '"+to, err)
		}
	}
	return nil
}

// moveDocsPin sets the site docs pin's "commit" to tag in the tree, keeping every other key, the
// key order, the indent and the trailing newline (B775). A missing file is not an error: it
// reports false and the release commits the site data only, as before.
func moveDocsPin(tree, tag string) (old string, found bool, err error) {
	file := filepath.Join(tree, siteDocsPin)
	raw, err := os.ReadFile(file)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fsErr("open", file, err)
	}
	text := string(raw)
	data, err := jsParse(text)
	if err != nil {
		return "", false, errors.New(siteDocsPin + " is not JSON: " + err.Error())
	}
	obj, ok := data.(*jsObject)
	if !ok {
		return "", false, errors.New(siteDocsPin + " must be a JSON object")
	}
	old = "(none)"
	if v, has := obj.get("commit"); has {
		old = jsTemplate(v, true)
	}
	obj.set("commit", tag)
	indent := "  "
	if m := indentRe.FindStringSubmatch(text); m != nil {
		indent = m[1]
	}
	newline := ""
	if strings.HasSuffix(text, "\n") {
		newline = "\n"
	}
	if err := os.WriteFile(file, []byte(jsStringify(obj, indent)+newline), 0o644); err != nil {
		return "", false, fsErr("open", file, err)
	}
	return old, true, nil
}

var deployURLRe = regexp.MustCompile(`https://[^\s<>"']+`)

func releaseSiteMain(a *app, argv []string, root string) int {
	if err := a.releaseSite(argv, root); err != nil {
		a.err("release:site: " + err.Error() + "\n")
		return 1
	}
	return 0
}

func (a *app) releaseSite(argv []string, root string) (err error) {
	opts, err := siteOptions(argv)
	if err != nil {
		return err
	}
	pkg, _, err := readPackage(root)
	if err != nil {
		return err
	}
	nameV, _ := pkg.get("name")
	versionV, versionOK := pkg.get("version")
	name, _ := nameV.(string)
	version := jsTemplate(versionV, versionOK)
	if name == "" || !versionRe.MatchString(version) {
		return errors.New("package.json needs a name and an x.y.z version")
	}
	tag := "v" + version
	if _, err := checked(root, []string{"git", "rev-parse", "--verify", "refs/tags/" + tag + "^{commit}"}, 0); err != nil {
		return err
	}
	if err := a.waitForNpm(root, name, version, opts.waitMin); err != nil {
		return err
	}
	// B690d: the site data comes from Go (sitedata.go), not node scripts/gen-site-data.mjs.
	home, _ := os.UserHomeDir()
	data, err := siteDataJSON(root, tag, filepath.Join(home, ".code-forge", "ledger"))
	if err != nil {
		return errors.New("site data: " + err.Error())
	}
	if _, err := jsParse(data); err != nil {
		return errors.New("site data is not JSON: " + err.Error())
	}
	site, err := realpath(resolvePath(root, opts.site))
	if err != nil {
		return err
	}
	top, err := checked(site, []string{"git", "rev-parse", "--show-toplevel"}, 0)
	if err != nil {
		return err
	}
	if realTop, err := realpath(jsTrim(top)); err != nil {
		return err
	} else if realTop != site {
		return errors.New("--site must name the site repository root")
	}
	headOut, err := checked(site, []string{"git", "rev-parse", "HEAD^{commit}"}, 0)
	if err != nil {
		return err
	}
	head := jsTrim(headOut)
	branch := "release-site/" + tag
	if ref := siteRun(site, []string{"git", "show-ref", "--verify", "--quiet", "refs/heads/" + branch}, 0, nil); ref.failed() || ref.code != 1 {
		return errors.New("Cannot create new branch " + branch + ": it already exists or git could not check it")
	}
	parent := filepath.Join(site, ".code-forge-tmp")
	tree := filepath.Join(parent, "release-site-"+version)
	hadParent := exists(parent)
	if hadParent {
		if st, err := os.Lstat(parent); err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New(parent + " must be a real directory")
		}
	}
	if exists(tree) {
		return errors.New("Temporary worktree already exists: " + tree)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return fsErr("mkdir", parent, err)
	}
	added := false
	defer func() {
		if added {
			if _, rmErr := checked(site, []string{"git", "worktree", "remove", "--force", tree}, 0); rmErr != nil {
				err = rmErr
				return // like a throw inside Node's finally: the parent stays
			}
		}
		if !hadParent {
			if rmErr := os.Remove(parent); rmErr != nil {
				err = fsErr("rmdir", parent, rmErr)
			}
		}
	}()
	if _, err := checked(site, []string{"git", "-c", "core.hooksPath=/dev/null", "worktree", "add", "--detach", tree, head}, 0); err != nil {
		return err
	}
	added = true
	dataPath := filepath.Join(tree, siteData)
	if err := os.MkdirAll(filepath.Dir(dataPath), 0o755); err != nil {
		return fsErr("mkdir", filepath.Dir(dataPath), err)
	}
	if err := os.WriteFile(dataPath, []byte(data), 0o644); err != nil {
		return fsErr("open", dataPath, err)
	}
	oldPin, pinFound, err := moveDocsPin(tree, tag)
	if err != nil {
		return err
	}
	commitFiles := []string{siteData}
	if pinFound {
		commitFiles = append(commitFiles, siteDocsPin)
		a.out("Docs pin " + siteDocsPin + ": " + oldPin + " -> " + tag + "\n")
	} else {
		a.out("Docs pin " + siteDocsPin + " not found; the site docs pin was not moved\n")
	}
	files, err := checked(tree, []string{"git", "ls-files", "-z", "--", "package.json", "*/package.json"}, 0)
	if err != nil {
		return err
	}
	for _, file := range splitNul(files) {
		dir := path.Dir(file)
		skip := false
		for _, part := range strings.Split(dir, "/") {
			if part == "node_modules" {
				skip = true
			}
		}
		if skip {
			continue
		}
		target := filepath.Join(tree, dir, "node_modules")
		source := filepath.Join(site, dir, "node_modules")
		if exists(target) {
			continue
		}
		if exists(source) {
			if err := linkModules(source, target); err != nil {
				return err
			}
		} else if _, err := checked(filepath.Join(tree, dir), []string{"npm", "ci"}, 600*time.Second); err != nil {
			return err
		}
	}
	a.out(siteSkipped + "\n")
	a.out("Running: npm run build:site\n")
	set := [][2]string{{"GIT_TERMINAL_PROMPT", "0"}, {"CI", "1"}, {"npm_config_yes", "false"}}
	r := siteRun(tree, []string{"npm", "run", "build:site"}, siteBuildTimeout, envWith(repoEnvKeys, set...))
	a.out(r.stdout)
	a.err(r.stderr)
	if r.failed() || r.code != 0 {
		why := r.errMsg
		if !r.failed() {
			why = "exit " + strconv.Itoa(r.code)
			if r.code < 0 {
				why = "exit null"
			}
		}
		return errors.New("Site build failed: npm run build:site (" + why + "); no commit or deploy; the old site remains live")
	}
	if err := checkDeployDir(tree, opts.deployDir); err != nil {
		return err
	}
	named, err := siteNamesRelease(tree, opts.deployDir, name+"@"+version)
	if err != nil {
		return err
	}
	a.out("Smoke check: " + named + " names " + name + "@" + version + "\n")
	deploy := deployArgv(opts.deployDir)
	if opts.dryRun {
		a.out("Would commit " + strings.Join(commitFiles, " and ") + " on " + branch + "\nWould deploy: npx " + strings.Join(deploy, " ") + "\n")
	} else {
		for _, argv := range [][]string{
			{"git", "switch", "-c", branch},
			append([]string{"git", "add", "--"}, commitFiles...),
			append([]string{"git", "-c", "core.hooksPath=/dev/null", "commit", "--only", "-m", "Release site " + tag, "--"}, commitFiles...),
		} {
			if _, err := checked(tree, argv, 0); err != nil {
				return err
			}
		}
		output, err := checked(tree, append([]string{"npx"}, deploy...), 600*time.Second)
		if err != nil {
			return err
		}
		a.out(output)
		if url := deployURLRe.FindString(stripVT(output)); url != "" {
			a.out("Deploy URL: " + url + "\n")
		}
	}
	a.out("Owner next steps (nothing was pushed):\ngit -C " + shQuote(site) + " merge " + branch + "\ngit -C " + shQuote(site) + " push origin main\n")
	return nil
}
