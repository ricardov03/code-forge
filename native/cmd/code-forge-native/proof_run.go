package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B462: proof lock, unlock, restore and export --remove run in Go, as
// src/cli/proof.mjs with src/proof/lock.mjs, export.mjs and the journal of
// red-green.mjs: same run lock, record keys, signed proof rows and output.

var proofNow = time.Now

var proofBlockID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`)

// proofError is a refusal with Node's StateError code (usage exits 2).
type proofError struct{ code, message string }

func (e *proofError) Error() string { return e.message }

// proofFail turns an error into Node's `proof <sub>: <message>` refusal. An
// error Go cannot word as Node does hands the call over instead.
func proofFail(sub string, err error) (*proofOutcome, handoverReason) {
	var pe *proofError
	if errors.As(err, &pe) {
		code := 1
		if pe.code == "usage" {
			code = 2
		}
		return proofRefuse(sub, pe.message, code), 0
	}
	var se *state.Error
	if errors.As(err, &se) {
		return proofRefuse(sub, se.Message, 1), 0
	}
	return nil, hProofRun
}

func proofJSON(value any) string {
	out, _ := ledger.Stringify(value, false)
	return string(out) + "\n"
}

func proofExportDir(workspace, blockID string) (string, error) {
	if !proofBlockID.MatchString(blockID) {
		return "", &proofError{"bad-block-id", "block id must match /^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/"}
	}
	return filepath.Join(workspace, ".code-forge", "export", blockID), nil
}

func proofBlocks(record ledger.Object) ledger.Object {
	blocks, _ := record.Get("blocks").(ledger.Object)
	return blocks
}

func proofBlock(record ledger.Object, blockID string) ledger.Object {
	block, _ := proofBlocks(record).Get(blockID).(ledger.Object)
	return block
}

// proofRecord signs a proof row: the fields, then event, block, isolation.
func proofRecord(home, runID, blockID, isolation string, fields ledger.Object) error {
	row := append(ledger.Object{}, fields...)
	row = append(row, ledger.Field{Key: "event", Value: "proof"}, ledger.Field{Key: "block", Value: blockID}, ledger.Field{Key: "isolation", Value: isolation})
	_, err := state.WriteSigned(home, runID, row)
	return err
}

func proofPidAlive(pid any) bool {
	n, ok := pid.(float64)
	if !ok {
		if i, isInt := pid.(int); isInt {
			n, ok = float64(i), true
		}
	}
	if !ok || n != float64(int64(n)) || n <= 0 {
		return false
	}
	err := syscall.Kill(int(n), 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func proofIsInteger(value any) bool {
	switch v := value.(type) {
	case float64:
		return v == float64(int64(v))
	case int, int64:
		return true
	}
	return false
}

// proofRunSub serves lock, unlock, restore and export --remove.
func proofRunSub(sub, runID, blockID string, remove bool) (*proofOutcome, handoverReason) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, hProofRun
	}
	record, err := state.ReadRun(home, runID)
	if err != nil {
		return proofFail(sub, err)
	}
	workspace, _ := record.Get("workspace").(string)
	switch sub {
	case "restore":
		exportDir, err := proofExportDir(workspace, blockID)
		if err != nil {
			return proofFail(sub, err)
		}
		restored := []any{}
		for _, dir := range []string{exportDir, workspace} {
			journal, err := proofReadJournal(dir)
			if err != nil {
				return proofFail(sub, err)
			}
			if journal == nil {
				continue
			}
			files, err := proofRestoreJournal(dir)
			if err != nil {
				return proofFail(sub, err)
			}
			restored = append(restored, ledger.Object{{Key: "dir", Value: dir}, {Key: "files", Value: files}})
		}
		if len(restored) == 0 {
			return proofRefuse(sub, "no red→green journal for block "+blockID+" — nothing to restore", 1), 0
		}
		return &proofOutcome{stdout: proofJSON(ledger.Object{{Key: "restored", Value: restored}})}, 0
	case "lock":
		if block := proofBlock(record, blockID); block != nil {
			if block.Get("worktree") != nil {
				return proofRefuse(sub, "block "+blockID+" is proven in an export built from its tree; proof lock does not apply", 2), 0
			}
		}
		lock, err := proofAcquireLock(home, runID, blockID)
		if err != nil {
			return proofFail(sub, err)
		}
		return &proofOutcome{stdout: proofJSON(ledger.Object{{Key: "locked", Value: lock}})}, 0
	case "unlock":
		if err := proofReleaseLock(home, runID, blockID); err != nil {
			return proofFail(sub, err)
		}
		return &proofOutcome{stdout: proofJSON(ledger.Object{{Key: "unlocked", Value: blockID}})}, 0
	}
	// export
	if block := proofBlock(record, blockID); block == nil || block.Get("status") != "open" {
		return proofRefuse(sub, "block "+blockID+" is not open in run "+runID, 1), 0
	}
	if !remove {
		return proofExport(home, runID, blockID, record)
	}

	dir, err := proofExportDir(workspace, blockID)
	if err != nil {
		return proofFail(sub, err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, hProofRun
	}
	return &proofOutcome{stdout: proofJSON(ledger.Object{{Key: "removed", Value: blockID}})}, 0
}

func proofOpenBlock(record ledger.Object, runID, blockID string) error {
	if record.Get("status") != "active" {
		return &proofError{"run-ended", "run " + fmt.Sprint(record.Get("run_id")) + " has ended"}
	}
	if block := proofBlock(record, blockID); block == nil || block.Get("status") != "open" {
		return &proofError{"no-block", "block " + blockID + " is not open in run " + fmt.Sprint(record.Get("run_id"))}
	}
	return nil
}

func proofAcquireLock(home, runID, blockID string) (any, error) {
	var lock any
	err := state.WithRunLock(home, runID, func() error {
		record, err := state.ReadRun(home, runID)
		if err != nil {
			return err
		}
		if err := proofOpenBlock(record, runID, blockID); err != nil {
			return err
		}
		if held, ok := record.Get("proof_lock").(ledger.Object); ok {
			if held.Get("block") == blockID {
				lock = held
				return nil
			}
			return &proofError{"proof.busy", "proof.busy: block " + fmt.Sprint(held.Get("block")) + " holds the proof lock"}
		} else if pcJSTruthy(record.Get("proof_lock")) {
			return errors.New("proof_lock is not an object")
		}
		running := []string{}
		for _, field := range proofBlocks(record) {
			block, _ := field.Value.(ledger.Object)
			if field.Key == blockID || block == nil || block.Get("status") != "open" || block.Get("gate_running") != true {
				continue
			}
			if pid := block.Get("gate_pid"); !proofIsInteger(pid) || proofPidAlive(pid) {
				running = append(running, field.Key)
			}
		}
		if len(running) > 0 {
			return &proofError{"proof.busy", "proof.busy: gate running for block " + strings.Join(running, ", ")}
		}
		fresh := ledger.Object{{Key: "block", Value: blockID}, {Key: "acquired_at", Value: proofNow().UTC().Format("2006-01-02T15:04:05.000Z")}}
		if err := proofRecord(home, runID, blockID, "lock", ledger.Object{{Key: "step", Value: "lock"}}); err != nil {
			return err
		}
		record.Set("proof_lock", fresh)
		lock = fresh
		return state.SaveRun(home, record)
	})
	return lock, err
}

func proofReleaseLock(home, runID, blockID string) error {
	return state.WithRunLock(home, runID, func() error {
		record, err := state.ReadRun(home, runID)
		if err != nil {
			return err
		}
		value := record.Get("proof_lock")
		if !pcJSTruthy(value) {
			return &proofError{"not-locked", "run " + runID + " has no proof lock"}
		}
		held, ok := value.(ledger.Object)
		if !ok {
			return errors.New("proof_lock is not an object")
		}
		if held.Get("block") != blockID {
			return &proofError{"not-holder", fmt.Sprintf("block %s does not hold the proof lock (%v does)", blockID, proofJSText(held.Get("block")))}
		}
		if err := proofRecord(home, runID, blockID, "lock", ledger.Object{{Key: "step", Value: "unlock"}}); err != nil {
			return err
		}
		record.Delete("proof_lock")
		return state.SaveRun(home, record)
	})
}

// proofJSText is a value in a JS template string (strings as they are).
func proofJSText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case nil:
		return "null"
	}
	out, _ := json.Marshal(value)
	return string(out)
}

const proofJournalRel = ".code-forge/proof-journal.json"

type proofJournalEntry struct {
	Path    string `json:"path"`
	Existed bool   `json:"existed"`
	Sha256  string `json:"sha256"`
	Mode    uint32 `json:"mode"`
	Content string `json:"content"`
}

// proofRelPath is assertRelPath of red-green.mjs.
func proofRelPath(p, key string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.ContainsAny(p, "\\\x00") {
		return &proofError{"bad-path", key + ": every entry must be a repo-relative POSIX path"}
	}
	segments := strings.Split(p, "/")
	for _, s := range segments {
		if s == "" || s == "." || s == ".." {
			return &proofError{"bad-path", key + `: no ".", ".." or empty segments`}
		}
	}
	if segments[0] == ".code-forge" {
		return &proofError{"bad-path", key + ": nothing under .code-forge/"}
	}
	return nil
}

// proofReadJournal is readJournal: nil without a journal. A journal Go
// cannot parse as Node does (bad JSON, odd field types) is an untyped error,
// which hands the call over.
func proofReadJournal(dir string) ([]proofJournalEntry, error) {
	data, err := os.ReadFile(filepath.Join(dir, proofJournalRel))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var head struct {
		Version any   `json:"version"`
		Files   []any `json:"files"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, err
	}
	if head.Version != float64(1) || head.Files == nil {
		return nil, &proofError{"bad-journal", proofJournalRel + " is not a version 1 journal"}
	}
	entries := make([]proofJournalEntry, 0, len(head.Files))
	for _, raw := range head.Files {
		object, _ := raw.(map[string]any)
		p, _ := object["path"].(string)
		if err := proofRelPath(p, "journal"); err != nil {
			return nil, err
		}
		encoded, _ := json.Marshal(raw)
		var entry proofJournalEntry
		if err := json.Unmarshal(encoded, &entry); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func proofSha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// proofRestoreJournal is restoreJournal: put every journalled file back,
// check it, then drop the journal.
func proofRestoreJournal(dir string) ([]any, error) {
	entries, err := proofReadJournal(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		abs := filepath.Join(dir, entry.Path)
		if entry.Existed {
			content, err := base64.StdEncoding.DecodeString(entry.Content)
			if err != nil {
				return nil, err
			}
			if err := os.MkdirAll(filepath.Dir(abs), 0o777); err != nil {
				return nil, err
			}
			if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			if err := os.WriteFile(abs, content, fs.FileMode(entry.Mode)); err != nil {
				return nil, err
			}
			if err := os.Chmod(abs, fs.FileMode(entry.Mode)); err != nil {
				return nil, err
			}
		} else if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	bad := []string{}
	for _, entry := range entries {
		abs := filepath.Join(dir, entry.Path)
		info, err := os.Lstat(abs)
		if entry.Existed {
			data, readErr := os.ReadFile(abs)
			if err != nil || !info.Mode().IsRegular() || readErr != nil || proofSha(data) != entry.Sha256 {
				bad = append(bad, entry.Path)
			}
		} else if err == nil {
			bad = append(bad, entry.Path)
		}
	}
	if len(bad) > 0 {
		return nil, &proofError{"hash-mismatch", fmt.Sprintf("restore: %d file(s) do not match the journal (%s); the journal is kept", len(bad), strings.Join(bad, ", "))}
	}
	if err := os.Remove(filepath.Join(dir, proofJournalRel)); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	files := make([]any, len(entries))
	for i, entry := range entries {
		files[i] = entry.Path
	}
	return files, nil
}
