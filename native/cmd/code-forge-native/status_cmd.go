package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/daemonclient"
	"github.com/ricardov03/code-forge/native/ledger"
)

func init() {
	daemonclient.Version = version
	routes["status"] = statusCommand
}

func statusCommand(args []string) int {
	// B877: --help (or -h) anywhere prints the usage on stdout and exits 0;
	// the router adds the Description and Options around it.
	for _, arg := range args {
		if metaIsHelp(arg) {
			fmt.Fprint(safeStdout, statusUsage)
			return 0
		}
	}
	if code, ok := directStatus(args); ok {
		return code
	}
	// Only the cases Go cannot match byte for byte (a block id outside
	// printable ASCII, a ledger number Go cannot read) are refused (B690a).
	return refuseUnported(append([]string{"status"}, args...))
}

// Shared with the review-wait routes: honor and verify pinned config snapshots.
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
