// Package releasecheck answers "is this project ready to release?" in one call (B799): the work
// tree is clean, HEAD is pushed, GitHub CI for the HEAD sha is green, CHANGELOG.md's
// [Unreleased] has the shape a release accepts, and `npm ls` is clean. It suggests the bump from
// [Unreleased] (Added → minor, else patch) and lists the release steps in order. It only reads:
// it never writes, commits, tags, pushes or publishes.
//
// `code-forge release check` prints it, `autopilot stop` prints it as the run's final report, and
// the maintainer release (cf-release) classifies `npm ls` with NpmLs (B778).
package releasecheck

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Exec is one finished command: Code -1 and Err when it could not start or timed out.
type Exec struct {
	Stdout, Stderr string
	Code           int
	Err            string
}

// Runner runs argv (never a shell) in dir.
type Runner func(dir string, timeout time.Duration, argv ...string) Exec

// repoEnvKeys pick a repository; a git hook exports them, and they would point git elsewhere.
var repoEnvKeys = map[string]bool{"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_PREFIX": true,
	"GIT_OBJECT_DIRECTORY": true, "GIT_COMMON_DIR": true, "GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_IMPLICIT_WORK_TREE": true}

// DefaultRunner runs the command with the process environment minus the git repository
// selectors, with GIT_TERMINAL_PROMPT=0 (no credential prompt) and no stdin.
func DefaultRunner(dir string, timeout time.Duration, argv ...string) Exec {
	if _, err := exec.LookPath(argv[0]); err != nil {
		return Exec{Code: -1, Err: argv[0] + " is not on PATH"}
	}
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	var env []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if !repoEnvKeys[k] && k != "GIT_TERMINAL_PROMPT" {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, "GIT_TERMINAL_PROMPT=0")
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := Exec{Stdout: out.String(), Stderr: errb.String()}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		r.Code, r.Err = -1, fmt.Sprintf("timed out after %s", timeout)
	case err == nil:
	default:
		if ee, ok := err.(*exec.ExitError); ok {
			r.Code = ee.ExitCode()
		} else {
			r.Code, r.Err = -1, err.Error()
		}
	}
	return r
}

// Check states.
const (
	OK   = "ok"
	Fail = "fail"
	Skip = "skip"
)

// Check is one line of the report: what was checked, what was found and, for a failure, the fix.
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Report is the whole answer.
type Report struct {
	Ready      bool     `json:"ready"`
	Root       string   `json:"root"`
	Branch     string   `json:"branch"`
	Head       string   `json:"head"`
	Package    string   `json:"package"`
	Current    string   `json:"current"`
	Bump       string   `json:"bump"`
	BumpReason string   `json:"bump_reason"`
	Next       string   `json:"next"`
	Checks     []Check  `json:"checks"`
	Failed     []string `json:"failed"`
	Steps      []string `json:"steps"`
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func or(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

type pkgJSON struct {
	found    bool
	name     string
	version  string
	private  bool
	scripts  map[string]string
	optional map[string]string
	bad      string
}

func readPkg(root string) pkgJSON {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return pkgJSON{}
	}
	var raw struct {
		Name     string            `json:"name"`
		Version  string            `json:"version"`
		Private  bool              `json:"private"`
		Scripts  map[string]string `json:"scripts"`
		Optional map[string]string `json:"optionalDependencies"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return pkgJSON{found: true, bad: err.Error()}
	}
	return pkgJSON{found: true, name: raw.Name, version: raw.Version, private: raw.Private, scripts: raw.Scripts, optional: raw.Optional}
}

// OwnPlatformPackages are the optionalDependencies named <package>-<suffix> (code-forge's
// per-platform binary packages), sorted by name.
func OwnPlatformPackages(name string, optional map[string]string) []string {
	var out []string
	if name == "" {
		return nil
	}
	for dep := range optional {
		if strings.HasPrefix(dep, name+"-") && len(dep) > len(name)+1 {
			out = append(out, dep)
		}
	}
	sort.Strings(out)
	return out
}

var versionRe = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)

func parseVersion(v string) ([3]int, bool) {
	m := versionRe.FindStringSubmatch(v)
	if m == nil {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// older reports whether version a is lower than version b (both x.y.z).
func older(a, b string) bool {
	x, ok1 := parseVersion(a)
	y, ok2 := parseVersion(b)
	if !ok1 || !ok2 {
		return false
	}
	for i := 0; i < 3; i++ {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return false
}

// NextVersion is current with the bump applied ("" when current is not x.y.z).
func NextVersion(current, bump string) string {
	v, ok := parseVersion(current)
	if !ok {
		return ""
	}
	switch bump {
	case "major":
		return fmt.Sprintf("%d.0.0", v[0]+1)
	case "minor":
		return fmt.Sprintf("%d.%d.0", v[0], v[1]+1)
	}
	return fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]+1)
}

// Run checks the project at root. A nil runner is DefaultRunner.
func Run(root string, run Runner) Report {
	if run == nil {
		run = DefaultRunner
	}
	r := Report{Root: root, Checks: []Check{}, Failed: []string{}, Steps: []string{}}
	add := func(c Check) {
		r.Checks = append(r.Checks, c)
		if c.Status == Fail {
			r.Failed = append(r.Failed, c.Name)
		}
	}
	git := func(args ...string) Exec { return run(root, 60*time.Second, append([]string{"git"}, args...)...) }

	inside := git("rev-parse", "--is-inside-work-tree")
	isRepo := inside.Code == 0 && strings.TrimSpace(inside.Stdout) == "true"
	if isRepo {
		r.Head = strings.TrimSpace(git("rev-parse", "HEAD").Stdout)
		r.Branch = strings.TrimSpace(git("rev-parse", "--abbrev-ref", "HEAD").Stdout)
	}

	// 1. tree
	if !isRepo {
		add(Check{Name: "tree", Status: Fail, Detail: root + " is not a git work tree" + suffix(or(inside.Err, firstLine(inside.Stderr))),
			Fix: "run the check in the project's git repository (--cwd <dir>)"})
	} else if st := git("status", "--porcelain", "--untracked-files=normal"); st.Code != 0 {
		add(Check{Name: "tree", Status: Fail, Detail: "git status failed: " + firstLine(or(st.Err, st.Stderr, st.Stdout)), Fix: "fix the repository so git status works"})
	} else if lines := nonEmpty(st.Stdout); len(lines) > 0 {
		add(Check{Name: "tree", Status: Fail, Detail: fmt.Sprintf("the working tree is not clean (%d changed): %s", len(lines), strings.Join(lines[:min(5, len(lines))], ", ")),
			Fix: "commit or remove the changes, then check again"})
	} else {
		add(Check{Name: "tree", Status: OK, Detail: "working tree is clean"})
	}

	// 2. pushed
	pushed := false
	switch {
	case !isRepo:
		add(Check{Name: "pushed", Status: Skip, Detail: "not a git work tree"})
	case r.Branch == "HEAD" || r.Branch == "":
		add(Check{Name: "pushed", Status: Fail, Detail: "HEAD is detached", Fix: "check out the release branch (git switch main)"})
	default:
		c := pushedCheck(git, r.Branch, r.Head)
		pushed = c.Status == OK
		add(c)
	}

	// 3. ci
	switch {
	case !isRepo || r.Head == "":
		add(Check{Name: "ci", Status: Skip, Detail: "not a git work tree"})
	default:
		add(ciCheck(run, root, r.Head, pushed))
	}

	// 4. changelog
	text, clErr := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if clErr != nil {
		add(Check{Name: "changelog", Status: Skip, Detail: "no CHANGELOG.md: the bump is patch by default"})
		r.Bump, r.BumpReason = "patch", "no CHANGELOG.md"
	} else {
		faults := ChangelogFaults(string(text))
		if isRepo {
			faults = append(faults, changelogOutside(git, string(text))...)
		}
		if len(faults) > 0 {
			add(Check{Name: "changelog", Status: Fail, Detail: strings.Join(faults, "; "),
				Fix: "fix CHANGELOG.md: every new entry goes under ## [Unreleased], under one ### heading per kind (Added, Changed, Deprecated, Removed, Fixed, Security)"})
		} else {
			add(Check{Name: "changelog", Status: OK, Detail: "## [Unreleased] has entries, each under one ### heading"})
		}
		r.Bump, r.BumpReason = Bump(string(text))
	}

	// 5. npm ls
	pkg := readPkg(root)
	switch {
	case !pkg.found:
		add(Check{Name: "npm-ls", Status: Skip, Detail: "no package.json"})
	case pkg.bad != "":
		add(Check{Name: "npm-ls", Status: Fail, Detail: "package.json is not valid JSON: " + pkg.bad, Fix: "fix package.json"})
	default:
		ls := run(root, 120*time.Second, "npm", "ls", "--json")
		if ls.Code == -1 {
			add(Check{Name: "npm-ls", Status: Fail, Detail: "npm ls could not run: " + ls.Err, Fix: "install Node.js and npm, then check again"})
		} else {
			res := NpmLs(ls.Stdout, pkg.name, pkg.optional)
			c := Check{Name: "npm-ls", Status: OK, Detail: res.Summary()}
			if !res.OK() {
				c.Status, c.Fix = Fail, res.Fix()
			} else if ls.Code != 0 && !res.Parsed {
				c.Status, c.Detail, c.Fix = Fail, "npm ls failed (exit "+strconv.Itoa(ls.Code)+"): "+firstLine(or(ls.Stderr, ls.Stdout)), "run npm install, then npm ls"
			}
			add(c)
		}
	}

	// version and steps
	r.Package, r.Current = pkg.name, pkg.version
	if _, ok := parseVersion(r.Current); !ok && isRepo {
		if tag := strings.TrimSpace(git("describe", "--tags", "--abbrev=0", "--match", "v[0-9]*", "HEAD").Stdout); tag != "" {
			r.Current = strings.TrimPrefix(tag, "v")
		}
	}
	r.Next = NextVersion(r.Current, r.Bump)
	r.Steps = steps(pkg, r.Bump, r.Next, or(r.Branch, "main"))
	r.Ready = len(r.Failed) == 0
	return r
}

func suffix(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

func nonEmpty(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, strings.TrimRight(l, "\r"))
		}
	}
	return out
}

func pushedCheck(git func(...string) Exec, branch, head string) Check {
	remote := git("ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if remote.Code != 0 {
		return Check{Name: "pushed", Status: Fail, Detail: "cannot read origin: " + firstLine(or(remote.Err, remote.Stderr, "no output")),
			Fix: "add the origin remote or fix its access (git remote -v), then check again"}
	}
	fields := strings.Fields(remote.Stdout)
	remoteSha := ""
	if len(fields) > 0 {
		remoteSha = fields[0]
	}
	switch {
	case remoteSha == "":
		return Check{Name: "pushed", Status: Fail, Detail: "origin has no branch " + branch, Fix: "git push -u origin " + branch}
	case remoteSha == head:
		return Check{Name: "pushed", Status: OK, Detail: "HEAD " + short(head) + " is on origin/" + branch}
	case git("cat-file", "-e", remoteSha+"^{commit}").Code != 0,
		git("merge-base", "--is-ancestor", head, remoteSha).Code == 0:
		return Check{Name: "pushed", Status: Fail, Detail: branch + " is behind origin/" + branch, Fix: "git pull --ff-only origin " + branch}
	case git("merge-base", "--is-ancestor", remoteSha, head).Code != 0:
		return Check{Name: "pushed", Status: Fail, Detail: branch + " and origin/" + branch + " have diverged", Fix: "reconcile " + branch + " with origin/" + branch + ", then push"}
	}
	return Check{Name: "pushed", Status: Fail, Detail: "HEAD " + short(head) + " is not pushed (" + branch + " is ahead of origin/" + branch + ")", Fix: "git push origin " + branch}
}

type ghRun struct {
	HeadSha      string `json:"headSha"`
	Status       string `json:"status"`
	Conclusion   string `json:"conclusion"`
	Name         string `json:"name"`
	WorkflowName string `json:"workflowName"`
}

func ciCheck(run Runner, root, head string, pushed bool) Check {
	watch := "code-forge ci watch --sha " + head
	gh := run(root, 60*time.Second, "gh", "run", "list", "--commit", head, "--limit", "50", "--json", "headSha,status,conclusion,name,workflowName")
	switch {
	case gh.Code == -1:
		return Check{Name: "ci", Status: Fail, Detail: "gh could not run: " + gh.Err, Fix: "install the GitHub CLI (gh) and run gh auth login"}
	case gh.Code != 0:
		return Check{Name: "ci", Status: Fail, Detail: fmt.Sprintf("gh run list failed (exit %d): %s", gh.Code, firstLine(or(gh.Stderr, gh.Stdout, "no output"))),
			Fix: "check gh auth status and that origin is a GitHub repository"}
	}
	var runs []ghRun
	if err := json.Unmarshal([]byte(gh.Stdout), &runs); err != nil {
		return Check{Name: "ci", Status: Fail, Detail: "gh run list printed no JSON list", Fix: "update the GitHub CLI (gh) and check again"}
	}
	var mine []ghRun
	for _, x := range runs {
		if x.HeadSha == head {
			mine = append(mine, x)
		}
	}
	label := func(x ghRun) string { return or(x.WorkflowName, x.Name, "run") }
	if len(mine) == 0 {
		fix := "wait for CI to start: " + watch
		if !pushed {
			fix = "push HEAD, then wait for CI: " + watch
		}
		return Check{Name: "ci", Status: Fail, Detail: "no CI run for HEAD " + short(head), Fix: fix}
	}
	var running, red, green []string
	for _, x := range mine {
		switch {
		case x.Status != "completed":
			running = append(running, label(x)+" ("+or(x.Status, "queued")+")")
		case x.Conclusion == "success" || x.Conclusion == "skipped" || x.Conclusion == "neutral":
			green = append(green, label(x))
		default:
			red = append(red, label(x)+" ("+or(x.Conclusion, "no conclusion")+")")
		}
	}
	if len(red) > 0 {
		return Check{Name: "ci", Status: Fail, Detail: "CI for HEAD " + short(head) + " failed: " + strings.Join(red, ", "),
			Fix: "fix the failure, push, and check again (" + watch + " names the failed tests)"}
	}
	if len(running) > 0 {
		return Check{Name: "ci", Status: Fail, Detail: "CI for HEAD " + short(head) + " is still running: " + strings.Join(running, ", "), Fix: "wait for it: " + watch}
	}
	return Check{Name: "ci", Status: OK, Detail: "CI for HEAD " + short(head) + " is green: " + strings.Join(green, ", ")}
}

// steps lists the release commands in order, from what the project has: its release script (or
// a git tag), its own platform packages, the npm publish, its release:site script and the push.
func steps(pkg pkgJSON, bump, next, branch string) []string {
	var out []string
	if _, ok := pkg.scripts["release"]; ok {
		out = append(out, "npm run release -- "+bump)
	} else {
		v := or(next, "<x.y.z>")
		out = append(out, "git tag -a v"+v+" -m \"Release v"+v+"\"")
	}
	if pkg.found && pkg.bad == "" && !pkg.private {
		for _, dep := range OwnPlatformPackages(pkg.name, pkg.optional) {
			out = append(out, "npm publish dist/npm/"+strings.TrimPrefix(dep, pkg.name+"-"))
		}
		out = append(out, "npm publish")
	}
	if _, ok := pkg.scripts["release:site"]; ok {
		out = append(out, "npm run release:site")
	}
	return append(out, "git push origin "+branch+" --follow-tags")
}

// Text is the report for a person: one line per check, the fix under a failure, the bump, the
// steps and the verdict.
func (r Report) Text() string {
	var b strings.Builder
	head := short(r.Head)
	if head == "" {
		head = "none"
	}
	fmt.Fprintf(&b, "release check: %s (branch %s, HEAD %s)\n", r.Root, or(r.Branch, "none"), head)
	for _, c := range r.Checks {
		tag := map[string]string{OK: "ok   ", Fail: "FAIL ", Skip: "skip "}[c.Status]
		fmt.Fprintf(&b, "%s %-9s %s\n", tag, c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(&b, "      %-9s fix: %s\n", "", c.Fix)
		}
	}
	next := ""
	if r.Next != "" {
		next = " → " + r.Next
		if r.Current != "" {
			next += " (now " + r.Current + ")"
		}
	}
	fmt.Fprintf(&b, "bump: %s%s; %s\n", r.Bump, next, r.BumpReason)
	if r.Ready {
		b.WriteString("steps, in order:\n")
	} else {
		b.WriteString("steps, in order, once every check passes:\n")
	}
	for i, s := range r.Steps {
		fmt.Fprintf(&b, "  %d. %s\n", i+1, s)
	}
	if r.Ready {
		b.WriteString("READY: every check passed\n")
	} else {
		fmt.Fprintf(&b, "NOT READY: %d check(s) failed: %s\n", len(r.Failed), strings.Join(r.Failed, ", "))
	}
	return b.String()
}

// JSON is the report as one JSON line.
func (r Report) JSON() string {
	data, _ := json.Marshal(r)
	return string(data) + "\n"
}
