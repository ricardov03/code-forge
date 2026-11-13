package worker

// B609: the answers ticket (queue.mjs readTicket's answers branch and
// review/answers.mjs reviewAnswers through engine.mjs reviewTicket). ansDry
// is the dry pass: every refusal, the ledger read, the open-finding check
// and the whole packet text, before the worker writes anything; ansRun
// writes the prompt, runs the one answers session (with its timeout retry)
// and writes the signed review.answered rows. A case Go cannot reproduce
// byte for byte is ErrUnsupported with nothing written.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/review"
	"github.com/ricardov03/code-forge/native/session"
	"github.com/ricardov03/code-forge/native/ticket"
)

// ansLens is answers.mjs ANSWERS_LENS.
const ansLens = "You are a code reviewer for code-forge, judging whether a change in one file resolves findings a review raised about another file. " +
	"You have no tools and no files; everything you may use is in the message. Answer once, with a JSON object that matches the schema."

// ansSchemaJSON is JSON.stringify(ANSWERS_SCHEMA).
const ansSchemaJSON = `{"type":"object","properties":{"verdicts":{"type":"array","items":{"type":"object","properties":{"id":{"type":"string"},"verdict":{"type":"string","enum":["resolved","not_resolved"]},"reason":{"type":"string"}},"required":["id","verdict","reason"],"additionalProperties":false}}},"required":["verdicts"],"additionalProperties":false}`

const (
	ansAround    = 5
	ansReasonMax = 500
	ansTruncated = "[... truncated at the packet budget]"
	ansShort     = "[cut]"
	ansMaxIDs    = 10
)

// ansReadTicket is readTicket's answers branch (kind already "answers"):
// a refusal outcome, or the ticket.
func ansReadTicket(id string, t ledger.Object, p ticket.Parts) (ledger.Object, ledger.Object) {
	byFile, ok := t.Get("by_file").(string)
	if !ok || rowPathError(byFile) {
		return refusal("refused", "bad-path"), nil
	}
	values, ok := t.Get("answers").([]any)
	if !ok || len(values) == 0 {
		return refusal("refused", "bad-ticket"), nil
	}
	given := []string{}
	for _, v := range values {
		s, ok := v.(string)
		if !ok || !findingID.MatchString(s) {
			return refusal("refused", "bad-ticket"), nil
		}
		given = append(given, s)
	}
	ids := ansUnique(given)
	if len(ids) > ansMaxIDs {
		return refusal("refused", "bad-ticket"), nil
	}
	byHash, ok := t.Get("by_hash").(string)
	if !ok || strings.Join(ids, ",") != strings.Join(given, ",") || queue.AnswersID(p, byFile, byHash, given) != id {
		return refusal("refused", "bad-ticket"), nil
	}
	return nil, t
}

// ansUnique is [...new Set(ids)].sort().
func ansUnique(ids []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range ids {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// ansPlan is an answers ticket ready for its session.
type ansPlan struct {
	id, block, file, hash string
	byFile, byHash        string
	ids                   []string
	severity              map[string]any
	gate                  gatecheck.Input
	packet                string
	workDir               string
	cfg                   map[string]any
	sopts                 session.Opts
	sdeps                 session.Deps
}

// ansOutcome is answers.mjs outcome(status, reason, sessions).
func ansOutcome(status, reason string, sessions []any) ledger.Object {
	if sessions == nil {
		sessions = []any{}
	}
	return ledger.Object{{Key: "status", Value: status}, {Key: "reason", Value: reason}, {Key: "approved", Value: false},
		{Key: "engine", Value: "adaptive"}, {Key: "kind", Value: "answers"}, {Key: "sessions", Value: sessions}}
}

// ansMapped is reviewWith's mapping of an outcome the dry pass answers.
func (w *TicketRunner) ansMapped(t ledger.Object, o ledger.Object) ledger.Object {
	return w.engineResult(o, func(string, string) ledger.Object {
		return ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: t.Get("block")}, {Key: "file", Value: t.Get("file")}, {Key: "content_hash", Value: t.Get("content_hash")}}
	})
}

// ansDry is reviewAnswers up to the prompt write, as a dry pass: a mapped
// outcome, a plan, or ErrUnsupported.
func (w *TicketRunner) ansDry(t ledger.Object, rec ledger.Object, cfg map[string]any, contentRoot string, timeoutS float64) (ledger.Object, *ansPlan, error) {
	out, plan, err := w.ansDryPass(t, rec, cfg, contentRoot, timeoutS)
	if err != nil {
		if !IsUnsupported(err) {
			err = unsupported("the answers dry pass: " + err.Error())
		}
		return nil, nil, err
	}
	if out != nil {
		return w.ansMapped(t, out), nil, nil
	}
	return nil, plan, nil
}

func (w *TicketRunner) ansDryPass(t ledger.Object, rec ledger.Object, cfg map[string]any, contentRoot string, timeoutS float64) (ledger.Object, *ansPlan, error) {
	p := &ansPlan{cfg: cfg}
	p.id, _ = t.Get("ticket").(string)
	p.block, _ = t.Get("block").(string)
	p.file, _ = t.Get("file").(string)
	p.hash, _ = t.Get("content_hash").(string)
	p.byFile, _ = t.Get("by_file").(string)
	for _, v := range t.Get("answers").([]any) {
		p.ids = append(p.ids, v.(string))
	}
	if len(w.Key) == 0 {
		return nil, nil, unsupported("an answers ticket with no run key")
	}
	budget, _ := obj(cfg["budget"])
	if usd, ok := budget["usd"].(float64); ok && usd > 0 && !math.IsInf(usd, 0) && w.sessionDeps().Warn == nil {
		return nil, nil, unsupported("budget.usd with no error-log warning seam")
	}
	e := blockEntryOf(rec, p.block, p.file)
	// both passed assertRowPath: posix.normalize leaves them as they are
	if p.byFile == p.file {
		return ansOutcome("answers", "answers-same-file", nil), nil, nil
	}
	if !owned.OwnsFile(e.owned, e.ownedKinds, p.file) || !owned.OwnsFile(e.owned, e.ownedKinds, p.byFile) {
		return ansOutcome("answers", "answers-not-owned", nil), nil, nil
	}
	byBytes, err := gitx.ReadRegularFileNoFollow(contentRoot, p.byFile)
	if err != nil || byBytes == nil {
		return ansOutcome("answers", "answers-file-missing", nil), nil, nil
	}
	p.byHash = ansSHA(byBytes)
	if p.byHash != t.Get("by_hash") {
		return ansOutcome("stale", "answering-file-changed", nil), nil, nil
	}
	srcBytes, err := gitx.ReadRegularFileNoFollow(contentRoot, p.file)
	if err != nil || srcBytes == nil || ansSHA(srcBytes) != p.hash {
		return ansOutcome("stale", "source-changed", nil), nil, nil
	}
	objs, err := w.readObjects()
	if err != nil {
		if IsUnsupported(err) {
			return nil, nil, err
		}
		return ansOutcome("unavailable", "ledger-unreadable", nil), nil, nil
	}
	rows := []ledger.Object{}
	p.gate = gatecheck.Input{Run: w.RunID, Block: p.block, Key: w.Key, Rows: []gatecheck.Row{}, Ordered: map[uintptr]ledger.Object{},
		Files: []gatecheck.File{{File: p.file, Hash: p.hash}, {File: p.byFile, Hash: p.byHash}}}
	for _, o := range objs {
		if o.Get("run") != w.RunID {
			continue
		}
		m, err := plainMap(o)
		if err != nil {
			return nil, nil, unsupported("ledger row form")
		}
		rows = append(rows, o)
		p.gate.Rows = append(p.gate.Rows, m)
		p.gate.Ordered[gatecheck.OrderedKey(m)] = o
	}
	open, severity, unknown := gatecheck.AnswerableOpen(p.gate, p.file, p.hash, p.id)
	if unknown {
		return nil, nil, unsupported("an answerable-finding rule Go cannot reproduce")
	}
	p.severity = severity
	isOpen := map[string]bool{}
	for _, id := range open {
		isOpen[id] = true
	}
	for _, id := range p.ids {
		if !isOpen[id] {
			return ansOutcome("answers", "answers-not-open", nil), nil, nil
		}
	}
	p.workDir = filepath.Join(w.RunRootDir, "packets", p.id)
	text, refusal, err := w.ansPacket(p, e, rows, contentRoot, srcBytes, byBytes)
	if err != nil {
		return nil, nil, err
	}
	if refusal != "" {
		return ansOutcome("unavailable", refusal, nil), nil, nil
	}
	p.packet = text
	p.sopts, p.sdeps = w.sessionOpts(cfg, p.block, timeoutS), w.sessionDeps()
	if err := session.Check(ansSessionOpts(p.sopts, review.SpawnOpts{Level: "L2", Role: "reviewer", PromptPath: "/answers.md"})); err != nil {
		return nil, nil, unsupported("the answers session: " + err.Error())
	}
	return nil, p, nil
}

func ansSHA(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ansSessionOpts is the worker's spawn options for the answers session.
func ansSessionOpts(base session.Opts, in review.SpawnOpts) session.Opts {
	o := base
	if in.Cfg != nil {
		o.Cfg = in.Cfg
	}
	schema, _ := ledger.DecodeObject([]byte(ansSchemaJSON))
	lens := ansLens
	o.Level, o.Role, o.PromptPath, o.Schema, o.RowExtra = in.Level, in.Role, in.PromptPath, schema, in.RowExtra
	o.SystemPrompt, o.Validate = &lens, ansValid
	return o
}

// ansValid is matchesSchema(ANSWERS_SCHEMA, value).
func ansValid(value any) bool {
	o, ok := value.(ledger.Object)
	if !ok || len(o) != 1 || o[0].Key != "verdicts" {
		return false
	}
	list, ok := o[0].Value.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		v, ok := item.(ledger.Object)
		if !ok {
			return false
		}
		seen := map[string]bool{}
		for _, f := range v {
			s, isString := f.Value.(string)
			switch f.Key {
			case "id", "reason":
				if !isString {
					return false
				}
			case "verdict":
				if !isString || (s != "resolved" && s != "not_resolved") {
					return false
				}
			default:
				return false
			}
			seen[f.Key] = true
		}
		if !seen["id"] || !seen["verdict"] || !seen["reason"] {
			return false
		}
	}
	return true
}

// ansJSNumber is String(n) for a number Go prints the same way.
func ansJSNumber(n float64) (string, bool) {
	if math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) >= 1e21 || (n != 0 && math.Abs(n) < 1e-6) {
		return "", false
	}
	if n == 0 {
		return "0", true
	}
	return strconv.FormatFloat(n, 'f', -1, 64), true
}

// ansString is String(v) for the values Go renders the same way.
func ansString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case float64:
		return ansJSNumber(x)
	}
	return "", false
}

// ansCapBytes is answers.mjs capBytes.
func ansCapBytes(text string, max int) string {
	if len(text) <= max {
		return text
	}
	tail := "\n" + ansTruncated
	if max < len(tail) {
		if max >= len(ansShort) {
			return ansShort
		}
		return ""
	}
	end := max - len(tail)
	for end > 0 && text[end]&0xc0 == 0x80 {
		end--
	}
	return text[:end] + tail
}

type ansFinding struct {
	head    []string
	text    [3]*string
	label   string
	excerpt *string
}

var ansFields = [3]string{"claim", "evidence", "fix"}

// ansPacket is answers.mjs packetText: the packet, or a refusal reason
// (packet-too-large, packet-unreadable).
func (w *TicketRunner) ansPacket(p *ansPlan, e blockEntry, rows []ledger.Object, root string, srcBytes, byBytes []byte) (string, string, error) {
	redact := func(s string) string { return gates.Mask(s, w.secrets()) }
	if err := review.AssertPacketPath(root, p.file); err != nil || review.IsSecretLike(p.file) {
		return "", "packet-unreadable", nil
	}
	details, err := w.ansDetails(p, rows, root)
	if err != nil {
		return "", "", err
	}
	if !utf8.Valid(srcBytes) || !utf8.Valid(byBytes) {
		return "", "", unsupported("an answers file that is not UTF-8")
	}
	lines := strings.Split(string(srcBytes), "\n")
	// the diff's temporary files go in the packet directory; a directory
	// this pass made is removed again (the run phase makes it for the prompt)
	if _, err := os.Stat(p.workDir); os.IsNotExist(err) {
		defer os.Remove(p.workDir)
	}
	kind, change, err := review.AnswersChange(root, p.byFile, e.base, string(byBytes), p.workDir, nil)
	if err != nil {
		if IsUnsupported(err) || errors.Is(err, review.ErrUnsupported) {
			return "", "", unsupported(err.Error())
		}
		return "", "packet-unreadable", nil
	}
	limit := review.BudgetFor(p.cfg, "full_in") * 4
	findings := []ansFinding{}
	for _, id := range p.ids {
		f := details[id]
		var start, end *float64
		if n, ok := f.Get("line_start").(float64); ok && n == math.Trunc(n) && !math.IsInf(n, 0) {
			start = &n
		}
		if n, ok := f.Get("line_end").(float64); ok && n == math.Trunc(n) && !math.IsInf(n, 0) {
			end = &n
		} else {
			end = start
		}
		linesText := "unknown"
		if start != nil {
			a, ok1 := ansJSNumber(*start)
			b, ok2 := ansJSNumber(*end)
			if !ok1 || !ok2 {
				return "", "", unsupported("a finding line Go prints unlike Node")
			}
			linesText = a + "-" + b
		}
		var sev any
		if f != nil && f.Get("severity") != nil {
			sev = f.Get("severity")
		} else if s := p.severity[id]; s != nil {
			sev = s
		} else {
			sev = "unknown"
		}
		sevText, ok := ansString(sev)
		if !ok {
			return "", "", unsupported("a finding severity Go prints unlike Node")
		}
		af := ansFinding{head: []string{redact("## Finding " + id), "", redact("- file: " + p.file), redact("- lines: " + linesText), redact("- severity: " + sevText)}}
		for i, k := range ansFields {
			if s, ok := f.Get(k).(string); ok {
				r := redact(s)
				af.text[i] = &r
			}
		}
		if start != nil && end != nil && *start <= *end {
			from := math.Max(1, *start-ansAround)
			to := math.Min(float64(len(lines)), *end+ansAround)
			if from <= to {
				parts := []string{}
				for i := int(from); i <= int(to); i++ {
					parts = append(parts, strconv.Itoa(i)+": "+lines[i-1])
				}
				af.label = redact("Source lines " + strconv.Itoa(int(from)) + "-" + strconv.Itoa(int(to)) + ":")
				x := redact(strings.Join(parts, "\n"))
				af.excerpt = &x
			}
		}
		findings = append(findings, af)
	}
	intro := []string{
		"# Answered findings",
		"",
		"A review of `" + p.file + "` left the findings below open. The coder says the change in `" + p.byFile + "` resolves them.",
		"For EACH finding id, decide whether that change resolves it: `resolved` only when the change clearly removes the problem the finding names; otherwise `not_resolved`. Give a short reason for each.",
		`Reply with ONE JSON object: {"verdicts": [{"id": string, "verdict": "resolved" | "not_resolved", "reason": string}]}, one entry per id below.`,
		"",
	}
	for i := range intro {
		intro[i] = redact(intro[i])
	}
	note := "(its diff against the block base)"
	if kind == "new" {
		note = "(a new file: its full content)"
	}
	changeHead := []string{redact("## The change in " + p.byFile), "", redact(note), "```"}
	render := func(parts [][4]string, change string) string {
		out := append([]string{}, intro...)
		for i, f := range findings {
			out = append(out, f.head...)
			for j, k := range ansFields {
				v := "(not recorded)"
				if f.text[j] != nil {
					v = parts[i][j]
				}
				out = append(out, "- "+k+": "+v)
			}
			out = append(out, "")
			if f.excerpt != nil {
				out = append(out, f.label, "```", parts[i][3], "```", "")
			}
		}
		out = append(out, changeHead...)
		out = append(out, change, "```", "")
		return strings.Join(out, "\n")
	}
	empty := make([][4]string, len(findings))
	fixed := len(render(empty, ""))
	if fixed > limit {
		return "", "packet-too-large", nil
	}
	share := int(math.Floor(float64(limit-fixed) / 2 / float64(max(1, len(findings)))))
	textCap := int(math.Floor(float64(share) / 5))
	sourceCap := share - len(ansFields)*textCap
	parts := make([][4]string, len(findings))
	for i, f := range findings {
		for j := range ansFields {
			s := ""
			if f.text[j] != nil {
				s = *f.text[j]
			}
			parts[i][j] = ansCapBytes(s, textCap)
		}
		x := ""
		if f.excerpt != nil {
			x = *f.excerpt
		}
		parts[i][3] = ansCapBytes(x, sourceCap)
	}
	used := len(render(parts, ""))
	packet := render(parts, ansCapBytes(redact(change), limit-used))
	if len(packet) > limit {
		return "", "packet-too-large", nil
	}
	if redact(packet) != packet {
		return "", "packet-unreadable", nil
	}
	return packet, "", nil
}

// ansDetails is answers.mjs findingDetails: the open, then late, findings
// of the source's fix-loop state by id (none when it does not load).
func (w *TicketRunner) ansDetails(p *ansPlan, rows []ledger.Object, root string) (map[string]ledger.Object, error) {
	out := map[string]ledger.Object{}
	where := StateWhere{Home: w.Home, RunRootDir: w.RunRootDir, RunID: w.RunID, Block: p.block, File: p.file, ContentRoot: root, Key: w.Key, Rows: rows}
	loaded, err := LoadState(where, false)
	if err != nil {
		if IsUnsupported(err) {
			return nil, err
		}
		return nil, unsupported("the answers state read: " + err.Error())
	}
	if loaded.Status != "ok" && loaded.Status != "restored" {
		return out, nil
	}
	for _, k := range []string{"open", "late"} {
		v := loaded.State.Get(k)
		if v == nil {
			continue
		}
		list, ok := v.([]any)
		if !ok {
			return nil, unsupported("a state " + k + " list that is not an array")
		}
		for _, item := range list {
			f, ok := item.(ledger.Object)
			if !ok {
				if item == nil {
					continue
				}
				return nil, unsupported("a state finding that is not an object")
			}
			if id, ok := f.Get("id").(string); ok {
				if _, had := out[id]; !had {
					out[id] = f
				}
			}
		}
	}
	return out, nil
}

// ansRun is reviewAnswers from the prompt write: the engine outcome.
func (w *TicketRunner) ansRun(p *ansPlan) (ledger.Object, error) {
	tag := make([]byte, 6)
	if _, err := rand.Read(tag); err != nil {
		return ansOutcome("unavailable", "packet-write-failed", nil), nil
	}
	promptPath := filepath.Join(p.workDir, "answers-"+hex.EncodeToString(tag)+".md")
	if err := os.MkdirAll(p.workDir, 0o700); err != nil {
		return ansOutcome("unavailable", "packet-write-failed", nil), nil
	}
	if err := os.WriteFile(promptPath, []byte(p.packet), 0o600); err != nil {
		return ansOutcome("unavailable", "packet-write-failed", nil), nil
	}
	stamp := func(row ledger.Object) error {
		out := ledger.Object{{Key: "run", Value: w.RunID}}
		for _, f := range row {
			out.Set(f.Key, f.Value)
		}
		out.Set("block", p.block)
		out.Set("file", p.file)
		out.Set("content_hash", p.hash)
		signed, err := w.signObject(out)
		if err != nil {
			return err
		}
		return w.writeRow(signed)
	}
	spawn := w.pooled(p.id, p.cfg, func(in review.SpawnOpts) (ledger.Object, error) {
		return session.Spawn(ansSessionOpts(p.sopts, in), p.sdeps)
	})
	res, err := w.ansSpawn(spawn, promptPath, stamp)
	_ = os.Remove(promptPath)
	if err != nil {
		return nil, err
	}
	var provider, model, tokensIn, tokensOut any
	if res != nil {
		provider, model = res.Get("provider"), res.Get("model")
		if usage, ok := res.Get("usage").(ledger.Object); ok {
			tokensIn, tokensOut = usage.Get("tokens_in"), usage.Get("tokens_out")
		}
	}
	sessions := []any{ledger.Object{{Key: "lens", Value: "answers"}, {Key: "role", Value: "reviewer"}, {Key: "level", Value: "L2"},
		{Key: "provider", Value: provider}, {Key: "model", Value: model}, {Key: "tokens_in", Value: tokensIn}, {Key: "tokens_out", Value: tokensOut}}}
	if res == nil {
		return ansOutcome("unavailable", "session-error", sessions), nil
	}
	status, isString := res.Get("status").(string)
	exitBad := false
	if hasKey(res, "exit_code") {
		code, ok := res.Get("exit_code").(float64)
		exitBad = !ok || code != 0
	}
	if status != "ok" || exitBad {
		reason := "exit"
		if isString && status != "ok" {
			reason = status
		}
		return ansOutcome("unavailable", reason, sessions), nil
	}
	verdicts := ansVerdicts(res.Get("answer"), p.ids)
	findings := []any{}
	resolved := []string{}
	for _, id := range p.ids {
		v, ok := verdicts[id]
		if !ok {
			v = [2]string{"not_resolved", "no valid verdict"}
		}
		findings = append(findings, ledger.Object{{Key: "id", Value: id}, {Key: "severity", Value: p.severity[id]}, {Key: "verdict", Value: v[0]}, {Key: "reason", Value: v[1]}})
		if v[0] == "resolved" {
			resolved = append(resolved, id)
		}
	}
	done := gatecheck.TicketAnswered(p.gate, p.file, p.id)
	for _, id := range resolved {
		if done[id] {
			continue
		}
		row := ledger.Object{{Key: "event", Value: "review.answered"}, {Key: "finding", Value: id}, {Key: "by_file", Value: p.byFile}, {Key: "by_hash", Value: p.byHash}, {Key: "ticket", Value: p.id}}
		if stamp(row) != nil {
			return ansOutcome("unavailable", "ledger-write-failed", sessions), nil
		}
	}
	return ledger.Object{{Key: "status", Value: "answers"}, {Key: "approved", Value: false}, {Key: "engine", Value: "adaptive"}, {Key: "kind", Value: "answers"},
		{Key: "findings", Value: findings}, {Key: "sessions", Value: sessions}}, nil
}

// ansSpawn is spawnWithTimeoutRetry with no ladder: one same-model retry
// after a timeout, each timeout noted; nil for a spawn that threw.
func (w *TicketRunner) ansSpawn(spawn func(review.SpawnOpts) (ledger.Object, error), promptPath string, note func(ledger.Object) error) (ledger.Object, error) {
	in := review.SpawnOpts{Level: "L2", Role: "reviewer", PromptPath: promptPath, RowExtra: ledger.Object{{Key: "lens", Value: "answers"}}}
	for attempts := 1; ; attempts++ {
		res, err := spawn(in)
		if err != nil {
			if IsUnsupported(err) || session.IsUnsupported(err) {
				return nil, unsupported("the answers session: " + err.Error())
			}
			return nil, nil
		}
		if res.Get("status") != "timeout" {
			return res, nil
		}
		retried := attempts <= 1
		row := ledger.Object{{Key: "event", Value: "review.session_timeout"}, {Key: "lens", Value: "answers"}, {Key: "role", Value: "reviewer"}, {Key: "level", Value: "L2"},
			{Key: "provider", Value: res.Get("provider")}, {Key: "model", Value: res.Get("model")}, {Key: "attempt", Value: float64(attempts)}, {Key: "retried", Value: retried}}
		if n, ok := res.Get("duration_ms").(float64); ok {
			row = append(row, ledger.Field{Key: "duration_ms", Value: n})
		} else {
			row = append(row, ledger.Field{Key: "duration_ms", Value: nil})
		}
		if s, ok := res.Get("stderr_tail").(string); ok {
			row = append(row, ledger.Field{Key: "stderr_tail", Value: s})
		} else {
			row = append(row, ledger.Field{Key: "stderr_tail", Value: nil})
		}
		if s, ok := res.Get("tail_note").(string); ok {
			row = append(row, ledger.Field{Key: "tail_note", Value: s})
		}
		if n, ok := res.Get("stdout_bytes").(float64); ok {
			row = append(row, ledger.Field{Key: "stdout_bytes", Value: n})
		} else {
			row = append(row, ledger.Field{Key: "stdout_bytes", Value: nil})
		}
		if ev, ok := res.Get("stdout_events").([]any); ok {
			row = append(row, ledger.Field{Key: "stdout_events", Value: ev})
		} else {
			row = append(row, ledger.Field{Key: "stdout_events", Value: []any{}})
		}
		_ = note(row)
		if !retried {
			return res, nil
		}
		if w.Warn != nil {
			w.Warn("review_retry", "a review session timed out; it was retried once")
		}
	}
}

// ansVerdicts is answers.mjs verdictsOf: [verdict, reason] per requested id.
func ansVerdicts(answer any, ids []string) map[string][2]string {
	out := map[string][2]string{}
	o, ok := answer.(ledger.Object)
	if !ok {
		return out
	}
	list, _ := o.Get("verdicts").([]any)
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	seen, repeated := map[string]bool{}, map[string]bool{}
	for _, item := range list {
		e, ok := item.(ledger.Object)
		if !ok {
			continue
		}
		id, ok := e.Get("id").(string)
		if !ok || !want[id] {
			continue
		}
		if seen[id] {
			repeated[id] = true
		}
		seen[id] = true
		verdict, _ := e.Get("verdict").(string)
		reason, ok := e.Get("reason").(string)
		if (verdict == "resolved" || verdict == "not_resolved") && ok {
			out[id] = [2]string{verdict, ansCutUTF16(reason, ansReasonMax)}
		}
	}
	for id := range repeated {
		delete(out, id)
	}
	return out
}

// ansCutUTF16 is s.slice(0, max) in UTF-16 units; a cut through a
// surrogate pair keeps U+FFFD where Node keeps the lone high surrogate.
func ansCutUTF16(s string, max int) string {
	n := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if n+w > max {
			if n < max {
				return s[:i] + "�"
			}
			return s[:i]
		}
		n += w
	}
	return s
}
