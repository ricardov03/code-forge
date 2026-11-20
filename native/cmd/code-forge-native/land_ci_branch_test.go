package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// B796 tests: a stale CI branch is replaced by a new branch and draft PR, the
// land waits for CI with a fake clock, red and timeout refuse, and an SSH push
// that fails on authentication is retried over HTTPS. gh is a fake script on
// PATH (no real GitHub); the remote is a local bare repo (no network).

// landCIFakeGhSeq puts a stateful fake gh first on PATH. Every call's argv is
// logged to $dir/calls (one line, args joined by " "). Answers:
//   - run list --commit <sha>: the k-th call for that sha reads
//     runs-<sha>-<k>.json, else runs-<sha>.json; a sha with neither file
//     reads runs-other-<k>.json, else runs-other.json, else [] (B910: a
//     rebased tip whose sha the test cannot know in advance).
//   - pr list: --search <q> reads prs-search-<q>.json when it exists (B805);
//     else the k-th call reads prs-<k>.json, else prs.json, else [].
//   - pr create: prints https://github.com/o/r/pull/<n>, n = 200, 201, ...
//   - pr close: exit 0.
//   - run view <id> --json jobs: jobs-<id>.json; run view <id> --log-failed: log-<id>.txt.
func landCIFakeGhSeq(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	q := closeCommand([]string{dir})
	script := `#!/bin/sh
d=` + q + `
printf '%s\n' "$*" >> "$d/calls"
next() { n=$(cat "$d/count-$1" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$d/count-$1"; echo $n; }
answer() { if [ -f "$d/$1" ]; then cat "$d/$1"; elif [ -f "$d/$2" ]; then cat "$d/$2"; else echo '[]'; fi; }
case "$1 $2" in
  'run list') k=$(next "run-$4"); if [ -f "$d/runs-$4-$k.json" ] || [ -f "$d/runs-$4.json" ]; then answer "runs-$4-$k.json" "runs-$4.json"; else answer "runs-other-$k.json" runs-other.json; fi ;;
  'pr list') if [ "$5" = "--search" ] && [ -f "$d/prs-search-$6.json" ]; then cat "$d/prs-search-$6.json"; else k=$(next pr); answer "prs-$k.json" prs.json; fi ;;
  'pr create') k=$(next create); echo "https://github.com/o/r/pull/$((199+k))" ;;
  'pr close') exit 0 ;;
  'run view') if [ "$4" = "--json" ]; then cat "$d/jobs-$3.json"; else cat "$d/log-$3.txt"; fi ;;
  *) echo "fake gh: unexpected $*" >&2; exit 9 ;;
esac
`
	landWrite(t, filepath.Join(dir, "gh"), script)
	if err := os.Chmod(filepath.Join(dir, "gh"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, text := range files {
		landWrite(t, filepath.Join(dir, name), text)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return dir
}

// landCIFakeClock replaces the CI wait's clock and sleep: a sleep moves the
// clock, so a 30-minute wait takes no wall time. It returns the slept total.
func landCIFakeClock(t *testing.T) *time.Duration {
	t.Helper()
	now := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	slept := new(time.Duration)
	oldNow, oldSleep := landCINow, landCISleep
	landCINow = func() time.Time { return now }
	landCISleep = func(d time.Duration) { now = now.Add(d); *slept += d }
	t.Cleanup(func() { landCINow, landCISleep = oldNow, oldSleep })
	return slept
}

const landCIRunning = `[{"databaseId":7,"status":"in_progress","conclusion":"","name":"CI"}]`

// A stale CI branch (open PR #105 on ci-b1 at an old sha): B1's tip goes to a
// new branch ci-b1-2 with a draft PR that replaces #105, #105 is closed, the
// land waits through pending to green and lands, and after the land the
// block's open CI PRs are closed.
func TestB796StaleBranchNewPRWaitsAndLands(t *testing.T) {
	f := landCIBatch(t, map[string]any{"require_ci": "github"}, "", "")
	bare := f.bareRemote()
	heads := f.heads()
	// The old CI branch holds a tip from before a rebase: B1's tip does not
	// fast-forward it.
	old := f.git(f.repo, "commit-tree", "-p", f.base, "-m", "B1 before rebase", f.base+"^{tree}")
	f.git(f.repo, "push", "-q", "origin", old+":refs/heads/ci-b1")
	gh := landCIFakeGhSeq(t, map[string]string{
		"prs-1.json":                      `[{"number":105,"headRefName":"ci-b1","headRefOid":"` + old + `","state":"OPEN"},{"number":99,"headRefName":"ci-b10","headRefOid":"` + old + `","state":"OPEN"}]`,
		"prs-2.json":                      `[]`,
		"prs-3.json":                      `[{"number":105,"headRefName":"ci-b1","headRefOid":"` + old + `","state":"CLOSED"},{"number":200,"headRefName":"ci-b1-2","headRefOid":"` + heads["B1"] + `","state":"OPEN"}]`,
		"prs-4.json":                      `[{"number":201,"headRefName":"ci-b2","headRefOid":"` + heads["B2"] + `","state":"OPEN"}]`,
		"runs-" + heads["B1"] + "-1.json": `[]`,
		"runs-" + heads["B1"] + "-2.json": landCIRunning,
		"runs-" + heads["B1"] + ".json":   landCIGreen,
		"runs-" + heads["B2"] + "-1.json": `[]`,
		"runs-" + heads["B2"] + ".json":   landCIGreen,
		"runs-other.json":                 landCIGreen, // B910: B2's rebased tip
	})
	slept := landCIFakeClock(t)
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	f.checkMain([]string{"B1", "B2"})
	// ci-b1 was not force-moved; the tip went to ci-b1-2.
	if got := f.git(bare, "rev-parse", "refs/heads/ci-b1"); got != old {
		t.Errorf("remote ci-b1=%s, want it untouched at %s (no force push)", got, old)
	}
	if got := f.git(bare, "rev-parse", "refs/heads/ci-b1-2"); got != heads["B1"] {
		t.Errorf("remote ci-b1-2=%s, want B1 tip %s", got, heads["B1"])
	}
	if got := f.git(bare, "rev-parse", "refs/heads/ci-b2"); got != heads["B2"] {
		t.Errorf("remote ci-b2=%s, want B2 tip %s", got, heads["B2"])
	}
	calls := strings.Join(landCICalls(t, gh), "\n")
	for _, want := range []string{
		"pr create --draft --base main --head ci-b1-2 --title B1: CI for " + heads["B1"][:8] + " --body Replaces #105 (rebased onto " + f.base[:8] + ").",
		"pr close 105 --comment Replaced by #200: block B1 tip " + heads["B1"][:8] + " (rebased onto " + f.base[:8] + ").",
		"pr create --draft --base main --head ci-b2 --title B2: CI for " + heads["B2"][:8] + " --body CI for block B2 at " + heads["B2"],
		"pr close 200 --comment Block B1 landed into main at ",
		"pr close 201 --comment Block B2 landed into main at ",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("gh calls must contain %q:\n%s", want, calls)
		}
	}
	if strings.Contains(calls, "pr close 99 ") || strings.Contains(calls, "pr close 105 --comment Block") {
		t.Errorf("only the block's open CI PRs are closed (ci-b10 is another block):\n%s", calls)
	}
	for _, want := range []string{
		"ci: pushed tip " + heads["B1"][:8] + " to origin/ci-b1-2 over local\n",
		"ci: opened draft PR #200 for ci-b1-2\n",
		"ci: closed #105 (replaced by #200)\n",
		"ci: tip " + heads["B1"][:8] + " green (1 runs)\n",
		"ci: closed #200 (block B1 landed)\n",
		"block B1 landed\n",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output must contain %q:\n%s", want, output)
		}
	}
	if *slept == 0 {
		t.Errorf("the land must wait (fake clock) through pending CI")
	}
}

// Pending CI on the tip (already pushed): the land polls with backoff, prints
// one progress line a minute, and lands when CI turns green.
func TestB796PendingThenGreenLands(t *testing.T) {
	f := landCIBatch(t, map[string]any{"require_ci": "github"}, "", "")
	heads := f.heads()
	files := map[string]string{"runs-" + heads["B2"] + ".json": landCIGreen, "runs-" + heads["B1"] + ".json": landCIGreen, "runs-other.json": landCIGreen}
	for k := 1; k <= 8; k++ {
		files["runs-"+heads["B1"]+"-"+string(rune('0'+k))+".json"] = landCIRunning
	}
	gh := landCIFakeGhSeq(t, files)
	slept := landCIFakeClock(t)
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	f.checkMain([]string{"B1", "B2"})
	if strings.Contains(strings.Join(landCICalls(t, gh), "\n"), "pr create") {
		t.Errorf("a tip with CI runs is never pushed again")
	}
	// 8 running polls: sleeps 10s, 20s, 40s, 60s x5 = 370s.
	if *slept != 370*time.Second {
		t.Errorf("slept %s, want 6m10s (10s doubling to 1m)", *slept)
	}
	lines := strings.Count(output, "ci: waiting for tip "+heads["B1"][:8]+" (running: CI), ")
	if lines < 5 || lines > 7 {
		t.Errorf("progress lines=%d, want one per minute over 6m10s:\n%s", lines, output)
	}
	if !strings.Contains(output, "ci: waiting for tip "+heads["B1"][:8]+" (running: CI), 1m10s of 30m0s\n") {
		t.Errorf("progress line must give elapsed and the limit:\n%s", output)
	}
}

// Red CI: the land refuses with the failed job names and the next step.
func TestB796RedRefusesWithFailedJobs(t *testing.T) {
	f := landCIBatch(t, map[string]any{"require_ci": "github"}, "", "")
	landCIFakeGhSeq(t, map[string]string{
		"runs-" + f.heads()["B1"] + ".json": landCIRed,
		"jobs-7.json":                       `{"jobs":[{"name":"test (ubuntu)","conclusion":"failure"},{"name":"lint","conclusion":"success"},{"name":"build (macos)","conclusion":"cancelled"}]}`,
		"log-7.txt":                         landCIRedLog,
	})
	landCIFakeClock(t)
	output := landCIRefused(t, f, "CI is red (CI: failure, run 7)")
	for _, want := range []string{"failed jobs: test (ubuntu), build (macos)", "next: gh run view 7 --log-failed"} {
		if !strings.Contains(output, want) {
			t.Errorf("output must contain %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "lint") {
		t.Errorf("a green job is not a failed job:\n%s", output)
	}
}

// Timeout: CI still running at land.ci_wait refuses with the resume command
// and merges nothing.
func TestB796TimeoutRefusesWithResume(t *testing.T) {
	f := landCIBatch(t, map[string]any{"require_ci": "github", "ci_wait": "3m"}, "", "")
	landCIFakeGhSeq(t, map[string]string{"runs-" + f.heads()["B1"] + ".json": landCIRunning})
	slept := landCIFakeClock(t)
	output := landCIRefused(t, f, "CI not green after 3m0s (running: CI)")
	if !strings.Contains(output, "next: code-forge block land B1 B2 --run r-batch-unit\n") {
		t.Errorf("output must name the resume command:\n%s", output)
	}
	if *slept != 3*time.Minute {
		t.Errorf("slept %s, want exactly the 3m limit", *slept)
	}
}

// SSH push fails on authentication: the CI branch push and the main push are
// retried once over HTTPS with the gh credential helper, and the output says so.
func TestB796SSHFailRetriesOverHTTPS(t *testing.T) {
	f := landCIBatch(t, map[string]any{"require_ci": "github", "push": true}, "", "")
	bare := f.bareRemote()
	heads := f.heads()
	sshDir := t.TempDir()
	fakeSSH := filepath.Join(sshDir, "ssh")
	landWrite(t, fakeSSH, "#!/bin/sh\necho 'sign_and_send_pubkey: signing failed for ED25519 \"key\" from agent: communication with agent failed' >&2\necho 'git@github.com: Permission denied (publickey).' >&2\nexit 255\n")
	if err := os.Chmod(fakeSSH, 0o700); err != nil {
		t.Fatal(err)
	}
	f.git(f.repo, "remote", "set-url", "origin", "git@github.com:o/r.git")
	f.git(f.repo, "config", "core.sshCommand", fakeSSH)
	// The HTTPS form of the remote resolves to the local bare repo: no network.
	f.git(f.repo, "config", "url."+bare+".insteadOf", "https://github.com/o/r.git")
	landCIFakeGhSeq(t, map[string]string{
		"runs-" + heads["B1"] + "-1.json": `[]`,
		"runs-" + heads["B1"] + ".json":   landCIGreen,
		"runs-" + heads["B2"] + "-1.json": `[]`,
		"runs-" + heads["B2"] + ".json":   landCIGreen,
		"runs-other.json":                 landCIGreen, // B910: B2's rebased tip
	})
	landCIFakeClock(t)
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	if got := f.git(bare, "rev-parse", "refs/heads/ci-b1"); got != heads["B1"] {
		t.Errorf("remote ci-b1=%s, want B1 tip %s", got, heads["B1"])
	}
	if got, want := f.git(bare, "rev-parse", "refs/heads/main"), f.git(f.repo, "rev-parse", "main"); got != want {
		t.Errorf("remote main=%s, want %s (main push retried over https)", got, want)
	}
	retry := "push: origin over ssh failed (communication with agent failed); retrying once over https with the gh credential helper\npush: https push to https://github.com/o/r.git worked\n"
	if got := strings.Count(output, retry); got != 3 {
		t.Errorf("https retry lines=%d, want 3 (ci-b1, ci-b2, main):\n%s", got, output)
	}
	if !strings.Contains(output, "ci: pushed tip "+heads["B1"][:8]+" to origin/ci-b1 over https\n") {
		t.Errorf("output must say the https push worked:\n%s", output)
	}
}

func TestB796HTTPSURL(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:o/r.git":             "https://github.com/o/r.git",
		"ssh://git@github.com/o/r.git":       "https://github.com/o/r.git",
		"ssh://git@example.com:2222/o/r.git": "https://example.com/o/r.git",
		"https://github.com/o/r.git":         "",
		"/srv/git/r.git":                     "",
	} {
		if got := landHTTPSURL(in); got != want {
			t.Errorf("landHTTPSURL(%q)=%q, want %q", in, got, want)
		}
	}
}
