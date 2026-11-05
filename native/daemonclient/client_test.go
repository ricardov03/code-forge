package daemonclient

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRequest(t *testing.T) {
	// A short private HOME keeps Unix paths below macOS's socket path limit.
	home, err := os.MkdirTemp("/tmp", "cf-client-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NATIVE", "1")
	const run = "r-test"
	dir := filepath.Join(home, ".code-forge", "runs", run)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "native.sock")
	for _, tc := range []struct {
		name, ping, response string
		method               string
		ok                   bool
	}{
		{name: "ok", response: `{"runs":[{"run_id":"r-test","blocks":[]}],"stats":{"rows":3}}`, ok: true},
		{name: "ping", method: "ping", ok: true},
		{name: "no socket"},
		{name: "bad JSON", response: `{broken}`},
		{name: "array", response: `[]`},
		{name: "null", response: `null`},
		{name: "failure", response: `{"ok":false}`},
		{name: "slow server"},
		{name: "slow ping"},
		{name: "oversized", response: `{"padding":"` + strings.Repeat("x", maxAnswer) + `"}`},
		{name: "wrong version", ping: `{"ok":true,"run":"r-test","version":"wrong"}`},
		{name: "wrong run", ping: `{"ok":true,"run":"r-other","version":"dev"}`},
		{name: "odd status", response: `{"runs":[{"run_id":"r-other","blocks":[]}]}`},
		{name: "rows", method: "rows", response: `{"rows":[{}]}`, ok: true},
		{name: "bad rows", method: "rows", response: `{"rows":[null]}`},
		{name: "wait-block", method: "wait-block", response: `{"block":"B1","done":false,"files":[{"file":"a","status":"done"}]}`, ok: true},
		{name: "bad wait-block", method: "wait-block", response: `{"block":"B2","done":false,"files":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			timeout := 50 * time.Millisecond
			if tc.name == "oversized" {
				timeout = 2 * time.Second
			}
			method := tc.method
			if method == "" {
				method = "status"
			}
			req := map[string]any{"method": method}
			if method == "wait-block" {
				req["block"] = "B1"
			}
			var received []map[string]any
			var serverDone chan struct{}
			if tc.name != "no socket" {
				listener, err := net.Listen("unix", socket)
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan struct{})
				serverDone = done
				defer func() { listener.Close(); <-done }()
				go func() {
					defer close(done)
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					conn.SetDeadline(time.Now().Add(max(time.Second, timeout)))
					scan := bufio.NewScanner(conn)
					for scan.Scan() {
						var got map[string]any
						json.Unmarshal(scan.Bytes(), &got)
						received = append(received, got)
						if len(received) == 1 {
							if tc.name == "slow ping" {
								continue
							}
							ping := tc.ping
							if ping == "" {
								ping = `{"ok":true,"run":"r-test","version":"dev"}`
							}
							conn.Write([]byte(ping + "\n"))
						} else if tc.name != "slow server" {
							conn.Write([]byte(tc.response + "\n"))
						}
					}
				}()
			}
			started := time.Now()
			answer, err := Request(run, req, timeout)
			if tc.ok {
				if err != nil || answer == nil {
					t.Fatalf("healthy %s: answer=%v error=%v", method, answer, err)
				}
				<-serverDone
				wantRequests := []map[string]any{{"method": "ping"}}
				if method != "ping" {
					wantRequests = append(wantRequests, req)
				}
				if !reflect.DeepEqual(received, wantRequests) {
					t.Fatalf("handshake/method requests: got %v, want %v", received, wantRequests)
				}
				if _, exists := answer["stats"]; exists {
					t.Fatal("method answer retained transport stats")
				}
				if method == "status" && len(answer["runs"].([]any)) != 1 {
					t.Fatal("status must return exactly one run")
				}
			} else if err == nil || answer != nil {
				t.Fatalf("%s: want nil answer and error, got %v, %v", tc.name, answer, err)
			}
			if tc.name == "oversized" && err.Error() != "daemon answer exceeds 8 MiB" {
				t.Fatalf("oversized: want size-limit error, got %v", err)
			}
			if time.Since(started) > timeout+450*time.Millisecond {
				t.Fatal("request exceeded its bounded timeout")
			}
		})
		if _, err := os.Stat(socket); !os.IsNotExist(err) {
			t.Fatalf("%s left native.sock: %v", tc.name, err)
		}
	}
	for _, run := range []string{"", "../escape", "Upper", strings.Repeat("a", 65)} {
		answer, err := Request(run, map[string]any{"method": "ping"}, time.Second)
		if err == nil || answer != nil {
			t.Fatalf("invalid run %q returned %v, %v", run, answer, err)
		}
	}
	for _, tc := range []struct {
		env    string
		config map[string]any
		want   bool
	}{
		{"", nil, true}, {"1", nil, true}, {"false", nil, true}, {"0", nil, false},
		{"1", map[string]any{"native": map[string]any{"daemon": false}}, false},
		{"1", map[string]any{"native": map[string]any{"daemon": "false"}}, true},
	} {
		t.Setenv("CODE_FORGE_NATIVE", tc.env)
		if got := Enabled(tc.config); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("Enabled(%v) with env %q = %v, want %v", tc.config, tc.env, got, tc.want)
		}
	}
}
