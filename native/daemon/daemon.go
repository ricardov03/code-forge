//go:build unix

// Package daemon serves the run's hot progress reads over a private Unix socket.
package daemon

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/gosafe"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/queue"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/ticket"
)

type object = map[string]any

type server struct {
	mu                                           sync.Mutex
	home, run, version, workspace, repo, project string
	ledger                                       ledger.Index
	queue                                        queue.Index
	rows                                         []ledger.Row
	record                                       object
	recordStamp                                  string
	trees                                        map[string]*treeState
	psProcesses                                  int64
	lastSweep                                    time.Time
	// A local join (Join) has no socket: it reads the scope mirror the CLI
	// found, asks the CLI's liveness probe and never sweeps the queue.
	local bool
	scope string
	live  func() (bool, bool)
}

type treeState struct {
	stamp  string
	base   string
	files  []string
	hashes map[string]fileHash
}
type fileHash struct{ stamp, hash string }

type request struct {
	Method string `json:"method"`
	Block  string `json:"block"`
	MaxMS  *int64 `json:"max_ms"`
}

// Serve owns one run socket until SIGTERM, SIGINT or an idle interval without
// requests. An OS lock serializes startup, including simultaneous stale recovery.
// Active long-polls keep the daemon alive; idle connected clients do not.
func Serve(home, run, version string, idle time.Duration) error {
	if idle <= 0 {
		return fmt.Errorf("idle duration must be positive")
	}
	s := &server{home: home, run: run, version: version, trees: make(map[string]*treeState)}
	record, err := readObject(filepath.Join(home, ".code-forge", "runs", run+".json"))
	if err != nil {
		return fmt.Errorf("read run record: %w", err)
	}
	s.workspace = text(record, "workspace")
	if !filepath.IsAbs(s.workspace) {
		return fmt.Errorf("run workspace must be absolute")
	}
	s.repo, err = git(s.workspace, "rev-parse", "--show-toplevel")
	if err != nil {
		return err
	}
	s.repo, err = filepath.EvalSymlinks(strings.TrimSpace(s.repo))
	if err != nil {
		return err
	}
	s.project = text(record, "project")
	s.queue.Dir = filepath.Join(s.repo, ".code-forge", "queue")
	dir := filepath.Join(home, ".code-forge", "runs", run)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "native.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("a live daemon already owns this run")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err := s.refresh(); err != nil {
		return err
	}
	s.sweep(time.Now())
	socket := filepath.Join(dir, "native.sock")
	if info, err := os.Lstat(socket); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("native.sock is not a socket")
		}
		conn, dialErr := net.DialTimeout("unix", socket, time.Second)
		if dialErr == nil {
			conn.Close()
			return fmt.Errorf("a live daemon already answers this run")
		}
		if !errors.Is(dialErr, syscall.ECONNREFUSED) && !errors.Is(dialErr, syscall.ENOENT) {
			return dialErr
		}
		if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := os.Chmod(socket, 0600); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	var activity sync.Mutex
	last := time.Now()
	active := 0
	touch := func(delta int) { activity.Lock(); active += delta; last = time.Now(); activity.Unlock() }
	connections := make(map[net.Conn]bool)
	var connectionsMu sync.Mutex
	var clients sync.WaitGroup
	defer clients.Wait()
	gosafe.Go("daemon-idle", func() {
		tick := time.NewTicker(min(idle, 200*time.Millisecond))
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				listener.Close()
				connectionsMu.Lock()
				for conn := range connections {
					conn.Close()
				}
				connectionsMu.Unlock()
				return
			case <-tick.C:
				activity.Lock()
				expired := active == 0 && time.Since(last) >= idle
				activity.Unlock()
				if expired {
					cancel()
				}
			}
		}
	})
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			cancel()
			return err
		}
		connectionsMu.Lock()
		if ctx.Err() != nil {
			conn.Close()
			connectionsMu.Unlock()
			continue
		}
		connections[conn] = true
		connectionsMu.Unlock()
		clients.Add(1)
		gosafe.Go("daemon-connection", func() {
			defer clients.Done()
			defer conn.Close()
			defer func() { connectionsMu.Lock(); delete(connections, conn); connectionsMu.Unlock() }()
			scan := bufio.NewScanner(conn)
			scan.Buffer(make([]byte, 4096), 1<<20)
			for scan.Scan() {
				touch(1)
				var req request
				var result object
				if err := json.Unmarshal(scan.Bytes(), &req); err != nil {
					result = failure("bad-request")
				} else {
					result = s.handle(ctx, req)
				}
				conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				err := json.NewEncoder(conn).Encode(result)
				touch(-1)
				if err != nil {
					return
				}
			}
		})
	}
}

func failure(code string) object { return object{"ok": false, "error": code} }

func (s *server) refresh() error {
	file := filepath.Join(s.repo, ".code-forge", "runs", s.run+".json")
	if s.scope != "" {
		file = s.scope
	}
	stamp := fileStamp(file)
	if s.record == nil || stamp != s.recordStamp {
		record, err := readObject(file)
		if err == nil && s.local && s.record == nil {
			s.project = text(record, "project")
		}
		if err != nil || record["run_id"] != s.run || text(record, "project") != s.project {
			return fmt.Errorf("block-scope-unreadable")
		}
		s.record, s.recordStamp = record, stamp
	}
	rows, err := s.ledger.Refresh(filepath.Join(s.home, ".code-forge", "ledger"), s.project)
	if err != nil && !s.local {
		return err
	}
	// Node's join reads reopen rows best effort: sandboxed callers may not
	// read the private ledger, and the worker verifies signed rows anyway.
	s.rows = rows
	return s.queue.Refresh()
}

func (s *server) stats(result object) object {
	result["stats"] = object{"bytes_read": s.ledger.BytesRead, "tickets_parsed": s.queue.TicketsParsed, "rows": len(s.rows), "ps_processes": s.psProcesses}
	return result
}

func (s *server) handle(ctx context.Context, req request) object {
	deadline := time.Now().Add(30 * time.Minute)
	if req.MaxMS != nil {
		if *req.MaxMS < 0 || *req.MaxMS > int64((24*time.Hour)/time.Millisecond) {
			return failure("bad-request")
		}
		deadline = time.Now().Add(time.Duration(*req.MaxMS) * time.Millisecond)
	}
	down := 0
	delivered := make(map[string]bool)
	for {
		s.mu.Lock()
		result, finished := s.step(req, deadline, &down, delivered)
		if finished {
			result = s.stats(result)
		}
		s.mu.Unlock()
		if finished {
			return result
		}
		delay := 200 * time.Millisecond
		if down == 0 && time.Until(deadline) < delay {
			delay = max(0, time.Until(deadline))
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return failure("shutdown")
		case <-timer.C:
		}
	}
}

func (s *server) step(req request, deadline time.Time, down *int, delivered map[string]bool) (object, bool) {
	if err := s.refresh(); err != nil {
		return failure(err.Error()), true
	}
	if !s.local {
		s.sweep(time.Now())
	}
	if req.Method == "rows" {
		if req.Block != "" && !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(req.Block) {
			return failure("bad-request"), true
		}
		rows := []ledger.Row{}
		for _, value := range s.rows {
			row := mapping(value)
			if row["run"] == s.run && (req.Block == "" || row["block"] == req.Block) {
				rows = append(rows, value)
			}
		}
		return object{"rows": rows}, true
	}
	if req.Method == "ping" {
		return object{"ok": true, "version": s.version, "pid": os.Getpid(), "run": s.run}, true
	}
	if req.Method == "status" {
		return s.status(), true
	}
	if req.Method != "block-tickets" && req.Method != "wait-block" && req.Method != "prune" {
		return failure("unknown-method"), true
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`).MatchString(req.Block) {
		return failure("bad-request"), true
	}
	if req.Method == "prune" {
		return s.prune(req.Block), true
	}
	entry := mapping(mapping(s.record["blocks"])[req.Block])
	owned, ok := entry["owned_files"].([]any)
	if !ok || len(owned) == 0 {
		return failure("block-scope-unreadable"), true
	}
	tree := s.repo
	var namedTree *string
	if worktree := mapping(entry["worktree"]); worktree != nil {
		var err error
		tree, err = filepath.EvalSymlinks(text(worktree, "path"))
		if err != nil || !filepath.IsAbs(tree) {
			return failure("worktree-missing"), true
		}
		if tree != s.repo {
			namedTree = &tree
		}
	}
	revision := optional(entry, "acceptance_revision")
	tickets := s.queue.Block(s.run, req.Block, namedTree, revision)
	if req.Method == "block-tickets" {
		return object{"block": req.Block, "tickets": tickets}, true
	}
	byFile := make(map[string][]object)
	for _, t := range tickets {
		file := text(t, "file")
		byFile[file] = append(byFile[file], t)
	}
	cfg, err := config(s.record)
	if err != nil {
		return failure("config-snapshot"), true
	}
	files, err := s.changed(tree, text(entry, "base_sha"), owned)
	if err != nil {
		return failure(err.Error()), true
	}
	live := false
	if s.live != nil {
		var supported bool
		if live, supported = s.live(); !supported {
			return failure("worker-unsupported"), true
		}
	} else {
		live = s.workerLive()
	}
	if live {
		*down = 0
	} else {
		*down++
	}
	if s.local && live {
		if stopped, err := s.stopped(files, owned, mapping(entry["owned_kinds"]), byFile); err != nil || stopped {
			// Restarting a changed stopped review writes a signed reopen row;
			// that mutation stays with Node (requeueStoppedFiles).
			return failure("stopped"), true
		}
	}
	states := []object{}
	done := true
	notQueued := false
	var split, unavailable string
	for _, file := range files {
		if !owns(owned, mapping(entry["owned_kinds"]), file) {
			continue
		}
		state := object{"file": file, "ticket": nil, "status": "not_queued", "outcome": nil}
		if rule := excluded(cfg, file); rule != nil {
			state["status"], state["rule"] = "excluded", *rule
			states = append(states, state)
			continue
		}
		hash, err := s.contentHash(tree, file)
		if err != nil {
			return failure("bad-path"), true
		}
		p := ticket.Parts{Run: s.run, Block: req.Block, File: file, ContentHash: hash, Tree: namedTree, AcceptanceRevision: revision}
		after := s.reopened(req.Block, file)
		matching := []object{}
		previous := []object{}
		for _, t := range byFile[file] {
			if after == "" || later(text(t, "enqueued_at"), after) {
				previous = append(previous, t)
			}
			if t["content_hash"] == hash {
				matching = append(matching, t)
			}
		}
		// Node reuses a manually queued attempt newer than a reopen, or the
		// latest reopened identity when no new reopen has been issued.
		var latest, fresh object
		for _, t := range matching {
			if t["reopened_at"] != nil {
				latest = t
			}
			if after != "" && later(text(t, "enqueued_at"), after) {
				fresh = t
			}
		}
		if fresh != nil {
			p.ReopenedAt = optional(fresh, "reopened_at")
		} else if after != "" {
			p.ReopenedAt = &after
		} else {
			p.ReopenedAt = optional(latest, "reopened_at")
		}
		id := ticket.ReviewID(p)
		present := false
		for _, t := range previous {
			if t["ticket"] == id {
				present = true
				break
			}
		}
		if !present && len(previous) > 0 {
			newest := previous[len(previous)-1]
			marker, err := s.queue.Marker(text(newest, "ticket"))
			if err != nil {
				return failure(err.Error()), true
			}
			if marker["status"] == "stopped" {
				state["ticket"], state["status"], state["outcome"] = newest["ticket"], "done", "stopped"
				timings(state, marker)
				states = append(states, state)
				continue
			}
			if live && marker["status"] == "reviewed" && marker["open_findings"] == true {
				previousID := text(newest, "ticket")
				handoff := delivered[previousID]
				if !handoff {
					handoff, err = s.queue.Deliver(req.Block, file, previousID)
					if err != nil {
						return failure(err.Error()), true
					}
				}
				if handoff {
					delivered[previousID] = true
					state["ticket"], state["status"], state["outcome"] = previousID, "findings", outcome(marker)
					timings(state, marker)
					states = append(states, state)
					continue
				}
			}
		}
		if !present && live {
			if err := s.queue.Ensure(p); err != nil {
				return failure(err.Error()), true
			}
			present = true
		}
		if present {
			state["ticket"], state["status"] = id, "pending"
			marker, err := s.queue.Marker(id)
			if err != nil {
				return failure(err.Error()), true
			}
			if live && marker != nil {
				marker, err = s.queue.RetryOnce(s.repo, id, marker)
				if err != nil {
					return failure(err.Error()), true
				}
			}
			if marker != nil {
				state["status"], state["outcome"] = "done", outcome(marker)
				timings(state, marker)
			}
		}
		if state["status"] == "pending" || state["status"] == "not_queued" {
			done = false
		}
		if state["status"] == "not_queued" {
			notQueued = true
		}
		if state["outcome"] == "split_required" && split == "" {
			split = file
		}
		if state["outcome"] == "unavailable" && unavailable == "" {
			unavailable = file
		}
		states = append(states, state)
	}
	result := object{"block": req.Block, "files": states, "done": false}
	// Node waitBlock checks worker_down before completion and requires live
	// even for terminal or empty file sets. Preserve that shared bug for B182.
	if *down >= 2 {
		result["status"] = "worker_down"
		return result, true
	}
	if live && split != "" {
		result["reason"], result["next"] = "split_required", fmt.Sprintf("The owner raises review.budgets for %s, runs run reload, then review-file %s --fresh.", split, split)
		return result, true
	}
	if live && unavailable != "" {
		result["reason"], result["next"] = "review_unavailable", fmt.Sprintf("review-file %s --fresh re-runs it; if --fresh refuses, block reopen %s --run %s --file %s --reason <text>", unavailable, req.Block, s.run, unavailable)
		return result, true
	}
	if live && done {
		result["done"] = true
		return result, true
	}
	if live && (notQueued || !time.Now().Before(deadline)) {
		result["reason"] = "wait_timeout"
		if notQueued {
			result["reason"] = "not_queued"
		}
		return result, true
	}
	return nil, false
}

func (s *server) prune(block string) object {
	file := filepath.Join(s.home, ".code-forge", "runs", s.run+".key")
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		return failure("run-key")
	}
	key, err := os.ReadFile(file)
	if err != nil || len(key) != 32 {
		return failure("run-key")
	}
	closed := false
	for _, value := range s.rows {
		row := mapping(value)
		if row["run"] != s.run || row["block"] != block {
			continue
		}
		if row["event"] != "block.close" && row["event"] != "block.open" {
			continue
		}
		checked, err := sign.VerifyRow(row, key)
		if err != nil {
			return failure("signature")
		}
		if checked.OK {
			closed = row["event"] == "block.close" && row["status"] == "complete"
		}
	}
	if !closed {
		return failure("block-open")
	}
	tickets, markers, err := s.queue.Prune(s.run, block)
	if err != nil {
		return failure("prune-failed")
	}
	return object{"ok": true, "block": block, "tickets": tickets, "markers": markers}
}

func (s *server) reopened(block, file string) string {
	after := ""
	for _, value := range s.rows {
		row := mapping(value)
		if row["event"] != "review.reopened" || row["run"] != s.run || row["block"] != block || row["file"] != file {
			continue
		}
		if row["by"] != "human" && !(row["by"] == "agent" && mapping(row["verdict"])["verdict"] == "agree") && !(s.local && automaticReopen(row)) {
			continue
		}
		ts := text(row, "ts")
		// Node waitBlock validates Date.parse but selects row.ts lexically.
		// Keep its fractional-precision ordering bug so reopen ticket IDs agree.
		if _, err := time.Parse(time.RFC3339Nano, ts); err == nil && ts > after {
			after = ts
		}
	}
	return after
}

func (s *server) workerLive() bool {
	// Match Node: a live pid plus a fresh heartbeat, unknown start time,
	// or matching process start time. Only the last fallback launches ps.
	w, err := readObject(filepath.Join(s.queue.Dir, "worker.json"))
	if err != nil || w["run"] != s.run {
		return false
	}
	pid, ok := w["pid"].(float64)
	if !ok || pid <= 1 || pid != float64(int(pid)) {
		return false
	}
	err = syscall.Kill(int(pid), 0)
	if err != nil && !errors.Is(err, syscall.EPERM) {
		return false
	}
	beat, err := time.Parse(time.RFC3339Nano, text(w, "heartbeat_at"))
	age := time.Since(beat)
	if err == nil && age <= time.Minute && age >= -5*time.Second {
		return true
	}
	if w["start_time"] == "unknown" {
		return true
	}
	ps := "/bin/ps"
	if _, err := os.Stat(ps); err != nil {
		ps = "ps"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, ps, "-o", "lstart=", "-p", strconv.Itoa(int(pid)))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	if cmd.Process != nil {
		s.psProcesses++
	}
	start := strings.Join(strings.Fields(string(out)), " ")
	return err == nil && start != "" && start == text(w, "start_time")
}

func outcome(marker object) any {
	switch marker["status"] {
	case "reviewed":
		if marker["open_findings"] == true || marker["approved"] == false {
			return "findings"
		}
		return "approved"
	case "stopped", "split_required":
		return marker["status"]
	default:
		return "unavailable"
	}
}

func timings(state, marker object) {
	for _, key := range []string{"queued_at", "started_at", "finished_at", "duration_ms"} {
		if v, ok := marker[key]; ok {
			state[key] = v
		}
	}
}

func (s *server) changed(tree, base string, owned []any) ([]string, error) {
	if !regexp.MustCompile(`^[0-9a-f]{7,64}$`).MatchString(base) {
		return nil, fmt.Errorf("bad-ref")
	}
	state := s.trees[tree]
	if state == nil {
		state = &treeState{hashes: make(map[string]fileHash)}
		s.trees[tree] = state
	}
	stamp, err := treeStamp(tree, owned)
	if err != nil {
		return nil, err
	}
	if stamp == state.stamp && base == state.base {
		return state.files, nil
	}
	diff, err := git(tree, "diff", "--name-only", "-z", "--no-renames", base, "--")
	if err != nil {
		return nil, err
	}
	untracked, err := git(tree, "ls-files", "-z", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool)
	for _, file := range strings.Split(diff+untracked, "\x00") {
		if file != "" {
			set[file] = true
		}
	}
	files := []string{}
	for file := range set {
		files = append(files, file)
	}
	sort.Strings(files)
	state.stamp, state.base, state.files = stamp, base, files
	return files, nil
}

func treeStamp(tree string, owned []any) (string, error) {
	paths := []string{filepath.Join(tree, ".gitignore")}
	for _, value := range owned {
		pattern, ok := value.(string)
		if !ok {
			continue
		}
		prefix := []string{tree}
		for _, part := range strings.Split(pattern, "/") {
			if strings.ContainsAny(part, "*?{") {
				break
			}
			prefix = append(prefix, part)
		}
		// Node waitTreeStamp also joins unchecked prefixes from the scope mirror.
		// Traversal is a shared bug; assertOwned rejects it at normal block open.
		paths = append(paths, filepath.Join(prefix...))
	}
	gitDir, common, err := gitDirs(tree)
	if err != nil {
		return "", err
	}
	for _, dir := range []string{gitDir, common} {
		for _, name := range []string{"HEAD", "index", "packed-refs", "refs", "info/exclude"} {
			paths = append(paths, filepath.Join(dir, name))
		}
	}
	entries := make(map[string]string)
	for _, root := range paths {
		err := filepath.WalkDir(root, func(file string, entry fs.DirEntry, err error) error {
			if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
				entries[file] = "missing"
				return nil
			}
			if err != nil {
				return err
			}
			if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == ".code-forge") {
				return filepath.SkipDir
			}
			entries[file] = fileStamp(file)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	keys := make([]string, 0, len(entries))
	for file := range entries {
		keys = append(keys, file)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, file := range keys {
		fmt.Fprintln(hash, file, entries[file])
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func gitDirs(tree string) (string, string, error) {
	dir := filepath.Join(tree, ".git")
	info, err := os.Lstat(dir)
	if err != nil {
		return "", "", err
	}
	if info.Mode().IsRegular() {
		data, err := os.ReadFile(dir)
		if err != nil {
			return "", "", err
		}
		dir = strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir: ")
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(tree, dir)
		}
	}
	common := dir
	if data, err := os.ReadFile(filepath.Join(dir, "commondir")); err == nil {
		common = strings.TrimSpace(string(data))
		if !filepath.IsAbs(common) {
			common = filepath.Join(dir, common)
		}
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	return filepath.Clean(dir), filepath.Clean(common), nil
}

func (s *server) contentHash(tree, file string) (string, error) {
	full := filepath.Join(tree, file)
	stamp := fileStamp(full)
	state := s.trees[tree]
	if old, ok := state.hashes[file]; ok && old.stamp == stamp {
		return old.hash, nil
	}
	info, err := os.Lstat(full)
	if os.IsNotExist(err) {
		state.hashes[file] = fileHash{stamp, "deleted"}
		return "deleted", nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("bad-path")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil || parent != tree && !strings.HasPrefix(parent, tree+string(os.PathSeparator)) {
		return "", fmt.Errorf("bad-path")
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	value := hex.EncodeToString(hash[:])
	state.hashes[file] = fileHash{stamp, value}
	return value, nil
}

func git(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "GIT_") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0")
	data, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git-failed")
	}
	return string(data), nil
}

func readObject(file string) (object, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var obj object
	err = json.Unmarshal(data, &obj)
	if err == nil && obj == nil {
		err = fmt.Errorf("expected object")
	}
	return obj, err
}
func mapping(value any) object           { obj, _ := value.(map[string]any); return obj }
func text(obj object, key string) string { s, _ := obj[key].(string); return s }
func optional(obj object, key string) *string {
	s, ok := obj[key].(string)
	if !ok {
		return nil
	}
	return &s
}
func later(a, b string) bool {
	x, e1 := time.Parse(time.RFC3339Nano, a)
	y, e2 := time.Parse(time.RFC3339Nano, b)
	return e1 == nil && e2 == nil && x.After(y)
}
func fileStamp(file string) string {
	info, err := os.Lstat(file)
	if err != nil {
		return "missing"
	}
	identity, _ := info.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%d:%d:%d:%d:%d", info.Size(), info.ModTime().UnixNano(), info.Mode(), identity.Dev, identity.Ino)
}

func config(record object) (object, error) {
	state := mapping(record["config"])
	hash := text(state, "hash")
	if hash == "" {
		return nil, nil
	}
	cfg := mapping(mapping(state["snapshots"])[hash])
	data, err := canon.Marshal(cfg)
	if err != nil || cfg == nil {
		return nil, fmt.Errorf("config-snapshot")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != hash {
		return nil, fmt.Errorf("config-snapshot")
	}
	return cfg, nil
}

func owns(owned []any, kinds object, file string) bool {
	for _, value := range owned {
		entry, ok := value.(string)
		if !ok || entry == "" {
			continue
		}
		if strings.ContainsAny(entry, "*?{") {
			// Node ownsFile trims trailing slashes only from exact entries,
			// leaving glob spelling intact. Preserve the same matching here.
			if glob(file, entry) {
				return true
			}
			continue
		}
		root := strings.TrimSuffix(strings.TrimPrefix(entry, "./"), "/")
		if root == file || kinds[entry] != "file" && strings.HasPrefix(file, root+"/") {
			return true
		}
	}
	return false
}

func excluded(cfg object, file string) *string {
	rules, ok := mapping(cfg["review"])["exclude"].([]any)
	if !ok {
		for _, name := range []string{"package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml", "bun.lock", "bun.lockb", "composer.lock", "Cargo.lock", "Gemfile.lock", "poetry.lock", "uv.lock", "go.sum", "Package.resolved", "Podfile.lock", "pubspec.lock", "mix.lock", "flake.lock"} {
			rules = append(rules, "**/"+name)
		}
	}
	for _, value := range rules {
		if rule, ok := value.(string); ok && glob(file, rule) {
			return &rule
		}
	}
	return nil
}

// glob implements Node's segment matcher, including zero-segment ** and nested braces.
func glob(file, pattern string) bool {
	if open := strings.IndexByte(pattern, '{'); open >= 0 {
		depth, start := 0, open+1
		alternatives := []string{}
		for i := open; i < len(pattern); i++ {
			switch pattern[i] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					alternatives = append(alternatives, pattern[start:i])
					for _, part := range alternatives {
						if glob(file, pattern[:open]+part+pattern[i+1:]) {
							return true
						}
					}
					return false
				}
			case ',':
				if depth == 1 {
					alternatives = append(alternatives, pattern[start:i])
					start = i + 1
				}
			}
		}
	}
	f, p := strings.Split(file, "/"), strings.Split(pattern, "/")
	var match func(int, int) bool
	match = func(i, j int) bool {
		if j == len(p) {
			return i == len(f)
		}
		if p[j] == "**" {
			for k := i; k <= len(f); k++ {
				if match(k, j+1) {
					return true
				}
			}
			return false
		}
		if i == len(f) {
			return false
		}
		body := ""
		for _, c := range p[j] {
			if c == '*' {
				body += "[^/]*"
			} else if c == '?' {
				body += "[^/]"
			} else {
				body += regexp.QuoteMeta(string(c))
			}
		}
		return regexp.MustCompile("^"+body+"$").MatchString(f[i]) && match(i+1, j+1)
	}
	return match(0, 0)
}
