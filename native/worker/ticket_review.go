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
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/reviewq"

	"crypto/sha256"
	"encoding/hex"
)

// ConfigRetry is loop.mjs CONFIG_RETRY_MS: configFor's wait before its one
// retry of a failed run-record read.
const ConfigRetry = 200 * time.Millisecond

// configFor is loop.mjs configFor: the ticket's config snapshot (its pin,
// or the run's hash on a retry), the boot config when the run has no record
// or no snapshot, and bad=true where snapshotFor throws (`config-snapshot`).
func (w *TicketRunner) configFor(id string, retry bool) (cfg map[string]any, bad bool, err error) {
	var rec ledger.Object
	var notObject bool
	for attempt := 1; ; attempt++ {
		var syntax bool
		rec, syntax, notObject, err = w.readRecordValue()
		if IsUnsupported(err) {
			return nil, false, err
		}
		if err == nil && !syntax {
			break
		}
		// configRecord: a transient read error gets ONE retry after CONFIG_RETRY_MS
		if attempt >= 2 {
			return nil, true, nil
		}
		sleep := w.Sleep
		if sleep == nil {
			sleep = time.Sleep
		}
		sleep(ConfigRetry)
	}
	if notObject {
		rec = nil
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
	// a nodeIOError is kept: the routing step (B90) and the route rows name it
	lines, err := w.ledgerLines()
	if err != nil {
		return nil, err
	}
	rows := []any{}
	for _, l := range lines {
		var v any
		if err := json.Unmarshal(l, &v); err != nil {
			return nil, unsupported("ledger line JSON.parse throws on")
		}
		rows = append(rows, v)
	}
	return rows, nil
}

// readObjects is readRows with each row's key order kept (the fix loop's
// view). A row that is not an object is left out: no row test matches one.
func (w *TicketRunner) readObjects() ([]ledger.Object, error) {
	out := []ledger.Object{}
	if w.ReadRows != nil {
		rows, err := w.ReadRows()
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if m, ok := r.(map[string]any); ok {
				data, err := json.Marshal(m)
				if err != nil {
					return nil, unsupported("ledger row encode")
				}
				o, err := ledger.DecodeObject(data)
				if err != nil {
					return nil, unsupported("ledger row decode")
				}
				out = append(out, o)
			}
		}
		return out, nil
	}
	lines, err := w.ledgerLines()
	if err != nil {
		return nil, keepHandover(err)
	}
	for _, l := range lines {
		var v any
		if err := json.Unmarshal(l, &v); err != nil {
			return nil, unsupported("ledger line JSON.parse throws on")
		}
		if _, ok := v.(map[string]any); !ok {
			continue
		}
		o, err := ledger.DecodeObject(l)
		if err != nil {
			return nil, unsupported("ledger row decode")
		}
		out = append(out, o)
	}
	return out, nil
}

// ledgerLines are the complete lines of the rotations in numeric order,
// then the current file.
func (w *TicketRunner) ledgerLines() ([][]byte, error) {
	dir := filepath.Join(w.Home, ".code-forge", "ledger")
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		if ne := nodeOpenError(err, "scandir"); ne != nil {
			return nil, ne
		}
		return nil, unsupported("ledger listing: " + err.Error())
	}
	// createTailReader opens the live file before the rotated ones
	if f, err := os.Open(filepath.Join(dir, w.Slug+".jsonl")); err == nil {
		_ = f.Close()
	} else if ne := nodeOpenError(err, "open"); ne != nil {
		return nil, ne
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
	lines := [][]byte{}
	for _, p := range parts {
		data, err := os.ReadFile(filepath.Join(dir, p.name))
		if os.IsNotExist(err) && p.n == 0 && p.name == w.Slug+".jsonl" {
			continue
		}
		if err != nil {
			if ne := nodeOpenError(err, "open"); ne != nil {
				return nil, ne
			}
			return nil, unsupported("ledger read: " + err.Error())
		}
		data = data[:bytes.LastIndexByte(data, '\n')+1]
		if !utf8.Valid(data) || loneSurrogate.Match(data) {
			return nil, unsupported("ledger bytes Node would decode with replacements")
		}
		for _, l := range bytes.Split(data, []byte("\n")) {
			if len(l) != 0 {
				lines = append(lines, l)
			}
		}
	}
	return lines, nil
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
			// B90: a failure the routing step names ends worker-error with its text
			var ne *nodeIOError
			if errors.As(err, &ne) {
				return nil, &StepError{Step: "routing", Name: "Error", Message: ne.message}
			}
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
	outcome, plan, err := w.engineRefusal(t, r.cfg, contentRoot, r.timeoutS)
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
	if plan != nil {
		return w.runEngine(plan, base)
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
		// Node's try around the read answers route-row for any throw
		var ne *nodeIOError
		if errors.As(err, &ne) {
			return false, nil
		}
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

// engineRefusal is engine.mjs reviewTicket's dry pass: an `excluded`,
// `unavailable` or `stale` outcome, the plan of a ticket whose review Go
// serves (planEngine), or ErrUnsupported. It writes nothing.
func (w *TicketRunner) engineRefusal(t ledger.Object, cfg map[string]any, contentRoot string, timeoutS float64) (ledger.Object, *enginePlan, error) {
	block, _ := t.Get("block").(string)
	file, _ := t.Get("file").(string)
	hash, _ := t.Get("content_hash").(string)
	out := func(status, reason string) ledger.Object {
		return ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: block}, {Key: "file", Value: file}, {Key: "content_hash", Value: hash},
			{Key: "status", Value: status}, {Key: "approved", Value: false}, {Key: "engine", Value: "adaptive"}, {Key: "reason", Value: reason}, {Key: "sessions", Value: []any{}}}
	}
	rule, err := w.exclusion(file, cfg)
	if err != nil {
		return nil, nil, err
	}
	if rule == "" && t.Get("kind") == "answers" {
		byFile, _ := t.Get("by_file").(string)
		if rule, err = w.exclusion(byFile, cfg); err != nil {
			return nil, nil, err
		}
	}
	if rule != "" {
		return out("excluded", "review.exclude "+rule), nil, nil
	}
	// packetDirFor: never null for an id readTicket accepted (24 hex)
	rec, syntax, err := w.readRecord()
	if err != nil {
		if !IsUnsupported(err) {
			err = unsupported("run record read inside the engine: " + err.Error())
		}
		return nil, nil, err
	}
	var revision any
	revisionSet := false
	if rec != nil || syntax {
		if syntax {
			return out("unavailable", "run-record-unreadable"), nil, nil
		}
		blocks, ok := rec.Get("blocks").(ledger.Object)
		if !ok {
			if _, isArr := rec.Get("blocks").([]any); isArr {
				return nil, nil, unsupported("blocks is an array")
			}
			return out("unavailable", "run-record-unreadable"), nil, nil
		}
		if !hasKey(blocks, block) {
			return out("unavailable", "block-unknown"), nil, nil
		}
		// entry?.base_sha of a value that is not an object is undefined
		entry, _ := blocks.Get(block).(ledger.Object)
		if sha, ok := entry.Get("base_sha").(string); !ok || sha == "" {
			return out("unavailable", "block-base-missing"), nil, nil
		}
		if wt := entry.Get("worktree"); wt != nil {
			if !worktreeRecordOK(wt) {
				return out("unavailable", "worktree-malformed"), nil, nil
			}
		}
		revision, revisionSet = entry.Get("acceptance_revision"), hasKey(entry, "acceptance_revision")
	}
	ticketRev, ticketSet := t.Get("acceptance_revision").(string)
	entryRev, entryIsString := revision.(string)
	same := (!ticketSet && !revisionSet) || (ticketSet && revisionSet && entryIsString && entryRev == ticketRev)
	// B609: an answers ticket skips the acceptance check (`!ticket.kind`)
	if !same && t.Get("kind") != "answers" {
		return out("stale", "acceptance-amended"), nil, nil
	}
	// treeTopOf and the contentRoot check read the same record contentRootFor
	// read: a record that changed between the two is left to Node
	root, code, err := w.contentRootFor(block)
	if err != nil {
		if !IsUnsupported(err) {
			err = unsupported("content root failure inside the engine: " + err.Error())
		}
		return nil, nil, err
	}
	if code != "" || canonicalPath(root) != canonicalPath(contentRoot) {
		return nil, nil, unsupported("run record changed between the content root and the engine")
	}
	if syntax || (rec != nil && !hasKey(rec, "blocks")) {
		return nil, nil, unsupported("run record changed between the content root and the engine")
	}
	if t.Get("kind") == "answers" {
		outcome, plan, err := w.ansDry(t, rec, cfg, contentRoot, timeoutS)
		if plan == nil {
			return outcome, nil, err
		}
		return nil, &enginePlan{answers: plan, workDir: plan.workDir}, nil
	}
	return w.planEngine(t, rec, cfg, contentRoot, timeoutS)
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
