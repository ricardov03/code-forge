package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// B765: opt-in land gate. With `land.require_ci: github` in the run-start
// config, `block land --merge` (and a batch land) merges a block only when
// GitHub CI is green for the block branch's own tip sha, read with the gh CLI
// on PATH (`gh run list --commit <sha>`). No run, a run still going, or a red
// run refuses the land before anything is merged or written.
//
// Known-red allowance (as plans/tools/forge/p4/block-ci.sh B769): a red run
// passes only when every top-level failing test name in its failed log
// matches a line of the known-red list (land.ci_red_list) of a block named in
// CODE_FORGE_CI_RED_BLOCKS. The landing block is always dropped from that
// list: a block never covers its own red.

// landCIRedBlocksEnv names the blocks whose known-red lines may cover a red run.
const landCIRedBlocksEnv = "CODE_FORGE_CI_RED_BLOCKS"

var (
	landCIStamp    = regexp.MustCompile(`^[0-9]{4}-[0-9-]+T[0-9:.]+Z ?`)
	landCIFailures = []*regexp.Regexp{
		regexp.MustCompile(`^--- FAIL: (\S+)`),
		regexp.MustCompile(`^✖ (.*\S) \([0-9.]+m?s\)$`),
		regexp.MustCompile(`^not ok [0-9]+ - (.*)$`),
		regexp.MustCompile(`^panic: (test timed out) after `),
	}
)

// landCIRequired reads land.require_ci: "" when the gate is off.
func landCIRequired(cfg map[string]any) (string, string) {
	switch value := blockMap(cfg["land"])["require_ci"]; value {
	case nil, "", "off", false:
		return "", ""
	case "github":
		return "github", ""
	default:
		return "", fmt.Sprintf("land.require_ci must be off or github, got %v", value)
	}
}

// landCIFailedTests names the top-level failing tests of a `gh run view
// --log-failed` text: Go `--- FAIL: TestX`, node:test `✖ name (12ms)` and
// `not ok N - name`, and a Go test binary timeout. Indented subtests are dropped.
func landCIFailedTests(log string) []string {
	seen, names := map[string]bool{}, []string{}
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimRight(line, "\r")
		if i := strings.LastIndex(line, "\t"); i >= 0 {
			line = line[i+1:]
		}
		line = landCIStamp.ReplaceAllString(line, "")
		for _, pattern := range landCIFailures {
			if m := pattern.FindStringSubmatch(line); m != nil && !seen[m[1]] {
				seen[m[1]] = true
				names = append(names, m[1])
				break
			}
		}
	}
	sort.Strings(names)
	return names
}

type landCIRedLine struct {
	block string
	re    *regexp.Regexp
}

// landCIRedList reads the known-red list: one "Bnnn <regexp>" per line (Go
// regexp syntax, matched against the failing test name); blank lines and
// lines starting with # are skipped.
func landCIRedList(path string) ([]landCIRedLine, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := []landCIRedLine{}
	for n, raw := range strings.Split(string(data), "\n") {
		text := strings.TrimSpace(raw)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		block, expr, _ := strings.Cut(text, " ")
		expr = strings.TrimSpace(expr)
		if expr == "" {
			return nil, fmt.Errorf("%s line %d: want \"<block> <regexp>\"", path, n+1)
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %v", path, n+1, err)
		}
		lines = append(lines, landCIRedLine{block: block, re: re})
	}
	return lines, nil
}

// landCIGate checks the CI of one block tip. It returns the lines to print
// when the land may go on, or a refusal ("" = pass) and its next command.
func landCIGate(cfg map[string]any, main, id, sha string) (notes []string, refusal, next string) {
	provider, bad := landCIRequired(cfg)
	if bad != "" {
		return nil, bad, ""
	}
	if provider == "" {
		return nil, "", ""
	}
	short := sha
	if len(short) > 8 {
		short = short[:8]
	}
	next = closeCommand([]string{"code-forge", "ci", "watch", "--sha", sha, "--cwd", main})
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, "land.require_ci: github reads CI runs with the gh CLI, and gh is not on PATH; install gh and run gh auth login (B765)", "gh auth status"
	}
	land := blockMap(cfg["land"])
	args := []string{"run", "list", "--commit", sha}
	if workflow, _ := land["ci_workflow"].(string); workflow != "" {
		args = append(args, "--workflow", workflow)
	}
	args = append(args, "--limit", "100", "--json", "databaseId,status,conclusion,name")
	out, err := ciGh(main, args...)
	if err != nil {
		return nil, "cannot read CI runs for tip " + short + ": " + strings.SplitN(err.Error(), "\n", 2)[0], next
	}
	runs := []ciRun{}
	if err := json.Unmarshal([]byte(out), &runs); err != nil {
		return nil, "cannot read CI runs for tip " + short + ": gh run list: unreadable JSON", next
	}
	if len(runs) == 0 {
		return nil, "tip " + short + " has no CI run; push it and wait for green CI (land.require_ci: github)", next
	}
	// One workflow is green when any of its completed runs (a rerun too)
	// concluded green; the land needs every workflow green.
	order, byName := []string{}, map[string][]ciRun{}
	for _, r := range runs {
		if _, ok := byName[r.Name]; !ok {
			order = append(order, r.Name)
		}
		byName[r.Name] = append(byName[r.Name], r)
	}
	running, red := []string{}, []ciRun{}
	for _, name := range order {
		green, going := false, false
		var failed *ciRun
		for i, r := range byName[name] {
			if r.Status != "completed" {
				going = true
			} else if ciGreen(r.Conclusion) {
				green = true
			} else if failed == nil || r.DatabaseID > failed.DatabaseID {
				failed = &byName[name][i]
			}
		}
		switch {
		case green:
		case going:
			running = append(running, name)
		case failed != nil:
			red = append(red, *failed)
		}
	}
	if len(red) == 0 && len(running) == 0 {
		return []string{fmt.Sprintf("ci: tip %s green (%d runs)", short, len(runs))}, "", ""
	}
	if len(red) == 0 {
		return nil, fmt.Sprintf("tip %s has no green CI run yet (running: %s); wait for green CI (land.require_ci: github)", short, strings.Join(running, ", ")), next
	}
	redText := []string{}
	for _, r := range red {
		redText = append(redText, fmt.Sprintf("%s: %s, run %d", r.Name, r.Conclusion, r.DatabaseID))
	}
	what := fmt.Sprintf("tip %s CI is red (%s)", short, strings.Join(redText, "; "))
	if len(running) > 0 {
		what += fmt.Sprintf(" and still running (%s)", strings.Join(running, ", "))
		return nil, what + "; fix it and wait for green CI (land.require_ci: github)", next
	}
	named, allowed := false, []string{}
	for _, b := range strings.FieldsFunc(os.Getenv(landCIRedBlocksEnv), func(r rune) bool { return r == ',' || r == ' ' }) {
		named = true
		if !strings.EqualFold(b, id) && !strings.EqualFold(b, "B"+id) {
			allowed = append(allowed, b)
		}
	}
	if len(allowed) == 0 {
		if named {
			return nil, what + "; a block never covers its own red (" + id + " dropped from " + landCIRedBlocksEnv + ")", next
		}
		return nil, what + "; fix it, or name the fix blocks of known reds in " + landCIRedBlocksEnv + " with lines in land.ci_red_list", next
	}
	listPath, _ := land["ci_red_list"].(string)
	if listPath == "" {
		return nil, what + "; " + landCIRedBlocksEnv + " is set but land.ci_red_list names no known-red list", next
	}
	if !filepath.IsAbs(listPath) {
		listPath = filepath.Join(main, listPath)
	}
	list, err := landCIRedList(listPath)
	if err != nil {
		return nil, what + "; cannot read the known-red list: " + err.Error(), next
	}
	report, uncovered := []string{}, false
	for _, r := range red {
		log, err := ciGh(main, "run", "view", fmt.Sprint(r.DatabaseID), "--log-failed")
		if err != nil {
			return nil, what + fmt.Sprintf("; cannot read the failed log of run %d: %s", r.DatabaseID, strings.SplitN(err.Error(), "\n", 2)[0]), next
		}
		names := landCIFailedTests(log)
		if len(names) == 0 {
			return nil, what + fmt.Sprintf("; no failing test name in the failed log of run %d (a setup or build step red?); fix it first", r.DatabaseID), next
		}
		for _, name := range names {
			hit := ""
			for _, b := range allowed {
				for _, line := range list {
					if line.block == b && line.re.MatchString(name) {
						hit = b
						break
					}
				}
				if hit != "" {
					break
				}
			}
			if hit == "" {
				hit, uncovered = "NO BLOCK", true
			}
			report = append(report, "  "+name+" -> "+hit)
		}
	}
	if uncovered {
		return nil, what + "; failing tests:\n" + strings.Join(report, "\n") + "\na failing test has no block in " + landCIRedBlocksEnv + "=" + strings.Join(allowed, ",") + " (add \"<block> <regexp>\" to " + listPath + ")", next
	}
	notes = append([]string{"ci: " + what + "; failing tests:"}, report...)
	return append(notes, "ci: tip "+short+" red only on known reds ("+strings.Join(allowed, ",")+"), landing allowed"), "", ""
}
