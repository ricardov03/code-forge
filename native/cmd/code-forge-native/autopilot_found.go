package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B800: self-improvement. A problem found while a run works (a red gate, a
// failed land, a coder's leftover, a flaky test, a hand-over hit, a review
// finding outside the block) becomes a block of the SAME run:
//
//	code-forge autopilot found --run <id> --source <src> --title <text> --evidence <text>|--evidence-file <path> [--label <l,…>] [--from <block>] [--fingerprint <key>] [--json]
//	code-forge autopilot found --run <id> --report <coder report> [--from <block>] [--json]
//	code-forge autopilot found --run <id> --list [--json]
//
// Each new finding gets an id (F1, F2, …) and a fingerprint; a finding whose
// fingerprint the run already holds counts as seen again and adds nothing.
// A finding that names product scope, security or secrets, a delete or a
// publish waits for the owner and is never queued. Up to
// autopilot.max_found_blocks (default 10) findings per run are queued:
// each takes the next free block id, gets a brief and an acceptance file
// (a red test from the evidence) under the run folder, and joins the run's
// planned blocks (autopilot.Planned), so an until-done grant runs until it
// is closed. Past the cap a finding waits for the owner. Every finding
// writes one signed autopilot.found row. The binnacle lists found → block →
// state, and the waiting ones under "Actions only you can take".
//
// B921 (scope fixed): a finding joins the current run only when its
// severity is customer-breaking (--severity customer-breaking, or the label
// customer-breaking on a FOUND line). Any other finding goes to the backlog
// (state backlog, the next release): no block id, no brief, no plan entry,
// and no owner action. Owner scope still wins over severity.
//
// B984: a new backlog finding is also one line of the workspace's
// next-release plan file (.code-forge/next-release.md), so the next
// release's planning sees it after the run record is gone. A fingerprint
// the file already holds (seen again, or found by an earlier run) adds no
// line; queued and owner findings never go there.

var apfSources = []string{"gate", "land", "coder", "flaky", "handover", "review"}

const (
	apfDefaultCap   = 10
	apfMaxTitle     = 200
	apfMaxEvidence  = 4000
	apfStateQueued  = "queued"
	apfStateOwner   = "waits_for_owner"
	apfStateBacklog = "backlog"
	apfSevBreaking  = "customer-breaking"
	apfSevNormal    = "normal"
	apfAcceptanceID = "red-test-from-the-evidence"
)

// apfOwnerLabels: a label that keeps a finding for the owner, and its kind.
var apfOwnerLabels = map[string]string{
	"scope": "scope", "product": "scope",
	"security": "security", "secret": "security", "secrets": "security",
	"delete": "delete", "destructive": "delete",
	"publish": "publish", "release": "publish",
}

// apfSecurityWords are read in the title and the evidence; the other kinds
// in the title only (evidence text names deleted files and releases often).
var (
	apfSecurityWords = regexp.MustCompile(`(?i)\b(secrets?|credentials?|passwords?|private key|api key|keychain|1password|signer key)\b`)
	apfTitleWords    = []struct {
		re   *regexp.Regexp
		kind string
	}{
		{regexp.MustCompile(`(?i)\b(npm publish|publish(es|ed|ing)?|git tag)\b`), "publish"},
		{regexp.MustCompile(`(?i)\b(delete[sd]?|deleting|rm -rf|git rm|drop table|force[- ]push)\b`), "delete"},
		{regexp.MustCompile(`(?i)\b(product scope|scope change|new feature)\b`), "scope"},
	}
	apfIDPattern = regexp.MustCompile(`^([A-Za-z]+)([0-9]{1,9})[A-Za-z]?$`)
	apfHexRun    = regexp.MustCompile(`\b[0-9a-f]{7,}\b`)
	apfDigitRun  = regexp.MustCompile(`[0-9]+`)
	apfLabelWord = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
)

// apfInput is one finding as given.
type apfInput struct {
	source, title, evidence, from, fingerprint, severity string
	labels                                               []string
}

// apfResult is what one finding became.
type apfResult struct {
	entry     ledger.Object
	duplicate bool
	plan      string // B984: the next-release plan file that holds this backlog finding
}

func apfSub(sub string) bool { return sub == "found" }

func apfRun(o *runOut, home string, args []string) int {
	flags, err := autopilotParse("found", args)
	if err != nil {
		return autopilotReport(o, "found", err)
	}
	runID := flags.values["run"]
	if flags.bools["list"] {
		for _, k := range []string{"source", "title", "evidence", "evidence-file", "label", "from", "report", "fingerprint", "severity"} {
			if _, has := flags.values[k]; has {
				return autopilotReport(o, "found", autopilotUsageFail("--list takes only --run and --json"))
			}
		}
		return apfList(o, home, runID, flags.bools["json"])
	}
	inputs, warns, err := apfInputs(flags)
	if err != nil {
		return autopilotReport(o, "found", err)
	}
	for _, w := range warns {
		o.err(w + "\n")
	}
	if len(inputs) == 0 {
		o.out("run " + runID + ": no FOUND line in " + flags.values["report"] + "; nothing was recorded\n")
		return 0
	}
	now := autopilotNow().Truncate(time.Millisecond)
	results, err := apfRecord(home, runID, inputs, now)
	if err != nil {
		return autopilotReport(o, "found", err)
	}
	if warn := apfNextRelease(home, runID, results, now); warn != "" {
		o.err(warn + "\n")
	}
	for _, r := range results {
		if flags.bools["json"] {
			out := apfJSON(r.entry, nil, r.duplicate)
			if r.plan != "" {
				out = append(out, ledger.Field{Key: "next_release", Value: r.plan})
			}
			data, _ := ledger.Stringify(out, false)
			o.out(string(data) + "\n")
			continue
		}
		o.out(apfText(runID, r))
	}
	return 0
}

// apfInputs reads one finding from the flags, or every FOUND line of a report.
func apfInputs(flags autopilotFlags) ([]apfInput, []string, error) {
	from := flags.values["from"]
	if file, has := flags.values["report"]; has {
		for _, k := range []string{"source", "title", "evidence", "evidence-file", "label", "fingerprint", "severity"} {
			if _, set := flags.values[k]; set {
				return nil, nil, autopilotUsageFail("--report reads its findings from the report; drop --" + k)
			}
		}
		data, err := os.ReadFile(file)
		if err != nil || !utf8.Valid(data) {
			return nil, nil, &runFail{"--report " + file + " cannot be read", false}
		}
		inputs, warns := apfScan(string(data), from)
		return inputs, warns, nil
	}
	source, hasSource := flags.values["source"]
	if !hasSource {
		return nil, nil, autopilotUsageFail("--source is required: one of " + strings.Join(apfSources, ", ") + " (or --report <file>, or --list)")
	}
	if !slices.Contains(apfSources, source) {
		return nil, nil, autopilotUsageFail("--source must be one of " + strings.Join(apfSources, ", ") + ", not " + autopilotQuote(source))
	}
	title := strings.TrimSpace(flags.values["title"])
	if title == "" {
		return nil, nil, autopilotUsageFail("--title is required: the proposed block title")
	}
	evidence, hasText := flags.values["evidence"]
	if file, hasFile := flags.values["evidence-file"]; hasFile {
		if hasText {
			return nil, nil, autopilotUsageFail("give --evidence or --evidence-file, not both")
		}
		data, err := os.ReadFile(file)
		if err != nil || !utf8.Valid(data) {
			return nil, nil, &runFail{"--evidence-file " + file + " cannot be read", false}
		}
		evidence = string(data)
	} else if !hasText {
		return nil, nil, autopilotUsageFail("--evidence <text> or --evidence-file <path> is required: what shows the problem (an error line, a log path, a failing test)")
	}
	if strings.TrimSpace(evidence) == "" {
		return nil, nil, autopilotUsageFail("the evidence is empty")
	}
	severity := apfSevNormal
	if raw, has := flags.values["severity"]; has {
		if raw != apfSevNormal && raw != apfSevBreaking {
			return nil, nil, autopilotUsageFail("--severity must be normal or customer-breaking, not " + autopilotQuote(raw))
		}
		severity = raw
	}
	labels := []string{}
	if raw, has := flags.values["label"]; has {
		for _, l := range autopilotItems(raw) {
			l = strings.ToLower(l)
			if !apfLabelWord.MatchString(l) {
				return nil, nil, autopilotUsageFail("--label " + autopilotQuote(l) + " is not a label word (a-z, 0-9, _ and -)")
			}
			if l == apfSevBreaking {
				severity = apfSevBreaking
				continue
			}
			if !slices.Contains(labels, l) {
				labels = append(labels, l)
			}
		}
	}
	return []apfInput{{source: source, title: title, evidence: evidence, from: from, labels: labels, fingerprint: flags.values["fingerprint"], severity: severity}}, nil, nil
}

// apfScan reads the FOUND lines of a coder report:
//
//	FOUND [<source>] [<label>,…] <title> :: <evidence>
//
// A list marker and backticks around the line are allowed; the source
// defaults to coder. A FOUND line without " :: " is skipped with a WARN.
func apfScan(text, from string) ([]apfInput, []string) {
	inputs, warns := []apfInput{}, []string{}
	for n, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		for _, marker := range []string{"- ", "* "} {
			line = strings.TrimPrefix(line, marker)
		}
		line = strings.TrimSpace(strings.Trim(line, "`"))
		rest, ok := strings.CutPrefix(line, "FOUND ")
		if !ok {
			continue
		}
		head, evidence, ok := strings.Cut(rest, " :: ")
		head, evidence = strings.TrimSpace(head), strings.TrimSpace(evidence)
		if !ok || head == "" || evidence == "" {
			warns = append(warns, "WARN found: report line "+strconv.Itoa(n+1)+" skipped: write it as FOUND [<source>] [<label>,…] <title> :: <evidence>")
			continue
		}
		in := apfInput{source: "coder", from: from, labels: []string{}, severity: apfSevNormal}
		if word, after, _ := strings.Cut(head, " "); slices.Contains(apfSources, word) && strings.TrimSpace(after) != "" {
			in.source, head = word, strings.TrimSpace(after)
		}
		if strings.HasPrefix(head, "[") {
			if inside, after, ok := strings.Cut(head[1:], "]"); ok && strings.TrimSpace(after) != "" {
				for _, l := range autopilotItems(inside) {
					if l = strings.ToLower(l); l == apfSevBreaking {
						in.severity = apfSevBreaking
					} else if apfLabelWord.MatchString(l) && !slices.Contains(in.labels, l) {
						in.labels = append(in.labels, l)
					}
				}
				head = strings.TrimSpace(after)
			}
		}
		in.title, in.evidence = head, evidence
		inputs = append(inputs, in)
	}
	return inputs, warns
}

// apfClean masks secret tokens, folds a title to one line and cuts it.
func apfClean(text string, limit int, oneLine bool) (string, bool) {
	masked := config.MaskSecrets(text)
	hit := masked != text
	if oneLine {
		masked = strings.Join(strings.Fields(masked), " ")
	}
	if len(masked) > limit {
		cut := limit
		for cut > 0 && !utf8.RuneStart(masked[cut]) {
			cut--
		}
		masked = masked[:cut] + " [cut]"
	}
	return masked, hit
}

// apfNormal is the text the fingerprint hashes: lower case, hashes and
// numbers (times, line numbers, durations, shas) folded, spaces collapsed.
func apfNormal(text string) string {
	text = strings.ToLower(text)
	text = apfHexRun.ReplaceAllString(text, "#")
	text = apfDigitRun.ReplaceAllString(text, "#")
	return strings.Join(strings.Fields(text), " ")
}

func apfFingerprint(in apfInput, title, evidence string) string {
	key := in.fingerprint
	if strings.TrimSpace(key) == "" {
		key = apfNormal(title) + "\n" + apfNormal(evidence)
	} else {
		key = "key:" + strings.TrimSpace(key)
	}
	sum := sha256.Sum256([]byte(key))
	return "fp-" + hex.EncodeToString(sum[:8])
}

// apfOwnerReason is why a finding waits for the owner ("" when the autopilot
// may queue it): an owner label, a secret in the text, or a title that names
// a publish, a delete or a scope change.
func apfOwnerReason(labels []string, title, evidence string, secretHit bool) string {
	for _, l := range labels {
		if kind, ok := apfOwnerLabels[l]; ok {
			return kind + ": label " + l
		}
	}
	if secretHit {
		return "security: the text held a secret token (masked)"
	}
	if m := apfSecurityWords.FindString(title + "\n" + evidence); m != "" {
		return "security: the text names " + autopilotQuote(strings.ToLower(m))
	}
	for _, w := range apfTitleWords {
		if m := w.re.FindString(title); m != "" {
			return w.kind + ": the title names " + autopilotQuote(strings.ToLower(m))
		}
	}
	return ""
}

// apfCap is autopilot.max_found_blocks from the run's config snapshot.
func apfCap(record ledger.Object) int {
	cfg, has, valid := sweepConfigInForce(record)
	if !has || !valid {
		return apfDefaultCap
	}
	section, _ := cfg.Get("autopilot").(ledger.Object)
	if v, ok := section.Get("max_found_blocks").(float64); ok && v >= 0 && v <= 100 && v == float64(int(v)) {
		return int(v)
	}
	return apfDefaultCap
}

// apfNextID is the next free block id of the run: one past the highest
// number among its blocks, its planned blocks and its queued found blocks,
// with that id's prefix (B1 for a run with none).
func apfNextID(record ledger.Object, found []any) string {
	prefix, best := "B", 0
	consider := func(id string) {
		if m := apfIDPattern.FindStringSubmatch(id); m != nil {
			if n, err := strconv.Atoi(m[2]); err == nil && n >= best {
				prefix, best = m[1], n
			}
		}
	}
	if blocks, ok := record.Get("blocks").(ledger.Object); ok {
		for _, f := range blocks {
			consider(f.Key)
		}
	}
	if grant, ok := record.Get("autopilot").(ledger.Object); ok {
		ids, _ := grant.Get("planned_blocks").([]any)
		for _, id := range ids {
			s, _ := id.(string)
			consider(s)
		}
	}
	for _, item := range found {
		if entry, ok := item.(ledger.Object); ok {
			s, _ := entry.Get("block").(string)
			consider(s)
		}
	}
	return prefix + strconv.Itoa(best+1)
}

func apfFoundDir(home, runID string) string {
	return filepath.Join(home, ".code-forge", "runs", runID, "found")
}

// apfWriteBrief writes the block's brief and its acceptance file.
func apfWriteBrief(home, runID string, entry ledger.Object) (string, string, error) {
	dir := apfFoundDir(home, runID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", "", err
	}
	block := autopilotText(entry.Get("block"))
	finding := autopilotText(entry.Get("finding"))
	title := autopilotText(entry.Get("title"))
	from := "-"
	if s, ok := entry.Get("from_block").(string); ok && s != "" {
		from = s
	}
	brief := "# " + block + " — " + title + "\n\n" +
		"Found by autopilot in run " + runID + " (source " + autopilotText(entry.Get("source")) + ", from block " + from + ", finding " + finding + ", " + autopilotText(entry.Get("fingerprint")) + ").\n\n" +
		"## Evidence\n\n```text\n" + strings.ReplaceAll(autopilotText(entry.Get("evidence")), "```", "'''") + "\n```\n\n" +
		"## Task\n\nFix the cause this evidence shows. First write a test that reproduces the evidence and fails on the current code (red), then fix the code so it passes (green). Keep the change to this cause; report anything else you find as a `FOUND <title> :: <evidence>` line.\n\n" +
		"## Acceptance\n\nA test reproduces the evidence: red before the fix, green after. Name that test in the acceptance file in place of `" + apfAcceptanceID + "` before `block open`.\n"
	clause, _ := ledger.Stringify("a test reproduces the evidence of "+finding+" ("+title+"): red before the fix, green after", false)
	acceptance := "- clause: " + string(clause) + "\n  tests: [" + apfAcceptanceID + "]\n"
	briefPath := filepath.Join(dir, block+".brief.md")
	acceptPath := filepath.Join(dir, block+".acceptance.yml")
	if err := os.WriteFile(briefPath, []byte(brief), 0600); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(acceptPath, []byte(acceptance), 0600); err != nil {
		return "", "", err
	}
	return briefPath, acceptPath, nil
}

// apfRecord records the findings under the run lock: dedup, owner scope,
// cap, the next free id, the brief, one signed row each.
func apfRecord(home, runID string, inputs []apfInput, now time.Time) ([]apfResult, error) {
	results := []apfResult{}
	err := state.UpdateRun(home, runID, func(record *ledger.Object) error {
		results = results[:0]
		if record.Get("status") != "active" {
			return &runFail{"run " + runID + " has ended; nothing was recorded", false}
		}
		found, _ := record.Get("found").([]any)
		found = slices.Clone(found)
		limit := apfCap(*record)
		queued := 0
		for _, item := range found {
			if e, ok := item.(ledger.Object); ok && e.Get("state") == apfStateQueued {
				queued++
			}
		}
		rows := []ledger.Object{}
		for _, in := range inputs {
			title, titleHit := apfClean(in.title, apfMaxTitle, true)
			evidence, evidenceHit := apfClean(in.evidence, apfMaxEvidence, false)
			fp := apfFingerprint(in, title, evidence)
			var dup ledger.Object
			for _, item := range found {
				if e, ok := item.(ledger.Object); ok && e.Get("fingerprint") == fp {
					dup = e
				}
			}
			if dup != nil {
				seen, _ := dup.Get("seen").(float64)
				dup.Set("seen", seen+1)
				dup.Set("last_seen_at", autopilotStamp(now))
				results = append(results, apfResult{entry: dup, duplicate: true})
				rows = append(rows, apfRow(dup, true, now))
				continue
			}
			labels := []any{}
			for _, l := range in.labels {
				labels = append(labels, l)
			}
			entry := ledger.Object{
				{Key: "finding", Value: "F" + strconv.Itoa(len(found)+1)},
				{Key: "fingerprint", Value: fp},
				{Key: "source", Value: in.source},
				{Key: "title", Value: title},
				{Key: "evidence", Value: evidence},
				{Key: "labels", Value: labels},
				{Key: "severity", Value: in.severity},
				{Key: "from_block", Value: nil},
				{Key: "block", Value: nil},
				{Key: "state", Value: apfStateOwner},
				{Key: "reason", Value: nil},
				{Key: "brief", Value: nil},
				{Key: "acceptance", Value: nil},
				{Key: "seen", Value: float64(1)},
				{Key: "found_at", Value: autopilotStamp(now)},
			}
			if in.from != "" {
				entry.Set("from_block", in.from)
			}
			reason := apfOwnerReason(in.labels, title, evidence, titleHit || evidenceHit)
			backlog := reason == "" && in.severity != apfSevBreaking
			if reason == "" && !backlog && queued >= limit {
				reason = "cap: " + strconv.Itoa(limit) + " found block(s) already queued in this run (autopilot.max_found_blocks)"
			}
			switch {
			case backlog:
				entry.Set("state", apfStateBacklog)
				entry.Set("reason", "backlog: not customer-breaking, so it goes to the next release (B921)")
			case reason != "":
				entry.Set("reason", reason)
			default:
				entry.Set("block", apfNextID(*record, found))
				entry.Set("state", apfStateQueued)
				brief, acceptance, err := apfWriteBrief(home, runID, entry)
				if err != nil {
					return err
				}
				entry.Set("brief", brief)
				entry.Set("acceptance", acceptance)
				queued++
			}
			found = append(found, entry)
			results = append(results, apfResult{entry: entry})
			rows = append(rows, apfRow(entry, false, now))
		}
		// the rows first: a queued block without its signed row would act unrecorded
		for _, row := range rows {
			if _, err := autopilotWriteSigned(home, runID, row); err != nil {
				return err
			}
		}
		record.Set("found", found)
		return nil
	})
	return results, err
}

func apfRow(entry ledger.Object, duplicate bool, now time.Time) ledger.Object {
	row := ledger.Object{{Key: "event", Value: "autopilot.found"}}
	for _, k := range []string{"finding", "fingerprint", "source", "title", "labels", "severity", "from_block", "block", "state", "reason", "seen"} {
		row = append(row, ledger.Field{Key: k, Value: entry.Get(k)})
	}
	return append(row, ledger.Field{Key: "duplicate", Value: duplicate}, ledger.Field{Key: "ts", Value: autopilotStamp(now)})
}

// apfBlockState is the found block's state: its run status, "not opened"
// before block open, "waits for the owner" when it has no block.
func apfBlockState(record, entry ledger.Object) string {
	id, ok := entry.Get("block").(string)
	if entry.Get("state") == apfStateBacklog && (!ok || id == "") {
		return "backlog (next release)"
	}
	if !ok || id == "" {
		return "waits for the owner"
	}
	blocks, _ := record.Get("blocks").(ledger.Object)
	b, ok := blocks.Get(id).(ledger.Object)
	if !ok {
		return "not opened"
	}
	if s, ok := b.Get("status").(string); ok {
		return s
	}
	return "open"
}

func apfJSON(entry, record ledger.Object, duplicate bool) ledger.Object {
	out := ledger.Object{}
	for _, k := range []string{"finding", "fingerprint", "source", "title", "labels", "severity", "from_block", "block", "state", "reason", "seen", "brief", "acceptance"} {
		out = append(out, ledger.Field{Key: k, Value: entry.Get(k)})
	}
	if record != nil {
		out = append(out, ledger.Field{Key: "block_state", Value: apfBlockState(record, entry)})
	} else {
		out = append(out, ledger.Field{Key: "duplicate", Value: duplicate})
	}
	return out
}

func apfText(runID string, r apfResult) string {
	e := r.entry
	finding, source, title := autopilotText(e.Get("finding")), autopilotText(e.Get("source")), autopilotText(e.Get("title"))
	block, _ := e.Get("block").(string)
	if r.duplicate {
		seen, _ := e.Get("seen").(float64)
		target := "waits for the owner"
		if e.Get("state") == apfStateBacklog {
			target = "backlog"
		}
		if block != "" {
			target = block
		}
		return "found " + finding + " again (seen " + strconv.Itoa(int(seen)) + " times) → " + target + ": " + title + "; nothing new was queued\n"
	}
	if block == "" && e.Get("state") == apfStateBacklog {
		text := "found " + finding + " " + source + " → backlog (next release; not customer-breaking, B921): " + title + "\n" +
			"  only a customer-breaking finding joins this run: --severity customer-breaking\n"
		if r.plan != "" {
			text += "  plan       " + r.plan + "\n"
		}
		return text
	}
	if block == "" {
		return "found " + finding + " " + source + " → waits for the owner (" + autopilotText(e.Get("reason")) + "): " + title + "\n"
	}
	return "found " + finding + " " + source + " → " + block + " queued: " + title + "\n" +
		"  brief      " + autopilotText(e.Get("brief")) + "\n" +
		"  acceptance " + autopilotText(e.Get("acceptance")) + "\n" +
		"  open it:   code-forge block open " + block + " --run " + runID + " --brief " + autopilotText(e.Get("brief")) + " --acceptance " + autopilotText(e.Get("acceptance")) + " --owned <paths…>\n"
}

// apfList prints found → block → state for the run.
func apfList(o *runOut, home, runID string, asJSON bool) int {
	record, err := state.ReadRun(home, runID)
	if err != nil {
		return autopilotReport(o, "found", err)
	}
	list, _ := record.Get("found").([]any)
	if asJSON {
		items := []any{}
		for _, item := range list {
			if e, ok := item.(ledger.Object); ok {
				items = append(items, apfJSON(e, record, false))
			}
		}
		data, _ := ledger.Stringify(ledger.Object{{Key: "run", Value: runID}, {Key: "found", Value: items}}, false)
		o.out(string(data) + "\n")
		return 0
	}
	if len(list) == 0 {
		o.out("run " + runID + ": nothing found yet\n")
		return 0
	}
	var b strings.Builder
	b.WriteString("run " + runID + ": " + strconv.Itoa(len(list)) + " found (found → block → state)\n")
	for _, item := range list {
		e, ok := item.(ledger.Object)
		if !ok {
			continue
		}
		block := "-"
		if s, ok := e.Get("block").(string); ok && s != "" {
			block = s
		}
		line := autopilotText(e.Get("finding")) + " " + autopilotText(e.Get("source")) + " → " + block + " → " + apfBlockState(record, e)
		if reason, ok := e.Get("reason").(string); ok && reason != "" {
			line += " (" + reason + ")"
		}
		b.WriteString(line + ": " + autopilotText(e.Get("title")) + "\n")
	}
	o.out(b.String())
	return 0
}

// apfBinnacle is the binnacle's found rows and the owner actions of the
// findings that wait for the owner (nil, nil for a run with none).
func apfBinnacle(runID string, record ledger.Object) ([]any, []string) {
	list, _ := record.Get("found").([]any)
	rows, actions := []any{}, []string{}
	for _, item := range list {
		e, ok := item.(ledger.Object)
		if !ok {
			continue
		}
		block := "-"
		if s, ok := e.Get("block").(string); ok && s != "" {
			block = s
		}
		rows = append(rows, ledger.Object{
			{Key: "found", Value: e.Get("finding")}, {Key: "source", Value: e.Get("source")}, {Key: "title", Value: e.Get("title")},
			{Key: "block", Value: block}, {Key: "state", Value: apfBlockState(record, e)},
		})
		if block == "-" && e.Get("state") != apfStateBacklog {
			actions = append(actions, "Decide on found "+autopilotText(e.Get("finding"))+" ("+autopilotText(e.Get("reason"))+"): "+autopilotText(e.Get("title"))+"; see code-forge autopilot found --run "+runID+" --list")
		}
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows, actions
}

// apfNextReleaseName is the next-release plan file, under the workspace (B984).
var apfNextReleaseName = filepath.Join(".code-forge", "next-release.md")

const apfNextReleaseHead = "# Next release (code-forge backlog)\n\n" +
	"Findings `code-forge autopilot found` put in the backlog: not customer-breaking, so they wait for the next release (B921, B984).\n" +
	"Plan each one as a block of the next release; tick or remove its line once it has one.\n\n"

// apfNextRelease appends each new backlog finding of results to the run
// workspace's next-release plan file and sets its plan path. It returns a
// warning when the file cannot be written: the run record already holds the
// finding, so the command still succeeds.
func apfNextRelease(home, runID string, results []apfResult, now time.Time) string {
	fresh := []int{}
	for i, r := range results {
		if !r.duplicate && r.entry.Get("state") == apfStateBacklog {
			fresh = append(fresh, i)
		}
	}
	if len(fresh) == 0 {
		return ""
	}
	record, err := state.ReadRun(home, runID)
	if err != nil {
		return "next-release plan not written: " + err.Error()
	}
	workspace, _ := record.Get("workspace").(string)
	if workspace == "" {
		return "next-release plan not written: run " + runID + " has no workspace"
	}
	path := filepath.Join(workspace, apfNextReleaseName)
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return "next-release plan " + path + " cannot be read: " + err.Error()
	}
	text := string(old)
	add := ""
	for _, i := range fresh {
		e := results[i].entry
		fp := autopilotText(e.Get("fingerprint"))
		marker := "<!-- cf-found " + fp + " -->"
		if !strings.Contains(text, marker) && !strings.Contains(add, marker) {
			add += apfNextReleaseLine(runID, e, now, marker)
		}
		results[i].plan = path
	}
	if add == "" {
		return ""
	}
	if text == "" {
		add = apfNextReleaseHead + add
	} else if !strings.HasSuffix(text, "\n") {
		add = "\n" + add
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		apfClearPlan(results, fresh)
		return "next-release plan " + path + " cannot be written: " + err.Error()
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err == nil {
		_, err = f.WriteString(add)
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}
	if err != nil {
		apfClearPlan(results, fresh)
		return "next-release plan " + path + " cannot be written: " + err.Error()
	}
	return ""
}

func apfClearPlan(results []apfResult, idx []int) {
	for _, i := range idx {
		results[i].plan = ""
	}
}

// apfNextReleaseLine is one plan line: finding, run, source, day, title and
// the first evidence line (one line, at most 200 bytes), then the fingerprint
// marker that keeps the line from being written twice.
func apfNextReleaseLine(runID string, e ledger.Object, now time.Time, marker string) string {
	evidence := autopilotText(e.Get("evidence"))
	if cut := strings.IndexByte(evidence, '\n'); cut >= 0 {
		evidence = evidence[:cut]
	}
	evidence, _ = apfClean(strings.ReplaceAll(evidence, "-->", "->"), 200, true)
	title := strings.ReplaceAll(autopilotText(e.Get("title")), "-->", "->")
	line := "- [ ] " + autopilotText(e.Get("finding")) + " · run " + runID + " · " + autopilotText(e.Get("source")) + " · " + now.UTC().Format("2006-01-02") + ": " + title
	if evidence != "" {
		line += " — " + evidence
	}
	return line + " " + marker + "\n"
}
