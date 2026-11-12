package main

// `npm run release:site`: wait for npm to serve the release, write the site data into a detached
// worktree of the site repository, run the site gates, commit on release-site/vX.Y.Z and deploy.
// It never pushes. Port of scripts/release-site.mjs (B570).

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

	"gopkg.in/yaml.v3"
)

const (
	siteUsage     = "usage: npm run release:site -- [--site <dir>] [--wait-min <minutes>] [--deploy-dir <path>] [--dry-run]"
	siteData      = "src/data/site-data.json"
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

type siteGate struct {
	name string
	argv []string
	env  [][2]string
}

func argvOf(value any, name string) ([]string, error) {
	bad := errors.New("Invalid site gate " + name + ": expected a nonempty argv array")
	list, ok := value.([]any)
	if !ok || len(list) == 0 {
		return nil, bad
	}
	out := make([]string, len(list))
	for i, v := range list {
		s, isStr := v.(string)
		if !isStr || s == "" {
			return nil, bad
		}
		out[i] = s
	}
	return out, nil
}

// yamlMap decodes a mapping node keeping its key order.
type yamlMap struct {
	keys []string
	vals map[string]any
}

func yamlValue(n *yaml.Node) any {
	switch n.Kind {
	case yaml.DocumentNode:
		if len(n.Content) == 0 {
			return nil
		}
		return yamlValue(n.Content[0])
	case yaml.AliasNode:
		return yamlValue(n.Alias)
	case yaml.MappingNode:
		m := &yamlMap{vals: map[string]any{}}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i].Value
			if _, seen := m.vals[k]; !seen {
				m.keys = append(m.keys, k)
			}
			m.vals[k] = yamlValue(n.Content[i+1])
		}
		return m
	case yaml.SequenceNode:
		out := make([]any, len(n.Content))
		for i, c := range n.Content {
			out[i] = yamlValue(c)
		}
		return out
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return n.Value
	}
	return v
}

// yamlNumber is Number(value) for a decoded YAML scalar.
func yamlNumber(v any) float64 {
	switch t := v.(type) {
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case uint64:
		return float64(t)
	case float64:
		return t
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		return jsNumber(t)
	case nil:
		return 0
	}
	return math.NaN()
}

func siteGates(tree string) ([]siteGate, time.Duration, error) {
	file := filepath.Join(tree, ".code-forge.yml")
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, 0, fsErr("open", file, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, 0, err
	}
	config := yamlValue(&doc)
	var gates *yamlMap
	if m, ok := config.(*yamlMap); ok {
		gates, _ = m.vals["gates"].(*yamlMap)
		if _, isList := m.vals["gates"].([]any); isList {
			gates = &yamlMap{vals: map[string]any{}} // an array is an object with no gates fields
		}
	}
	if gates == nil {
		return nil, 0, errors.New("Site .code-forge.yml must define gates")
	}
	var custom []siteGate
	if c := gates.vals["custom"]; c != nil {
		list, ok := c.([]any)
		if !ok {
			return nil, 0, errors.New("Invalid site gates.custom: expected an array")
		}
		for _, g := range list {
			gate, _ := g.(*yamlMap)
			var name string
			if gate != nil {
				name, _ = gate.vals["name"].(string)
			}
			if name == "" {
				return nil, 0, errors.New("Invalid site custom gate: missing name")
			}
			var env [][2]string
			if e := gate.vals["env"]; e != nil {
				em, ok := e.(*yamlMap)
				if !ok {
					return nil, 0, errors.New("Invalid site gate " + name + ": env must contain strings")
				}
				for _, k := range em.keys {
					s, isStr := em.vals[k].(string)
					if !isStr {
						return nil, 0, errors.New("Invalid site gate " + name + ": env must contain strings")
					}
					env = append(env, [2]string{k, s})
				}
			}
			argv, err := argvOf(gate.vals["argv"], name)
			if err != nil {
				return nil, 0, err
			}
			custom = append(custom, siteGate{name, argv, env})
		}
	}
	if t := gates.vals["test"]; t != nil {
		argv, err := argvOf(t, "test")
		if err != nil {
			return nil, 0, err
		}
		var kept []any
		for _, arg := range argv {
			if arg != "{test_files}" && arg != "{changed_files}" {
				kept = append(kept, arg)
			}
		}
		argv, err = argvOf(kept, "test")
		if err != nil {
			return nil, 0, err
		}
		custom = append(custom, siteGate{name: "test", argv: argv})
	}
	timeout := 300000.0
	if t := gates.vals["timeout_s"]; t != nil {
		timeout = yamlNumber(t) * 1000
	}
	if !finite(timeout) || timeout <= 0 {
		return nil, 0, errors.New("Invalid site gates.timeout_s")
	}
	return custom, time.Duration(timeout * float64(time.Millisecond)), nil
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
	data, err := checked(root, []string{"node", "scripts/gen-site-data.mjs", "--tag", tag}, 0)
	if err != nil {
		return err
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
	custom, timeout, err := siteGates(tree)
	if err != nil {
		return err
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
	var failures []string
	gates := append([]siteGate{{name: "build:site", argv: []string{"npm", "run", "build:site"}}}, custom...)
	for _, gate := range gates {
		a.out("Running gate: " + gate.name + "\n")
		set := append([][2]string{{"GIT_TERMINAL_PROMPT", "0"}, {"CI", "1"}, {"npm_config_yes", "false"}}, gate.env...)
		r := siteRun(tree, gate.argv, timeout, envWith(repoEnvKeys, set...))
		a.out(r.stdout)
		a.err(r.stderr)
		if r.failed() || r.code != 0 {
			failures = append(failures, gate.name)
			why := " (" + r.errMsg + ")"
			if !r.failed() {
				code := strconv.Itoa(r.code)
				if r.code < 0 {
					code = "null"
				}
				why = " (exit " + code + ")"
			}
			a.err("Gate failed: " + gate.name + why + "\n")
		}
	}
	if len(failures) > 0 {
		return errors.New("Site gates failed: " + strings.Join(failures, ", ") + "; no commit or deploy; the old site remains live")
	}
	if err := checkDeployDir(tree, opts.deployDir); err != nil {
		return err
	}
	deploy := deployArgv(opts.deployDir)
	if opts.dryRun {
		a.out("Would commit " + siteData + " on " + branch + "\nWould deploy: npx " + strings.Join(deploy, " ") + "\n")
	} else {
		for _, argv := range [][]string{
			{"git", "switch", "-c", branch},
			{"git", "add", "--", siteData},
			{"git", "-c", "core.hooksPath=/dev/null", "commit", "--only", "-m", "Release site " + tag, "--", siteData},
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
