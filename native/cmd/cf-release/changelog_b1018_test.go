package main

import (
	"strings"
	"testing"
)

func TestB1018ReleaseDuplicateBlocks(t *testing.T) {
	text := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n- B998: first wording\n- B998: near duplicate\n"
	lines := strings.Split(text, "\n")
	sec := findUnreleased(lines)
	if sec == nil {
		t.Fatal("fixture has no Unreleased")
	}
	if msg := sectionShapeError(lines, sec); !strings.Contains(msg, "duplicate block B998") || !strings.Contains(msg, "first at line 7") {
		t.Fatalf("shape error=%q", msg)
	}
}
