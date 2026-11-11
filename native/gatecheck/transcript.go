package gatecheck

import (
	"encoding/json"
	"os"
	"path"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

var transcriptTokens = regexp.MustCompile(">>?|[^\\s\\x0b'\"`;|&(),{}\\[\\]>]+")

// Transcript handles the plain-text grep and Claude Code JSONL logs. Codex logs, code-forge
// launcher spellings, the .code-forge/reviews and .code-forge/queue paths, shell wrappers and
// interpreter scripts stay with Node's full interpreter-aware scanner. code-forge-runs-access
// is checked here with Node's rules after B415 (B416).
// Returning unsupported happens before any close warning is published.
func Transcript(text string) ([]Blocker, bool) {
	for _, needle := range []string{"thread.started", "--no-require-reviews", "--remove-worktree", ".code-forge/reviews", ".code-forge/queue"} {
		if strings.Contains(text, needle) {
			return nil, false
		}
	}
	// "forge" may appear only inside ".code-forge": no launcher or forge verb can match then.
	for i := strings.Index(text, "forge"); i >= 0; {
		if i < 6 || text[i-6:i] != ".code-" {
			return nil, false
		}
		next := strings.Index(text[i+5:], "forge")
		if next < 0 {
			break
		}
		i += 5 + next
	}
	if !transcriptASCII(text) {
		return nil, false
	}
	runs := runsMatcher{}
	if strings.Contains(text, ".code-forge") {
		home := os.Getenv("HOME")
		if home == "" {
			return nil, false
		}
		runs = newRunsMatcher(home)
	}
	lines := strings.Split(text, "\n")
	events, claude := claudeEvents(lines)
	hits := []Blocker{}
	for lineNo, line := range lines {
		command, runsHit := line, false
		if claude {
			ev := events[lineNo]
			if ev == nil || ev.kind != "assistant" {
				continue
			}
			l, ok := claudeAssistantLine(ev.message)
			if !ok {
				return nil, false
			}
			if !l.found {
				continue
			}
			command = l.command
			runsHit = runs.enabled && (runs.anyToken(l.path) || runs.anyRelative(l.words))
		} else if runs.enabled {
			runsHit = runs.anyToken(line)
			if variant := transcriptUnescaped(line); variant != line {
				runsHit = runsHit || runs.anyToken(variant)
			}
		}
		ids, ok := transcriptCommandIDs(command)
		if !ok {
			return nil, false
		}
		if runsHit {
			ids["code-forge-runs-access"] = true
		}
		for _, id := range transcriptOrder {
			if ids[id] {
				hits = append(hits, Blocker{Code: "rule_break", Detail: "transcript line " + strconv.Itoa(lineNo+1) + ": " + id})
			}
		}
	}
	return hits, true
}

var transcriptOrder = []string{"gh-pr-ready", "gh-pr-merge", "gh-pr-close", "gh-pr-base-retarget", "git-push-force", "git-reset-hard", "git-checkout-discard", "git-restore", "git-clean", "git-stash", "git-branch-force-delete", "git-branch-force-delete-long", "git-rm", "rm-rf", "production-marker", "code-forge-runs-access"}

func transcriptASCII(text string) bool {
	for i := 0; i < len(text); i++ {
		if text[i] > 127 {
			return false
		}
	}
	return true
}

// transcriptCommandIDs runs the git/gh/rm and production-marker rules on one line's command text.
func transcriptCommandIDs(line string) (map[string]bool, bool) {
	tokens := transcriptTokens.FindAllString(line, -1)
	ids := map[string]bool{}
	if strings.Contains(line, "--env=production") {
		ids["production-marker"] = true
	}
	for i, token := range tokens {
		name := path.Base(token)
		if strings.HasSuffix(strings.ToLower(name), ".exe") {
			name = name[:len(name)-4]
		}
		if !slices.Contains([]string{"git", "gh", "rm"}, name) {
			continue
		}
		if token != name {
			return nil, false
		}
		end := min(len(tokens), i+64)
		words := tokens[i:end]
		if len(words) < 2 {
			continue
		}
		if strings.HasPrefix(words[1], "-") && name != "rm" {
			return nil, false
		}
		flags := func(exact, prefix []string, short string) bool {
			for _, w := range words[1:] {
				if slices.Contains(exact, w) {
					return true
				}
				for _, p := range prefix {
					if strings.HasPrefix(w, p) {
						return true
					}
				}
				if strings.HasPrefix(w, "-") && !strings.HasPrefix(w, "--") && strings.ContainsAny(w[1:], short) {
					return true
				}
			}
			return false
		}
		switch name {
		case "gh":
			if words[1] != "pr" || len(words) < 3 {
				continue
			}
			if strings.HasPrefix(words[2], "-") {
				return nil, false
			}
			if slices.Contains([]string{"ready", "merge", "close"}, words[2]) {
				ids["gh-pr-"+words[2]] = true
			}
			if words[2] == "edit" && flags([]string{"--base", "-B"}, []string{"--base=", "-B"}, "") {
				ids["gh-pr-base-retarget"] = true
			}
		case "git":
			switch words[1] {
			case "push":
				if flags([]string{"--force", "-f", "--force-with-lease", "--force-if-includes", "--mirror"}, []string{"--force-with-lease=", "--force-if-includes=", "+"}, "f") {
					ids["git-push-force"] = true
				}
			case "reset":
				if flags([]string{"--hard"}, nil, "") {
					ids["git-reset-hard"] = true
				}
			case "checkout":
				if flags([]string{"--", "--force"}, nil, "f") {
					ids["git-checkout-discard"] = true
				}
			case "restore", "clean", "stash", "rm":
				ids["git-"+words[1]] = true
			case "branch":
				if flags([]string{"-D"}, nil, "D") {
					ids["git-branch-force-delete"] = true
				}
				if flags([]string{"--delete"}, nil, "d") && flags([]string{"--force"}, nil, "f") {
					ids["git-branch-force-delete-long"] = true
				}
			}
		case "rm":
			if flags([]string{"--recursive", "--force"}, nil, "rRf") {
				ids["rm-rf"] = true
			}
		}
	}
	return ids, true
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
	// Node's transcriptCommandText unwraps one sh -c script and blanks interpreter scripts; a
	// command it may change goes to Node, so the command rules here read the command as given.
	claudeShellWrapper = regexp.MustCompile(`^[\s\x0b]*(?:[^\s\x0b'"\;|&()<>` + "`" + `$]+/)?(?:sh|bash|zsh|dash|ksh)(?:[ \t]+-[li]+)*[ \t]+-[li]*c[ \t]+['"]`)
	claudeInterpreter  = regexp.MustCompile(`node|python|ruby|perl`)
)

// claudeAssistantLine is one assistant line as Node's Claude mode reads it: per tool_use, the
// command text (Bash command; Write(<path>) for write tools; else the JSON input), the path text
// (B415 transcriptPathText for Bash) and the words where the home-relative spelling counts.
func claudeAssistantLine(message json.RawMessage) (claudeLine, bool) {
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
			if !transcriptASCII(cmd) || claudeShellWrapper.MatchString(cmd) || claudeInterpreter.MatchString(cmd) {
				return claudeLine{}, false
			}
			command = cmd
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
					if !transcriptASCII(v) {
						return claudeLine{}, false
					}
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
				return false
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

func jsQuote(b *strings.Builder, s string) bool {
	if !transcriptASCII(s) {
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
