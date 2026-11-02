//go:build unix

package daemon

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func writeObject(t *testing.T, file string, value object) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// These shared Node bugs remain compatibility requirements for B182. Change
// them together with the Node wait-block implementation in a later block.
func TestReopenedKeepsNodeTextOrdering(t *testing.T) {
	for _, timestamps := range [][]string{
		{"2100-01-01T00:00:00Z", "2100-01-01T00:00:00.001Z"},
		{"2100-01-01T00:00:00.001Z", "2100-01-01T00:00:00Z"},
	} {
		s := &server{run: "r-test"}
		for _, ts := range timestamps {
			s.rows = append(s.rows, object{"event": "review.reopened", "run": s.run, "block": "B1", "file": "a.go", "by": "human", "ts": ts})
		}
		if got := s.reopened("B1", "a.go"); got != "2100-01-01T00:00:00Z" {
			t.Fatalf("reopened = %q; Node wait-block chooses the lexically greatest timestamp", got)
		}
	}
}

func TestOwnsKeepsNodeGlobSpelling(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		file    string
		want    bool
	}{
		{"./**/*.go", "a.go", false},
		{"./**/*.go", "src/a.go", false},
		{"./**/*.go/", "src/a.go", false},
		{"**/*.go/", "src/a.go", false},
		{"**/*.go", "a.go", true},
		{"**/*.go", "src/a.go", true},
		{"src/", "src/a.go", true},
		{"src/a.go/", "src/a.go", true},
	} {
		t.Run(tc.pattern+"/"+tc.file, func(t *testing.T) {
			if got := owns([]any{tc.pattern}, nil, tc.file); got != tc.want {
				t.Fatalf("owns(%q, %q) = %v, want Node's %v", tc.pattern, tc.file, got, tc.want)
			}
		})
	}
}

func TestTreeStampKeepsNodeOutsideOwnershipPrefix(t *testing.T) {
	root := t.TempDir()
	tree := filepath.Join(root, "repo")
	for _, dir := range []string{filepath.Join(tree, ".git"), filepath.Join(tree, "src"), filepath.Join(root, "outside")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(root, "outside", "a.go")
	if err := os.WriteFile(file, []byte("before"), 0600); err != nil {
		t.Fatal(err)
	}
	stamp := func(owned string) string {
		t.Helper()
		got, err := treeStamp(tree, []any{owned})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	for _, pattern := range []string{"../outside", "../outside/**", "src/../../outside/**"} {
		t.Run(pattern, func(t *testing.T) {
			before, confined := stamp(pattern), stamp("src/**")
			if err := os.WriteFile(file, []byte("after "+pattern), 0600); err != nil {
				t.Fatal(err)
			}
			if stamp(pattern) == before {
				t.Fatal("Node waitTreeStamp visits outside ownership prefixes; daemon must retain parity")
			}
			if stamp("src/**") != confined {
				t.Fatal("an unrelated outside edit changed the stamp for valid ownership")
			}
		})
	}
}

func TestRefreshRejectsScopeMismatchRepeatedly(t *testing.T) {
	for _, field := range []string{"project", "run_id"} {
		for _, cached := range []bool{false, true} {
			t.Run(field+"/cached="+map[bool]string{false: "false", true: "true"}[cached], func(t *testing.T) {
				s := &server{repo: t.TempDir(), home: t.TempDir(), run: "r-test", project: "test"}
				file := filepath.Join(s.repo, ".code-forge", "runs", s.run+".json")
				if cached {
					writeObject(t, file, object{"run_id": s.run, "project": s.project})
					if err := s.refresh(); err != nil {
						t.Fatal(err)
					}
				}
				previous, stamp := s.record, s.recordStamp
				bad := object{"run_id": s.run, "project": s.project}
				bad[field] = "wrong-scope"
				writeObject(t, file, bad)
				for call := 1; call <= 2; call++ {
					result := s.handle(context.Background(), request{Method: "ping"})
					if result["error"] != "block-scope-unreadable" {
						t.Fatalf("call %d served a rejected record: %v", call, result)
					}
					if s.recordStamp != stamp || (s.record == nil) != (previous == nil) || s.record != nil && s.record[field] != previous[field] {
						t.Fatalf("call %d changed the accepted cache", call)
					}
				}
			})
		}
	}
}

func TestWorkerLiveIdentityAndHeartbeat(t *testing.T) {
	s := &server{run: "r-test"}
	s.queue.Dir = t.TempDir()
	cmd := exec.Command("/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(os.Getpid()))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Join(strings.Fields(string(out)), " ")
	if start == "" {
		t.Fatal("current process start time must be readable")
	}
	now := time.Now()
	fresh := now.UTC().Format(time.RFC3339Nano)
	stale := now.Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)
	for _, tc := range []struct {
		name  string
		pid   int
		start string
		beat  any
		live  bool
	}{
		{"unknown fresh", os.Getpid(), "unknown", fresh, true},
		{"unknown stale", os.Getpid(), "unknown", stale, true},
		{"unknown missing heartbeat", os.Getpid(), "unknown", nil, true},
		{"unknown malformed heartbeat", os.Getpid(), "unknown", "not-a-time", true},
		{"mismatch fresh", os.Getpid(), "wrong start", fresh, true},
		{"mismatch stale", os.Getpid(), "wrong start", stale, false},
		{"mismatch missing heartbeat", os.Getpid(), "wrong start", nil, false},
		{"mismatch malformed heartbeat", os.Getpid(), "wrong start", "not-a-time", false},
		{"mismatch near future", os.Getpid(), "wrong start", now.Add(2 * time.Second).UTC().Format(time.RFC3339Nano), true},
		{"mismatch far future", os.Getpid(), "wrong start", now.Add(time.Minute).UTC().Format(time.RFC3339Nano), false},
		{"match stale", os.Getpid(), start, stale, true},
		{"match missing heartbeat", os.Getpid(), start, nil, true},
		{"match far future", os.Getpid(), start, now.Add(time.Minute).UTC().Format(time.RFC3339Nano), true},
		{"dead fresh", 1 << 30, "unknown", fresh, false},
		{"pid one", 1, "unknown", fresh, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeObject(t, filepath.Join(s.queue.Dir, "worker.json"), object{"pid": tc.pid, "run": s.run, "start_time": tc.start, "heartbeat_at": tc.beat})
			if live := s.workerLive(); live != tc.live {
				t.Fatalf("workerLive = %v, want %v", live, tc.live)
			}
		})
	}
}

func TestWorkerLiveEPERM(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can signal all processes; no EPERM fixture")
	}
	out, err := exec.Command("/bin/ps", "-axo", "pid=").Output()
	if err != nil {
		t.Fatal(err)
	}
	// Node rejects pid 1, so find another process owned by a different user.
	for _, field := range strings.Fields(string(out)) {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 1 || syscall.Kill(pid, 0) != syscall.EPERM {
			continue
		}
		s := &server{run: "r-test"}
		s.queue.Dir = t.TempDir()
		writeObject(t, filepath.Join(s.queue.Dir, "worker.json"), object{
			"pid": pid, "run": s.run, "start_time": "wrong start", "heartbeat_at": time.Now().UTC().Format(time.RFC3339Nano),
		})
		if !s.workerLive() {
			t.Fatalf("EPERM pid %d with fresh heartbeat must be live", pid)
		}
		return
	}
	t.Skip("no live pid greater than 1 returns EPERM on this host")
}

func TestStaleMismatchedWorkerReportsDown(t *testing.T) {
	s := &server{repo: t.TempDir(), home: t.TempDir(), run: "r-test", project: "test", trees: make(map[string]*treeState)}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base"}} {
		if _, err := git(s.repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	base, err := git(s.repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	writeObject(t, filepath.Join(s.repo, ".code-forge", "runs", s.run+".json"), object{
		"run_id": s.run, "project": s.project,
		"blocks": object{"B1": object{"owned_files": []any{"src/**"}, "base_sha": base[:len(base)-1]}},
	})
	s.queue.Dir = filepath.Join(s.repo, ".code-forge", "queue")
	writeObject(t, filepath.Join(s.queue.Dir, "worker.json"), object{"pid": os.Getpid(), "run": s.run, "start_time": "wrong start", "heartbeat_at": time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)})
	zero := int64(0)
	result := s.handle(context.Background(), request{Method: "wait-block", Block: "B1", MaxMS: &zero})
	if result["status"] != "worker_down" || result["done"] != false {
		t.Fatalf("stale worker was served: %v", result)
	}
}

func TestWaitBlockUnavailableReview(t *testing.T) {
	s := &server{repo: t.TempDir(), home: t.TempDir(), run: "r-test", project: "test", trees: make(map[string]*treeState)}
	var err error
	s.repo, err = filepath.EvalSymlinks(s.repo)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base"}} {
		if _, err := git(s.repo, args...); err != nil {
			t.Fatal(err)
		}
	}
	base, err := git(s.repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	writeObject(t, filepath.Join(s.repo, ".code-forge", "runs", s.run+".json"), object{
		"run_id": s.run, "project": s.project,
		"blocks": object{"B1": object{"owned_files": []any{"*.go"}, "base_sha": strings.TrimSpace(base)}},
	})
	if err := os.WriteFile(filepath.Join(s.repo, "a.go"), []byte("package fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s.queue.Dir = filepath.Join(s.repo, ".code-forge", "queue")
	writeObject(t, filepath.Join(s.queue.Dir, "worker.json"), object{"pid": os.Getpid(), "run": s.run, "start_time": "unknown"})
	zero := int64(0)
	req := request{Method: "wait-block", Block: "B1", MaxMS: &zero}
	pending := s.handle(context.Background(), req)
	states, ok := pending["files"].([]object)
	if !ok || len(states) != 1 || states[0]["status"] != "pending" {
		t.Fatalf("expected exactly one pending file, got %v", pending)
	}
	id := text(states[0], "ticket")
	marker := filepath.Join(s.queue.Dir, id+".done")
	// Spend the single retry through the real join before publishing its last result.
	rel := ".code-forge/reviews/initial.json"
	writeObject(t, filepath.Join(s.repo, rel), object{"status": "unavailable"})
	writeObject(t, marker, object{"ticket": id, "status": "unavailable", "result": rel})
	if got := s.handle(context.Background(), req); got["reason"] != "wait_timeout" {
		t.Fatalf("first unavailable result must stay pending: %v", got)
	}
	for _, tc := range []struct {
		name   string
		marker object
		done   bool
	}{
		{"unavailable", object{"status": "unavailable"}, false},
		{"stale", object{"status": "stale"}, false},
		{"refused", object{"status": "refused", "reason": "review-state-lost"}, false},
		{"unknown", object{"status": "unknown"}, false},
		{"approved", object{"status": "reviewed", "approved": true}, true},
		{"findings", object{"status": "reviewed", "approved": false, "open_findings": true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.marker["ticket"] = id
			writeObject(t, marker, tc.marker)
			got := s.handle(context.Background(), req)
			if got["done"] != tc.done {
				t.Fatalf("done = %v, want %v: %v", got["done"], tc.done, got)
			}
			if !tc.done {
				wantNext := "review-file a.go --fresh re-runs it; if --fresh refuses, block reopen B1 --run r-test --file a.go --reason <text>"
				if got["reason"] != "review_unavailable" || got["next"] != wantNext {
					t.Fatalf("missing unavailable report: %v", got)
				}
			} else if got["reason"] != nil || got["next"] != nil {
				t.Fatalf("completed review has an unavailable report: %v", got)
			}
		})
	}
}
