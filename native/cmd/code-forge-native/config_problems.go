package main

import (
	"strconv"

	"github.com/ricardov03/code-forge/native/config"
	"gopkg.in/yaml.v3"
)

// B787: what the diagnostic and reload verbs (validate, doctor, plan check,
// run reload, autopilot approve) print about a config that does not check
// clean, in place of a hand-over.

// cpLoadErrorText is a config load error as the user reads it: a YAML error
// carries "(line N, column M)", so it is named "<file>:<N>: <message>", the
// message run start prints; any other error is its message.
func cpLoadErrorText(file string, e *config.Error) string {
	if m := runYAMLLine.FindStringSubmatch(e.Message); m != nil {
		return file + ":" + m[1] + ": " + e.Message
	}
	return e.Message
}

// cpYAMLTag is the first explicit YAML tag (`!!binary`, `!foo`, a key's or
// a value's) in source and its line; "" when there is none. A `!!` inside a
// comment or a quoted string is text, not a tag, so it is never found here.
// A source that does not parse has no tag to report (its parse error is).
func cpYAMLTag(source string) (string, int) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(source), &doc); err != nil {
		return "", 0
	}
	var walk func(n *yaml.Node) (string, int)
	walk = func(n *yaml.Node) (string, int) {
		if n == nil {
			return "", 0
		}
		if n.Kind != yaml.DocumentNode && n.Kind != yaml.AliasNode && n.Style&yaml.TaggedStyle != 0 {
			return n.Tag, n.Line
		}
		for _, child := range n.Content {
			if tag, line := walk(child); tag != "" {
				return tag, line
			}
		}
		return "", 0
	}
	return walk(&doc)
}

// cpFirstProblem is the first reason the config text does not check clean
// (a YAML tag, a parse error, a schema issue, a domain error), worded for
// the user; "" when it checks clean or when the only problem is a
// secret-shaped value (that case keeps its own path). home is where the
// seen-in-cache model list is read.
func cpFirstProblem(home, file, text string) string {
	if tag, line := cpYAMLTag(text); tag != "" {
		return cpTagText(config.Filename, tag, line)
	}
	cfg, order, err := config.ParseTextOrdered(text, file)
	if err != nil {
		if e, ok := err.(*config.Error); ok {
			return e.Kind + ": " + cpLoadErrorText(config.Filename, e)
		}
		return err.Error()
	}
	if issues := config.SchemaIssues(cfg, version); len(issues) > 0 {
		return "[schema] " + issues[0].Text
	}
	if configHoldsSecretShape(cfg) {
		return ""
	}
	v := configValidator{cfg: cfg, order: order, seen: config.LoadSeenInCache(home), hasCli: hasCliOnPath}
	if errs, _ := v.run(); len(errs) > 0 {
		return maskTokens("[" + errs[0].rule + "] " + errs[0].message)
	}
	return ""
}

// cpTagText names a real YAML tag code-forge does not reload.
func cpTagText(file, tag string, line int) string {
	return file + ":" + strconv.Itoa(line) + " uses the YAML tag " + tag + ", which code-forge does not reload; write the plain value"
}
