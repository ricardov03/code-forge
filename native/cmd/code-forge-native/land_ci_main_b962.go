package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// B962: land.require_main_ci (with land.require_ci: github) refuses a land
// while the newest completed push run on the base branch is red, as
// plans/tools/forge/p4/ci-gate.sh (B598/B702) did for our own lands. A red
// base lands only when every top-level failing test of that run matches a
// land.ci_red_list line of a block named in CODE_FORGE_CI_RED_BLOCKS. Unlike
// the tip gate, the landing block may cover the base's red: landing the fix
// block is how a red main turns green. A base with no completed push run yet
// passes with a note (nothing to judge).

type landCIMainRun struct {
	DatabaseID int64  `json:"databaseId"`
	HeadSha    string `json:"headSha"`
	Conclusion string `json:"conclusion"`
	Name       string `json:"name"`
}

// landCIMainRequired reads land.require_main_ci: true, false or unset.
func landCIMainRequired(land map[string]any) (bool, string) {
	switch value := land["require_main_ci"]; value {
	case nil, false:
		return false, ""
	case true:
		return true, ""
	default:
		return false, fmt.Sprintf("land.require_main_ci must be true or false, got %v", value)
	}
}

// landCIMainGate judges the newest completed push run of base. It returns the
// lines to print when the land may go on, or a refusal and its next command.
func landCIMainGate(land map[string]any, main, base string) (notes []string, refusal, next string) {
	on, bad := landCIMainRequired(land)
	if bad != "" || !on {
		return nil, bad, ""
	}
	args := []string{"run", "list", "--branch", base, "--event", "push", "--status", "completed"}
	if workflow, _ := land["ci_workflow"].(string); workflow != "" {
		args = append(args, "--workflow", workflow)
	}
	args = append(args, "--limit", "1", "--json", "databaseId,headSha,conclusion,name")
	out, err := ciGh(main, args...)
	if err != nil {
		return nil, "cannot read the push runs of " + base + ": " + strings.SplitN(err.Error(), "\n", 2)[0], "gh auth status"
	}
	runs := []landCIMainRun{}
	if json.Unmarshal([]byte(out), &runs) != nil {
		return nil, "cannot read the push runs of " + base + ": gh run list: unreadable JSON", "gh auth status"
	}
	if len(runs) == 0 {
		return []string{"ci: " + base + " has no completed push run yet; nothing to judge (land.require_main_ci)"}, "", ""
	}
	r := runs[0]
	short := landCIShort(r.HeadSha)
	if ciGreen(r.Conclusion) {
		return []string{fmt.Sprintf("ci: %s push run %d green (%s)", base, r.DatabaseID, short)}, "", ""
	}
	what := fmt.Sprintf("%s CI is red (%s: %s, push run %d at %s)", base, r.Name, r.Conclusion, r.DatabaseID, short)
	next = closeCommand([]string{"gh", "run", "view", fmt.Sprint(r.DatabaseID), "--log-failed"})
	// No hidden reds: the failing tests are named before any block is asked.
	log, err := ciGh(main, "run", "view", fmt.Sprint(r.DatabaseID), "--log-failed")
	if err != nil {
		return nil, what + fmt.Sprintf("; cannot read the failed log of run %d: %s", r.DatabaseID, strings.SplitN(err.Error(), "\n", 2)[0]), next
	}
	names := landCIFailedTests(log)
	if len(names) == 0 {
		return nil, what + fmt.Sprintf("; no failing test name in the failed log of run %d (a setup or build step red?); fix it first", r.DatabaseID), next
	}
	blocks := strings.FieldsFunc(os.Getenv(landCIRedBlocksEnv), func(c rune) bool { return c == ',' || c == ' ' })
	list, listPath, listProblem := []landCIRedLine{}, "", ""
	if len(blocks) > 0 {
		listPath, _ = land["ci_red_list"].(string)
		if listPath == "" {
			listProblem = landCIRedBlocksEnv + " is set but land.ci_red_list names no known-red list"
		} else {
			if !filepath.IsAbs(listPath) {
				listPath = filepath.Join(main, listPath)
			}
			if list, err = landCIRedList(listPath); err != nil {
				listProblem = "cannot read the known-red list: " + err.Error()
			}
		}
	}
	report, uncovered := []string{}, false
	for _, name := range names {
		hit := "NO BLOCK"
		for _, b := range blocks {
			if landCIMainCovers(list, b, name) {
				hit = b
				break
			}
		}
		uncovered = uncovered || hit == "NO BLOCK"
		report = append(report, "  "+name+" -> "+hit)
	}
	failing := what + "; failing tests:\n" + strings.Join(report, "\n") + "\n"
	switch {
	case len(blocks) == 0:
		return nil, failing + "fix " + base + " first, or name the fix blocks of its reds in " + landCIRedBlocksEnv + " with lines in land.ci_red_list (land.require_main_ci)", next
	case listProblem != "":
		return nil, failing + listProblem, next
	case uncovered:
		return nil, failing + "a failing test has no block in " + landCIRedBlocksEnv + "=" + strings.Join(blocks, ",") + " (add \"<block> <regexp>\" to " + listPath + ")", next
	}
	notes = append([]string{"ci: " + what + "; failing tests:"}, report...)
	return append(notes, "ci: "+base+" red only on known reds ("+strings.Join(blocks, ",")+"), landing allowed"), "", ""
}

// landCIMainCovers is true when a known-red line of block matches name.
func landCIMainCovers(list []landCIRedLine, block, name string) bool {
	for _, line := range list {
		if strings.EqualFold(line.block, block) && line.re.MatchString(name) {
			return true
		}
	}
	return false
}
