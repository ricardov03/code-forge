package yamledit

// RemoveSpliceFirst is RemoveOrSplice the other way round (B791): the
// splice edit first, so every line but the removed key's stays byte for
// byte, and the model edit (which writes the document as the yaml package
// does, flow sequences spaced) only when the splice refuses the shape. An
// *Unsupported error left means neither can edit the text safely.
func RemoveSpliceFirst(text string, segs []string, prune int) (string, error) {
	out, err := spliceRemove(text, segs, prune)
	if err == nil || !IsUnsupported(err) {
		return out, err
	}
	if modelOut, modelErr := Remove(text, segs, prune); modelErr == nil {
		return modelOut, nil
	}
	return "", err
}
