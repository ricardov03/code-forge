package gatecheck

import (
	"encoding/json"
	"math"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/ledger"
)

var transcriptTokens = regexp.MustCompile(">>?|[^\\s\\x0b'\"`;|&(),{}\\[\\]>]+")

// Transcript handles the plain-text grep, Claude Code JSONL logs and Codex exec --json logs with
// Node's list order and hit rules: the git/gh/rm rules, the production marker,
// code-forge-runs-access (Node after B415, B416), code-forge-reviews-write (B466) and the
// code-forge launcher verbs and flags a coder may not run. Text whose tokens Go could split
// differently (JavaScript-only whitespace) stays with Node's scanner. Returning unsupported happens
// before any close warning is published.
func Transcript(text string) ([]Blocker, bool) {
	runs := runsMatcher{}
	if strings.Contains(text, ".code-forge") && utf8.ValidString(text) {
		home := os.Getenv("HOME")
		if home == "" {
			return nil, false
		}
		runs = newRunsMatcher(home)
	}
	hits := []Blocker{}
	ok := transcriptLines(text, func(lineNo int, scans []transcriptScan) {
		ids := map[string]bool{}
		for _, s := range scans {
			for id := range transcriptCommandIDs(s.command) {
				ids[id] = true
			}
			if runs.enabled && (runs.anyToken(s.path) || runs.anyRelative(s.words)) {
				ids["code-forge-runs-access"] = true
			}
			if reviewsWrite(s.path) {
				ids["code-forge-reviews-write"] = true
			}
		}
		for _, id := range transcriptOrder {
			if ids[id] {
				hits = append(hits, Blocker{Code: "rule_break", Detail: "transcript line " + strconv.Itoa(lineNo+1) + ": " + id})
			}
		}
	})
	if !ok {
		return nil, false
	}
	return hits, true
}

// transcriptScan is one text Node's scanTranscript reads on a line: the command text, the text
// path rules scan and the words where a home-relative spelling counts.
type transcriptScan struct {
	command, path string
	words         []string
}

// transcriptLines walks the transcript in Node's scanTranscript line modes (plain text with its
// unescaped variant, Claude Code JSONL, Codex exec --json) and hands each scanned line's texts to
// fn. false: text Go cannot read or split exactly like Node.
func transcriptLines(text string, fn func(lineNo int, scans []transcriptScan)) bool {
	if !utf8.ValidString(text) {
		return false
	}
	lines := strings.Split(text, "\n")
	events, claude := claudeEvents(lines)
	var codex []*codexLine
	if !claude {
		var isCodex, ok bool
		codex, isCodex, ok = codexLines(lines)
		if !ok {
			return false
		}
		if !isCodex {
			codex = nil
			// Plain text is split on JavaScript whitespace.
			if !transcriptJSSafe(text) {
				return false
			}
		}
	}
	type scanned struct {
		line  int
		scans []transcriptScan
	}
	all := []scanned{}
	for lineNo, line := range lines {
		switch {
		case claude:
			ev := events[lineNo]
			if ev == nil || ev.kind != "assistant" {
				continue
			}
			l, ok := claudeAssistantLine(ev.message)
			if !ok {
				return false
			}
			if !l.found {
				continue
			}
			all = append(all, scanned{lineNo, []transcriptScan{{l.command, l.path, l.words}}})
		case codex != nil:
			l := codex[lineNo]
			if l == nil {
				continue
			}
			all = append(all, scanned{lineNo, []transcriptScan{{l.command, l.path, l.words}}})
		default:
			scans := []transcriptScan{{line, line, nil}}
			if variant := transcriptUnescaped(line); variant != line {
				scans = append(scans, transcriptScan{variant, variant, nil})
			}
			all = append(all, scanned{lineNo, scans})
		}
	}
	for _, s := range all {
		fn(s.line, s.scans)
	}
	return true
}

// HookTranscript is Node's B64b hook check: scanTranscript(text, hookForbidden(argv)). A coder
// transcript line where a command named like the on_block_end hook's command (and its first
// argument, when there is one) runs is a rule_break with id code-forge-on-block-end-hook.
// false: text Go cannot read or split exactly like Node.
func HookTranscript(argv []string, text string) ([]Blocker, bool) {
	prefix := hookPrefix(argv)
	hits := []Blocker{}
	ok := transcriptLines(text, func(lineNo int, scans []transcriptScan) {
		if prefix == nil {
			return
		}
		for _, s := range scans {
			if hookHit(prefix, transcriptTokens.FindAllString(s.command, -1)) {
				hits = append(hits, Blocker{Code: "rule_break", Detail: "transcript line " + strconv.Itoa(lineNo+1) + ": code-forge-on-block-end-hook"})
				return
			}
		}
	})
	if !ok {
		return nil, false
	}
	return hits, true
}

// hookPrefix is hookForbidden's prefix tokens: the command name and the trimmed first argument;
// nil when Node builds no entry.
func hookPrefix(argv []string) []string {
	if len(argv) == 0 || slices.Contains(argv, "") {
		return nil
	}
	first := jsTrim(argv[0])
	if first == "" || strings.HasSuffix(first, "/") || strings.HasSuffix(first, "\\") {
		return nil
	}
	name := jsTrim(commandName(first))
	if name == "" || name == "." || name == ".." {
		return nil
	}
	if len(argv) > 1 {
		if arg := jsTrim(argv[1]); arg != "" {
			return []string{name, arg}
		}
	}
	return []string{name}
}

// hookHit is the scan's window loop for the one prefix entry: a window starts where the token's
// command name is the prefix's first word (the npx/node starts and code-forge tokens never read
// as another name), git reads every global-option tail.
func hookHit(prefix, tokens []string) bool {
	for i, token := range tokens {
		if commandName(token) != prefix[0] {
			continue
		}
		window := tokens[i:min(len(tokens), i+64)]
		readings := [][]string{append([]string{prefix[0]}, window[1:]...)}
		if prefix[0] == "git" {
			readings = gitReadings(window)
		}
		for _, r := range readings {
			if len(r) >= len(prefix) && slices.Equal(r[:len(prefix)], prefix) {
				return true
			}
		}
	}
	return false
}

// codexLine is one Codex item line as Node's scanTranscript reads it: the command text, the text
// path rules scan and the words where a home-relative spelling counts.
type codexLine struct {
	command, path string
	words         []string
}

// codexLines is Node's Codex exec --json reading (B466): every non-empty line an object with a
// string type, the first one thread.started (a trimmed stdin banner before the first JSON line is
// allowed). isCodex false means Node scans the text as plain text. ok false: a scanned value Go
// cannot render or split exactly like Node.
func codexLines(lines []string) (out []*codexLine, isCodex, ok bool) {
	type event struct {
		kind string
		item json.RawMessage
	}
	events := make([]*event, len(lines))
	beforeJSON, first, seen := true, "", 0
	for i, line := range lines {
		trimmed := jsTrim(line)
		if trimmed == "" {
			continue
		}
		if beforeJSON && trimmed == "Reading additional input from stdin..." {
			continue
		}
		if !json.Valid([]byte(line)) {
			return nil, false, true
		}
		beforeJSON = false
		var obj map[string]json.RawMessage
		if !jsonIsObject(json.RawMessage(line)) || json.Unmarshal([]byte(line), &obj) != nil || obj == nil {
			return nil, false, true
		}
		kind, isStr := jsonString(obj["type"])
		if !isStr {
			return nil, false, true
		}
		if seen == 0 {
			first = kind
		}
		seen++
		events[i] = &event{kind, obj["item"]}
	}
	if seen == 0 || first != "thread.started" {
		return nil, false, true
	}
	out = make([]*codexLine, len(lines))
	for i, ev := range events {
		if ev == nil || !strings.HasPrefix(ev.kind, "item.") || !jsonTruthy(ev.item) {
			continue
		}
		var item map[string]json.RawMessage
		if jsonIsObject(ev.item) && json.Unmarshal(ev.item, &item) != nil {
			return nil, false, false
		}
		kind, _ := jsonString(item["type"])
		if kind == "agent_message" || kind == "reasoning" {
			continue
		}
		l := &codexLine{}
		switch kind {
		case "command_execution":
			if command, isStr := jsonString(item["command"]); isStr {
				if !transcriptJSSafe(command) {
					return nil, false, false
				}
				l.command = transcriptCommandText(command, true)
				l.path, l.words = transcriptPathText(command)
			} else {
				text, good := jsStringifyWithout(ev.item, "aggregated_output")
				if !good {
					return nil, false, false
				}
				l.command, l.path = text, text
			}
		case "file_change":
			var changes []json.RawMessage
			if strings.HasPrefix(strings.TrimSpace(string(item["changes"])), "[") && json.Unmarshal(item["changes"], &changes) != nil {
				return nil, false, false
			}
			parts := []string{}
			for _, raw := range changes {
				var change map[string]json.RawMessage
				if !jsonIsObject(raw) || json.Unmarshal(raw, &change) != nil {
					continue
				}
				file, isStr := jsonString(change["path"])
				if !isStr {
					continue
				}
				var quoted strings.Builder
				if !jsQuote(&quoted, file) {
					return nil, false, false
				}
				parts = append(parts, "Write("+quoted.String()+")")
				l.words = append(l.words, file)
			}
			l.command = strings.Join(parts, " ")
			l.path = l.command
		default:
			text, good := jsStringify(ev.item)
			if !good {
				return nil, false, false
			}
			l.command, l.path = text, text
		}
		out[i] = l
	}
	return out, true, true
}

// jsonTruthy is JavaScript truthiness of a parsed JSON value (a missing key is undefined).
func jsonTruthy(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return false
	}
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case json.Number:
		f, err := strconv.ParseFloat(x.String(), 64)
		return err != nil || f != 0
	}
	return true
}

// jsStringifyWithout is JSON.stringify({...object, [key]: undefined}): the object's text with
// that key left out (non-objects are stringified whole).
func jsStringifyWithout(raw json.RawMessage, key string) (string, bool) {
	if !jsonIsObject(raw) {
		return jsStringify(raw)
	}
	obj, err := ledger.DecodeObject(raw)
	if err != nil {
		return "", false
	}
	obj.Delete(key)
	data, err := json.Marshal(rawObject(obj))
	if err != nil {
		return "", false
	}
	return jsStringify(data)
}

// rawObject re-encodes an ordered object with its key order kept, for jsStringify to check.
type rawObject ledger.Object

func (o rawObject) MarshalJSON() ([]byte, error) {
	var b strings.Builder
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, _ := json.Marshal(f.Key)
		b.Write(k)
		b.WriteByte(':')
		v, err := marshalOrdered(f.Value)
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func marshalOrdered(v any) ([]byte, error) {
	switch x := v.(type) {
	case ledger.Object:
		return rawObject(x).MarshalJSON()
	case []any:
		var b strings.Builder
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			data, err := marshalOrdered(item)
			if err != nil {
				return nil, err
			}
			b.Write(data)
		}
		b.WriteByte(']')
		return []byte(b.String()), nil
	}
	return json.Marshal(v)
}

var (
	reviewsWritePaths = []*regexp.Regexp{
		regexp.MustCompile(`(?:^|[/=:\s\x0b'"])` + regexp.QuoteMeta(".code-forge/reviews") + `(?:/|$|[\s\x0b'";)])`),
		regexp.MustCompile(`(?:^|[/=:\s\x0b'"])` + regexp.QuoteMeta(".code-forge/queue/") + `[^/\s\x0b]*` + regexp.QuoteMeta(".done") + `(?:/|$|[\s\x0b'";)])`),
	}
	writeVerbs        = []string{"cp", "mv", "tee", "touch", "rm", "ln", "install", "truncate", "dd", "rsync", "chmod", "chown", "unlink", "mkdir", "rmdir", ">", ">>", "Write", "Edit", "MultiEdit", "NotebookEdit"}
	writeWrappers     = []string{"sudo", "env", "xargs", "nice", "timeout", "command", "nohup"}
	writeWrapperValue = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=|^\d+(\.\d+)?[smhd]?$`)
	writeInPlace      = regexp.MustCompile(`^-[a-zA-Z]*i|^--in-place(=|$)`)
)

// reviewsWrite is Node's code-forge-reviews-write path rule (access write) on one text: a token
// naming .code-forge/reviews or .code-forge/queue/*.done after the first write command.
func reviewsWrite(text string) bool {
	if !strings.Contains(text, ".code-forge/") {
		return false
	}
	tokens := transcriptTokens.FindAllString(text, -1)
	firstWrite := -1
	for i, t := range tokens {
		name := commandName(t)
		if (slices.Contains(writeVerbs, name) || slices.Contains(writeWrappers, name) || name == "sed" || name == "perl" || name == "git") && isWriteCommand(tokens[i:min(len(tokens), i+64)]) {
			firstWrite = i
			break
		}
	}
	if firstWrite < 0 {
		return false
	}
	for _, t := range tokens[firstWrite+1:] {
		for _, p := range reviewsWritePaths {
			if p.MatchString(t) {
				return true
			}
		}
	}
	return false
}

// isWriteCommand is Node's isWriteCommand: wrappers skipped, then a write verb, sed/perl in place,
// or a git subcommand that writes a path.
func isWriteCommand(argv []string) bool {
	i := 0
	for i < len(argv) && slices.Contains(writeWrappers, commandName(argv[i])) {
		i++
		for i < len(argv) && (strings.HasPrefix(argv[i], "-") || writeWrapperValue.MatchString(argv[i])) {
			i++
		}
	}
	verb := ""
	if i < len(argv) {
		verb = commandName(argv[i])
	}
	switch {
	case slices.Contains(writeVerbs, verb):
		return true
	case verb == "sed" || verb == "perl":
		return slices.ContainsFunc(argv[i+1:], writeInPlace.MatchString)
	case verb == "git":
		for _, r := range gitReadings(argv[i:]) {
			if len(r) > 1 && slices.Contains([]string{"mv", "rm", "restore", "checkout"}, r[1]) {
				return true
			}
		}
	}
	return false
}

// transcriptJSSafe: no rune JavaScript's \s treats as whitespace and Go's does not (and no U+0085,
// which Go trims and JavaScript does not), so both split the text into the same tokens.
func transcriptJSSafe(text string) bool {
	for _, r := range text {
		if r < 0x80 {
			continue
		}
		switch {
		case r == 0x85, r == 0xa0, r == 0x1680, r >= 0x2000 && r <= 0x200a, r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000, r == 0xfeff:
			return false
		}
	}
	return true
}

var transcriptOrder = []string{"gh-pr-ready", "gh-pr-merge", "gh-pr-close", "gh-pr-base-retarget", "git-push-force", "git-reset-hard", "git-checkout-discard", "git-restore", "git-clean", "git-stash", "git-branch-force-delete", "git-branch-force-delete-long", "git-rm", "rm-rf", "production-marker", "code-forge-runs-access", "code-forge-worker-from-coder", "code-forge-reviews-write",
	"code-forge-block-waive-from-coder", "code-forge-block-reopen-from-coder", "code-forge-autopilot-from-coder", "code-forge-no-require-reviews", "code-forge-remove-worktree",
	"code-forge-block-accept-hit-from-coder", "code-forge-block-unwaive-from-coder", "code-forge-block-ack-waivers-from-coder", "code-forge-block-human-pass-from-coder",
	"code-forge-add-worker-from-coder", "code-forge-block-amend-acceptance-from-coder"}

// commandName is Node's commandName: the basename without a trailing .exe (any case).
func commandName(token string) string {
	name := path.Base(token)
	if token == "/" || strings.Trim(token, "/") == "" {
		name = ""
	}
	if n := len(name); n >= 4 && name[n-4] == '.' && strings.ToLower(name[n-3:]) == "exe" {
		name = name[:n-4]
	}
	return name
}

var (
	codeForgeScoped   = regexp.MustCompile(`^@codedology/code-forge(@.*)?$`)
	codeForgeBin      = regexp.MustCompile(`^code-forge(\.mjs)?(@.*)?$`)
	codeForgePrefixes = []struct {
		id    string
		verbs [][]string
	}{
		{"code-forge-worker-from-coder", [][]string{{"worker"}, {"run", "start"}, {"run", "reload"}}},
		{"code-forge-block-waive-from-coder", [][]string{{"block", "waive"}}},
		{"code-forge-block-reopen-from-coder", [][]string{{"block", "reopen"}}},
		{"code-forge-autopilot-from-coder", [][]string{{"autopilot"}}},
		{"code-forge-block-accept-hit-from-coder", [][]string{{"block", "accept-hit"}}},
		{"code-forge-block-unwaive-from-coder", [][]string{{"block", "unwaive"}}},
		{"code-forge-block-ack-waivers-from-coder", [][]string{{"block", "ack-waivers"}}},
		{"code-forge-block-human-pass-from-coder", [][]string{{"block", "human-pass"}}},
		{"code-forge-block-amend-acceptance-from-coder", [][]string{{"block", "amend-acceptance"}}},
	}
)

// codeForgeIDs is Node's anyPrefix and commandFlag code-forge entries on one command window:
// unwrapLauncher (npx/bunx, pnpm/yarn exec|dlx, node, the package or bin read as code-forge,
// option tokens dropped), then each entry's code-forge or forge prefixes.
func codeForgeIDs(window []string, ids map[string]bool) {
	reading := append([]string{commandName(window[0])}, window[1:]...)
	if len(reading) >= 4 && reading[0] == "code-forge" && reading[1] == "run" && reading[2] == "start" && slices.Contains(reading[3:], "--add-worker") {
		ids["code-forge-add-worker-from-coder"] = true
	}
	rest := reading
	skipOptions := func() {
		for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			if slices.Contains([]string{"-p", "--package", "-r", "--require"}, rest[0]) {
				rest = rest[min(2, len(rest)):]
			} else {
				rest = rest[1:]
			}
		}
	}
	switch {
	case rest[0] == "npx" || rest[0] == "bunx":
		rest = rest[1:]
		skipOptions()
	case (rest[0] == "pnpm" || rest[0] == "yarn") && len(rest) > 1 && (rest[1] == "exec" || rest[1] == "dlx"):
		rest = rest[2:]
		skipOptions()
	case rest[0] == "node":
		rest = rest[1:]
		skipOptions()
	}
	if len(rest) == 0 {
		return
	}
	isPackage := func(t string) bool {
		base := path.Base(t)
		if strings.Trim(t, "/") == "" {
			base = ""
		}
		return codeForgeScoped.MatchString(t) || codeForgeBin.MatchString(base)
	}
	head, tail := rest[0], rest[1:]
	if isPackage(head) && len(tail) > 0 && isPackage(tail[0]) {
		tail = tail[1:]
	}
	name := commandName(head)
	if isPackage(head) {
		name = "code-forge"
	}
	if name != "code-forge" && name != "forge" {
		return
	}
	words := []string{}
	for _, t := range tail {
		if !strings.HasPrefix(t, "-") {
			words = append(words, t)
		}
	}
	for _, rule := range codeForgePrefixes {
		for _, verb := range rule.verbs {
			if len(words) >= len(verb) && slices.Equal(words[:len(verb)], verb) {
				ids[rule.id] = true
			}
		}
	}
}

// transcriptCommandIDs runs the git/gh/rm, production-marker and code-forge command rules on one
// line's command text.
func transcriptCommandIDs(line string) map[string]bool {
	tokens := transcriptTokens.FindAllString(line, -1)
	ids := map[string]bool{}
	if strings.Contains(line, "--env=production") {
		ids["production-marker"] = true
	}
	for _, flag := range []string{"no-require-reviews", "remove-worktree"} {
		if strings.Contains(line, "--"+flag) {
			ids["code-forge-"+flag] = true
		}
	}
	for i, token := range tokens {
		name := commandName(token)
		if slices.Contains([]string{"npx", "bunx", "pnpm", "yarn", "node", "code-forge", "forge"}, name) || strings.Contains(token, "code-forge") {
			codeForgeIDs(tokens[i:min(len(tokens), i+64)], ids)
		}
		if !slices.Contains([]string{"git", "gh", "rm"}, name) {
			continue
		}
		window := tokens[i:min(len(tokens), i+64)]
		readings := [][]string{append([]string{name}, window[1:]...)}
		if name == "git" {
			readings = gitReadings(window)
		}
		for _, words := range readings {
			commandRuleIDs(words, ids)
		}
	}
	return ids
}

var (
	gitGlobalWithValue = []string{"-C", "-c", "--git-dir", "--work-tree", "--namespace", "--config-env", "--attr-source", "--super-prefix", "--list-cmds"}
	gitGlobalNoValue   = []string{"-p", "-P", "--paginate", "--no-pager", "--bare", "--no-replace-objects", "--literal-pathspecs", "--glob-pathspecs",
		"--noglob-pathspecs", "--icase-pathspecs", "--no-optional-locks", "--no-lazy-fetch", "--no-advice", "--exec-path", "--html-path", "--man-path", "--info-path"}
	gitAttachedValue = regexp.MustCompile(`^-[Cc].`)
)

// gitReadings is Node's normalizedReadings for git: every plausible ["git", subcommand, ...]
// after the global options (an unknown option read both alone and with a value, at most 64).
func gitReadings(argv []string) [][]string {
	tails := [][]string{}
	var collect func(i int)
	collect = func(i int) {
		for i < len(argv) && len(tails) < 64 {
			token := argv[i]
			switch {
			case !strings.HasPrefix(token, "-"):
				tails = append(tails, argv[i:])
				return
			case token == "--" || strings.HasPrefix(token, "--") && strings.Contains(token, "=") || slices.Contains(gitGlobalNoValue, token):
				i++
			case slices.Contains(gitGlobalWithValue, token):
				i += 2
			case gitAttachedValue.MatchString(token):
				i++
			default:
				collect(i + 2)
				i++
			}
		}
	}
	collect(1)
	if len(tails) == 0 {
		return [][]string{{"git"}}
	}
	out := make([][]string, len(tails))
	for k, tail := range tails {
		out[k] = append([]string{"git"}, tail...)
	}
	return out
}

// commandRuleIDs is Node's prefix and commandFlag git/gh/rm entries on one normalized reading.
func commandRuleIDs(words []string, ids map[string]bool) {
	// rest: the tokens after the entry's command words.
	flags := func(rest []string, exact, prefix []string, short string) bool {
		for _, w := range rest {
			if slices.Contains(exact, w) {
				return true
			}
			for _, p := range prefix {
				if strings.HasPrefix(w, p) {
					return true
				}
			}
			if short != "" && len(w) > 1 && w[0] == '-' && w[1] != '-' && strings.ContainsAny(w[1:], short) {
				return true
			}
		}
		return false
	}
	switch words[0] {
	case "gh":
		if len(words) < 3 || words[1] != "pr" {
			return
		}
		if slices.Contains([]string{"ready", "merge", "close"}, words[2]) {
			ids["gh-pr-"+words[2]] = true
		}
		if words[2] == "edit" && flags(words[3:], []string{"--base", "-B"}, []string{"--base=", "-B"}, "") {
			ids["gh-pr-base-retarget"] = true
		}
	case "git":
		if len(words) < 2 {
			return
		}
		rest := words[2:]
		switch words[1] {
		case "push":
			if flags(rest, []string{"--force", "-f", "--force-with-lease", "--force-if-includes", "--mirror"}, []string{"--force-with-lease=", "--force-if-includes=", "+"}, "f") {
				ids["git-push-force"] = true
			}
		case "reset":
			if flags(rest, []string{"--hard"}, nil, "") {
				ids["git-reset-hard"] = true
			}
		case "checkout":
			if flags(rest, []string{"--", "--force"}, nil, "f") {
				ids["git-checkout-discard"] = true
			}
		case "restore", "clean", "stash", "rm":
			ids["git-"+words[1]] = true
		case "branch":
			if flags(rest, []string{"-D"}, nil, "D") {
				ids["git-branch-force-delete"] = true
			}
			if flags(rest, []string{"--delete"}, nil, "d") && flags(rest, []string{"--force"}, nil, "f") {
				ids["git-branch-force-delete-long"] = true
			}
		}
	case "rm":
		if flags(words[1:], []string{"--recursive", "--force"}, nil, "rRf") {
			ids["rm-rf"] = true
		}
	}
}

// runsMatcher is Node's code-forge-runs-access path rule (paths ['~/' + ".code-forge/runs"],
// access readwrite): pathPattern for argv tokens, namesHomeRelative for B415's words.
type runsMatcher struct {
	enabled            bool
	absolute, relative *regexp.Regexp
}

func newRunsMatcher(home string) runsMatcher {
	glob := regexp.QuoteMeta(".code-forge/runs")
	homes := strings.Join([]string{"~", `\$HOME`, `\$\{HOME\}`, regexp.QuoteMeta(home)}, "|")
	return runsMatcher{
		enabled:  true,
		absolute: regexp.MustCompile(`(?:^|[=:\s\x0b'"])(?:` + homes + `)/` + glob + `(?:/|$|[\s\x0b'";)])`),
		relative: regexp.MustCompile(`(?:^|[=:(\s\x0b'"])(?:\./)?` + glob + `(?:/|$|[\s\x0b'";)])`),
	}
}

// anyToken: a token of text (Node's transcript split) names the path.
func (m runsMatcher) anyToken(text string) bool {
	for _, t := range transcriptTokens.FindAllString(text, -1) {
		if m.absolute.MatchString(t) {
			return true
		}
	}
	return false
}

// anyRelative: a command word, redirect target or file-tool path uses the home-relative spelling.
func (m runsMatcher) anyRelative(words []string) bool {
	for _, w := range words {
		if m.relative.MatchString(w) {
			return true
		}
	}
	return false
}

var (
	transcriptEscapedQuote = regexp.MustCompile(`\\+(['"])`)
	transcriptEscapedSpace = regexp.MustCompile(`\\+[nrt]`)
)

// transcriptUnescaped is Node's plain-mode second variant of a line.
func transcriptUnescaped(line string) string {
	return transcriptEscapedSpace.ReplaceAllString(transcriptEscapedQuote.ReplaceAllString(line, "$1"), " ")
}

type claudeEvent struct {
	kind    string
	message json.RawMessage
}

var claudeLineTypes = []string{"user", "assistant", "system", "summary", "attachment", "api-request", "api-request-shape", "api-request-blob", "file-history-snapshot", "progress", "queue-operation"}

// claudeEvents is Node's Claude Code JSONL detection: every non-empty line an object with a known
// string type and a string sessionId, user/assistant lines with a message object, at least one of
// them and no stdin banner. Anything else is a plain-text log.
func claudeEvents(lines []string) ([]*claudeEvent, bool) {
	events := make([]*claudeEvent, len(lines))
	beforeJSON, banner, chat, objects, known := true, false, false, true, true
	first := ""
	seen := 0
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if beforeJSON && trimmed == "Reading additional input from stdin..." {
			banner = true
			continue
		}
		if !json.Valid([]byte(line)) {
			return nil, false
		}
		beforeJSON = false
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(line), &obj) != nil || obj == nil {
			objects = false
			continue
		}
		kind, isStr := jsonString(obj["type"])
		if !isStr {
			objects = false
			continue
		}
		if seen == 0 {
			first = kind
		}
		seen++
		if _, isStr := jsonString(obj["sessionId"]); !slices.Contains(claudeLineTypes, kind) || !isStr {
			known = false
		}
		if kind == "user" || kind == "assistant" {
			chat = true
			if !jsonIsObject(obj["message"]) {
				known = false
			}
		}
		events[i] = &claudeEvent{kind: kind, message: obj["message"]}
	}
	if !objects || first == "thread.started" || banner || !chat || !known {
		return nil, false
	}
	return events, true
}

// jsonString is a JSON string value (not null, a number or a missing key).
func jsonString(raw json.RawMessage) (string, bool) {
	var v string
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), `"`) || json.Unmarshal(raw, &v) != nil {
		return "", false
	}
	return v, true
}

func jsonIsObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return strings.HasPrefix(trimmed, "{")
}

type claudeLine struct {
	found         bool
	command, path string
	words         []string
}

var (
	claudeWriteTools = []string{"Write", "Edit", "MultiEdit", "NotebookEdit"}
	claudeFileTools  = []string{"Read", "Glob", "Grep", "LS", "NotebookRead"}
)

// claudeAssistantLine is one assistant line as Node's Claude mode reads it: per tool_use, the
// command text (Bash command; Write(<path>) for write tools; else the JSON input), the path text
// (B415 transcriptPathText for Bash) and the words where the home-relative spelling counts.
func claudeAssistantLine(message json.RawMessage) (claudeLine, bool) {
	// A lone \uD800-\uDFFF escape: Go decodes it to U+FFFD, JavaScript keeps it.
	// B583: a high-low escape pair is one code point in both.
	if jsonLoneSurrogate(message) {
		return claudeLine{}, false
	}
	var msg map[string]json.RawMessage
	if json.Unmarshal(message, &msg) != nil {
		return claudeLine{}, false
	}
	content := strings.TrimSpace(string(msg["content"]))
	if strings.HasPrefix(content, `"`) {
		return claudeLine{found: false}, true
	}
	type part struct {
		kind, name string
		input      json.RawMessage
	}
	parts := []part{}
	if strings.HasPrefix(content, "[") {
		var raw []json.RawMessage
		if json.Unmarshal(msg["content"], &raw) != nil {
			return claudeLine{}, false
		}
		for _, r := range raw {
			var p map[string]json.RawMessage
			if !jsonIsObject(r) || json.Unmarshal(r, &p) != nil {
				continue
			}
			kind, _ := jsonString(p["type"])
			if kind != "tool_use" {
				continue
			}
			name, _ := jsonString(p["name"])
			parts = append(parts, part{kind, name, p["input"]})
		}
	} else {
		parts = append(parts, part{"tool_use", "", message})
	}
	out := claudeLine{}
	commands, paths := []string{}, []string{}
	for _, p := range parts {
		text, ok := "", true
		if p.input != nil {
			text, ok = jsStringify(p.input)
			if !ok {
				return claudeLine{}, false
			}
		}
		command, pathText := text, text
		var input map[string]json.RawMessage
		if jsonIsObject(p.input) && json.Unmarshal(p.input, &input) != nil {
			return claudeLine{}, false
		}
		str := func(key string) (string, bool) { return jsonString(input[key]) }
		if cmd, isStr := str("command"); p.name == "Bash" && isStr {
			command = transcriptCommandText(cmd, true)
			var words []string
			pathText, words = transcriptPathText(cmd)
			out.words = append(out.words, words...)
		} else if slices.Contains(claudeWriteTools, p.name) {
			key := "file_path"
			if raw, has := input[key]; !has || strings.TrimSpace(string(raw)) == "null" {
				key = "notebook_path"
			}
			if file, isStr := str(key); isStr {
				var quoted strings.Builder
				if !jsQuote(&quoted, file) {
					return claudeLine{}, false
				}
				command = "Write(" + quoted.String() + ")"
				pathText = command
			}
		}
		if slices.Contains(claudeFileTools, p.name) || slices.Contains(claudeWriteTools, p.name) {
			keys := []string{"file_path", "notebook_path", "path"}
			if p.name == "Glob" {
				keys = append(keys, "pattern")
			}
			for _, key := range keys {
				if v, isStr := str(key); isStr {
					out.words = append(out.words, v)
				}
			}
		}
		commands = append(commands, command)
		paths = append(paths, pathText)
	}
	if len(commands) == 0 {
		return claudeLine{found: false}, true
	}
	out.found = true
	out.command = strings.Join(commands, "\n")
	out.path = strings.Join(paths, "\n")
	return out, true
}

var (
	jsIndexKey = regexp.MustCompile(`^(?:0|[1-9][0-9]*)$`)
	jsSafeInt  = regexp.MustCompile(`^-?(?:0|[1-9][0-9]{0,14})$`)
)

// jsStringify is JSON.stringify(JSON.parse(raw)) for the values Go can render the same way:
// ASCII strings, safe integers, no duplicate or array-index object keys (JS orders those first).
func jsStringify(raw json.RawMessage) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var b strings.Builder
	var walk func() bool
	walk = func() bool {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		switch v := tok.(type) {
		case json.Delim:
			if v == '[' {
				b.WriteByte('[')
				for n := 0; dec.More(); n++ {
					if n > 0 {
						b.WriteByte(',')
					}
					if !walk() {
						return false
					}
				}
				_, err := dec.Token()
				b.WriteByte(']')
				return err == nil
			}
			b.WriteByte('{')
			seen := map[string]bool{}
			for n := 0; dec.More(); n++ {
				keyTok, err := dec.Token()
				key, isKey := keyTok.(string)
				if err != nil || !isKey || seen[key] || jsIndexKey.MatchString(key) {
					return false
				}
				seen[key] = true
				if n > 0 {
					b.WriteByte(',')
				}
				if !jsQuote(&b, key) {
					return false
				}
				b.WriteByte(':')
				if !walk() {
					return false
				}
			}
			_, err := dec.Token()
			b.WriteByte('}')
			return err == nil
		case string:
			return jsQuote(&b, v)
		case json.Number:
			s := v.String()
			if !jsSafeInt.MatchString(s) || s == "-0" {
				f, err := strconv.ParseFloat(s, 64)
				if err != nil {
					return false
				}
				s = jsNumber(f)
			}
			b.WriteString(s)
		case bool:
			b.WriteString(strconv.FormatBool(v))
		case nil:
			b.WriteString("null")
		default:
			return false
		}
		return true
	}
	if !walk() {
		return "", false
	}
	return b.String(), true
}

// jsNumber is Number.prototype.toString for a finite JSON number: the shortest round-trip digits,
// fixed notation from 1e-6 up to 1e21, else d.ddde+n.
func jsNumber(f float64) string {
	if f == 0 {
		return "0"
	}
	if a := math.Abs(f); a >= 1e-6 && a < 1e21 {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	text := strconv.FormatFloat(f, 'e', -1, 64)
	mantissa, exp, _ := strings.Cut(text, "e")
	sign := exp[0]
	exp = strings.TrimLeft(exp[1:], "0")
	return mantissa + "e" + string(sign) + exp
}

func jsQuote(b *strings.Builder, s string) bool {
	if !utf8.ValidString(s) || !transcriptJSSafe(s) {
		return false
	}
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00` + strconv.FormatUint(uint64(c>>4), 16) + strconv.FormatUint(uint64(c&15), 16))
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
	return true
}

func shellSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}

var (
	shellAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*=`)
	shellFd     = regexp.MustCompile(`^\d+$|^-$`)
	shellPlans  = regexp.MustCompile(`(?:^|/)plans/`)
	shellFlag   = regexp.MustCompile(`^-[A-Za-z]+$`)
)

// transcriptPathText is Node's transcriptPathText (B415): the command text path rules scan, with
// text the command only writes to a file left out (a cat/tee heredoc body that cannot substitute,
// quoted echo/printf arguments into plans/), and the words where the home-relative spelling
// counts. Unknown syntax returns the whole command (fail closed).
func transcriptPathText(command string) (string, []string) {
	type word struct {
		text          string
		start, end    int
		quoted, subst bool
	}
	type cmdT struct {
		words           []word
		targets, inputs []string
		fdOnly, piped   bool
	}
	type span struct{ start, end int }
	type heredoc struct {
		delimiter     string
		tabs, expands bool
		cmd           *cmdT
	}
	whole := func() (string, []string) { return command, []string{command} }
	cmds := []*cmdT{{}}
	cmd := cmds[0]
	next := func() { cmd = &cmdT{}; cmds = append(cmds, cmd) }
	base := func(t string) string { return t[strings.LastIndex(t, "/")+1:] }
	firstWord := func(c *cmdT) int {
		for i, w := range c.words {
			if !shellAssign.MatchString(w.text) {
				return i
			}
		}
		return -1
	}
	name := func(c *cmdT) string {
		if i := firstWord(c); i >= 0 {
			return base(c.words[i].text)
		}
		return ""
	}
	substitutes := func(body string) bool {
		for k := 0; k < len(body); k++ {
			if body[k] == '\\' {
				k++
				continue
			}
			if body[k] == '`' || (body[k] == '$' && k+1 < len(body) && body[k+1] == '(') {
				return true
			}
		}
		return false
	}
	writesHeredoc := func(c *cmdT) bool {
		if c.piped {
			return false
		}
		switch name(c) {
		case "cat":
			return len(c.targets) > 0
		case "tee":
			for _, w := range c.words[firstWord(c)+1:] {
				if !strings.HasPrefix(w.text, "-") {
					return true
				}
			}
		}
		return false
	}
	inert := []span{}
	bodyWords := []string{}
	heredocs := []heredoc{}
	redirect := ""
	n := len(command)
	i := 0
	for i < n {
		ch := command[i]
		if ch == '\n' {
			if redirect != "" {
				return whole()
			}
			i++
			next()
			for _, doc := range heredocs {
				start, closed := i, false
				for i < n {
					end := n
					if nl := strings.IndexByte(command[i:], '\n'); nl >= 0 {
						end = i + nl
					}
					line := command[i:end]
					if doc.tabs {
						line = strings.TrimLeft(line, "\t")
					}
					if line == doc.delimiter {
						body := command[start:i]
						if writesHeredoc(doc.cmd) && !(doc.expands && substitutes(body)) {
							inert = append(inert, span{start, i})
						} else {
							bodyWords = append(bodyWords, strings.Fields(body)...)
						}
						i = min(end+1, n)
						if end == n {
							i = n
						}
						closed = true
						break
					}
					i = min(end+1, n)
					if end == n {
						i = n
					}
				}
				if !closed {
					return whole()
				}
			}
			heredocs = nil
			continue
		}
		if shellSpace(ch) {
			i++
			continue
		}
		if ch == '#' {
			if nl := strings.IndexByte(command[i:], '\n'); nl >= 0 {
				i += nl
			} else {
				i = n
			}
			continue
		}
		if strings.IndexByte(";|&()", ch) >= 0 {
			if redirect != "" {
				return whole()
			}
			if ch == '|' && (i+1 >= n || command[i+1] != '|') {
				cmd.piped = true
			}
			if (ch == '|' || ch == '&') && i+1 < n && command[i+1] == ch {
				i += 2
			} else {
				i++
			}
			next()
			continue
		}
		if ch == '<' || ch == '>' {
			if redirect != "" {
				return whole()
			}
			redirect = string(ch)
			i++
			if i < n && command[i] == ch {
				redirect += string(ch)
				i++
			}
			if redirect == "<<" && i < n && command[i] == '-' {
				redirect += "-"
				i++
			}
			if strings.HasPrefix(redirect, ">") && i < n && command[i] == '&' {
				redirect += "&"
				i++
			}
			if i < n && command[i] == '<' {
				return whole()
			}
			continue
		}
		start := i
		var parts strings.Builder
		quotes := 0
		unquoted, escaped, subst := false, false, false
		for i < n && !shellSpace(command[i]) && strings.IndexByte(";|&()<>", command[i]) < 0 {
			c := command[i]
			i++
			if c == '\'' || c == '"' {
				quotes++
				closed := false
				for i < n {
					inside := command[i]
					i++
					if inside == c {
						closed = true
						break
					}
					if c == '"' && (inside == '`' || (inside == '$' && i < n && command[i] == '(')) {
						subst = true
					}
					if inside == '\\' && c == '"' && i < n {
						parts.WriteByte(command[i])
						i++
					} else {
						parts.WriteByte(inside)
					}
				}
				if !closed {
					return whole()
				}
			} else {
				unquoted = true
				if c == '`' || (c == '$' && i < n && command[i] == '(') {
					subst = true
				}
				if c == '\\' {
					escaped = true
				}
				if c == '\\' && i < n {
					parts.WriteByte(command[i])
					i++
				} else {
					parts.WriteByte(c)
				}
			}
		}
		text := parts.String()
		if redirect != "" {
			if strings.HasPrefix(redirect, "<<") {
				heredocs = append(heredocs, heredoc{delimiter: text, tabs: redirect == "<<-", expands: quotes == 0 && !escaped, cmd: cmd})
			} else if redirect == ">" || redirect == ">>" {
				cmd.targets = append(cmd.targets, text)
			} else if strings.HasSuffix(redirect, "&") {
				cmd.fdOnly = cmd.fdOnly || !shellFd.MatchString(text)
			}
			if !strings.HasPrefix(redirect, "<<") {
				cmd.inputs = append(cmd.inputs, text)
			}
			redirect = ""
			continue
		}
		cmd.words = append(cmd.words, word{text: text, start: start, end: i, quoted: quotes > 0 && !unquoted, subst: subst})
	}
	if redirect != "" || len(heredocs) > 0 {
		return whole()
	}
	for _, c := range cmds {
		nm := name(c)
		if (nm != "echo" && nm != "printf") || c.piped || c.fdOnly || len(c.targets) == 0 {
			continue
		}
		underPlans := true
		for _, t := range c.targets {
			if !shellPlans.MatchString(t) || slices.Contains(strings.Split(t, "/"), "..") {
				underPlans = false
			}
		}
		if !underPlans {
			continue
		}
		at := -1
		for k, w := range c.words {
			if base(w.text) == nm {
				at = k
				break
			}
		}
		args := c.words[at+1:]
		plain := true
		for _, w := range args {
			if !((w.quoted && !w.subst) || shellFlag.MatchString(w.text)) {
				plain = false
			}
		}
		if !plain {
			continue
		}
		for _, w := range args {
			if w.quoted {
				inert = append(inert, span{w.start, w.end})
			}
		}
	}
	masked := func(w word) bool {
		for _, s := range inert {
			if s.start == w.start {
				return true
			}
		}
		return false
	}
	words := []string{}
	for _, c := range cmds {
		for _, w := range c.words {
			if !masked(w) {
				words = append(words, w.text)
			}
		}
		words = append(words, c.inputs...)
	}
	words = append(words, bodyWords...)
	if len(inert) == 0 {
		return command, words
	}
	sort.SliceStable(inert, func(a, b int) bool { return inert[a].start < inert[b].start })
	var out strings.Builder
	end := 0
	for _, s := range inert {
		if s.start > end {
			out.WriteString(command[end:s.start])
		}
		out.WriteByte(' ')
		end = s.end
	}
	out.WriteString(command[end:])
	return out.String(), words
}

// Rule is one forbidden rule the block close's transcript scan enforces for a coder (B364).
type Rule struct{ ID, Description string }

// CoderRules is the coder rule table the context pack lists: mergeForbidden() in
// src/util/forbidden.mjs (FORBIDDEN, then CODER_ONLY_FORBIDDEN), same ids, descriptions and
// order. test/compat/parity-context-pack.test.mjs keeps the Go pack's lines equal to Node's.
var CoderRules = []Rule{
	{"gh-pr-ready", "Mark a PR ready for review"},
	{"gh-pr-merge", "Merge a PR"},
	{"gh-pr-close", "Close a PR"},
	{"gh-pr-base-retarget", "Retarget a PR base branch (--base / -B / --base=, anywhere after `gh pr edit`)"},
	{"git-push-force", "Force-push (rewrites remote history): --force/-f (also inside a cluster like -uf), --force-with-lease[=…], --force-if-includes, --mirror, or a +refspec"},
	{"git-reset-hard", "Discard local changes (--hard anywhere after `git reset`)"},
	{"git-checkout-discard", "Discard working-tree changes (`git checkout [<ref>] -- <path>`, or a forced checkout)"},
	{"git-restore", "Discard working-tree changes to a path (the modern replacement for checkout --)"},
	{"git-clean", "Delete untracked files"},
	{"git-stash", "Stash working-tree state"},
	{"git-branch-force-delete", "Force-delete a branch (short form -D, also inside a cluster)"},
	{"git-branch-force-delete-long", "Force-delete a branch (--delete/-d together with --force/-f, any order)"},
	{"git-rm", "Delete a tracked file via git — includes deleting a test file"},
	{"rm-rf", "rm with ANY recursive or force flag (-r, -R, -f, any cluster containing one, --recursive, --force) — a coder never legitimately needs either; the package cleans .code-forge/ itself"},
	{"production-marker", "Any argv token CONTAINING a configured production.markers/production.names value (env flag, DB name, prod host, deploy tool) — this entry is a placeholder; mergeForbidden() adds the real, project-configured tokens"},
	{"code-forge-runs-access", "Any read or write of ~/.code-forge/runs/ (the run record pin and the per-run HMAC key) — a coder never needs it; reading the key is a named rule-break, not a boundary (§8.6)"},
	{"code-forge-worker-from-coder", "Start a worker or a run, or reload its config (forge worker / code-forge worker / run start / run reload) from a coder — the orchestrator only"},
	{"code-forge-reviews-write", "Any write under .code-forge/reviews/ or to .code-forge/queue/*.done — only the worker writes review results"},
	{"code-forge-block-waive-from-coder", "Waive a review finding (forge block waive) — human-only, typed by the human in their own terminal (§4.9, B80)"},
	{"code-forge-block-reopen-from-coder", "Reopen the review of a stopped or lost file (forge block reopen) — human-only, typed by the human in their own terminal (B72, B80)"},
	{"code-forge-autopilot-from-coder", "Any autopilot command (forge autopilot start / status / stop / …) — the owner grants, reads and ends autopilot, never a coder"},
	{"code-forge-no-require-reviews", "Close a block without the per-file review check (--no-require-reviews) — human-only"},
	{"code-forge-remove-worktree", "Remove a block's worktree at close (block close --remove-worktree, B62a) — the orchestrator only"},
	{"code-forge-block-accept-hit-from-coder", "Accept a false transcript hit — human-only, confirmed in the human terminal (B127)"},
	{"code-forge-block-unwaive-from-coder", "Undo agent waivers — human-only, confirmed in the human terminal (B124b)"},
	{"code-forge-block-ack-waivers-from-coder", "Acknowledge agent waivers — human-only, confirmed in the human terminal (B124b)"},
	{"code-forge-block-human-pass-from-coder", "Pass a human device check — human-only, confirmed in the human terminal (B137)"},
	{"code-forge-add-worker-from-coder", "Add a review worker to a run — the orchestrator only (B107)"},
	{"code-forge-block-amend-acceptance-from-coder", "Correct a block acceptance list — human-only, confirmed in the human terminal (B143)"},
}

var (
	shellWrapperRe   = regexp.MustCompile(`^[\s\x0b]*(?:[^\s\x0b'"\;|&()<>` + "`" + `$]+/)?(?:sh|bash|zsh|dash|ksh)(?:[ \t]+-[li]+)*[ \t]+-[li]*c[ \t]+(['"])`)
	spawnWordRe      = regexp.MustCompile(`\b(?:exec|execSync|execFile|execFileSync|spawn|spawnSync|fork|system|popen|subprocess|child_process|Popen)\b`)
	interpreterRe    = regexp.MustCompile(`^(node|python|python3|ruby|perl)(?:\d+(?:\.\d+)*)?$`)
	rubyPerlSpawnRe  = regexp.MustCompile("`|\\$\\(|%x")
	perlQxRe         = regexp.MustCompile(`\bqx\b`)
	dynamicLookupRe  = regexp.MustCompile(`\b(?:getattr|__import__|importlib|eval|exec|compile|globals|locals|vars|__builtins__|operator[\s\x0b]*\.[\s\x0b]*attrgetter|setattr)\b`)
	pyStringPrefixRe = regexp.MustCompile(`^(?i:r|u|b|br|rb)$`)
	envAssignRe      = regexp.MustCompile(`^[A-Za-z_][A-Za-z_0-9]*=`)
)

func wordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// pythonSpawnText is Node's pythonSpawnText: literal JSON data passed to json.loads is masked
// so its spawn words count as data, unless dynamic lookup could use it.
func pythonSpawnText(body string) string {
	var chunks strings.Builder
	end, i := 0, 0
	for i < len(body) {
		if body[i] == '#' {
			nl := strings.IndexByte(body[i:], '\n')
			if nl < 0 {
				i = len(body)
			} else {
				i += nl + 1
			}
			continue
		}
		jsonArgument := false
		if strings.HasPrefix(body[i:], "json.loads(") && (i == 0 || !(wordByte(body[i-1]) || body[i-1] == '.')) {
			i += len("json.loads(")
			for i < len(body) && shellSpace(body[i]) {
				i++
			}
			jsonArgument = true
		}
		if i >= len(body) || body[i] != '"' && body[i] != '\'' {
			i++
			continue
		}
		quote := body[i]
		start := i
		prefixStart := start
		for prefixStart > 0 && wordByte(body[prefixStart-1]) {
			prefixStart--
		}
		prefix := body[prefixStart:start]
		if prefix != "" && !pyStringPrefixRe.MatchString(prefix) {
			return body
		}
		delimiter := string(quote)
		if strings.HasPrefix(body[i:], strings.Repeat(delimiter, 3)) {
			delimiter = strings.Repeat(delimiter, 3)
		}
		i += len(delimiter)
		closed := false
		for i < len(body) {
			if strings.HasPrefix(body[i:], delimiter) {
				i += len(delimiter)
				closed = true
				break
			}
			if len(delimiter) == 1 && (body[i] == '\r' || body[i] == '\n') {
				return body
			}
			if body[i] == '\\' {
				i += 2
			} else {
				i++
			}
		}
		if !closed {
			return body
		}
		if !jsonArgument || prefix != "" || delimiter != `"` {
			continue
		}
		next := i
		for next < len(body) && shellSpace(body[next]) {
			next++
		}
		if next >= len(body) || body[next] != ')' {
			continue
		}
		var inner string
		if i > len(body) || json.Unmarshal([]byte(body[start:i]), &inner) != nil || !json.Valid([]byte(inner)) {
			continue
		}
		chunks.WriteString(body[end:start])
		chunks.WriteByte(' ')
		end = i
	}
	if end < len(body) {
		chunks.WriteString(body[end:])
	}
	masked := chunks.String()
	if dynamicLookupRe.MatchString(masked) {
		return body
	}
	return masked
}

func hasShellSubstitution(body string) bool {
	for i := 0; i < len(body); i++ {
		if body[i] == '\\' {
			i++
			continue
		}
		if body[i] == '`' || body[i] == '$' && i+1 < len(body) && body[i+1] == '(' {
			return true
		}
	}
	return false
}

func hasSpawn(body, language string, shellExpands bool) bool {
	text := body
	if language == "python" || language == "python3" {
		text = pythonSpawnText(body)
	}
	return spawnWordRe.MatchString(text) || shellExpands && hasShellSubstitution(body) ||
		(language == "ruby" || language == "perl") && rubyPerlSpawnRe.MatchString(body) ||
		language == "perl" && perlQxRe.MatchString(body)
}

func shellBreak(c byte) bool {
	return shellSpace(c) || strings.IndexByte(";|&()<>", c) >= 0
}

// transcriptCommandText is Node's transcriptCommandText: one sh/bash -c wrapper decoded, and
// node/python/ruby/perl heredoc bodies and quoted eval bodies without a spawn word blanked, for
// the command and contains rules only. Unknown or unfinished syntax returns the command whole.
func transcriptCommandText(command string, unwrapShell bool) string {
	if unwrapShell {
		if m := shellWrapperRe.FindStringSubmatchIndex(command); m != nil {
			open := command[m[2]]
			quote := open
			var parts strings.Builder
			i := m[1]
			for i < len(command) {
				char := command[i]
				i++
				if char == quote {
					other := byte('"')
					if quote == '"' {
						other = '\''
					}
					if i < len(command) && command[i] == other {
						quote = command[i]
						i++
						continue
					}
					if quote != open || strings.TrimSpace(command[i:]) != "" {
						return command
					}
					return command[:m[1]-1] + transcriptCommandText(parts.String(), false)
				}
				if quote == '"' && char == '\\' && i < len(command) && strings.IndexByte("\"\\$`\n", command[i]) >= 0 {
					escaped := command[i]
					i++
					if escaped != '\n' {
						parts.WriteByte(escaped)
					}
				} else {
					if quote == '"' && (char == '`' || char == '$' && i < len(command) && command[i] == '(') {
						return command
					}
					parts.WriteByte(char)
				}
			}
			return command
		}
	}
	type span struct{ start, end int }
	type heredoc struct {
		delimiter, language string
		tabs, shellExpands  bool
	}
	evalFlags := []string{"-e", "-c", "--eval", "-p", "--print"}
	inert := []span{}
	heredocs := []heredoc{}
	state, language, redirect := "command", "", ""
	evalNext, envValue := false, false
	i := 0
	for i < len(command) {
		char := command[i]
		if char == '\n' {
			i++
			for _, doc := range heredocs {
				start := i
				closed := false
				for i < len(command) {
					nl := strings.IndexByte(command[i:], '\n')
					end := len(command)
					if nl >= 0 {
						end = i + nl
					}
					line := command[i:end]
					if doc.tabs {
						line = strings.TrimLeft(line, "\t")
					}
					if line == doc.delimiter {
						if doc.language != "" && !hasSpawn(command[start:i], doc.language, doc.shellExpands) {
							inert = append(inert, span{start, i})
						}
						if nl < 0 {
							i = end
						} else {
							i = end + 1
						}
						closed = true
						break
					}
					if nl < 0 {
						i = end
					} else {
						i = end + 1
					}
				}
				if !closed {
					return command
				}
			}
			heredocs = heredocs[:0]
			state, evalNext, redirect = "command", false, ""
			continue
		}
		if shellSpace(char) {
			i++
			continue
		}
		if char == '#' {
			nl := strings.IndexByte(command[i:], '\n')
			if nl < 0 {
				i = len(command)
			} else {
				i += nl
			}
			continue
		}
		if strings.IndexByte(";|&()", char) >= 0 {
			state, evalNext = "command", false
			i++
			continue
		}
		if char == '<' || char == '>' {
			redirect = string(char)
			i++
			if i < len(command) && command[i] == char {
				redirect += string(char)
				i++
			}
			if redirect == "<<" && i < len(command) && command[i] == '-' {
				redirect += "-"
				i++
			}
			if i < len(command) && command[i] == '<' {
				return command
			}
			continue
		}
		start := i
		var parts strings.Builder
		quotes, quote := 0, byte(0)
		escaped, unquoted := false, false
		for i < len(command) && !shellBreak(command[i]) {
			next := command[i]
			i++
			if next == '\'' || next == '"' {
				quotes++
				quote = next
				closed := false
				for i < len(command) {
					inside := command[i]
					i++
					if inside == next {
						closed = true
						break
					}
					if inside == '\\' && next == '"' && i < len(command) {
						parts.WriteByte(command[i])
						i++
					} else {
						parts.WriteByte(inside)
					}
				}
				if !closed {
					return command
				}
			} else {
				unquoted = true
				if next == '\\' {
					escaped = true
				}
				if next == '\\' && i < len(command) {
					parts.WriteByte(command[i])
					i++
				} else {
					parts.WriteByte(next)
				}
			}
		}
		word := parts.String()
		if redirect != "" {
			if strings.HasPrefix(redirect, "<<") {
				lang := ""
				if state == "interpreter" {
					lang = language
				}
				heredocs = append(heredocs, heredoc{delimiter: word, tabs: redirect == "<<-", language: lang, shellExpands: quotes == 0 && !escaped})
			}
			redirect = ""
			continue
		}
		if state == "env" {
			if envValue {
				envValue = false
				continue
			}
			if envAssignRe.MatchString(word) || word == "-i" || word == "--ignore-environment" {
				continue
			}
			if word == "-u" || word == "--unset" || word == "-C" || word == "--chdir" {
				envValue = true
				continue
			}
			if word == "--" {
				state = "command"
				continue
			}
			if strings.HasPrefix(word, "-") {
				state = "other"
				continue
			}
			state = "command"
		}
		if state == "command" {
			if envAssignRe.MatchString(word) {
				continue
			}
			name := word[strings.LastIndexByte(word, '/')+1:]
			language = ""
			if m := interpreterRe.FindStringSubmatch(name); m != nil {
				language = m[1]
			}
			switch {
			case name == "env":
				state = "env"
			case language != "":
				state = "interpreter"
			default:
				state = "other"
			}
		} else if state == "interpreter" {
			if evalNext {
				if quotes == 1 && !unquoted && !hasSpawn(command[start:i], language, quote == '"') {
					inert = append(inert, span{start, i})
				}
				evalNext = false
			} else if slices.Contains(evalFlags, word) {
				evalNext = true
			} else if !strings.HasPrefix(word, "-") || word == "--" {
				state = "other"
			}
		}
	}
	if len(heredocs) > 0 {
		return command
	}
	var out strings.Builder
	end := 0
	for _, s := range inert {
		out.WriteString(command[end:s.start])
		out.WriteByte(' ')
		end = s.end
	}
	out.WriteString(command[end:])
	return out.String()
}

// jsonLoneSurrogate is true when raw JSON text has a \u escape of a UTF-16
// surrogate that is not a high surrogate escape directly followed by a low
// one (JSON.parse keeps it as a lone code unit; Go's decoder writes U+FFFD).
func jsonLoneSurrogate(raw []byte) bool {
	unit := func(i int) (int, bool) {
		if i+6 > len(raw) || raw[i] != '\\' || raw[i+1] != 'u' {
			return 0, false
		}
		n, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
		return int(n), err == nil
	}
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		u, ok := unit(i)
		if !ok {
			i++ // a two-character escape such as \\ or \"
			continue
		}
		switch {
		case u >= 0xdc00 && u <= 0xdfff:
			return true
		case u >= 0xd800 && u <= 0xdbff:
			low, ok := unit(i + 6)
			if !ok || low < 0xdc00 || low > 0xdfff {
				return true
			}
			i += 11
		default:
			i += 5
		}
	}
	return false
}
