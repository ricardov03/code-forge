package main

import (
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/yamledit"

	"gopkg.in/yaml.v3"
)

// B686: init reads and rewrites a .code-forge.yml (and the user config) that
// holds YAML anchors, aliases, << keys and standard tags as runInit does.
// Node's yaml package parses an alias to the very object (or array) its
// anchor names, keeps << as a plain key (merge keys are off), and
// serializeConfig's stringify writes an object met twice once with an
// anchor (&a1, &a2, … in the order each is first met again) and then as an
// alias (*a1). mergeInto keeps every child of the existing config it does
// not merge by reference, so a shared object the wizard leaves alone keeps
// its anchor; a merged one is a new object and loses it.

// initYAMLKey names one collection: its first element's address and length.
type initYAMLKey struct {
	ptr uintptr
	n   int
}

// initYAMLKeyOf is the identity of a non-empty object or array; an empty
// one has no address Go can tell apart (initYAMLServed hands over an alias
// to an empty collection).
func initYAMLKeyOf(v any) (initYAMLKey, bool) {
	switch x := v.(type) {
	case ledger.Object:
		if len(x) > 0 {
			return initYAMLKey{reflect.ValueOf(x).Pointer(), len(x)}, true
		}
	case []any:
		if len(x) > 0 {
			return initYAMLKey{reflect.ValueOf(x).Pointer(), len(x)}, true
		}
	}
	return initYAMLKey{}, false
}

var (
	initYAMLIntText   = regexp.MustCompile(`^-?(?:0|[1-9][0-9]{0,14})$`)
	initYAMLFloatText = regexp.MustCompile(`^-?(?:0|[1-9][0-9]{0,14})\.[0-9]{1,15}$`)
)

// initYAMLServed reports a YAML text whose anchors, aliases, << keys and
// tags Go reads like Node's yaml package. It is false for a %-directive, an
// anchor, alias or tag on a mapping key, an alias to an empty collection,
// and a tag other than a standard one that fits its node (Node warns on
// stderr, with its process id, for an unknown or unfitting tag).
func initYAMLServed(source string) bool {
	for _, line := range strings.Split(source, "\n") {
		if strings.HasPrefix(line, "%") {
			return false
		}
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(source), &doc); err != nil {
		return false
	}
	var walk func(n *yaml.Node, key bool) bool
	walk = func(n *yaml.Node, key bool) bool {
		if key && (n.Kind == yaml.AliasNode || n.Anchor != "" || n.Style&yaml.TaggedStyle != 0) {
			return false
		}
		if n.Kind == yaml.AliasNode {
			t := n.Alias
			return t != nil && !((t.Kind == yaml.MappingNode || t.Kind == yaml.SequenceNode) && len(t.Content) == 0)
		}
		if n.Style&yaml.TaggedStyle != 0 && !initYAMLTagServed(n) {
			return false
		}
		for i, c := range n.Content {
			if !walk(c, n.Kind == yaml.MappingNode && i%2 == 0) {
				return false
			}
		}
		return true
	}
	return walk(&doc, false)
}

// initYAMLTagServed is a standard tag that resolves its node without a
// warning in Node's yaml package and to the same value in Go's reader.
func initYAMLTagServed(n *yaml.Node) bool {
	switch n.Kind {
	case yaml.MappingNode:
		return n.Tag == "!!map"
	case yaml.SequenceNode:
		return n.Tag == "!!seq"
	case yaml.ScalarNode:
	default:
		return false
	}
	if n.Tag == "!!str" {
		return true
	}
	if n.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle|yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		return false
	}
	switch n.Tag {
	case "!!null":
		return initHas([]string{"", "~", "null", "Null", "NULL"}, n.Value)
	case "!!bool":
		return initHas([]string{"true", "True", "TRUE", "false", "False", "FALSE"}, n.Value)
	case "!!int":
		return initYAMLIntText.MatchString(n.Value)
	case "!!float":
		return initYAMLFloatText.MatchString(n.Value)
	}
	return false
}

// initYAMLRefs converts parsed YAML to plain JSON values like
// initPlainValue, but an alias gives the very object its anchor gave (as
// Node's parse does), and it records every collection it made.
type initYAMLRefs struct {
	maps map[uintptr]any
	seqs map[initYAMLKey]any
	made map[initYAMLKey]bool
}

func newInitYAMLRefs() *initYAMLRefs {
	return &initYAMLRefs{maps: map[uintptr]any{}, seqs: map[initYAMLKey]any{}, made: map[initYAMLKey]bool{}}
}

// value is initPlainValue with shared identity; false where initPlainValue
// is false.
func (r *initYAMLRefs) value(value any, order config.Ordered) (any, bool) {
	switch v := value.(type) {
	case map[string]any:
		ptr := reflect.ValueOf(v).Pointer()
		if out, ok := r.maps[ptr]; ok {
			return out, true
		}
		out := ledger.Object{}
		for _, key := range order.Keys(v) {
			if !initPlainKey(key) {
				return nil, false
			}
			child, ok := r.value(v[key], order)
			if !ok {
				return nil, false
			}
			out = append(out, ledger.Field{Key: key, Value: child})
		}
		out = out[:len(out):len(out)]
		r.maps[ptr] = out
		r.mark(out)
		return out, true
	case []config.Pair:
		var src initYAMLKey
		if len(v) > 0 {
			src = initYAMLKey{reflect.ValueOf(v).Pointer(), len(v)}
			if out, ok := r.seqs[src]; ok {
				return out, true
			}
		}
		out := ledger.Object{}
		for _, p := range v {
			if !initPlainKey(p.Key) {
				return nil, false
			}
			child, ok := r.value(p.Value, order)
			if !ok {
				return nil, false
			}
			out = append(out, ledger.Field{Key: p.Key, Value: child})
		}
		out = out[:len(out):len(out)]
		if len(v) > 0 {
			r.seqs[src] = out
		}
		r.mark(out)
		return out, true
	case []any:
		var src initYAMLKey
		if len(v) > 0 {
			src = initYAMLKey{reflect.ValueOf(v).Pointer(), len(v)}
			if out, ok := r.seqs[src]; ok {
				return out, true
			}
		}
		out := []any{}
		for _, item := range v {
			child, ok := r.value(item, order)
			if !ok {
				return nil, false
			}
			out = append(out, child)
		}
		out = out[:len(out):len(out)]
		if len(v) > 0 {
			r.seqs[src] = out
		}
		r.mark(out)
		return out, true
	}
	return initPlainValue(value)
}

func (r *initYAMLRefs) mark(v any) {
	if k, ok := initYAMLKeyOf(v); ok {
		r.made[k] = true
	}
}

// initYAMLStringify is serializeConfig (stringify(cfg, {lineWidth: 0})) for
// a value that may hold the same read object twice, or a << key: the plain
// text when neither is there, else the text with anchors and aliases.
func initYAMLStringify(value any, refs *initYAMLRefs) (string, error) {
	names := map[initYAMLKey]string{}
	if refs != nil {
		seen := map[initYAMLKey]bool{}
		var walk func(v any)
		walk = func(v any) {
			if k, ok := initYAMLKeyOf(v); ok && refs.made[k] {
				if seen[k] {
					if names[k] == "" {
						names[k] = fmt.Sprintf("a%d", len(names)+1)
					}
					return
				}
				seen[k] = true
			}
			switch x := v.(type) {
			case ledger.Object:
				for _, f := range x {
					walk(f.Value)
				}
			case []any:
				for _, item := range x {
					walk(item)
				}
			}
		}
		walk(value)
	}
	text, err := yamledit.StringifyPlain(value)
	if err == nil && len(names) == 0 {
		return text, nil
	}
	w := initYAMLWriter{names: names, seen: map[initYAMLKey]bool{}}
	out, err := w.node(value, "")
	if err != nil {
		return "", err
	}
	return out + "\n", nil
}

// initYAMLWriter is yamledit's plainNode/plainPair with yaml's anchor and
// alias props; scalars and keys go through yamledit, so they read the same.
type initYAMLWriter struct {
	names map[initYAMLKey]string
	seen  map[initYAMLKey]bool
}

func (w *initYAMLWriter) scalar(v any) (string, error) {
	text, err := yamledit.StringifyPlain(v)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(text, "\n"), nil
}

// node writes v's body: a scalar's text, or a collection's lines.
func (w *initYAMLWriter) node(v any, indent string) (string, error) {
	lines := []string{}
	switch x := v.(type) {
	case ledger.Object:
		keys, values := []string{}, map[string]any{}
		for _, f := range x {
			if f.Key != "<<" {
				if _, err := yamledit.StringifyPlain(ledger.Object{{Key: f.Key, Value: nil}}); err != nil {
					return "", err
				}
			}
			if _, again := values[f.Key]; !again {
				keys = append(keys, f.Key)
			}
			values[f.Key] = f.Value
		}
		for _, key := range keys {
			line, err := w.pair(key, values[key], indent)
			if err != nil {
				return "", err
			}
			lines = append(lines, line)
		}
		if len(lines) == 0 {
			return "{}", nil
		}
	case []any:
		for _, item := range x {
			switch item.(type) {
			case ledger.Object, []any:
				return "", fmt.Errorf("a collection inside a sequence")
			}
			text, err := w.scalar(item)
			if err != nil {
				return "", err
			}
			lines = append(lines, "- "+text)
		}
		if len(lines) == 0 {
			return "[]", nil
		}
	default:
		return w.scalar(v)
	}
	return strings.Join(lines, "\n"+indent), nil
}

// pair writes `key: value`, with `&aN` before a named collection's first
// appearance and `*aN` for each later one.
func (w *initYAMLWriter) pair(key string, value any, indent string) (string, error) {
	ctxIndent := indent + "  "
	props := ""
	if k, ok := initYAMLKeyOf(value); ok && w.names[k] != "" {
		if w.seen[k] {
			return key + ": *" + w.names[k], nil
		}
		w.seen[k] = true
		props = " &" + w.names[k]
	}
	body, err := w.node(value, ctxIndent)
	if err != nil {
		return "", err
	}
	collection := false
	switch x := value.(type) {
	case ledger.Object:
		collection = len(x) > 0
	case []any:
		collection = len(x) > 0
	}
	if collection {
		return key + ":" + props + "\n" + ctxIndent + body, nil
	}
	return key + ":" + props + " " + body, nil
}
