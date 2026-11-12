package worker

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
)

// B534: testdata/b534_node_watch.golden is what Node's watchTick
// (src/worker/watch.mjs, default exec, real queue writes) produced for the
// scenario below: the same repository (fixed author and dates, so the same
// commit ids), the same run record and the same now() per tick. After each
// tick it lists the rows writeRow got and every file under the queue
// directory; the repository path and the ticket times (enqueueHash stamps
// them with new Date()) are normalized.

type watchFix struct {
	*b488Fix
	n    int
	rows []string
}

func newWatchFix(t *testing.T) *watchFix {
	t.Helper()
	fx := &watchFix{b488Fix: newB488Fix(t)}
	if err := os.Remove(filepath.Join(fx.root, "a.txt")); err != nil {
		t.Fatal(err)
	}
	fx.git(t, nil, "init", "-q")
	writeJSON(t, filepath.Join(fx.root, ".git", "info", "exclude"), ".code-forge/\n")
	return fx
}

func (fx *watchFix) git(t *testing.T, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = fx.root
	cmd.Env = append(os.Environ(), append([]string{"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@x", "GIT_COMMITTER_NAME=T",
		"GIT_COMMITTER_EMAIL=t@x", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}, env...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files and commits them at a fixed date (the golden script's
// commit ids).
func (fx *watchFix) commit(t *testing.T, files map[string]string) string {
	t.Helper()
	for name, body := range files {
		writeJSON(t, filepath.Join(fx.root, name), body)
	}
	fx.n++
	date := fmt.Sprintf("2026-11-01T00:00:%02d +0000", fx.n)
	fx.git(t, nil, "add", "-A")
	fx.git(t, []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}, "commit", "-q", "-m", fmt.Sprintf("c%d", fx.n))
	return fx.git(t, nil, "rev-parse", "HEAD")
}

// watch is the worker's Watch for this fixture with the debounce clock now.
func (fx *watchFix) watch(now *int64) *Watch {
	clock := fixedNow
	runner := fx.runner(&clock)
	runner.Cfg = map[string]any{"review": map[string]any{"file_loop": "watch"}}
	runner.WriteRow = func(row ledger.Object) error {
		plain := row.Clone()
		for _, k := range []string{"run", "ts", "mac"} {
			plain.Delete(k)
		}
		data, err := ledger.Stringify(plain, false)
		fx.rows = append(fx.rows, string(data))
		return err
	}
	return &Watch{Runner: runner, Now: func() int64 { return *now }}
}

// dump is the golden script's listing after one tick.
func (fx *watchFix) dump(t *testing.T, now int64) string {
	t.Helper()
	var out strings.Builder
	out.WriteString("== tick " + strconv.FormatInt(now, 10) + "\n")
	for _, r := range fx.rows {
		out.WriteString("row " + r + "\n")
	}
	fx.rows = nil
	q := QueueDir(fx.root)
	var files []string
	filepath.WalkDir(q, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(q, p)
			files = append(files, rel)
		}
		return nil
	})
	sort.Strings(files)
	for _, f := range files {
		out.WriteString("file " + f + " " + strings.TrimRight(readFile(t, filepath.Join(q, f)), "\n") + "\n")
	}
	return out.String()
}

var watchStamp = regexp.MustCompile(`"(enqueued_at|queued_at)":"[^"]*"`)

type watchStep struct {
	now   int64
	files map[string]string
}

func watchScenario() []watchStep {
	steps := []watchStep{
		{1000, nil},
		{2000, map[string]string{"src/a.go": "package a\n// 1\n", "src/b.go": "package b\n", "README.md": "hi 2\n"}},
		{16999, nil}, // 14999 ms after the commit: still quiet
		{17000, nil}, // 15000 ms: queued
		{20000, map[string]string{"src/c.go": "c2\n"}},
		{30000, map[string]string{"src/c.go": "c3\n"}}, // a new HEAD restarts the quiet wait
		{44999, nil},
		{45000, nil}, // one ticket, for the last HEAD's content
	}
	for i := 0; i <= 12; i++ { // a commit every 10 s: the 120 s burst limit queues at 170000
		steps = append(steps, watchStep{50000 + int64(i)*10000, map[string]string{"src/d.go": "d" + strconv.Itoa(i) + "\n"}})
	}
	return append(steps,
		watchStep{200000, map[string]string{"src/a.go": "package a\n// 2\n"}},
		watchStep{215000, nil}, // a.go is held by its pending ticket, which is superseded
		watchStep{216000, nil})
}

func (fx *watchFix) start(t *testing.T) string {
	t.Helper()
	base := fx.commit(t, map[string]string{"src/a.go": "package a\n", "README.md": "hi\n"})
	fx.record(t, `{"run_id":"r1","project":"unit","workspace":"`+fx.root+`","status":"active","blocks":{"B1":{"status":"open","base_sha":"`+base+`","owned_files":["src"]},"B2":{"status":"open","base_sha":"`+strings.Repeat("0", 40)+`","owned_files":["src"]}}}`)
	return base
}

// Commits enqueue after 15 s without a new HEAD (at most 120 s per burst),
// no change queues nothing, and every row, ticket, superseded marker and
// cursor file is byte-equal to Node's.
func TestB534WatchMatchesNodeGolden(t *testing.T) {
	fx := newWatchFix(t)
	fx.start(t)
	var now int64
	w := fx.watch(&now)
	var got strings.Builder
	for _, step := range watchScenario() {
		if step.files != nil {
			fx.commit(t, step.files)
		}
		now = step.now
		if err := w.Tick(map[string]bool{}); err != nil {
			t.Fatalf("tick %d: %v", now, err)
		}
		got.WriteString(fx.dump(t, now))
	}
	text := watchStamp.ReplaceAllString(strings.ReplaceAll(got.String(), fx.root, "@ROOT@"), `"$1":"T"`)
	want := readFile(t, filepath.Join("testdata", "b534_node_watch.golden"))
	if text != want {
		gl, wl := strings.Split(text, "\n"), strings.Split(want, "\n")
		for i := range wl {
			if i >= len(gl) || gl[i] != wl[i] {
				g := ""
				if i < len(gl) {
					g = gl[i]
				}
				t.Fatalf("line %d differs:\n got %s\nwant %s", i+1, g, wl[i])
			}
		}
		t.Fatalf("Go wrote %d lines, Node %d", len(gl), len(wl))
	}
	// The burst queued the last commit's d.go: the 13th, 120 s after the first.
	if !strings.Contains(text, `"file":"src/d.go","content_hash":"`+sha("d12\n")+`"`) {
		t.Fatal("the burst did not queue the last HEAD's d.go")
	}
}

// Node's ticket times are the wall clock (new Date()); Go stamps them with
// Clock, in Node's ISO form.
func TestB534WatchStampsTicketsWithClock(t *testing.T) {
	fx := newWatchFix(t)
	fx.start(t)
	fx.commit(t, map[string]string{"src/b.go": "package b\n"})
	var now int64 = 1000
	w := fx.watch(&now)
	w.Clock = func() time.Time { return fixedNow }
	for _, at := range []int64{1000, 16000} {
		now = at
		if err := w.Tick(nil); err != nil {
			t.Fatal(err)
		}
	}
	text := fx.dump(t, now)
	if !strings.Contains(text, `"enqueued_at":"2026-11-11T10:00:00.000Z","queued_at":"2026-11-11T10:00:00.000Z","started_at":null`) {
		t.Fatalf("ticket stamps:\n%s", text)
	}
}

// The pool ticks the watch only while the worker is not stopping: a stopped
// worker reads no HEAD and writes no cursor.
func TestB534WatchStopsWithTheWorker(t *testing.T) {
	fx := newWatchFix(t)
	fx.start(t)
	head := fx.commit(t, map[string]string{"src/b.go": "package b\n"})
	var now int64 = 5000
	stopped := true
	p := &Pool{Root: fx.root, Run: "r1", PollMs: 5, Stopping: func() bool { return stopped },
		WatchTick: fx.watch(&now).Tick, RunOne: func(string) (int, bool) { return 0, false }}
	cursor := filepath.Join(QueueDir(fx.root), "watch", "B1.json")
	if _, err := p.Drain(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(cursor); !os.IsNotExist(err) {
		t.Fatalf("a stopped worker ticked the watch: %v", err)
	}
	stopped = false
	if _, err := p.Drain(); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, cursor); !strings.Contains(got, `"pending":{"sha":"`+head+`","seen_at":5000}`) {
		t.Fatalf("cursor %s", got)
	}
}

// A shape Go does not reproduce hands the worker to Node before the tick
// writes a ticket, a row or the cursor.
func TestB534WatchHandsOverUnportedShape(t *testing.T) {
	fx := newWatchFix(t)
	base := fx.start(t)
	head := fx.commit(t, map[string]string{"src/b.go": "package b\n"})
	fx.record(t, `{"run_id":"r1","project":"unit","workspace":"`+fx.root+`","status":"active","blocks":{"B1":{"status":"open","base_sha":"`+base+`","owned_files":["src"],"owned_kinds":["src"]}}}`)
	cursor := filepath.Join(QueueDir(fx.root), "watch", "B1.json")
	pending := `{"run":"r1","base":"` + base + `","tree":null,"skipped":[],"pending":{"sha":"` + head + `","seen_at":0},"burst_started_at":0}` + "\n"
	writeJSON(t, cursor, pending)
	var now int64 = 20000
	w := fx.watch(&now)
	var reasons []string
	w.HandOff = func(r string) { reasons = append(reasons, r) }
	if err := w.Tick(nil); err != nil {
		t.Fatal(err)
	}
	if len(reasons) != 1 || !strings.HasPrefix(reasons[0], WatchHandOff) {
		t.Fatalf("hand-offs %v", reasons)
	}
	if readFile(t, cursor) != pending || len(fx.rows) != 0 || len(Pending(fx.root)) != 0 {
		t.Fatalf("the tick wrote before the hand-off: rows %v pending %v", fx.rows, Pending(fx.root))
	}
}

// Node's throws before the per-block try are the tick's failure (the pool
// warns `watch tick failed (<kind>)`); the watch off reads nothing.
func TestB534WatchFailureKinds(t *testing.T) {
	fx := newWatchFix(t)
	var now int64
	w := fx.watch(&now)
	if err := os.Remove(filepath.Join(fx.home, ".code-forge", "runs", "r1.json")); err != nil {
		t.Fatal(err)
	}
	if err := w.Tick(nil); ErrorKind(err) != "StateError:no-run" {
		t.Fatalf("no record: %v (%s)", err, ErrorKind(err))
	}
	fx.record(t, `{"status":"active","blocks":{"B1":null}}`)
	if err := w.Tick(nil); ErrorKind(err) != "TypeError" {
		t.Fatalf("null block: %v", err)
	}
	fx.record(t, `{"status":`)
	if err := w.Tick(nil); ErrorKind(err) != "SyntaxError" {
		t.Fatalf("bad JSON: %v", err)
	}
	w.Runner.Cfg = map[string]any{"review": map[string]any{"file_loop": "wait"}}
	if err := w.Tick(nil); err != nil {
		t.Fatalf("watch off read the record: %v", err)
	}
}
