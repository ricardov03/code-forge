// code-forge-native is an optional, prebuilt accelerator for the Node CLI.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/ricardov03/code-forge/native/daemon"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
)

// Set from package.json by scripts/build-native.mjs.
var version = "dev"

type failure struct {
	Index  int    `json:"index"`
	Event  string `json:"event"`
	Reason string `json:"reason"`
}

type verification struct {
	Rows int       `json:"rows"`
	OK   bool      `json:"ok"`
	Bad  []failure `json:"bad"`
}

func main() {
	code, err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	os.Exit(code)
}

func run(args []string) (int, error) {
	if len(args) == 1 && args[0] == "version" {
		return 0, json.NewEncoder(os.Stdout).Encode(map[string]string{"version": version, "goVersion": runtime.Version()})
	}
	if len(args) == 0 || (args[0] != "verify-ledger" && args[0] != "serve") {
		return 2, fmt.Errorf("usage: code-forge-native version | verify-ledger | serve --home <dir> --run <id>")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	home := flags.String("home", "", "home directory containing .code-forge")
	runID := flags.String("run", "", "run ID")
	var idle *time.Duration
	if args[0] == "serve" {
		idle = flags.Duration("idle", 30*time.Minute, "exit after this duration without requests")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return 2, err
	}
	if *home == "" || !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`).MatchString(*runID) || flags.NArg() != 0 {
		return 2, fmt.Errorf("%s requires --home <dir> and a valid --run <id>", args[0])
	}
	if args[0] == "serve" {
		if *idle <= 0 {
			return 2, fmt.Errorf("idle duration must be positive")
		}
		if err := daemon.Serve(*home, *runID, version, *idle); err != nil {
			return 1, err
		}
		return 0, nil
	}
	result, err := verifyLedger(*home, *runID)
	if err != nil {
		return 1, err
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		return 1, err
	}
	if !result.OK {
		return 1, nil
	}
	return 0, nil
}

func verifyLedger(home, runID string) (verification, error) {
	result := verification{OK: true, Bad: []failure{}}
	runs := filepath.Join(home, ".code-forge", "runs")
	keyPath := filepath.Join(runs, runID+".key")
	// Match src/state/signer.mjs loadKey: private mode and exactly 32 raw bytes.
	info, err := os.Stat(keyPath)
	if err != nil {
		return result, fmt.Errorf("load run key: %w", err)
	}
	if info.Mode().Perm()&0077 != 0 {
		return result, fmt.Errorf("run signer key is accessible to other users; expected mode 600")
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return result, fmt.Errorf("load run key: %w", err)
	}
	if len(key) != 32 {
		return result, fmt.Errorf("run signer key is not 32 bytes")
	}
	data, err := os.ReadFile(filepath.Join(runs, runID+".json"))
	if err != nil {
		return result, fmt.Errorf("read run record: %w", err)
	}
	var record struct {
		Project string `json:"project"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return result, fmt.Errorf("read run record: %w", err)
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), record.Project)
	if err != nil {
		return result, err
	}
	for _, value := range rows {
		row, ok := value.(map[string]any)
		if !ok || row["run"] != runID {
			continue
		}
		checked, err := sign.VerifyRow(row, key)
		if err != nil {
			return result, err
		}
		if !checked.OK {
			event := "undefined"
			if v, exists := row["event"]; exists {
				event = fmt.Sprint(v)
			}
			result.Bad = append(result.Bad, failure{Index: result.Rows, Event: event, Reason: checked.Reason})
		}
		result.Rows++
	}
	result.OK = len(result.Bad) == 0
	return result, nil
}
