// Package healq is the heal queue (B429): a verb that fails for a reason that
// passes by itself (a held lock, a transient git error) adds one item with its
// own argv instead of waiting for a human to re-run it, and `heal run` retries
// the items with backoff until they pass or need a coder.
//
// The queue of a run is one append-only JSONL file,
// <home>/.code-forge/heal/<run>.jsonl. Every line is a full item snapshot; the
// last line of an id wins. Appends take a short file lock; readers take none
// and skip a torn last line.
package healq

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/filelock"
)

// Item kinds and statuses.
const (
	KindLock   = "lock"
	KindGate   = "gate"
	KindLand   = "land"
	KindCI     = "ci"
	KindWorker = "worker"

	StatusOpen       = "open"
	StatusRetrying   = "retrying"
	StatusHealed     = "healed"
	StatusNeedsCoder = "needs_coder"
)

// RetryEnv is set in the environment of every retry child, so a retry that
// fails again updates its own item instead of queueing a new one.
const RetryEnv = "CODE_FORGE_HEAL_RETRY"

// Item is one queued problem. Times are RFC 3339 UTC.
type Item struct {
	ID        string   `json:"id"`
	Run       string   `json:"run"`
	Block     string   `json:"block"`
	Kind      string   `json:"kind"`
	Argv      []string `json:"argv"`
	Cwd       string   `json:"cwd"`
	Detail    string   `json:"detail"`
	Tries     int      `json:"tries"`
	Status    string   `json:"status"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
	NextAt    string   `json:"next_at,omitempty"`
}

var runPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidRun reports whether run is a run id (the same rule as the run records).
func ValidRun(run string) bool { return runPattern.MatchString(run) }

// Dir is the heal queue directory under home.
func Dir(home string) string { return filepath.Join(home, ".code-forge", "heal") }

// File is the queue file of one run.
func File(home, run string) (string, error) {
	if !ValidRun(run) {
		return "", fmt.Errorf("run id must match /^[a-z0-9][a-z0-9-]{0,63}$/")
	}
	return filepath.Join(Dir(home), run+".jsonl"), nil
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func newID(now time.Time) string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "h" + now.UTC().Format("20060102t150405") + "-" + hex.EncodeToString(b)
}

// Append writes one item snapshot under the short append lock of its run.
func Append(home string, item Item) error {
	file, err := File(home, item.Run)
	if err != nil {
		return err
	}
	line, err := json.Marshal(item)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		return err
	}
	return filelock.WithLock("heal-append-"+item.Run, func() error {
		f, err := os.OpenFile(file, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0600)
		if err != nil {
			return err
		}
		_, werr := f.Write(line)
		cerr := f.Close()
		if werr != nil {
			return werr
		}
		return cerr
	}, filelock.Options{})
}

// Enqueue adds a new open item and returns it.
func Enqueue(home, run, block, kind string, argv []string, cwd, detail string, now time.Time) (Item, error) {
	item := Item{
		ID: newID(now), Run: run, Block: block, Kind: kind, Argv: append([]string{}, argv...),
		Cwd: cwd, Detail: Trim(detail), Status: StatusOpen, CreatedAt: stamp(now), UpdatedAt: stamp(now),
	}
	return item, Append(home, item)
}

// Load folds the queue of one run: the last snapshot of each id, in the order
// the ids first appeared. A missing file is an empty queue.
func Load(home, run string) ([]Item, error) {
	file, err := File(home, run)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	order := []string{}
	byID := map[string]Item{}
	scan := bufio.NewScanner(bytes.NewReader(data))
	scan.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scan.Scan() {
		var item Item
		if json.Unmarshal(scan.Bytes(), &item) != nil || item.ID == "" {
			continue // a torn last line from a crashed writer
		}
		if _, seen := byID[item.ID]; !seen {
			order = append(order, item.ID)
		}
		byID[item.ID] = item
	}
	items := make([]Item, 0, len(order))
	for _, id := range order {
		items = append(items, byID[id])
	}
	return items, nil
}

// Runs lists the runs that have a heal queue, sorted.
func Runs(home string) ([]string, error) {
	entries, err := os.ReadDir(Dir(home))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	runs := []string{}
	for _, e := range entries {
		name := e.Name()
		if run, ok := strings.CutSuffix(name, ".jsonl"); ok && ValidRun(run) && e.Type().IsRegular() {
			runs = append(runs, run)
		}
	}
	sort.Strings(runs)
	return runs, nil
}

// Trim keeps the last 4 KiB of a failure text, at a line start when it can.
func Trim(text string) string {
	text = strings.TrimSpace(text)
	const max = 4096
	if len(text) <= max {
		return text
	}
	text = text[len(text)-max:]
	if i := strings.IndexByte(text, '\n'); i >= 0 && i < len(text)-1 {
		text = text[i+1:]
	}
	return text
}

var (
	lockText      = regexp.MustCompile(`(?i)is locked \(|lock timeout|timed out waiting for (?:the )?lock|index\.lock|cannot lock ref|unable to create '[^']*\.lock'`)
	transientText = regexp.MustCompile(`(?i)could not resolve host|connection (?:reset|refused|timed out)|the remote end hung up|early eof|ssh: connect to host|rpc failed|operation timed out`)
)

// Classify says whether a failure text is one that passes by itself, and the
// item kind: lock for a held lock, else gate (gates run) or land (block verbs)
// for a transient git error. Anything else is not retryable.
func Classify(argv []string, text string) (string, bool) {
	if lockText.MatchString(text) {
		return KindLock, true
	}
	if transientText.MatchString(text) {
		if len(argv) > 0 && argv[0] == "gates" {
			return KindGate, true
		}
		return KindLand, true
	}
	return "", false
}
