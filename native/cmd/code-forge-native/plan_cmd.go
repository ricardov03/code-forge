package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const planUsage = "usage: code-forge plan check <plan-file> [--facts <sheet>] [--slug <slug>]\n"

func init() {
	routes["plan"] = smallVerb{name: "plan", usage: planUsage, subs: []string{"check"},
		subUsage: map[string]string{"check": planUsage}, prepare: preparePlan}.run
}

// nodeFlags is parseFlags in src/state/cli-args.mjs for boolean and
// single-value flags: the first problem's message, or "" when the args parse.
func nodeFlags(args []string, booleans, values []string) (map[string]string, []string, string) {
	has := func(list []string, name string) bool {
		for _, v := range list {
			if v == name {
				return true
			}
		}
		return false
	}
	flags := map[string]string{}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			positionals = append(positionals, arg)
			continue
		}
		name, inline, eq := strings.Cut(arg[2:], "=")
		if !has(booleans, name) && !has(values, name) {
			return nil, nil, "unknown flag " + jsQuote(arg)
		}
		if _, seen := flags[name]; seen {
			return nil, nil, "--" + name + " given more than once"
		}
		switch {
		case has(booleans, name):
			if eq {
				return nil, nil, "--" + name + " takes no value"
			}
			flags[name] = ""
		case eq:
			if inline == "" {
				return nil, nil, "--" + name + " needs a value"
			}
			flags[name] = inline
		default:
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return nil, nil, "--" + name + " needs a value"
			}
			flags[name] = args[i+1]
			i++
		}
	}
	return flags, positionals, ""
}

// preparePlan is src/cli/plan.mjs up to its config load: usage and the plan
// and facts reads answer here. The check itself (checkPlan with the facts
// sheet, project config and ledger lanes) stays in Node.
func preparePlan(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	refuse := func(text string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
		return func(_, stderr io.Writer) int {
			fmt.Fprint(stderr, text)
			return 2
		}, 0, true
	}
	flags, positionals, problem := nodeFlags(args, nil, []string{"facts", "slug"})
	if problem != "" {
		return refuse("plan: " + problem + "\n")
	}
	if len(positionals) != 2 || positionals[0] != "check" {
		return refuse(planUsage)
	}
	cwd := physicalCwd()
	resolve := func(p string) string {
		if filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
		return filepath.Join(cwd, p)
	}
	if _, err := os.ReadFile(resolve(positionals[1])); err != nil {
		return refuse("plan: the plan file cannot be read\n")
	}
	if facts, ok := flags["facts"]; ok {
		if _, err := os.ReadFile(resolve(facts)); err != nil {
			return refuse("plan: --facts cannot be read\n")
		}
	}
	return nil, hPlanCheckUnported, false
}
