package review

import "testing"

// TestB696MarkdownHeadingSurrogateCutMatchNode: a Markdown full review whose
// second heading is cut at 120 UTF-16 units inside a surrogate pair (117 a's
// then an emoji). Node keeps the pair's high half, and its results and rows
// carry it as \ud83d. Cases recorded from src/review/engine.mjs reviewFile:
// all three sections reviewed, the second section's session unavailable,
// that section alone over the budget (split_required names the cut heading),
// and a needs_file path cut at 200 units inside a pair (needs_file_requested
// keeps the high half). Before B696 Go refused the first three with
// ErrUnsupported (the ticket went to Node) and wrote U+FFFD in the fourth.
func TestB696MarkdownHeadingSurrogateCutMatchNode(t *testing.T) {
	b545MatchNode(t, "b696_md_split_fixture.json", "b696_md_split_golden.json")
}
