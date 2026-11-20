package main

import (
	"strings"
	"testing"
)

func TestB1006FullPageWithMatchesFindsOlderPRs(t *testing.T) {
	recent := `{"number":993,"headRefName":"ci-b1-3","headRefOid":"recent","state":"OPEN"}`
	older := `{"number":12,"headRefName":"ci-b1","headRefOid":"older","state":"OPEN"}`
	gh := landCIFakeGhSeq(t, map[string]string{
		"prs.json":                   landB805PRs(t, 1, 200, 7),
		"prs-search-head:ci-b1.json": "[" + older + "," + recent + "]",
	})
	prs, err := landCIBlockPRs(t.TempDir(), "B1", "ci-")
	if err != nil {
		t.Fatal(err)
	}
	numbers := map[int]bool{}
	for _, pr := range prs {
		if numbers[pr.Number] {
			t.Fatalf("duplicate PR: %+v", prs)
		}
		numbers[pr.Number] = true
	}
	if len(prs) != 2 || !numbers[12] || !numbers[993] {
		t.Fatalf("PRs=%+v, want recent #993 and older #12", prs)
	}
	if calls := landCICalls(t, gh); len(calls) != 2 || !strings.Contains(calls[1], "--search head:ci-b1 ") {
		t.Fatalf("calls=%q, want head search after full page", calls)
	}
}

func TestB1006FullSearchRetainsKnownPRs(t *testing.T) {
	search := strings.TrimSuffix(landB805PRs(t, 1, 199, 7), "]") + `,{"number":12,"headRefName":"ci-b1","headRefOid":"older","state":"OPEN"}]`
	landCIFakeGhSeq(t, map[string]string{
		"prs-1.json":                 landB805PRs(t, 1, 200, 8),
		"prs-search-head:ci-b1.json": search,
	})
	prs, err := landCIBlockPRs(t.TempDir(), "B1", "ci-")
	if err != nil {
		t.Fatal(err)
	}
	numbers := map[int]bool{}
	for _, pr := range prs {
		if numbers[pr.Number] {
			t.Fatalf("duplicate PR: %+v", prs)
		}
		numbers[pr.Number] = true
	}
	if len(prs) != 3 || !numbers[992] || !numbers[993] || !numbers[12] {
		t.Fatalf("PRs=%+v, want union of first and search pages even when exact lookup is empty", prs)
	}
}
