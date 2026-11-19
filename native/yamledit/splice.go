package yamledit

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"gopkg.in/yaml.v3"
)

// B781: the splice edit. The model above copies the yaml package's output
// byte for byte, so it serves only the shapes Node wrote that way; anything
// else (for example review.routes as a block list of mappings) was an
// *Unsupported hand-over to Node, a dead end once Node was removed. When the
// model refuses, Set and Remove fall back to a splice: only the lines of the
// one key the edit touches are rewritten, every other line (comments, blank
// lines, other shapes) stays byte for byte. The result must read back as the
// original value with only that key changed, or the edit is refused.

// spMapping is one block mapping on the edit path: its yaml node and the
// line (1-based, exclusive) where its parent pair's region ends.
type spMapping struct {
	node  *yaml.Node
	limit int
}

// spDoc is the text split into lines plus its yaml node tree.
type spDoc struct {
	lines   []string
	newline bool // the text ends with a newline
	root    *yaml.Node
}

func spParse(text string) (*spDoc, error) {
	if strings.Contains(text, "\r") {
		return nil, refuse("a CR line break")
	}
	var doc yaml.Node
	decoder := yaml.NewDecoder(strings.NewReader(text))
	if err := decoder.Decode(&doc); err != nil {
		return nil, refuse("the text does not parse")
	}
	var extra yaml.Node
	if decoder.Decode(&extra) != io.EOF {
		return nil, refuse("more than one document")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || doc.Content[0].Style&yaml.FlowStyle != 0 {
		return nil, refuse("a root that is not a block mapping")
	}
	if err := spPlain(doc.Content[0]); err != nil {
		return nil, err
	}
	d := &spDoc{root: doc.Content[0], newline: strings.HasSuffix(text, "\n")}
	d.lines = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for _, l := range d.lines {
		if l == "..." || strings.HasPrefix(l, "... ") {
			return nil, refuse("a document end marker")
		}
	}
	return d, nil
}

// spPlain refuses anchors, aliases, explicit tags and non-scalar keys
// anywhere in the tree: a splice cannot keep their meaning.
func spPlain(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" || n.Style&yaml.TaggedStyle != 0 {
		return refuse("an anchor, alias or tag")
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i < len(n.Content); i += 2 {
			if k := n.Content[i]; k.Kind != yaml.ScalarNode || k.Value == "<<" || k.Tag != "!!str" {
				return refuse("a key that is not a plain string")
			}
		}
	}
	for _, c := range n.Content {
		if err := spPlain(c); err != nil {
			return err
		}
	}
	return nil
}

func (d *spDoc) text() string {
	out := strings.Join(d.lines, "\n")
	if d.newline {
		out += "\n"
	}
	return out
}

// spLoose is a blank or comment-only line.
func spLoose(l string) bool {
	t := strings.TrimSpace(l)
	return t == "" || t[0] == '#'
}

// region is the 0-based line span [start, end) of pair i of m: its key line
// up to the next sibling's key line (or the parent's limit), less the
// trailing blank and comment lines, which stay where they are.
func (d *spDoc) region(m spMapping, i int) (start, end, limit int) {
	start = m.node.Content[2*i].Line - 1
	limit = m.limit - 1
	if 2*i+2 < len(m.node.Content) {
		limit = m.node.Content[2*i+2].Line - 1
	}
	end = limit
	for end > start+1 && spLoose(d.lines[end-1]) {
		end--
	}
	return start, end, limit
}

// keyComment is the comment on a pair's key line, kept on the new key line.
func keyComment(k, v *yaml.Node) string {
	if k.LineComment != "" {
		return k.LineComment
	}
	if v.Line == k.Line && v.Kind == yaml.ScalarNode {
		return v.LineComment
	}
	return ""
}

func (d *spDoc) replace(start, end int, with []string) {
	d.lines = append(d.lines[:start:start], append(with, d.lines[end:]...)...)
}

func spIndent(k *yaml.Node) string { return strings.Repeat(" ", k.Column-1) }

// spPair writes key: value at indent as block YAML lines.
func spPair(key string, value any, indent string) ([]string, error) {
	if !plainKey(key) {
		return nil, refuse("a key that is not a plain key")
	}
	switch v := value.(type) {
	case ledger.Object:
		if len(v) == 0 {
			return []string{indent + key + ": {}"}, nil
		}
		out := []string{indent + key + ":"}
		seen := map[string]bool{}
		for _, f := range v {
			if seen[f.Key] {
				return nil, refuse("a repeated key")
			}
			seen[f.Key] = true
			lines, err := spPair(f.Key, f.Value, indent+"  ")
			if err != nil {
				return nil, err
			}
			out = append(out, lines...)
		}
		return out, nil
	case []any:
		if len(v) == 0 {
			return []string{indent + key + ": []"}, nil
		}
		out := []string{indent + key + ":"}
		for _, item := range v {
			lines, err := spItem(item, indent+"  ")
			if err != nil {
				return nil, err
			}
			out = append(out, lines...)
		}
		return out, nil
	}
	text, err := spScalar(value)
	if err != nil {
		return nil, err
	}
	return []string{indent + key + ": " + text}, nil
}

// spItem writes one sequence item at indent.
func spItem(value any, indent string) ([]string, error) {
	var lines []string
	switch v := value.(type) {
	case ledger.Object:
		if len(v) == 0 {
			return []string{indent + "- {}"}, nil
		}
		for _, f := range v {
			more, err := spPair(f.Key, f.Value, indent+"  ")
			if err != nil {
				return nil, err
			}
			lines = append(lines, more...)
		}
	case []any:
		if len(v) == 0 {
			return []string{indent + "- []"}, nil
		}
		for _, item := range v {
			more, err := spItem(item, indent+"  ")
			if err != nil {
				return nil, err
			}
			lines = append(lines, more...)
		}
	default:
		text, err := spScalar(value)
		if err != nil {
			return nil, err
		}
		return []string{indent + "- " + text}, nil
	}
	lines[0] = indent + "- " + lines[0][len(indent)+2:]
	return lines, nil
}

func spScalar(value any) (string, error) {
	n, err := createNode(value, true)
	if err != nil {
		return "", err
	}
	if n.kind != kScalar || strings.ContainsAny(n.out, "\n") {
		return "", refuse("a value that is not a one-line scalar")
	}
	return n.out, nil
}

// spOrdered is the ordered value at segs (mappings as ledger.Object).
func spOrdered(text string, segs []string) (any, bool) {
	value, err := config.ParseOrdered(text)
	if err != nil {
		return nil, false
	}
	for _, seg := range segs {
		pairs, ok := value.([]config.Pair)
		if !ok {
			return nil, false
		}
		found := false
		for _, p := range pairs {
			if p.Key == seg {
				value, found = p.Value, true
			}
		}
		if !found {
			return nil, false
		}
	}
	return spLedger(value), true
}

func spLedger(value any) any {
	switch v := value.(type) {
	case []config.Pair:
		out := ledger.Object{}
		for _, p := range v {
			out.Set(p.Key, spLedger(p.Value))
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = spLedger(item)
		}
		return out
	}
	return value
}

// spSetOrdered sets segs in an ordered mapping (a copy of the path).
func spSetOrdered(obj ledger.Object, segs []string, value any) (ledger.Object, error) {
	out := append(ledger.Object{}, obj...)
	if len(segs) == 1 {
		out.Set(segs[0], value)
		return out, nil
	}
	child, has := ledger.Object(nil), false
	for _, f := range out {
		if f.Key == segs[0] {
			has = true
			c, ok := f.Value.(ledger.Object)
			if !ok {
				return nil, ErrNotMapping
			}
			child = c
		}
	}
	if !has {
		child = ledger.Object{}
	}
	next, err := spSetOrdered(child, segs[1:], value)
	if err != nil {
		return nil, err
	}
	out.Set(segs[0], next)
	return out, nil
}

// spNest is value under the keys segs, outermost first.
func spNest(segs []string, value any) any {
	for i := len(segs) - 1; i >= 0; i-- {
		value = ledger.Object{{Key: segs[i], Value: value}}
	}
	return value
}

// spFind is the index of key in m, or -1.
func spFind(m *yaml.Node, key string) int {
	found := -1
	for i := 0; i < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			found = i / 2
		}
	}
	return found
}

// spliceSet sets segs to value by rewriting only that key's lines.
func spliceSet(text string, segs []string, value any) (string, error) {
	if err := checkSegs(segs); err != nil {
		return "", err
	}
	d, err := spParse(text)
	if err != nil {
		return "", err
	}
	cur := spMapping{node: d.root, limit: len(d.lines) + 1}
	for i, seg := range segs {
		at := spFind(cur.node, seg)
		if at < 0 {
			// insert after the mapping's last pair, at its indent
			_, end, limit := d.region(cur, len(cur.node.Content)/2-1)
			indent := spIndent(cur.node.Content[0])
			lines, err := spPair(seg, spNest(segs[i+1:], value), indent)
			if err != nil {
				return "", err
			}
			// a comment indented deeper than the new key still belongs to
			// the pair before it: insert after it
			for j := end; j < limit; j++ {
				if t := strings.TrimLeft(d.lines[j], " "); strings.HasPrefix(t, "#") && len(d.lines[j])-len(t) > len(indent) {
					end = j + 1
				}
			}
			d.replace(end, end, lines)
			return spCheck(text, d.text(), segs, value, false)
		}
		k, v := cur.node.Content[2*at], cur.node.Content[2*at+1]
		start, end, limit := d.region(cur, at)
		write := func(newValue any) (string, error) {
			lines, err := spPair(seg, newValue, spIndent(k))
			if err != nil {
				return "", err
			}
			if c := keyComment(k, v); c != "" {
				lines[0] += " " + c
			}
			d.replace(start, end, lines)
			return spCheck(text, d.text(), segs, value, false)
		}
		if i == len(segs)-1 {
			return write(value)
		}
		switch {
		case v.Kind == yaml.MappingNode && v.Style&yaml.FlowStyle != 0:
			old, ok := spOrdered(text, segs[:i+1])
			obj, isObj := old.(ledger.Object)
			if !ok || !isObj {
				return "", refuse("a flow mapping Go cannot read in order")
			}
			next, err := spSetOrdered(obj, segs[i+1:], value)
			if err != nil {
				return "", err
			}
			return write(next)
		case v.Kind == yaml.MappingNode:
			cur = spMapping{node: v, limit: limit + 1}
		case v.Kind == yaml.SequenceNode:
			return "", refuse("a key path through a sequence")
		default:
			return "", ErrNotMapping
		}
	}
	return "", refuse("empty key path")
}

// spliceRemove deletes segs and up to prune emptied parents; a parent left
// empty and not pruned becomes {} as the yaml package writes it.
func spliceRemove(text string, segs []string, prune int) (string, error) {
	if err := checkSegs(segs); err != nil {
		return "", err
	}
	d, err := spParse(text)
	if err != nil {
		return "", err
	}
	chain := []spMapping{{node: d.root, limit: len(d.lines) + 1}}
	index := []int{}
	for i, seg := range segs {
		cur := chain[len(chain)-1]
		at := spFind(cur.node, seg)
		if at < 0 {
			if i == len(segs)-1 {
				return text, nil // deleteIn of a missing key changes nothing
			}
			return "", ErrNotMapping
		}
		index = append(index, at)
		if i == len(segs)-1 {
			break
		}
		v := cur.node.Content[2*at+1]
		switch {
		case v.Kind == yaml.MappingNode && v.Style&yaml.FlowStyle != 0:
			return "", refuse("a remove inside a flow mapping")
		case v.Kind == yaml.MappingNode:
			_, _, limit := d.region(cur, at)
			chain = append(chain, spMapping{node: v, limit: limit + 1})
		case v.Kind == yaml.SequenceNode:
			return "", refuse("a key path through a sequence")
		default:
			return "", ErrNotMapping
		}
	}
	// level is the depth of the pair to delete; climb while pruning an
	// emptied parent.
	level := len(segs) - 1
	for p := 0; p < prune && level > 0 && len(chain[level].node.Content) == 2; p++ {
		level--
	}
	cur := chain[level]
	if level > 0 && len(cur.node.Content) == 2 {
		// the parent empties and stays: write it as {}
		parent := chain[level-1]
		k, v := parent.node.Content[2*index[level-1]], parent.node.Content[2*index[level-1]+1]
		start, end, _ := d.region(parent, index[level-1])
		line := spIndent(k) + segs[level-1] + ": {}"
		if c := keyComment(k, v); c != "" {
			line += " " + c
		}
		d.replace(start, end, []string{line})
	} else {
		start, end, _ := d.region(cur, index[level])
		d.replace(start, end, nil)
	}
	return spCheck(text, d.text(), segs[:level+1], nil, true)
}

// spCheck is the safety net: out must read back as text's value with only
// segs set to value (or removed).
func spCheck(text, out string, segs []string, value any, remove bool) (string, error) {
	before, err := config.ParseYAML(text)
	if err != nil {
		return "", refuse("the text does not parse: " + err.Error())
	}
	var plain any
	if !remove {
		encoded, err := canon.Marshal(value)
		if err != nil {
			return "", refuse("value: " + err.Error())
		}
		if err := json.Unmarshal(encoded, &plain); err != nil {
			return "", refuse("value: " + err.Error())
		}
	}
	cur, ok := before.(map[string]any)
	if !ok {
		return "", refuse("a root that is not a mapping")
	}
	for i, seg := range segs {
		if i == len(segs)-1 {
			if remove {
				delete(cur, seg)
			} else {
				cur[seg] = plain
			}
			break
		}
		next, ok := cur[seg].(map[string]any)
		if !ok {
			if _, has := cur[seg]; has || remove {
				return "", ErrNotMapping
			}
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
	}
	want, err := canon.Marshal(before)
	if err != nil {
		return "", refuse("value: " + err.Error())
	}
	after, err := config.ParseYAML(out)
	if err != nil {
		return "", refuse("the written text does not parse: " + err.Error())
	}
	got, err := canon.Marshal(after)
	if err != nil || string(got) != string(want) {
		return "", refuse("the written text does not read back as the edited value")
	}
	return out, nil
}

// SetOrSplice is Set; a shape Set does not serve falls back to the splice
// edit. An *Unsupported error left means neither can edit the text safely
// (its Reason is the splice's: the shape the line edit would not touch).
func SetOrSplice(text string, segs []string, value any) (string, error) {
	out, err := Set(text, segs, value)
	if err == nil {
		return out, nil
	}
	return spFallback(err, func() (string, error) { return spliceSet(text, segs, value) })
}

// RemoveOrSplice is Remove with the same splice fallback.
func RemoveOrSplice(text string, segs []string, prune int) (string, error) {
	out, err := Remove(text, segs, prune)
	if err == nil {
		return out, nil
	}
	return spFallback(err, func() (string, error) { return spliceRemove(text, segs, prune) })
}

// spFallback runs the splice when the model refused the document.
func spFallback(err error, splice func() (string, error)) (string, error) {
	if !IsUnsupported(err) {
		return "", err
	}
	out, spliceErr := splice()
	if spliceErr == nil || errors.Is(spliceErr, ErrNotMapping) || IsUnsupported(spliceErr) {
		return out, spliceErr
	}
	return "", err
}
