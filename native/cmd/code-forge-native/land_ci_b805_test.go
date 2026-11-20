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
