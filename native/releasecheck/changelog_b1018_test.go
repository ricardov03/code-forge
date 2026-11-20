package releasecheck

import (
	"strings"
	"testing"
)

func TestB1018ChangelogDuplicateBlocks(t *testing.T) {
	for _, tc := range []struct {
		name, entries string
		bad           bool
	}{
		{"prefix", "- B998: first wording\n- B998: another wording\n", true},
		{"suffix", "- first wording (B998).\n- another wording (B998)\n", true},
		{"mixed categories", "- B998: first wording\n\n### Added\n\n- another wording (B998).\n", true},
		{"markdown prefix", "- **B998**: first wording\n* `B998`: another wording\n", true},
		{"distinct ids", "- B998: first wording\n- B998b: another wording\n- B9980: third wording\n", false},
		{"references", "- B998: first wording\n- B999: relates to B998 but is different\n", false},
		{"no ids", "- first wording\n- another wording\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "# Changelog\n\n## [Unreleased]\n\n### Fixed\n\n" + tc.entries + "\n## [0.1.0]\n\n### Fixed\n\n- B998: historical entry\n- B998: historical duplicate\n"
			faults := ChangelogFaults(body)
			if tc.bad {
				if len(faults) != 1 || !strings.Contains(faults[0], "B998") || !strings.Contains(faults[0], "first at line 7") || !strings.Contains(faults[0], "duplicate block") {
					t.Fatalf("faults=%v", faults)
				}
			} else if len(faults) != 0 {
				t.Fatalf("unexpected faults=%v", faults)
			}
		})
	}
}
