package review

import "testing"

// TestB696MarkdownHeadingSurrogateCutMatchNode checks UTF-16 heading cuts
// and needs_file paths. The former oversized-heading stop records B1019 Go
// behaviour: its diff splits into approvable parts, retaining the heading's
// lone high surrogate in section metadata. Other cases keep Node recordings.
func TestB696MarkdownHeadingSurrogateCutMatchNode(t *testing.T) {
	b545MatchNode(t, "b696_md_split_fixture.json", "b696_md_split_golden.json")
}
