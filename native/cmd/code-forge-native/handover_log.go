package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type handoverRow struct {
	At      string `json:"at"`
	Verb    string `json:"verb"`
	Sub     string `json:"sub"`
	Reason  string `json:"reason"`
	Version string `json:"version"`
}

func handoverFlagReason(prefix, flag string, fallback handoverReason) handoverReason {
	if flag == "-h" {
		flag = "--help"
	}
	code := prefix + "-" + strings.TrimLeft(flag, "-")
	for i, known := range handoverReasons {
		if code == known {
			return handoverReason(i)
		}
	}
	return fallback
}

func defaultHandoverReason(args []string) handoverReason {
	// The status route predates reason-aware wrappers and has one fallback.
	if len(args) > 0 && args[0] == "status" {
		return hStatusUnsupported
	}
	return hUnrouted
}

func handoverLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".code-forge", "native", "handovers.jsonl")
}

// Only public verbs from routes and src/cli/*.mjs, and their own known
// subcommands, may be recorded. Other tokens can be private argument values;
// store "unknown" for them, or an empty field when the argument is absent.
var handoverCommands = map[string]string{
	"author":        "",
	"autopilot":     "start status stop ask waive round level approve binnacle log",
	"block":         "open attempt rebase claim ready close land stop waive reopen accept-hit unwaive ack-waivers human-pass amend-acceptance",
	"ci":            "watch",
	"cloud-coder":   "",
	"context":       "pack",
	"decisions":     "list",
	"docs":          "sync path",
	"doctor":        "",
	"facts":         "",
	"gates":         "detect run secret-scan safe-edit scope acceptance transcript-grep",
	"heal":          "list show run",
	"help":          "",
	"init":          "",
	"jev":           "ask",
	"keys":          "list set test remove",
	"land":          "",
	"ledger":        "calibration outcome tail add outside-review",
	"list":          "",
	"logs":          "summary clear path report handovers",
	"models":        "",
	"native-status": "",
	"outcome":       "seal check record",
	"pipeline":      "status next",
	"plan":          "check",
	"proof":         "tier export lock unlock restore red-green baseline",
	"queue":         "status run",
	"remove":        "",
	"report":        "",
	"resolve":       "",
	"review-file":   "",
	"review":        "",
	"run":           "start status reload end",
	"s2":            "",
	"setup":         "",
	"spawn":         "",
	"status":        "",
	"sweep":         "",
	"tools":         "install",
	"upgrade":       "",
	"validate":      "",
	"version":       "",
	"worker":        "",
}

func handoverCommand(args []string) (string, string) {
	if len(args) == 0 {
		return "", ""
	}
	verb := "unknown"
	subs, known := handoverCommands[args[0]]
	if known {
		verb = args[0]
	}
	if len(args) == 1 {
		return verb, ""
	}
	for _, sub := range strings.Fields(subs) {
		if args[1] == sub {
			return verb, sub
		}
	}
	return verb, "unknown"
}

// Best effort only: no diagnostics, no changes to process inputs. A separate
// inode protects append AND rename, so concurrent trimming cannot lose a row.
func recordHandover(args []string, reason string) {
	if os.Getenv("CODE_FORGE_HANDOVER_LOG") == "0" || os.Getenv("CODE_FORGE_NATIVE") == "0" {
		return
	}
	file := handoverLogPath()
	if file == "" {
		return
	}
	dir := filepath.Dir(file)
	if os.MkdirAll(dir, 0700) != nil {
		return
	}
	lock, err := os.OpenFile(file+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return
	}
	defer lock.Close()
	// Bound contention: an unavailable logger must never hold up a command.
	deadline := time.Now().Add(100 * time.Millisecond)
	for syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		if !time.Now().Before(deadline) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	verb, sub := handoverCommand(args)
	data, err := json.Marshal(handoverRow{time.Now().UTC().Format(time.RFC3339Nano), verb, sub, reason, version})
	if err != nil {
		return
	}
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	if f.Chmod(0600) != nil {
		_ = f.Close()
		return
	}
	_, err = f.Write(append(data, '\n'))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return
	}
	all, err := os.ReadFile(file)
	if err != nil {
		return
	}
	lines := bytes.Split(bytes.TrimSuffix(all, []byte("\n")), []byte("\n"))
	if len(lines) <= 5000 {
		return
	}
	tmp, err := os.CreateTemp(dir, "handovers.tmp-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(append(bytes.Join(lines[len(lines)-4000:], []byte("\n")), '\n'))
	closeErr = tmp.Close()
	if err == nil && closeErr == nil {
		_ = os.Rename(tmp.Name(), file)
	}
}

type handoverCount struct {
	Verb   string `json:"verb"`
	Sub    string `json:"sub"`
	Reason string `json:"reason"`
	Count  int    `json:"count"`
	LastAt string `json:"last_at"`
}

func runHandoverLogs(args []string) int {
	usage := func(message string) int {
		fmt.Fprintln(os.Stderr, "logs handovers: "+message)
		fmt.Fprintln(os.Stderr, "usage: code-forge logs handovers [--json] [--since <ISO date>]")
		return 2
	}
	asJSON, seenSince := false, false
	var since time.Time
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			if asJSON {
				return usage("--json given more than once")
			}
			asJSON = true
		case "--since":
			if seenSince {
				return usage("--since given more than once")
			}
			seenSince = true
			i++
			if i == len(args) {
				return usage("--since requires an ISO date")
			}
			var err error
			since, err = time.Parse(time.RFC3339Nano, args[i])
			if err != nil {
				since, err = time.Parse("2006-01-02", args[i])
			}
			if err != nil {
				return usage("--since requires a valid ISO date")
			}
		default:
			return usage("unexpected argument; use --json or --since <ISO date>")
		}
	}
	groups := map[[3]string]*handoverCount{}
	total := 0
	data, err := os.ReadFile(handoverLogPath())
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "logs handovers: cannot read hand-over log")
		return 1
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		var row handoverRow
		if json.Unmarshal(line, &row) != nil {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, row.At)
		if err != nil || at.Before(since) {
			continue
		}
		key := [3]string{row.Verb, row.Sub, row.Reason}
		group := groups[key]
		if group == nil {
			group = &handoverCount{Verb: row.Verb, Sub: row.Sub, Reason: row.Reason}
			groups[key] = group
		}
		group.Count++
		last, _ := time.Parse(time.RFC3339Nano, group.LastAt)
		if at.After(last) {
			group.LastAt = row.At
		}
		total++
	}
	rows := []handoverCount{}
	for _, group := range groups {
		rows = append(rows, *group)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Verb != b.Verb {
			return a.Verb < b.Verb
		}
		if a.Sub != b.Sub {
			return a.Sub < b.Sub
		}
		return a.Reason < b.Reason
	})
	if asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(struct {
			Total int             `json:"total"`
			Rows  []handoverCount `json:"rows"`
		}{total, rows})
	} else if total == 0 {
		fmt.Fprintln(os.Stdout, "no hand-overs recorded")
	} else {
		fmt.Fprintln(os.Stdout, "count  verb  sub  reason")
		for _, row := range rows {
			fmt.Fprintf(os.Stdout, "%d  %s  %s  %s\n", row.Count, row.Verb, row.Sub, row.Reason)
		}
		fmt.Fprintf(os.Stdout, "total  %d\n", total)
	}
	return 0
}
