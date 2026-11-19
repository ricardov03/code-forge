package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/procguard"
	"github.com/ricardov03/code-forge/native/state"
)

// B885, the customer's case (Laravel + Pest, 0.25.1): after the worker died,
// `run status` said worker_pinned: true while `block ready` said worker.down.
// Now run status, forge status and block ready judge the pins with one probe.

// b885Stop stops the run's live workers and waits until none is left.
func b885Stop(t *testing.T, run string) {
	t.Helper()
	for _, pid := range b884Workers(run) {
		_ = syscall.Kill(pid, syscall.SIGTERM)
	}
	for deadline := time.Now().Add(30 * time.Second); len(b884Workers(run)) > 0; {
		if time.Now().After(deadline) {
			t.Fatal("the worker did not stop")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// b885RunStatus is `run status --run <r>`: the decoded JSON and the stderr.
func b885RunStatus(t *testing.T, f b884Fix, run string) (map[string]any, string) {
	t.Helper()
	code, out, errText := f.sh(t, "code-forge run status --run "+run)
	var doc map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &doc) != nil {
		t.Fatalf("run status = %d\n%s\n%s", code, out, errText)
	}
	return doc, errText
}

// b885Pins is the alive flags of the run status workers list.
func b885Pins(t *testing.T, doc map[string]any) []bool {
	t.Helper()
	list, ok := doc["workers"].([]any)
	if !ok {
		t.Fatalf("workers %v", doc["workers"])
	}
	alive := []bool{}
	for _, p := range list {
		pin, _ := p.(map[string]any)
		a, isBool := pin["alive"].(bool)
		if !isBool || pin["pid"] == nil || pin["started_at"] == nil {
			t.Fatalf("pin %v", p)
		}
		alive = append(alive, a)
	}
	return alive
}

func TestB885CustomerRunStatusAgreesWithBlockReady(t *testing.T) {
	binary := b553Binary(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := b884Fix{dir: dir, home: filepath.Join(dir, "home"), repo: filepath.Join(dir, "repo"), bin: filepath.Join(dir, "bin"), tmp: filepath.Join(dir, "tmp")}
	for _, d := range []string{f.home, f.repo, f.bin, f.tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.bin, "claude"), []byte(b884Claude), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.bin, "code-forge"), []byte("#!/bin/sh\nexec '"+binary+"' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.env = []string{"PATH=" + f.bin + ":/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + f.home, "TMPDIR=" + f.tmp, "CODE_FORGE_TMP_ROOT=" + f.tmp,
		"CODE_FORGE_KEY_BACKEND=file", "CODE_FORGE_KEY_JEV=", "CODE_FORGE_NODE=" + filepath.Join(dir, "missing-node"), "CODE_FORGE_HANDOVER_LOG=1",
		"GIT_CONFIG_GLOBAL=/dev/null", procguard.Env + "=" + os.Getenv(procguard.Env)}
	run := ""
	t.Cleanup(func() {
		if run == "" {
			return
		}
		for _, pid := range b884Workers(run) {
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
		deadline := time.Now().Add(30 * time.Second)
		for len(b884Workers(run)) > 0 && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		for _, pid := range b884Workers(run) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Errorf("worker %d left", pid)
		}
	})
	gitc := "git -c user.name=t -c user.email=t@t -c commit.gpgsign=false"
	f.must(t, "git init -q -b main && echo hello > a.txt && "+gitc+" add -A && "+gitc+" commit -q -m base")
	if err := os.WriteFile(filepath.Join(f.repo, ".code-forge.yml"), []byte(b884Config), 0o644); err != nil {
		t.Fatal(err)
	}
	f.must(t, gitc+" add -A && "+gitc+" commit -q -m cfg")
	out := f.must(t, "code-forge run start 2>&1")
	m := regexp.MustCompile(`run (\S+) started`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("run start: %s", out)
	}
	run = m[1]
	if err := os.WriteFile(filepath.Join(dir, "acc.json"), []byte(`[{"clause":"a","tests":["t1"]}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	f.must(t, "code-forge block open B1 --run "+run+" --level L1 --owned a.txt --acceptance "+filepath.Join(dir, "acc.json"))
	fixLine := "run " + run + ": no live worker; fix: code-forge run start --add-worker --run " + run + "\n"

	// alive checks the live case: run status, forge status and block ready
	// all say the worker runs, and a pin is alive exactly when its pid is a
	// live worker of the run.
	alive := func(stage string) {
		t.Helper()
		doc, errText := b885RunStatus(t, f, run)
		pins := b885Pins(t, doc)
		if doc["worker_live"] != true || doc["worker_pinned"] != true || strings.Contains(errText, "no live worker") || len(pins) == 0 {
			t.Fatalf("%s: run status %v stderr %q", stage, doc, errText)
		}
		live := map[float64]bool{}
		for _, pid := range b884Workers(run) {
			live[float64(pid)] = true
		}
		for i, p := range doc["workers"].([]any) {
			pid, _ := p.(map[string]any)["pid"].(float64)
			if pins[i] != live[pid] {
				t.Fatalf("%s: pin %v alive %v, live workers %v", stage, p, pins[i], live)
			}
		}
		_, _, statusErr := f.sh(t, "code-forge status --run "+run)
		_, ready, readyErr := f.sh(t, "code-forge block ready B1 --run "+run)
		if strings.Contains(statusErr, "no live worker") || strings.Contains(ready+readyErr, "worker.down") {
			t.Fatalf("%s: status stderr %q, block ready:\n%s%s", stage, statusErr, ready, readyErr)
		}
	}
	alive("after run start")

	// The customer's sequence: the first worker is gone, a worker is added
	// from a shell that exits at once, then that worker exits too.
	b885Stop(t, run)
	f.must(t, "bash -c 'code-forge run start --add-worker --run "+run+"'")
	if got := b884Workers(run); len(got) != 1 {
		t.Fatalf("workers after --add-worker: %v", got)
	}
	alive("after --add-worker")
	b885Stop(t, run)

	doc, errText := b885RunStatus(t, f, run)
	pins := b885Pins(t, doc)
	if doc["worker_live"] != false || doc["worker_pinned"] != true || len(pins) < 2 || strings.Contains(fmt.Sprint(pins), "true") || errText != fixLine {
		t.Fatalf("run status with no live worker: %v\nstderr %q", doc, errText)
	}
	_, ready, readyErr := f.sh(t, "code-forge block ready B1 --run "+run)
	if text := ready + readyErr; !strings.Contains(text, "worker.down") || !strings.Contains(text, "no pinned worker is running") ||
		!strings.Contains(text, "fix: code-forge run start --add-worker --run "+run) {
		t.Fatalf("block ready disagrees:\n%s", text)
	}
	code, statusOut, statusErr := f.sh(t, "code-forge status --run "+run)
	if code != 0 || statusErr != strings.TrimSuffix(fixLine, "\n")+"\n" || !strings.HasPrefix(statusOut, "run "+run+" ") {
		t.Fatalf("status = %d %q %q", code, statusOut, statusErr)
	}
	code, statusOut, statusErr = f.sh(t, "code-forge status --run "+run+" --json")
	if code != 0 || strings.Count(statusOut, "\n") != 1 || !json.Valid([]byte(statusOut)) || statusErr != fixLine {
		t.Fatalf("status --json = %d %q %q", code, statusOut, statusErr)
	}

	// Adding a worker again brings all three back to alive.
	f.must(t, "bash -c 'code-forge run start --add-worker --run "+run+"'")
	alive("after a second --add-worker")
}

// Doctor reports the same probe: one WARN run-worker row for an active run
// whose pins are all gone; none for a live pin, an ended run or a run with
// no pin.
func TestB885DoctorRunWorkerRows(t *testing.T) {
	home := t.TempDir()
	save := func(id, status string, pins ...ledger.Object) {
		r := ledger.Object{{Key: "run_id", Value: id}, {Key: "workspace", Value: t.TempDir()}, {Key: "project", Value: "b885"}, {Key: "status", Value: status}, {Key: "blocks", Value: ledger.Object{}}}
		if len(pins) > 0 {
			r.Set("worker", pins[len(pins)-1])
			list := []any{}
			for _, p := range pins {
				list = append(list, p)
			}
			r.Set("workers", list)
		}
		if err := state.SaveRun(home, r); err != nil {
			t.Fatal(err)
		}
	}
	self := float64(os.Getpid())
	live := ledger.Object{{Key: "pid", Value: self}, {Key: "started_at", Value: statusStart(self)}}
	gone := ledger.Object{{Key: "pid", Value: self}, {Key: "started_at", Value: "Thu Jan  1 00:00:00 1970"}}
	save("r-down", "active", gone, gone)
	save("r-live", "active", gone, live)
	save("r-ended", "ended", gone)
	save("r-none", "active")
	rows := rwlDoctorRows(home)
	if len(rows) != 1 || rows[0] != (doctorRow{"run-worker", "WARN", "run worker", "run r-down: no live worker; fix: code-forge run start --add-worker --run r-down"}) {
		t.Fatalf("doctor rows %v", rows)
	}
}
