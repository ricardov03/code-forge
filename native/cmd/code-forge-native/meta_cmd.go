package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/ricardov03/code-forge/native/docsync"
)

// B458: help, version and the metadata, help and usage-error forms of init,
// native-status, facts, author, s2 and jev run in Go, as bin/code-forge.mjs
// and src/cli/<verb>.mjs do. Each verb's model session, wizard or companion
// still hands over to Node with its own reason.
func init() {
	routes["help"] = runHelpVerb
	routes["version"] = runVersionVerb
	routes["init"] = runInitVerb
	routes["native-status"] = runNativeStatusVerb
	routes["facts"] = runFactsVerb
	routes["author"] = runAuthorVerb
	routes["s2"] = runS2Verb
	routes["jev"] = runJevVerb
}

const (
	helpUsage    = "usage: code-forge help\n"
	versionUsage = "usage: code-forge version\n"
	initUsage    = "usage: code-forge init [--no-interaction] [--tools recommended|current] [--yes-tool <tool>]…\n" +
		"  [--harness a,b] [-g|-p] [--copy] [--provider P] [--level Ln=model[:effort][@provider]]…\n" +
		"  [--refresh-models] [--multimodel on|off] [--second-provider P]\n" +
		"  [--jev-ref <item-id|link|op://…> | --jev-env NAME | --no-jev] [--engine auto|solo|harness] [--solo-project N]\n" +
		"  [--coder-may-push yes|no] [--coder-may-open-draft-pr yes|no]\n" +
		"  [--gate name=cmd]… [--proof isolation=export|lock | high=a,b | link_dirs=a,b | copy_untracked=a,b]… [--skip-doctor]\n"
	nativeStatusUsage = "usage: code-forge native-status\n  Internal daemon companion: reads JSON records and rows from standard input.\n"
	factsUsage        = "usage: code-forge facts --brief <file> [--sources <path>...] [--out <file>] [--run <id>] [--timeout <seconds>]\n"
	authorUsage       = "usage: code-forge author --job plan|harden --brief <file> [--facts <file>] [--draft <file>] [--answers <file>] [--out <file>] [--run <id>] [--timeout <seconds>]\n"
	s2Usage           = "usage: code-forge s2 --packet <file.json> [--run <id>] [--block <id>] [--timeout <seconds>]\n"
	jevUsage          = "usage: code-forge jev ask <question-id> --state <file> [--cwd <dir>] [--slug <slug>] [--key-ref <ref>] [--block <id>] [--plan <file>] [--rules]\n"
)

// metaPackageRoot is the install root (<root>/native/bin/<platform>/); a test seam.
var metaPackageRoot = docsync.PackageRoot

// metaVerbFile is VERB_FILE_PATTERN in bin/code-forge.mjs and src/cli/help.mjs.
var metaVerbFile = regexp.MustCompile(`^[a-z][a-z0-9-]*\.mjs$`)

func metaIsHelp(s string) bool { return s == "--help" || s == "-h" }

// metaHelp is the router's help step: `<verb> --help` prints USAGE, and
// `<verb> <sub> --help` prints SUB_USAGE[sub] when the verb has one. Any other
// form falls through to the verb, as in Node.
func metaHelp(args []string, usage string, sub map[string]string) bool {
	if len(args) > 0 && metaIsHelp(args[0]) {
		fmt.Fprint(os.Stdout, usage)
		return true
	}
	if len(args) > 1 && metaIsHelp(args[1]) {
		if text, ok := sub[args[0]]; ok {
			fmt.Fprint(os.Stdout, text)
			return true
		}
	}
	return false
}

// metaSub is subcommandOf in src/util/error-log.mjs: the first non-flag word,
// kept only when it is one of the verb's own subcommands.
func metaSub(args []string, known ...string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		for _, sub := range known {
			if arg == sub {
				return sub
			}
		}
		return ""
	}
	return ""
}

// metaServe runs a Go-served form with the router's breadcrumb and error log
// (noteVerb before the verb, writeVerbFailure after a non-zero exit).
func metaServe(verb, sub string, args []string, fn func(stdout, stderr io.Writer) int) int {
	name := verb
	if sub != "" {
		name += " " + sub
	}
	before := gatesBreadcrumbName(name)
	stderr := &contextErrWriter{}
	code := fn(os.Stdout, stderr)
	if code != 0 {
		var message any
		if stderr.last != "" {
			message = stderr.last
		}
		nativeVerbFailure(verb, sub, args, code, message, before)
	}
	return code
}

func metaHandover(verb string, args []string, reason handoverReason) int {
	code, err := handover(append([]string{verb}, args...), reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}

// helpVerbs is listVerbs in bin/code-forge.mjs: the regular src/cli/*.mjs
// files of this install, sorted.
func helpVerbs() ([]string, error) {
	root, err := metaPackageRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(root, "src", "cli"))
	if err != nil {
		return nil, err
	}
	verbs := []string{}
	for _, entry := range entries {
		if entry.Type().IsRegular() && metaVerbFile.MatchString(entry.Name()) {
			verbs = append(verbs, strings.TrimSuffix(entry.Name(), ".mjs"))
		}
	}
	sort.Strings(verbs)
	return verbs, nil
}

func runHelpVerb(args []string) int {
	if metaHelp(args, helpUsage, nil) {
		return 0
	}
	verbs, err := helpVerbs()
	if err != nil {
		return metaHandover("help", args, hHelpVerbs)
	}
	return metaServe("help", "", args, func(stdout, _ io.Writer) int {
		var b strings.Builder
		b.WriteString("Usage: code-forge <verb> [args]\n\nVerbs:\n")
		for _, verb := range verbs {
			b.WriteString("  " + verb + "\n")
		}
		b.WriteString("\nBlock usage: code-forge block <verb> --help (or -h).\n")
		b.WriteString("  block open --acceptance <file>: JSON/YAML list [{clause, tests}]; tests is a nonempty list of test ids.\n")
		b.WriteString("  block open --brief <file>: a file inside the repository, not inline text.\n")
		b.WriteString("  block land <id> --run <r> --report <file> --transcript <file> [--max <dur>] [--merge] [--no-merge-gate] [--push] [--watch-ci] [--copy-links] [--json]: join reviews, run changed gates, close, then with --merge run the merge gate and merge with --no-ff.\n")
		b.WriteString("  block land <id> <id>... --run <r> [--max <dur>] [--push] [--watch-ci] [--copy-links] [--json]: land closed blocks in order with one merge gate.\n")
		b.WriteString("  Configuration fallback entries must be {provider, model} objects, not model-name strings (optional effort).\n")
		b.WriteString("\nTest queue: gates wait in one machine-wide FIFO queue (code-forge queue shows it); wrap other test commands in `code-forge queue run -- <cmd>`.\n")
		fmt.Fprint(stdout, b.String())
		return 0
	})
}

func runVersionVerb(args []string) int {
	if metaHelp(args, versionUsage, nil) {
		return 0
	}
	root, err := metaPackageRoot()
	if err != nil {
		return metaHandover("version", args, hVersionPackage)
	}
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	var pkg struct {
		Name    *string `json:"name"`
		Version *string `json:"version"`
	}
	// Node prints any value; Go serves the string fields every release has.
	if err != nil || json.Unmarshal(data, &pkg) != nil || pkg.Name == nil || pkg.Version == nil {
		return metaHandover("version", args, hVersionPackage)
	}
	return metaServe("version", "", args, func(stdout, _ io.Writer) int {
		fmt.Fprintf(stdout, "%s %s\n", *pkg.Name, *pkg.Version)
		return 0
	})
}

func runNativeStatusVerb(args []string) int {
	if metaHelp(args, nativeStatusUsage, nil) {
		return 0
	}
	return metaHandover("native-status", args, hNativeStatusCompanion)
}

// metaFlagSpec is the declaration parseFlags (src/state/cli-args.mjs) takes.
type metaFlagSpec struct{ values, booleans, multi []string }

func metaHas(list []string, name string) bool {
	for _, item := range list {
		if item == name {
			return true
		}
	}
	return false
}

// metaParseFlags is parseFlags in src/state/cli-args.mjs. It returns the
// StateError usage message, or "" when the arguments parse.
func metaParseFlags(args []string, spec metaFlagSpec) (map[string]any, []string, string) {
	flags, positionals := map[string]any{}, []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			positionals = append(positionals, arg)
			continue
		}
		name, value, inline := strings.Cut(arg[2:], "=")
		boolean, multi := metaHas(spec.booleans, name), metaHas(spec.multi, name)
		if !boolean && !multi && !metaHas(spec.values, name) {
			return flags, positionals, "unknown flag " + statusQuote(arg)
		}
		if _, seen := flags[name]; seen && !multi {
			return flags, positionals, "--" + name + " given more than once"
		}
		switch {
		case boolean:
			if inline {
				return flags, positionals, "--" + name + " takes no value"
			}
			flags[name] = true
		case multi:
			collected, _ := flags[name].([]string)
			if inline && value != "" {
				collected = append(collected, value)
			}
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
				collected = append(collected, args[i])
			}
			if len(collected) == 0 {
				return flags, positionals, "--" + name + " needs at least one value"
			}
			flags[name] = collected
		case inline:
			if value == "" {
				return flags, positionals, "--" + name + " needs a value"
			}
			flags[name] = value
		default:
			if i+1 == len(args) || strings.HasPrefix(args[i+1], "--") {
				return flags, positionals, "--" + name + " needs a value"
			}
			i++
			flags[name] = args[i]
		}
	}
	return flags, positionals, ""
}

// metaSessionVerb serves the usage errors of a session verb (facts, author,
// s2): a parseFlags refusal prints "<verb>: <message>", a positional or a
// missing required flag prints USAGE; both exit 2. Every other call runs the
// model session in Node.
func metaSessionVerb(verb, usage string, spec metaFlagSpec, required []string, reason handoverReason, args []string) int {
	if metaHelp(args, usage, nil) {
		return 0
	}
	flags, positionals, message := metaParseFlags(args, spec)
	if message == "" && len(positionals) == 0 {
		missing := false
		for _, name := range required {
			if _, ok := flags[name].(string); !ok {
				missing = true
			}
		}
		if !missing {
			return metaHandover(verb, args, reason)
		}
	}
	return metaServe(verb, "", args, func(_, stderr io.Writer) int {
		if message != "" {
			fmt.Fprintf(stderr, "%s: %s\n", verb, message)
		} else {
			fmt.Fprint(stderr, usage)
		}
		return 2
	})
}

func runFactsVerb(args []string) int {
	spec := metaFlagSpec{values: []string{"brief", "out", "run", "timeout"}, multi: []string{"sources"}}
	return metaSessionVerb("facts", factsUsage, spec, []string{"brief"}, hFactsSession, args)
}

func runAuthorVerb(args []string) int {
	spec := metaFlagSpec{values: []string{"job", "brief", "facts", "draft", "answers", "out", "run", "timeout"}}
	return metaSessionVerb("author", authorUsage, spec, []string{"job", "brief"}, hAuthorSession, args)
}

func runS2Verb(args []string) int {
	spec := metaFlagSpec{values: []string{"packet", "run", "block", "timeout"}}
	return metaSessionVerb("s2", s2Usage, spec, []string{"packet"}, hS2Session, args)
}

// jevQuestions is QUESTIONS in src/decide/questions.mjs.
var jevQuestions = []string{"lane", "risk", "security_sensitive", "next", "scope", "defect", "resolved"}

var jevBlockID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// jevAskUsage returns the refusal runJev prints before it reads the --state
// file, or "" when the call goes on to the state file.
func jevAskUsage(args []string) string {
	if len(args) < 2 || args[0] != "ask" || strings.HasPrefix(args[1], "--") {
		return jevUsage
	}
	id := args[1]
	if !metaHas(jevQuestions, id) {
		return "jev ask: unknown question id " + strconvQuoteJS(id) + "\n"
	}
	values, rules := map[string]string{}, false
	rest := args[2:]
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if arg == "--rules" {
			if rules {
				return jevUsage
			}
			rules = true
			continue
		}
		_, seen := values[arg]
		if !metaHas([]string{"--state", "--cwd", "--slug", "--key-ref", "--block", "--plan"}, arg) || seen {
			return jevUsage
		}
		if i+1 == len(rest) || strings.HasPrefix(rest[i+1], "--") {
			return jevUsage
		}
		values[arg] = rest[i+1]
		i++
	}
	if _, ok := values["--state"]; !ok {
		return jevUsage
	}
	if block, ok := values["--block"]; ok && !jevBlockID.MatchString(block) {
		return "jev ask: --block must be a block id (letters, digits, . _ -; at most 32 characters)\n"
	}
	if rules && id != "lane" && id != "risk" && id != "security_sensitive" {
		return "jev ask: no rule answers " + strconvQuoteJS(id) + " (--rules covers lane, risk, security_sensitive)\n"
	}
	return ""
}

// strconvQuoteJS is the template literal "${id}" in jev.mjs: plain double
// quotes around the raw text, no escaping.
func strconvQuoteJS(s string) string { return `"` + s + `"` }

func runJevVerb(args []string) int {
	if metaHelp(args, jevUsage, map[string]string{"ask": jevUsage}) {
		return 0
	}
	refusal := jevAskUsage(args)
	if refusal == "" {
		return metaHandover("jev", args, hJevAsk)
	}
	return metaServe("jev", metaSub(args, "ask"), args, func(_, stderr io.Writer) int {
		fmt.Fprint(stderr, refusal)
		return 2
	})
}
