package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/filelock"
	"github.com/ricardov03/code-forge/native/sign"
)

// runVerbCall runs `code-forge run <args>` in process and returns its exit
// code and the bytes it wrote. A missing Node makes any hand-over exit 127.
func runVerbCall(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	stdout, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	beforeOut, beforeErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdout, stderr
	code, runErr := run(append([]string{"run"}, args...))
	os.Stdout, os.Stderr = beforeOut, beforeErr
	_ = stdout.Close()
	_ = stderr.Close()
	out, _ := os.ReadFile(stdout.Name())
	errText, _ := os.ReadFile(stderr.Name())
	if runErr != nil {
		errText = append(errText, []byte("handover: "+runErr.Error())...)
	}
	return code, string(out), string(errText)
}

type runFixture struct {
	home, workspace string
	key             []byte
}

func newRunFixture(t *testing.T) runFixture {
	t.Helper()
	f := runFixture{home: t.TempDir(), workspace: t.TempDir(), key: bytes.Repeat([]byte{7}, 32)}
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_NODE", filepath.Join(f.home, "missing-node"))
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	if err := os.MkdirAll(filepath.Join(f.home, ".code-forge", "runs"), 0700); err != nil {
		t.Fatal(err)
	}
	return f
}

// record writes a run record whose extra fields follow the fixed head.
func (f runFixture) record(t *testing.T, id, status, extra string) {
	t.Helper()
	ws, _ := json.Marshal(f.workspace)
	body := fmt.Sprintf(`{"run_id":%q,"project":"unit","workspace":%s,"status":%q,"engine":"claude"%s}`, id, ws, status, extra)
	dir := filepath.Join(f.home, ".code-forge", "runs")
	if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".key"), f.key, 0600); err != nil {
		t.Fatal(err)
	}
}

func (f runFixture) read(t *testing.T, id string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", id+".json"))
	var out map[string]any
	if err != nil || json.Unmarshal(data, &out) != nil {
		t.Fatalf("read record %s: %v %s", id, err, data)
	}
	return out
}

// rows returns the signed ledger rows, each verified with the run key.
func (f runFixture) rows(t *testing.T) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "ledger", "unit.jsonl"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	out := []map[string]any{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var row map[string]any
		if err := json.Unmarshal(line, &row); err != nil {
			t.Fatal(err)
		}
		if verified, err := sign.VerifyRow(row, f.key); err != nil || !verified.OK {
			t.Fatalf("row signature: %s %#v %v", line, verified, err)
		}
		out = append(out, row)
	}
	return out
}

func startSleeper(t *testing.T) (*exec.Cmd, string) {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	started := filelock.ProcessStartTime(cmd.Process.Pid)
	if started == nil {
		t.Fatal("no start time for the sleeper")
	}
	return cmd, *started
}

func TestRunVerbHelpAndUsage(t *testing.T) {
	newRunFixture(t)
	for _, tc := range []struct {
		args         string
		code         int
		stdout, errs string
	}{
		{"--help", 0, runUsage, ""},
		{"-h", 0, runUsage, ""},
		{"start|--help", 0, runSubUsage["start"], ""},
		{"status|-h", 0, runSubUsage["status"], ""},
		{"reload|--help", 0, runSubUsage["reload"], ""},
		{"end|-h", 0, runSubUsage["end"], ""},
		{"", 2, "", runUsage},
		{"bogus", 2, "", runUsage},
		{"bogus|--help", 2, "", runUsage},
		{"status", 2, "", runUsage},
		{"end|positional", 2, "", runUsage},
		{"reload", 2, "", runUsage},
		{"status|--nope", 2, "", "run status: unknown flag \"--nope\"\n"},
		{"status|--run|a|--run|b", 2, "", "run status: --run given more than once\n"},
		{"start|--reattach=yes", 2, "", "run start: --reattach takes no value\n"},
		{"status|--run", 2, "", "run status: --run needs a value\n"},
		{"status|--run|--cwd|x", 2, "", "run status: --run needs a value\n"},
		{"status|--run=", 2, "", "run status: --run needs a value\n"},
		{"start|--worker-pid|0", 2, "", "run start: --worker-pid must be a positive integer\n"},
		{"start|--worker-pid|012", 2, "", "run start: --worker-pid must be a positive integer\n"},
		{"start|--worker-pid|99999999999999999", 2, "", "run start: --worker-pid must be a positive integer\n"},
		{"status|--run|r|--mode|plan", 2, "", "run status: --mode is set once, by a new run start (the mode is fixed for the run)\n"},
		{"start|--reattach|--run|r|--mode|plan", 2, "", "run start: --mode is set once, by a new run start (the mode is fixed for the run)\n"},
		{"start|--mode|fast", 2, "", "run start: --mode must be one of plan, brief\n"},
		{"start|--add-worker", 2, "", "run start: run start --add-worker needs --run and cannot use --reattach or --worker-pid\n"},
		{"start|--add-worker|--run|r|--worker-pid|5", 2, "", "run start: run start --add-worker needs --run and cannot use --reattach or --worker-pid\n"},
		{"end|--add-worker|--run|r", 2, "", "run end: run start --add-worker needs --run and cannot use --reattach or --worker-pid\n"},
		{"start|--reattach", 2, "", "run start: run start --reattach needs --run\n"},
	} {
		t.Run(tc.args, func(t *testing.T) {
			args := []string{}
			if tc.args != "" {
				args = strings.Split(tc.args, "|")
			}
			code, out, errs := runVerbCall(t, args...)
			if code != tc.code || out != tc.stdout || errs != tc.errs {
				t.Fatalf("run %v = %d stdout %q stderr %q, want %d %q %q", args, code, out, errs, tc.code, tc.stdout, tc.errs)
			}
		})
	}
}

func TestRunVerbStatus(t *testing.T) {
	f := newRunFixture(t)
	f.record(t, "r-test", "active", `,"worker":{"pid":4242,"started_at":"x"},"blocks":{"B2":{"status":"awaiting_human_check"},"B1":{"status":"awaiting_human_check","owned_files":[]},"B3":{"status":"open"}},"orphans":["keep"]`)
	if err := os.WriteFile(filepath.Join(f.workspace, ".code-forge.yml"), []byte("version: 1\nbudget:\n  usd: 12\n"), 0644); err != nil {
		t.Fatal(err)
	}
	ledgerDir := filepath.Join(f.home, ".code-forge", "ledger")
	if err := os.MkdirAll(ledgerDir, 0700); err != nil {
		t.Fatal(err)
	}
	rows := strings.Join([]string{
		`{"run":"r-test","event":"session","usd":0.5}`,
		`{"run":"r-test","event":"session.background","usd":0.25}`,
		`{"run":"r-test","event":"session","usd_unknown":true}`,
		`{"run":"r-test","event":"session","usd":7,"source":"shadow"}`,
		`{"run":"r-other","event":"session","usd":9}`,
		`{"run":"r-test","event":"block.claim","usd":3}`,
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(ledgerDir, "unit.jsonl"), []byte(rows), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runVerbCall(t, "status", "--run", "r-test")
	want := `{
  "run_id": "r-test",
  "status": "active",
  "engine": "claude",
  "worker_pinned": true,
  "blocks": {
    "B2": {
      "status": "awaiting_human_check"
    },
    "B1": {
      "status": "awaiting_human_check",
      "owned_files": []
    },
    "B3": {
      "status": "open"
    }
  },
  "orphans": [
    "keep"
  ],
  "spent_usd": 0.75,
  "budget_usd": 12,
  "unknown_usd_sessions": 1,
  "awaiting_human_check": [
    "B1",
    "B2"
  ]
}
`
	if code != 0 || out != want || errs != "1 session has no known price; the budget does not count it\n" {
		t.Fatalf("status = %d\n%s\nstderr %q", code, out, errs)
	}

	// No config, no ledger, no worker, no awaiting: budget null and absent keys stay absent.
	f2 := newRunFixture(t)
	ws, _ := json.Marshal(f2.workspace)
	body := fmt.Sprintf(`{"run_id":"r-two","workspace":%s,"project":"unit","status":"ended","blocks":{}}`, ws)
	if err := os.WriteFile(filepath.Join(f2.home, ".code-forge", "runs", "r-two.json"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, errs = runVerbCall(t, "status", "--run", "r-two")
	want = "{\n  \"run_id\": \"r-two\",\n  \"status\": \"ended\",\n  \"worker_pinned\": false,\n  \"blocks\": {},\n  \"spent_usd\": 0,\n  \"budget_usd\": null,\n  \"unknown_usd_sessions\": 0\n}\n"
	if code != 0 || out != want || errs != "" {
		t.Fatalf("bare status = %d %q %q", code, out, errs)
	}

	for _, tc := range []struct{ id, errs string }{
		{"r-none", "run status: no run record for r-none\n"},
		{"Bad!", "run status: run id must match /^[a-z0-9][a-z0-9-]{0,63}$/\n"},
	} {
		code, out, errs = runVerbCall(t, "status", "--run", tc.id)
		if code != 1 || out != "" || errs != tc.errs {
			t.Fatalf("status %s = %d %q %q", tc.id, code, out, errs)
		}
	}
}

func TestRunVerbEnd(t *testing.T) {
	t.Run("stops the verified worker and ends the run", func(t *testing.T) {
		f := newRunFixture(t)
		sleeper, started := startSleeper(t)
		pid := sleeper.Process.Pid
		f.record(t, "r-test", "active", fmt.Sprintf(`,"worker":{"pid":%d,"started_at":%q},"blocks":{"B1":{"status":"done"},"B2":{"status":"stopped"}}`, pid, started))
		code, out, errs := runVerbCall(t, "end", "--run", "r-test")
		if code != 0 || out != "run r-test ended\n" || errs != "" {
			t.Fatalf("end = %d %q %q", code, out, errs)
		}
		state, _ := sleeper.Process.Wait()
		if status, ok := state.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGTERM {
			t.Fatalf("the pinned worker was not stopped with SIGTERM: %v", state)
		}
		record := f.read(t, "r-test")
		ended, _ := record["ended_at"].(string)
		if record["status"] != "ended" || len(ended) != 24 || !strings.HasSuffix(ended, "Z") {
			t.Fatalf("record not ended: %#v", record)
		}
		if _, err := time.Parse("2006-01-02T15:04:05.000Z", ended); err != nil {
			t.Fatalf("ended_at %q: %v", ended, err)
		}
		mirror, err := os.ReadFile(filepath.Join(f.workspace, ".code-forge", "runs", "r-test.json"))
		if err != nil || bytes.Contains(mirror, []byte(`"worker"`)) || !bytes.Contains(mirror, []byte(`"status": "ended"`)) {
			t.Fatalf("mirror: %v %s", err, mirror)
		}
		rows := f.rows(t)
		if len(rows) != 1 || rows[0]["event"] != "run.end" || rows[0]["worker_check"] != "ok" || rows[0]["worker_stopped"] != true || rows[0]["run"] != "r-test" {
			t.Fatalf("rows: %#v", rows)
		}
	})
	t.Run("failed pin checks are written before run.end", func(t *testing.T) {
		killed := []int{}
		before := runKill
		runKill = func(pid int) error { killed = append(killed, pid); return nil }
		t.Cleanup(func() { runKill = before })
		self := os.Getpid()
		for _, tc := range []struct{ name, extra, event, reason string }{
			{"none", ``, "worker.down", "no worker is pinned for this run"},
			{"bad pid", `,"worker":{"pid":1,"started_at":"x"}`, "worker.replaced", "the pinned worker pid is not an integer > 1"},
			{"string pin", `,"worker":"x"`, "worker.replaced", "the pinned worker pid is not an integer > 1"},
			{"restarted", fmt.Sprintf(`,"worker":{"pid":%d,"started_at":"Mon Jan  1 00:00:00 2001"}`, self), "worker.replaced", fmt.Sprintf("pid %d was restarted (start time differs from the pin)", self)},
			{"many down", `,"workers":[{"pid":2147483000,"started_at":"a"},{"pid":2147483001,"started_at":"b"}]`, "worker.down", "no pinned worker is running"},
			{"many bad", `,"workers":[{"pid":5,"started_at":"a"},{"pid":0.5,"started_at":"b"}]`, "worker.replaced", "the live worker does not match a pinned worker"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				f := newRunFixture(t)
				f.record(t, "r-test", "active", tc.extra+`,"blocks":{}`)
				code, out, errs := runVerbCall(t, "end", "--run", "r-test")
				if code != 0 || out != "run r-test ended\n" || errs != "" {
					t.Fatalf("end = %d %q %q", code, out, errs)
				}
				rows := f.rows(t)
				if len(rows) != 2 || rows[0]["event"] != tc.event || rows[0]["reason"] != tc.reason || rows[1]["event"] != "run.end" || rows[1]["worker_check"] != tc.event || rows[1]["worker_stopped"] != false {
					t.Fatalf("rows: %#v", rows)
				}
			})
		}
		t.Run("dead pin", func(t *testing.T) {
			f := newRunFixture(t)
			beforeProbe := runProbe
			runProbe = func(float64) *string { return nil }
			t.Cleanup(func() { runProbe = beforeProbe })
			f.record(t, "r-test", "active", `,"worker":{"pid":4242,"started_at":"x"},"blocks":{}`)
			if code, _, errs := runVerbCall(t, "end", "--run", "r-test"); code != 0 {
				t.Fatalf("end = %d %q", code, errs)
			}
			rows := f.rows(t)
			if len(rows) != 2 || rows[0]["reason"] != "pinned worker pid 4242 is not running" {
				t.Fatalf("rows: %#v", rows)
			}
		})
		if len(killed) != 0 {
			t.Fatalf("an unverified pin was signalled: %v", killed)
		}
	})
	t.Run("refusals change nothing", func(t *testing.T) {
		for _, tc := range []struct{ status, blocks, errs string }{
			{"ended", `{}`, "run end: run r-test has already ended\n"},
			{"active", `{"B2":{"status":"awaiting_human_check"},"B1":{"status":"awaiting_human_check"},"B3":{"status":"open"}}`, "run end: run r-test has blocks awaiting human check: B2, B1; human-pass or stop them first\n"},
			{"active", `{"B3":{"status":"open"},"B1":{"status":"done"},"B0":{"status":"open"}}`, "run end: run r-test still has open blocks: B3, B0\n"},
		} {
			f := newRunFixture(t)
			f.record(t, "r-test", tc.status, `,"blocks":`+tc.blocks)
			beforeRecord, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", "r-test.json"))
			code, out, errs := runVerbCall(t, "end", "--run", "r-test")
			afterRecord, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", "r-test.json"))
			if code != 1 || out != "" || errs != tc.errs || !bytes.Equal(beforeRecord, afterRecord) || len(f.rows(t)) != 0 {
				t.Fatalf("end refusal = %d %q %q", code, out, errs)
			}
			if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "runs", "r-test.lock")); !os.IsNotExist(err) {
				t.Fatalf("run lock left behind: %v", err)
			}
		}
		f := newRunFixture(t)
		for _, tc := range []struct{ id, errs string }{
			{"r-none", "run end: no run record for r-none\n"},
			{"Bad!", "run end: run id must match /^[a-z0-9][a-z0-9-]{0,63}$/\n"},
		} {
			code, out, errs := runVerbCall(t, "end", "--run", tc.id)
			if code != 1 || out != "" || errs != tc.errs {
				t.Fatalf("end %s = %d %q %q", tc.id, code, out, errs)
			}
		}
		_ = f
	})
}

func TestRunVerbReattach(t *testing.T) {
	t.Run("with --worker-pid", func(t *testing.T) {
		f := newRunFixture(t)
		sleeper, started := startSleeper(t)
		pid := sleeper.Process.Pid
		f.record(t, "r-test", "active", `,"worker":{"pid":4242,"started_at":"old"},"worker_history":[{"pid":11,"started_at":"older"}],"blocks":{}`)
		code, out, errs := runVerbCall(t, "start", "--reattach", "--run", "r-test", "--worker-pid", strconv.Itoa(pid))
		if code != 0 || out != fmt.Sprintf("run r-test: worker re-pinned to pid %d\n", pid) || errs != "" {
			t.Fatalf("reattach = %d %q %q", code, out, errs)
		}
		record := f.read(t, "r-test")
		pin := map[string]any{"pid": float64(pid), "started_at": started}
		history := record["worker_history"].([]any)
		workers := record["workers"].([]any)
		if fmt.Sprint(record["worker"]) != fmt.Sprint(pin) || len(workers) != 1 || fmt.Sprint(workers[0]) != fmt.Sprint(pin) || len(history) != 2 || history[1].(map[string]any)["pid"] != float64(4242) {
			t.Fatalf("record: %#v", record)
		}
		data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "runs", "r-test.json"))
		if !bytes.Contains(data, []byte(`"worker_history": [`)) || bytes.Index(data, []byte(`"worker_history"`)) > bytes.Index(data, []byte(`"workers"`)) {
			t.Fatalf("key order: %s", data)
		}
		rows := f.rows(t)
		if len(rows) != 1 || rows[0]["event"] != "worker.reattach" || rows[0]["pid"] != float64(pid) {
			t.Fatalf("rows: %#v", rows)
		}
	})
	t.Run("refusals", func(t *testing.T) {
		f := newRunFixture(t)
		f.record(t, "r-test", "active", `,"blocks":{}`)
		f.record(t, "r-done", "ended", `,"blocks":{}`)
		beforeProbe := runProbe
		runProbe = func(float64) *string { return nil }
		t.Cleanup(func() { runProbe = beforeProbe })
		for _, tc := range []struct {
			args []string
			errs string
		}{
			{[]string{"--run", "r-test", "--worker-pid", "1"}, "run start: worker pid must be an integer > 1\n"},
			{[]string{"--run", "r-test", "--worker-pid", "4242"}, "run start: worker pid 4242 is not running\n"},
			{[]string{"--run", "r-done", "--worker-pid", "4242"}, "run start: run r-done has ended\n"},
			{[]string{"--run", "r-none", "--worker-pid", "4242"}, "run start: no run record for r-none\n"},
			{[]string{"--run", "r-none"}, "run start: no run record for r-none\n"},
		} {
			code, out, errs := runVerbCall(t, append([]string{"start", "--reattach"}, tc.args...)...)
			if code != 1 || out != "" || errs != tc.errs {
				t.Fatalf("reattach %v = %d %q %q", tc.args, code, out, errs)
			}
		}
		if len(f.rows(t)) != 0 {
			t.Fatal("a refusal wrote a ledger row")
		}
	})
	t.Run("without --worker-pid re-pins the live worker", func(t *testing.T) {
		f := newRunFixture(t)
		if out, err := exec.Command("git", "-C", f.workspace, "init", "-q").CombinedOutput(); err != nil {
			t.Fatalf("git init: %v %s", err, out)
		}
		f.record(t, "r-test", "active", `,"blocks":{}`)
		code, out, errs := runVerbCall(t, "start", "--reattach", "--run", "r-test")
		if code != 1 || out != "" || errs != "run start: no live worker serves this queue\n" {
			t.Fatalf("no worker = %d %q %q", code, out, errs)
		}
		sleeper, started := startSleeper(t)
		pid := sleeper.Process.Pid
		queue := filepath.Join(f.workspace, ".code-forge", "queue")
		if err := os.MkdirAll(queue, 0755); err != nil {
			t.Fatal(err)
		}
		announce := func(run string) {
			body := fmt.Sprintf(`{"pid":%d,"run":%q,"heartbeat_at":%q}`, pid, run, time.Now().UTC().Format(time.RFC3339Nano))
			if err := os.WriteFile(filepath.Join(queue, "worker.json"), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
		}
		announce("r-other")
		code, out, errs = runVerbCall(t, "start", "--reattach", "--run", "r-test")
		if code != 1 || out != "" || errs != "run start: the live worker serves run r-other\n" {
			t.Fatalf("other run = %d %q %q", code, out, errs)
		}
		announce("r-test")
		code, out, errs = runVerbCall(t, "start", "--reattach", "--run", "r-test")
		if code != 0 || out != fmt.Sprintf("run r-test: worker re-pinned to pid %d\n", pid) || errs != "" {
			t.Fatalf("repin = %d %q %q", code, out, errs)
		}
		record := f.read(t, "r-test")
		if worker := record["worker"].(map[string]any); worker["pid"] != float64(pid) || worker["started_at"] != started {
			t.Fatalf("record: %#v", record)
		}
		if rows := f.rows(t); len(rows) != 1 || rows[0]["event"] != "worker.reattach" {
			t.Fatalf("rows: %#v", rows)
		}

		plain := newRunFixture(t)
		plain.record(t, "r-test", "active", `,"blocks":{}`)
		code, out, errs = runVerbCall(t, "start", "--reattach", "--run", "r-test")
		if code != 1 || out != "" || errs != "run start: not inside a git repository\n" {
			t.Fatalf("no repo = %d %q %q", code, out, errs)
		}
	})
}

func TestRunVerbHandsOver(t *testing.T) {
	f := newRunFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	f.record(t, "r-test", "active", `,"blocks":{}`)
	broken := filepath.Join(f.home, ".code-forge", "runs", "r-bad.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	f.record(t, "r-shape", "active", `,"blocks":[1]`)
	cases := []struct {
		args   []string
		reason string
	}{
		{[]string{"start"}, "run-start"},
		{[]string{"start", "--run", "r-new", "--mode", "brief"}, "run-start"},
		{[]string{"start", "--add-worker", "--run", "r-test"}, "run-add-worker"},
		{[]string{"reload", "--run", "r-test"}, "run-reload"},
		{[]string{"status", "--run", "r-bad"}, "run-record"},
		{[]string{"end", "--run", "r-bad"}, "run-record"},
		{[]string{"status", "--run", "r-shape"}, "run-shape"},
		{[]string{"end", "--run", "r-shape"}, "run-shape"},
	}
	for _, tc := range cases {
		code, out, _ := runVerbCall(t, tc.args...)
		if code != 127 || out != "" {
			t.Fatalf("%v: hand-over exit = %d stdout %q, want 127", tc.args, code, out)
		}
	}
	data, err := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) != len(cases) {
		t.Fatalf("handover rows = %d, want %d", len(lines), len(cases))
	}
	for i, line := range lines {
		var row map[string]string
		if err := json.Unmarshal(line, &row); err != nil || row["verb"] != "run" || row["reason"] != cases[i].reason {
			t.Fatalf("row %d = %s, want reason %s", i, line, cases[i].reason)
		}
	}
	if rows := f.rows(t); len(rows) != 0 {
		t.Fatalf("a hand-over wrote rows: %#v", rows)
	}
	if record := f.read(t, "r-shape"); record["status"] != "active" {
		t.Fatalf("hand-over changed the record: %#v", record)
	}
}
