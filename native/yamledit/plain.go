package yamledit

import "strings"

// StringifyPlain is the yaml package's stringify(value, {lineWidth: 0}) for
// a JSON value built in Go (a ledger.Object keeps its key order, as a JS
// object does): what serializeConfig in src/install/wizard/project-file.mjs
// writes. The served subset is the one createNode serves (plain keys,
// printable ASCII strings, finite numbers, sequences of scalars); any other
// value returns an *Unsupported error before anything is written. With
// lineWidth 0 yaml never folds a line, so no length limit applies.
func StringifyPlain(value any) (string, error) {
	n, err := createNode(value)
	if err != nil {
		return "", err
	}
	return plainNode(n, "") + "\n", nil
}

// plainNode is stringifyNode without comments, flow input or folding.
func plainNode(n *node, indent string) string {
	if n.kind == kScalar {
		return n.out
	}
	lines := []string{}
	if n.kind == kMap {
		for _, p := range n.pairs {
			lines = append(lines, plainPair(p, indent))
		}
	} else {
		for _, item := range n.items {
			lines = append(lines, "- "+plainNode(item, indent+"  "))
		}
	}
	if len(lines) == 0 {
		if n.kind == kSeq {
			return "[]"
		}
		return "{}"
	}
	return strings.Join(lines, "\n"+indent)
}

// plainPair is stringifyPair for a plain key and an undecorated value.
func plainPair(p *pair, indent string) string {
	ctxIndent := indent + "  "
	value := p.value
	valueStr := plainNode(value, ctxIndent)
	ws := " "
	if value.kind != kScalar && len(value.pairs)+len(value.items) > 0 {
		ws = "\n" + ctxIndent
	}
	return p.key + ":" + ws + valueStr
}
