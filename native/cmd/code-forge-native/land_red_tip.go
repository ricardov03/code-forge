package main

import (
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

// B963: one fix round for a red combined tip. When the batch merge gate is
// red on the combined tip, land lists every failing test, maps each test to
// its suspect blocks (a block id in the test name, a changed file named in
// the test line or its Go package, else the bisect culprit), and records ONE
// found block (B800, source land) whose brief carries the whole list. The fix
// is then a reviewed, gated block; nobody hand-commits on the combined branch.

// landRedBlock is one batch block and the files it changes on the tip.
type landRedBlock struct {
	id    string
	files []string
}

// landRedTip is the red combined tip of a batch.
type landRedTip struct {
	tests   []string
	culprit string
	blocks  []landRedBlock
	log     string
}

const landRedMaxTests = 100

var (
	landRedGoFail  = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	landRedGoBuild = regexp.MustCompile(`^FAIL\s+(\S+)\s+\[(build failed|setup failed)\]`)
	landRedGoPkg   = regexp.MustCompile(`^FAIL\s+(\S+)\s+[0-9.]+s\s*$`)
	landRedPkgOf   = regexp.MustCompile(`^\S+ \(([^ ():]+)\)$|^(\S+) \[(?:build|setup) failed\]$`)
)

// landRedTests is every failing test of a suite output, in order, without
// repeats, up to landRedMaxTests: TAP "not ok" lines, Go "--- FAIL:" lines
// (subtests too) named "<test> (<package>)" once the package's FAIL line
// follows, Go packages that failed to build ("<package> [build failed]"), and
// the node spec reporter's "✖ failing tests:" summary ("<name> (<file>:<line>)").
func landRedTests(output string) []string {
	failed, seen := []string{}, map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] && len(failed) < landRedMaxTests {
			failed = append(failed, name)
			seen[name] = true
		}
	}
	pending := []string{} // Go test names waiting for their package line
	flush := func(pkg string) {
		for _, name := range pending {
			if pkg != "" {
				name += " (" + pkg + ")"
			}
			add(name)
		}
		pending = pending[:0]
	}
	summary, where := false, ""
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if match := landRedGoFail.FindStringSubmatch(line); match != nil {
			if !slices.Contains(pending, match[1]) {
				pending = append(pending, match[1])
			}
			continue
		}
		if match := landRedGoPkg.FindStringSubmatch(line); match != nil {
			flush(match[1])
			continue
		}
		if match := landRedGoBuild.FindStringSubmatch(trimmed); match != nil {
			add(match[1] + " [" + match[2] + "]")
			continue
		}
		if match := landTapFailure.FindStringSubmatch(trimmed); match != nil {
			add(strings.TrimSpace(landTapSkip.Split(match[1], 2)[0]))
			continue
		}
		if trimmed == "✖ failing tests:" {
			summary, where = true, ""
			continue
		}
		if !summary {
			continue
		}
		if match := landSpecWhere.FindStringSubmatch(line); match != nil {
			where = match[1] + ":" + match[2]
			continue
		}
		if name, ok := strings.CutPrefix(line, "✖ "); ok {
			name = strings.TrimSpace(landSpecElapsed.ReplaceAllString(name, ""))
			if where != "" {
				name += " (" + where + ")"
			}
			add(name)
			where = ""
		}
	}
	flush("")
	return failed
}

// landRedNamesBlock: the test text holds the block id, not followed by a digit
// (B1 is not named by TestB12).
func landRedNamesBlock(test, id string) bool {
	if id == "" {
		return false
	}
	for rest := test; ; {
		i := strings.Index(rest, id)
		if i < 0 {
			return false
		}
		next := i + len(id)
		if next >= len(rest) || rest[next] < '0' || rest[next] > '9' {
			return true
		}
		rest = rest[next:]
	}
}

// landRedSuspects maps one failing test to its suspect blocks and the why.
func landRedSuspects(test string, red landRedTip) ([]string, string) {
	named := []string{}
	for _, block := range red.blocks {
		if landRedNamesBlock(test, block.id) {
			named = append(named, block.id)
		}
	}
	if len(named) > 0 {
		return named, "test name"
	}
	pkg := ""
	if m := landRedPkgOf.FindStringSubmatch(test); m != nil {
		pkg = m[1] + m[2]
	}
	byFile := []string{}
	for _, block := range red.blocks {
		for _, file := range block.files {
			dir := path.Dir(file)
			if strings.Contains(test, file) || (pkg != "" && dir != "." && (pkg == dir || strings.HasSuffix(pkg, "/"+dir))) {
				byFile = append(byFile, block.id)
				break
			}
		}
	}
	if len(byFile) > 0 {
		return byFile, "changed file"
	}
	if red.culprit != "" {
		return []string{red.culprit}, "bisect culprit"
	}
	all := []string{}
	for _, block := range red.blocks {
		all = append(all, block.id)
	}
	return all, "no match: every batch block"
}

// landRedFinding is the one found block of a red tip: the title, the evidence
// (every failing test with its suspects, every block's changed files) and the
// fingerprint key, so the same red seen again adds no second block.
func landRedFinding(red landRedTip) apfInput {
	ids := []string{}
	for _, block := range red.blocks {
		ids = append(ids, block.id)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "The merge gate is red on the combined tip of %s.\n\nFailing tests (%d), each with its suspect blocks:\n", strings.Join(ids, " "), len(red.tests))
	suspected := map[string]bool{}
	for _, test := range red.tests {
		suspects, why := landRedSuspects(test, red)
		for _, id := range suspects {
			suspected[id] = true
		}
		fmt.Fprintf(&b, "- %s -> %s (%s)\n", test, strings.Join(suspects, " "), why)
	}
	b.WriteString("\nChanged files per block:\n")
	for _, block := range red.blocks {
		files := strings.Join(block.files, ", ")
		if files == "" {
			files = "none"
		}
		fmt.Fprintf(&b, "- %s: %s\n", block.id, files)
	}
	culprit := red.culprit
	if culprit == "" {
		culprit = "none"
	}
	fmt.Fprintf(&b, "\nBisect culprit: %s\n", culprit)
	if red.log != "" {
		fmt.Fprintf(&b, "%s\n", red.log)
	}
	b.WriteString("\nFix every failing test above in this one block, in one round. Do not hand-commit on the combined branch.\n")
	keys := []string{}
	for id := range suspected {
		keys = append(keys, id)
	}
	slices.Sort(keys)
	tests := slices.Clone(red.tests)
	slices.Sort(tests)
	from := red.culprit
	if from == "" && len(keys) > 0 {
		from = keys[0]
	}
	return apfInput{
		source:   "land",
		title:    fmt.Sprintf("red combined tip of %s: %d failing test(s)", strings.Join(ids, " "), len(red.tests)),
		evidence: b.String(),
		from:     from,
		// B921: a red combined tip stops this run's own blocks from landing,
		// so its fix joins this run (customer-breaking), not the backlog.
		severity:    apfSevBreaking,
		labels:      []string{},
		fingerprint: "land-red:" + strings.Join(tests, "\n") + "|suspects:" + strings.Join(keys, " "),
	}
}

// landRedTipOf reads the red tip of a batch: the failing tests of the tip's
// suite and each block's changed files between its prefix and the one below.
// It returns nil when the output names no failing test (the red is not a test
// failure, such as a timeout or a changed tree).
func landRedTipOf(main string, ids, prefixes []string, suite landResult, reason, culprit string) *landRedTip {
	parts := []string{}
	values, _ := suite.data.Get("results").([]any)
	for _, value := range values {
		r := blockObject(value)
		if r.Get("ok") != true {
			parts = append(parts, blockString(r.Get("stdout")), blockString(r.Get("stderr")))
		}
	}
	parts = append(parts, suite.output, suite.error)
	tests := landRedTests(strings.Join(parts, "\n"))
	if len(tests) == 0 {
		tests = landRedTests(reason)
	}
	if len(tests) == 0 {
		return nil
	}
	red := &landRedTip{tests: tests, culprit: culprit}
	for _, line := range strings.Split(reason, "\n") {
		if strings.HasPrefix(line, "full log: ") {
			red.log = line
		}
	}
	for i, id := range ids {
		block := landRedBlock{id: id, files: []string{}}
		if i+1 < len(prefixes) {
			if out, err := gitx.Run(main, "diff", "--name-only", prefixes[i], prefixes[i+1], "--"); err == nil {
				for _, file := range strings.Split(strings.TrimSpace(out), "\n") {
					if file != "" {
						block.files = append(block.files, file)
					}
				}
			}
		}
		red.blocks = append(red.blocks, block)
	}
	return red
}

// landRedRecord records the red tip as one found block of the run and prints
// it. A record error is a warning: the land already stopped on the red.
func landRedRecord(home, run string, red *landRedTip, json bool, emit func(ledger.Object), print io.Writer) {
	results, err := apfRecord(home, run, []apfInput{landRedFinding(*red)}, time.Now().UTC().Truncate(time.Millisecond))
	if err != nil {
		fmt.Fprintf(print, "land: WARN the red combined tip was not recorded as a fix block: %v\n", err)
		return
	}
	if len(results) == 0 {
		fmt.Fprintf(print, "land: WARN the red combined tip was not recorded as a fix block: the run recorded no finding\n")
		return
	}
	entry := results[0].entry
	block, _ := entry.Get("block").(string)
	finding := autopilotText(entry.Get("finding"))
	if json {
		emit(ledger.Object{{Key: "event", Value: "fix_block"}, {Key: "finding", Value: finding}, {Key: "block", Value: entry.Get("block")}, {Key: "state", Value: entry.Get("state")}, {Key: "brief", Value: entry.Get("brief")}, {Key: "tests", Value: len(red.tests)}, {Key: "duplicate", Value: results[0].duplicate}})
		return
	}
	switch {
	case results[0].duplicate:
		fmt.Fprintf(print, "land: the red combined tip is finding %s again (fix block %s); no new block\n", finding, autopilotText(entry.Get("block")))
	case block == "":
		fmt.Fprintf(print, "land: the red combined tip is finding %s; it waits for the owner (%s)\n", finding, autopilotText(entry.Get("reason")))
	default:
		fmt.Fprintf(print, "land: fix block %s queued for the red combined tip (finding %s, %d failing test(s)); brief %s\n", block, finding, len(red.tests), autopilotText(entry.Get("brief")))
	}
}
