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

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/healq"
)

// B430: `code-forge ci watch` follows the GitHub Actions runs of one commit
// through the gh CLI on PATH. Success exits 0; a failure names the failing
// jobs and tests, queues a heal item of kind ci and exits 1; no run after
// ciNoRunAfter exits 2; still running at --timeout exits 3.

func init() { routes["ci"] = runCI }

const ciUsage = "usage: code-forge ci watch --sha <sha> [--run <r>] [--timeout 30m] [--cwd <dir>] [--json]"

// Test seams: the poll interval, the no-run limit and the clock.
var (
	ciPollInterval = 30 * time.Second
	ciNoRunAfter   = 5 * time.Minute
	ciSleep        = time.Sleep
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
	started := time.Now()
	lastError := ""
	for {
		out, err := ciGh(cwd, "run", "list", "--commit", o.sha, "--json", "databaseId,status,conclusion,name")
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
			if len(runs) == 0 && time.Since(started) >= ciNoRunAfter {
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
				return ciFailure(o, cwd, failed, &report, finish)
			}
		}
		if time.Since(started) >= o.timeout {
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
func ciFailure(o ciOptions, cwd string, failed []ciRun, report *ciReport, finish func(string, string, int) int) int {
	jobs, tests := []string{}, []string{}
	seenJob, seenTest := map[string]bool{}, map[string]bool{}
	for _, r := range failed {
		log, err := ciGh(cwd, "run", "view", fmt.Sprint(r.DatabaseID), "--log-failed")
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
