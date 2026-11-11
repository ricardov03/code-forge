package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
)

// smallVerb runs one of the small B455 verbs the way bin/code-forge.mjs runs
// its module: help first, then applyTmpRoot, the breadcrumb, the verb, and an
// errors.jsonl entry on a non-zero exit. prepare decides, before any side
// effect, whether Go can finish the run; when it cannot, the whole run goes
// to Node with the reason it returns.
type smallVerb struct {
	name     string
	usage    string
	subs     []string
	subUsage map[string]string
	prepare  func(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool)
}

func (v smallVerb) run(args []string) int {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		fmt.Fprint(os.Stdout, v.usage)
		return 0
	}
	sub := ""
	if len(args) > 0 {
		for _, known := range v.subs {
			if args[0] == known {
				sub = known
			}
		}
	}
	if len(args) > 1 && (args[1] == "--help" || args[1] == "-h") {
		if usage, ok := v.subUsage[args[0]]; ok {
			if !strings.HasSuffix(usage, "\n") {
				usage += "\n"
			}
			fmt.Fprint(os.Stdout, usage)
			return 0
		}
	}
	body, reason, ok := v.prepare(args)
	if !ok {
		return smallVerbHandover(v.name, args, reason)
	}
	contextApplyTmpRoot()
	name := v.name
	if sub != "" {
		name += " " + sub
	}
	before := gatesBreadcrumbName(name)
	stderr := &contextErrWriter{}
	code := body(os.Stdout, stderr)
	if code != 0 {
		var message any
		if stderr.last != "" {
			message = stderr.last
		}
		nativeVerbFailure(v.name, sub, args, code, message, before)
	}
	return code
}

func smallVerbHandover(verb string, args []string, reason handoverReason) int {
	code, err := handover(append([]string{verb}, args...), reason)
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}

// nodeLog is src/util/log.mjs: every line of the message carries the prefix.
func nodeLog(w io.Writer, level, message string) {
	lines := strings.Split(message, "\n")
	for i, line := range lines {
		lines[i] = "[code-forge:" + level + "] " + line
	}
	fmt.Fprint(w, strings.Join(lines, "\n")+"\n")
}

func maskTokens(text string) string { return config.MaskSecrets(text) }

// yamlSyntaxError reports a parse-error whose text comes from the YAML
// library's own wording. The Go reader does not reproduce every one of those
// messages, so such runs go to Node; the root-mapping message is our own.
func yamlSyntaxError(err error) bool {
	e, ok := err.(*config.Error)
	return ok && e.Kind == "parse-error" && !strings.HasPrefix(e.Message, "config root must be a mapping, got ")
}
