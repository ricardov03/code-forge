// Package worker is the Go review worker (Node: src/worker/loop.mjs and the
// worker half of src/worker/queue.mjs). B485 step a: the announcement, the
// private registry, the heartbeat, the boot lock and the clean stop. The CLI
// still hands `worker --run` over to Node until the ticket glue is ported.
package worker

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/ledger"
)

// WorkerFile is the legacy announcement in the queue directory.
const WorkerFile = "worker.json"

// HeartbeatMs is how often a serving worker refreshes heartbeat_at (B30).
const HeartbeatMs = 5000

// MinHeartbeatMs is the shortest heartbeat interval a worker accepts.
const MinHeartbeatMs = 100

// HeartbeatStaleMs: a heartbeat older than this is not fresh.
const HeartbeatStaleMs = 60_000

// HeartbeatFutureMs: a heartbeat further ahead than this is not fresh.
const HeartbeatFutureMs = 5000

// UnknownStartTime is reaper.mjs UNKNOWN_START_TIME.
const UnknownStartTime = "unknown"

// ErrUndecided marks an input Node may read differently than Go (a date
// string Date.parse might accept, bytes JSON.parse decodes with
// replacements). The caller hands the whole invocation over to Node.
var ErrUndecided = errors.New("worker: input needs Node")

// Probes are the process seams: liveness, ps start time and the clock.
type Probes struct {
	Alive     func(pid int) bool
	StartTime func(pid int) *string
	Now       func() time.Time
}

func (p Probes) alive(pid int) bool {
	if p.Alive != nil {
		return p.Alive(pid)
	}
	return IsAlive(pid)
}

func (p Probes) startTime(pid int) *string {
	if p.StartTime != nil {
		return p.StartTime(pid)
	}
	return filelock.ProcessStartTime(pid)
}

func (p Probes) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// IsAlive is reaper.mjs isAlive: an integer pid > 1 that kill(0) reaches,
// EPERM counting as alive. Out-of-range pids make Node's kill throw: not alive.
func IsAlive(pid int) bool {
	if pid <= 1 || pid > math.MaxInt32 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// ISO is Date.prototype.toISOString.
func ISO(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// QueueDir is <repo>/.code-forge/queue.
func QueueDir(root string) string { return filepath.Join(root, ".code-forge", "queue") }

// WorkersDir is <repo>/.code-forge/workers.
func WorkersDir(root string) string { return filepath.Join(root, ".code-forge", "workers") }

func jsNumber(n float64) string { return strconv.FormatFloat(n, 'f', -1, 64) }

func entryPath(root string, pid float64, start string) string {
	return filepath.Join(WorkersDir(root), jsNumber(pid)+"-"+start+".json")
}

// writeAtomic is queue.mjs writeAtomic: <file>.<pid>.<8 hex>.tmp then rename.
func writeAtomic(file string, data []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o777); err != nil {
		return err
	}
	tmp := tmpName(file)
	if err := writeNew(tmp, data, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func tmpName(file string) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return file + "." + strconv.Itoa(os.Getpid()) + "." + hex.EncodeToString(b[:]) + ".tmp"
}

// writeNew is writeFileSync(tmp, text, {mode}): the mode applies (under the
// umask) only when the file is created.
func writeNew(file string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func line(o ledger.Object) ([]byte, error) {
	data, err := ledger.Stringify(o, false)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

var loneSurrogate = regexp.MustCompile(`\\u[dD][89a-fA-F][0-9a-fA-F]{2}`)

// readJSON is queue.mjs readJSON for an object: nil when missing or not a
// JSON object. Bytes Node would decode with replacements are undecided.
func readJSON(file string) (ledger.Object, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, nil
	}
	if !utf8.Valid(data) || loneSurrogate.Match(data) || bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		return nil, ErrUndecided
	}
	obj, err := ledger.DecodeObject(data)
	if err != nil {
		return nil, nil
	}
	return obj, nil
}

// dateOK is Number.isFinite(Date.parse(s)) for the RFC 3339 strings Go reads;
// any other string is undecided.
func dateOK(v any) (time.Time, bool, error) {
	s, ok := v.(string)
	if !ok {
		return time.Time{}, false, nil
	}
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false, ErrUndecided
	}
	return at, true, nil
}

func intPid(v any) (float64, bool) {
	n, ok := v.(float64)
	return n, ok && n > 1 && math.Trunc(n) == n && !math.IsInf(n, 0)
}

func pidInt(n float64) int {
	if n > math.MaxInt32 {
		return math.MaxInt32 + 1
	}
	return int(n)
}

// HeartbeatFresh is queue.mjs heartbeatFresh.
func HeartbeatFresh(w ledger.Object, now time.Time) (bool, error) {
	at, ok, err := dateOK(w.Get("heartbeat_at"))
	if err != nil || !ok {
		return false, err
	}
	age := now.Sub(at).Milliseconds()
	return age <= HeartbeatStaleMs && age >= -HeartbeatFutureMs, nil
}

// shape is queue.mjs workerShape.
func shape(w ledger.Object) (bool, error) {
	if w == nil {
		return false, nil
	}
	if _, ok := intPid(w.Get("pid")); !ok {
		return false, nil
	}
	start, _ := w.Get("start_time").(string)
	run, _ := w.Get("run").(string)
	if start == "" || strings.ContainsAny(start, `\/`) || run == "" {
		return false, nil
	}
	for _, key := range []string{"started_at", "heartbeat_at"} {
		if _, ok, err := dateOK(w.Get(key)); err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func (p Probes) live(w ledger.Object) (bool, error) {
	n, _ := w.Get("pid").(float64)
	pid := pidInt(n)
	if !p.alive(pid) {
		return false, nil
	}
	fresh, err := HeartbeatFresh(w, p.now())
	if err != nil || fresh {
		return fresh, err
	}
	start, _ := w.Get("start_time").(string)
	if start == UnknownStartTime {
		return true, nil
	}
	actual := p.startTime(pid)
	return actual != nil && *actual == start, nil
}

func (p Probes) liveAtBoot(w ledger.Object) (bool, error) {
	n, _ := w.Get("pid").(float64)
	pid := pidInt(n)
	if !p.alive(pid) {
		return false, nil
	}
	start, _ := w.Get("start_time").(string)
	actual := p.startTime(pid)
	if actual != nil && start != UnknownStartTime {
		return *actual == start, nil
	}
	fresh, err := HeartbeatFresh(w, p.now())
	return fresh || start == UnknownStartTime, err
}

// Entry is one registry file: Worker is nil for an unreadable or bad entry,
// which stays occupied (Live) until a human fixes it.
type Entry struct {
	File   string
	Worker ledger.Object
	Live   bool
}

// Registry is queue.mjs workerRegistry. prune removes entries proven stale by
// the boot-time liveness rule.
func Registry(root string, prune bool, p Probes) ([]Entry, error) {
	list, err := os.ReadDir(WorkersDir(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(list))
	for _, e := range list {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	var entries []Entry
	for _, name := range names {
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		file := filepath.Join(WorkersDir(root), name)
		w, err := readJSON(file)
		if err != nil {
			return nil, err
		}
		ok, err := shape(w)
		if err != nil {
			return nil, err
		}
		if ok {
			pid, _ := w.Get("pid").(float64)
			start, _ := w.Get("start_time").(string)
			if name != jsNumber(pid)+"-"+start+".json" {
				ok = false
			}
		}
		if !ok {
			entries = append(entries, Entry{File: file, Live: true})
			continue
		}
		var live bool
		if prune {
			live, err = p.liveAtBoot(w)
		} else {
			live, err = p.live(w)
		}
		if err != nil {
			return nil, err
		}
		if !live && prune {
			_ = os.Remove(file)
			continue
		}
		entries = append(entries, Entry{File: file, Worker: w, Live: live})
	}
	return entries, nil
}

// ReadLegacy is queue.mjs readWorker.
func ReadLegacy(root string) (ledger.Object, error) {
	w, err := readJSON(filepath.Join(QueueDir(root), WorkerFile))
	if err != nil || w == nil {
		return nil, err
	}
	if _, ok := intPid(w.Get("pid")); !ok {
		return nil, nil
	}
	if _, ok := w.Get("run").(string); !ok {
		return nil, nil
	}
	return w, nil
}

// LiveOpts are liveWorker's options; Pid 0 means any pid.
type LiveOpts struct {
	Pid            int
	RegisteredOnly bool
}

// Live is queue.mjs liveWorker: the first live registry entry, then the
// legacy announcement. nil means worker_down.
func Live(root string, opts LiveOpts, p Probes) (ledger.Object, error) {
	entries, err := Registry(root, false, p)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.Live || e.Worker == nil {
			continue
		}
		pid, _ := e.Worker.Get("pid").(float64)
		if opts.Pid == 0 || pid == float64(opts.Pid) {
			return e.Worker, nil
		}
	}
	if opts.RegisteredOnly {
		return nil, nil
	}
	w, err := ReadLegacy(root)
	if err != nil || w == nil {
		return nil, err
	}
	pid, _ := w.Get("pid").(float64)
	if opts.Pid != 0 && pid != float64(opts.Pid) {
		return nil, nil
	}
	live, err := p.live(w)
	if err != nil || !live {
		return nil, err
	}
	return w, nil
}

// Announce is queue.mjs announceWorker: the private registry entry (0600 in a
// 0700 directory) and, when no live worker owns it, the legacy worker.json.
func Announce(root string, pid int, run string, p Probes) (ledger.Object, error) {
	now := ISO(p.now())
	start := UnknownStartTime
	if s := p.startTime(pid); s != nil {
		start = *s
	}
	body := ledger.Object{{Key: "pid", Value: float64(pid)}, {Key: "start_time", Value: start}, {Key: "run", Value: run}, {Key: "started_at", Value: now}, {Key: "heartbeat_at", Value: now}}
	data, err := line(body)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(WorkersDir(root), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(WorkersDir(root), 0o700); err != nil {
		return nil, err
	}
	if err := writeAtomic(entryPath(root, float64(pid), start), data, 0o600); err != nil {
		return nil, err
	}
	legacy, err := ReadLegacy(root)
	if err != nil {
		return nil, err
	}
	owned := false
	if legacy != nil {
		if owned, err = p.live(legacy); err != nil {
			return nil, err
		}
	}
	if !owned {
		if err := writeAtomic(filepath.Join(QueueDir(root), WorkerFile), data, 0o644); err != nil {
			return nil, err
		}
	}
	return body, nil
}

func mine(w, self ledger.Object) bool {
	if w == nil {
		return false
	}
	pid, ok := w.Get("pid").(float64)
	start, isString := w.Get("start_time").(string)
	return ok && isString && pid == self.Get("pid") && start == self.Get("start_time")
}

// Beat is queue.mjs beatWorker: refresh heartbeat_at in both announcements
// while each still names this worker's pid and start time; another owner's
// file is never replaced. Reports whether a beat was written.
func Beat(root string, self ledger.Object, now time.Time) (bool, error) {
	pid, _ := self.Get("pid").(float64)
	start, _ := self.Get("start_time").(string)
	registryFile := entryPath(root, pid, start)
	written := false
	for _, file := range []string{registryFile, filepath.Join(QueueDir(root), WorkerFile)} {
		w, err := readJSON(file)
		if err != nil {
			return written, err
		}
		if !mine(w, self) {
			continue
		}
		next := w.Clone()
		next.Set("heartbeat_at", ISO(now))
		data, err := line(next)
		if err != nil {
			return written, err
		}
		mode := os.FileMode(0o644)
		if file == registryFile {
			mode = 0o600
		}
		tmp := tmpName(file)
		err = func() error {
			defer os.Remove(tmp)
			if err := writeNew(tmp, data, mode); err != nil {
				return err
			}
			again, err := readJSON(file)
			if err != nil {
				return err
			}
			if !mine(again, self) {
				return nil
			}
			if err := os.Rename(tmp, file); err != nil {
				return err
			}
			written = true
			return nil
		}()
		if err != nil {
			return written, err
		}
	}
	return written, nil
}

// Retract is queue.mjs retractWorker: remove only this owner's
// announcements. A nil startTime matches any start time.
func Retract(root string, pid int, startTime *string, p Probes) error {
	match := func(w ledger.Object) bool {
		if w == nil {
			return false
		}
		n, _ := w.Get("pid").(float64)
		if n != float64(pid) {
			return false
		}
		return startTime == nil || w.Get("start_time") == *startTime
	}
	entries, err := Registry(root, false, p)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if match(e.Worker) {
			_ = os.Remove(e.File)
		}
	}
	legacy, err := ReadLegacy(root)
	if err != nil {
		return err
	}
	if match(legacy) {
		_ = os.Remove(filepath.Join(QueueDir(root), WorkerFile))
	}
	return nil
}
