package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/yamledit"
	"gopkg.in/yaml.v3"
)

// B536: approveChange and the restore of a due approval on an active run
// (src/autopilot/limits.mjs), in Go: the .code-forge.yml edit (yamledit
// gives the yaml package's bytes), reloadWorkspace with limitGuard's allow
// (reloadApplyLocked, shared with `run reload`), the signed
// autopilot.approve row and the stored approval. Every case Go does not
// reproduce is decided before anything is written and keeps a narrow
// hand-over: a YAML shape yamledit does not serve, and an edited config Go
// does not load, validate or word the way Node does.

// apdWithoutKey is withoutKey: a copy of obj without the key at segs and
// without the mappings on its path that removing it leaves empty.
func apdWithoutKey(obj ledger.Object, segs []string) ledger.Object {
	copied, _ := rrDeepCloneJSON(obj).(ledger.Object)
	chain := []ledger.Object{copied}
	cur := copied
	for _, seg := range segs[:len(segs)-1] {
		next, ok := cur.Get(seg).(ledger.Object)
		if !ok {
			return copied // remove: a parent that is not a mapping leaves the copy as it is
		}
		cur = next
		chain = append(chain, cur)
	}
	cur.Delete(segs[len(segs)-1])
	chain[len(chain)-1] = cur
	// ledger.Object is a slice: write each shortened mapping back into its parent
	for depth := len(chain) - 1; depth >= 1; depth-- {
		chain[depth-1].Set(segs[depth-1], chain[depth])
	}
	for i := 0; i < len(segs)-1; i++ {
		depth := len(segs) - 1 - i
		if depth < 1 || len(chain[depth]) > 0 {
			break
		}
		chain[depth-1].Delete(segs[depth-1])
		for d := depth - 1; d >= 1; d-- {
			chain[d-1].Set(segs[d-1], chain[d])
		}
	}
	return chain[0]
}

// apdReloadDecide is reloadWorkspace's checks before the run lock on the
// text the file will hold: the reload input, or ok false for a case Go does
// not load, validate or guard the way Node does (nothing is written then).
// The refusals reloadRun words (a fixed key, the autopilot limit guard, the
// review queue) are left to reloadApplyLocked, which plans again under the
// run lock.
func apdReloadDecide(home, runID string, record ledger.Object, workspace, text string, allow []string, now time.Time) (reloadInput, bool) {
	in := reloadInput{home: home, runID: runID, now: now, allow: allow}
	if rrWorkspaceHoldsRuns(home, workspace) || apdHasTag(text) {
		return in, false
	}
	file := filepath.Join(workspace, config.Filename)
	// readWorkspace edits <workspace>/.code-forge.yml; loadProjectConfig reads the project root's
	here, errHere := os.Stat(file)
	root, errRoot := os.Stat(filepath.Join(config.ProjectRootFor(workspace), config.Filename))
	if errHere != nil || errRoot != nil || !os.SameFile(here, root) {
		return in, false
	}
	cfgMap, order, err := config.ParseTextOrdered(text, file)
	if err != nil {
		return in, false
	}
	if len(config.SchemaIssues(cfgMap, "")) > 0 || configHoldsSecretShape(cfgMap) {
		return in, false
	}
	if proofSandboxIssues(cfgMap) > 0 {
		return in, false // B781: add_dirs entries are checked in Go (validate rule 19)
	}
	v := configValidator{cfg: cfgMap, order: order, seen: config.LoadSeenInCache(home), hasCli: hasCliOnPath}
	if errs, _ := v.run(); len(errs) > 0 {
		return in, false // approve words these first (apdConfigRefusal)
	}
	in.cfg, _ = order.Object(cfgMap).(ledger.Object)
	in.effectiveSlug = planSlugFor(cfgMap, workspace)
	if top, err := gitx.RepoTop(workspace); err == nil {
		in.repoRoot = top
	}
	if _, _, _, ok := planReload(record, in, nil); !ok {
		return in, false
	}
	if pending, err := reloadPendingTickets(in.repoRoot); err == nil {
		if _, _, _, ok := planReload(record, in, pending); !ok {
			return in, false
		}
	}
	return in, true
}

// apdReloadDo runs the reload: the new hash, or the message reloadRun
// throws, or the hand-over a record changed under the lock asks for.
func apdReloadDo(in reloadInput) (string, error, handoverReason) {
	out, message, race, err := reloadApplyLocked(in)
	switch {
	case race != 0:
		return "", nil, race
	case err != nil:
		return "", err, 0
	case message != "":
		return "", &state.Error{Code: "reload", Message: message}, 0
	}
	return out.newHash, nil, 0
}

// apdErrText is `thrown?.message ?? String(thrown)` for a Go error.
func apdErrText(err error) string {
	var stateErr *state.Error
	if errors.As(err, &stateErr) {
		return stateErr.Message
	}
	return autopilotFsMessage(err)
}

// apdApproveStep is approveChange's work on one read of the record and the
// workspace file, decided before any write.
type apdApproveStep struct {
	file, text, edited string
	segs               []string
	old                any
	oldAbsent          bool
	prune              int
	in                 reloadInput
}

// apdApproveDecide is approveChange's checks: Node's refusal (no config,
// no change, a parent that is not a mapping), the step, or the reason Node
// must run it.
func apdApproveDecide(home, runID string, record ledger.Object, key string, value any, now time.Time, write handoverReason) (*apdApproveStep, error, handoverReason) {
	workspace, _ := record.Get("workspace").(string)
	js, text, refusal, ok := autopilotReadWorkspaceText(workspace)
	if !ok {
		return nil, nil, hAutopilotApproveConfig
	}
	if refusal != nil {
		return nil, refusal, 0
	}
	segs := strings.Split(key, ".")
	current, present := autopilotValueAt(js, segs)
	if present && autopilotSameJSON(current, value) {
		return nil, &state.Error{Code: "no-change", Message: key + " already has that value in .code-forge.yml; nothing to approve"}, 0
	}
	step := &apdApproveStep{file: filepath.Join(workspace, config.Filename), text: text, segs: segs, oldAbsent: !present}
	if present {
		// the old value in the yaml package's key order
		cfgMap, order, err := config.ParseTextOrdered(text, step.file)
		if err != nil {
			return nil, nil, write
		}
		ordered, has := apdOrderedAt(order.Object(cfgMap), segs)
		if !has || !autopilotSameJSON(ordered, current) {
			return nil, nil, write
		}
		step.old = ordered
	} else {
		step.prune = apdMissing(js, segs)
	}
	edited, err := yamledit.SetOrSplice(text, segs, value)
	switch {
	case errors.Is(err, yamledit.ErrNotMapping):
		return nil, autopilotUsageFail(key + " cannot be set: a parent of it is not a mapping in .code-forge.yml"), 0
	case yamledit.IsUnsupported(err):
		// B781: neither the model nor the splice edit can write this text
		// safely; no runtime is left to hand it to.
		return nil, apdShapeRefusal(key, err), 0
	case err != nil:
		return nil, nil, write
	}
	step.edited = edited
	if refusal := apdConfigRefusal(home, workspace, edited); refusal != nil {
		return nil, refusal, 0
	}
	in, ok := apdReloadDecide(home, runID, record, workspace, edited, segs, now)
	if !ok {
		return nil, nil, hAutopilotApproveReloadShape
	}
	step.in = in
	return step, nil, 0
}

// apdShapeRefusal is approve's answer for a .code-forge.yml it cannot edit
// without changing other lines: what to change by hand instead.
func apdShapeRefusal(key string, err error) error {
	reason := err.Error()
	var u *yamledit.Unsupported
	if errors.As(err, &u) {
		reason = u.Reason
	}
	return &state.Error{Code: "yaml-shape", Message: key + " cannot be changed safely in .code-forge.yml (it holds " + reason + "); set " + key + " by hand and run code-forge run reload, or rewrite that part of the file in plain block style and approve again; nothing was changed"}
}

// apdConfigRefusal is approve's answer when .code-forge.yml would not be a
// valid config after the change (B781; 0.25.0 handed these over with
// autopilot-approve-reload-shape): a YAML tag, a schema issue, a raw
// secret, a refused sandbox dir or a validate error. Nothing is written.
func apdConfigRefusal(home, workspace, text string) error {
	refuse := func(why string) error {
		return &state.Error{Code: "config-invalid", Message: "with that change .code-forge.yml " + why + "; nothing was changed; run code-forge validate"}
	}
	if apdHasTag(text) {
		return refuse("holds a YAML tag (!!...), which code-forge does not read")
	}
	cfgMap, order, err := config.ParseTextOrdered(text, filepath.Join(workspace, config.Filename))
	if err != nil {
		return refuse("does not load: " + config.MaskSecrets(err.Error()))
	}
	if issues := config.SchemaIssues(cfgMap, ""); len(issues) > 0 {
		lines := make([]string, 0, len(issues))
		for _, issue := range issues {
			lines = append(lines, issue.Text)
		}
		return refuse("fails the config schema (" + config.MaskSecrets(strings.Join(lines, "; ")) + ")")
	}
	if configHoldsSecretShape(cfgMap) {
		return refuse("holds what looks like a raw secret; store the key with code-forge keys set <name> (the config may hold only an env: or op:// reference)")
	}
	if n := proofSandboxIssues(cfgMap); n > 0 {
		return refuse("lists a coder.sandbox.add_dirs entry code-forge refuses")
	}
	v := configValidator{cfg: cfgMap, order: order, seen: config.LoadSeenInCache(home), hasCli: hasCliOnPath}
	if errs, _ := v.run(); len(errs) > 0 {
		var lines strings.Builder
		for _, issue := range errs {
			lines.WriteString("\n  [" + issue.rule + "] " + issue.message)
		}
		noun := "errors"
		if len(errs) == 1 {
			noun = "error"
		}
		return &state.Error{Code: "config-invalid", Message: "with that change .code-forge.yml is invalid (" + strconv.Itoa(len(errs)) + " " + noun + "); nothing was changed; run code-forge validate" + config.MaskSecrets(lines.String())}
	}
	return nil
}

// apdHasTag reports an explicit YAML tag (!!str, !!binary, ...) on any node;
// a "!!" inside a comment or a quoted string is not one.
func apdHasTag(text string) bool {
	if !strings.Contains(text, "!") {
		return false
	}
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return true // the loader words the error; nothing is guessed here
	}
	var walk func(n *yaml.Node) bool
	walk = func(n *yaml.Node) bool {
		if n.Style&yaml.TaggedStyle != 0 {
			return true
		}
		for _, c := range n.Content {
			if walk(c) {
				return true
			}
		}
		return false
	}
	return walk(&doc)
}

// apdOrderedAt is the value at segs of a config read as ledger.Object.
func apdOrderedAt(value any, segs []string) (any, bool) {
	for _, seg := range segs {
		obj, ok := value.(ledger.Object)
		if !ok || !autopilotHas(obj, seg) {
			return nil, false
		}
		value = obj.Get(seg)
	}
	return value, true
}

// apdMissing is valueAt's missing: how many trailing parent mappings of
// segs do not exist yet.
func apdMissing(js map[string]any, segs []string) int {
	var cur any = js
	for i, seg := range segs {
		m, isMapping := cur.(map[string]any)
		if !isMapping {
			return 0
		}
		next, has := m[seg]
		if !has {
			return len(segs) - 1 - i
		}
		cur = next
	}
	return 0
}

// apdNewApprovalID makes an approval id (a seam for tests).
var apdNewApprovalID = apdApproveID

// apdApproveID is `apr-${randomBytes(4).toString('hex')}`.
func apdApproveID() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "apr-" + hex.EncodeToString(b[:]), nil
}

// apdApproveApply is approveChange after its checks, under the approvals
// lock: write the file, reload, then under the run lock the signed
// autopilot.approve row and the stored approval. Any failure puts the file
// back (and, after a reload, reloads the old value). late is a hand-over
// for a record that changed under the run lock (the file is back then).
func apdApproveApply(home, runID, key, until string, value any, step *apdApproveStep, now time.Time) (ledger.Object, error, handoverReason) {
	id, err := apdNewApprovalID()
	if err != nil {
		return nil, err, 0
	}
	var old any
	if !step.oldAbsent {
		old = step.old
	}
	approval := ledger.Object{
		{Key: "approval_id", Value: id}, {Key: "key", Value: key}, {Key: "new", Value: value}, {Key: "old", Value: old},
		{Key: "old_absent", Value: step.oldAbsent}, {Key: "prune", Value: float64(step.prune)}, {Key: "until", Value: until},
		{Key: "approved_at", Value: autopilotStamp(now)}, {Key: "status", Value: "active"}, {Key: "ended_at", Value: nil},
	}
	if err := autopilotWriteText(step.file, step.edited); err != nil {
		return nil, err, 0
	}
	newHash, err, race := apdReloadDo(step.in)
	if race != 0 || err != nil {
		if backErr := autopilotWriteText(step.file, step.text); backErr != nil {
			return nil, backErr, 0
		}
		return nil, err, race
	}
	err = state.WithRunLock(home, runID, func() error {
		fresh, err := state.ReadRun(home, runID)
		if err != nil {
			return err
		}
		var shownOld any
		if !step.oldAbsent {
			shownOld = autopilotRedactValue(step.old)
		}
		if _, err := autopilotWriteSigned(home, runID, ledger.Object{
			{Key: "event", Value: "autopilot.approve"}, {Key: "approval_id", Value: id}, {Key: "key", Value: key},
			{Key: "old", Value: shownOld}, {Key: "old_absent", Value: step.oldAbsent}, {Key: "new", Value: autopilotRedactValue(value)},
			{Key: "until", Value: until}, {Key: "new_hash", Value: newHash}, {Key: "ts", Value: autopilotStamp(now)},
		}); err != nil {
			return err
		}
		values, _ := fresh.Get("autopilot_approvals").([]any)
		fresh.Set("autopilot_approvals", append(append([]any{}, values...), approval))
		return state.SaveRun(home, fresh)
	})
	if err != nil {
		// the row or the record failed: undo the change so nothing stands unrecorded
		if backErr := autopilotWriteText(step.file, step.text); backErr != nil {
			return nil, backErr, 0
		}
		if record, readErr := state.ReadRun(home, runID); readErr == nil {
			workspace, _ := record.Get("workspace").(string)
			if in, ok := apdReloadDecide(home, runID, record, workspace, step.text, step.segs, now); ok {
				_, _, _ = apdReloadDo(in) // .catch(() => null)
			}
		}
		return nil, err, 0
	}
	return approval, nil, 0
}
