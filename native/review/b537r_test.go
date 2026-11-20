package review

import "testing"

// TestB537MovedCodeSectionsAndJudgeMatchNode checks moved-code hints in
// section and dual-lens packets. The two former split_required cases record
// B1019 Go behaviour: oversized groups split while preserving moved-code
// evidence, and every part's reviewers and judge must pass. Other cases
// retain their original Node recordings byte for byte.
func TestB537MovedCodeSectionsAndJudgeMatchNode(t *testing.T) {
	b545MatchNode(t, "b537r_moved_fixture.json", "b537r_moved_golden.json")
}
