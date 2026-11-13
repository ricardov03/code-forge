// code-forge-native routes public CLI verbs and hosts internal accelerators.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/daemon"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/rebase"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
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

// main calls run (router.go), which sends `internal <verb>` to runInternal,
// runs ported verbs from the route table, and hands every other verb to Node.
// runGuarded (panic_guard.go) turns a panic into a hand-over or one error line.
func main() {
	childregInstall(os.Args[1:])
	code, err := runGuarded(os.Args[1:])
	childregRelease()
	if err != nil {
		fmt.Fprintln(os.Stderr, "code-forge-native:", err)
	}
	os.Exit(code)
}

func runInternal(args []string) (int, error) {
	if len(args) == 1 && args[0] == "kit-golden" {
		if err := kitGolden(os.Stdin, os.Stdout); err != nil {
			return 1, err
		}
		return 0, nil
	}
	if len(args) == 1 && args[0] == "version" {
		return 0, json.NewEncoder(os.Stdout).Encode(map[string]string{"version": version, "goVersion": runtime.Version()})
	}
	if len(args) == 0 || (args[0] != "verify-ledger" && args[0] != "serve" && args[0] != "state-golden" && args[0] != "block-rebase-landed") {
		return 2, fmt.Errorf("usage: code-forge-native internal version | verify-ledger | serve | state-golden | block-rebase-landed --home <dir> --run <id> --block <id> --json")
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	home := flags.String("home", "", "home directory containing .code-forge")
	runID := flags.String("run", "", "run ID")
	var block *string
	if args[0] == "block-rebase-landed" {
		block = flags.String("block", "", "open worktree block ID")
		flags.Bool("json", false, "print JSON result")
	}
	var op *string
	if args[0] == "state-golden" {
		op = flags.String("op", "", "test operation: append, save, lock")
	}
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
	if args[0] == "block-rebase-landed" {
		if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`).MatchString(*block) {
			return 2, fmt.Errorf("block-rebase-landed requires a valid --block <id>")
		}
		result, err := rebase.Run(*home, *runID, *block)
		if err != nil {
			return 1, err
		}
		return 0, json.NewEncoder(os.Stdout).Encode(result)
	}
	if args[0] == "state-golden" {
		if err := stateGolden(*home, *runID, *op); err != nil {
			return 1, err
		}
		return 0, nil
	}
	if args[0] == "serve" {
		if *idle <= 0 {
			return 2, fmt.Errorf("idle duration must be positive")
		}
		// B602: SIGTERM and SIGINT stay handled until the process exits, so a
		// second signal (the worker's daemon stop after a stop of its process
		// group) cannot kill the daemon before main releases its lazy run root.
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-signals
			cancel()
		}()
		if err := daemon.ServeContext(ctx, *home, *runID, version, *idle); err != nil {
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

// stateGolden is a hidden compatibility-test driver, not a product command.
// save merges stdin properties into the authoritative record under the lock;
// append consumes {slug, rows, rotateAtBytes?, signed?}; lock holds until EOF.
func stateGolden(home, runID, op string) error {
	if op == "lock" {
		return state.WithRunLock(home, runID, func() error {
			if _, err := fmt.Fprintln(os.Stdout, "locked"); err != nil {
				return err
			}
			_, err := io.Copy(io.Discard, os.Stdin)
			return err
		})
	}
	if op != "save" && op != "append" {
		return fmt.Errorf("state-golden requires --op append|save|lock")
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	input, err := ledger.DecodeObject(data)
	if err != nil {
		return err
	}
	if op == "save" {
		return state.UpdateRun(home, runID, func(record *ledger.Object) error {
			for _, field := range input {
				record.Set(field.Key, field.Value)
			}
			return nil
		})
	}
	slug, _ := input.Get("slug").(string)
	limit := ledger.DefaultRotateAtBytes
	if value := input.Get("rotateAtBytes"); value != nil {
		number, ok := value.(float64)
		if !ok || number <= 0 || number > float64(int64(1<<63-1)) {
			return fmt.Errorf("invalid rotateAtBytes")
		}
		limit = int64(number)
	}
	rows, ok := input.Get("rows").([]any)
	if !ok {
		return fmt.Errorf("append requires rows array")
	}
	for _, value := range rows {
		row, ok := value.(ledger.Object)
		if !ok {
			return fmt.Errorf("appendRow: row must be a plain object")
		}
		if input.Get("signed") == true {
			_, err = state.WriteSigned(home, runID, row)
		} else {
			_, err = ledger.AppendObject(filepath.Join(home, ".code-forge", "ledger"), slug, row, limit)
		}
		if err != nil {
			return err
		}
	}
	return nil
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
