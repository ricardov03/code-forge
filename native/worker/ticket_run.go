package worker

// B488: the per-ticket path of src/worker/loop.mjs (runOne → processTicket →
// reviewOne → complete) for the outcomes Go decides byte-equal to Node: a
// ticket that cannot be read, a ticket of another run, a content root or
// worktree that cannot be used, a path refusal, stale content and a config
// snapshot that does not match its hash. A ticket that would reach the review
// engine (routing, the fix loop, an answers ticket) returns ErrUnsupported
// BEFORE anything is written, so the loop leaves it to Node: no lost ticket,
// no double review.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/gates"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/proof"
	"github.com/ricardov03/code-forge/native/session"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/ticket"
)

// ErrUnsupported: Go does not serve this ticket yet; nothing was written and
// no lock is held, so Node's worker serves it.
var ErrUnsupported = errors.New("worker: ticket case not ported to Go")

func unsupported(why string) error { return fmt.Errorf("%w: %s", ErrUnsupported, why) }

// IsUnsupported reports an ErrUnsupported from RunTicket.
func IsUnsupported(err error) bool { return errors.Is(err, ErrUnsupported) }

// TicketRunner holds what createWorker fixes at boot for every ticket.
type TicketRunner struct {
	Home     string // the run record and key live under Home/.code-forge/runs
	RepoRoot string // realpath'd repository root
	RunID    string
	Slug     string // the ledger project slug (the run record's project)
	Key      []byte // the run's HMAC key
	Worker   ledger.Object
	// Now is the clock (default time.Now); WriteRow writes a signed ledger
	// row (default ledger.AppendObject under Home); Stopping is the loop's
	// stop flag (an unavailable outcome while stopping is abandoned).
	Now      func() time.Time
	WriteRow func(ledger.Object) error
	Stopping func() bool
	// Cfg is the boot config (a ticket of a run with no record or no
	// snapshot is reviewed with it); ReadRows is the project ledger view
	// (default: the rotations and current file under Home).
	Cfg      map[string]any
	ReadRows func() ([]any, error)
	// B497, the review session: RunRootDir holds the packets
	// (<RunRootDir>/packets/<ticket>) and the sessions' process root;
	// Secrets are the registered redact values; SessionDeps are the
	// session seams (Home defaults to Home, Environ to SessionEnv of the
	// process env); Warn is the error log's warning line (nil: none, and a
	// ticket under budget.usd is left to Node); JevKey is the resolved
	// System 1 key ("" none; a key is left to Node: Jev triage is not ported).
	RunRootDir  string
	Secrets     []string
	SessionDeps session.Deps
	Warn        func(warning, message string)
	JevKey      string
	// Pool (B489) holds every review session to the worker's caps
	// (loop.mjs spawnPooled); nil runs sessions without them.
	Pool *Pool
	// B507 seams: ReadRecordFile reads the run record (nil: os.ReadFile);
	// Sleep is configFor's wait before its one retry (nil: time.Sleep).
	ReadRecordFile func(string) ([]byte, error)
	Sleep          func(time.Duration)
}

type attempt struct {
	ticket    ledger.Object
	queuedAt  any
	startedAt time.Time
}

// RunTicket is loop.mjs runOne for one ticket: (1, false, nil) completed,
// (0, false, nil) abandoned while stopping, (0, true, nil) poisoned (not even
// a failure could be recorded), or (0, false, ErrUnsupported) for Node.
func (w *TicketRunner) RunTicket(id string) (completed int, poison bool, err error) {
	a := &attempt{startedAt: w.now()}
	outcome, ticket, err := w.decideRead(id)
	if err != nil {
		return 0, false, err
	}
	if ticket == nil {
		return w.finish(id, outcome, a)
	}
	a.ticket = ticket
	if q := ticket.Get("queued_at"); q != nil {
		a.queuedAt = q
	} else {
		a.queuedAt = ticket.Get("enqueued_at")
	}
	block, _ := ticket.Get("block").(string)
	file, _ := ticket.Get("file").(string)
	var runErr error
	lockErr := filelock.WithLock(w.RunID+"\x00"+FileLockKey(block, file), func() error {
		a.startedAt = w.now()
		outcome, runErr = w.reviewOne(id, ticket)
		if runErr != nil {
			return nil
		}
		// Node writes the start timings before the checks; the final write
		// below replaces them, so the files end the same.
		if writeErr := w.writeTimings(ticket, a, nil); writeErr != nil {
			runErr = writeErr
		}
		return nil
	}, filelock.Options{})
	if lockErr != nil {
		return 0, false, unsupported("file lock: " + lockErr.Error())
	}
	if runErr != nil {
		if IsUnsupported(runErr) {
			return 0, false, runErr
		}
		return w.workerError(id, a, runErr)
	}
	return w.finish(id, outcome, a)
}

func (w *TicketRunner) finish(id string, outcome ledger.Object, a *attempt) (int, bool, error) {
	if w.Stopping != nil && w.Stopping() && outcome.Get("status") == "unavailable" {
		return 0, false, nil
	}
	if err := w.complete(id, outcome, a); err != nil {
		return w.workerError(id, a, err)
	}
	return 1, false, nil
}

// workerError is runOne's catch: a signed `unavailable` / `worker-error`
// result, else the ticket is poisoned. A *StepError (B90) keeps its step and
// text, redacted, as the result's `detail`.
func (w *TicketRunner) workerError(id string, a *attempt, cause error) (int, bool, error) {
	outcome := ledger.Object{{Key: "event", Value: "review.result"}, {Key: "status", Value: "unavailable"}, {Key: "reason", Value: "worker-error"}}
	var step *StepError
	if errors.As(cause, &step) {
		outcome = append(outcome, ledger.Field{Key: "detail", Value: gates.Mask(step.Error(), w.secrets())})
	}
	outcome = append(outcome, ledger.Field{Key: "approved", Value: false})
	if err := w.complete(id, outcome, a); err != nil {
		return 0, true, nil
	}
	return 1, false, nil
}

// StepError is loop.mjs StepError: an unexpected failure inside a named
// worker step (today: routing). Its text is `<step>: <name>: <message>`.
type StepError struct {
	Step string
	// Name and Message are the JavaScript error's name and message.
	Name, Message string
}

func (e *StepError) Error() string { return e.Step + ": " + e.Name + ": " + e.Message }

// nodeIOError is a file-system failure Node throws with this exact message
// (libuv's `<CODE>: <description>, <syscall> '<path>'`); its name is Error.
type nodeIOError struct{ message string }

func (e *nodeIOError) Error() string { return e.message }

// nodeOpenError is Node's message for an open/scandir failure Go can name
// byte for byte (EACCES); nil for any other failure.
func nodeOpenError(err error, syscallName string) error {
	var pe *os.PathError
	if errors.As(err, &pe) && errors.Is(pe.Err, syscall.EACCES) {
		return &nodeIOError{"EACCES: permission denied, " + syscallName + " '" + pe.Path + "'"}
	}
	return nil
}

// keepHandover turns a nodeIOError a caller has no Node mapping for into
// ErrUnsupported, as before B507.
func keepHandover(err error) error {
	var ne *nodeIOError
	if errors.As(err, &ne) {
		return unsupported("ledger read: " + ne.message)
	}
	return err
}

func (w *TicketRunner) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func refusal(status, reason string) ledger.Object {
	o := ledger.Object{{Key: "event", Value: "review.result"}, {Key: "status", Value: status}}
	if reason != "" {
		o = append(o, ledger.Field{Key: "reason", Value: reason})
	}
	return append(o, ledger.Field{Key: "approved", Value: false})
}

// decideRead is queue.mjs readTicket: (outcome, nil, nil) for a refused
// ticket, (nil, ticket, nil) for a valid one.
func (w *TicketRunner) decideRead(id string) (ledger.Object, ledger.Object, error) {
	if !ticketID.MatchString(id) {
		return refusal("refused", "usage"), nil, nil
	}
	data, err := os.ReadFile(filepath.Join(QueueDir(w.RepoRoot), id+".json"))
	if err != nil {
		return refusal("refused", "bad-ticket"), nil, nil
	}
	if !utf8.Valid(data) || loneSurrogate.Match(data) || (len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF) {
		return nil, nil, unsupported("ticket bytes Node would decode with replacements")
	}
	var parsed any
	if json.Unmarshal(data, &parsed) != nil {
		return refusal("refused", "bad-ticket"), nil, nil
	}
	if _, isArray := parsed.([]any); isArray {
		// typeof [] === 'object': assertRowPath(undefined) refuses it
		return refusal("refused", "bad-path"), nil, nil
	}
	t, err := ledger.DecodeObject(data)
	if err != nil {
		return refusal("refused", "bad-ticket"), nil, nil
	}
	file, ok := t.Get("file").(string)
	if !ok || rowPathError(file) {
		return refusal("refused", "bad-path"), nil, nil
	}
	if tree := t.Get("tree"); tree != nil {
		s, ok := tree.(string)
		if !ok || !filepath.IsAbs(s) {
			return refusal("refused", "bad-ticket"), nil, nil
		}
	}
	run, okRun := t.Get("run").(string)
	block, okBlock := t.Get("block").(string)
	hash, okHash := t.Get("content_hash").(string)
	if !okRun || !okBlock || !okHash {
		return refusal("refused", "bad-ticket"), nil, nil
	}
	if hasKey(t, "kind") {
		if t.Get("kind") != "answers" {
			return refusal("refused", "bad-ticket"), nil, nil
		}
		return nil, nil, unsupported("answers ticket")
	}
	p := ticket.Parts{Run: run, Block: block, File: file, ContentHash: hash}
	if tree, ok := t.Get("tree").(string); ok {
		p.Tree = &tree
	}
	if hasKey(t, "acceptance_revision") {
		s, ok := t.Get("acceptance_revision").(string)
		if !ok {
			return refusal("refused", "bad-ticket"), nil, nil
		}
		p.AcceptanceRevision = &s
	}
	if hasKey(t, "reopened_at") {
		s, ok := t.Get("reopened_at").(string)
		if !ok {
			return refusal("refused", "bad-ticket"), nil, nil
		}
		if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
			return nil, nil, unsupported("reopened_at Date.parse undecided")
		}
		p.ReopenedAt = &s
	}
	if ticket.ReviewID(p) != id {
		return refusal("refused", "bad-ticket"), nil, nil
	}
	return nil, t, nil
}

func hasKey(o ledger.Object, key string) bool {
	for _, f := range o {
		if f.Key == key {
			return true
		}
	}
	return false
}

// rowPathError is ticket.mjs assertRowPath (no disk checks): true when it throws.
func rowPathError(file string) bool {
	if file == "" || len(utf16.Encode([]rune(file))) > 4096 || strings.ContainsAny(file, "\x00\\") || strings.HasPrefix(file, "/") {
		return true
	}
	parts := strings.Split(file, "/")
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return true
		}
	}
	top := strings.ToLower(parts[0])
	return top == ".git" || top == ".code-forge"
}

// reviewOne: the refused / unavailable / stale checks. A ticket that passes
// them all would be reviewed: ErrUnsupported.
func (w *TicketRunner) reviewOne(id string, t ledger.Object) (ledger.Object, error) {
	block, _ := t.Get("block").(string)
	file, _ := t.Get("file").(string)
	hash, _ := t.Get("content_hash").(string)
	base := func(status, reason string) ledger.Object {
		o := ledger.Object{{Key: "event", Value: "review.result"}, {Key: "block", Value: block}, {Key: "file", Value: file}, {Key: "content_hash", Value: hash}, {Key: "status", Value: status}}
		if reason != "" {
			o = append(o, ledger.Field{Key: "reason", Value: reason})
		}
		return append(o, ledger.Field{Key: "approved", Value: false})
	}
	if t.Get("run") != w.RunID {
		return base("refused", "other-run"), nil
	}
	contentRoot, code, err := w.contentRootFor(block)
	if err != nil {
		return nil, err
	}
	if code != "" {
		return base("unavailable", code), nil
	}
	hashedIn := w.RepoRoot
	if tree, ok := t.Get("tree").(string); ok {
		hashedIn = tree
	}
	if canonicalPath(hashedIn) != canonicalPath(contentRoot) {
		return base("unavailable", "worktree-mismatch"), nil
	}
	data, err := gitx.ReadRegularFileNoFollow(contentRoot, file)
	if err != nil {
		var ge *gitx.Error
		if errors.As(err, &ge) && ge.Code == "bad-path" {
			return base("refused", "bad-path"), nil
		}
		return base("refused", "unreadable"), nil
	}
	current := gitx.DeletedHash
	if data != nil {
		sum := sha256.Sum256(data)
		current = hex.EncodeToString(sum[:])
	}
	if current == gitx.DeletedHash && hash != gitx.DeletedHash {
		return base("stale", "missing-file"), nil
	}
	if current != hash {
		return base("stale", ""), nil
	}
	retry := t.Get("started_at") != nil || w.resultExists(id)
	cfg, bad, err := w.configFor(id, retry)
	if err != nil {
		return nil, err
	} else if bad {
		return base("unavailable", "config-snapshot"), nil
	}
	return w.reviewWith(id, t, cfg, contentRoot, data, base)
}

func canonicalPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func (w *TicketRunner) resultExists(id string) bool {
	data, err := os.ReadFile(filepath.Join(w.RepoRoot, ".code-forge", "reviews", w.RunID, id+".json"))
	if err != nil {
		return false
	}
	var v any
	return json.Unmarshal(data, &v) == nil && v != nil
}

// readRecord is state/run.mjs readRun: (nil, false, nil) for no record,
// (nil, true, nil) for a record JSON.parse rejects; an object, or a JSON value
// that is not one (null, a number, an array as an empty object).
func (w *TicketRunner) readRecord() (rec ledger.Object, syntax bool, err error) {
	rec, syntax, notObject, err := w.readRecordValue()
	return rec, syntax || notObject, err
}

// readRecordValue is readRecord telling a JSON.parse failure (syntax) from a
// record that parses to a non-object (notObject: null, a number, a string, a
// boolean). An I/O failure other than a missing file is a plain error: Node
// rethrows it (B507).
func (w *TicketRunner) readRecordValue() (rec ledger.Object, syntax, notObject bool, err error) {
	read := w.ReadRecordFile
	if read == nil {
		read = os.ReadFile
	}
	data, err := read(filepath.Join(w.Home, ".code-forge", "runs", w.RunID+".json"))
	if os.IsNotExist(err) {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, fmt.Errorf("run record read: %w", err)
	}
	if !utf8.Valid(data) || loneSurrogate.Match(data) {
		return nil, false, false, unsupported("run record bytes Node would decode with replacements")
	}
	var v any
	if json.Unmarshal(data, &v) != nil {
		return nil, true, false, nil
	}
	switch v.(type) {
	case map[string]any:
		rec, err := ledger.DecodeObject(data)
		if err != nil {
			return nil, false, false, unsupported("run record decode")
		}
		return rec, false, false, nil
	case []any:
		return ledger.Object{}, false, false, nil
	}
	return nil, false, true, nil
}

// contentRootFor is engine.mjs contentRootFor: the root, or an unavailable
// code (run-record-unreadable or a proof.TreeError code). A failure Node
// rethrows is a plain error: the ticket ends worker-error (B507).
func (w *TicketRunner) contentRootFor(block string) (string, string, error) {
	rec, syntax, err := w.readRecord()
	if err != nil {
		return "", "", err
	}
	if syntax {
		return "", "run-record-unreadable", nil
	}
	if rec == nil {
		return w.RepoRoot, "", nil
	}
	blocks, ok := rec.Get("blocks").(ledger.Object)
	if !ok {
		if rec.Get("blocks") != nil {
			if _, isArr := rec.Get("blocks").([]any); isArr {
				return "", "", unsupported("blocks is an array")
			}
		}
		return w.RepoRoot, "", nil
	}
	entry, ok := blocks.Get(block).(ledger.Object)
	if !ok {
		if _, isArr := blocks.Get(block).([]any); isArr {
			return "", "", unsupported("block entry is an array")
		}
		return w.RepoRoot, "", nil
	}
	if entry.Get("worktree") == nil {
		return w.RepoRoot, "", nil
	}
	workspace, ok := rec.Get("workspace").(string)
	if !ok || !filepath.IsAbs(workspace) {
		return "", "run-record-unreadable", nil
	}
	tree, err := proof.BlockTree(workspace, entry)
	if err != nil {
		// treeTopOf: a TREE_CODES refusal is the ticket's reason; any other
		// failure (an I/O error) is rethrown, so the ticket ends worker-error
		if code := proof.TreeCode(err); code != "" {
			return "", code, nil
		}
		return "", "", err
	}
	if tree == nil {
		return w.RepoRoot, "", nil
	}
	return canonicalPath(tree.Path), "", nil
}

// iso is new Date().toISOString().
func iso(t time.Time) string { return ISO(t) }

func (w *TicketRunner) timings(a *attempt, finished *time.Time) ledger.Object {
	var fin, dur any
	if finished != nil {
		fin = iso(*finished)
		// Date.parse works in whole milliseconds: subtract the truncated stamps
		ms := finished.Truncate(time.Millisecond).Sub(a.startedAt.Truncate(time.Millisecond)).Milliseconds()
		if ms < 0 {
			ms = 0
		}
		dur = float64(ms)
	}
	return ledger.Object{{Key: "queued_at", Value: a.queuedAt}, {Key: "started_at", Value: iso(a.startedAt)}, {Key: "finished_at", Value: fin}, {Key: "duration_ms", Value: dur}}
}

// writeTimings is queue.mjs writeTicketTimings: {...ticket, ...timings}.
func (w *TicketRunner) writeTimings(t ledger.Object, a *attempt, finished *time.Time) error {
	id, _ := t.Get("ticket").(string)
	if !ticketID.MatchString(id) {
		return fmt.Errorf("a ticket id is 24 lowercase hex characters")
	}
	out := t.Clone()
	for _, f := range w.timings(a, finished) {
		out.Set(f.Key, f.Value)
	}
	data, err := line(out)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(QueueDir(w.RepoRoot), id+".json"), data, 0o644)
}

// signObject is signer.mjs signRow over an ordered row: ts (when absent) then mac last.
func (w *TicketRunner) signObject(row ledger.Object) (ledger.Object, error) {
	out := row.Clone()
	if out.Get("ts") == nil {
		out.Set("ts", iso(w.now()))
	}
	out.Delete("mac")
	signed, err := sign.SignRow(out.Map(), w.Key)
	if err != nil {
		return nil, err
	}
	out.Set("mac", signed["mac"])
	return out, nil
}

// complete is loop.mjs complete: timings, the signed result and
// done marker (writeResult), then the signed ledger row (a failure there is
// swallowed: the done marker is what the queue reads).
func (w *TicketRunner) complete(id string, outcome ledger.Object, a *attempt) error {
	finished := w.now()
	timings := w.timings(a, &finished)
	if a.ticket != nil {
		if err := w.writeTimings(a.ticket, a, &finished); err != nil {
			return err
		}
	}
	result := ledger.Object{}
	if a.ticket != nil {
		for _, k := range []string{"block", "file", "content_hash"} {
			result = append(result, ledger.Field{Key: k, Value: a.ticket.Get(k)})
		}
	}
	for _, f := range recordedVerdict(outcome) {
		result.Set(f.Key, f.Value)
	}
	for _, f := range timings {
		result.Set(f.Key, f.Value)
	}
	result.Set("worker", w.Worker)
	// queue.mjs writeResult: a split names its budget and round-1 lens.
	measured := measuredOf(result)
	for _, f := range measured {
		result.Set(f.Key, f.Value)
	}
	if !ticketID.MatchString(id) {
		return fmt.Errorf("a ticket id is 24 lowercase hex characters")
	}
	result.Set("run", w.RunID)
	result.Set("ticket", id)
	signed, err := w.signObject(result)
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
	done = append(done, timings...)
	if openFindingsMarked(signed) {
		done = append(done, ledger.Field{Key: "open_findings", Value: true})
	}
	done = append(done, ledger.Field{Key: "result", Value: rel})
	data, err = line(done)
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(QueueDir(w.RepoRoot), id+".done"), data, 0o644); err != nil {
		return err
	}
	row := ledger.Object{{Key: "run", Value: w.RunID}, {Key: "event", Value: "review.result"}, {Key: "ticket", Value: id}, {Key: "worker", Value: w.Worker}}
	for _, k := range []string{"block", "file", "content_hash"} {
		row = append(row, ledger.Field{Key: k, Value: result.Get(k)})
	}
	row = append(row, ledger.Field{Key: "status", Value: result.Get("status")})
	row = append(row, timings...)
	row = append(row, ledger.Field{Key: "approved", Value: result.Get("approved")}, ledger.Field{Key: "open", Value: result.Get("open")})
	if sessions := needsFileSessions(result.Get("sessions")); len(sessions) > 0 {
		row = append(row, ledger.Field{Key: "sessions", Value: sessions})
	}
	for _, k := range []string{"reason", "trigger", "stopped"} {
		if v, ok := result.Get(k).(string); ok {
			row = append(row, ledger.Field{Key: k, Value: v})
		}
	}
	row = append(row, packetSizeOf(result)...)
	if signedRow, err := w.signObject(row); err == nil {
		_ = w.writeRow(signedRow)
	}
	return nil
}

func (w *TicketRunner) writeRow(row ledger.Object) error {
	if w.WriteRow != nil {
		return w.WriteRow(row)
	}
	_, err := ledger.AppendObject(filepath.Join(w.Home, ".code-forge", "ledger"), w.Slug, row, ledger.DefaultRotateAtBytes)
	return err
}

// measuredOf is writeResult's measured fields: a split_required result (or
// one stopped for a split) names its budget and its round-1 lens.
func measuredOf(r ledger.Object) []ledger.Field {
	status, _ := r.Get("status").(string)
	stopped, _ := r.Get("stopped").(string)
	if status != "split_required" && !(status == "stopped" && stopped == "split_required") {
		return nil
	}
	var lens any
	if r.Get("lens") != nil {
		lens = r.Get("lens")
	} else if status == "stopped" {
		lens = "recheck"
	} else if d := r.Get("depth"); d == "quick" {
		lens = "quick"
	} else if d == "dual" {
		lens = "A"
	} else {
		lens = "full"
	}
	return []ledger.Field{{Key: "budget", Value: r.Get("budget")}, {Key: "lens", Value: lens}}
}

// openFindingsMarked is writeResult's open_findings: a reviewed, not
// approved result with findings.
func openFindingsMarked(r ledger.Object) bool {
	if r.Get("status") != "reviewed" || r.Get("approved") == true {
		return false
	}
	switch list := r.Get("findings").(type) {
	case []any:
		return len(list) > 0
	case []ledger.Object:
		return len(list) > 0
	}
	return false
}
