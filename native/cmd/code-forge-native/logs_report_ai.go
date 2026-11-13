package main

// The AI cleaning pass of `logs report` (B615), src/session/scrub.mjs
// runAiScrub: one closed-book reviewer session at L1 (the project's config
// when its L1 resolves, else the shipped defaults of its provider or of
// anthropic), the packet on stdin, no tools. The model only LISTS what it
// would hide; lrApplyItems replaces each exact substring with <kind>.

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/session"
)

// lrAIMaxBytes is AI_MAX_BYTES; lrAITimeoutMs is AI_TIMEOUT_MS (a seam for tests).
const lrAIMaxBytes = 16 * 1024

var lrAITimeoutMs float64 = 180_000

// lrAIKinds is AI_KINDS.
var lrAIKinds = []string{"person", "company", "project", "host", "url", "account", "path", "secret", "other"}

// lrAIKindLabels is KIND_LABELS.
var lrAIKindLabels = map[string][2]string{
	"person": {"person", "people"}, "company": {"company", "companies"}, "project": {"project", "projects"},
	"host": {"host", "hosts"}, "url": {"URL", "URLs"}, "account": {"account", "accounts"},
	"path": {"path", "paths"}, "secret": {"secret", "secrets"}, "other": {"other item", "other items"},
}

// lrScrubSchemaJSON is JSON.stringify(SCRUB_SCHEMA).
const lrScrubSchemaJSON = `{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{"text":{"type":"string"},"kind":{"type":"string"},"reason":{"type":"string"}},"required":["text","kind","reason"],"additionalProperties":false}}},"required":["items"],"additionalProperties":false}`

// lrScrubLens is SCRUB_LENS.
const lrScrubLens = "You check an error report before it is posted publicly. You have no tools and no files; the report is in the message. " +
	"You never rewrite the report: you only list what should be hidden. Answer once, with JSON only."

// lrScrubPrompt is buildScrubPrompt.
func lrScrubPrompt(report string) string {
	return strings.Join([]string{
		"# Find private details in this error report",
		"",
		"Find anything in the text that could identify a person, a company, a private project, a host, an internal URL, an account, a path, or anything that looks like a secret or credential.",
		"Placeholders such as <project>, <slug>, <email>, <item-id>, op://<ref> and ~ are already cleaned: leave them out. Public names (code-forge, Node, GitHub, npm, an OS) are fine.",
		"Return JSON only, exactly this shape:",
		`{ "items": [{ "text": "<exact substring>", "kind": "person|company|project|host|url|account|path|secret|other", "reason": "<short>" }] }`,
		`Each "text" must be copied exactly from the report. Return { "items": [] } when nothing should be hidden.`,
		"",
		"## Report",
		"",
		report,
		"",
	}, "\n")
}

func lrScrubSchema() ledger.Object {
	schema, _ := ledger.DecodeObject([]byte(lrScrubSchemaJSON))
	return schema
}

// lrScrubAnswerOK is matchesSchema(SCRUB_SCHEMA, answer).
func lrScrubAnswerOK(answer any) bool {
	obj, ok := answer.(ledger.Object)
	if !ok || len(obj) != 1 || obj[0].Key != "items" {
		return false
	}
	list, ok := obj[0].Value.([]any)
	if !ok {
		return false
	}
	for _, raw := range list {
		item, ok := raw.(ledger.Object)
		if !ok || len(item) != 3 {
			return false
		}
		seen := map[string]bool{}
		for _, f := range item {
			if _, isString := f.Value.(string); !isString || seen[f.Key] || (f.Key != "text" && f.Key != "kind" && f.Key != "reason") {
				return false
			}
			seen[f.Key] = true
		}
	}
	return true
}

type lrScrubItem struct{ text, kind string }

func lrUTF16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// lrReadItems is readItems on an answer that matched the schema.
func lrReadItems(answer any) ([]lrScrubItem, bool) {
	obj, ok := answer.(ledger.Object)
	if !ok {
		return nil, false
	}
	list, ok := obj.Get("items").([]any)
	if !ok {
		return nil, false
	}
	out := []lrScrubItem{}
	for _, raw := range list {
		item, ok := raw.(ledger.Object)
		if !ok {
			continue
		}
		text, isString := item.Get("text").(string)
		if !isString || strings.TrimFunc(text, jsSpace) == "" || lrUTF16Len(text) < 3 {
			continue
		}
		kind := "other"
		if k, ok := item.Get("kind").(string); ok {
			for _, known := range lrAIKinds {
				if strings.ToLower(k) == known {
					kind = known
				}
			}
		}
		out = append(out, lrScrubItem{text: text, kind: kind})
	}
	return out, true
}

// lrProtected is PROTECTED_PLACEHOLDERS: SCRUB_PLACEHOLDERS, then <kind> per AI kind.
func lrProtected() []string {
	list := []string{"<project>", "<slug>", "<email>", "<ref>", "<item-id>", "<unprintable error>", "[REDACTED]"}
	for _, k := range lrAIKinds {
		list = append(list, "<"+k+">")
	}
	return list
}

type lrAICount struct {
	kind  string
	count int
}

// lrApplyItems is applyItems: every exact occurrence of each item's text
// becomes <kind>, longest text first, never inside one of the tool's own
// placeholders and never overlapping a range already taken.
func lrApplyItems(texts []string, items []lrScrubItem) ([]string, []lrAICount) {
	sorted := append([]lrScrubItem{}, items...)
	sort.SliceStable(sorted, func(i, j int) bool { return lrUTF16Len(sorted[i].text) > lrUTF16Len(sorted[j].text) })
	tally := map[string]int{}
	protected := lrProtected()
	out := make([]string, len(texts))
	type span struct {
		start, end int
		kind       string
	}
	for n, t := range texts {
		var guarded [][2]int
		for _, p := range protected {
			for at := strings.Index(t, p); at != -1; {
				guarded = append(guarded, [2]int{at, at + len(p)})
				next := strings.Index(t[at+len(p):], p)
				if next == -1 {
					break
				}
				at += len(p) + next
			}
		}
		var taken []span
		blocked := func(a, b int) bool {
			for _, g := range guarded {
				s, e := g[0], g[1]
				if (a == s && b == e) || (a < e && s < b && !(a <= s && e <= b)) {
					return true
				}
			}
			for _, r := range taken {
				if a < r.end && r.start < b {
					return true
				}
			}
			return false
		}
		for _, item := range sorted {
			for at := strings.Index(t, item.text); at != -1; {
				end := at + len(item.text)
				if !blocked(at, end) {
					taken = append(taken, span{at, end, item.kind})
					tally[item.kind]++
				}
				// indexOf(text, at + 1): the next start is past this one's first character
				step := 1
				for step < len(t)-at && (t[at+step]&0xC0) == 0x80 {
					step++
				}
				next := strings.Index(t[at+step:], item.text)
				if next == -1 {
					break
				}
				at += step + next
			}
		}
		sort.SliceStable(taken, func(i, j int) bool { return taken[i].start < taken[j].start })
		var built strings.Builder
		pos := 0
		for _, r := range taken {
			built.WriteString(t[pos:r.start] + "<" + r.kind + ">")
			pos = r.end
		}
		built.WriteString(t[pos:])
		out[n] = built.String()
	}
	counts := []lrAICount{}
	for _, k := range lrAIKinds {
		if tally[k] > 0 {
			counts = append(counts, lrAICount{k, tally[k]})
		}
	}
	return out, counts
}

// lrDescribeAICounts is describeAiCounts.
func lrDescribeAICounts(counts []lrAICount) string {
	parts := []string{}
	for _, c := range counts {
		if c.count <= 0 {
			continue
		}
		label, ok := lrAIKindLabels[c.kind]
		if !ok {
			label = [2]string{c.kind, c.kind}
		}
		word := label[1]
		if c.count == 1 {
			word = label[0]
		}
		parts = append(parts, strconv.Itoa(c.count)+" "+word)
	}
	if len(parts) == 0 {
		return "AI pass: nothing found."
	}
	return "AI pass: " + strings.Join(parts, ", ") + "."
}

// lrLevelResolves is resolveLevel(cfg, level) not throwing.
func lrLevelResolves(cfg map[string]any, level string) bool {
	levels, _ := cfg["levels"].(map[string]any)
	entry, _ := levels[level].(map[string]any)
	if model, _ := entry["model"].(string); model == "" {
		return false
	}
	raw, has := entry["provider"]
	if !has || raw == nil {
		raw = cfg["provider"]
	}
	provider, _ := raw.(string)
	return provider != ""
}

// lrScrubConfig is scrubConfig: the project's config when its L1 resolves,
// else the shipped defaults alone; why is the fixed message of a config that
// cannot be read.
func lrScrubConfig(cwd string) (map[string]any, string) {
	loaded, err := config.LoadUnchecked(cwd)
	if err != nil {
		var cfgErr *config.Error
		if errors.As(err, &cfgErr) && cfgErr.Kind == "not-found" {
			return config.DefaultsForProvider("anthropic"), ""
		}
		return nil, "the project config could not be read"
	}
	if lrLevelResolves(loaded, "L1") {
		return loaded, ""
	}
	provider, _ := loaded["provider"].(string)
	if provider == "" {
		provider = "anthropic"
	}
	if d := config.DefaultsForProvider(provider); d != nil {
		return d, ""
	}
	return config.DefaultsForProvider("anthropic"), ""
}

func lrScrubOpts(cfg map[string]any, promptPath, root string) session.Opts {
	lens := lrScrubLens
	return session.Opts{Cfg: cfg, Level: "L1", Role: "reviewer", PromptPath: promptPath, Schema: lrScrubSchema(),
		SystemPrompt: &lens, Validate: lrScrubAnswerOK, TimeoutMs: lrAITimeoutMs, RunRoot: root}
}

// lrAIServed tells, before any output, whether Go runs the AI pass like Node:
// false = a session Go's session layer does not serve (hand it to Node).
func lrAIServed(cwd string) bool {
	cfg, why := lrScrubConfig(cwd)
	if why != "" {
		return true // Node's pass is "unavailable" with that reason; so is Go's
	}
	return !session.IsUnsupported(session.Check(lrScrubOpts(cfg, "/packet.md", "")))
}

// lrPlainReason is plainReason.
func lrPlainReason(status, reason string) string {
	switch status {
	case "unavailable":
		switch reason {
		case "cli-missing":
			return "the model CLI is not installed"
		case "login-expired":
			return "the model CLI is not logged in"
		case "rate-limited", "http-402":
			return "the model is not available right now"
		}
		return "the model is not available"
	case "timeout":
		return "it timed out"
	case "invalid-output":
		return "its answer was not valid JSON"
	}
	return "the session failed"
}

// lrRunAIScrub is runAiScrub: the items, or why the pass is unavailable.
func lrRunAIScrub(report, cwd string) ([]lrScrubItem, string) {
	packet := lrScrubPrompt(report)
	if len(packet) > lrAIMaxBytes {
		return nil, "the report is over 16 KB"
	}
	cfg, why := lrScrubConfig(cwd)
	if why != "" {
		return nil, why
	}
	root, err := lrProcRoot()
	if err != nil {
		return nil, "the session could not start"
	}
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		return nil, "the session could not start"
	}
	dir := filepath.Join(root, "scrub", strconv.FormatInt(time.Now().UnixMilli(), 10)+"-"+hex.EncodeToString(suffix))
	defer os.RemoveAll(dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, "the session could not start"
	}
	packetPath := filepath.Join(dir, "packet.md")
	if err := os.WriteFile(packetPath, []byte(packet), 0o600); err != nil {
		return nil, "the session could not start"
	}
	res, err := session.Spawn(lrScrubOpts(cfg, packetPath, root), session.Deps{Stderr: lrDiscard{}, RunRoot: lrProcRoot, PidsRoot: lrProcRoot})
	if err != nil || res == nil {
		return nil, "the session could not start"
	}
	if status, _ := res.Get("status").(string); status != "ok" {
		reason, _ := res.Get("reason").(string)
		return nil, lrPlainReason(status, reason)
	}
	items, ok := lrReadItems(res.Get("answer"))
	if !ok {
		return nil, "its answer was not valid JSON"
	}
	return items, ""
}

// lrDiscard is Node's stderr { write: () => true }: the session's notes are dropped.
type lrDiscard struct{}

func (lrDiscard) Write(p []byte) (int, error) { return len(p), nil }
