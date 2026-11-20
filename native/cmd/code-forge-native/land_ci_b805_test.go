package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// landB805PRs is pull request rows from..to (1-based, newest first); row at
// is block B1's CI PR.
func landB805PRs(t *testing.T, from, to, at int) string {
	t.Helper()
	rows := []map[string]any{}
	for i := from; i <= to; i++ {
		head := fmt.Sprintf("feature-%d", i)
		if i == at {
			head = "ci-b1-3"
		}
		rows = append(rows, map[string]any{"number": 1000 - i, "headRefName": head, "headRefOid": fmt.Sprintf("%040d", i), "state": "OPEN"})
	}
	data, err := json.Marshal(rows)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// B805: gh pr list returns at most --limit (200) rows. With 250 pull
// requests and block B1's CI PR at row 230, the newest 200 miss it; the land
// then searches by head branch and finds it. The fake gh answers the search
// with the rows whose branch starts with ci-b1 (row 230 and a ci-b10 decoy).
func TestB805BlockPRsFoundPast200(t *testing.T) {
	decoy := `{"number":1,"headRefName":"ci-b10","headRefOid":"` + strings.Repeat("e", 40) + `","state":"OPEN"}`
	found := strings.TrimSuffix(landB805PRs(t, 230, 230, 230), "]") + "," + decoy + "]"
	gh := landCIFakeGhSeq(t, map[string]string{
		"prs.json":                   landB805PRs(t, 1, 200, 230),
		"prs-search-head:ci-b1.json": found,
	})
	prs, err := landCIBlockPRs(t.TempDir(), "B1", "ci-")
	if err != nil {
		t.Fatal(err)
	}
	if len(prs) != 1 || prs[0].Number != 770 || prs[0].n != 3 {
		t.Fatalf("block PRs=%+v, want only #770 on ci-b1-3 (row 230 of 250)", prs)
	}
	calls := landCICalls(t, gh)
	if len(calls) != 2 || !strings.Contains(calls[1], "--search head:ci-b1 ") {
		t.Errorf("gh calls=%q, want the newest 200, then a head-branch search", calls)
	}
}

// A first page shorter than the limit is the whole list, and a page that
// holds the block's PR is enough: one call each, no search.
func TestB805NoSearchWhenNotNeeded(t *testing.T) {
	for name, rows := range map[string]string{"short list": landB805PRs(t, 1, 30, 99), "found in page": landB805PRs(t, 1, 200, 7)} {
		gh := landCIFakeGhSeq(t, map[string]string{"prs.json": rows})
		if _, err := landCIBlockPRs(t.TempDir(), "B1", "ci-"); err != nil {
			t.Fatal(err)
		}
		if calls := landCICalls(t, gh); len(calls) != 1 {
			t.Errorf("%s: gh calls=%q, want one", name, calls)
		}
	}
}

// B998 (B805 review): a land whose first pull request page is 200 unrelated
// rows finds block B1's stale CI PR #105 (ci-b1 at an old sha) only by the
// head-branch search, and replaces it: the tip goes to ci-b1-2, the new
// draft PR names #105, and #105 is closed. B2 lands after it as usual.
func TestB998StalePRPast200Replaced(t *testing.T) {
	f := landCIBatch(t, map[string]any{"require_ci": "github"}, "", "")
	f.bareRemote()
	heads := f.heads()
	old := f.git(f.repo, "commit-tree", "-p", f.base, "-m", "B1 before rebase", f.base+"^{tree}")
	f.git(f.repo, "push", "-q", "origin", old+":refs/heads/ci-b1")
	gh := landCIFakeGhSeq(t, map[string]string{
		"prs-1.json":                      landB805PRs(t, 1, 200, 999),
		"prs-search-head:ci-b1.json":      `[{"number":105,"headRefName":"ci-b1","headRefOid":"` + old + `","state":"OPEN"}]`,
		"prs.json":                        `[{"number":200,"headRefName":"ci-b1-2","headRefOid":"` + heads["B1"] + `","state":"OPEN"}]`,
		"runs-" + heads["B1"] + "-1.json": `[]`,
		"runs-" + heads["B1"] + ".json":   landCIGreen,
		"runs-" + heads["B2"] + ".json":   landCIGreen,
		"runs-other.json":                 landCIGreen, // B2 rebased onto B1's merge
	})
	landCIFakeClock(t)
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0: %s", code, output)
	}
	calls := landCICalls(t, gh)
	joined := strings.Join(calls, "\n")
	lists := []string{}
	for _, call := range calls {
		if strings.HasPrefix(call, "pr list ") {
			lists = append(lists, call)
		}
	}
	if len(lists) < 2 || !strings.HasPrefix(lists[0], "pr list --state all --limit 200 ") || !strings.HasPrefix(lists[1], "pr list --state all --search head:ci-b1 --limit 200 ") {
		t.Fatalf("the PR lists must start with the 200-row page, then the head-branch search:\n%s", joined)
	}
	for _, want := range []string{
		"pr create --draft --base main --head ci-b1-2 --title B1: CI for " + heads["B1"][:8] + " --body Replaces #105 ",
		"pr close 105 --comment Replaced by #200: block B1 tip " + heads["B1"][:8],
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("gh calls must contain %q:\n%s", want, joined)
		}
	}
	for _, want := range []string{"ci: pushed tip " + heads["B1"][:8] + " to origin/ci-b1-2 ", "ci: closed #105 (replaced by #200)\n", "block B1 landed\n", "block B2 landed\n"} {
		if !strings.Contains(output, want) {
			t.Errorf("output must contain %q:\n%s", want, output)
		}
	}
}
