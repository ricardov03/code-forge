package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

func TestUnknownStartRequiresFreshHeartbeat(t *testing.T) {
	s := &server{run: "r-test"}
	s.queue.Dir = t.TempDir()
	for _, tc := range []struct {
		name string
		beat any
		live bool
	}{
		{"fresh", time.Now().UTC().Format(time.RFC3339Nano), true},
		{"two minutes old", time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano), false},
		{"missing", nil, false},
		{"malformed", "not-a-time", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeObject(t, filepath.Join(s.queue.Dir, "worker.json"), object{"pid": os.Getpid(), "run": s.run, "start_time": "unknown", "heartbeat_at": tc.beat})
			if live := s.workerLive(); live != tc.live {
				t.Fatalf("workerLive = %v, want %v", live, tc.live)
			}
		})
	}
}

func TestStaleUnknownWorkerReportsDown(t *testing.T) {
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
	writeObject(t, filepath.Join(s.queue.Dir, "worker.json"), object{"pid": os.Getpid(), "run": s.run, "start_time": "unknown", "heartbeat_at": time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)})
	zero := int64(0)
	result := s.handle(context.Background(), request{Method: "wait-block", Block: "B1", MaxMS: &zero})
	if result["status"] != "worker_down" || result["done"] != false {
		t.Fatalf("stale worker was served: %v", result)
	}
}
