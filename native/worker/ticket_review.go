package worker

// B492: loop.mjs reviewWith (B90 routing and the signed `review.route` rows)
// and the refusals at the top of engine.mjs reviewTicket (review.exclude, the
// block entry, an amended acceptance). Node writes the route rows BEFORE the
// engine runs, so Go decides the engine's refusal first, from reads only, and
// writes the rows and the result only when that refusal is the outcome. A
// ticket that would reach a review session returns ErrUnsupported with
// nothing written.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/reviewq"

	"crypto/sha256"
	"encoding/hex"
)

// configFor is loop.mjs configFor: the ticket's config snapshot (its pin,
// or the run's hash on a retry), the boot config when the run has no record
// or no snapshot, and bad=true where snapshotFor throws (`config-snapshot`).
func (w *TicketRunner) configFor(id string, retry bool) (cfg map[string]any, bad bool, err error) {
	rec, syntax, err := w.readRecord()
	if err != nil {
		return nil, false, err
	}
	if syntax {
		return nil, false, unsupported("run record unreadable at the config read (Node retries)")
	}
	boot := func() (map[string]any, bool, error) {
		if w.Cfg == nil {
			return nil, false, unsupported("no boot config")
		}
		return w.Cfg, false, nil
	}
	st, ok := rec.Get("config").(ledger.Object)
	if !ok {
		return boot()
	}
	hash, ok := st.Get("hash").(string)
	if !ok {
		return boot()
	}
	key := id
	if retry {
		key = ""
	}
	if pins, ok := st.Get("pins").(ledger.Object); ok {
		if pin, ok := pins.Get(key).(string); ok {
			hash = pin
		}
	}
	snaps, _ := st.Get("snapshots").(ledger.Object)
	snap, ok := snaps.Get(hash).(ledger.Object)
	if !ok {
		return nil, true, nil
	}
	canonical, err := canon.Marshal(snap)
	if err != nil {
		return nil, false, unsupported("config canonical form")
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, true, nil
	}
	m, err := plainMap(snap)
	if err != nil {
		return nil, false, unsupported("config snapshot form")
	}
	return m, false, nil
}

func plainMap(o ledger.Object) (map[string]any, error) {
	data, err := ledger.Stringify(o, false)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// readRows is the worker's ledger view (ledger/tail.mjs createTailReader):
// the rotations in numeric order, then the current file without its
// unfinished last line. Any line Node's JSON.parse could read differently,
// or any read error, is ErrUnsupported (Node throws).
func (w *TicketRunner) readRows() ([]any, error) {
	if w.ReadRows != nil {
		return w.ReadRows()
	}
	dir := filepath.Join(w.Home, ".code-forge", "ledger")
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return nil, unsupported("ledger listing: " + err.Error())
	}
	rotated := regexp.MustCompile(`^` + regexp.QuoteMeta(w.Slug) + `\.(\d+)\.jsonl$`)
	type part struct {
		name string
		n    float64
	}
	parts := []part{}
	for _, e := range entries {
		if m := rotated.FindStringSubmatch(e.Name()); m != nil {
			n, _ := strconv.ParseFloat(m[1], 64)
			parts = append(parts, part{e.Name(), n})
		}
	}
	sort.SliceStable(parts, func(i, j int) bool { return parts[i].n < parts[j].n })
	parts = append(parts, part{name: w.Slug + ".jsonl"})
	rows := []any{}
	for _, p := range parts {
		data, err := os.ReadFile(filepath.Join(dir, p.name))
		if os.IsNotExist(err) && p.n == 0 && p.name == w.Slug+".jsonl" {
			continue
		}
		if err != nil {
			return nil, unsupported("ledger read: " + err.Error())
		}
		data = data[:bytes.LastIndexByte(data, '\n')+1]
		if !utf8.Valid(data) || loneSurrogate.Match(data) {
			return nil, unsupported("ledger bytes Node would decode with replacements")
		}
		for _, l := range bytes.Split(data, []byte("\n")) {
			if len(l) == 0 {
				continue
			}
			var v any
			if err := json.Unmarshal(l, &v); err != nil {
				return nil, unsupported("ledger line JSON.parse throws on")
			}
			rows = append(rows, v)
		}
	}
	return rows, nil
}

// reviewWith is loop.mjs reviewWith up to the engine call, plus the engine
// refusals Go serves. base(status, reason) is reviewOne's outcome shape.
func (w *TicketRunner) reviewWith(id string, t ledger.Object, cfg map[string]any, contentRoot string, data []byte, base func(string, string) ledger.Object) (ledger.Object, error) {
	timeoutS := float64(defaultSessionTimeoutS)
	if v := reviewOf(cfg)["session_timeout_s"]; isPositiveInt(v) {
		timeoutS = v.(float64)
	}
	lines, deleted := countLines(data), data == nil
	var r routed
	var err error
	adaptive, _ := obj(reviewOf(cfg)["adaptive"])
	levels, _ := obj(cfg["levels"])
	l2, _ := obj(levels["L2"])
	if (adaptive != nil && adaptive["enabled"] == false) || !truthy(l2["model"]) {
		r, err = explicitRoutes(cfg, lines, deleted, timeoutS)
	} else {
		var rows []any
		if rows, err = w.readRows(); err != nil {
			return nil, err
		}
		r, err = adaptiveRoutes(cfg, lines, deleted, timeoutS, rows, id, w.RunID, w.now())
	}
	if errors.Is(err, errRouteInvalid) {
		return base("unavailable", "route-invalid"), nil
	}
	if err != nil {
		return nil, unsupported("routing: " + err.Error())
	}
	outcome, err := w.engineRefusal(t, cfg, contentRoot)
	if err != nil {
		return nil, err
	}
	if len(r.rows) > 0 {
		ok, err := w.writeRouteRows(id, t, r.rows)
		if err != nil {
			return nil, err
		}
		if !ok {
			return base("unavailable", "route-row"), nil
		}
	}
	return outcome, nil
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	}
	return true
}

// writeRouteRows writes the signed rows of the levels this ticket has no row
// for yet; false where Node answers `route-row` (a row that cannot be
// written). A ledger Go cannot read the way Node does is ErrUnsupported, and
// nothing is written.
func (w *TicketRunner) writeRouteRows(id string, t ledger.Object, rows []ledger.Object) (bool, error) {
	existing, err := w.readRows()
	if err != nil {
		return false, err
	}
	have := map[string]bool{}
	for _, r := range existing {
		if m, ok := obj(r); ok && m["event"] == "review.route" && m["run"] == w.RunID && m["ticket"] == id {
			if level, ok := m["level"].(string); ok {
				have[level] = true
			}
		}
	}
	for _, row := range rows {
		if level, _ := row.Get("level").(string); have[level] {
			continue
		}
		out := ledger.Object{{Key: "run", Value: w.RunID}}
		out = append(out, row...)
		out = append(out, ledger.Field{Key: "ticket", Value: id})
		for _, k := range []string{"block", "file", "content_hash"} {
			out = append(out, ledger.Field{Key: k, Value: t.Get(k)})
		}
		signed, err := w.signObject(out)
		if err != nil || w.writeRow(signed) != nil {
			return false, nil
		}
	}
	return true, nil
}

// engineRefusal is the top of engine.mjs reviewTicket: an `excluded`,
// `unavailable` or `stale` outcome, or ErrUnsupported when the ticket would
// go on to a review session.
func (w *TicketRunner) engineRefusal(t ledger.Object, cfg map[string]any, contentRoot string) (ledger.Object, error) {
	block, _ := t.Get("block").(string)
	file, _ := t.Get("file").(string)
	hash, _ := t.Get("content_hash").(string)
	out := func(status, reason string) ledger.Object {
		return ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: block}, {Key: "file", Value: file}, {Key: "content_hash", Value: hash},
			{Key: "status", Value: status}, {Key: "approved", Value: false}, {Key: "engine", Value: "adaptive"}, {Key: "reason", Value: reason}, {Key: "sessions", Value: []any{}}}
	}
	rule, err := w.exclusion(file, cfg)
	if err != nil {
		return nil, err
	}
	if rule != "" {
		return out("excluded", "review.exclude "+rule), nil
	}
	// packetDirFor: never null for an id readTicket accepted (24 hex)
	rec, syntax, err := w.readRecord()
	if err != nil {
		return nil, err
	}
	var revision any
	revisionSet := false
	if rec != nil || syntax {
		if syntax {
			return out("unavailable", "run-record-unreadable"), nil
		}
		blocks, ok := rec.Get("blocks").(ledger.Object)
		if !ok {
			if _, isArr := rec.Get("blocks").([]any); isArr {
				return nil, unsupported("blocks is an array")
			}
			return out("unavailable", "run-record-unreadable"), nil
		}
		if !hasKey(blocks, block) {
			return out("unavailable", "block-unknown"), nil
		}
		// entry?.base_sha of a value that is not an object is undefined
		entry, _ := blocks.Get(block).(ledger.Object)
		if sha, ok := entry.Get("base_sha").(string); !ok || sha == "" {
			return out("unavailable", "block-base-missing"), nil
		}
		if wt := entry.Get("worktree"); wt != nil {
			if !worktreeRecordOK(wt) {
				return out("unavailable", "worktree-malformed"), nil
			}
		}
		revision, revisionSet = entry.Get("acceptance_revision"), hasKey(entry, "acceptance_revision")
	}
	ticketRev, ticketSet := t.Get("acceptance_revision").(string)
	entryRev, entryIsString := revision.(string)
	same := (!ticketSet && !revisionSet) || (ticketSet && revisionSet && entryIsString && entryRev == ticketRev)
	if !same {
		return out("stale", "acceptance-amended"), nil
	}
	// treeTopOf and the contentRoot check read the same record contentRootFor
	// read: a record that changed between the two is left to Node
	root, code, err := w.contentRootFor(block)
	if err != nil {
		return nil, err
	}
	if code != "" || canonicalPath(root) != canonicalPath(contentRoot) {
		return nil, unsupported("run record changed between the content root and the engine")
	}
	return nil, unsupported("the review engine (context digest, fix loop, sessions) is not ported")
}

var objectID = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)

// worktreeRecordOK is proof/worktree.mjs worktreeRecord's shape check.
func worktreeRecordOK(v any) bool {
	o, ok := v.(ledger.Object)
	if !ok {
		return false
	}
	p, ok1 := o.Get("path").(string)
	b, ok2 := o.Get("branch").(string)
	sha, ok3 := o.Get("base_sha").(string)
	return ok1 && ok2 && ok3 && filepath.IsAbs(p) && !controlChar.MatchString(p) && b != "" && !controlChar.MatchString(b) && objectID.MatchString(sha)
}

var controlChar = regexp.MustCompile(`[\x00-\x1f\x7f]`)

// exclusion is queue.mjs reviewExclusionFor: the rule in force for the run
// (configInForce, no ticket pin), else the ticket's config; none when the
// snapshot in force does not match its hash.
func (w *TicketRunner) exclusion(file string, ticketCfg map[string]any) (string, error) {
	data, err := os.ReadFile(filepath.Join(w.Home, ".code-forge", "runs", w.RunID+".json"))
	var record any
	if err == nil && utf8.Valid(data) && !loneSurrogate.Match(data) {
		if json.Unmarshal(data, &record) != nil {
			record = nil
		}
	} else if err == nil {
		return "", unsupported("run record bytes Node would decode with replacements")
	} else if !os.IsNotExist(err) {
		return "", unsupported("run record read: " + err.Error())
	}
	if !truthy(record) {
		// readJSON(...) ?? the repository's own copy
		if _, err := os.Lstat(filepath.Join(w.RepoRoot, ".code-forge", "runs", w.RunID+".json")); err == nil {
			return "", unsupported("repository run record fallback")
		}
	}
	cfg := ticketCfg
	if _, ok := obj(record); ok {
		inForce, bad, err := w.configInForce(data)
		if err != nil {
			return "", err
		}
		if bad {
			return "", nil
		}
		if inForce != nil {
			cfg = inForce
		}
	}
	rule, ok := reviewq.Exclusion(cfg, file)
	if !ok {
		return "", unsupported("review.exclude Go cannot match")
	}
	return rule, nil
}

// configInForce: snapshotFor(record, ”) — nil for no snapshot, bad when it
// does not match its hash.
func (w *TicketRunner) configInForce(data []byte) (map[string]any, bool, error) {
	rec, err := ledger.DecodeObject(data)
	if err != nil {
		return nil, false, unsupported("run record decode")
	}
	st, ok := rec.Get("config").(ledger.Object)
	if !ok {
		return nil, false, nil
	}
	hash, ok := st.Get("hash").(string)
	if !ok {
		return nil, false, nil
	}
	if pins, ok := st.Get("pins").(ledger.Object); ok {
		if pin, ok := pins.Get("").(string); ok {
			hash = pin
		}
	}
	snaps, _ := st.Get("snapshots").(ledger.Object)
	snap, ok := snaps.Get(hash).(ledger.Object)
	if !ok {
		return nil, true, nil
	}
	canonical, err := canon.Marshal(snap)
	if err != nil {
		return nil, false, unsupported("config canonical form")
	}
	sum := sha256.Sum256(canonical)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, true, nil
	}
	m, err := plainMap(snap)
	if err != nil {
		return nil, false, unsupported("config snapshot form")
	}
	return m, false, nil
}
