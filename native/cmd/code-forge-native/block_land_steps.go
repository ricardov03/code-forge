package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/blockclose"
	"github.com/ricardov03/code-forge/native/blockops"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
)

// A composed step must never start Node, including after a concurrent change.
// Preflight runs the verb's read-only boundary with discarded output.
const landHandover = -1

type landStep struct {
	preflight      bool
	stdout, stderr io.Writer
	ctx            context.Context
	record         ledger.Object
}

func (s *landStep) writers() (io.Writer, io.Writer) {
	if s == nil {
		return os.Stdout, os.Stderr
	}
	out, err := s.stdout, s.stderr
	if out == nil {
		out = io.Discard
	}
	if err == nil {
		err = io.Discard
	}
	return out, err
}

type landResult struct {
	code          int
	output, error string
	data          ledger.Object
}

func landCapture(run func(*landStep) int, ctx context.Context) landResult {
	var out, err bytes.Buffer
	code := run(&landStep{stdout: &out, stderr: &err, ctx: ctx})
	data, _ := ledger.DecodeObject(out.Bytes())
	return landResult{code, out.String(), err.String(), data}
}

// Findings detail uses the same result decoder and ordered summary as --wait.
// A completed block answer names completed tickets, so no polling is needed.
func landTicket(root, ticket string) landResult {
	result, ok := reviewWaitResult(root, filepath.Join(root, ".code-forge/queue", ticket+".done"))
	if !ok {
		return landResult{code: 1, error: "reviews did not finish"}
	}
	out := ledger.Object{{Key: "ticket", Value: ticket}, {Key: "status", Value: "done"}, {Key: "result", Value: result}}
	if !reviewWaitSummary(&out, result) {
		return landResult{code: 1, error: "reviews did not finish"}
	}
	data, _ := ledger.Stringify(out, false)
	return landResult{output: string(data) + "\n", data: out}
}

func landClaimFiles(home, run, id, top, main string, record, entry ledger.Object) ([]string, error) {
	base := blockString(blockObject(entry.Get("worktree")).Get("base_sha"))
	if base == "" {
		base = blockString(entry.Get("base_sha"))
	}
	changed, err := gitx.ChangedFiles(top, base)
	if err != nil {
		return nil, err
	}
	values, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), blockString(record.Get("project")))
	if err != nil {
		return nil, err
	}
	rows := []gatecheck.Row{}
	for _, v := range values {
		if r, ok := v.(map[string]any); ok && r["run"] == run {
			rows = append(rows, r)
		}
	}
	key, err := blockclose.ReadKey(home, run)
	if err != nil {
		return nil, err
	}
	files, warnings, checked := gatecheck.Scope(top, gatecheck.Prefix(main, blockString(record.Get("workspace"))), id, run, blockObject(record.Get("blocks")), changed, rows, key, entry.Get("worktree") != nil)
	if !checked {
		return nil, fmt.Errorf("cannot auto-claim without a checked block scope")
	}
	// Scope warnings identify siblings in a shared tree; auto-claim includes them.
	for _, warning := range warnings {
		prefix := "WARN block " + id + ": "
		file, _, ok := strings.Cut(strings.TrimPrefix(warning, prefix), " changed and is owned by open block ")
		if ok {
			files = append(files, file)
		}
	}
	gitx.SortStrings(files)
	unique := []string{}
	for _, f := range files {
		if len(unique) == 0 || unique[len(unique)-1] != f {
			unique = append(unique, f)
		}
	}
	return unique, nil
}

func landAutoClaim(home, run, id, top string, record ledger.Object, files []string, print io.Writer, preflight bool) error {
	cfg, _ := blockSnapshot(record)
	why := ""
	external := blockMap(blockMap(cfg["review"])["external"])
	if external["required"] == true && strings.TrimSpace(blockString(external["name"])) != "" {
		why = "review.external.required is true"
	} else if blockMap(cfg["proof"])["runners"] != nil {
		why = "proof.runners is set"
	}
	guarded, err := blockops.Guarded(record, why)
	if err != nil {
		return err
	}
	for _, file := range files {
		if err := blockops.ValidateClaim(file); err != nil {
			return err
		}
	}
	if preflight {
		// Check every enqueue before the first claim can publish a signed row.
		for _, file := range files {
			if reviewEnqueueStep([]string{file, "--block", id, "--run", run, "--cwd", top}, &landStep{preflight: true}) == landHandover {
				return blockops.ErrHandover
			}
		}
		return nil
	}
	for _, file := range files {
		if err := blockops.ClaimAuto(home, run, id, file, guarded, why, func(r ledger.Object) bool { return blockGrantEndSafe(r) && blockStorageSafe(home, run, r) }, true); err != nil {
			return err
		}
		fmt.Fprintf(print, "block %s auto-claimed %s\n", id, file)
		result := landCapture(func(s *landStep) int {
			return reviewEnqueueStep([]string{file, "--block", id, "--run", run, "--cwd", top}, s)
		}, nil)
		if result.code != 0 {
			return fmt.Errorf("%s", landFirst(result.error, result.output, "auto-claim review failed"))
		}
	}
	return nil
}

// landSuiteFailure names the failing tests of a red suite and saves its whole
// output under the run folder, so a failed land never needs a rerun to read it.
func landSuiteFailure(home, run, id string, suite landResult) string {
	values, _ := suite.data.Get("results").([]any)
	if suite.code == 0 && suite.data.Get("allOk") == true {
		for _, value := range values {
			r := blockObject(value)
			if r.Get("gate") == "test" && r.Get("skipped") != true {
				return ""
			}
		}
		return "full suite unavailable: configure gates.test before merging"
	}
	outputs, whole := []string{}, []string{}
	for _, value := range values {
		r := blockObject(value)
		text := blockString(r.Get("stdout")) + "\n" + blockString(r.Get("stderr"))
		whole = append(whole, fmt.Sprintf("== gate %s ok=%v ==\n%s", blockString(r.Get("gate")), r.Get("ok"), text))
		if r.Get("ok") != true {
			outputs = append(outputs, text)
		}
	}
	output := strings.Join(outputs, "\n")
	logLine := "full log: " + landSuiteLog(home, run, id, strings.Join(append(whole, suite.output, suite.error), "\n"))
	if failed := landFailedTests(output); len(failed) > 0 {
		return "full suite failed:\n" + strings.Join(failed, "\n") + "\n" + logLine
	}
	lines := strings.Split(strings.TrimSpace(landFirst(suite.error, output, suite.output)), "\n")
	return "full suite failed:\n" + strings.Join(lines[max(0, len(lines)-20):], "\n") + "\n" + logLine
}

var (
	landTapFailure  = regexp.MustCompile(`^not ok\s+\d+\s+-\s+(.+)$`)
	landTapSkip     = regexp.MustCompile(`(?i)\s*#\s*(SKIP|TODO)\b`)
	landSpecWhere   = regexp.MustCompile(`^test at (.+):(\d+):\d+$`)
	landSpecElapsed = regexp.MustCompile(`\s+\(\d+(?:\.\d+)?ms\)$`)
)

// landFailedTests reads TAP "not ok" lines and the node spec reporter's
// "✖ failing tests:" summary. Spec names repeat in the running output, so only
// unindented "✖" lines after the summary header count. At most 10, no repeats.
func landFailedTests(output string) []string {
	failed, seen := []string{}, map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] && len(failed) < 10 {
			failed = append(failed, name)
			seen[name] = true
		}
	}
	summary, where := false, ""
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
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
	return failed
}

// landSuiteLog writes the whole suite output to
// <home>/.code-forge/runs/<run>/land-logs/<id>-<UTC time>.log and returns its
// path, or why it could not be saved.
func landSuiteLog(home, run, id, output string) string {
	if !reviewBlockRun.MatchString(run) {
		return "not saved (bad run id)"
	}
	dir := filepath.Join(home, ".code-forge", "runs", run, "land-logs")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "not saved (" + err.Error() + ")"
	}
	name := strings.NewReplacer("/", "-", " ", "-").Replace(id) + "-" + time.Now().UTC().Format("20060102T150405.000000000Z") + ".log"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(output), 0600); err != nil {
		return "not saved (" + err.Error() + ")"
	}
	return path
}

func landFirst(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// Match the router's captureStderr: the final nonempty write is the log message.
type landErrorStream struct{ last string }

func (s *landErrorStream) Write(data []byte) (int, error) {
	if text := strings.TrimSpace(string(data)); text != "" {
		s.last = text
	}
	return os.Stderr.Write(data)
}

// Fingerprints use the router's message normalization, including numeric values
// in a captured gate refusal. Keep runtime durations out of long-wait digests.
func landFingerprintMessage(text string) string {
	alnum := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' }
	var quoted strings.Builder
	for i := 0; i < len(text); {
		quote := text[i]
		if quote == '"' || quote == '\x60' || quote == '\x27' && (i == 0 || !alnum(text[i-1])) {
			end := strings.IndexByte(text[i+1:], quote)
			if end >= 0 && !strings.ContainsRune(text[i+1:i+1+end], '\n') {
				quoted.WriteByte(' ')
				i += end + 2
				continue
			}
		}
		quoted.WriteByte(text[i])
		i++
	}
	text = reviewWaitLogPlaceholders.ReplaceAllString(quoted.String(), " ")
	text = reviewWaitLogPaths.ReplaceAllString(text, " ")
	text = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}(?:[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?)?\b`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`\b\d{1,2}:\d{2}(?::\d{2}(?:\.\d+)?)?\b`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`(?i)\b[0-9a-f]{7,}\b`).ReplaceAllStringFunc(text, func(value string) string {
		if strings.ContainsAny(value, "0123456789") {
			return " "
		}
		return value
	})
	matches := regexp.MustCompile(`\d{4,}(?:\.\d+)?`).FindAllStringIndex(text, -1)
	for i := len(matches) - 1; i >= 0; i-- {
		a, b := matches[i][0], matches[i][1]
		if a > 0 && (alnum(text[a-1]) || text[a-1] == '_' || text[a-1] == '.') {
			continue
		}
		if b < len(text) && (alnum(text[b]) || text[b] == '_') {
			continue
		}
		text = text[:a] + " " + text[b:]
	}
	return strings.TrimSpace(reviewWaitLogWords.ReplaceAllString(text, " "))
}
