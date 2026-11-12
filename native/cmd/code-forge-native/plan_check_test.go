package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// planCheckProject is the Node test's checkVariant folder: plans/x.plan.md
// (good.plan.md with the pairs applied), briefs/tool-brief.md, and a lanes
// ledger in HOME. Expected texts below are what Node printed for the same
// folders (CODE_FORGE_NATIVE=0, temp HOME).
func planCheckProject(t *testing.T, config string, pairs ...string) (home, repo string) {
	t.Helper()
	home, repo = smallProject(t, config)
	read := func(name string) string {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	text := read(filepath.Join(planFixtureRoot, "plans", "good.plan.md"))
	for i := 0; i+1 < len(pairs); i += 2 {
		if strings.Count(text, pairs[i]) != 1 {
			t.Fatalf("fixture text must occur once: %s", pairs[i])
		}
		text = strings.Replace(text, pairs[i], pairs[i+1], 1)
	}
	writeHomeFile(t, filepath.Join(repo, "plans"), "x.plan.md", text)
	writeHomeFile(t, filepath.Join(repo, "briefs"), "tool-brief.md", read(filepath.Join(planFixtureRoot, "briefs", "tool-brief.md")))
	rows := strings.Join([]string{
		`{"event":"decision","question":"lane","block":"B1","answer":"L1","source":"jev"}`,
		`{"event":"decision","question":"lane","block":"B2","answer":" l2 ","source":"rules"}`,
		`{"event":"decision","question":"lane","block":"B3","answer":"lane L3 please","source":"jev","plan":"other/x.plan.md"}`,
		`{"event":"decision","question":"lane","block":"B3","answer":"L1","source":"jev","plan":"y.plan.md"}`,
		`{"event":"decision","question":"lane","block":"B4","answer":"ﬁne","source":"jev"}`,
		`{"event":"decision","question":"lane","block":"B5","answer":"L2","source":"human"}`,
	}, "\n") + "\n"
	writeHomeFile(t, filepath.Join(home, ".code-forge", "ledger"), "lanes.jsonl", rows)
	return home, repo
}

var planFixtureRoot = func() string {
	abs, _ := filepath.Abs(filepath.Join("..", "..", "..", "test", "fixtures"))
	return abs
}()

const (
	planLanes   = "lanes: B1 L1 (jev) · B2 L2 (rules) · B3 L3 (jev)\n"
	planB3Lane  = "block B3: level L1 differs from the recorded lane L3 (jev)\n"
	planOKLanes = "plan check: ok (3 blocks)\nlanes: B1 L1 (jev) · B2 L2 (rules) · B3 L1 (jev)\n"
)

func planCheckCase(t *testing.T, config string, args []string, code int, stdout, stderr string, pairs ...string) {
	t.Helper()
	planCheckProject(t, config, pairs...)
	checkSmallCases(t, []smallCase{{append([]string{"plan", "check", "plans/x.plan.md"}, args...), code, stdout, stderr}})
}

// B475: every case below handed over to Node (plan-check-unported) before.
func TestB475PlanCheckRunsInGo(t *testing.T) {
	lanes := []string{"--slug", "lanes"}
	t.Run("lanes and a differing level", func(t *testing.T) {
		planCheckCase(t, "", lanes, 1, planLanes, planB3Lane)
	})
	t.Run("ok", func(t *testing.T) {
		home, _ := planCheckProject(t, "")
		writeHomeFile(t, filepath.Join(home, ".code-forge", "ledger"), "lanes.jsonl",
			`{"event":"decision","question":"lane","block":"B1","answer":"L1","source":"jev"}`+"\n"+
				`{"event":"decision","question":"lane","block":"B2","answer":"L2","source":"rules"}`+"\n"+
				`{"event":"decision","question":"lane","block":"B3","answer":"L1","source":"jev"}`+"\n")
		checkSmallCases(t, []smallCase{{[]string{"plan", "check", "plans/x.plan.md", "--slug", "lanes"}, 0, planOKLanes, ""}})
	})
	t.Run("slug from config", func(t *testing.T) {
		planCheckCase(t, "project:\n  slug: lanes\n", nil, 1, planLanes, planB3Lane)
	})
	t.Run("directory slug, no ledger", func(t *testing.T) {
		pending := func(id, level string) string {
			return "block " + id + ": level " + level + " has no recorded lane decision — run forge jev ask lane --block " + id + " --state <file> (or --rules when Jev is unavailable)\n"
		}
		planCheckCase(t, "", nil, 1, "lanes: B1 none · B2 none · B3 none\n", pending("B1", "L1")+pending("B2", "L2")+pending("B3", "L1"))
	})
	t.Run("bad slug", func(t *testing.T) {
		planCheckCase(t, "", []string{"--slug", "Bad_Slug"}, 2, "", "plan: --slug must be lowercase letters, digits and hyphens\n")
	})
	t.Run("config refusal", func(t *testing.T) {
		planCheckCase(t, "- 1\n", lanes, 2, "", "plan: config root must be a mapping, got a list\n")
	})
	t.Run("caps and budget", func(t *testing.T) {
		planCheckCase(t, "caps:\n  coders: 1\nbudget:\n  block_cases: 7\n", lanes, 1, planLanes,
			"block B1: cases forecast 8 exceeds budget.block_cases 7 — split it\n"+planB3Lane+
				"dispatch step 1 (wave 1) runs 2 blocks concurrently (caps.coders 1): B1 ∥ B2\n")
	})
	t.Run("no block table", func(t *testing.T) {
		planCheckCase(t, "", lanes, 1, "", "no block table found (a table with id, owned_files and acceptance columns)\n", "| id |", "| ident |")
	})
	t.Run("facts sheet missing", func(t *testing.T) {
		planCheckCase(t, "", lanes, 1, planLanes, "facts sheet missing: run forge facts and paste the sheet into the plan §0\n"+
			"unbackable clause without tolerance: B1 the reviewer argv carries `--json-schema` (1)\n"+
			"unbackable clause without tolerance: B3 the report prints the `claude plugin` status (1)\n"+planB3Lane,
			"<!-- facts-json", "<!-- facts-jsonx")
	})
	t.Run("stale brief", func(t *testing.T) {
		planCheckCase(t, "", lanes, 1, planLanes, "facts sheet is stale: brief changed after it was built\n"+planB3Lane,
			"f84545299f1f5c4016e26cacdef056b08b4ccabbf7819124184180384d14ab17", strings.Repeat("0", 64))
	})
	t.Run("brief not found", func(t *testing.T) {
		planCheckCase(t, "", lanes, 1, planLanes, "facts sheet brief not found: /nonexistent/brief.md\n"+planB3Lane,
			"`../briefs/tool-brief.md`", "`/nonexistent/brief.md`")
	})
	t.Run("facts flag", func(t *testing.T) {
		planCheckCase(t, "", []string{"--slug", "lanes", "--facts", "plans/x.plan.md"}, 1, planLanes, planB3Lane)
	})
	t.Run("near-miss headings warn", func(t *testing.T) {
		planCheckCase(t, "", lanes, 1, planLanes,
			"WARN section \"0.6 Unbackable clauses\" at §0.x read as \"Acceptance clauses the facts sheet cannot back\" — use the exact heading \"Acceptance clauses the facts sheet cannot back\"\n"+
				"WARN section \"3. Callers\" at §3 read as \"Caller map\" — use the exact heading \"Caller map\"\n"+planB3Lane,
			"## 0.6 Acceptance clauses the facts sheet cannot back", "## 0.6 Unbackable clauses", "## Caller map", "## 3. Callers")
	})
	t.Run("sections missing", func(t *testing.T) {
		planCheckCase(t, "", lanes, 1, planLanes, "unbackable-clauses section missing (write \"none\" when there is none)\n"+
			"unbackable clause without tolerance: B1 rows land under `~/.code-forge/ledger` (1)\n"+
			"unbackable clause without tolerance: B2 the reviewer argv never carries `--max-turns` (1)\n"+
			"caller map missing\n"+planB3Lane,
			"## 0.6 Acceptance clauses the facts sheet cannot back", "## Notes", "## Caller map", "## Other")
	})
	t.Run("caller map absent block", func(t *testing.T) {
		planCheckCase(t, "", lanes, 1, planLanes, "caller map: block B1 is absent\n"+planB3Lane, "B1 config → B2, B3 · ", "")
	})
	t.Run("level, forecasts, overlap, cycle", func(t *testing.T) {
		planCheckCase(t, "", lanes, 1, planLanes,
			"block B1: lines forecast 2001 exceeds budget.block_lines 2000 — split it\n"+
				"block B2: no lane (level L0–L3)\n"+
				"block B2: no cases forecast\n"+
				planB3Lane+
				"blocks B1 and B3 (wave 1) overlap: src/config/** / src/config/x.mjs\n"+
				"depends_on cycle: B1 → B3 → B1\n",
			"4 → 8 → **1 200**", "4 → 8 → 2 001 (est)",
			"| Config loader | L1 | — |", "| Config loader | L1 | B3 |",
			"| Reviewer session | L2 |", "| Reviewer session | — |",
			"3 → 6 → **1 200**", "3 → x → **1 200**",
			"`types/node-extra.d.ts`", "`src/config/x.mjs`")
	})
	t.Run("bad owned path", func(t *testing.T) {
		planCheckCase(t, "", lanes, 1, planLanes, planB3Lane+"block B3: owned files: invalid path \"/abs/path\": must be repo-relative, not absolute\n",
			"`types/node-extra.d.ts`", "`/abs/path`")
	})
	t.Run("imports a batch-mate", func(t *testing.T) {
		planCheckCase(t, "", lanes, 1, planLanes, planB3Lane+"block B2 imports src/config/load.mjs owned by B1, which is not in its depends_on\n",
			"`src/util/log.mjs`", "`src/config/load.mjs`")
	})
	t.Run("claims of every kind", func(t *testing.T) {
		clause := "reads $CODE_FORGE_HOME and v1.2.3 and https://x.io/a, and @scope/pkg and ./a/b.txt (1) (`test/a.test.mjs`)"
		planCheckCase(t, "", lanes, 1, planLanes, "unbackable clause without tolerance: B3 "+clause+"\n"+planB3Lane,
			"the report prints the `claude plugin` status (1)", clause)
	})
	t.Run("second wave", func(t *testing.T) {
		wave := "## Wave 2\n\n**Dispatch order:** B4 ∥ B5 ∥ B6\n\n| id | level | owned_files | cases | lines | acceptance |\n|---|---|---|---|---|---|\n| B4 | L1 | `a/**` | 1 | 1 | ok |\n| B5 | L2 | `a/b` | 1 | 1 | ok |\n| B6 | L4 | `c` | (1) | 1 | ok |\n\n## Caller map"
		planCheckCase(t, "", lanes, 1, "lanes: B1 L1 (jev) · B2 L2 (rules) · B3 L3 (jev) · B4 ﬁne (jev) · B5 none · B6 none\n",
			"caller map: block B4 is absent\ncaller map: block B5 is absent\ncaller map: block B6 is absent\n"+planB3Lane+
				"block B4: level L1 differs from the recorded lane ﬁne (jev)\n"+
				"block B5: level L2 has no recorded lane decision — run forge jev ask lane --block B5 --state <file> (or --rules when Jev is unavailable)\n"+
				"block B6: no lane (level L0–L3)\nblock B6: no cases forecast\n"+
				"blocks B4 and B5 (wave 2) overlap: a/** / a/b\n"+
				"dispatch step 1 (wave 2) runs 3 blocks concurrently (caps.coders 2): B4 ∥ B5 ∥ B6\n",
			"## Caller map", wave)
	})
	t.Run("package.json bins", func(t *testing.T) {
		pairs := []string{"the report prints the `claude plugin` status (1)", "the report runs `plan check` (1)"}
		_, repo := planCheckProject(t, "", pairs...)
		writeHomeFile(t, repo, "package.json", `{"name":"@x/code-forge","bin":"./b.mjs"}`)
		checkSmallCases(t, []smallCase{{[]string{"plan", "check", "plans/x.plan.md", "--slug", "lanes"}, 1, planLanes,
			"unbackable clause without tolerance: B3 the report runs `plan check` (1)\n" + planB3Lane}})
		writeHomeFile(t, repo, "package.json", `{bad`)
		checkSmallCases(t, []smallCase{{[]string{"plan", "check", "plans/x.plan.md", "--slug", "lanes"}, 2, "", "plan: package.json at the project root is not valid JSON\n"}})
	})
}

// Cases where Node throws or Go cannot see the config value stay in Node.
func TestB475PlanCheckHandsOverOddCases(t *testing.T) {
	planCheckProject(t, "budget:\n  block_cases: \"5\"\n")
	expectHandover(t, "plan-check-limits", "plan", "check", "plans/x.plan.md", "--slug", "lanes")
	planCheckProject(t, "budget:\n  block_cases: -.inf\n")
	expectHandover(t, "plan-check-limits", "plan", "check", "plans/x.plan.md", "--slug", "lanes")
	planCheckProject(t, "", `{"fact_id":"F1"`, `null,{"fact_id":"F1"`)
	expectHandover(t, "plan-check-node-throws", "plan", "check", "plans/x.plan.md", "--slug", "lanes")
	planCheckProject(t, "caps: [\n")
	expectHandover(t, "config-parse-error", "plan", "check", "plans/x.plan.md", "--slug", "lanes")
}
