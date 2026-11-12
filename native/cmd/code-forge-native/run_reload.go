package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B473: `run reload` with a config that loads, in Go (src/cli/run.mjs
// reloadVerb → src/autopilot/limits.mjs reloadWorkspace → src/state/run.mjs
// reloadRun). runRun (run_cmd.go) parses the flags and runReloadPlan
// (run_start.go) answers the record and config-load refusals; it calls
// runReloadLoadedPlan once the config loads.

// runReloadNow is the clock seam for reloaded_at and the autopilot grant check.
var runReloadNow = time.Now

// reloadInput is what reloadWorkspace reads before it takes the run lock.
type reloadInput struct {
	home, runID, repoRoot string
	cfg                   ledger.Object
	effectiveSlug         string
	now                   time.Time
}

// rrWriter sends a write to runOut, so the last stderr text reaches the error log.
type rrWriter struct {
	o      *runOut
	stderr bool
}

func (w rrWriter) Write(p []byte) (int, error) {
	if w.stderr {
		w.o.err(string(p))
	} else {
		w.o.out(string(p))
	}
	return len(p), nil
}

// runReloadLoadedPlan is reloadWorkspace after loadProjectConfig succeeded:
// validation, the guards and the plan are read before the run lock; a case Go
// does not reproduce keeps its named hand-over.
func runReloadLoadedPlan(home, runID string, record ledger.Object, workspace string) (func(o *runOut) int, handoverReason) {
	if rrWorkspaceHoldsRuns(home, workspace) {
		return nil, hRunReloadWorkspace
	}
	root := config.ProjectRootFor(workspace)
	raw, readErr := os.ReadFile(filepath.Join(root, config.Filename))
	if readErr == nil && strings.Contains(string(raw), "!!") {
		// a tagged scalar can load as a Date, a byte array, a Set or a Map in Node
		return nil, hRunReloadYAMLTag
	}
	cfgMap, order, err := config.LoadOrdered(workspace)
	if err != nil {
		// LoadSnapshot loaded the same file a moment ago
		return nil, hRunReloadConfigError
	}
	if len(config.SchemaIssues(cfgMap, "")) > 0 {
		return nil, hRunReloadSchemaIssues
	}
	if configHoldsSecretShape(cfgMap) {
		return nil, hRunReloadSecretValues
	}
	if sandbox, ok := objAt(cfgMap, "coder", "sandbox"); ok {
		if dirs, ok := sandbox["add_dirs"].([]any); ok && len(dirs) > 0 {
			return nil, hRunReloadSandboxDirs
		}
	}
	v := configValidator{cfg: cfgMap, order: order, seen: config.LoadSeenInCache(home), hasCli: hasCliOnPath}
	if errs, _ := v.run(); len(errs) > 0 {
		return func(o *runOut) int {
			var lines strings.Builder
			for _, issue := range errs {
				lines.WriteString("  [" + issue.rule + "]\n")
			}
			noun := "errors"
			if len(errs) == 1 {
				noun = "error"
			}
			o.err(fmt.Sprintf("run reload: %s is invalid (%d %s) — nothing changed; run code-forge validate\n%s", config.Filename, len(errs), noun, lines.String()))
			return 1
		}, 0
	}
	cfg, _ := order.Object(cfgMap).(ledger.Object)
	in := reloadInput{home: home, runID: runID, cfg: cfg, effectiveSlug: planSlugFor(cfgMap, workspace), now: runReloadNow()}
	if top, err := gitx.RepoTop(workspace); err == nil {
		in.repoRoot = top
	}
	// every refusal Go cannot word is decided here, before any side effect
	pending, _ := reloadPendingTickets(in.repoRoot)
	if _, _, reason, ok := planReload(record, in, pending); !ok {
		return nil, reason
	}
	return func(o *runOut) int {
		return runReloadLocked([]string{"reload", "--run", runID}, in, rrWriter{o, false}, rrWriter{o, true})
	}, 0
}

// rrWorkspaceHoldsRuns is the case where state.SaveRun refuses a record that
// Node's saveRun would write.
func rrWorkspaceHoldsRuns(home, workspace string) bool {
	canonical := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			p = resolved
		}
		abs, _ := filepath.Abs(p)
		return abs
	}
	rel, err := filepath.Rel(canonical(workspace), filepath.Join(canonical(home), ".code-forge", "runs"))
	return err != nil || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel))
}

type reloadOutcome struct {
	changed  []string
	legacy   bool
	noChange bool
	oldHash  any
	newHash  string
	pinned   int
	kept     []string
	rowError string
}

func runReloadLocked(args []string, in reloadInput, stdout, stderr io.Writer) int {
	var out reloadOutcome
	var message string
	race := handoverReason(0)
	err := state.WithRunLock(in.home, in.runID, func() error {
		current, err := state.ReadRun(in.home, in.runID)
		if err != nil {
			return err
		}
		// readPending runs after the guard; planReload checks the guard first
		plan, msg, reason, ok := planReload(current, in, nil)
		if !ok {
			race = reason
			return nil
		}
		if msg != "" {
			message = msg
			return nil
		}
		if plan.noChange {
			out = reloadOutcome{noChange: true}
			return nil
		}
		pending, err := reloadPendingTickets(in.repoRoot)
		if err != nil {
			message = err.Error()
			return nil
		}
		plan, msg, reason, ok = planReload(current, in, pending)
		if !ok || msg != "" {
			race = reason
			return nil
		}
		pins := plan.pins
		snapshots := ledger.Object{{Key: plan.newHash, Value: plan.config}}
		for _, field := range pins {
			hash := field.Value.(string)
			value, _ := rrObjGet(plan.stored, hash)
			snapshots.Set(hash, value)
		}
		current.Set("config", ledger.Object{
			{Key: "hash", Value: plan.newHash}, {Key: "snapshots", Value: snapshots}, {Key: "pins", Value: pins},
			{Key: "reloaded_at", Value: in.now.UTC().Format("2006-01-02T15:04:05.000Z")},
		})
		current.Set("provider", rrJSOr(plan.config, nil, "provider"))
		current.Set("levels", rrJSOr(plan.config, nil, "levels"))
		current.Set("fallback_mode", rrJSOr(plan.config, "rules", "system1", "fallback"))
		if err := state.SaveRun(in.home, current); err != nil {
			return err
		}
		out = reloadOutcome{changed: plan.changed, legacy: plan.legacy, oldHash: plan.oldHash, newHash: plan.newHash, pinned: len(pins), kept: plan.kept}
		var changed any
		if !plan.legacy {
			list := make([]any, len(plan.changed))
			for i, key := range plan.changed {
				list[i] = key
			}
			changed = list
		}
		row := ledger.Object{
			{Key: "event", Value: "run.reload"}, {Key: "changed_keys", Value: changed},
			{Key: "old_hash", Value: plan.oldHash}, {Key: "new_hash", Value: plan.newHash},
			{Key: "pinned_tickets", Value: float64(len(pins))},
		}
		if plan.kept != nil {
			kept := make([]any, len(plan.kept))
			for i, key := range plan.kept {
				kept[i] = key
			}
			row = append(row, ledger.Field{Key: "kept_keys", Value: kept})
		}
		if _, err := state.WriteSigned(in.home, in.runID, row); err != nil {
			out.rowError = rrRowErrorCode(err)
		}
		return nil
	})
	if race != 0 {
		// the record changed between the check and the lock into a shape Go
		// does not reproduce; nothing was written, so Node runs it from the top
		return runHandover(args, race)
	}
	if err != nil {
		var stateErr *state.Error
		if errors.As(err, &stateErr) {
			message = stateErr.Message
		} else {
			message = err.Error()
		}
	}
	if message != "" {
		fmt.Fprintf(stderr, "run reload: %s\n", message)
		return 1
	}
	if out.noChange {
		fmt.Fprintf(stdout, "run %s: no config change\n", in.runID)
		return 0
	}
	if out.legacy {
		fmt.Fprintf(stdout, "run %s: config snapshot recorded (the run predates run reload; no earlier snapshot to compare)\n", in.runID)
	} else {
		suffix := "s"
		if len(out.changed) == 1 {
			suffix = ""
		}
		fmt.Fprintf(stdout, "run %s: config reloaded · %d key%s changed\n", in.runID, len(out.changed), suffix)
		for _, key := range out.changed {
			fmt.Fprintf(stdout, "  %s\n", key)
		}
	}
	if len(out.kept) > 0 {
		fmt.Fprintf(stdout, "kept frozen keys from the run snapshot: %s\n", strings.Join(out.kept, ", "))
	}
	fmt.Fprintf(stdout, "queued review tickets (%d) keep the config they were enqueued with; new tickets use the new config\n", out.pinned)
	if out.rowError != "" {
		fmt.Fprintf(stderr, "run reload: WARN the run.reload ledger row could not be written (%s); the run record holds the new config\n", out.rowError)
	}
	return 0
}

// rrRowErrorCode is err.code ?? err.name ?? 'error' for a failed writeSigned.
func rrRowErrorCode(err error) string {
	var stateErr *state.Error
	if errors.As(err, &stateErr) {
		return stateErr.Code
	}
	if name, ok := errnoName(err); ok {
		return name
	}
	return "Error"
}

var reloadClaimMarker = regexp.MustCompile(`^([0-9a-f]{24})\.done\.\d+\.[0-9a-f]{8}\.fresh$`)
var reloadTicketName = regexp.MustCompile(`^[0-9a-f]{24}\.json$`)

// reloadPendingTickets is readPending + pendingTickets (src/worker/queue.mjs):
// tickets without a done or claim marker, oldest enqueued_at first.
func reloadPendingTickets(repoRoot string) ([]string, error) {
	if repoRoot == "" {
		return nil, nil
	}
	dir := filepath.Join(repoRoot, ".code-forge", "queue")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, errors.New("cannot read the review queue; nothing changed")
	}
	present, claimed := map[string]bool{}, map[string]bool{}
	for _, entry := range entries {
		present[entry.Name()] = true
		if m := reloadClaimMarker.FindStringSubmatch(entry.Name()); m != nil {
			claimed[m[1]] = true
		}
	}
	type dated struct{ id, at string }
	list := []dated{}
	for _, entry := range entries {
		name := entry.Name()
		if !reloadTicketName.MatchString(name) {
			continue
		}
		id := name[:24]
		if present[id+".done"] || claimed[id] {
			continue
		}
		at := ""
		if data, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
			var ticket any
			if json.Unmarshal(data, &ticket) == nil {
				if m, ok := ticket.(map[string]any); ok {
					at, _ = m["enqueued_at"].(string)
				}
			}
		}
		list = append(list, dated{id, at})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].at == list[j].at {
			return jsLess(list[i].id, list[j].id)
		}
		return jsLess(list[i].at, list[j].at)
	})
	out := make([]string, len(list))
	for i, item := range list {
		out[i] = item.id
	}
	return out, nil
}

type reloadPlan struct {
	noChange bool
	legacy   bool
	oldHash  any
	newHash  string
	config   ledger.Object
	stored   ledger.Object
	changed  []string
	kept     []string
	pins     ledger.Object
}

// planReload is reloadRun's decisions (src/state/run.mjs) up to the save: a
// message for a refusal, ok false for a record shape Go does not reproduce.
func planReload(current ledger.Object, in reloadInput, pending []string) (reloadPlan, string, handoverReason, bool) {
	shape := func() (reloadPlan, string, handoverReason, bool) {
		return reloadPlan{}, "", hRunReloadRecordShape, false
	}
	refuse := func(message string) (reloadPlan, string, handoverReason, bool) { return reloadPlan{}, message, 0, true }
	if status, _ := rrObjGet(current, "status"); status != "active" {
		return refuse(fmt.Sprintf("run %s has ended", in.runID))
	}
	var stateObj, stored ledger.Object
	if value, _ := rrObjGet(current, "config"); value != nil {
		if obj, ok := value.(ledger.Object); ok {
			if _, ok := obj.Get("hash").(string); ok {
				stateObj = obj
			}
		}
	}
	if stateObj != nil {
		switch snaps := stateObj.Get("snapshots").(type) {
		case ledger.Object:
			stored = snaps
		case []any:
			return shape()
		}
	}
	p := reloadPlan{config: in.cfg, stored: stored}
	newHash, err := rrSnapshotHash(in.cfg)
	if err != nil {
		return shape()
	}
	p.newHash = newHash
	if stateObj != nil {
		p.oldHash = stateObj.Get("hash").(string)
	}
	if p.oldHash == newHash {
		p.noChange = true
		return p, "", 0, true
	}
	var before ledger.Object
	if stateObj != nil {
		value, ok := rrObjGet(stored, p.oldHash.(string))
		if _, isList := value.([]any); isList {
			return shape()
		}
		obj, isObj := value.(ledger.Object)
		if !ok || !isObj {
			return refuse(fmt.Sprintf("run %s: the current config snapshot is missing from the run record", in.runID))
		}
		before = obj
	}
	oldHuman, oldHas := rrChainGet(before, "close", "require_human")
	newHuman, newHas := rrChainGet(in.cfg, "close", "require_human")
	if !rrJSStrictEqual(oldHuman, oldHas, newHuman, newHas) {
		return refuse("close.require_human cannot change mid-run (fixed by the owner before the run starts)")
	}
	var frozen []string
	if stateObj != nil {
		frozen = immutableChangesGo(before, in.cfg)
	} else {
		frozen = []string{}
		if project, _ := rrObjGet(current, "project"); project != in.effectiveSlug {
			frozen = append(frozen, "project.slug")
		}
		if engine, ok := current.Get("engine").(string); ok {
			want, has := rrObjGet(in.cfg, "engine")
			if !has || want == nil {
				want = "auto"
			}
			if want != engine {
				frozen = append(frozen, "engine")
			}
		}
		if v, _ := rrChainGet(in.cfg, "review", "external", "required"); v == true {
			frozen = append(frozen, "review.external")
		}
		if v, _ := rrChainGet(in.cfg, "proof", "isolation"); v == "worktree" {
			frozen = append(frozen, "proof.isolation")
		}
		if v, _ := rrChainGet(in.cfg, "context", "require_coder_tests"); v == true {
			frozen = append(frozen, "context.require_coder_tests")
		}
		if _, has := rrChainGet(in.cfg, "hooks", "on_block_end"); has {
			frozen = append(frozen, "hooks.on_block_end")
		}
		if _, has := rrChainGet(in.cfg, "coder", "sandbox"); has {
			frozen = append(frozen, "coder.sandbox")
		}
	}
	if stateObj != nil && len(frozen) > 0 {
		next, supported := reviewOnlyReloadGo(before, in.cfg, frozen)
		if !supported {
			return reloadPlan{}, "", hRunReloadReviewShape, false
		}
		if next != nil {
			p.config = next
			if p.newHash, err = rrSnapshotHash(next); err != nil {
				return shape()
			}
			p.kept = frozen
			frozen = nil
		}
	}
	if len(frozen) > 0 {
		why := make([]string, len(frozen))
		for i, key := range frozen {
			why[i] = key + " cannot change mid-run (" + rrImmutableReasons[key] + ")"
		}
		pronoun := "them"
		if len(frozen) == 1 {
			pronoun = "it"
		}
		return refuse(strings.Join(why, "; ") + " — put " + pronoun + " back, or end the run and start a new one")
	}
	if stateObj != nil {
		p.changed = changedKeyPathsGo(before, p.config)
	} else {
		p.legacy = true
	}
	// B48: limitGuard(null, now)
	if grant, has := rrObjGet(current, "autopilot"); has && jsTruthy(grant, true) {
		if _, isList := grant.([]any); isList {
			return reloadPlan{}, "", hRunReloadAutopilotGrant, false
		}
		if obj, ok := grant.(ledger.Object); ok {
			status, err := autopilot.GrantState(obj, in.now)
			if err != nil {
				return reloadPlan{}, "", hRunReloadAutopilotGrant, false
			}
			if status == "active" {
				hit := ""
				if p.legacy {
					hit = "the whole config"
				} else {
					for _, key := range p.changed {
						if rrIsLimitKey(key) {
							hit = key
							break
						}
					}
				}
				if hit != "" {
					return refuse("autopilot is active: " + hit + " is a limit; only the owner can change it with code-forge autopilot approve")
				}
			}
		}
	}
	var oldPins ledger.Object
	if stateObj != nil {
		oldPins, _ = stateObj.Get("pins").(ledger.Object)
		if p.oldHash == "__proto__" {
			// snapshots[oldHash] = ... would set the prototype in Node
			return shape()
		}
	}
	p.pins = ledger.Object{}
	for _, ticket := range pending {
		if _, seen := rrObjGet(p.pins, ticket); seen {
			continue
		}
		if hash, ok := oldPins.Get(ticket).(string); ok {
			if _, has := rrObjGet(stored, hash); has {
				if hash == "__proto__" {
					return shape()
				}
				p.pins.Set(ticket, hash)
				continue
			}
		}
		if p.oldHash != nil {
			p.pins.Set(ticket, p.oldHash)
		}
	}
	return p, "", 0, true
}

// rrSnapshotHash is configHash: sha256 of canonicalJSON.
func rrSnapshotHash(value any) (string, error) {
	data, err := canon.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// rrObjGet is an own property of a mapping; anything else has none.
func rrObjGet(value any, key string) (any, bool) {
	obj, ok := value.(ledger.Object)
	if !ok {
		return nil, false
	}
	for _, field := range obj {
		if field.Key == key {
			return field.Value, true
		}
	}
	return nil, false
}

// rrChainGet is a?.b?.c: undefined past a missing key or a non-mapping.
func rrChainGet(value any, keys ...string) (any, bool) {
	present := value != nil
	for _, key := range keys {
		if value, present = rrObjGet(value, key); !present {
			return nil, false
		}
	}
	return value, present
}

// rrJSOr is a?.b ?? fallback.
func rrJSOr(value any, fallback any, keys ...string) any {
	if v, ok := rrChainGet(value, keys...); ok && v != nil {
		return v
	}
	return fallback
}

// rrJSStrictEqual is === on JSON values: a mapping or list is never equal.
func rrJSStrictEqual(a any, aok bool, b any, bok bool) bool {
	if !aok || !bok {
		return aok == bok
	}
	switch a.(type) {
	case ledger.Object, []any:
		return false
	}
	switch b.(type) {
	case ledger.Object, []any:
		return false
	}
	return a == b
}

func rrCanonText(value any, present bool) string {
	if !present {
		value = nil
	}
	data, err := canon.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(data)
}

// rrValueAtDotted is valueAt in src/state/config-snapshot.mjs.
func rrValueAtDotted(value any, dotted string) (any, bool) {
	cur, present := value, true
	for _, seg := range strings.Split(dotted, ".") {
		if _, ok := cur.(ledger.Object); !ok {
			return nil, false
		}
		if cur, present = rrObjGet(cur, seg); !present {
			return nil, false
		}
	}
	return cur, present
}

var rrImmutableKeyOrder = []string{
	"version", "project.slug", "engine", "tmp.root", "keys", "system1.key", "review.external",
	"review.agent_waivers.require_ack", "proof.isolation", "proof.worktree", "context.require_coder_tests",
	"hooks.on_block_end", "proof.runners", "coder.sandbox",
}

var rrImmutableReasons = map[string]string{
	"version":                          "the config format is fixed for the run",
	"project.slug":                     "the ledger file of the run is chosen at run start",
	"engine":                           "the run engine is fixed at run start",
	"tmp.root":                         "the worker's run temp root is created at run start",
	"keys":                             "the worker resolves keys once, at start",
	"system1.key":                      "the worker resolves the Jev key once, at start",
	"review.external":                  "the outside reviewer is fixed by the owner before the run starts",
	"review.agent_waivers.require_ack": "human acknowledgement of agent waivers is fixed by the owner before the run starts",
	"proof.isolation":                  "every block of the run is isolated the same way",
	"proof.worktree":                   "the worktrees and branches of the open blocks were made with it",
	"context.require_coder_tests":      "whether close needs the coder's green test run is fixed by the owner before the run starts",
	"hooks.on_block_end":               "it runs a command on the owner's machine at every block end; fixed by the owner before the run starts",
	"proof.runners":                    "the commands that prove a block are fixed by the owner before the run starts",
	"coder.sandbox":                    "the coder sandbox widens what the coder may write; fixed by the owner before the run starts",
}

// immutableChangesGo is immutableChanges: proof.runners may only grow at the end.
func immutableChangesGo(before, after any) []string {
	out := []string{}
	for _, key := range rrImmutableKeyOrder {
		oldValue, oldHas := rrValueAtDotted(before, key)
		newValue, newHas := rrValueAtDotted(after, key)
		if key == "proof.runners" {
			if newList, ok := newValue.([]any); ok && newHas {
				oldRunners := any([]any{})
				if oldHas && oldValue != nil {
					oldRunners = oldValue
				}
				if oldList, ok := oldRunners.([]any); ok && len(newList) >= len(oldList) {
					prefix := true
					for i, entry := range oldList {
						if rrCanonText(entry, true) != rrCanonText(newList[i], true) {
							prefix = false
							break
						}
					}
					if prefix {
						continue
					}
				}
			}
		}
		if rrCanonText(oldValue, oldHas) != rrCanonText(newValue, newHas) {
			out = append(out, key)
		}
	}
	return out
}

var rrPlainKeySegment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

func rrJoinKeyPath(at, key string) string {
	seg := key
	if !rrPlainKeySegment.MatchString(key) {
		quoted, _ := ledger.Stringify(key, false)
		seg = "[" + string(quoted) + "]"
	}
	if at == "" {
		return seg
	}
	if strings.HasPrefix(seg, "[") {
		return at + seg
	}
	return at + "." + seg
}

// changedKeyPathsGo is changedKeyPaths, sorted the way Array.prototype.sort does.
func changedKeyPathsGo(before, after any) []string {
	out := []string{}
	var walk func(a any, aok bool, b any, bok bool, at string)
	walk = func(a any, aok bool, b any, bok bool, at string) {
		am, aMap := a.(ledger.Object)
		bm, bMap := b.(ledger.Object)
		if aok && bok && aMap && bMap {
			keys := []string{}
			seen := map[string]bool{}
			for _, obj := range []ledger.Object{am, bm} {
				for _, field := range obj {
					if !seen[field.Key] {
						seen[field.Key] = true
						keys = append(keys, field.Key)
					}
				}
			}
			for _, key := range keys {
				av, ah := rrObjGet(am, key)
				bv, bh := rrObjGet(bm, key)
				walk(av, ah, bv, bh, rrJoinKeyPath(at, key))
			}
			return
		}
		if rrCanonText(a, aok) != rrCanonText(b, bok) || aok != bok {
			out = append(out, at)
		}
	}
	walk(before, true, after, true, "")
	sort.SliceStable(out, func(i, j int) bool { return jsLess(out[i], out[j]) })
	return out
}

func rrDeepCloneJSON(value any) any {
	switch v := value.(type) {
	case ledger.Object:
		out := make(ledger.Object, len(v))
		for i, field := range v {
			out[i] = ledger.Field{Key: field.Key, Value: rrDeepCloneJSON(field.Value)}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = rrDeepCloneJSON(item)
		}
		return out
	default:
		return value
	}
}

// reviewOnlyReloadGo is reviewOnlyReload: nil when the reload is more than a
// review recovery; supported false for a shape Node handles with JS quirks.
func reviewOnlyReloadGo(before, after ledger.Object, frozen []string) (ledger.Object, bool) {
	next := rrDeepCloneJSON(before).(ledger.Object)
	review := ledger.Object{}
	beforeReview, beforeHas := rrObjGet(before, "review")
	if beforeHas {
		obj, ok := beforeReview.(ledger.Object)
		if !ok {
			return nil, false
		}
		review = rrDeepCloneJSON(obj).(ledger.Object)
	}
	afterReview, afterHas := rrObjGet(after, "review")
	if afterHas && afterReview != nil {
		if _, ok := afterReview.(ledger.Object); !ok {
			return nil, false
		}
	}
	for _, key := range []string{"second_levels", "routes", "session_timeout_s"} {
		if value, ok := rrObjGet(afterReview, key); ok {
			review.Set(key, rrDeepCloneJSON(value))
		} else {
			review.Delete(key)
		}
	}
	next.Set("review", review)
	if len(review) == 0 && !beforeHas {
		next.Delete("review")
	}
	nextHash, err1 := rrSnapshotHash(next)
	beforeHash, err2 := rrSnapshotHash(before)
	if err1 != nil || err2 != nil {
		return nil, false
	}
	if nextHash == beforeHash {
		return nil, true
	}
	retained := rrDeepCloneJSON(after).(ledger.Object)
	for _, key := range frozen {
		parts := strings.Split(key, ".")
		value, has := rrValueAtDotted(before, key)
		updated, ok := rrRestoreFrozen(retained, parts[:len(parts)-1], parts[len(parts)-1], value, has)
		if !ok {
			return nil, false
		}
		retained = updated
	}
	for _, key := range changedKeyPathsGo(next, retained) {
		value, _ := rrValueAtDotted(retained, key)
		_, nextHas := rrValueAtDotted(next, key)
		if obj, ok := value.(ledger.Object); ok && !nextHas && len(obj) == 0 {
			continue
		}
		return nil, true
	}
	return next, true
}

// rrRestoreFrozen is `target = target[part] ??= {}` down the parts, then the
// leaf set to a clone of value or deleted.
func rrRestoreFrozen(target ledger.Object, parts []string, leaf string, value any, present bool) (ledger.Object, bool) {
	if len(parts) == 0 {
		if present {
			target.Set(leaf, rrDeepCloneJSON(value))
		} else {
			target.Delete(leaf)
		}
		return target, true
	}
	child := ledger.Object{}
	if existing, ok := rrObjGet(target, parts[0]); ok && existing != nil {
		obj, isObj := existing.(ledger.Object)
		if !isObj {
			return nil, false
		}
		child = obj
	}
	updated, ok := rrRestoreFrozen(child, parts[1:], leaf, value, present)
	if !ok {
		return nil, false
	}
	target.Set(parts[0], updated)
	return target, true
}

var rrLimitKeys = []string{
	"budget", "review.budgets", "review.block_budget_tokens", "review.block_budget_usd",
	"review.max_rounds_per_file", "review.single_reviewer_max_risk", "review.multimodel",
	"review.closed_book", "review.allow_open_book_codex", "thresholds", "escalation", "proof",
	"system1", "production", "autopilot",
}

// rrIsLimitKey is src/autopilot/scopes.mjs rrIsLimitKey.
func rrIsLimitKey(keyPath string) bool {
	if keyPath == "" {
		return true
	}
	for _, key := range rrLimitKeys {
		if keyPath == key || strings.HasPrefix(keyPath, key+".") || strings.HasPrefix(key, keyPath+".") {
			return true
		}
	}
	return false
}
