package session

// B523: src/util/forbidden.mjs for the doctor probe sessions: the static
// list (FORBIDDEN + CODER_ONLY_FORBIDDEN, i.e. mergeForbidden() with no extra
// production tokens), its structural matcher isForbidden and the Claude /
// Codex renderers. The transcript scanner stays in Node.

import (
	"os"
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

type forbidGroup struct{ anyOf, anyPrefixOf, shortLetters []string }

// forbidEntry is forbidden.mjs ForbiddenEntry (description left out: no
// caller here prints it).
type forbidEntry struct {
	id, kind        string
	tokens, command []string
	groups          []forbidGroup
	prefixes        [][]string
	paths           []string
	access          string
}

func forbidPrefixes(words ...string) [][]string {
	var out [][]string
	for _, w := range words {
		parts := strings.Fields(w)
		out = append(out, append([]string{"code-forge"}, parts...), append([]string{"forge"}, parts...))
	}
	return out
}

// forbiddenStatic is FORBIDDEN, in list order.
var forbiddenStatic = []forbidEntry{
	{id: "gh-pr-ready", kind: "prefix", tokens: []string{"gh", "pr", "ready"}},
	{id: "gh-pr-merge", kind: "prefix", tokens: []string{"gh", "pr", "merge"}},
	{id: "gh-pr-close", kind: "prefix", tokens: []string{"gh", "pr", "close"}},
	{id: "gh-pr-base-retarget", kind: "commandFlag", command: []string{"gh", "pr", "edit"},
		groups: []forbidGroup{{anyOf: []string{"--base", "-B"}, anyPrefixOf: []string{"--base=", "-B"}}}},
	{id: "git-push-force", kind: "commandFlag", command: []string{"git", "push"},
		groups: []forbidGroup{{anyOf: []string{"--force", "-f", "--force-with-lease", "--force-if-includes", "--mirror"},
			anyPrefixOf: []string{"--force-with-lease=", "--force-if-includes=", "+"}, shortLetters: []string{"f"}}}},
	{id: "git-reset-hard", kind: "commandFlag", command: []string{"git", "reset"}, groups: []forbidGroup{{anyOf: []string{"--hard"}}}},
	{id: "git-checkout-discard", kind: "commandFlag", command: []string{"git", "checkout"},
		groups: []forbidGroup{{anyOf: []string{"--", "--force"}, shortLetters: []string{"f"}}}},
	{id: "git-restore", kind: "prefix", tokens: []string{"git", "restore"}},
	{id: "git-clean", kind: "prefix", tokens: []string{"git", "clean"}},
	{id: "git-stash", kind: "prefix", tokens: []string{"git", "stash"}},
	{id: "git-branch-force-delete", kind: "commandFlag", command: []string{"git", "branch"},
		groups: []forbidGroup{{anyOf: []string{"-D"}, shortLetters: []string{"D"}}}},
	{id: "git-branch-force-delete-long", kind: "commandFlag", command: []string{"git", "branch"},
		groups: []forbidGroup{{anyOf: []string{"--delete"}, shortLetters: []string{"d"}}, {anyOf: []string{"--force"}, shortLetters: []string{"f"}}}},
	{id: "git-rm", kind: "prefix", tokens: []string{"git", "rm"}},
	{id: "rm-rf", kind: "commandFlag", command: []string{"rm"},
		groups: []forbidGroup{{anyOf: []string{"--recursive", "--force"}, shortLetters: []string{"r", "R", "f"}}}},
	{id: "production-marker", kind: "contains", tokens: []string{"--env=production"}},
	{id: "code-forge-runs-access", kind: "path", paths: []string{"~/.code-forge/runs"}, access: "readwrite"},
	{id: "code-forge-worker-from-coder", kind: "anyPrefix", prefixes: [][]string{
		{"code-forge", "worker"}, {"forge", "worker"}, {"code-forge", "run", "start"}, {"forge", "run", "start"},
		{"code-forge", "run", "reload"}, {"forge", "run", "reload"}}},
	{id: "code-forge-reviews-write", kind: "path", paths: []string{".code-forge/reviews", ".code-forge/queue/*.done"}, access: "write"},
}

// forbiddenCoderOnly is CODER_ONLY_FORBIDDEN, in list order.
var forbiddenCoderOnly = []forbidEntry{
	{id: "code-forge-block-waive-from-coder", kind: "anyPrefix", prefixes: forbidPrefixes("block waive")},
	{id: "code-forge-block-reopen-from-coder", kind: "anyPrefix", prefixes: forbidPrefixes("block reopen")},
	{id: "code-forge-autopilot-from-coder", kind: "anyPrefix", prefixes: forbidPrefixes("autopilot")},
	{id: "code-forge-no-require-reviews", kind: "contains", tokens: []string{"--no-require-reviews"}},
	{id: "code-forge-remove-worktree", kind: "contains", tokens: []string{"--remove-worktree"}},
	{id: "code-forge-block-accept-hit-from-coder", kind: "anyPrefix", prefixes: forbidPrefixes("block accept-hit")},
	{id: "code-forge-block-unwaive-from-coder", kind: "anyPrefix", prefixes: forbidPrefixes("block unwaive")},
	{id: "code-forge-block-ack-waivers-from-coder", kind: "anyPrefix", prefixes: forbidPrefixes("block ack-waivers")},
	{id: "code-forge-block-human-pass-from-coder", kind: "anyPrefix", prefixes: forbidPrefixes("block human-pass")},
	{id: "code-forge-add-worker-from-coder", kind: "commandFlag", command: []string{"code-forge", "run", "start"},
		groups: []forbidGroup{{anyOf: []string{"--add-worker"}}}},
	{id: "code-forge-block-amend-acceptance-from-coder", kind: "anyPrefix", prefixes: forbidPrefixes("block amend-acceptance")},
}

// forbiddenMerged is mergeForbidden(): FORBIDDEN then the coder-only entries.
func forbiddenMerged() []forbidEntry {
	return append(append([]forbidEntry{}, forbiddenStatic...), forbiddenCoderOnly...)
}

var gitGlobalWithValue = map[string]bool{"-C": true, "-c": true, "--git-dir": true, "--work-tree": true, "--namespace": true,
	"--config-env": true, "--attr-source": true, "--super-prefix": true, "--list-cmds": true}

var gitGlobalNoValue = map[string]bool{"-p": true, "-P": true, "--paginate": true, "--no-pager": true, "--bare": true,
	"--no-replace-objects": true, "--literal-pathspecs": true, "--glob-pathspecs": true, "--noglob-pathspecs": true,
	"--icase-pathspecs": true, "--no-optional-locks": true, "--no-lazy-fetch": true, "--no-advice": true, "--exec-path": true,
	"--html-path": true, "--man-path": true, "--info-path": true}

const forbidMaxGitReadings = 64

// forbidBasename is Node's path.basename (POSIX): trailing slashes dropped, "" for "/".
func forbidBasename(p string) string {
	end := len(p)
	for end > 0 && p[end-1] == '/' {
		end--
	}
	p = p[:end]
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

var exeSuffix = regexp.MustCompile(`(?i)\.exe$`)

// forbidCommandName is commandName: the basename minus a Windows .exe suffix.
func forbidCommandName(token string) string {
	return exeSuffix.ReplaceAllString(forbidBasename(token), "")
}

var attachedGitValue = regexp.MustCompile(`^-[Cc]` + jsDot)

func collectGitTails(argv []string, i int, out *[][]string) {
	for i < len(argv) && len(*out) < forbidMaxGitReadings {
		token := argv[i]
		if !strings.HasPrefix(token, "-") {
			*out = append(*out, argv[i:])
			return
		}
		switch {
		case token == "--" || (strings.HasPrefix(token, "--") && strings.Contains(token, "=")) || gitGlobalNoValue[token]:
			i++
		case gitGlobalWithValue[token]:
			i += 2
		case attachedGitValue.MatchString(token):
			i++
		default:
			collectGitTails(argv, i+2, out)
			i++
		}
	}
}

func normalizedReadings(argv []string) [][]string {
	name := forbidCommandName(argv[0])
	if name != "git" {
		return [][]string{append([]string{name}, argv[1:]...)}
	}
	var tails [][]string
	collectGitTails(argv, 1, &tails)
	if len(tails) == 0 {
		return [][]string{{"git"}}
	}
	out := make([][]string, len(tails))
	for i, t := range tails {
		out[i] = append([]string{"git"}, t...)
	}
	return out
}

var (
	scopedPackage = regexp.MustCompile(`^@codedology/code-forge(?:@` + jsDot + `*)?$`)
	barePackage   = regexp.MustCompile(`^code-forge(?:\.mjs)?(?:@` + jsDot + `*)?$`)
)

func forbidIsPackage(t string) bool {
	return scopedPackage.MatchString(t) || barePackage.MatchString(forbidBasename(t))
}

func unwrapLauncher(reading []string) []string {
	rest := reading
	at := func(i int) string {
		if i < len(rest) {
			return rest[i]
		}
		return "\x00none"
	}
	skipOptions := func() {
		for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			n := 1
			if rest[0] == "-p" || rest[0] == "--package" || rest[0] == "-r" || rest[0] == "--require" {
				n = 2
			}
			if n > len(rest) {
				n = len(rest)
			}
			rest = rest[n:]
		}
	}
	switch {
	case at(0) == "npx" || at(0) == "bunx":
		rest = rest[1:]
		skipOptions()
	case (at(0) == "pnpm" || at(0) == "yarn") && (at(1) == "exec" || at(1) == "dlx"):
		rest = rest[2:]
		skipOptions()
	case at(0) == "node":
		rest = rest[1:]
		skipOptions()
	}
	if len(rest) == 0 {
		return rest
	}
	head, tail := rest[0], rest[1:]
	if forbidIsPackage(head) && len(tail) > 0 && forbidIsPackage(tail[0]) {
		tail = tail[1:]
	}
	first := forbidCommandName(head)
	if forbidIsPackage(head) {
		first = "code-forge"
	}
	out := []string{first}
	for _, t := range tail {
		if !strings.HasPrefix(t, "-") {
			out = append(out, t)
		}
	}
	return out
}

var writeVerbs = map[string]bool{"cp": true, "mv": true, "tee": true, "touch": true, "rm": true, "ln": true, "install": true,
	"truncate": true, "dd": true, "rsync": true, "chmod": true, "chown": true, "unlink": true, "mkdir": true, "rmdir": true,
	">": true, ">>": true, "Write": true, "Edit": true, "MultiEdit": true, "NotebookEdit": true}

var gitWriteSubcommands = map[string]bool{"mv": true, "rm": true, "restore": true, "checkout": true}

var forbidWrappers = map[string]bool{"sudo": true, "env": true, "xargs": true, "nice": true, "timeout": true, "command": true, "nohup": true}

var (
	inPlaceShort  = regexp.MustCompile(`^-[a-zA-Z]*i`)
	inPlaceLong   = regexp.MustCompile(`^--in-place(?:=|$)`)
	envAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	duration      = regexp.MustCompile(`^\d+(?:\.\d+)?[smhd]?$`)
)

func isWriteCommand(argv []string) bool {
	i := 0
	for i < len(argv) && forbidWrappers[forbidCommandName(argv[i])] {
		i++
		for i < len(argv) && (strings.HasPrefix(argv[i], "-") || envAssignment.MatchString(argv[i]) || duration.MatchString(argv[i])) {
			i++
		}
	}
	verb := ""
	if i < len(argv) {
		verb = forbidCommandName(argv[i])
	}
	if writeVerbs[verb] {
		return true
	}
	if verb == "sed" || verb == "perl" {
		for _, t := range argv[i+1:] {
			if inPlaceShort.MatchString(t) || inPlaceLong.MatchString(t) {
				return true
			}
		}
	}
	if verb == "git" {
		for _, r := range normalizedReadings(argv[i:]) {
			if len(r) > 1 && gitWriteSubcommands[r[1]] {
				return true
			}
		}
	}
	return false
}

// jsWSChars is the JS \s set without its brackets.
const jsWSChars = `\t\n\v\f\r \x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}`

func forbidGlob(s string) string {
	parts := strings.Split(s, "*")
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	return strings.Join(parts, `[^/`+jsWSChars+`]*`)
}

// forbidHome is os.homedir(): $HOME, else the account's home.
func forbidHome() string {
	if h := os.Getenv("HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

func pathPattern(p string) *regexp.Regexp {
	end := `(?:/|$|[` + jsWSChars + `'";)])`
	if strings.HasPrefix(p, "~/") {
		homes := []string{"~", `\$HOME`, `\$\{HOME\}`, regexp.QuoteMeta(forbidHome())}
		return regexp.MustCompile(`(?:^|[=:` + jsWSChars + `'"])(?:` + strings.Join(homes, "|") + `)/` + forbidGlob(p[2:]) + end)
	}
	return regexp.MustCompile(`(?:^|[/=:` + jsWSChars + `'"])` + forbidGlob(p) + end)
}

func startsWithPrefix(argv, prefix []string) bool {
	if len(argv) < len(prefix) {
		return false
	}
	for i, t := range prefix {
		if argv[i] != t {
			return false
		}
	}
	return true
}

func tokenMatchesGroup(token string, g forbidGroup) bool {
	for _, f := range g.anyOf {
		if token == f {
			return true
		}
	}
	for _, p := range g.anyPrefixOf {
		if strings.HasPrefix(token, p) {
			return true
		}
	}
	if len(g.shortLetters) > 0 && len(token) >= 2 && token[0] == '-' && token[1] != '-' {
		cluster := token[1:]
		for _, l := range g.shortLetters {
			if strings.Contains(cluster, l) {
				return true
			}
		}
	}
	return false
}

func matchesEntry(reading, raw []string, e forbidEntry) bool {
	switch e.kind {
	case "contains":
		for _, t := range e.tokens {
			hit := false
			for _, a := range raw {
				if strings.Contains(a, t) {
					hit = true
					break
				}
			}
			if !hit {
				return false
			}
		}
		return true
	case "prefix":
		return startsWithPrefix(reading, e.tokens)
	case "anyPrefix":
		unwrapped := unwrapLauncher(reading)
		for _, p := range e.prefixes {
			if startsWithPrefix(unwrapped, p) {
				return true
			}
		}
		return false
	case "path":
		named := false
		for _, token := range raw {
			for _, p := range e.paths {
				if pathPattern(p).MatchString(token) {
					named = true
				}
			}
		}
		if !named || e.access == "readwrite" {
			return named
		}
		return isWriteCommand(raw)
	}
	if !startsWithPrefix(reading, e.command) {
		return false
	}
	rest := reading[len(e.command):]
	for _, g := range e.groups {
		hit := false
		for _, t := range rest {
			if tokenMatchesGroup(t, g) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// forbiddenHit is isForbidden: the id of the first entry argv matches, "" when clear.
func forbiddenHit(argv []string, list []forbidEntry) string {
	if len(argv) == 0 {
		return ""
	}
	readings := normalizedReadings(argv)
	for _, e := range list {
		for _, r := range readings {
			if matchesEntry(r, argv, e) {
				return e.id
			}
		}
	}
	return ""
}

func groupSpellings(g forbidGroup) []string {
	spellings := append([]string{}, g.anyOf...)
	for _, a := range g.shortLetters {
		spellings = append(spellings, "-"+a)
	}
	for _, a := range g.shortLetters {
		for _, b := range g.shortLetters {
			if a != b {
				spellings = append(spellings, "-"+a+b)
			}
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, s := range spellings {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func flagSequences(groups []forbidGroup) [][]string {
	if len(groups) == 0 {
		return [][]string{{}}
	}
	var out [][]string
	for i, g := range groups {
		others := append(append([]forbidGroup{}, groups[:i]...), groups[i+1:]...)
		for _, s := range groupSpellings(g) {
			for _, tail := range flagSequences(others) {
				out = append(out, append([]string{s}, tail...))
			}
		}
	}
	return out
}

func renderPatterns(e forbidEntry) [][]string {
	switch e.kind {
	case "prefix":
		return [][]string{append([]string{}, e.tokens...)}
	case "commandFlag":
		seen := map[string]bool{}
		var out [][]string
		for _, flags := range flagSequences(e.groups) {
			p := append(append([]string{}, e.command...), flags...)
			key := strings.Join(p, "\x00")
			if !seen[key] {
				seen[key] = true
				out = append(out, p)
			}
		}
		return out
	case "anyPrefix":
		var out [][]string
		for _, p := range e.prefixes {
			if p[0] == "code-forge" {
				out = append(out, append([]string{}, p...), append([]string{"npx"}, p...),
					append([]string{"npx", "@codedology/code-forge"}, p[1:]...))
			} else {
				out = append(out, append([]string{}, p...))
			}
		}
		return out
	}
	return nil
}

func pathRules(e forbidEntry) []string {
	if e.kind != "path" {
		return nil
	}
	tools := []string{"Edit", "Write"}
	if e.access == "readwrite" {
		tools = []string{"Read", "Edit", "Write"}
	}
	var out []string
	for _, p := range e.paths {
		for _, tool := range tools {
			glob := p + "/**"
			if strings.Contains(p, "*") {
				glob = p
			}
			out = append(out, tool+"("+glob+")")
		}
	}
	return out
}

// claudeRuleStrings is flattenRuleStrings(renderForClaude(list)) (Grok's
// renderForGrok is the same render).
func claudeRuleStrings(list []forbidEntry) []string {
	var out []string
	for _, e := range list {
		for _, p := range renderPatterns(e) {
			out = append(out, "Bash("+strings.Join(p, " ")+":*)")
		}
		out = append(out, pathRules(e)...)
	}
	return out
}

// codexRulesFile is renderCodexRules(renderForCodex(list)): the rules file
// content and its rule count.
func codexRulesFile(list []forbidEntry) (string, int) {
	lines := []string{"# code-forge forbidden list (src/util/forbidden.mjs). Generated per session; do not edit."}
	count := 0
	for _, e := range list {
		for _, p := range renderPatterns(e) {
			tokens := make([]string, len(p))
			for i, t := range p {
				tokens[i] = jsonQuote(t)
			}
			lines = append(lines, "prefix_rule(pattern=["+strings.Join(tokens, ", ")+`], decision="forbidden", justification=`+jsonQuote("code-forge: "+e.id)+")")
			count++
		}
	}
	return strings.Join(lines, "\n") + "\n", count
}

// jsonQuote is JSON.stringify of a string.
func jsonQuote(s string) string {
	b, _ := ledger.Stringify(s, false)
	return string(b)
}
