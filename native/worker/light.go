package worker

// B545: engine.mjs lightRound — a light block's batch round. Under the
// `light:<run>\0<block>` lock: the block's changed owned files and their
// hashes, a cached batch outcome for this content, the two-batch cap (a
// third ticket enters the ordinary per-file loop), the reserved
// review.light_batch row, one batch session (review.LightBatch), then every
// file's outcome: review.round / review.approved rows, the file's queue
// ticket (enqueueHash), its review.result row and signed result, and the
// review.light_result row. The dry pass (lightDry) decides before anything
// is written whether Go serves the round; a batch the run phase reaches
// that the dry pass did not check is left to Node.

import (
	"path/filepath"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/review"
	"github.com/ricardov03/code-forge/native/session"
	"github.com/ricardov03/code-forge/native/sign"
)

// lightPlan is a light ticket's round as the dry pass left it.
type lightPlan struct {
	t                     ledger.Object
	block, file, hash, id string
	e                     blockEntry
	cfg                   map[string]any
	contentRoot           string
	digest, facts         string
	workDir               string
	sopts                 session.Opts
	sdeps                 session.Deps
	// batchChecked: the dry pass found a batch to run and checked it
	batchChecked bool
}

// lightState is lightRound's decision under its lock: an outcome (stale,
// cached), none (the ordinary loop), or a batch at round.
type lightState struct {
	outcome   ledger.Object
	ordinary  bool
	round     int
	files     []string
	hashes    []watchHash
	signature string
}

func (p *lightPlan) base() string {
	if p.e.base == nil {
		return "HEAD"
	}
	return *p.e.base
}

// lightDecide is lightRound from its file listing to the cap.
func (w *TicketRunner) lightDecide(p *lightPlan) (lightState, error) {
	all, ok, err := computeFileSet(p.contentRoot, p.base())
	if err != nil {
		return lightState{}, err
	}
	if !ok {
		return lightState{}, &StepError{Step: "light", Name: "Error", Message: "the change listing failed"}
	}
	st := lightState{files: []string{}}
	for _, f := range all {
		if !owned.OwnsFile(p.e.owned, p.e.ownedKinds, f) {
			continue
		}
		rule, err := w.exclusion(f, p.cfg)
		if err != nil {
			return lightState{}, err
		}
		if rule == "" {
			st.files = append(st.files, f)
		}
	}
	gitx.SortStrings(st.files)
	current := false
	list := []any{}
	for _, f := range st.files {
		h, err := gitx.ContentHash(p.contentRoot, f)
		if err != nil {
			return lightState{}, err
		}
		st.hashes = append(st.hashes, watchHash{f, h})
		list = append(list, ledger.Object{{Key: "file", Value: f}, {Key: "content_hash", Value: h}})
		if f == p.file && h == p.hash {
			current = true
		}
	}
	if !current {
		st.outcome = ledger.Object{{Key: "status", Value: "stale"}, {Key: "approved", Value: false}, {Key: "engine", Value: "adaptive"}, {Key: "sessions", Value: []any{}}}
		return st, nil
	}
	var base any
	if p.e.base != nil {
		base = *p.e.base
	}
	sig, err := ledger.Stringify([]any{base, list}, false)
	if err != nil {
		return lightState{}, err
	}
	st.signature = string(sig)
	rows, err := w.readObjects()
	if err != nil {
		return lightState{}, err
	}
	var cached ledger.Object
	count := 0
	for _, r := range rows {
		if r.Get("run") != w.RunID || r.Get("block") != p.block {
			continue
		}
		if v, err := sign.VerifyRow(r.Map(), w.Key); err != nil || !v.OK {
			continue
		}
		switch r.Get("event") {
		case "review.light_result":
			if r.Get("signature") == st.signature {
				outcomes, _ := r.Get("outcomes").(ledger.Object)
				cached, _ = outcomes.Get(p.file).(ledger.Object)
			}
		case "review.light_batch":
			count++
		}
	}
	if cached != nil && (cached.Get("status") == "reviewed" || IsDone(w.RepoRoot, p.id)) {
		st.outcome = cached
		return st, nil
	}
	if count >= 2 {
		st.ordinary = true
		return st, nil
	}
	st.round = count + 1
	return st, nil
}

func (p *lightPlan) input(files []string) review.LightInput {
	in := review.LightInput{RepoRoot: p.contentRoot, Files: files, Cfg: p.cfg, WorkDir: p.workDir, FactsExcerpt: p.facts, RulesDigest: p.digest}
	if p.e.base != nil {
		in.Base = *p.e.base
	}
	return in
}

// lightTree is the ticket's tree (enqueueHash's `tree`).
func (p *lightPlan) tree() *string {
	if s, ok := p.t.Get("tree").(string); ok && s != "" {
		return &s
	}
	return nil
}

// lightChecks is the batch's dry check: the diffs and packet, every
// session config, and every file's queue identity.
func (w *TicketRunner) lightChecks(p *lightPlan, st lightState) (*review.LightBatch, error) {
	batch, err := review.PrepareLight(p.input(st.files))
	if err != nil {
		return nil, err
	}
	if batch.ReadErr == nil {
		for _, c := range review.LightSpawnChecks(p.cfg) {
			o := p.sopts
			o.Cfg = c.Cfg
			if err := session.CheckReview(o, c.Level, c.Role); err != nil {
				return nil, unsupported("the light batch session: " + err.Error())
			}
		}
	}
	wt := &Watch{Runner: w, Clock: w.now}
	for _, h := range st.hashes {
		if _, err := wt.plan(p.block, h, p.tree()); err != nil {
			if _, ok := err.(*watchUnported); ok {
				return nil, unsupported("the light batch's queue ticket: " + err.Error())
			}
			return nil, err
		}
	}
	return batch, nil
}

// lightDry is the dry pass: ordinary true when the ticket enters the
// ordinary per-file loop now (the run phase still re-decides under the
// lock, like lightRound); ErrUnsupported when the batch it would run is
// not ported. A read that fails here is left for the run phase, where Node
// throws too.
func (w *TicketRunner) lightDry(p *lightPlan) (ordinary bool, err error) {
	st, err := w.lightDecide(p)
	if err != nil {
		if IsUnsupported(err) {
			return false, err
		}
		return false, nil
	}
	if st.ordinary {
		return true, nil
	}
	if st.outcome == nil {
		if _, err := w.lightChecks(p, st); err != nil {
			if IsUnsupported(err) {
				return false, err
			}
			return false, nil
		}
		p.batchChecked = true
	}
	return false, nil
}

// writeBlockRow is loop.mjs writeBlockRow: ledger({...row, block}) signed.
func (w *TicketRunner) writeBlockRow(block string, row ledger.Object) error {
	out := ledger.Object{{Key: "run", Value: w.RunID}}
	for _, f := range row {
		out.Set(f.Key, f.Value)
	}
	out.Set("block", block)
	signed, err := w.signObject(out)
	if err != nil {
		return err
	}
	return w.writeRow(signed)
}

// writeResultFor is queue.mjs writeResult for another ticket of the batch:
// the signed result and its done marker.
func (w *TicketRunner) writeResultFor(id string, result ledger.Object) error {
	out := result.Clone()
	measured := measuredOf(out)
	for _, f := range measured {
		out.Set(f.Key, f.Value)
	}
	if !ticketID.MatchString(id) {
		return &StepError{Step: "light", Name: "WorkerError", Message: "a ticket id is 24 lowercase hex characters"}
	}
	out.Set("run", w.RunID)
	out.Set("ticket", id)
	signed, err := w.signObject(out)
	if err != nil {
		return err
	}
	data, err := line(signed)
	if err != nil {
		return err
	}
	resultFile := filepath.Join(w.RepoRoot, ".code-forge", "reviews", w.RunID, id+".json")
	if err := writeAtomic(resultFile, data, 0o644); err != nil {
		return err
	}
	rel, _ := filepath.Rel(w.RepoRoot, resultFile)
	done := ledger.Object{{Key: "ticket", Value: id}, {Key: "status", Value: signed.Get("status")}}
	done = append(done, measured...)
	for _, k := range []string{"queued_at", "started_at", "finished_at", "duration_ms"} {
		if hasKey(signed, k) {
			done = append(done, ledger.Field{Key: k, Value: signed.Get(k)})
		}
	}
	if openFindingsMarked(signed) {
		done = append(done, ledger.Field{Key: "open_findings", Value: true})
	}
	done = append(done, ledger.Field{Key: "result", Value: rel})
	if data, err = line(done); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(QueueDir(w.RepoRoot), id+".done"), data, 0o644)
}

// lightRun is lightRound under its lock. ordinary true: the ticket enters
// the ordinary per-file loop (nothing written).
func (w *TicketRunner) lightRun(p *lightPlan) (outcome ledger.Object, ordinary bool, err error) {
	err = filelock.WithLock("light:"+w.RunID+"\x00"+p.block, func() error {
		outcome, ordinary, err = w.lightLocked(p)
		return err
	}, filelock.Options{})
	return outcome, ordinary, err
}

func (w *TicketRunner) lightLocked(p *lightPlan) (ledger.Object, bool, error) {
	st, err := w.lightDecide(p)
	if err != nil {
		return nil, false, err
	}
	if st.ordinary {
		return nil, true, nil
	}
	if st.outcome != nil {
		return st.outcome, false, nil
	}
	if !p.batchChecked {
		return nil, false, unsupported("a light batch the dry pass did not check")
	}
	batch, err := w.lightChecks(p, st)
	if err != nil {
		return nil, false, err
	}
	block := p.block
	write := func(row ledger.Object) error { return w.writeBlockRow(block, row) }
	// Reserve before the session: a crash cannot reset the cap on a worker restart.
	if err := write(ledger.Object{{Key: "event", Value: "review.light_batch"}, {Key: "round", Value: float64(st.round)}, {Key: "signature", Value: st.signature}}); err != nil {
		return nil, false, err
	}
	deps := review.Deps{Spawn: w.pooled(p.id, p.cfg, session.ReviewSpawn(p.sopts, p.sdeps)), WriteRow: write, Warn: w.Warn, ReadRows: w.readRows}
	res, err := batch.Run(deps)
	if err != nil {
		return nil, false, err
	}
	moved := false
	for _, h := range st.hashes {
		now, err := gitx.ContentHash(p.contentRoot, h.file)
		if err != nil {
			return nil, false, err
		}
		if now != h.hash {
			moved = true
		}
	}
	findings, _ := res.Get("findings").([]any)
	blocking := false
	for _, f := range findings {
		s := f.(ledger.Object).Get("severity")
		if s == "critical" || s == "warning" {
			blocking = true
		}
	}
	reviewed := res.Get("status") == "reviewed"
	outcomes := ledger.Object{}
	wt := &Watch{Runner: w, Clock: w.now}
	for _, h := range st.hashes {
		own := []any{}
		allNit := true
		openAfter := 0
		for _, f := range findings {
			fo := f.(ledger.Object)
			if fo.Get("file") != h.file {
				continue
			}
			own = append(own, f)
			if fo.Get("severity") != "nit" {
				allNit = false
				openAfter++
			}
		}
		approved := !moved && reviewed && (res.Get("approved") == true || blocking) && allNit
		action := "fix"
		if approved {
			action = "complete"
		}
		outcome := res.Clone()
		if moved {
			outcome.Set("status", "stale")
		}
		outcome.Set("approved", approved)
		outcome.Set("findings", own)
		outcome.Set("round", float64(st.round))
		outcome.Set("kind", "full")
		outcome.Set("level", p.e.level)
		outcome.Set("next", ledger.Object{{Key: "action", Value: action}, {Key: "level", Value: p.e.level}})
		outcomes.Set(h.file, outcome)
		if reviewed && !moved {
			if err := write(ledger.Object{{Key: "event", Value: "review.round"}, {Key: "file", Value: h.file}, {Key: "content_hash", Value: h.hash},
				{Key: "round", Value: float64(st.round)}, {Key: "kind", Value: "full"}, {Key: "level", Value: p.e.level}, {Key: "open_after", Value: float64(openAfter)}}); err != nil {
				return nil, false, err
			}
			if approved {
				if err := write(ledger.Object{{Key: "event", Value: "review.approved"}, {Key: "file", Value: h.file}, {Key: "content_hash", Value: h.hash}}); err != nil {
					return nil, false, err
				}
			}
		}
		// enqueueHash at this point: the queue may have changed since the check
		plan, err := wt.plan(block, h, p.tree())
		if err != nil {
			if _, ok := err.(*watchUnported); ok {
				return nil, false, &StepError{Step: "light", Name: "Error", Message: err.Error()}
			}
			return nil, false, err
		}
		if err := wt.enqueue(QueueDir(w.RepoRoot), plan); err != nil {
			return nil, false, err
		}
		if plan.excluded {
			continue
		}
		open := own
		if approved {
			open = []any{}
		}
		if err := write(ledger.Object{{Key: "event", Value: "review.result"}, {Key: "ticket", Value: plan.id}, {Key: "file", Value: h.file}, {Key: "content_hash", Value: h.hash},
			{Key: "status", Value: outcome.Get("status")}, {Key: "approved", Value: approved}, {Key: "open", Value: open}, {Key: "worker", Value: w.Worker}}); err != nil {
			return nil, false, err
		}
		result := outcome.Clone()
		result.Set("block", block)
		result.Set("file", h.file)
		result.Set("content_hash", h.hash)
		result.Set("open", open)
		result.Set("worker", w.Worker)
		if err := w.writeResultFor(plan.id, result); err != nil {
			return nil, false, err
		}
	}
	if err := write(ledger.Object{{Key: "event", Value: "review.light_result"}, {Key: "signature", Value: st.signature}, {Key: "outcomes", Value: outcomes}}); err != nil {
		return nil, false, err
	}
	out, _ := outcomes.Get(p.file).(ledger.Object)
	return out, false, nil
}
