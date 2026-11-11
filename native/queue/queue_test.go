package queue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ticket"
)

func TestPublishAfterReopenedMillisecond(t *testing.T) {
	reopened := "2026-11-05T12:00:00.123Z"
	boundary, err := time.Parse(time.RFC3339Nano, reopened)
	if err != nil {
		t.Fatal(err)
	}
	for _, offset := range []time.Duration{-time.Millisecond, 0, time.Nanosecond, time.Millisecond - time.Nanosecond, time.Millisecond, 2 * time.Millisecond} {
		t.Run(offset.String(), func(t *testing.T) {
			dir := t.TempDir()
			p := ticket.Parts{Run: "r-test", Block: "B1", File: "a.go", ContentHash: "hash", ReopenedAt: &reopened}
			id := ticket.ReviewID(p)
			if err := Publish(dir, p, id, boundary.Add(offset)); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, id+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var value Object
			if err := json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			want := boundary.Add(time.Millisecond)
			if offset > time.Millisecond {
				want = boundary.Add(offset).Truncate(time.Millisecond)
			}
			stamp := want.Format("2006-01-02T15:04:05.000Z")
			if value["enqueued_at"] != stamp || value["queued_at"] != stamp {
				t.Fatalf("ticket stamps = %v, %v; want %s", value["enqueued_at"], value["queued_at"], stamp)
			}
		})
	}
}

func retryFixture(t *testing.T) (*Index, string, string, Object) {
	t.Helper()
	repo := t.TempDir()
	dir := filepath.Join(repo, ".code-forge", "queue")
	result := ".code-forge/reviews/result.json"
	for _, name := range []string{dir, filepath.Dir(filepath.Join(repo, result))} {
		if err := os.MkdirAll(name, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, result), []byte(`{"status":"stale"}`), 0600); err != nil {
		t.Fatal(err)
	}
	id := "0123456789abcdef01234567"
	idx := &Index{Dir: dir}
	if err := idx.Refresh(); err != nil {
		t.Fatal(err)
	}
	return idx, repo, id, Object{"ticket": id, "status": "stale", "result": result}
}

func TestRetryOnceRejectsInvalidTicketID(t *testing.T) {
	idx, repo, _, marker := retryFixture(t)
	outside := filepath.Join(filepath.Dir(idx.Dir), "x.done")
	data := []byte(`{"status":"stale"}`)
	if err := os.WriteFile(outside, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"stale", "reviewed"} {
		marker["status"] = status
		got, err := idx.RetryOnce(repo, "../x", marker)
		if got != nil || err == nil || err.Error() != "bad-ticket" {
			t.Fatalf("status %s: RetryOnce = %v, %v; want bad-ticket", status, got, err)
		}
	}
	if got, err := os.ReadFile(outside); err != nil || string(got) != string(data) {
		t.Fatalf("outside marker changed: %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(idx.Dir), "x.retried")); !os.IsNotExist(err) {
		t.Fatalf("outside retry marker must not be created: %v", err)
	}
}

func TestRetryOnceRestoresEmptyMarkerWithoutCachedBytes(t *testing.T) {
	for _, cachedEntry := range []bool{false, true} {
		name := "absent entry"
		if cachedEntry {
			name = "nil bytes"
		}
		t.Run(name, func(t *testing.T) {
			idx, repo, id, marker := retryFixture(t)
			file := filepath.Join(idx.Dir, id+".done")
			if err := os.WriteFile(file, []byte{}, 0600); err != nil {
				t.Fatal(err)
			}
			if cachedEntry {
				info, err := os.Stat(file)
				if err != nil {
					t.Fatal(err)
				}
				idx.entries[id+".done"] = cached{info: info, value: marker}
			}
			got, err := idx.RetryOnce(repo, id, marker)
			if got != nil || err == nil || err.Error() != "result-unreadable" {
				t.Fatalf("RetryOnce = %v, %v; want result-unreadable", got, err)
			}
			if data, err := os.ReadFile(file); err != nil || len(data) != 0 {
				t.Fatalf("empty marker must be restored: %q, %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(idx.Dir, id+".retried")); !os.IsNotExist(err) {
				t.Fatalf("retry must remain unused: %v", err)
			}
		})
	}
}

func TestRetryOnceReloadsChangedMarkerWithSameStat(t *testing.T) {
	idx, repo, id, marker := retryFixture(t)
	file := filepath.Join(idx.Dir, id+".done")
	write := func(value Object) {
		t.Helper()
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, data, 0600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Unix(1700000000, 0)
		if err := os.Chtimes(file, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	marker["reason"] = "older"
	write(marker)
	seen, err := idx.Marker(id)
	if err != nil {
		t.Fatal(err)
	}
	marker["reason"] = "newer"
	write(marker)
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	prev := idx.entries[id+".done"].info
	if !os.SameFile(prev, info) || prev.Size() != info.Size() || !prev.ModTime().Equal(info.ModTime()) {
		t.Fatal("fixture must preserve inode, size and mtime")
	}
	got, err := idx.RetryOnce(repo, id, seen)
	if err != nil || !reflect.DeepEqual(got, marker) {
		t.Fatalf("RetryOnce = %v, %v; want replaced marker %v", got, err, marker)
	}
	if got, err := idx.Marker(id); err != nil || !reflect.DeepEqual(got, marker) {
		t.Fatalf("Marker retained stale cache: %v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(idx.Dir, id+".retried")); !os.IsNotExist(err) {
		t.Fatalf("changed marker must not spend the retry: %v", err)
	}
}

func TestNullTreeTicket(t *testing.T) {
	idx := &Index{Dir: t.TempDir()}
	id := ticket.ReviewID(ticket.Parts{Run: "r-test", Block: "B1", File: "src/a.mjs", ContentHash: "hash"})
	value := Object{"ticket": id, "run": "r-test", "block": "B1", "file": "src/a.mjs", "content_hash": "hash", "tree": nil}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(idx.Dir, id+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for call := 1; call <= 2; call++ {
		if err := idx.Refresh(); err != nil {
			t.Fatalf("call %d: %v", call, err)
		}
		got := idx.Block("r-test", "B1", nil, nil)
		if len(got) != 1 || got[0]["ticket"] != id {
			t.Fatalf("call %d: null tree excluded: %v", call, got)
		}
	}
	for _, tree := range []any{"relative/tree", 42, false} {
		value["tree"] = tree
		if err := validate(id, value); err == nil {
			t.Fatalf("accepted invalid tree %v", tree)
		}
	}
}

func TestPruneRun(t *testing.T) {
	idx := &Index{Dir: t.TempDir()}
	for _, run := range []string{"r-ended", "r-open"} {
		for _, block := range []string{"B1", "B2"} {
			p := ticket.Parts{Run: run, Block: block, File: "a.go", ContentHash: "hash"}
			id := ticket.ReviewID(p)
			data, err := json.Marshal(Object{"ticket": id, "run": run, "block": block, "file": p.File, "content_hash": p.ContentHash})
			if err != nil {
				t.Fatal(err)
			}
			for _, suffix := range []string{".json", ".done", ".delivered", ".retried"} {
				if err := os.WriteFile(filepath.Join(idx.Dir, id+suffix), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if err := idx.Refresh(); err != nil {
		t.Fatal(err)
	}
	pruner, ok := any(idx).(interface {
		PruneRun(string) (int, int, error)
	})
	if !ok {
		t.Fatal("run pruning removed 0 tickets and 0 markers; want 2 tickets and 6 markers")
	}
	n, m, err := pruner.PruneRun("r-ended")
	if n != 2 || m != 6 || err != nil {
		t.Fatalf("PruneRun = %d, %d, %v; want 2, 6, nil", n, m, err)
	}
	names, err := os.ReadDir(idx.Dir)
	if err != nil || len(names) != 8 {
		t.Fatalf("remaining files = %d, %v; want 8, nil", len(names), err)
	}
	for _, block := range []string{"B1", "B2"} {
		if got := len(idx.Block("r-ended", block, nil, nil)); got != 0 {
			t.Errorf("ended %s index has %d tickets", block, got)
		}
		if got := len(idx.Block("r-open", block, nil, nil)); got != 1 {
			t.Errorf("open %s index has %d tickets, want 1", block, got)
		}
	}
}

// Ensure writes enqueueHash's bytes: Node key order, named fields last, no
// HTML escaping, one trailing newline.
func TestEnsureWritesNodeTicketBytes(t *testing.T) {
	for _, named := range []bool{false, true} {
		idx := &Index{Dir: t.TempDir()}
		p := ticket.Parts{Run: "r-test", Block: "B1", File: "café<&>.mjs", ContentHash: "h1"}
		tail := ""
		if named {
			tree, revision, reopened := "/tree", "rev1", "2000-01-01T00:00:00.000Z"
			p.Tree, p.AcceptanceRevision, p.ReopenedAt = &tree, &revision, &reopened
			tail = `,"tree":"/tree","acceptance_revision":"rev1","reopened_at":"2000-01-01T00:00:00.000Z"`
		}
		if err := idx.Ensure(p); err != nil {
			t.Fatal(err)
		}
		id := ticket.ReviewID(p)
		data, err := os.ReadFile(filepath.Join(idx.Dir, id+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatal(err)
		}
		stamp, _ := body["enqueued_at"].(string)
		want := `{"ticket":"` + id + `","run":"r-test","block":"B1","file":"café<&>.mjs","content_hash":"h1","enqueued_at":"` + stamp + `","queued_at":"` + stamp + `","started_at":null,"finished_at":null,"duration_ms":null` + tail + "}\n"
		if string(data) != want {
			t.Fatalf("named=%v ticket bytes\n got %s\nwant %s", named, data, want)
		}
	}
}
