package contextpack

import (
	"fmt"
	"math"
)

// DefaultCoderTestParallel is the -p value of the B923 test budget when
// gates.coder_test_parallel is not set.
const DefaultCoderTestParallel = 2

// testBudgetRule is the one wording of the B923 CPU budget: a coder runs
// only the tests it adds or changes, never package-wide, full or stress
// runs; GitHub CI is the full gate. %d is the -p value.
const testBudgetRule = "run only the test names you add or change: go test <pkg> -run '^(TestA|TestB)$' -count=1 -p %d (or your stack's single-test form). No package-wide or full-suite runs, and no stress runs (-count=N) unless the owner asks. GitHub CI is the full gate."

// CoderTestParallel is gates.coder_test_parallel, or the default when the
// key is missing or not a whole number of at least 1.
func CoderTestParallel(cfg map[string]any) int {
	gates, _ := cfg["gates"].(map[string]any)
	switch v := gates["coder_test_parallel"].(type) {
	case int:
		if v >= 1 {
			return v
		}
	case int64:
		if v >= 1 && v <= 1<<20 {
			return int(v)
		}
	case float64:
		if v >= 1 && v == math.Trunc(v) && v <= 1<<20 {
			return int(v)
		}
	}
	return DefaultCoderTestParallel
}

// TestBudgetLine is the B923 rule as one line, with the configured -p.
func TestBudgetLine(cfg map[string]any) string {
	return "Test budget (code-forge): " + fmt.Sprintf(testBudgetRule, CoderTestParallel(cfg))
}

// testBudgetSection is the pack's B923 section.
func testBudgetSection(cfg map[string]any) []string {
	return []string{"## test budget (code-forge)", "- " + fmt.Sprintf(testBudgetRule, CoderTestParallel(cfg))}
}
