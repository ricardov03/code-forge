package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/config"
)

const b486Usage = "usage: code-forge sweep [--apply] [--json]\nDry run by default: list stale workers, dead-run registry children and marked orphans with RSS and total MB.\n--apply stops proven stale processes with SIGTERM, then SIGKILL after a grace period.\nZombies are reported by parent only; neither they nor their parents are signalled. Live engines have no memory limit.\n"

// b486Env: a temp HOME, a temp base for run roots, Node unreachable (so a
// hand-over shows as exit 127 with an error) and a process table that lists
// only the given fake rows. Nothing real is signalled by a dry run.
func b486Env(t *testing.T, rows []runProc, envs map[int]map[string]string) (home, base string) {
	t.Helper()
	home, base = t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b486-node")
	t.Setenv("CODE_FORGE_TMP_ROOT", base)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "")
	t.Setenv("CODE_FORGE_NATIVE", "")
	beforeRows, beforeEnv := runReadProcesses, runReadEnv
	runReadProcesses = func() []runProc { return append([]runProc{}, rows...) }
	runReadEnv = func(p runProc) map[string]string { return envs[p.pid] }
	t.Cleanup(func() { runReadProcesses, runReadEnv = beforeRows, beforeEnv })
	return home, base
}

// b486Call runs `code-forge sweep <args>` in Go; handed is the hand-over error.
func b486Call(t *testing.T, args ...string) (code int, out, errText string, handed error) {
	t.Helper()
	dir := t.TempDir()
	files := [2]*os.File{}
	for i := range files {
		file, err := os.CreateTemp(dir, "out-")
		if err != nil {
			t.Fatal(err)
		}
		files[i] = file
	}
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = files[0], files[1]
	code, handed = run(append([]string{"sweep"}, args...))
	os.Stdout, os.Stderr = stdout, stderr
	texts := [2]string{}
	for i, file := range files {
		_ = file.Close()
		data, _ := os.ReadFile(file.Name())
		texts[i] = string(data)
	}
	return code, texts[0], texts[1], handed
}

func b486Handovers(t *testing.T, home string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
	if err != nil {
		return nil
	}
	reasons := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var row map[string]any
		if json.Unmarshal([]byte(line), &row) == nil {
			reasons = append(reasons, fmt.Sprint(row["reason"]))
		}
	}
	return reasons
}

// TestB486SweepHelpAndUsage: help and every usage error answer in Go with
// Node's text, exit code, breadcrumb and errors.jsonl row.
func TestB486SweepHelpAndUsage(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"-h", "--bogus"}, {"--apply", "--help"}, {"--json", "--help", "--apply"}} {
		home, _ := b486Env(t, nil, nil)
		code, out, errText, handed := b486Call(t, args...)
		if handed != nil || code != 0 || out != helpExpect(t, append([]string{"sweep"}, args...), 0, b486Usage) || errText != "" {
			t.Fatalf("%v: code=%d out=%q err=%q handed=%v", args, code, out, errText, handed)
		}
		if got := b486Handovers(t, home); got != nil {
			t.Fatalf("%v handed over: %v", args, got)
		}
	}
	for _, args := range [][]string{{"--bogus"}, {"x"}, {"--json=1"}, {"--apply", "--apply"}, {"--apply", "-h"}, {"--help=yes"}} {
		home, _ := b486Env(t, nil, nil)
		code, out, errText, handed := b486Call(t, args...)
		if handed != nil || code != 2 || out != "" || errText != b486Usage {
			t.Fatalf("%v: code=%d out=%q err=%q handed=%v", args, code, out, errText, handed)
		}
		crumbs, _ := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "breadcrumbs.json"))
		if string(crumbs) != "[\"sweep\"]\n" {
			t.Fatalf("%v: breadcrumbs=%q", args, crumbs)
		}
		file, err := os.Open(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		scanner := bufio.NewScanner(file)
		var entry map[string]any
		if !scanner.Scan() || json.Unmarshal(scanner.Bytes(), &entry) != nil {
			t.Fatalf("%v: no error entry", args)
		}
		_ = file.Close()
		if entry["verb"] != "sweep" || entry["sub"] != nil || entry["exit"] != float64(2) || entry["kind"] != "usage" || entry["message"] != strings.TrimSpace(b486Usage) {
			t.Fatalf("%v: entry=%s", args, scanner.Bytes())
		}
	}
}

// TestB486SweepDryRunEmpty: nothing to sweep, text and --json.
func TestB486SweepDryRunEmpty(t *testing.T) {
	b486Env(t, []runProc{}, nil)
	code, out, errText, handed := b486Call(t)
	if handed != nil || code != 0 || errText != "" || out != "dry run: 0 processes, total 0.0 MB; 0 dead-run roots\n" {
		t.Fatalf("code=%d out=%q err=%q handed=%v", code, out, errText, handed)
	}
	code, out, errText, handed = b486Call(t, "--json")
	want := `{"apply":false,"items":[],"registryRoots":[],"zombies":[],"totalRssMb":0,"memory":{"codeForgeMb":0,"childrenMb":0,"orphansMb":0}}` + "\n"
	if handed != nil || code != 0 || errText != "" || out != want {
		t.Fatalf("code=%d out=%q err=%q handed=%v", code, out, errText, handed)
	}
}

const b486Start = "Wed Nov 11 10:00:00 2026"

func b486Record(t *testing.T, home, id, body string) {
	t.Helper()
	dir := filepath.Join(home, ".code-forge", "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func b486DeadRoot(t *testing.T, base, name string, children ...runProc) string {
	t.Helper()
	root := filepath.Join(base, name)
	if err := os.MkdirAll(filepath.Join(root, "pids"), 0o700); err != nil {
		t.Fatal(err)
	}
	owner := fmt.Sprintf(`{"pid":%d,"start_time":"unknown"}`, deadPid(t))
	if err := os.WriteFile(filepath.Join(root, "owner.json"), []byte(owner), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range children {
		entry := fmt.Sprintf(`{"pid":%d,"start_time":%q}`, c.pid, c.start)
		if err := os.WriteFile(filepath.Join(root, "pids", strconv.Itoa(c.pid)+".json"), []byte(entry), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestB486SweepDryRunReport: workers of ended or unknown runs, an orphan of
// a dead owner, zombies by parent, a dead run root's child and the memory
// totals, in Node's order and format (text and --json).
func TestB486SweepDryRunReport(t *testing.T) {
	worker := func(run string) string { return "node /x/bin/code-forge.mjs worker --run " + run }
	forge := runProc{uid: os.Getuid(), pid: 900010, ppid: 4242, state: "S", start: b486Start, command: "node /x/bin/code-forge.mjs run status", rss: 2048}
	rows := []runProc{
		{uid: os.Getuid(), pid: 900001, ppid: 4242, state: "S", start: b486Start, command: worker("r-old"), rss: 1536},
		{uid: os.Getuid(), pid: 900002, ppid: 4242, state: "S", start: b486Start, command: worker("r-none"), rss: 512},
		{uid: os.Getuid(), pid: 900003, ppid: 4242, state: "S", start: b486Start, command: worker("r-live"), rss: 100},
		{uid: os.Getuid(), pid: 900004, ppid: 4242, state: "S", start: b486Start, command: worker("r-old"), rss: 100},
		{uid: os.Getuid(), pid: 900005, ppid: 1, state: "S", start: b486Start, command: "sh -c long-task", rss: 2560},
		{uid: os.Getuid(), pid: 900006, ppid: 1, state: "S", start: b486Start, command: "sh -c owned-task", rss: 4096},
		{uid: os.Getuid() + 1, pid: 900007, ppid: 1, state: "S", start: b486Start, command: worker("r-old"), rss: 4096},
		forge,
		{uid: os.Getuid(), pid: 900011, ppid: 900010, state: "S", start: b486Start, command: "git status", rss: 1024},
		{uid: os.Getuid(), pid: 900020, ppid: 900010, state: "Z", start: b486Start, command: "<defunct>"},
		{uid: os.Getuid(), pid: 900021, ppid: 900010, state: "Z+", start: b486Start, command: "<defunct>"},
		{uid: os.Getuid(), pid: 900022, ppid: 900099, state: "Z", start: b486Start, command: "<defunct>"},
		{uid: os.Getuid(), pid: 900030, ppid: 4242, state: "S", start: b486Start, command: "sh -c registered", rss: 307},
	}
	homeEnv := map[string]string{}
	envs := map[int]map[string]string{
		900001: homeEnv, 900002: homeEnv, 900003: homeEnv,
		900004: {"HOME": "/elsewhere"},
		900005: {"CODE_FORGE_OWNER_PID": strconv.Itoa(deadPid(t)), "CODE_FORGE_RUN_ID": "r9"},
		900006: {"CODE_FORGE_OWNER_PID": strconv.Itoa(os.Getpid())},
	}
	home, base := b486Env(t, rows, envs)
	homeEnv["HOME"] = home
	workspace := t.TempDir()
	b486Record(t, home, "r-old", `{"run_id":"r-old","status":"ended"}`)
	b486Record(t, home, "r-live", fmt.Sprintf(`{"run_id":"r-live","status":"active","workspace":%q}`, workspace))
	root := b486DeadRoot(t, base, "r-dead", rows[12])
	// a live run's root is not swept
	if err := os.MkdirAll(filepath.Join(base, "r-alive"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "r-alive", "owner.json"), []byte(fmt.Sprintf(`{"pid":%d,"start_time":"unknown"}`, os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errText, handed := b486Call(t)
	want := "would stop worker pid 900001 1.5 MB node /x/bin/code-forge.mjs worker --run r-old\n" +
		"would stop worker pid 900002 0.5 MB node /x/bin/code-forge.mjs worker --run r-none\n" +
		"would stop orphan pid 900005 2.5 MB sh -c long-task\n" +
		"would stop registry pid 900030 0.3 MB sh -c registered\n" +
		"2 zombies (parent 900010 node /x/bin/code-forge.mjs run status) — report only\n" +
		"1 zombies (parent 900099 (unreadable)) — report only\n" +
		"dry run: 4 processes, total 4.8 MB; 1 dead-run roots\n"
	if handed != nil || code != 0 || errText != "" || out != want {
		t.Fatalf("code=%d err=%q handed=%v out:\n%s", code, errText, handed, out)
	}
	code, out, errText, handed = b486Call(t, "--json")
	item := func(pid, ppid int, rss, command, kind, extra string) string {
		return fmt.Sprintf(`{"uid":%d,"pid":%d,"ppid":%d,"state":"S","rssMb":%s,"start_time":%q,"command":%q,"kind":%q%s}`, os.Getuid(), pid, ppid, rss, b486Start, command, kind, extra)
	}
	wantJSON := `{"apply":false,"items":[` +
		item(900001, 4242, "1.5", worker("r-old"), "worker", `,"runId":"r-old"`) + "," +
		item(900002, 4242, "0.5", worker("r-none"), "worker", `,"runId":"r-none"`) + "," +
		item(900005, 1, "2.5", "sh -c long-task", "orphan", `,"runId":"r9"`) + "," +
		item(900030, 4242, "0.2998046875", "sh -c registered", "registry", "") +
		`],"registryRoots":[` + strconv.Quote(root) + `],"zombies":[` +
		`{"ppid":900010,"command":"node /x/bin/code-forge.mjs run status","count":2,"pids":[900020,900021]},` +
		`{"ppid":900099,"command":"(unreadable)","count":1,"pids":[900022]}],` +
		`"totalRssMb":4.7998046875,"memory":{"codeForgeMb":4.1953125,"childrenMb":1,"orphansMb":2.5}}` + "\n"
	if handed != nil || code != 0 || errText != "" || out != wantJSON {
		t.Fatalf("code=%d err=%q handed=%v\n got: %s\nwant: %s", code, errText, handed, out, wantJSON)
	}
	if got := b486Handovers(t, home); got != nil {
		t.Fatalf("handed over: %v", got)
	}
}

// b486Repo: a git repo with one commit, an active run whose config snapshot
// sets worker.idle_exit_hours to 1, and its worker in the process table.
func b486Repo(t *testing.T, extra string) (home, repo string) {
	t.Helper()
	worker := "node /x/bin/code-forge.mjs worker --run r-idle"
	rows := []runProc{{uid: os.Getuid(), pid: 900040, ppid: 4242, state: "S", start: b486Start, command: worker, rss: 1024}}
	env := map[string]string{}
	home, _ = b486Env(t, rows, map[int]map[string]string{900040: env})
	env["HOME"] = home
	repo = t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "one"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{"worker": map[string]any{"idle_exit_hours": 1.0}}
	hash := config.Hash(cfg)
	b486Record(t, home, "r-idle", fmt.Sprintf(`{"run_id":"r-idle","status":"active","workspace":%q,"project":"unit","started_at":"2026-01-01T00:00:00.000Z","config":{"hash":%q,"snapshots":{%q:{"worker":{"idle_exit_hours":1}}}},"blocks":{"B1":{"status":"open"}}%s}`, repo, hash, hash, extra))
	return home, repo
}

func b486Clock(t *testing.T, offset time.Duration) {
	t.Helper()
	before := sweepNowMs
	at := float64(time.Now().Add(offset).UnixNano()) / 1e6
	sweepNowMs = func() float64 { return at }
	t.Cleanup(func() { sweepNowMs = before })
}

// TestB486SweepIdleWorker: an active run's worker is swept only when the run
// has been idle longer than worker.idle_exit_hours (files, commits, ledger
// rows and pending queue tickets all count as activity).
func TestB486SweepIdleWorker(t *testing.T) {
	listed := "would stop worker pid 900040 1.0 MB node /x/bin/code-forge.mjs worker --run r-idle\ndry run: 1 processes, total 1.0 MB; 0 dead-run roots\n"
	kept := "dry run: 0 processes, total 0.0 MB; 0 dead-run roots\n"
	t.Run("idle past the limit", func(t *testing.T) {
		b486Repo(t, "")
		b486Clock(t, 3*time.Hour)
		if code, out, errText, handed := b486Call(t); handed != nil || code != 0 || errText != "" || out != listed {
			t.Fatalf("code=%d out=%q err=%q handed=%v", code, out, errText, handed)
		}
	})
	t.Run("recent file activity", func(t *testing.T) {
		b486Repo(t, "")
		b486Clock(t, 10*time.Minute)
		if code, out, _, handed := b486Call(t); handed != nil || code != 0 || out != kept {
			t.Fatalf("code=%d out=%q handed=%v", code, out, handed)
		}
	})
	t.Run("recent ledger row", func(t *testing.T) {
		home, _ := b486Repo(t, "")
		b486Clock(t, 3*time.Hour)
		dir := filepath.Join(home, ".code-forge", "ledger")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		ts := time.Now().Add(150 * time.Minute).UTC().Format("2006-01-02T15:04:05.000Z")
		if err := os.WriteFile(filepath.Join(dir, "unit.jsonl"), []byte(fmt.Sprintf(`{"run":"r-idle","ts":%q}`+"\n", ts)), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, out, _, handed := b486Call(t); handed != nil || code != 0 || out != kept {
			t.Fatalf("code=%d out=%q handed=%v", code, out, handed)
		}
	})
	t.Run("pending queue ticket", func(t *testing.T) {
		_, repo := b486Repo(t, "")
		b486Clock(t, 3*time.Hour)
		queue := filepath.Join(repo, ".code-forge", "queue")
		if err := os.MkdirAll(queue, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(queue, "0123456789abcdef01234567.json"), []byte(`{"run":"r-idle"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if code, out, _, handed := b486Call(t); handed != nil || code != 0 || out != kept {
			t.Fatalf("code=%d out=%q handed=%v", code, out, handed)
		}
		// a done ticket is no activity
		if err := os.WriteFile(filepath.Join(queue, "0123456789abcdef01234567.done"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if code, out, _, handed := b486Call(t); handed != nil || code != 0 || out != listed {
			t.Fatalf("code=%d out=%q handed=%v", code, out, handed)
		}
	})
	t.Run("snapshot that does not match its hash keeps the worker", func(t *testing.T) {
		home, repo := b486Repo(t, "")
		b486Clock(t, 3*time.Hour)
		b486Record(t, home, "r-idle", fmt.Sprintf(`{"run_id":"r-idle","status":"active","workspace":%q,"project":"unit","started_at":"2026-01-01T00:00:00.000Z","config":{"hash":"abc","snapshots":{"abc":{}}}}`, repo))
		if code, out, _, handed := b486Call(t); handed != nil || code != 0 || out != kept {
			t.Fatalf("code=%d out=%q handed=%v", code, out, handed)
		}
	})
}

// TestB486SweepApply: --apply stops the stale worker, the orphan and the
// dead root's child (children of this test only) and reports each stopped.
func TestB486SweepApply(t *testing.T) {
	home, base := b486Env(t, nil, nil)
	b486Record(t, home, "r-old", `{"run_id":"r-old","status":"ended"}`)
	w := &sweepWorld{}
	worker := w.spawn(t, false, 4242, "node /x/bin/code-forge.mjs worker --run r-old", map[string]string{"HOME": home})
	orphan := w.spawn(t, true, 1, "sh -c long-task", map[string]string{"CODE_FORGE_OWNER_PID": strconv.Itoa(deadPid(t))})
	registered := w.spawn(t, false, 4242, "sh -c registered", nil)
	live := w.spawn(t, false, 4242, "sh -c unrelated", nil)
	b486DeadRoot(t, base, "r-dead", runProc{pid: registered.pid, start: registered.start})
	w.install(t)
	code, out, errText, handed := b486Call(t, "--apply")
	want := fmt.Sprintf("stopped worker pid %d 0.0 MB %s\nstopped orphan pid %d 0.0 MB %s\nstopped registry pid %d 0.0 MB %s\nsweep: 3 processes, total 0.0 MB; 1 dead-run roots\n",
		worker.pid, worker.command, orphan.pid, orphan.command, registered.pid, registered.command)
	if handed != nil || code != 0 || errText != "" || out != want {
		t.Fatalf("code=%d err=%q handed=%v out:\n%s\nwant:\n%s", code, errText, handed, out, want)
	}
	for _, c := range []*sweepChild{worker, orphan, registered} {
		waitGone(t, c)
	}
	if !w.alive(live) {
		t.Fatal("an unrelated process was stopped")
	}
	for _, s := range w.sent() {
		if strings.Contains(s, strconv.Itoa(live.pid)) {
			t.Fatalf("signal to the unrelated process: %v", w.sent())
		}
	}
	if _, err := os.Stat(filepath.Join(base, "r-dead")); !os.IsNotExist(err) {
		t.Fatalf("dead root kept: %v", err)
	}
}

// TestB486SweepHandovers: the cases Go cannot answer as Node does keep a
// hand-over with their own reason and write nothing first.
func TestB486SweepHandovers(t *testing.T) {
	t.Run("HOME unset or relative", func(t *testing.T) {
		b486Env(t, nil, nil)
		t.Setenv("CODE_FORGE_HANDOVER_LOG", "0")
		for _, home := range []string{"", "rel"} {
			t.Setenv("HOME", home)
			if code, out, errText, _ := b486Call(t); !strings.Contains(errText, "/nonexistent-b486-node") || code != 127 || out != "" {
				t.Fatalf("HOME=%q: code=%d out=%q err=%q", home, code, out, errText)
			}
		}
	})
	t.Run("unreadable temp base", func(t *testing.T) {
		home, base := b486Env(t, []runProc{}, nil)
		if err := os.Chmod(base, 0o300); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(base, 0o700) })
		code, out, errText, _ := b486Call(t)
		if !strings.Contains(errText, "/nonexistent-b486-node") || code != 127 || out != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errText)
		}
		if got := fmt.Sprint(b486Handovers(t, home)); got != "[sweep-root-unreadable]" {
			t.Fatalf("reasons=%s", got)
		}
		if _, err := os.Stat(filepath.Join(home, ".code-forge", "logs", "breadcrumbs.json")); !os.IsNotExist(err) {
			t.Fatalf("breadcrumbs written before the hand-over: %v", err)
		}
	})
	t.Run("active run whose workspace is not a string", func(t *testing.T) {
		rows := []runProc{{uid: os.Getuid(), pid: 900050, ppid: 4242, state: "S", start: b486Start, command: "node /x/bin/code-forge.mjs worker --run r-odd"}}
		env := map[string]string{}
		home, _ := b486Env(t, rows, map[int]map[string]string{900050: env})
		env["HOME"] = home
		b486Record(t, home, "r-odd", `{"run_id":"r-odd","status":"running","workspace":7}`)
		code, out, errText, _ := b486Call(t, "--json")
		if !strings.Contains(errText, "/nonexistent-b486-node") || code != 127 || out != "" {
			t.Fatalf("code=%d out=%q err=%q", code, out, errText)
		}
		if got := fmt.Sprint(b486Handovers(t, home)); got != "[sweep-idle-unknown]" {
			t.Fatalf("reasons=%s", got)
		}
	})
}

// TestB486JSToFixed: Number.prototype.toFixed(1) rounds the exact binary
// value half away from zero.
func TestB486JSToFixed(t *testing.T) {
	for in, want := range map[float64]string{0: "0.0", 0.25: "0.3", 1.25: "1.3", 0.05: "0.1", 0.15: "0.1", 9.95: "9.9", 99.96: "100.0", 4.7998046875: "4.8", 1536.0 / 1024: "1.5", 0.09765625: "0.1"} {
		if got := jsToFixed1(in); got != want {
			t.Fatalf("toFixed(%v)=%q, want %q", in, got, want)
		}
	}
}

// Node's sweep output for usage and help (B471), now served by the B486 port.
func TestB471SweepUsageMatchesNode(t *testing.T) {
	smallProject(t, "")
	checkSmallCases(t, []smallCase{
		{[]string{"sweep", "--help"}, 0, sweepUsage, ""},
		{[]string{"sweep", "-h"}, 0, sweepUsage, ""},
		{[]string{"sweep", "--json", "--help"}, 0, sweepUsage, ""},
		{[]string{"sweep", "--bogus"}, 2, "", sweepUsage},
		{[]string{"sweep", "x"}, 2, "", sweepUsage},
		{[]string{"sweep", "--json", "--json"}, 2, "", sweepUsage},
		{[]string{"sweep", "--apply=1"}, 2, "", sweepUsage},
		{[]string{"sweep", "--apply", "-h"}, 2, "", sweepUsage},
	})
}
