package gatecheck

// B609: the worker's answers check (gate-check.mjs answerableFindings and
// ticketAnswered), read by the same rules as the close.

// AnswerableOpen is answerableFindings: the ids of file still open at hash
// that an answer may clear, in the Map's insertion order, with the severity
// read for each. ticket: that answers ticket's own rows do not close an id.
// Unknown is true when a value needed an engine detail Go cannot reproduce.
func AnswerableOpen(in Input, file, hash, ticket string) (ids []string, severity map[string]any, unknown bool) {
	if in.ProofFiles == nil {
		in.ProofFiles = in.Files
	}
	g := newGate(in)
	g.except = &ticket
	named := &namedOpen{sev: map[string]any{}}
	l := g.latestOf(file, hash)
	if l.latest != nil && l.latestRow != nil && l.latestRow["content_hash"] == hash && g.ok(l.latestRow) {
		for _, f := range l.latest.open {
			named.set(f.id, f.severity)
		}
	}
	for _, id := range g.capIDsAt(file, hash) {
		named.set(id, g.refusalSeverity(file, hash, id))
	}
	severity = map[string]any{}
	for _, id := range named.ids {
		sev := named.sev[id]
		if id == "proof" || !answerable(sev) {
			continue
		}
		if by, _ := g.waiverOf(file, id, sev, sev); by != "" {
			continue
		}
		ids = append(ids, id)
		severity[id] = sev
	}
	return ids, severity, g.unknown
}

// TicketAnswered is ticketAnswered: the findings of file the answers ticket
// already answered by a signed row that is still current.
func TicketAnswered(in Input, file, ticket string) map[string]bool {
	if in.ProofFiles == nil {
		in.ProofFiles = in.Files
	}
	g := newGate(in)
	out := map[string]bool{}
	now, has := g.current[file]
	if !has {
		return out
	}
	for _, r := range g.answers {
		by, isString := r["by_file"].(string)
		if r["file"] != file || r["content_hash"] != now || !isString || by == file || r["ticket"] != ticket {
			continue
		}
		if target, ok := g.current[by]; !ok || r["by_hash"] != target {
			continue
		}
		if id, ok := r["finding"].(string); ok {
			out[id] = true
		}
	}
	return out
}
