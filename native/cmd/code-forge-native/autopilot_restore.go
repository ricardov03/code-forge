package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/yamledit"
)

// B490: checkApprovals (src/autopilot/limits.mjs), the lazy restore every
// autopilot command runs first, in Go for each due approval whose step needs
// no .code-forge.yml write and no run reload: a finished step whose signed
// row is still missing (finish_pending), a key with a prototype segment, a
// workspace config that cannot be read, a value changed by hand (the signed
// autopilot.restore_skipped row) and an ended run whose file already holds
// the old value (the signed autopilot.restore row). B494: an ended run
// whose file needs the old value back is written in Go too (yamledit gives
// the yaml package's bytes; writeText's temp file and rename). B536: an
// active run's revert is written and reloaded in Go too (apdReloadDecide,
// apdReloadDo; a failed reload writes the file back and marks the approval
// failed, as Node does). A reverted config Go does not load, validate or
// word like Node (autopilot-restore-reload-shape), a YAML shape yamledit
// does not serve, or a second read of the file after a Go write keeps the
// whole check on Node, before Go writes anything.

// autopilotApprovalsLockTimeout is LOCK_TIMEOUT_MS (a seam for tests).
var autopilotApprovalsLockTimeout = 10 * time.Second

// autopilotCheck is ApprovalCheck: the ids restored, skipped and failed.
type autopilotCheck struct {
	runID             string
	restored, skipped []string
	failed            [][2]string // approval id, reason
	keyRead           bool        // a signed write loaded the run key
}

// text is approvalCheckText; withFailed false leaves the failed lines to
// the Node process a hand-over starts (its check reports them again).
func (c *autopilotCheck) text(withFailed bool) string {
	var b strings.Builder
	for _, id := range c.restored {
		b.WriteString("autopilot: approval " + id + " expired; the old value is back\n")
	}
	for _, id := range c.skipped {
		b.WriteString("autopilot: approval " + id + " expired but its key was changed by hand; left for the owner\n")
	}
	if withFailed {
		for _, f := range c.failed {
			if f[1] == "restore-incomplete" {
				b.WriteString("autopilot: WARN approval " + f[0] + ": its signed row could not be written yet (restore-incomplete); the next check writes it\n")
			} else {
				b.WriteString("autopilot: WARN approval " + f[0] + " could not be restored yet (" + f[1] + ")\n")
			}
		}
	}
	return b.String()
}

// autopilotRestored is the check this process ran while planning; the
// command prints it first, as Node's runAutopilot does.
var autopilotRestored *autopilotCheck

// autopilotWithApprovalsLock is withApprovalsLock: a mkdir lock next to
// the run record, Node's 10 s deadline and 25 ms polling.
func autopilotWithApprovalsLock(home, runID string, fn func() error) (err error) {
	runs := filepath.Join(home, ".code-forge", "runs")
	if err := os.MkdirAll(runs, 0700); err != nil {
		return err
	}
	lock := filepath.Join(runs, runID+".approvals.lock")
	deadline := time.Now().Add(autopilotApprovalsLockTimeout)
	for {
		err := os.Mkdir(lock, 0777)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return err
		}
		if time.Now().After(deadline) {
			return &state.Error{Code: "locked", Message: fmt.Sprintf("run %s's approvals are locked (%s); remove it if no forge verb is running", runID, lock)}
		}
		time.Sleep(25 * time.Millisecond)
	}
	failed := true
	defer func() {
		// never mask fn's own error; a lock left behind after a success is reported
		if rmErr := os.Remove(lock); rmErr != nil && !failed {
			err = rmErr
		}
	}()
	err = fn()
	failed = err != nil
	return err
}

// autopilotReadWorkspace is readWorkspace's read: the config as a value, or
// Node's refusal. ok is false when Go cannot read the file the way Node
// does: an error code Go does not name, bytes that are not UTF-8 or start
// with a BOM, or a document the Go parser refuses.
func autopilotReadWorkspace(workspace string) (js map[string]any, refusal error, ok bool) {
	js, _, refusal, ok = autopilotReadWorkspaceText(workspace)
	return js, refusal, ok
}

// autopilotReadWorkspaceText is autopilotReadWorkspace with the file's text
// (the text the YAML edit starts from).
func autopilotReadWorkspaceText(workspace string) (js map[string]any, text string, refusal error, ok bool) {
	data, err := os.ReadFile(filepath.Join(workspace, config.Filename))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", &state.Error{Code: "no-config", Message: "no .code-forge.yml in the workspace"}, true
		}
		var errno syscall.Errno
		if !errors.As(err, &errno) || blockErrno(errno) == "UNKNOWN" {
			return nil, "", nil, false
		}
		return nil, "", &state.Error{Code: "config-read", Message: ".code-forge.yml could not be read (" + blockErrno(errno) + ")"}, true
	}
	if !utf8.Valid(data) || bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
		return nil, "", nil, false
	}
	value, err := config.ParseYAML(string(data))
	if err != nil {
		return nil, "", nil, false
	}
	if !autopilotFinite(value) {
		return nil, "", nil, false // canonicalJSON drops a non-finite member; Go would compare it as null
	}
	mapping, isMapping := value.(map[string]any)
	if !isMapping {
		return nil, "", &state.Error{Code: "config-parse", Message: ".code-forge.yml could not be parsed"}, true
	}
	return mapping, string(data), nil, true
}

// autopilotFinite reports whether every number in a YAML value is finite.
func autopilotFinite(value any) bool {
	switch v := value.(type) {
	case float64:
		return !math.IsNaN(v) && !math.IsInf(v, 0)
	case map[string]any:
		for _, child := range v {
			if !autopilotFinite(child) {
				return false
			}
		}
	case []any:
		for _, child := range v {
			if !autopilotFinite(child) {
				return false
			}
		}
	}
	return true
}

// autopilotValueAt is valueAt: the value at the key path, and whether every
// segment is an own key of a mapping.
func autopilotValueAt(js map[string]any, segs []string) (any, bool) {
	var cur any = js
	for _, seg := range segs {
		m, isMapping := cur.(map[string]any)
		if !isMapping {
			return nil, false
		}
		next, has := m[seg]
		if !has {
			return nil, false
		}
		cur = next
	}
	return cur, true
}

// autopilotSameJSON compares canonicalJSON forms.
func autopilotSameJSON(a, b any) bool {
	x, errA := canon.Marshal(a)
	y, errB := canon.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(x, y)
}

func autopilotHas(o ledger.Object, key string) bool {
	for _, field := range o {
		if field.Key == key {
			return true
		}
	}
	return false
}

// autopilotRedactValue is redact (src/util/redact.mjs) on a JSON value:
// every string and every key masked, two keys that mask alike kept apart.
func autopilotRedactValue(value any) any {
	switch v := value.(type) {
	case string:
		return autopilotRedact(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = autopilotRedactValue(item)
		}
		return out
	case ledger.Object:
		out := ledger.Object{}
		for _, field := range v {
			key := autopilotRedact(field.Key)
			candidate := key
			for n := 2; autopilotHas(out, candidate); n++ {
				candidate = fmt.Sprintf("%s#%d", key, n)
			}
			out = append(out, ledger.Field{Key: candidate, Value: autopilotRedactValue(field.Value)})
		}
		return out
	}
	return value
}

const (
	apStepPending = iota // finish_pending: only the signed row is missing
	apStepUnsafe         // a key that is not text or has a prototype segment
	apStepRead           // the workspace config cannot be read
	apStepSkip           // changed by hand
	apStepRestore        // an ended run whose file holds the old value
	apStepRewrite        // an ended run whose file gets the old value back (B494)
	apStepReload         // an active run: the old value back (when needed) and the reload (B536)
)

type autopilotStep struct {
	kind   int
	id     string
	entry  ledger.Object
	reason string // apStepRewrite: editDoc's refusal (nothing is written)
	file   string // apStepRewrite: the workspace .code-forge.yml
	text   string // apStepRewrite: the bytes String(doc) gives after the edit
	orig   string // apStepReload: the file's text before the edit (put back on a failed reload)
	write  bool   // apStepReload: the file needs the old value written back
	reload reloadInput
}

// autopilotRevert is revertChange applied with editDoc: the text with the
// approval's old value back (a set, or a remove with its prune). reason is
// editDoc's usage error when a parent is not a mapping; handOver is true
// for a shape Go does not write byte-equal to the yaml package.
func autopilotRevert(text, key string, segs []string, a ledger.Object) (out, reason string, handOver bool) {
	var err error
	if a.Get("old_absent") == true {
		prune := 0
		switch p := a.Get("prune").(type) {
		case nil: // i < undefined: no prune
		case float64:
			if math.IsNaN(p) || math.IsInf(p, 0) || p > 64 {
				return "", "", true
			}
			if p > 0 {
				prune = int(math.Ceil(p)) // i < 1.5 runs twice
			}
		default:
			return "", "", true // i < "2" coerces: left to Node
		}
		out, err = yamledit.Remove(text, segs, prune)
	} else {
		out, err = yamledit.Set(text, segs, a.Get("old"))
	}
	switch {
	case err == nil:
		return out, "", false
	case errors.Is(err, yamledit.ErrNotMapping):
		return "", key + " cannot be set: a parent of it is not a mapping in .code-forge.yml", false
	}
	return "", "", true
}

// autopilotRestorePlan reads checkApprovals' steps for the due approvals of
// record, or the reason the check stays with Node. It writes nothing.
func autopilotRestorePlan(home, runID string, record ledger.Object, now time.Time) ([]autopilotStep, handoverReason) {
	switch err := autopilot.Approvals(record, now); {
	case err == nil:
		return nil, 0
	case !errors.Is(err, autopilot.ErrApprovalDue):
		return nil, hAutopilotShape
	}
	workspace, ok := record.Get("workspace").(string)
	if !ok {
		return nil, hAutopilotRecord
	}
	values, _ := record.Get("autopilot_approvals").([]any)
	steps := []autopilotStep{}
	rewritten := false // Node reads the file again for each approval: after a Go write, the next read stays with Node
	for _, value := range values {
		a, isObject := value.(ledger.Object)
		if !isObject {
			continue
		}
		due, known := autopilot.ApprovalDue(a, now)
		if !known {
			return nil, hAutopilotShape
		}
		if !due {
			continue
		}
		id, ok := a.Get("approval_id").(string)
		if !ok {
			return nil, hAutopilotShape
		}
		step := autopilotStep{id: id, entry: a}
		if pending := a.Get("finish_pending"); pending != nil {
			p, isObject := pending.(ledger.Object)
			row, rowObject := p.Get("row").(ledger.Object)
			_, status := p.Get("status").(string)
			_, ts := row.Get("ts").(string)
			if !isObject || !rowObject || !status || !ts {
				return nil, hAutopilotShape // a finish_pending Node would read differently
			}
			step.kind = apStepPending
			steps = append(steps, step)
			continue
		}
		key, ok := a.Get("key").(string)
		segs := strings.Split(key, ".")
		unsafe := !ok // approval.key.split throws on a key that is not text: Node reports it the same way
		for _, seg := range segs {
			unsafe = unsafe || seg == "__proto__" || seg == "constructor" || seg == "prototype"
		}
		if unsafe {
			step.kind, step.reason = apStepUnsafe, "unsafe key"
			steps = append(steps, step)
			continue
		}
		if rewritten {
			return nil, hAutopilotRestoreWrite
		}
		js, text, refusal, ok := autopilotReadWorkspaceText(workspace)
		if !ok {
			return nil, hAutopilotRestoreConfig
		}
		if refusal != nil {
			step.kind, step.reason = apStepRead, refusal.Error()
			steps = append(steps, step)
			continue
		}
		oldAbsent, isBool := a.Get("old_absent").(bool)
		if !isBool || !autopilotHas(a, "new") || !oldAbsent && !autopilotHas(a, "old") {
			return nil, hAutopilotShape
		}
		current, present := autopilotValueAt(js, segs)
		holdsOld := !present
		if !oldAbsent {
			holdsOld = present && autopilotSameJSON(current, a.Get("old"))
		}
		if a.Get("restoring") != true && (!present || !autopilotSameJSON(current, a.Get("new"))) {
			step.kind = apStepSkip
			steps = append(steps, step)
			continue
		}
		if record.Get("status") == "active" {
			// B536: the old value back, then reloadWorkspace with the key allowed
			step.kind, step.file, step.orig, step.text, step.write = apStepReload, filepath.Join(workspace, config.Filename), text, text, !holdsOld
			if !holdsOld {
				out, reason, handOver := autopilotRevert(text, key, segs, a)
				if handOver {
					return nil, hAutopilotRestoreWrite // a YAML shape Go does not write like the yaml package
				}
				step.text, step.reason = out, reason
			}
			if step.reason == "" {
				in, ok := apdReloadDecide(home, runID, record, workspace, step.text, segs, now)
				if !ok {
					return nil, hAutopilotRestoreReloadShape
				}
				step.reload = in
			}
			rewritten = true // a later approval would read the file and the record again
			steps = append(steps, step)
			continue
		}
		if !holdsOld {
			out, reason, handOver := autopilotRevert(text, key, segs, a)
			if handOver {
				return nil, hAutopilotRestoreWrite // a YAML shape Go does not write like the yaml package
			}
			step.kind, step.reason, step.file, step.text = apStepRewrite, reason, filepath.Join(workspace, config.Filename), out
			rewritten = reason == ""
			steps = append(steps, step)
			continue
		}
		step.kind = apStepRestore
		steps = append(steps, step)
	}
	return steps, 0
}

// autopilotRestore runs checkApprovals for a record with a due approval:
// the steps are planned on the record first and again under the approvals
// lock, so a hand-over happens before anything is written. err is what
// Node's check throws (the command then reports it).
func autopilotRestore(home, runID string, record ledger.Object, now time.Time) (*autopilotCheck, error, handoverReason) {
	if _, reason := autopilotRestorePlan(home, runID, record, now); reason != 0 {
		return nil, nil, reason
	}
	check := &autopilotCheck{runID: runID}
	var late handoverReason
	err := autopilotWithApprovalsLock(home, runID, func() error {
		fresh, err := state.ReadRun(home, runID)
		if err != nil {
			return err
		}
		steps, reason := autopilotRestorePlan(home, runID, fresh, now)
		if reason != 0 {
			late = reason
			return nil
		}
		for _, step := range steps {
			var status string
			row := ledger.Object{}
			switch step.kind {
			case apStepUnsafe, apStepRead:
				check.failed = append(check.failed, [2]string{step.id, step.reason})
				continue
			case apStepPending:
				status, _ = step.entry.Get("finish_pending").(ledger.Object).Get("status").(string)
			case apStepSkip:
				status = "restore_skipped"
				row = ledger.Object{{Key: "event", Value: "autopilot.restore_skipped"}, {Key: "reason", Value: "changed-by-hand"}}
			case apStepRestore, apStepRewrite, apStepReload:
				if step.entry.Get("restoring") != true {
					// crash safety: the restore is recorded as started first
					if err := state.WithRunLock(home, runID, func() error {
						latest, err := state.ReadRun(home, runID)
						if err != nil {
							return err
						}
						if values, i := autopilotStored(latest, step.id); i >= 0 {
							stored := values[i].(ledger.Object)
							stored.Set("restoring", true)
							values[i] = stored
						}
						return state.SaveRun(home, latest)
					}); err != nil {
						return err
					}
				}
				if step.kind == apStepRewrite {
					reason := step.reason
					if reason == "" {
						if err := autopilotWriteText(step.file, step.text); err != nil {
							reason = autopilotFsMessage(err)
						}
					}
					if reason != "" {
						check.failed = append(check.failed, [2]string{step.id, reason})
						continue
					}
				}
				var newHash any
				if step.kind == apStepReload {
					if step.reason == "" && step.write {
						if err := autopilotWriteText(step.file, step.text); err != nil {
							step.reason = autopilotFsMessage(err)
						}
					}
					if step.reason != "" {
						check.failed = append(check.failed, [2]string{step.id, step.reason})
						continue
					}
					hash, err, race := apdReloadDo(step.reload)
					if race != 0 || err != nil {
						if err := autopilotWriteText(step.file, step.orig); err != nil {
							return err
						}
						if race != 0 {
							late = hAutopilotRestoreReloadShape // the file is back; the restoring mark makes Node finish it
							return nil
						}
						check.failed = append(check.failed, [2]string{step.id, apdErrText(err)})
						continue
					}
					newHash = hash
				}
				status = "restored"
				var restored any
				if !step.entry.Get("old_absent").(bool) {
					restored = autopilotRedactValue(step.entry.Get("old"))
				}
				row = ledger.Object{{Key: "event", Value: "autopilot.restore"}, {Key: "restored", Value: restored}, {Key: "old_absent", Value: step.entry.Get("old_absent")},
					{Key: "reloaded", Value: step.kind == apStepReload}, {Key: "new_hash", Value: newHash}}
			}
			wrote, err := autopilotFinish(home, runID, step, status, row, now, check)
			if err != nil {
				return err
			}
			switch {
			case !wrote:
				check.failed = append(check.failed, [2]string{step.id, "restore-incomplete"})
			case status == "restored":
				check.restored = append(check.restored, step.id)
			default:
				check.skipped = append(check.skipped, step.id)
			}
		}
		return nil
	})
	if late != 0 {
		return nil, nil, late // nothing was written
	}
	return check, err, 0
}

// autopilotWriteText is writeText: an atomic write through a temp file next
// to file (<file>.<pid>.<6 hex>.tmp) that keeps the file's mode (0644 when
// it cannot be read); the temp file is removed when a step fails.
func autopilotWriteText(file, text string) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(file); err == nil {
		mode = info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	}
	var suffix [3]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.%s.tmp", file, os.Getpid(), hex.EncodeToString(suffix[:]))
	err := func() error {
		fh, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode&os.ModePerm)
		if err != nil {
			return err
		}
		if _, err := fh.WriteString(text); err != nil {
			fh.Close()
			return err
		}
		if err := fh.Close(); err != nil {
			return err
		}
		if err := os.Chmod(tmp, mode); err != nil { // the umask may have narrowed it
			return err
		}
		return os.Rename(tmp, file)
	}()
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// autopilotFsMessage is the message of Node's fs error for err: "<CODE>:
// <libuv text>, <syscall> '<path>'" (rename names both paths).
func autopilotFsMessage(err error) string {
	texts := map[syscall.Errno]string{syscall.EACCES: "permission denied", syscall.EPERM: "operation not permitted", syscall.ENOENT: "no such file or directory",
		syscall.ENOTDIR: "not a directory", syscall.EISDIR: "illegal operation on a directory", syscall.ENOSPC: "no space left on device",
		syscall.EROFS: "read-only file system", syscall.ENAMETOOLONG: "name too long", syscall.ELOOP: "too many symbolic links encountered", syscall.EEXIST: "file already exists"}
	var errno syscall.Errno
	if !errors.As(err, &errno) || texts[errno] == "" {
		return err.Error()
	}
	head := blockErrno(errno) + ": " + texts[errno]
	var link *os.LinkError
	if errors.As(err, &link) {
		return head + ", " + link.Op + " '" + link.Old + "' -> '" + link.New + "'"
	}
	var path *fs.PathError
	if errors.As(err, &path) {
		return head + ", " + path.Op + " '" + path.Path + "'"
	}
	return head
}

// autopilotStored finds an approval by id in a record (approvalsOf + find).
func autopilotStored(record ledger.Object, id string) ([]any, int) {
	values, _ := record.Get("autopilot_approvals").([]any)
	for i, value := range values {
		if a, ok := value.(ledger.Object); ok && a.Get("approval_id") == id {
			return values, i
		}
	}
	return values, -1
}

// autopilotFinish is checkApprovals' finish: under the run lock, write the
// signed row (the stored finish_pending one, or a new one), then end the
// approval; a row that cannot be written leaves finish_pending for the next
// check. It reports whether the row was written now.
func autopilotFinish(home, runID string, step autopilotStep, status string, row ledger.Object, now time.Time, check *autopilotCheck) (bool, error) {
	wrote := false
	err := state.WithRunLock(home, runID, func() error {
		latest, err := state.ReadRun(home, runID)
		if err != nil {
			return err
		}
		values, i := autopilotStored(latest, step.id)
		if i < 0 {
			return nil
		}
		stored := values[i].(ledger.Object)
		if stored.Get("status") != "active" {
			return nil
		}
		pending, _ := stored.Get("finish_pending").(ledger.Object)
		if stored.Get("finish_pending") == nil {
			full := ledger.Object{{Key: "approval_id", Value: step.id}, {Key: "key", Value: step.entry.Get("key")}, {Key: "until", Value: step.entry.Get("until")}}
			for _, field := range row {
				full.Set(field.Key, field.Value)
			}
			full.Set("ts", autopilotStamp(now))
			pending = ledger.Object{{Key: "status", Value: status}, {Key: "row", Value: full}}
		}
		pendingRow, _ := pending.Get("row").(ledger.Object)
		check.keyRead = true
		if _, err := autopilotWriteSigned(home, runID, pendingRow); err != nil {
			stored.Set("finish_pending", pending)
			values[i] = stored
			return state.SaveRun(home, latest)
		}
		stored.Delete("finish_pending")
		stored.Set("status", pending.Get("status"))
		stored.Set("ended_at", pendingRow.Get("ts"))
		values[i] = stored
		wrote = true
		return state.SaveRun(home, latest)
	})
	return wrote, err
}

// autopilotLate is a hand-over a Go step found only after the owner's
// prompt (the command then runs on Node, which asks again).
var autopilotLate handoverReason

// autopilotRestoredFirst registers the run key again when the check's signed
// write loaded it (the command's secrets start fresh), and returns the check's
// lines. Before a hand-over it prints the restored and skipped lines itself:
// the Node process finds those approvals ended and reports only the failed
// ones, which it tries again.
func autopilotRestoredFirst(home string, handingOver bool) string {
	check := autopilotRestored
	if check == nil {
		return ""
	}
	if check.keyRead {
		autopilotRegisterKey(home, check.runID)
	}
	if handingOver {
		if text := check.text(false); text != "" {
			o := &runOut{redact: autopilotRedact}
			o.err(text)
		}
		return ""
	}
	return check.text(true)
}
