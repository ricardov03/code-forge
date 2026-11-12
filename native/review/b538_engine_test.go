package review

import (
	"fmt"
	"strings"
	"testing"
)

// TestB538ReviewFileMatchesNode: the levels.L2 fallback ladder (schema miss,
// timeout, capacity, closed book), review.routes recovery and
// review.second_levels give Node's result, ledger rows, session calls (packet
// text, config), warnings and seam calls, byte for byte (goldens: Node's
// reviewFile on the same repository).
func TestB538ReviewFileMatchesNode(t *testing.T) {
	matchEngineGolden(t, "b538_engine_fixture.json", "b538_engine_golden.json")
}

// TestB538SpawnChecksListsEveryConfig: before anything is written, the
// worker learns each config the review may spawn on: the level's config,
// each pinned ladder entry the closed book allows (the level itself first, no
// ladder of its own; openai/gx is skipped), and the second level with its
// entries.
func TestB538SpawnChecksListsEveryConfig(t *testing.T) {
	useRepoLenses(t)
	repo := buildRepo(t, readFixture(t, "packet_fixture.json"))
	cfg := map[string]any{"provider": "claude", "levels": map[string]any{"L2": map[string]any{"provider": "claude", "model": "m2",
		"fallback": []any{map[string]any{"model": "m2b"}, map[string]any{"provider": "openai", "model": "gx"}}}},
		"review": map[string]any{"second_levels": map[string]any{"L2": map[string]any{"model": "s2", "fallback": []any{map[string]any{"model": "s3", "effort": "low"}}}}}}
	risk := 1.0
	in := Input{RepoRoot: repo, File: "notes.txt", Cfg: cfg, Risk: &risk, RulesDigest: "r", FactsExcerpt: "f", WorkDir: t.TempDir()}
	checks, err := SpawnChecks(in)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, c := range checks {
		l2 := c.Cfg["levels"].(map[string]any)["L2"].(map[string]any)
		got = append(got, fmt.Sprintf("%s/%s %v/%v effort=%v ladder=%v", c.Level, c.Role, l2["provider"], l2["model"], l2["effort"], l2["fallback"]))
	}
	want := []string{
		"L2/reviewer claude/m2 effort=<nil> ladder=[map[model:m2b] map[model:gx provider:openai]]",
		"L2/reviewer claude/m2 effort=<nil> ladder=[]",
		"L2/reviewer claude/m2b effort=<nil> ladder=[]",
		"L2/reviewer claude/s2 effort=<nil> ladder=[map[effort:low model:s3 provider:claude]]",
		"L2/reviewer claude/s2 effort=<nil> ladder=[]",
		"L2/reviewer claude/s3 effort=low ladder=[]",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("checks\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
