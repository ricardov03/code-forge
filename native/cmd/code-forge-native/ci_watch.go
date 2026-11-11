package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/healq"
)

// B430: `code-forge ci watch` follows the GitHub Actions runs of one commit
// through the gh CLI on PATH. Success exits 0; a failure names the failing
// jobs and tests, queues a heal item of kind ci and exits 1; no run after
// ciNoRunAfter exits 2; still running at --timeout exits 3.
//
// B448: a GitHub API rate-limit reply is never a CI result. The watch waits
// until the limit resets (read from `gh api rate_limit`, which does not count
// against the limit) or backs off 60s doubling to 15m, prints one wait line
// each time, and gives up with status rate_limited (exit 4) only when the next
// retry falls past --timeout. Polling makes one `gh run list` call per 30s.
// One watch per repo and commit: a second start finds the first one's lock
// (the shared code-forge lock files) and exits 0 without watching.

func init() { routes["ci"] = runCI }

const ciUsage = "usage: code-forge ci watch --sha <sha> [--run <r>] [--timeout 30m] [--cwd <dir>] [--json]"

// Test seams: the poll interval, the no-run limit, the rate-limit backoff and the clock.
var (
	ciPollInterval = 30 * time.Second
	ciNoRunAfter   = 5 * time.Minute
	ciBackoffStart = 60 * time.Second
	ciBackoffMax   = 15 * time.Minute
	ciSleep        = time.Sleep
	ciNow          = time.Now
)

type ciRun struct {
	DatabaseID int64  `json:"databaseId"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Name       string `json:"name"`
}

type ciOptions struct {
	sha, run, cwd string
	timeout       time.Duration
	json          bool
}

func ciParse(args []string) (ciOptions, string) {
	o := ciOptions{timeout: 30 * time.Minute}
	if len(args) == 0 || args[0] != "watch" {
		return o, ciUsage
	}
	seen := map[string]bool{}
	for i := 1; i < len(args); i++ {
		name, value, inline := strings.Cut(args[i], "=")
		if seen[name] {
			return o, name + " given more than once"
		}
		seen[name] = true
		if name == "--json" {
			if inline {
				return o, "--json takes no value"
			}
			o.json = true
			continue
		}
		if name != "--sha" && name != "--run" && name != "--timeout" && name != "--cwd" {
			return o, ciUsage
		}
		if !inline {
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return o, name + " needs a value"
			}
			i++
			value = args[i]
		}
		switch name {
		case "--sha":
			if !regexp.MustCompile(`^[0-9a-f]{7,64}$`).MatchString(value) {
				return o, "--sha must be a hex commit id"
			}
			o.sha = value
		case "--run":
			if !healq.ValidRun(value) {
				return o, "--run must be a run id"
			}
			o.run = value
		case "--timeout":
			d, err := time.ParseDuration(value)
			if err != nil || d <= 0 {
				return o, "--timeout must look like 30m or 90s"
			}
			o.timeout = d
		case "--cwd":
			o.cwd = value
		}
	}
	if o.sha == "" {
		return o, "--sha is required"
	}
	return o, ""
}

func runCI(args []string) int {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			fmt.Fprintln(os.Stdout, ciUsage)
			return 0
		}
	}
	o, message := ciParse(args)
	if message != "" {
		if message != ciUsage {
			fmt.Fprintln(os.Stderr, "ci watch: "+message)
		}
		fmt.Fprintln(os.Stderr, ciUsage)
		return 2
	}
	return ciWatch(o)
}

// ciGh runs gh from PATH in cwd and returns stdout.
func ciGh(cwd string, args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	cmd.Dir = cwd
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		text := strings.TrimSpace(errOut.String())
		if text == "" {
			text = err.Error()
		}
		return out.String(), fmt.Errorf("gh %s: %s", strings.Join(args[:2], " "), text)
	}
	return out.String(), nil
}

// ciRateLimited reports a GitHub API rate-limit reply (HTTP 403 or 429 with
// "rate limit" in the text, or gh's bare "API rate limit exceeded").
func ciRateLimited(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "rate limit")
}

// ciLimiter runs gh and sits out rate-limit replies until a deadline.
type ciLimiter struct {
	cwd      string
	deadline time.Time
	backoff  time.Duration
	// gaveUp is set when a rate-limit wait would pass the deadline.
	gaveUp  bool
	retryAt time.Time
}

// gh runs one gh call; on a rate-limit reply it waits and retries the same call.
func (l *ciLimiter) gh(args ...string) (string, error) {
	for {
		out, err := ciGh(l.cwd, args...)
		if !ciRateLimited(err) {
			if err == nil {
				l.backoff = 0
			}
			return out, err
		}
		retry := l.next()
		if retry.After(l.deadline) {
			l.gaveUp, l.retryAt = true, retry
			return out, err
		}
		fmt.Fprintf(os.Stderr, "ci watch: GitHub API rate limit; retry at %s UTC\n", retry.UTC().Format("15:04"))
		ciSleep(retry.Sub(ciNow()))
	}
}

// next is the reset time from `gh api rate_limit` when the core limit is used
// up, else now plus a backoff of 60s doubling to 15m.
func (l *ciLimiter) next() time.Time {
	now := ciNow()
	if out, err := ciGh(l.cwd, "api", "rate_limit"); err == nil {
		var limits struct {
			Resources struct {
				Core struct {
					Remaining *int  `json:"remaining"`
					Reset     int64 `json:"reset"`
				} `json:"core"`
			} `json:"resources"`
		}
		if json.Unmarshal([]byte(out), &limits) == nil {
			core := limits.Resources.Core
			reset := time.Unix(core.Reset, 0).Add(5 * time.Second)
			if core.Remaining != nil && *core.Remaining == 0 && reset.After(now) {
				return reset
			}
		}
	}
	if l.backoff == 0 {
		l.backoff = ciBackoffStart
	} else if l.backoff *= 2; l.backoff > ciBackoffMax {
		l.backoff = ciBackoffMax
	}
	return now.Add(l.backoff)
}

// ciLock takes the one-watch-per-repo-and-commit lock. ok false means another
// live ci watch holds it; a lock that cannot be taken for another reason only
// warns, so the watch still runs.
func ciLock(cwd, sha string) (release func(), ok bool) {
	repo, err := gitx.RepoTop(cwd)
	if err != nil {
		repo = cwd
	}
	full := sha
	if out, err := gitx.Run(cwd, "rev-parse", "--verify", "--quiet", sha+"^{commit}"); err == nil && strings.TrimSpace(out) != "" {
		full = strings.TrimSpace(out)
	}
	lock, err := filelock.TryLock("ci-watch "+repo+" "+full, filelock.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ci watch: watch lock not taken: "+err.Error())
		return func() {}, true
	}
	if !lock.OK {
		return func() {}, false
	}
	return lock.Release, true
}

type ciReport struct {
	SHA         string   `json:"sha"`
	Status      string   `json:"status"`
	Runs        []ciRun  `json:"runs"`
	FailedJobs  []string `json:"failed_jobs,omitempty"`
	FailedTests []string `json:"failed_tests,omitempty"`
	Blocks      []string `json:"blocks,omitempty"`
	Heal        string   `json:"heal,omitempty"`
	Message     string   `json:"message,omitempty"`
}

func ciWatch(o ciOptions) int {
	report := ciReport{SHA: o.sha, Runs: []ciRun{}}
	finish := func(status, message string, code int) int {
		report.Status, report.Message = status, message
		if o.json {
			_ = json.NewEncoder(os.Stdout).Encode(report)
		} else {
			fmt.Fprintln(os.Stdout, "ci watch: "+message)
		}
		if code != 0 && o.json {
			fmt.Fprintln(os.Stderr, "ci watch: "+message)
		}
		return code
	}
	cwd := o.cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	release, ok := ciLock(cwd, o.sha)
	if !ok {
		return finish("already_running", "another ci watch is already watching "+o.sha+"; not starting a second one", 0)
	}
	defer release()
	started := ciNow()
	limiter := &ciLimiter{cwd: cwd, deadline: started.Add(o.timeout)}
	lastError := ""
	for {
		out, err := limiter.gh("run", "list", "--commit", o.sha, "--json", "databaseId,status,conclusion,name")
		if limiter.gaveUp {
			return finish("rate_limited", fmt.Sprintf("GitHub API rate limit, gave up watching %s: the next retry (%s UTC) is past the %s timeout; CI result unknown, not a CI failure", o.sha, limiter.retryAt.UTC().Format("15:04"), o.timeout), 4)
		}
		runs := []ciRun{}
		if err == nil {
			if jerr := json.Unmarshal([]byte(out), &runs); jerr != nil {
				err = fmt.Errorf("gh run list: unreadable JSON: %v", jerr)
			}
		}
		if err != nil {
			// A gh hiccup is transient; it only ends the watch at the timeout.
			lastError = err.Error()
			fmt.Fprintln(os.Stderr, "ci watch: "+lastError)
		} else {
			report.Runs = runs
			if len(runs) == 0 && ciNow().Sub(started) >= ciNoRunAfter {
				return finish("no_run", "no CI run for "+o.sha, 2)
			}
			if len(runs) > 0 && ciAllDone(runs) {
				failed := []ciRun{}
				for _, r := range runs {
					if !ciGreen(r.Conclusion) {
						failed = append(failed, r)
					}
				}
				if len(failed) == 0 {
					return finish("success", fmt.Sprintf("CI passed for %s (%d runs)", o.sha, len(runs)), 0)
				}
				return ciFailure(o, cwd, limiter, failed, &report, finish)
			}
		}
		if ciNow().Sub(started) >= o.timeout {
			message := "CI still running for " + o.sha + " after " + o.timeout.String()
			if lastError != "" && len(report.Runs) == 0 {
				message += " (last gh error: " + lastError + ")"
			}
			return finish("timeout", message, 3)
		}
		ciSleep(ciPollInterval)
	}
}

func ciAllDone(runs []ciRun) bool {
	for _, r := range runs {
		if r.Status != "completed" {
			return false
		}
	}
	return true
}

func ciGreen(conclusion string) bool {
	return conclusion == "success" || conclusion == "skipped" || conclusion == "neutral"
}

var (
	ciTestPatterns = []*regexp.Regexp{
		regexp.MustCompile(`--- FAIL: (\S+)`),
		regexp.MustCompile(`not ok \d+ - (.+?)\s*(?:#.*)?$`),
		regexp.MustCompile(`✖ (.+?)\s*(?:\([\d.]+m?s\))?$`),
	}
	ciBlockID = regexp.MustCompile(`\bB\d+[A-Za-z]?\b`)
)

// ciFailure reads the failed logs, names jobs and tests, queues one heal item.
func ciFailure(o ciOptions, cwd string, limiter *ciLimiter, failed []ciRun, report *ciReport, finish func(string, string, int) int) int {
	jobs, tests := []string{}, []string{}
	seenJob, seenTest := map[string]bool{}, map[string]bool{}
	for _, r := range failed {
		log, err := limiter.gh("run", "view", fmt.Sprint(r.DatabaseID), "--log-failed")
		lines := strings.Split(log, "\n")
		if len(lines) > 200 {
			lines = lines[:200]
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "ci watch: "+err.Error())
		}
		for _, line := range lines {
			fields := strings.SplitN(line, "\t", 3)
			if len(fields) == 3 && fields[0] != "" {
				job := r.Name + " / " + fields[0]
				if !seenJob[job] {
					seenJob[job] = true
					jobs = append(jobs, job)
				}
			}
			text := line
			if len(fields) == 3 {
				text = fields[2]
			}
			for _, pattern := range ciTestPatterns {
				if m := pattern.FindStringSubmatch(text); m != nil {
					name := strings.TrimSpace(m[1])
					if name != "" && !seenTest[name] {
						seenTest[name] = true
						tests = append(tests, name)
					}
				}
			}
		}
		// A run whose log names no job is still named by its workflow.
		if !seenJobFor(seenJob, r.Name) {
			seenJob[r.Name] = true
			jobs = append(jobs, r.Name)
		}
	}
	report.FailedJobs, report.FailedTests = jobs, tests
	report.Blocks = ciBlocks(cwd, o.sha)
	detail := "CI failed for " + o.sha + "; jobs: " + strings.Join(jobs, ", ")
	if len(tests) > 0 {
		detail += "; tests: " + strings.Join(tests, ", ")
	}
	argv := []string{"ci", "watch", "--sha", o.sha, "--cwd", cwd}
	if o.run != "" {
		argv = append(argv, "--run", o.run)
	}
	home, err := os.UserHomeDir()
	if err == nil {
		report.Heal, err = healQueue(home, o.run, strings.Join(report.Blocks, ","), healq.KindCI, argv, cwd, detail)
	}
	if err != nil {
		return finish("failure", detail+"; heal item not written: "+err.Error(), 1)
	}
	return finish("failure", detail+"\nheal: queued "+report.Heal, 1)
}

func seenJobFor(seen map[string]bool, run string) bool {
	for job := range seen {
		if job == run || strings.HasPrefix(job, run+" / ") {
			return true
		}
	}
	return false
}

// ciBlocks names the block ids in the subjects of sha and the commits it
// merged (sha^..sha); a root commit uses its own subject.
func ciBlocks(cwd, sha string) []string {
	out, err := gitx.Run(cwd, "log", "--format=%s", sha+"^.."+sha)
	if err != nil {
		out, _ = gitx.Run(cwd, "log", "-1", "--format=%s", sha)
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, id := range ciBlockID.FindAllString(out, -1) {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
