package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/daemon"
	"github.com/ricardov03/code-forge/native/daemonclient"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

func TestStatusMapConversionErrors(t *testing.T) {
	for _, value := range []any{math.NaN(), math.Inf(1), make(chan int)} {
		if m, err := statusMap(ledger.Object{{Key: "unsupported", Value: value}}); err == nil || m != nil {
			t.Fatalf("unsupported %T: map=%v, error=%v; want nil map and error", value, m, err)
		}
	}
	r, err := ledger.DecodeObject([]byte(`{"blocks":{"B10":{},"B2":{}},"run_id":"r-test"}`))
	if err != nil {
		t.Fatal(err)
	}
	m, err := statusMap(r)
	if err != nil || m["run_id"] != "r-test" || len(m["blocks"].(map[string]any)) != 2 {
		t.Fatalf("valid conversion: map=%v, error=%v", m, err)
	}
}

// This uses the public route, including its process-free error behavior, rather
// than checking implementation text. The helper is a real socket daemon.
func TestStatusDirectSources(t *testing.T) {
	if os.Getenv("CF_STATUS_DAEMON_HELPER") == "1" {
		if err := daemon.Serve(os.Getenv("HOME"), "r-measure", version, time.Minute); err != nil {
			t.Fatal(err)
		}
		return
	}
	home, err := os.MkdirTemp("/tmp", "cfs-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(home)
	workspace := filepath.Join(home, "repo")
	if err := os.MkdirAll(filepath.Join(home, ".code-forge", "runs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	git := exec.Command("git", "init", "-q", workspace)
	git.Dir, git.Env = workspace, gitx.ChildEnv()
	if output, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", output, err)
	}
	data, _ := json.Marshal(map[string]any{"run_id": "r-measure", "workspace": workspace, "project": "measure", "status": "ended", "blocks": map[string]any{"B1": map[string]any{"status": "open", "level": "L1"}}})
	if err := os.WriteFile(filepath.Join(home, ".code-forge", "runs", "r-measure.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, ".code-forge", "runs"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".code-forge", "runs", "r-measure.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".code-forge.yml"), []byte("version: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-status-node")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	output, err := os.CreateTemp(home, "stdout-")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	previous := os.Stdout
	os.Stdout = output
	defer func() { os.Stdout = previous }()
	args := []string{"status", "--run", "r-measure", "--json"}
	if code, err := run(args); err != nil || code != 0 {
		t.Fatalf("without socket or Node: code=%d, error=%v, want 0", code, err)
	}
	want, err := os.ReadFile(output.Name())
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestStatusDirectSources$")
	child.Env = append(envWithoutGit(), "CF_STATUS_DAEMON_HELPER=1")
	var stderr bytes.Buffer
	child.Stderr = &stderr
	child.Stdout = &stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Signal(os.Interrupt); _ = child.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := daemonclient.Request("r-measure", map[string]any{"method": "ping"}, time.Second); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon not ready: %s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if b, ok := b268Status(args[1:]); !ok || !bytes.Equal(append(b, '\n'), want) {
		t.Fatalf("B268 status=%s, ok=%v, want %s", b, ok, want)
	}
	direct, socket := []time.Duration{}, []time.Duration{}
	for i := 0; i < 101; i++ {
		start := time.Now()
		if code, err := run(args); code != 0 || err != nil {
			t.Fatalf("direct code=%d: %v", code, err)
		}
		direct = append(direct, time.Since(start))
		start = time.Now()
		if _, ok := b268Status(args[1:]); !ok {
			t.Fatal("B268 transport declined the fixture")
		}
		socket = append(socket, time.Since(start))
	}
	sort.Slice(direct, func(i, j int) bool { return direct[i] < direct[j] })
	sort.Slice(socket, func(i, j int) bool { return socket[i] < socket[j] })
	t.Logf("101 warm calls, median: direct %.3f ms; B268 daemon %.3f ms", float64(direct[50])/float64(time.Millisecond), float64(socket[50])/float64(time.Millisecond))
}

// The previous B268 transport is retained only as a benchmark control.
// Accept only the single-run JSON form. Usage and all other forms remain Node's.
func b268Run(args []string) (string, bool) {
	run, jsonFlag, runSeen := "", false, false
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--json" && !jsonFlag:
			jsonFlag = true
		case args[i] == "--run" && !runSeen && i+1 < len(args):
			runSeen = true
			i++
			run = args[i]
		case strings.HasPrefix(args[i], "--run=") && !runSeen:
			runSeen = true
			run = strings.TrimPrefix(args[i], "--run=")
		default:
			return "", false
		}
		if run == "" && strings.HasPrefix(args[i], "--run=") {
			return "", false
		}
	}
	return run, jsonFlag && run != ""
}

func b268Status(args []string) ([]byte, bool) {
	run, ok := b268Run(args)
	if !ok || !daemonclient.Enabled(nil) {
		return nil, false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false
	}
	record, err := state.ReadRun(home, run)
	if err != nil || record.Get("run_id") != run || !b268WarningsAbsent(record) {
		return nil, false
	}
	cfg, ok := statusConfig(record)
	if !ok || !daemonclient.Enabled(cfg) {
		return nil, false
	}
	answer, err := daemonclient.Request(run, map[string]any{"method": "status"}, 0)
	if err != nil {
		return nil, false
	}
	runs := answer["runs"].([]any) // Request validated the envelope.
	summary, ok := b268OrderedStatus(runs[0].(map[string]any))
	if !ok {
		return nil, false
	}
	output, err := ledger.Stringify(ledger.Object{{Key: "runs", Value: []any{summary}}}, false)
	return output, err == nil
}

// Node warns for every live registered worker without a matching pid/start pin.
// Avoid process probes: even a possibly stale unpinned entry hands over. Linked
// worktrees and uncertain registry data also hand over to Node's repo discovery.
func b268WarningsAbsent(record ledger.Object) bool {
	if record.Get("status") == "ended" {
		return true
	}
	pins, ok := record.Get("workers").([]any)
	if !ok {
		if pin, ok := record.Get("worker").(ledger.Object); ok {
			pins = []any{pin}
		}
	}
	if len(pins) == 0 {
		return false
	}
	workspace, ok := record.Get("workspace").(string)
	if !ok || !filepath.IsAbs(workspace) {
		return false
	}
	git, err := os.Stat(filepath.Join(workspace, ".git"))
	if err != nil || !git.IsDir() {
		return false
	}
	dir := filepath.Join(workspace, ".code-forge", "workers")
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return true
	}
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		worker, decodeErr := ledger.DecodeObject(data)
		if err != nil || decodeErr != nil {
			return false
		}
		if worker.Get("run") != record.Get("run_id") {
			continue
		}
		start, ok := worker.Get("start_time").(string)
		if !ok {
			return false
		}
		pid, ok := worker.Get("pid").(float64)
		if !ok || pid <= 1 {
			return false
		}
		pinned := false
		for _, value := range pins {
			pin, _ := value.(ledger.Object)
			at, ok := pin.Get("started_at").(string)
			if ok && pin.Get("pid") == pid && strings.Join(strings.Fields(at), " ") == strings.Join(strings.Fields(start), " ") {
				pinned = true
			}
		}
		if !pinned {
			return false
		}
	}
	return true
}

// Restore daemonStatus's insertion order using Node-compatible scalar encoding.
// Missing or unusual fields are a reason to let Node render its own answer.
func b268OrderedStatus(s map[string]any) (ledger.Object, bool) {
	keys := []string{"run_id", "status", "mode", "spent_usd", "unknown_usd_sessions", "budget_usd", "blocks", "more", "closed", "stopped"}
	result := ledger.Object{}
	for _, key := range keys {
		value, exists := s[key]
		if !exists {
			return nil, false
		}
		switch key {
		case "run_id", "mode":
			if _, ok := value.(string); !ok {
				return nil, false
			}
		case "status":
			if _, ok := value.(string); value != nil && !ok {
				return nil, false
			}
		case "spent_usd", "unknown_usd_sessions", "budget_usd":
			if _, ok := value.(float64); value != nil && !ok {
				return nil, false
			}
		case "more", "closed", "stopped":
			if _, ok := value.(float64); !ok {
				return nil, false
			}
		}
		result.Set(key, value)
	}
	blocks, ok := s["blocks"].([]any)
	if !ok {
		return nil, false
	}
	ordered := []any{}
	for _, value := range blocks {
		block, ok := value.(map[string]any)
		if !ok {
			return nil, false
		}
		b := ledger.Object{}
		for _, key := range []string{"id", "state", "level", "model", "gate", "findings"} {
			v, exists := block[key]
			if !exists {
				return nil, false
			}
			switch key {
			case "id", "state":
				if _, ok := v.(string); !ok {
					return nil, false
				}
			case "level", "model":
				if _, ok := v.(string); v != nil && !ok {
					return nil, false
				}
			case "findings":
				if _, ok := v.(float64); !ok {
					return nil, false
				}
			}
			b.Set(key, v)
		}
		gate, ok := block["gate"].(map[string]any)
		if !ok {
			return nil, false
		}
		if _, ok := gate["result"].(string); !ok {
			return nil, false
		}
		name, exists := gate["name"]
		if _, ok := name.(string); !exists || name != nil && !ok {
			return nil, false
		}
		b.Set("gate", ledger.Object{{Key: "result", Value: gate["result"]}, {Key: "name", Value: gate["name"]}})
		ordered = append(ordered, b)
	}
	result.Set("blocks", ordered)
	if awaiting, ok := s["awaiting_human_check"]; ok && awaiting != nil && awaiting != false {
		if !b268Strings(awaiting) {
			return nil, false
		}
		result.Set("awaiting_human_check", awaiting)
	}
	if flaky, ok := s["flaky"]; ok && flaky != nil {
		files, ok := flaky.([]any)
		if !ok || !b268Strings(files) {
			return nil, false
		}
		if len(files) > 0 {
			result.Set("flaky", files)
		}
	}
	return result, true
}

func b268Strings(value any) bool {
	values, ok := value.([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	return true
}
