package worker

// B534: the opt-in commit watch (src/worker/watch.mjs watchTick) in the Go
// worker. Each scheduler tick (Pool.WatchTick, only while the worker is not
// stopping) reads every open block's HEAD (its worktree, else the repository),
// waits for 15 s without a new commit (at most 120 s per burst), then queues
// one review ticket per owned, not excluded file the commits changed, read
// from git objects. Rows (watch.skipped, watch.held), the cursor file
// .code-forge/queue/watch/<block>.json, tickets and superseded markers are
// written as Node writes them, under Node's `watch:<repo>\0<block>` lock.
// A shape Go does not reproduce byte for byte (bytes Node decodes with
// replacements, a timestamp Go cannot parse like Date.parse, a review.exclude
// Go cannot match, the repository run-record fallback) hands the worker to
// Node before any ticket of that tick is written.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/childreg"
	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/reviewq"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/ticket"
)

const (
	watchQuietMs    = 15000
	watchMaxBurstMs = 120000
	watchMaxBuffer  = 10 * 1024 * 1024
)

// WatchHandOff starts the reason a watch tick gives the worker to Node with.
const WatchHandOff = "watch: "

var (
	watchSHA     = regexp.MustCompile(`^[0-9a-f]{40,64}$`)
	watchBlockID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	watchBlob    = regexp.MustCompile(`^100(?:644|755) blob `)
	watchDecimal = regexp.MustCompile(`^[+-]?(?:[0-9]+\.?[0-9]*|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
)

// Watch is watchTick for one worker. Runner gives the run, the repository,
// the boot config (Cfg) and the signed ledger writer.
type Watch struct {
	Runner *TicketRunner
	// Now is the debounce clock in ms (Date.now); Clock stamps tickets (new Date()).
	Now   func() int64
	Clock func() time.Time
	// Git runs git in dir (the exec seam); ok is Node's result 'ok'.
	Git func(dir string, env []string, args ...string) (stdout []byte, ok bool)
	// HandOff gives the worker to Node (Loop.HandOff).
	HandOff func(reason string)
	// AfterHead and BeforeQueue are watchTick's race hooks, for tests.
	AfterHead   func(head string)
	BeforeQueue func()
}

// watchUndef is JavaScript's undefined (an absent property).
type watchUndef struct{}

var watchUndefined any = watchUndef{}

// watchThrow is a value Node throws: errorKind reads its kind.
type watchThrow struct{ kind string }

func (e *watchThrow) Error() string { return e.kind }
func (e *watchThrow) Kind() string  { return e.kind }

// watchUnported is a shape Go leaves to Node.
type watchUnported struct{ why string }

func (e *watchUnported) Error() string { return WatchHandOff + e.why }

// Tick is one scheduler tick. A shape Go does not reproduce hands the worker
// to Node and returns nil; any other error is the tick's failure (the pool
// warns `watch tick failed (<kind>)`).
func (w *Watch) Tick(running map[string]bool) error {
	err := w.tick(running)
	var hand *watchUnported
	if errors.As(err, &hand) && w.HandOff != nil {
		w.HandOff(hand.Error())
		return nil
	}
	return err
}

func watchOn(cfg map[string]any) bool {
	review, _ := cfg["review"].(map[string]any)
	return review["file_loop"] == "watch"
}

func (w *Watch) tick(running map[string]bool) error {
	r := w.Runner
	if !watchOn(r.Cfg) {
		return nil
	}
	data, err := os.ReadFile(filepath.Join(r.Home, ".code-forge", "runs", r.RunID+".json"))
	if os.IsNotExist(err) {
		return &state.Error{Code: "no-run", Message: "no run record for " + r.RunID}
	}
	if err != nil {
		return err
	}
	if !utf8.Valid(data) || loneSurrogate.Match(data) {
		return &watchUnported{"run record bytes Node decodes with replacements"}
	}
	record, err := ledger.DecodeValue(data)
	if err != nil {
		return &watchThrow{"SyntaxError"}
	}
	if record == nil {
		return &watchThrow{"TypeError"}
	}
	if watchProp(record, "status") != "active" {
		return nil
	}
	cfg := r.Cfg
	snapshot, bad, err := r.configInForce(data)
	if err != nil {
		return &watchUnported{"config snapshot"}
	}
	if bad {
		return &watchThrow{"Error"} // snapshotFor throws Error('config-snapshot')
	}
	if snapshot != nil {
		cfg = snapshot
	}
	if !watchOn(cfg) {
		return nil
	}
	env := gitChildEnv()
	for _, field := range watchEntries(watchProp(record, "blocks")) {
		block, entry := field.Key, field.Value
		if entry == nil {
			return &watchThrow{"TypeError"} // null.status
		}
		if watchProp(entry, "status") != "open" || !watchBlockID.MatchString(block) {
			continue
		}
		real, err := filepath.EvalSymlinks(r.RepoRoot)
		if err != nil {
			return err
		}
		if err := filelock.WithLock("watch:"+real+"\x00"+block, func() error {
			return w.block(record, block, entry, cfg, running, env)
		}, filelock.Options{}); err != nil {
			return err
		}
	}
	return nil
}

// block is the locked body for one open block.
func (w *Watch) block(record any, block string, entry any, cfg map[string]any, running map[string]bool, env []string) error {
	r := w.Runner
	stateFile := filepath.Join(QueueDir(r.RepoRoot), "watch", block+".json")
	st, err := watchReadState(stateFile)
	if err != nil {
		return err
	}
	base := watchProp(entry, "base_sha")
	save := func(next ledger.Object) error {
		if err := os.MkdirAll(filepath.Dir(stateFile), 0777); err != nil {
			return err
		}
		data, err := ledger.Stringify(next, false)
		if err != nil {
			return err
		}
		var random [4]byte
		if _, err := rand.Read(random[:]); err != nil {
			return err
		}
		tmp := fmt.Sprintf("%s.%d.%x.tmp", stateFile, os.Getpid(), random)
		defer os.Remove(tmp)
		if err := os.WriteFile(tmp, append(data, '\n'), 0600); err != nil {
			return err
		}
		if err := os.Rename(tmp, stateFile); err != nil {
			return err
		}
		st = next
		return nil
	}
	skip := func(reason string) error {
		skipped, _ := watchProp(st, "skipped").([]any)
		for _, s := range skipped {
			if s == reason {
				return nil
			}
		}
		if err := r.Ledger(ledger.Object{{Key: "event", Value: "watch.skipped"}, {Key: "block", Value: block}, {Key: "reason", Value: reason}}); err != nil {
			return err
		}
		next := st.Clone()
		next.Set("skipped", append(append([]any{}, skipped...), reason))
		return save(next)
	}
	err = w.watchBody(record, block, entry, cfg, running, env, base, &st, save, skip)
	var hand *watchUnported
	if err == nil || errors.As(err, &hand) {
		return err
	}
	// A bad path, missing tree or failed git read is a skip, never a block failure.
	return skip("unreadable_head")
}

type watchHash struct{ file, hash string }

// watchPlan is enqueueHash decided before any ticket of the tick is written.
type watchPlan struct {
	err        error // thrown when this file is queued
	excluded   bool
	parts      ticket.Parts
	id         string
	supersedes []string
}

func (w *Watch) watchBody(record any, block string, entry any, cfg map[string]any, running map[string]bool, env []string, base any, st *ledger.Object,
	save func(ledger.Object) error, skip func(string) error) error {
	r := w.Runner
	thrown := &watchThrow{"Error"}
	var tree any // null
	root := r.RepoRoot
	if worktree := watchProp(entry, "worktree"); watchTruthy(worktree) {
		p, ok := watchProp(worktree, "path").(string)
		if !ok {
			return thrown
		}
		real, err := watchRealpath(p)
		if err != nil {
			return thrown
		}
		root, tree = real, real
	}
	if !watchSame(watchProp(*st, "run"), r.RunID) || !watchSame(watchProp(*st, "base"), base) || !watchSame(watchProp(*st, "tree"), tree) {
		fresh := ledger.Object{{Key: "run", Value: r.RunID}}
		watchSet(&fresh, "base", base)
		fresh.Set("tree", tree)
		fresh.Set("skipped", []any{})
		*st = fresh
	}
	git := func(args ...string) ([]byte, bool) { return w.git(root, env, args...) }
	out, ok := git("rev-parse", "--verify", "HEAD^{commit}")
	head := watchTrim(string(out))
	if !ok || !watchSHA.MatchString(head) {
		return skip("unreadable_head")
	}
	var treePtr *string
	if s, isString := tree.(string); isString {
		treePtr = &s
	}
	idx := &queue.Index{Dir: QueueDir(r.RepoRoot)}
	if err := idx.Refresh(); err != nil {
		return thrown
	}
	revision, err := w.revision(block)
	if err != nil {
		return err
	}
	earlier := idx.Block(r.RunID, block, treePtr, revision)
	if w.AfterHead != nil {
		w.AfterHead(head)
	}
	baseSHA, isString := base.(string)
	if !isString || !watchSHA.MatchString(baseSHA) {
		return skip("not_descendant")
	}
	if _, ok := git("merge-base", "--is-ancestor", baseSHA, head); !ok {
		return skip("not_descendant")
	}
	last := baseSHA
	if s, ok := watchProp(*st, "sha").(string); ok && watchSHA.MatchString(s) {
		last = s
	}
	previous := last
	if s, ok := watchProp(watchProp(*st, "pending"), "sha").(string); ok && watchSHA.MatchString(s) {
		previous = s
	}
	if previous != baseSHA {
		if _, ok := git("merge-base", "--is-ancestor", previous, head); !ok {
			if err := skip("rewound"); err != nil {
				return err
			}
			restart := st.Clone()
			restart.Delete("sha")
			restart.Delete("pending")
			restart.Delete("burst_started_at")
			return save(restart)
		}
	}
	// A healthy tick ends the skip streak: no new commit here, or a queued commit below.
	if last == head {
		if watchTruthy(watchProp(watchProp(*st, "skipped"), "length")) {
			next := st.Clone()
			next.Set("skipped", []any{})
			return save(next)
		}
		return nil
	}
	now := w.Now
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	at := float64(now())
	if !watchSame(watchProp(watchProp(*st, "pending"), "sha"), head) {
		// A new HEAD restarts the quiet wait, but keeps the burst's original deadline.
		next := st.Clone()
		next.Set("pending", ledger.Object{{Key: "sha", Value: head}, {Key: "seen_at", Value: at}})
		burst := watchProp(*st, "burst_started_at")
		if burst == nil || burst == watchUndefined {
			burst = at
		}
		next.Set("burst_started_at", burst)
		if err := save(next); err != nil {
			return err
		}
	}
	seen := watchNumber(watchProp(watchProp(*st, "pending"), "seen_at"))
	burst := watchNumber(watchProp(*st, "burst_started_at"))
	if at-seen < watchQuietMs && at-burst < watchMaxBurstMs {
		return nil
	}
	diff, ok := git("diff", "--name-only", "-z", "--no-renames", last, head, "--")
	if !ok {
		return skip("unreadable_head")
	}
	if !utf8.Valid(diff) {
		return &watchUnported{"changed file names that are not UTF-8"}
	}
	prefix := ""
	if tree == nil {
		workspace, ok := watchProp(record, "workspace").(string)
		if !ok {
			return thrown
		}
		prefix = watchRelative(r.RepoRoot, workspace)
	}
	prefix = strings.TrimRight(prefix, "/")
	files := []string{}
	for _, f := range strings.Split(string(diff), "\x00") {
		if f != "" {
			files = append(files, f)
		}
	}
	ownedFiles := []string{}
	if len(files) > 0 {
		list, isList := watchProp(entry, "owned_files").([]any)
		if !isList {
			return thrown // owned.some is not a function
		}
		kindsValue := watchProp(entry, "owned_kinds")
		if _, isArray := kindsValue.([]any); isArray {
			return &watchUnported{"owned_kinds that is an array"}
		}
		kinds, _ := kindsValue.(ledger.Object)
		for _, f := range files {
			if owned.OwnsFile(list, kinds, f) || prefix != "" && prefix != "." && strings.HasPrefix(f, prefix+"/") && owned.OwnsFile(list, kinds, f[len(prefix)+1:]) {
				ownedFiles = append(ownedFiles, f)
			}
		}
	}
	if len(ownedFiles) != len(files) {
		if err := skip("unowned_path"); err != nil {
			return err
		}
	}
	// Stage every hash before publishing anything: an unreadable object queues nothing.
	hashes := []watchHash{}
	unreadable := false
	for _, f := range ownedFiles {
		rule, ok := reviewq.Exclusion(cfg, f)
		if !ok {
			return &watchUnported{"review.exclude Go cannot match"}
		}
		if rule != "" {
			continue
		}
		if rowPathError(f) {
			return thrown
		}
		listed, ok := git("ls-tree", "-z", head, "--", f)
		if !ok {
			unreadable = true
			break
		}
		if len(listed) == 0 {
			hashes = append(hashes, watchHash{f, "deleted"})
			continue
		}
		if !watchBlob.Match(listed) {
			unreadable = true
			break
		}
		shown, ok := git("show", head+":"+f)
		if !ok {
			unreadable = true
			break
		}
		sum := sha256.Sum256(shown)
		hashes = append(hashes, watchHash{f, hex.EncodeToString(sum[:])})
	}
	if unreadable {
		return skip("unreadable_head")
	}
	if w.BeforeQueue != nil {
		w.BeforeQueue()
	}
	held := func(file string) bool {
		for _, old := range earlier {
			if old["file"] == file {
				return true
			}
		}
		return false
	}
	plans := make([]watchPlan, len(hashes))
	for i, h := range hashes {
		if !held(h.file) {
			plan, err := w.plan(block, h, treePtr)
			var hand *watchUnported
			if errors.As(err, &hand) {
				return err
			}
			plans[i] = plan
		}
	}
	dir := QueueDir(r.RepoRoot)
	for i, h := range hashes {
		isHeld := held(h.file)
		if !isHeld {
			if err := w.enqueue(dir, plans[i]); err != nil {
				return err
			}
		}
		for _, old := range earlier {
			id, _ := old["ticket"].(string)
			if old["file"] == h.file && old["content_hash"] != h.hash && !running[id] && !IsDone(r.RepoRoot, id) {
				if err := watchSupersede(dir, id); err != nil {
					return err
				}
			}
		}
		if isHeld {
			if err := r.Ledger(ledger.Object{{Key: "event", Value: "watch.held"}, {Key: "block", Value: block}, {Key: "file", Value: h.file}, {Key: "content_hash", Value: h.hash}}); err != nil {
				return err
			}
		}
	}
	queued := st.Clone()
	queued.Delete("pending")
	queued.Delete("burst_started_at")
	queued.Set("run", r.RunID)
	watchSet(&queued, "base", base)
	queued.Set("tree", tree)
	queued.Set("sha", head)
	queued.Set("skipped", []any{})
	return save(queued)
}

// plan is enqueueHash up to its first write: the exclusion it re-reads from
// the run record, then reviewIdentity (acceptance revision, latest reopen).
// A Node throw is kept for the file's turn; a shape Go does not reproduce
// returns watchUnported.
func (w *Watch) plan(block string, h watchHash, tree *string) (watchPlan, error) {
	r := w.Runner
	rule, err := r.exclusion(h.file, nil)
	if err != nil {
		if IsUnsupported(err) {
			return watchPlan{}, &watchUnported{"review exclusion: " + err.Error()}
		}
		return watchPlan{err: err}, nil
	}
	if rule != "" {
		return watchPlan{excluded: true}, nil
	}
	parts := ticket.Parts{Run: r.RunID, Block: block, File: h.file, ContentHash: h.hash}
	if tree != nil {
		real, err := watchRealpath(*tree)
		if err != nil {
			return watchPlan{err: &watchThrow{"WorkerError:bad-path"}}, nil
		}
		parts.Tree = &real
	}
	revision, err := w.revision(block)
	if err != nil {
		return watchPlan{}, err
	}
	parts.AcceptanceRevision = revision
	idx := &queue.Index{Dir: QueueDir(r.RepoRoot)}
	if err := idx.Refresh(); err != nil {
		return watchPlan{err: &watchThrow{"WorkerError:bad-ticket"}}, nil
	}
	matching := []queue.Object{}
	for _, t := range idx.Block(r.RunID, block, parts.Tree, revision) {
		if t["file"] == h.file && t["content_hash"] == h.hash {
			matching = append(matching, t)
		}
	}
	for i := len(matching) - 1; i >= 0; i-- {
		if value, present := matching[i]["reopened_at"]; present {
			at, ok := value.(string)
			if !ok {
				return watchPlan{}, &watchUnported{"a reopen time that is not a string"}
			}
			parts.ReopenedAt = &at
			break
		}
	}
	id := ticket.ReviewID(parts)
	plan := watchPlan{parts: parts, id: id}
	if parts.ReopenedAt != nil {
		reopened, ok := watchDate(*parts.ReopenedAt)
		if !ok {
			return watchPlan{}, &watchUnported{"a reopen time Go cannot parse like Date.parse"}
		}
		for _, previous := range matching {
			prevID, _ := previous["ticket"].(string)
			if prevID == id {
				continue
			}
			stamp, _ := previous["enqueued_at"].(string)
			enqueued, ok := watchDate(stamp)
			if !ok {
				return watchPlan{}, &watchUnported{"an enqueue time Go cannot parse like Date.parse"}
			}
			if !enqueued.After(reopened) {
				plan.supersedes = append(plan.supersedes, prevID)
			}
		}
	}
	return plan, nil
}

// enqueue is enqueueHash's writes: retire the attempts a reopen replaces,
// publish the ticket when missing, then drop a superseded marker so the
// returning content is current again.
func (w *Watch) enqueue(dir string, plan watchPlan) error {
	if plan.err != nil {
		return plan.err
	}
	if plan.excluded {
		return nil
	}
	for _, id := range plan.supersedes {
		if err := watchSupersede(dir, id); err != nil {
			return err
		}
	}
	clock := w.Clock
	if clock == nil {
		clock = time.Now
	}
	if err := queue.Publish(dir, plan.parts, plan.id, clock()); err != nil {
		return err
	}
	seen, err := os.ReadFile(filepath.Join(dir, plan.id+".done"))
	if err != nil {
		return nil
	}
	var done any
	if json.Unmarshal(seen, &done) != nil {
		return nil
	}
	if m, ok := done.(map[string]any); ok && m["status"] == "refused" && m["reason"] == "superseded" && !truthy(m["result"]) {
		if _, err := queue.DropDone(dir, plan.id, seen); err != nil {
			return err
		}
	}
	return nil
}

// revision is acceptanceRevision: readJSON(home record) ?? readJSON(repo
// mirror), then blocks[block].acceptance_revision when it is a string.
func (w *Watch) revision(block string) (*string, error) {
	r := w.Runner
	record, err := watchReadJSON(filepath.Join(r.Home, ".code-forge", "runs", r.RunID+".json"))
	if err != nil {
		return nil, err
	}
	if record == nil {
		if record, err = watchReadJSON(filepath.Join(r.RepoRoot, ".code-forge", "runs", r.RunID+".json")); err != nil {
			return nil, err
		}
	}
	if s, ok := watchProp(watchProp(watchProp(record, "blocks"), block), "acceptance_revision").(string); ok {
		return &s, nil
	}
	return nil, nil
}

// watchReadJSON is readJSON: nil for a missing or unparsable file.
func watchReadJSON(file string) (any, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, nil
	}
	if !utf8.Valid(data) || loneSurrogate.Match(data) {
		return nil, &watchUnported{"JSON bytes Node decodes with replacements"}
	}
	v, err := ledger.DecodeValue(data)
	if err != nil {
		return nil, nil
	}
	return v, nil
}

// watchReadState is `JSON.parse(...) ?? {}` (unreadable: {}), as the object
// a spread of it gives.
func watchReadState(file string) (ledger.Object, error) {
	v, err := watchReadJSON(file)
	if err != nil {
		return nil, err
	}
	switch x := v.(type) {
	case ledger.Object:
		return x, nil
	case []any:
		out := ledger.Object{}
		for i, item := range x {
			out = append(out, ledger.Field{Key: strconv.Itoa(i), Value: item})
		}
		return out, nil
	case string:
		out := ledger.Object{}
		for i, unit := range utf16.Encode([]rune(x)) {
			if utf16.IsSurrogate(rune(unit)) {
				return nil, &watchUnported{"a cursor string with surrogate pairs"}
			}
			out = append(out, ledger.Field{Key: strconv.Itoa(i), Value: string(rune(unit))})
		}
		return out, nil
	}
	return ledger.Object{}, nil
}

// watchSupersede is supersedeTicket: a refused/superseded marker, never over one.
func watchSupersede(dir, id string) error {
	if !ticketID.MatchString(id) {
		return &watchThrow{"WorkerError:bad-ticket"}
	}
	marker := filepath.Join(dir, id+".done")
	var random [4]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.%x.tmp", marker, os.Getpid(), random)
	defer os.Remove(tmp)
	body := fmt.Sprintf("{\"ticket\":%q,\"status\":\"refused\",\"reason\":\"superseded\"}\n", id)
	if err := os.WriteFile(tmp, []byte(body), 0644); err != nil {
		return err
	}
	if err := os.Link(tmp, marker); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

// git is Node's exec(['git', ...], {cwd, env, timeoutMs: 10000}): ok only
// for exit 0 within the time and the 10 MiB per-stream cap.
func (w *Watch) git(dir string, env []string, args ...string) ([]byte, bool) {
	if w.Git != nil {
		return w.Git(dir, env, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = time.Second
	var stdout, stderr watchCapped
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := childreg.Run(cmd)
	return stdout.buf, err == nil && !stdout.over && !stderr.over
}

type watchCapped struct {
	buf  []byte
	over bool
}

func (c *watchCapped) Write(p []byte) (int, error) {
	if c.over || len(c.buf)+len(p) > watchMaxBuffer {
		c.over = true
		return len(p), nil
	}
	c.buf = append(c.buf, p...)
	return len(p), nil
}

// watchRealpath is realpathSync: relative to the process directory.
func watchRealpath(p string) (string, error) {
	if p == "" || strings.ContainsRune(p, 0) {
		return "", errors.New("bad path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}

// watchRelative is path.relative(from, to); the same path gives "".
func watchRelative(from, to string) string {
	a, errA := filepath.Abs(from)
	b, errB := filepath.Abs(to)
	if errA != nil || errB != nil {
		return ""
	}
	rel, err := filepath.Rel(a, b)
	if err != nil || rel == "." {
		return ""
	}
	return rel
}

// watchProp is a property read (`v?.[key]`): undefined when absent.
func watchProp(v any, key string) any {
	switch x := v.(type) {
	case ledger.Object:
		for _, f := range x {
			if f.Key == key {
				return f.Value
			}
		}
	case []any:
		if key == "length" {
			return float64(len(x))
		}
		if i, ok := watchArrayIndex(key); ok && i < uint64(len(x)) {
			return x[i]
		}
	case string:
		if key == "length" {
			return float64(len(utf16.Encode([]rune(x))))
		}
	}
	return watchUndefined
}

// watchSet is `{...o, key: value}` for one key; undefined is left out, as
// JSON.stringify leaves it out.
func watchSet(o *ledger.Object, key string, value any) {
	if value == watchUndefined {
		o.Delete(key)
		return
	}
	o.Set(key, value)
}

func watchArrayIndex(key string) (uint64, bool) {
	n, err := strconv.ParseUint(key, 10, 32)
	return n, err == nil && n < 4294967295 && strconv.FormatUint(n, 10) == key
}

// watchEntries is Object.entries for a JSON value (index keys first).
func watchEntries(v any) []ledger.Field {
	switch x := v.(type) {
	case ledger.Object:
		fields := x.Clone()
		sort.SliceStable(fields, func(i, j int) bool {
			left, a := watchArrayIndex(fields[i].Key)
			right, b := watchArrayIndex(fields[j].Key)
			if a != b {
				return a
			}
			return a && left < right
		})
		return fields
	case []any:
		fields := []ledger.Field{}
		for i, item := range x {
			fields = append(fields, ledger.Field{Key: strconv.Itoa(i), Value: item})
		}
		return fields
	}
	return nil // strings give one-character entries: none is open
}

// watchSame is `===` between JSON values (objects are never the same).
func watchSame(a, b any) bool {
	switch x := a.(type) {
	case watchUndef:
		return b == watchUndefined
	case nil:
		return b == nil
	case string:
		y, ok := b.(string)
		return ok && x == y
	case float64:
		y, ok := b.(float64)
		return ok && x == y
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	}
	return false
}

func watchTruthy(v any) bool {
	switch x := v.(type) {
	case watchUndef, nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0 && !math.IsNaN(x)
	}
	return true
}

// watchTrim is String.prototype.trim.
func watchTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return r == 0xFEFF || r != 0x85 && unicode.IsSpace(r) })
}

// watchNumber is ToNumber for a JSON value (the debounce arithmetic).
func watchNumber(v any) float64 {
	switch x := v.(type) {
	case nil:
		return 0
	case bool:
		if x {
			return 1
		}
		return 0
	case float64:
		return x
	case string:
		return watchStringNumber(x)
	case []any:
		return watchStringNumber(watchString(x))
	}
	return math.NaN() // undefined, objects
}

// watchString is ToString inside an array join.
func watchString(v any) string {
	switch x := v.(type) {
	case nil, watchUndef:
		return ""
	case bool:
		return strconv.FormatBool(x)
	case float64:
		data, _ := canon.Marshal(x)
		return string(data)
	case string:
		return x
	case []any:
		parts := make([]string, len(x))
		for i, item := range x {
			parts[i] = watchString(item)
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

// watchStringNumber is StringToNumber.
func watchStringNumber(s string) float64 {
	s = watchTrim(s)
	switch s {
	case "":
		return 0
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if len(s) > 2 && s[0] == '0' {
		base := map[byte]int{'x': 16, 'X': 16, 'o': 8, 'O': 8, 'b': 2, 'B': 2}[s[1]]
		if base != 0 {
			n, ok := new(big.Int).SetString(s[2:], base)
			if !ok || strings.ContainsAny(s[2:], "_+-") {
				return math.NaN()
			}
			f, _ := new(big.Float).SetInt(n).Float64()
			return f
		}
	}
	if !watchDecimal.MatchString(s) {
		return math.NaN()
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// watchDate parses the ISO timestamps Node writes (Date.parse); false for
// any other shape.
func watchDate(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.Truncate(time.Millisecond), true
}
