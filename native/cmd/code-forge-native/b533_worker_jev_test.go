package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/jev"
	"github.com/ricardov03/code-forge/native/worker"
)

// B533: a worker whose Jev key resolves serves in Go. The reviewer (fake
// claude) reports one warning; the fake Jev (httptest, never the network)
// calls it a nit, so the round approves with a review.triage row from Jev.

const b533FakeClaude = `#!/bin/sh
in=$(cat)
heads=$(printf '%s\n' "$in" | grep -E '^@@ -[0-9]+(,[0-9]+)? \+[0-9]+(,[0-9]+)? @@' | sed -E 's/^(@@ -[0-9,]+ \+[0-9,]+ @@).*/\1/' | awk '!s[$0]++' | sed 's/.*/"&"/' | paste -sd, -)
printf '%s' '{"type":"result","is_error":false,"result":"done","structured_output":{"passed":false,"summary":"fake review","reviewed_hunks":['"$heads"'],"findings":[{"id":"F1","file":"a.txt","line_start":2,"line_end":2,"severity":"warning","category":"style","claim":"a warning","evidence":"the line says world","fix":"remove it"}]},"usage":{"input_tokens":1000,"output_tokens":300}}'
`

type b533Jev struct {
	mu     sync.Mutex
	auth   []string
	bodies []string
}

func b533Serve(t *testing.T, f *b489Fix) *b533Jev {
	t.Helper()
	j := &b533Jev{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		j.mu.Lock()
		j.auth = append(j.auth, r.Header.Get("Authorization"))
		j.bodies = append(j.bodies, string(body))
		j.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"answers":{"defect":{"type":"noul","noul":0.12}},"usage":{"input_tokens":5,"output_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	old := workerJevOpts
	workerJevOpts = jev.Opts{URL: srv.URL + "/v1/systemone", Sleep: func(float64) {}}
	t.Cleanup(func() { workerJevOpts = old })
	bin := filepath.Dir(b533Which(t))
	if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(b533FakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	return j
}

func b533Which(t *testing.T) string {
	t.Helper()
	for _, dir := range strings.Split(os.Getenv("PATH"), ":") {
		if p := filepath.Join(dir, "claude"); b489Exists(p) {
			return p
		}
	}
	t.Fatal("no fake claude on PATH")
	return ""
}

func (j *b533Jev) check(t *testing.T, f *b489Fix, key, id string) {
	t.Helper()
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.auth) != 1 || j.auth[0] != "Bearer "+key {
		t.Fatalf("jev calls %d, auth %q", len(j.auth), j.auth)
	}
	if !strings.HasPrefix(j.bodies[0], `{"state":{"file_diff_hunk":"","finding":{"id":"F1","file":"a.txt","line_start":2,`) ||
		!strings.Contains(j.bodies[0], `"model":"jev-latest","questions":{"defect":{"type":"noul"`) || strings.Contains(j.bodies[0], key) {
		t.Fatalf("jev body %s", j.bodies[0])
	}
	ledgerText, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"))
	if !strings.Contains(string(ledgerText), `"event":"review.triage","file":"a.txt","finding":"F1","severity":"warning","source":"jev","p":0.12,"band":"nit","verdict":"nit"`) ||
		!strings.Contains(string(ledgerText), `"event":"review.approved","file":"a.txt"`) {
		t.Fatalf("ledger:\n%s", ledgerText)
	}
	if strings.Contains(string(ledgerText), key) {
		t.Fatal("the Jev key is in the ledger")
	}
	if !b489Exists(filepath.Join(worker.QueueDir(f.root), id+".done")) || f.results(t, id) != 1 {
		t.Fatalf("ticket %s not reviewed once in Go", id)
	}
	if rows := b460Handovers(t, f.home); len(rows) != 0 {
		t.Fatalf("hand-overs %v", rows)
	}
}

// The env variable's key: --once drains in Go with Jev triage.
func TestB533WorkerEnvJevKeyServesInGo(t *testing.T) {
	f := newB489Fix(t)
	j := b533Serve(t, f)
	key := "fake-env-jev-key-0001"
	t.Setenv("CODE_FORGE_KEY_JEV", key)
	id := f.ticket(t, "B1", "a.txt", "hello\nworld\n")
	code, out, errText := b460Call(t, "worker", "--run", "r1", "--once")
	if code != 0 || out != "" || !strings.HasSuffix(errText, "worker: drained 1 ticket(s)\n") || strings.Contains(errText, key) {
		t.Fatalf("worker --once = %d %q %q", code, out, errText)
	}
	j.check(t, f, key, id)
}

// The file store's key (CODE_FORGE_KEY_BACKEND=file): the serving line says
// the key resolved, like loop.mjs.
func TestB533WorkerStoreJevKeyServesInGo(t *testing.T) {
	f := newB489Fix(t)
	j := b533Serve(t, f)
	key := "fake-store-jev-key-0002"
	b460Write(t, filepath.Join(f.home, ".code-forge", "store", "jev.key"), `{"v":"`+key+`","exp":99999999999999}`+"\n")
	id := f.ticket(t, "B1", "a.txt", "hello\nworld\n")
	stopped := make(chan struct{})
	workerSignals = func(c chan<- os.Signal) func() {
		go func() {
			for !b489Exists(filepath.Join(worker.QueueDir(f.root), id+".done")) {
				select {
				case <-stopped:
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
			c <- syscall.SIGTERM
		}()
		return func() { close(stopped) }
	}
	code, _, errText := b460Call(t, "worker", "--run", "r1", "--poll-ms", "10")
	want := fmt.Sprintf("worker: pid %d serving run r1 · jev key resolved\n", os.Getpid())
	if code != 0 || !strings.Contains(errText, want) || !strings.HasSuffix(errText, "worker: stopped\n") || strings.Contains(errText, key) {
		t.Fatalf("worker = %d %q", code, errText)
	}
	j.check(t, f, key, id)
}

// With the default key backends a key Go proves absent (no env, no store
// file, the keychain says no item) is none: the worker serves in Go and
// falls back to the rules.
func TestB533WorkerKeychainAbsentServesInGo(t *testing.T) {
	f := newB489Fix(t)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	old := workerJevProbe
	workerJevProbe = worker.JevProbe{Keychain: func(string, string) (bool, bool) { return false, true }}
	t.Cleanup(func() { workerJevProbe = old })
	id := f.ticket(t, "B1", "a.txt", "hello\nworld\n")
	code, _, errText := b460Call(t, "worker", "--run", "r1", "--once")
	if code != 0 || !strings.HasSuffix(errText, "worker: drained 1 ticket(s)\n") || f.results(t, id) != 1 {
		t.Fatalf("worker --once = %d %q", code, errText)
	}
	if rows := b460Handovers(t, f.home); len(rows) != 0 {
		t.Fatalf("hand-overs %v", rows)
	}
}
