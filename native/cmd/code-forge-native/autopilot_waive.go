package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/blockstate"
	"github.com/ricardov03/code-forge/native/childreg"
	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B681: `autopilot waive` in Go (runAction in src/cli/autopilot.mjs and
// waiveForOwner in src/autopilot/actions.mjs): the delegate's acted decision
// waives one warning or nit finding the block's signed review rows record,
// written as a signed review.waived row (by: autopilot), with one tracked
// issue in the project's GitHub repo (deduped by a body marker).

// awNeverCritical is NEVER_CRITICAL.
const awNeverCritical = "critical findings are never delegated"

// awWaiverLabel is WAIVER_LABEL.
const awWaiverLabel = "autopilot-waiver"

// awDedupeLimit is DEDUPE_LIMIT.
const awDedupeLimit = 100

// awCancelled is CANCELLED_COMMENT.
const awCancelled = "waiver cancelled: autopilot grant ended"

var (
	awFindingID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,63}$`)
	awRepoName  = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	awURLLine   = regexp.MustCompile(`^https://\S+$`)
	awIssueURL  = regexp.MustCompile(`^https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/issues/(\d+)$`)
	awNewline   = regexp.MustCompile(`\r?\n`)
)

// awInput is the checked request: the normalised file and the scrubbed reason.
type awInput struct {
	block, file, finding, severity, reason, decision string
}

// awUsage is runAction's need() for each flag, then waiveForOwner's checks
// before readRun, in their order. A proof finding is a refusal (exit 1),
// the others are usage errors (exit 2).
func awUsage(flags autopilotFlags) (awInput, error) {
	for _, name := range []string{"block", "file", "finding", "severity", "reason", "decision"} {
		if value, ok := flags.values[name]; !ok || jsTrimEmpty(value) {
			return awInput{}, autopilotUsageFail("autopilot waive needs --" + name)
		}
	}
	in := awInput{block: flags.values["block"], finding: flags.values["finding"], severity: flags.values["severity"], decision: flags.values["decision"]}
	if in.severity != "warning" && in.severity != "nit" {
		return awInput{}, autopilotUsageFail("--severity must be warning or nit (" + awNeverCritical + ")")
	}
	if !awFindingID.MatchString(in.finding) {
		return awInput{}, autopilotUsageFail("a finding id is required (letters, digits, . _ : -)")
	}
	in.reason = apdScrub(jsTrim(flags.values["reason"]))
	if _, _, hit := lrFindSecret(in.reason); hit {
		return awInput{}, autopilotUsageFail("the reason looks like it holds a secret; reword it")
	}
	if in.finding == "proof" {
		return awInput{}, &roundRefused{"proof is never delegated; only the owner can waive it (block waive)"}
	}
	if !roundDecisionID.MatchString(in.decision) {
		return awInput{}, autopilotUsageFail("autopilot waive needs --decision <id> (the delegate's acted decision from autopilot ask)")
	}
	file, ok, _ := roundRepoPath(flags.values["file"])
	if !ok {
		return awInput{}, autopilotUsageFail("--file must be a repo-relative path")
	}
	in.file = file
	return in, nil
}

// awSupported says whether Go reads this waive the way Node does: what
// roundSupported checks (block names on Object.prototype, the path's
// normalisation, the key, the named decision row).
func awSupported(home, runID string, record ledger.Object, flags autopilotFlags, rows []ledger.Row) bool {
	return roundSupported(home, runID, record, flags, rows)
}

// awGrantFor is grantFor(runId, 'waive:<severity>', now): the review budget
// category is the one a pause names.
func awGrantFor(home, runID, scope string, now time.Time) (ledger.Object, error) {
	checked, err := autopilotCheckExpiry(home, runID, now)
	if err != nil {
		return nil, err
	}
	if checked.Grant == nil {
		return nil, roundGrantRefusal("no-grant")
	}
	switch checked.State {
	case "run-ended", "stopped", "expired":
		return nil, roundGrantRefusal(checked.State)
	}
	if refusal := roundScopeRefusal(checked.Grant, scope); refusal != "" {
		return nil, roundGrantRefusal(refusal)
	}
	if paused, ok := checked.Grant.Get("paused").(ledger.Object); ok {
		for _, entry := range paused {
			if entry.Key == "review" {
				return nil, roundGrantRefusal("paused")
			}
		}
	}
	return checked.Grant, nil
}

// awDecisionProblem is decisionProblem with a block, file and finding subject.
func awDecisionProblem(rows []map[string]any, in awInput, grantID, scope string, at int64, key []byte) string {
	problem := roundDecisionProblem(rows, in.decision, grantID, scope, "waive", in.block, in.file, at, key)
	if problem != "" && problem != "decision "+in.decision+" is older than 30 min (or not yet made)" {
		return problem
	}
	// the finding is compared after the file and before the age
	for _, r := range rows {
		if r["event"] == "autopilot.decision" && r["decision_id"] == in.decision {
			if named, _ := r["subject"].(map[string]any); named["finding"] != in.finding {
				return "decision " + in.decision + " is about another finding"
			}
			break
		}
	}
	return problem
}

// awText is waiverIssueText.
type awText struct{ title, body, comment, marker string }

func awIssueText(runID string, in awInput, severity, grantID, ts string) awText {
	marker := "<!-- code-forge-waiver: " + runID + "/" + in.block + "/" + in.file + "/" + in.finding + " -->"
	reason := awNewline.ReplaceAllString(apdScrub(in.reason), " ")
	title := apdScrub("code-forge autopilot waived " + severity + " " + in.finding + " in " + in.file + " (block " + in.block + ")")
	body := strings.Join([]string{
		"code-forge autopilot waived a " + severity + " review finding while the owner was away. It still needs a follow-up.",
		"",
		"| field | value |",
		"|---|---|",
		"| run | " + runID + " |",
		"| block | " + in.block + " |",
		"| file | `" + in.file + "` |",
		"| finding | " + in.finding + " |",
		"| severity | " + severity + " |",
		"| grant | " + grantID + " |",
		"| waived at | " + ts + " |",
		"",
		"Reason: " + reason,
		"",
		marker,
		"",
	}, "\n")
	comment := "Waived again by code-forge autopilot at " + ts + " (grant " + grantID + "). Reason: " + reason + "\n\n" + marker + "\n"
	return awText{title: title, body: apdScrub(body), comment: apdScrub(comment), marker: marker}
}

// awGhIn is ghRun with a cwd (exec's spawn in the run's workspace): ok and
// stdout. A spawn that fails, a timeout or an exit other than 0 is not ok.
func awGhIn(dir string, argv []string) (bool, string) {
	_, _ = lrProcRoot() // exec makes the run root first
	ctx, cancel := context.WithTimeout(context.Background(), lrGhTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	env := childreg.Env
	if env == nil {
		env = runChildOwnerEnv
	}
	cmd.Env = env(os.Environ())
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if cmd.Start() != nil {
		return false, ""
	}
	settled := childreg.Begin(lrRegisterChild, cmd.Process.Pid, argv[0])
	err := cmd.Wait()
	settled()
	if err != nil || ctx.Err() != nil || stdout.Len() > childMaxBuffer || stderr.Len() > childMaxBuffer {
		return false, ""
	}
	return true, nodeUTF8(stdout.String())
}

// awIssue is trackWaiverIssue's result: url "" with the failure code.
type awIssue struct{ url, action, failure string }

// awFindReported is findReported with the match option: the first listed
// issue whose body holds the marker; a full page without it fails.
func awFindReported(repo, marker string) (number float64, url string, failed bool) {
	res := lrExec([]string{"gh", "issue", "list", "--repo", repo, "--state", "all", "--search", "\"" + marker + "\" in:body", "--json", "number,url,state,body", "--limit", strconv.Itoa(awDedupeLimit)}, lrGhTimeout)
	if !res.ok {
		return 0, "", true
	}
	var list any
	if json.Unmarshal([]byte(nodeUTF8(res.stdout)), &list) != nil {
		return 0, "", true
	}
	items, ok := list.([]any)
	if !ok {
		return 0, "", true
	}
	for _, item := range items {
		obj, _ := item.(map[string]any)
		n, isNum := obj["number"].(float64)
		link, isURL := obj["url"].(string)
		body, isBody := obj["body"].(string)
		if !isNum || n != math.Trunc(n) || math.Abs(n) > 9007199254740991 || !isURL || !strings.HasPrefix(link, "https://") || !isBody || !strings.Contains(body, marker) {
			continue
		}
		return n, link, false
	}
	if len(items) >= awDedupeLimit {
		return 0, "", true // too-many-results
	}
	return 0, "", false
}

// awTrackIssue is trackWaiverIssue: never fails the waive.
func awTrackIssue(workspace string, text awText) awIssue {
	failed := func(code string) awIssue { return awIssue{failure: code} }
	if !lrOnPath("gh") {
		return failed("gh-missing")
	}
	if res := lrExec([]string{"gh", "auth", "status"}, lrGhTimeout); !res.ok {
		return failed("gh-not-logged-in")
	}
	ok, stdout := awGhIn(workspace, []string{"gh", "repo", "view", "--json", "nameWithOwner"})
	if !ok {
		return failed("no-project-repo")
	}
	var view any
	if json.Unmarshal([]byte(stdout), &view) != nil {
		return failed("no-project-repo")
	}
	obj, _ := view.(map[string]any)
	repo, _ := obj["nameWithOwner"].(string)
	if !awRepoName.MatchString(repo) {
		return failed("no-project-repo")
	}
	if _, _, hit := lrFindSecret(text.title + "\n" + text.body + "\n" + text.comment); hit {
		return failed("secret-in-body")
	}
	number, link, searchFailed := awFindReported(repo, text.marker)
	if searchFailed {
		return failed("search-failed")
	}
	if link != "" {
		n := strconv.FormatFloat(number, 'f', -1, 64)
		if lrGhWithBodyFile(text.comment, func(file string) [][]string {
			return [][]string{{"gh", "issue", "comment", n, "--repo", repo, "--body-file", file}}
		}, nil) != "" {
			return failed("comment-failed")
		}
		return awIssue{url: link, action: "commented"}
	}
	url := ""
	why := lrGhWithBodyFile(text.body, func(file string) [][]string {
		base := []string{"gh", "issue", "create", "--repo", repo, "--title", text.title, "--body-file", file}
		return [][]string{append(slices.Clone(base), "--label", awWaiverLabel), base}
	}, func(stdout string) {
		for _, line := range strings.Split(stdout, "\n") {
			if line = jsTrim(line); awURLLine.MatchString(line) {
				url = line
			}
		}
	})
	if why != "" || url == "" {
		return failed("create-failed")
	}
	return awIssue{url: url, action: "created"}
}

// awCancelOnIssue is cancelOnIssue: best effort, one comment.
func awCancelOnIssue(url, marker string) {
	m := awIssueURL.FindStringSubmatch(url)
	if m == nil {
		return
	}
	lrGhWithBodyFile(awCancelled+"\n\n"+marker+"\n", func(file string) [][]string {
		return [][]string{{"gh", "issue", "comment", m[2], "--repo", m[1], "--body-file", file}}
	}, nil)
}

// autopilotWaive is runAction('waive').
func autopilotWaive(o *runOut, home, runID string, flags autopilotFlags) int {
	report := func(err error) int {
		var refused *roundRefused
		if errors.As(err, &refused) {
			o.err("autopilot waive: refused — " + refused.message + "\n")
			return 1
		}
		return autopilotReport(o, "waive", err)
	}
	clock := func() time.Time { return autopilotNow().Truncate(time.Millisecond) }
	in, err := awUsage(flags)
	if err != nil {
		return report(err)
	}
	record, err := state.ReadRun(home, runID)
	if err != nil {
		return report(err)
	}
	if blocks, _ := record.Get("blocks").(ledger.Object); blocks == nil || !runTruthy(blocks.Get(in.block)) {
		return report(&state.Error{Code: "unknown_block", Message: "block " + in.block + " is not in run " + runID})
	}
	key, err := rebaseKey(home, runID)
	if err != nil {
		return report(err)
	}
	autopilotRegisterKey(home, runID) // loadKey registers the key
	project, _ := record.Get("project").(string)
	recordRun, _ := record.Get("run_id").(string)
	workspace, _ := record.Get("workspace").(string)
	rows, err := roundRunRows(home, project, recordRun)
	if err != nil {
		return report(err)
	}
	severity := blockstate.Severity(awRows(rows), in.block, in.file, in.finding, key)
	switch severity {
	case "not-recorded", "unverified", "unknown":
		return report(&roundRefused{"finding " + in.finding + " in " + in.file + " has no verifiable recorded severity (" + severity + "); " + awNeverCritical})
	case "warning", "nit":
	default:
		return report(&roundRefused{"finding " + in.finding + " in " + in.file + " is recorded as " + severity + "; " + awNeverCritical})
	}
	if severity != in.severity {
		return report(&roundRefused{"finding " + in.finding + " in " + in.file + " is recorded as " + severity + ", not " + in.severity})
	}
	scope := "waive:" + severity
	now := clock()
	grant, err := awGrantFor(home, runID, scope, now)
	if err != nil {
		return report(err)
	}
	grantID, _ := grant.Get("grant_id").(string)
	if rows, err = roundRunRows(home, project, recordRun); err != nil {
		return report(err)
	}
	if problem := awDecisionProblem(rows, in, grantID, scope, now.UnixMilli(), key); problem != "" {
		return report(&roundRefused{problem + "; the question goes to the owner (autopilot ask, then --decision <id>)"})
	}
	text := awIssueText(runID, in, severity, grantID, autopilotStamp(now))
	var row ledger.Object
	var issue awIssue
	issueRowWritten := true
	err = filelock.WithLock("autopilot-waive\x00"+runID+"\x00"+in.block+"\x00"+in.file+"\x00"+in.finding, func() error {
		issue = awTrackIssue(workspace, text)
		err := state.WithRunLock(home, runID, func() error {
			fresh := clock()
			if err := roundLiveRefusal(home, runID, grantID, scope, fresh); err != nil {
				return err
			}
			rows, err := roundRunRows(home, project, recordRun)
			if err != nil {
				return err
			}
			if problem := awDecisionProblem(rows, in, grantID, scope, fresh.UnixMilli(), key); problem != "" {
				return &roundRefused{problem + "; the question goes to the owner (autopilot ask, then --decision <id>)"}
			}
			var url any
			if issue.url != "" {
				url = issue.url
			}
			row, err = autopilotWriteSigned(home, runID, ledger.Object{{Key: "event", Value: "review.waived"}, {Key: "block", Value: in.block}, {Key: "file", Value: in.file}, {Key: "finding", Value: in.finding}, {Key: "reason", Value: in.reason}, {Key: "by", Value: "autopilot"}, {Key: "grant_id", Value: grantID}, {Key: "decision_id", Value: in.decision}, {Key: "severity", Value: severity}, {Key: "issue", Value: url}, {Key: "ts", Value: autopilotStamp(fresh)}})
			return err
		})
		if err != nil {
			var refused *roundRefused
			if issue.url != "" && errors.As(err, &refused) {
				awCancelOnIssue(issue.url, text.marker)
			}
			return err
		}
		if issue.url == "" {
			_, failed := autopilotWriteSigned(home, runID, ledger.Object{{Key: "event", Value: "autopilot.issue_failed"}, {Key: "block", Value: in.block}, {Key: "file", Value: in.file}, {Key: "finding", Value: in.finding}, {Key: "grant_id", Value: grantID}, {Key: "reason", Value: issue.failure}, {Key: "ts", Value: row.Get("ts")}})
			issueRowWritten = failed == nil
		}
		return nil
	}, filelock.Options{})
	if err != nil {
		return report(err)
	}
	line := "issue " + issue.url + " (" + issue.action + ")"
	if issue.url == "" {
		line = "issue none (" + issue.failure + "; the waiver still stands"
		if !issueRowWritten {
			line += "; the autopilot.issue_failed row could not be written"
		}
		line += ")"
	}
	o.out("autopilot waive run " + runID + ": waived " + in.finding + " (" + bnJSString(row.Get("severity")) + ") in " + bnJSString(row.Get("file")) + ", block " + in.block + " (by: autopilot, grant " + bnJSString(row.Get("grant_id")) + ") · " + line + "\n")
	return 0
}

// awRows is the run's rows as the gate's row type.
func awRows(rows []map[string]any) []gatecheck.Row {
	out := make([]gatecheck.Row, len(rows))
	for i, r := range rows {
		out[i] = r
	}
	return out
}
