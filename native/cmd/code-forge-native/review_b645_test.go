package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// b645Ticket is a queue ticket of a.go ("package a\n") in run r-test block B1,
// in Node's key order.
func b645Ticket(id, enqueued, reopened string) string {
	body := `{"ticket":"` + id + `","run":"r-test","block":"B1","file":"a.go","content_hash":"` + b645Hash + `","enqueued_at":"` + enqueued + `","queued_at":"` + enqueued + `","started_at":null,"finished_at":null,"duration_ms":null`
	if reopened != "" {
		body += `,"reopened_at":"` + reopened + `"`
	}
	return body + "}\n"
}

const (
	b645Hash  = "7b39baa38a2ec2b8d111bbbd8e448e80226477ab40105d9d2123d4dc18067438"
	b645Plain = "56e81c268be71f1d97e11ed3"
	// the reopen attempts of a.go at 2026-01-02T00:00:00.000Z, at the
	// date-only 2026-01-02 and at 2026-01-03T00:00:00.000Z
	b645Day2     = "069fcf11c5ef1a9d23fd30fc"
	b645Day2Date = "2d5e1b88843d6837f355936e"
	b645Day3     = "66b3c641928b3ac7e1c35220"
	b645Done     = `{"ticket":"` + b645Plain + `","status":"refused","reason":"superseded"}` + "\n"
)

// Former hand-over enqueue-identity ("reopened ticket requires Node"): a file
// with a reopened attempt is enqueued in Go. The goldens were recorded from
// Node (CODE_FORGE_NATIVE=0 ./bin/code-forge review-file a.go --block B1) on
// the same queue: stdout, stderr, exit code and every queue file.
func TestB645ReviewFileEnqueueReopened(t *testing.T) {
	for _, tc := range []struct {
		name   string
		queue  map[string]string
		stdout string
		after  map[string]string
	}{
		{"one-reopen",
			map[string]string{
				b645Plain + ".json": b645Ticket(b645Plain, "2026-01-01T00:00:00.000Z", ""),
				b645Day2 + ".json":  b645Ticket(b645Day2, "2026-01-02T00:00:00.001Z", "2026-01-02T00:00:00.000Z"),
			},
			`{"ticket":"` + b645Day2 + `","status":"queued","file":"a.go","content_hash":"` + b645Hash + `","reopened_at":"2026-01-02T00:00:00.000Z"}` + "\n",
			map[string]string{b645Plain + ".done": b645Done}},
		{"latest-of-two-reopens-date-only",
			map[string]string{
				b645Plain + ".json":    b645Ticket(b645Plain, "2026-01-01T00:00:00.000Z", ""),
				b645Day2Date + ".json": b645Ticket(b645Day2Date, "2026-01-02T00:00:00.001Z", "2026-01-02"),
				b645Day3 + ".json":     b645Ticket(b645Day3, "2026-01-03T00:00:00.001Z", "2026-01-03T00:00:00.000Z"),
			},
			`{"ticket":"` + b645Day3 + `","status":"queued","file":"a.go","content_hash":"` + b645Hash + `","reopened_at":"2026-01-03T00:00:00.000Z"}` + "\n",
			map[string]string{
				b645Plain + ".done":    b645Done,
				b645Day2Date + ".done": `{"ticket":"` + b645Day2Date + `","status":"refused","reason":"superseded"}` + "\n",
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			repo := reviewFileRepo(t, home)
			stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
			pid := strconv.Itoa(os.Getpid())
			files := map[string]string{
				filepath.Join(repo, ".code-forge", "workers", pid+"-unknown.json"): `{"pid":` + pid + `,"start_time":"unknown","run":"r-test","started_at":"` + stamp + `","heartbeat_at":"` + stamp + `"}`,
				filepath.Join(repo, "a.go"):                                        "package a\n",
			}
			for name, body := range tc.queue {
				files[filepath.Join(repo, ".code-forge", "queue", name)] = body
			}
			for name, body := range files {
				closeFixtureWrite(t, name, []byte(body))
			}
			got := reviewFileExec(t, repo, home, true, "a.go", "--block", "B1")
			if got != (reviewFileRun{tc.stdout, "", 0}) {
				t.Fatalf("got %+v\nwant stdout %s exit 0", got, tc.stdout)
			}
			want := map[string]string{}
			for name, body := range tc.queue {
				want[name] = body
			}
			for name, body := range tc.after {
				want[name] = body
			}
			queue := reviewFileQueue(t, repo)
			if len(queue) != len(want) {
				t.Fatalf("queue = %v\nwant %v", queue, want)
			}
			for name, body := range want {
				if queue[name] != body {
					t.Errorf("%s = %q, want %q", name, queue[name], body)
				}
			}
			// No hand-over row.
			if data, err := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl")); !os.IsNotExist(err) {
				t.Fatalf("a hand-over row was written: %s %v", data, err)
			}
		})
	}
}
