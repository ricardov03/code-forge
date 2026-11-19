package main

// B884: why a worker stopped is kept. The launcher (run start, --add-worker,
// doctor's probe) points the worker's stderr at
// <repo>/.code-forge/logs/worker-<pid>.log instead of /dev/null; at its exit
// the worker cuts that file to its last 50 lines. The serving worker writes
// one signed worker.exit {reason, detail} row (worker_serve.go) and block
// ready / close print the newest one on their worker.down line.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/worker"
)

// wexitLogLines is how many stderr lines a worker log keeps.
const wexitLogLines = 50

// wexitLogKeep is how many worker logs a repo keeps (the newest).
const wexitLogKeep = 20

// wexitLogDir is the repo's worker log folder.
func wexitLogDir(repoRoot string) string {
	return filepath.Join(repoRoot, ".code-forge", "logs")
}

// wexitLogPath is the stderr log of worker pid.
func wexitLogPath(repoRoot string, pid int) string {
	return filepath.Join(wexitLogDir(repoRoot), fmt.Sprintf("worker-%d.log", pid))
}

// wexitLaunchLog opens the stderr file for a worker about to start; nil when
// it cannot (the worker's stderr is then /dev/null, as before).
func wexitLaunchLog(repoRoot string) *os.File {
	dir := wexitLogDir(repoRoot)
	if os.MkdirAll(dir, 0o700) != nil {
		return nil
	}
	f, err := os.CreateTemp(dir, "worker-start-*.log")
	if err != nil {
		return nil
	}
	return f
}

// wexitLaunched names the log after the started worker's pid (pid 0: the
// start failed, the file goes) and trims old worker logs.
func wexitLaunched(f *os.File, repoRoot string, pid int) {
	if f == nil {
		return
	}
	_ = f.Close()
	if pid == 0 {
		_ = os.Remove(f.Name())
		return
	}
	_ = os.Rename(f.Name(), wexitLogPath(repoRoot, pid))
	wexitPrune(repoRoot)
}

// wexitPrune keeps the newest wexitLogKeep worker-<pid>.log files.
func wexitPrune(repoRoot string) {
	entries, err := os.ReadDir(wexitLogDir(repoRoot))
	if err != nil {
		return
	}
	type logFile struct {
		name string
		mod  int64
	}
	logs := []logFile{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "worker-") || !strings.HasSuffix(name, ".log") || strings.HasPrefix(name, "worker-start-") {
			continue
		}
		if info, err := e.Info(); err == nil {
			logs = append(logs, logFile{name, info.ModTime().UnixNano()})
		}
	}
	if len(logs) <= wexitLogKeep {
		return
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].mod > logs[j].mod })
	for _, l := range logs[wexitLogKeep:] {
		_ = os.Remove(filepath.Join(wexitLogDir(repoRoot), l.name))
	}
}

// wexitTrim cuts file to its last wexitLogLines lines (a missing file: the
// worker ran in the foreground, nothing to keep).
func wexitTrim(file string) {
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n"))
	if len(lines) <= wexitLogLines {
		return
	}
	kept := append(bytes.Join(lines[len(lines)-wexitLogLines:], []byte("\n")), '\n')
	tmp := file + ".trim"
	if os.WriteFile(tmp, kept, 0o600) == nil {
		_ = os.Rename(tmp, file)
	}
}

// wexitDownDetail adds the newest signed worker.exit row of run to a
// worker.down blocker's detail, so the line says why the worker stopped.
func wexitDownDetail(rows []gatecheck.Row, run, event, detail string) string {
	if event != "worker.down" {
		return detail
	}
	for i := len(rows) - 1; i >= 0; i-- {
		row := rows[i]
		if row["event"] != worker.ExitEvent || (row["run"] != nil && row["run"] != run) {
			continue
		}
		reason, _ := row["reason"].(string)
		why, _ := row["detail"].(string)
		text := detail + "; last worker exit: " + reason
		if why != "" {
			text += " (" + why + ")"
		}
		if ts, ok := row["ts"].(string); ok && ts != "" {
			text += " at " + ts
		}
		return text
	}
	return detail
}

// wexitUnsupportedMark starts the note an unreviewed blocker gets when the
// Go worker closed the file's ticket as unsupported (B884).
const wexitUnsupportedMark = "; the Go worker could not review it: "

// wexitUnsupportedNote adds, to each unreviewed blocker of block id, the
// reason of the newest signed review.unsupported row of that file in run, so
// block ready / close say why the file has no review.
func wexitUnsupportedNote(blockers []gatecheck.Blocker, rows []gatecheck.Row, run, id string) {
	for i, b := range blockers {
		if b.Code != "unreviewed" || b.File == "" || strings.Contains(b.Detail, wexitUnsupportedMark) {
			continue
		}
		for j := len(rows) - 1; j >= 0; j-- {
			row := rows[j]
			if row["event"] != worker.UnsupportedEvent || row["run"] != run || row["block"] != id || row["file"] != b.File {
				continue
			}
			reason, _ := row["reason"].(string)
			blockers[i].Detail += wexitUnsupportedMark + reason
			break
		}
	}
}

// wexitUnsupportedFix is the fix line of an unreviewed blocker that carries
// the unsupported note: a fresh review once the cause is fixed, else the
// owner's ruling.
func wexitUnsupportedFix(in blockerFixInput) string {
	return closeCommand([]string{"code-forge", "review-file", in.p.File, "--block", in.id, "--run", in.run}) +
		" once the cause is fixed; otherwise the owner waives the file (code-forge block waive) or rules on it (--decide)"
}
