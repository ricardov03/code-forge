package main

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B460: parseFlags and intFlag from src/state/cli-args.mjs, shared by the Go
// worker, spawn and review verbs. Every flag must be declared; an unknown
// flag, a repeated single-value flag or a value on a boolean is a usage error
// with Node's exact message.
type cliSpec struct {
	values, booleans, multi []string
}

type cliParsed struct {
	values      map[string]string
	multi       map[string][]string
	booleans    map[string]bool
	positionals []string
}

func (p cliParsed) has(name string) bool {
	_, v := p.values[name]
	_, m := p.multi[name]
	return v || m || p.booleans[name]
}

func cliContains(list []string, name string) bool {
	for _, item := range list {
		if item == name {
			return true
		}
	}
	return false
}

func cliParseFlags(args []string, spec cliSpec) (cliParsed, string) {
	p := cliParsed{values: map[string]string{}, multi: map[string][]string{}, booleans: map[string]bool{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			p.positionals = append(p.positionals, arg)
			continue
		}
		name, inline, hasInline := strings.Cut(arg[2:], "=")
		isBool, isMulti, isValue := cliContains(spec.booleans, name), cliContains(spec.multi, name), cliContains(spec.values, name)
		if !isBool && !isMulti && !isValue {
			quoted, _ := ledger.Stringify(arg, false)
			return p, "unknown flag " + string(quoted)
		}
		if p.has(name) && !isMulti {
			return p, "--" + name + " given more than once"
		}
		switch {
		case isBool:
			if hasInline {
				return p, "--" + name + " takes no value"
			}
			p.booleans[name] = true
		case isMulti:
			collected := p.multi[name]
			if hasInline && inline != "" {
				collected = append(collected, inline)
			}
			for i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
				collected = append(collected, args[i])
			}
			if len(collected) == 0 {
				return p, "--" + name + " needs at least one value"
			}
			p.multi[name] = collected
		case hasInline:
			if inline == "" {
				return p, "--" + name + " needs a value"
			}
			p.values[name] = inline
		default:
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return p, "--" + name + " needs a value"
			}
			p.values[name] = args[i+1]
			i++
		}
	}
	return p, ""
}

var cliPositiveInt = regexp.MustCompile(`^[1-9][0-9]*$`)

// cliIntFlag is intFlag: a positive safe integer with no sign or leading zero.
func cliIntFlag(raw, name string) (float64, string) {
	if cliPositiveInt.MatchString(raw) {
		if n, err := strconv.ParseFloat(raw, 64); err == nil && n <= 1<<53-1 {
			return n, ""
		}
	}
	return 0, "--" + name + " must be a positive integer"
}

// cliHelp reports whether a token is one of the router's help aliases.
func cliHelp(s string) bool { return s == "--help" || s == "-h" }
