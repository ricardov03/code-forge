package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
)

const resolveUsage = "usage: code-forge resolve <L0|L1|L2|L3>\n"

const codexClosedBookRefusal = "codex cannot run closed-book yet: it always has a shell; use anthropic or xai for reviewer, judge, S2 and plan author"
const openBookCodexWarning = "codex reviewer is not closed-book: it can read files on this machine (review.allow_open_book_codex)"

var providerCLINames = map[string]string{"anthropic": "claude", "openai": "codex", "xai": "grok"}

func init() {
	routes["resolve"] = smallVerb{name: "resolve", usage: resolveUsage, prepare: prepareResolve}.run
}

// isNoClosedBookProvider is closed-book.mjs: trimmed, lower-cased "openai".
func isNoClosedBookProvider(provider any) bool {
	s, ok := provider.(string)
	return ok && strings.ToLower(strings.TrimSpace(s)) == "openai"
}

// prepareResolve is src/cli/resolve.mjs. Only a fallback that is not a list
// of mappings is left to Node: its spread and TypeError text are JS-specific.
func prepareResolve(args []string) (func(stdout, stderr io.Writer) int, handoverReason, bool) {
	if len(args) != 1 {
		return func(_, stderr io.Writer) int {
			nodeLog(stderr, "error", maskTokens(strings.TrimRight(strings.Replace(resolveUsage, "code-forge ", "", 1), " \t\n")))
			return 2
		}, 0, true
	}
	level := args[0]
	if level != "L0" && level != "L1" && level != "L2" && level != "L3" {
		return func(_, stderr io.Writer) int {
			nodeLog(stderr, "error", maskTokens("unknown level \""+level+"\" — expected one of L0, L1, L2, L3"))
			return 2
		}, 0, true
	}
	cwd, _ := os.Getwd()
	cfg, order, err := config.LoadOrdered(cwd)
	if yamlSyntaxError(err) {
		return nil, hConfigParseError, false
	}
	if err != nil {
		e, _ := err.(*config.Error)
		return func(_, stderr io.Writer) int {
			nodeLog(stderr, "error", maskTokens(e.Kind+": "+e.Message))
			return 1
		}, 0, true
	}
	levels, _ := cfg["levels"].(map[string]any)
	entry, _ := levels[level].(map[string]any)
	if entry != nil {
		if raw, present := entry["fallback"]; present && raw != nil {
			list, ok := raw.([]any)
			if !ok {
				return nil, hResolveFallbackShape, false
			}
			for _, item := range list {
				if _, ok := item.(map[string]any); !ok {
					return nil, hResolveFallbackShape, false
				}
			}
		}
	}
	return func(stdout, stderr io.Writer) int {
		fail := func(message string) int {
			nodeLog(stderr, "error", maskTokens(message))
			return 1
		}
		model, _ := entry["model"].(string)
		if entry == nil || model == "" {
			return fail("resolveLevel: levels." + level + " is missing or has no model")
		}
		rawProvider := entry["provider"]
		if rawProvider == nil {
			rawProvider = cfg["provider"]
		}
		provider, _ := rawProvider.(string)
		if provider == "" {
			return fail("resolveLevel: levels." + level + " has no provider (neither per-level nor top-level)")
		}
		cli, ok := providerCLINames[provider]
		if !ok {
			return fail("resolve: provider \"" + provider + "\" (from levels." + level + ") has no known CLI mapping")
		}
		closedBook := level == "L2" || level == "L3"
		review, _ := cfg["review"].(map[string]any)
		openBook := review["allow_open_book_codex"] == true
		mark := "refused"
		if openBook {
			mark = "open-book (allowed)"
		}
		marked := closedBook && isNoClosedBookProvider(provider)
		fallback := []any{}
		anyMarked := false
		list, _ := entry["fallback"].([]any)
		for _, item := range list {
			object := order.Object(item).(ledger.Object)
			if closedBook && isNoClosedBookProvider(item.(map[string]any)["provider"]) {
				object = setField(object, "closed_book", mark)
			}
			for _, field := range object {
				if field.Key == "closed_book" {
					anyMarked = true
				}
			}
			fallback = append(fallback, object)
		}
		if marked || anyMarked {
			warning := codexClosedBookRefusal
			if openBook {
				warning = openBookCodexWarning
			}
			nodeLog(stderr, "warn", warning)
		}
		effort := order.Object(entry["effort"])
		output := ledger.Object{{Key: "provider", Value: provider}, {Key: "model", Value: model}, {Key: "effort", Value: effort}, {Key: "fallback", Value: fallback}, {Key: "cli", Value: cli}}
		if marked {
			output = append(output, ledger.Field{Key: "closed_book", Value: mark})
		}
		data, err := ledger.Stringify(output, false)
		if err != nil {
			return fail(err.Error())
		}
		fmt.Fprint(stdout, string(data)+"\n")
		return 0
	}, 0, true
}

// setField is {...object, key: value}: an existing key keeps its place.
func setField(object ledger.Object, key string, value any) ledger.Object {
	out := append(ledger.Object{}, object...)
	for i := range out {
		if out[i].Key == key {
			out[i].Value = value
			return out
		}
	}
	return append(out, ledger.Field{Key: key, Value: value})
}
