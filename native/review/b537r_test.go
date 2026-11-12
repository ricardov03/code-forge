package review

import "testing"

// TestB537MovedCodeSectionsAndJudgeMatchNode: the moved-code text (Node's
// movedOf) goes into every packet after B545's sections and dual lenses. A
// Markdown file over the budget with code moved to and from a peer gets, in
// each section's packets, only the moves under that section's headings; the
// section measure counts that text (a group alone over the budget is
// split_required with its heading and size); the dual-lens judge packet
// carries the moved text before the diff, per section too. Result, rows,
// packet text and work dir match Node's reviewFile byte for byte (goldens:
// Node on the same repository, base HEAD, the peer named).
func TestB537MovedCodeSectionsAndJudgeMatchNode(t *testing.T) {
	b545MatchNode(t, "b537r_moved_fixture.json", "b537r_moved_golden.json")
}
