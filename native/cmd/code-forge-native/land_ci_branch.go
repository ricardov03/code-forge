package main

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
)

// B796: with land.require_ci: github, `block land` (and an autopilot that
// runs it) no longer needs a human between a rebase and green CI.
//   - A block tip with no CI run and no open CI pull request at that tip is
//     pushed (never forced) to the block's CI branch: the newest open CI PR's
//     branch when the tip fast-forwards it, else a new <prefix><block>[-<n>]
//     branch with a draft PR "Replaces #N (rebased onto <sha>)"; the replaced
//     PRs are closed with a link to the new one.
//   - The land waits for CI on the exact tip sha up to land.ci_wait (default
//     30m), polling with backoff and printing one progress line a minute.
//     Green lands; red refuses with the failed jobs; the timeout refuses with
//     the command that resumes the land.
//   - A push over SSH that fails on authentication is retried once over
//     HTTPS with the gh credential helper; the output says which one worked.
//   - After the land, the block's open CI PRs are closed.

// Test seams: the clock, the sleep and the poll backoff.
var (
	landCINow       = time.Now
	landCISleep     = time.Sleep
	landCIPollStart = 10 * time.Second
	landCIPollMax   = time.Minute
)

type landCISettings struct {
	wait           time.Duration
	prefix, remote string
	maxFile        int64 // B986: land.max_file_mb in bytes
}

var landCIWaitShape = regexp.MustCompile(`^([0-9]+(ms|s|m|h))+$`)

// landCIConfig reads land.ci_wait, land.ci_branch_prefix and land.ci_remote.
func landCIConfig(cfg map[string]any) (landCISettings, string) {
	land := blockMap(cfg["land"])
	s := landCISettings{wait: 30 * time.Minute, prefix: "ci-", remote: "origin", maxFile: landBigFileLimit(cfg)}
	switch raw := land["ci_wait"].(type) {
	case nil:
	case string:
		if raw == "off" || raw == "0" {
			s.wait = 0
		} else if d, err := time.ParseDuration(raw); err == nil && landCIWaitShape.MatchString(raw) {
			s.wait = d
		} else {
			return s, "land.ci_wait must be off or a duration like 30m, got " + raw
		}
	default:
		return s, fmt.Sprintf("land.ci_wait must be off or a duration like 30m, got %v", raw)
	}
	if raw, ok := land["ci_branch_prefix"].(string); ok && raw != "" {
		s.prefix = raw
	}
	if raw, ok := land["ci_remote"].(string); ok && raw != "" {
		s.remote = raw
	}
	return s, ""
}

type landCIPR struct {
	Number      int    `json:"number"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	State       string `json:"state"`
	n           int
}

// landCIBranchName is <prefix><block> for the first CI branch and
// <prefix><block>-<n> after it.
func landCIBranchName(prefix, id string, n int) string {
	name := prefix + strings.ToLower(id)
	if n > 1 {
		name += "-" + strconv.Itoa(n)
	}
	return name
}

// landCIBlockPRs lists the pull requests (any state) whose head is one of the
// block's CI branches, oldest branch first.
func landCIBlockPRs(main, id, prefix string) ([]landCIPR, error) {
	shape := regexp.MustCompile(`(?i)^` + regexp.QuoteMeta(prefix+id) + `(?:-([0-9]+))?$`)
	list := func(extra ...string) ([]landCIPR, int, error) {
		args := append([]string{"pr", "list", "--state", "all"}, extra...)
		out, err := landCIGh(main, append(args, "--limit", "200", "--json", "number,headRefName,headRefOid,state")...)
		if err != nil {
			return nil, 0, err
		}
		all := []landCIPR{}
		if err := json.Unmarshal([]byte(out), &all); err != nil {
			return nil, 0, fmt.Errorf("gh pr list: unreadable JSON")
		}
		prs := []landCIPR{}
		for _, pr := range all {
			m := shape.FindStringSubmatch(pr.HeadRefName)
			if m == nil {
				continue
			}
			pr.n = 1
			if m[1] != "" {
				pr.n, _ = strconv.Atoi(m[1])
			}
			prs = append(prs, pr)
		}
		return prs, len(all), nil
	}
	prs, rows, err := list()
	if err != nil || rows < 200 {
		return prs, err
	}
	// B805: gh pr list returns at most 200 rows here (newest first), so a
	// full page may hide older PRs even when some match already: search by
	// head branch (a word prefix; the shape above keeps the exact ones).
	known := prs
	merge := func(found []landCIPR) {
		for _, pr := range found {
			seen := false
			for _, previous := range known {
				if previous.Number == pr.Number {
					seen = true
					break
				}
			}
			if !seen {
				known = append(known, pr)
			}
		}
	}
	prs, rows, err = list("--search", "head:"+strings.ToLower(prefix+id))
	if err != nil {
		return nil, err
	}
	merge(prs)
	if rows < 200 {
		return known, nil
	}
	// B981: a full search page (other branches share the word prefix, and
	// GitHub search stops at 1000 results) may still hide older PRs: list
	// each exact CI branch name, stopping after 5 names in a row with none
	// (the push loop skips at most 5 names).
	for n, miss := 1, 0; miss < 5; n++ {
		found, _, err := list("--head", landCIBranchName(prefix, id, n))
		if err != nil {
			return nil, err
		}
		if len(found) == 0 {
			miss++
			continue
		}
		miss = 0
		merge(found)
	}
	return known, nil
}

// landCIOpenPROn finds the open CI pull request on branch, as gh pr create
// prints it (a URL ending /pull/<n>) (B981).
func landCIOpenPROn(main, id, prefix, branch string) (string, bool) {
	prs, err := landCIBlockPRs(main, id, prefix)
	if err != nil {
		return "", false
	}
	for _, pr := range prs {
		if strings.EqualFold(pr.HeadRefName, branch) && strings.EqualFold(pr.State, "OPEN") {
			return fmt.Sprintf("pull/%d", pr.Number), true
		}
	}
	return "", false
}

// landCIPRClose closes pull request n with the B806 retry; before a retry it
// reads the PR's state, and a PR already closed or merged is not closed
// again (B981).
func landCIPRClose(main string, n int, comment string) error {
	number := strconv.Itoa(n)
	_, err := landCIGhOnce(main, func() (string, bool) {
		out, err := landCIGh(main, "pr", "view", number, "--json", "state")
		if err != nil {
			return "", false
		}
		var pr landCIPR
		if json.Unmarshal([]byte(out), &pr) != nil {
			return "", false
		}
		return "", strings.EqualFold(pr.State, "CLOSED") || strings.EqualFold(pr.State, "MERGED")
	}, "pr", "close", number, "--comment", comment)
	return err
}

func landCIShort(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// landCIEnsureBranch puts the block tip on a CI branch with an open PR. It
// returns a refusal, or "" when the tip is on a CI branch (CI may still be
// starting).
func landCIEnsureBranch(main, id, sha, base string, s landCISettings, out io.Writer) string {
	short := landCIShort(sha)
	prs, err := landCIBlockPRs(main, id, s.prefix)
	if err != nil {
		return "cannot list the CI pull requests of block " + id + ": " + strings.SplitN(err.Error(), "\n", 2)[0]
	}
	open, last := []landCIPR{}, 0
	var newest *landCIPR
	for i, pr := range prs {
		if pr.n > last {
			last = pr.n
		}
		if !strings.EqualFold(pr.State, "OPEN") {
			continue
		}
		if strings.EqualFold(pr.HeadRefOid, sha) {
			fmt.Fprintf(out, "ci: tip %s is on %s (#%d); waiting for its CI\n", short, pr.HeadRefName, pr.Number)
			return ""
		}
		open = append(open, pr)
		if newest == nil || pr.n > newest.n {
			newest = &prs[i]
		}
	}
	// A tip that only adds commits to the newest open CI branch updates its PR.
	if newest != nil {
		transport, _, err := landGitPush(main, s.remote, sha+":refs/heads/"+newest.HeadRefName, s.maxFile, out)
		if err == nil {
			fmt.Fprintf(out, "ci: pushed tip %s to %s/%s (#%d) over %s\n", short, s.remote, newest.HeadRefName, newest.Number, transport)
			return ""
		}
		if !landPushRejected(err) {
			return fmt.Sprintf("cannot push tip %s to %s/%s: %s", short, s.remote, newest.HeadRefName, landPushDetail(err))
		}
	}
	branch, pushed := "", false
	for n := last + 1; n <= last+5; n++ {
		branch = landCIBranchName(s.prefix, id, n)
		transport, _, err := landGitPush(main, s.remote, sha+":refs/heads/"+branch, s.maxFile, out)
		if err == nil {
			fmt.Fprintf(out, "ci: pushed tip %s to %s/%s over %s\n", short, s.remote, branch, transport)
			pushed = true
			break
		}
		if !landPushRejected(err) {
			return fmt.Sprintf("cannot push tip %s to %s/%s: %s", short, s.remote, branch, landPushDetail(err))
		}
	}
	if !pushed {
		return fmt.Sprintf("cannot push tip %s: %s rejected the CI branches %s up to %s; push the tip by hand", short, s.remote, landCIBranchName(s.prefix, id, last+1), branch)
	}
	onto := ""
	if mb, err := gitx.Run(main, "merge-base", sha, "refs/heads/"+base); err == nil {
		onto = landCIShort(strings.TrimSpace(mb))
	}
	body := fmt.Sprintf("CI for block %s at %s (base %s %s).", id, sha, base, onto)
	if len(open) > 0 {
		refs := []string{}
		for _, pr := range open {
			refs = append(refs, "#"+strconv.Itoa(pr.Number))
		}
		body = fmt.Sprintf("Replaces %s (rebased onto %s).\n\nCI for block %s at %s.", strings.Join(refs, ", "), onto, id, sha)
	}
	body += "\n\nOpened by code-forge block land (land.require_ci: github); closed when the block lands."
	created, err := landCIGhOnce(main, func() (string, bool) { return landCIOpenPROn(main, id, s.prefix, branch) }, "pr", "create", "--draft", "--base", base, "--head", branch, "--title", id+": CI for "+short, "--body", body)
	if err != nil {
		fmt.Fprintf(out, "warning: ci: gh pr create for %s failed: %s; waiting for a CI run on the push\n", branch, strings.SplitN(err.Error(), "\n", 2)[0])
		return ""
	}
	number := 0
	if m := regexp.MustCompile(`pull/([0-9]+)\s*$`).FindStringSubmatch(strings.TrimSpace(created)); m != nil {
		number, _ = strconv.Atoi(m[1])
	}
	fmt.Fprintf(out, "ci: opened draft PR #%d for %s\n", number, branch)
	for _, pr := range open {
		comment := fmt.Sprintf("Replaced by #%d: block %s tip %s (rebased onto %s).", number, id, short, onto)
		if err := landCIPRClose(main, pr.Number, comment); err != nil {
			fmt.Fprintf(out, "warning: ci: cannot close #%d: %s\n", pr.Number, strings.SplitN(err.Error(), "\n", 2)[0])
			continue
		}
		fmt.Fprintf(out, "ci: closed #%d (replaced by #%d)\n", pr.Number, number)
	}
	return ""
}

// landCICloseLanded closes the open CI pull requests of a landed block. It
// only warns: the land already happened.
func landCICloseLanded(cfg map[string]any, main, id, base string, out io.Writer) {
	if provider, _ := landCIRequired(cfg); provider == "" {
		return
	}
	s, bad := landCIConfig(cfg)
	if bad != "" {
		return
	}
	prs, err := landCIBlockPRs(main, id, s.prefix)
	if err != nil {
		fmt.Fprintf(out, "warning: ci: cannot list the CI pull requests of landed block %s: %s\n", id, strings.SplitN(err.Error(), "\n", 2)[0])
		return
	}
	at := ""
	if head, err := gitx.Run(main, "rev-parse", "--verify", "refs/heads/"+base); err == nil {
		at = " at " + landCIShort(strings.TrimSpace(head))
	}
	for _, pr := range prs {
		if !strings.EqualFold(pr.State, "OPEN") {
			continue
		}
		comment := fmt.Sprintf("Block %s landed into %s%s; this CI pull request is no longer needed.", id, base, at)
		if err := landCIPRClose(main, pr.Number, comment); err != nil {
			fmt.Fprintf(out, "warning: ci: cannot close #%d: %s\n", pr.Number, strings.SplitN(err.Error(), "\n", 2)[0])
			continue
		}
		fmt.Fprintf(out, "ci: closed #%d (block %s landed)\n", pr.Number, id)
	}
}

// landCIResume is the land command that picks a CI wait up again.
func landCIResume(ids []string, run string, flags map[string]any) string {
	argv := append([]string{"code-forge", "block", "land"}, ids...)
	argv = append(argv, "--run", run)
	for _, name := range []string{"report", "transcript", "max"} {
		if value, ok := flags[name].(string); ok && value != "" {
			argv = append(argv, "--"+name, value)
		}
	}
	for _, name := range []string{"merge", "no-merge-gate", "push", "watch-ci", "copy-links", "json"} {
		if flags[name] == true {
			argv = append(argv, "--"+name)
		}
	}
	return closeCommand(argv)
}

// landSSHAuthText names SSH authentication failures, most specific first.
var landSSHAuthText = []string{"communication with agent failed", "agent refused operation", "Permission denied (publickey", "Host key verification failed", "No supported authentication methods", "Could not read from remote repository"}

var (
	landSCPURL = regexp.MustCompile(`^(?:[^@/:]+@)?([^/:]+):(.+)$`)
)

// landRemoteURLKind names the transport of a remote URL.
func landRemoteURLKind(url string) string {
	if scheme, _, ok := strings.Cut(url, "://"); ok {
		if scheme == "git+ssh" || scheme == "ssh+git" {
			return "ssh"
		}
		return scheme
	}
	if landSCPURL.MatchString(url) && !strings.HasPrefix(url, "/") && !strings.HasPrefix(url, ".") {
		return "ssh"
	}
	return "local"
}

// landHTTPSURL turns an SSH remote URL into its HTTPS form ("" when it is not SSH).
func landHTTPSURL(url string) string {
	if landRemoteURLKind(url) != "ssh" {
		return ""
	}
	if _, rest, ok := strings.Cut(url, "://"); ok {
		hostPart, path, _ := strings.Cut(rest, "/")
		if at := strings.LastIndex(hostPart, "@"); at >= 0 {
			hostPart = hostPart[at+1:]
		}
		if colon := strings.Index(hostPart, ":"); colon >= 0 {
			hostPart = hostPart[:colon]
		}
		return "https://" + hostPart + "/" + path
	}
	m := landSCPURL.FindStringSubmatch(url)
	return "https://" + m[1] + "/" + strings.TrimPrefix(m[2], "/")
}

// landGitPush pushes refspec to remote from dir. When the remote is SSH and
// the push fails on SSH authentication, it retries once over HTTPS with the
// gh credential helper and says so. It returns the transport that worked.
// B986: commits not yet on remote that add a file over maxFile bytes (0: no
// check) are refused before anything is pushed (*landBigFileError).
func landGitPush(dir, remote, refspec string, maxFile int64, out io.Writer) (string, string, error) {
	src, _, _ := strings.Cut(refspec, ":")
	if refusal := landBigFileRefusal(dir, maxFile, src, "--remotes="+remote); refusal != "" {
		return "", "", &landBigFileError{refusal}
	}
	url := ""
	if got, err := gitx.Run(dir, "remote", "get-url", "--push", remote); err == nil {
		url = strings.TrimSpace(got)
	}
	kind := landRemoteURLKind(url)
	output, err := gitx.Run(dir, "push", remote, refspec)
	if err == nil {
		return kind, output, nil
	}
	detail := landPushDetail(err)
	https := landHTTPSURL(url)
	line := ""
	for _, text := range landSSHAuthText {
		if strings.Contains(detail, text) {
			line = text
			break
		}
	}
	if https == "" || line == "" || landPushRejected(err) {
		return kind, output, err
	}
	fmt.Fprintf(out, "push: %s over ssh failed (%s); retrying once over https with the gh credential helper\n", remote, line)
	output, err = gitx.Run(dir, "-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential", "push", https, refspec)
	if err != nil {
		return "https", output, fmt.Errorf("push over ssh failed (%s) and the https retry to %s failed: %s", line, https, landPushDetail(err))
	}
	fmt.Fprintf(out, "push: https push to %s worked\n", https)
	return "https", output, nil
}

func landPushDetail(err error) string {
	return strings.TrimSpace(landFirst(landGitError(err, ""), err.Error()))
}

// landPushRejected: the remote refused the update (not a transport failure).
func landPushRejected(err error) bool {
	text := landPushDetail(err)
	return strings.Contains(text, "[rejected]") || strings.Contains(text, "non-fast-forward") || strings.Contains(text, "fetch first") || strings.Contains(text, "Updates were rejected")
}
