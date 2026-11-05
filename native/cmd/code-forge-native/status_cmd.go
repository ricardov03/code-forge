package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/daemonclient"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

func init() {
	daemonclient.Version = version
	routes["status"] = statusCommand
}

func statusCommand(args []string) int {
	if output, ok := nativeStatus(args); ok {
		_, err := os.Stdout.Write(append(output, '\n'))
		if err != nil {
			return 1
		}
		return 0
	}
	code, err := handover(append([]string{"status"}, args...))
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	return code
}

// Accept only the single-run JSON form. Usage and all other forms remain Node's.
func statusRun(args []string) (string, bool) {
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

func nativeStatus(args []string) ([]byte, bool) {
	run, ok := statusRun(args)
	if !ok || !daemonclient.Enabled(nil) {
		return nil, false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, false
	}
	record, err := state.ReadRun(home, run)
	if err != nil || record.Get("run_id") != run || !statusWarningsAbsent(record) {
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
	summary, ok := orderedStatus(runs[0].(map[string]any))
	if !ok {
		return nil, false
	}
	output, err := ledger.Stringify(ledger.Object{{Key: "runs", Value: []any{summary}}}, false)
	return output, err == nil
}

func statusConfig(record ledger.Object) (map[string]any, bool) {
	st, _ := record.Get("config").(ledger.Object)
	hash, snapshot := st.Get("hash").(string)
	if !snapshot {
		workspace, ok := record.Get("workspace").(string)
		if !ok {
			return nil, false
		}
		cfg, err := config.Load(workspace)
		return cfg, err == nil
	}
	snapshots, _ := st.Get("snapshots").(ledger.Object)
	// snapshotFor(record, '') honors a pin under the empty ticket key as well.
	if pins, ok := st.Get("pins").(ledger.Object); ok {
		if pin, ok := pins.Get("").(string); ok {
			hash = pin
		}
	}
	cfg, ok := snapshots.Get(hash).(ledger.Object)
	if !ok {
		return nil, false
	}
	canonical, err := canon.Marshal(cfg)
	if err != nil {
		return nil, false
	}
	digest := sha256.Sum256(canonical)
	if hex.EncodeToString(digest[:]) != hash {
		return nil, false
	}
	// Convert nested ordered objects for the reusable map-based client.
	data, err := json.Marshal(cfg)
	var mapping map[string]any
	if err != nil || json.Unmarshal(data, &mapping) != nil {
		return nil, false
	}
	return mapping, true
}

// Node warns for every live registered worker without a matching pid/start pin.
// Avoid process probes: even a possibly stale unpinned entry hands over. Linked
// worktrees and uncertain registry data also hand over to Node's repo discovery.
func statusWarningsAbsent(record ledger.Object) bool {
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
func orderedStatus(s map[string]any) (ledger.Object, bool) {
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
		if !statusStrings(awaiting) {
			return nil, false
		}
		result.Set("awaiting_human_check", awaiting)
	}
	if flaky, ok := s["flaky"]; ok && flaky != nil {
		files, ok := flaky.([]any)
		if !ok || !statusStrings(files) {
			return nil, false
		}
		if len(files) > 0 {
			result.Set("flaky", files)
		}
	}
	return result, true
}

func statusStrings(value any) bool {
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
