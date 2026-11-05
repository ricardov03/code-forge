// Package filelock shares Node's exclusive lock files and FIFO keyed mutexes.
package filelock

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"

	"github.com/ricardov03/code-forge/native/ledger"
)

// Options provides Node's stale-owner probes. A nil Probe result means unknown.
type Options struct {
	Probe     func(int) *string
	Alive     func(int) bool
	AfterRead func() error
	Poll      time.Duration
}

type Result struct {
	OK      bool
	Release func()
}

var self struct {
	sync.Once
	start *string
}
var keyed = struct {
	sync.Mutex
	tails map[string]chan struct{}
}{tails: map[string]chan struct{}{}}

// KeyedLock serializes same-key calls in arrival order and releases on panic.
func KeyedLock(key string, fn func() error) error {
	done := make(chan struct{})
	keyed.Lock()
	prev := keyed.tails[key]
	keyed.tails[key] = done
	keyed.Unlock()
	defer func() {
		close(done)
		keyed.Lock()
		if keyed.tails[key] == done {
			delete(keyed.tails, key)
		}
		keyed.Unlock()
	}()
	if prev != nil {
		<-prev
	}
	return fn()
}

// ProcessStartTime mirrors ps -o lstart= with C locale and normalized spaces.
func ProcessStartTime(pid int) *string {
	if pid <= 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ps := "/bin/ps"
	if _, err := os.Stat(ps); err != nil {
		ps = "ps"
	}
	cmd := exec.CommandContext(ctx, ps, "-o", "lstart=", "-p", strconv.Itoa(pid))
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "LC_ALL=") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	start := strings.Join(strings.Fields(string(out)), " ")
	if start == "" {
		return nil
	}
	return &start
}

func alive(pid int) bool {
	// Node kill rejects out-of-range integers; alive treats every non-ESRCH
	// failure as unknown/live. Never truncate a JSON pid into a process group.
	return pid > math.MaxInt32 || syscall.Kill(pid, 0) != syscall.ESRCH
}
func dir() (string, error) {
	home, set := os.LookupEnv("HOME")
	if !set {
		current, err := user.Current()
		if err != nil {
			return "", err
		}
		home = current.HomeDir
	}
	return filepath.Join(home, ".code-forge", "locks"), nil
}
func Path(key string) (string, error) {
	d, err := dir()
	return filepath.Join(d, fmt.Sprintf("%x.lock", sha256.Sum256([]byte(key)))), err
}

func ownerOf(data []byte) (key string, pid int, start string, ok bool) {
	var owner map[string]any
	if json.Unmarshal(data, &owner) != nil {
		return
	}
	key, k := owner["key"].(string)
	start, s := owner["start"].(string)
	p, n := owner["pid"].(float64)
	if !k || !s || !n || p <= 1 || p != math.Trunc(p) {
		return "", 0, "", false
	}
	if p > math.MaxInt32 {
		return key, math.MaxInt32 + 1, start, true
	}
	return key, int(p), start, true
}

func create(file string, data []byte) (Result, error) {
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if os.IsExist(err) {
		return Result{}, nil
	}
	if err != nil {
		return Result{}, err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return Result{}, err
	}
	if closeErr != nil {
		return Result{}, closeErr
	}
	var once sync.Once
	return Result{true, func() {
		once.Do(func() {
			if current, err := os.ReadFile(file); err == nil && bytes.Equal(current, data) {
				_ = os.Remove(file)
			}
		})
	}}, nil
}

// TryLock fails closed except for one guarded break of an unchanged dead owner.
func TryLock(key string, opts Options) (result Result, err error) {
	defer func() { err = nodeError(err, "", "") }()
	self.Do(func() { self.start = ProcessStartTime(os.Getpid()) })
	if self.start == nil {
		return Result{}, fmt.Errorf("cannot determine lock owner process start time")
	}
	file, err := Path(key)
	if err != nil {
		return Result{}, err
	}
	data, err := ledger.Stringify(ledger.Object{{Key: "key", Value: key}, {Key: "pid", Value: os.Getpid()}, {Key: "start", Value: *self.start}}, false)
	if err != nil {
		return Result{}, err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		// mkdir recursive reports EEXIST for a file at the final directory,
		// whereas Go's MkdirAll reports ENOTDIR for that same obstruction.
		if st, statErr := os.Stat(filepath.Dir(file)); statErr == nil && !st.IsDir() {
			err = &os.PathError{Op: "mkdir", Path: filepath.Dir(file), Err: syscall.EEXIST}
		}
		return Result{}, nodeError(err, "mkdir", filepath.Dir(file))
	}
	initial, err := create(file, data)
	if err != nil || initial.OK {
		return initial, err
	}
	seen, err := os.ReadFile(file)
	if err != nil {
		return Result{}, nil
	}
	if opts.AfterRead != nil {
		if err := opts.AfterRead(); err != nil {
			return Result{}, err
		}
	}
	ownerKey, pid, start, ok := ownerOf(seen)
	if !ok || ownerKey != key {
		return Result{}, nil
	}
	isAlive := opts.Alive
	if isAlive == nil {
		isAlive = alive
	}
	probe := opts.Probe
	if probe == nil {
		probe = ProcessStartTime
	}
	dead := !isAlive(pid)
	if !dead {
		actual := probe(pid)
		dead = actual != nil && *actual != start
	}
	if !dead {
		return Result{}, nil
	}
	mutex := file + ".break"
	guard, err := os.OpenFile(mutex, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return Result{}, nil
	}
	defer func() { _ = guard.Close(); _ = os.Remove(mutex) }()
	if _, err := guard.Write(data); err != nil {
		return Result{}, err
	}
	current, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(current, seen) {
		return Result{}, nil
	}
	if err := os.Remove(file); err != nil {
		return Result{}, err
	}
	return create(file, data)
}

func WithLock(key string, fn func() error, opts Options) error {
	return KeyedLock(key, func() error {
		for {
			lock, err := TryLock(key, opts)
			if err != nil {
				return err
			}
			if lock.OK {
				defer lock.Release()
				return fn()
			}
			poll := opts.Poll
			if poll <= 0 {
				poll = 25 * time.Millisecond
			}
			time.Sleep(poll)
		}
	})
}

// ListBadLocks includes malformed/unreadable locks and dead-owner break mutexes.
func ListBadLocks() ([]string, error) {
	d, err := dir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(d)
	out := []string{}
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, nodeError(err, "scandir", d)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".lock") && !strings.HasSuffix(name, ".lock.break") {
			continue
		}
		file := filepath.Join(d, name)
		data, err := os.ReadFile(file)
		_, pid, _, ok := ownerOf(data)
		if err != nil || !ok || strings.HasSuffix(name, ".lock.break") && !alive(pid) {
			out = append(out, file)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := utf16.Encode([]rune(out[i])), utf16.Encode([]rune(out[j]))
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) < len(b)
	})
	return out, nil
}
