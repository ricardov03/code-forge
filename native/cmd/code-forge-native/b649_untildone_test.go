package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// B649: Go never hands a command to Node while a run it names holds a live
// until-done autopilot grant (Node's Date.parse(null) misreads it). The fake
// Node on CODE_FORGE_NODE records its argv: no file means Node never ran.

const b649UntilDone = `{"grant_id":"ap-done","status":"active","until":null,"until_done":true,"scopes":["waive:critical","close:routine"],"deny":[],"delegate":"L2","caps":{},"stop_at":0.9,"started_at":"2026-01-01T00:00:00.000Z","stopped_at":null,"expired_at":null,"link":null}`
const b649Timed = `{"grant_id":"ap-timed","status":"active","until":"2999-01-01T00:00:00.000Z","scopes":["waive:critical","close:routine"],"deny":[],"delegate":"L2","caps":{},"stop_at":0.9,"started_at":"2026-01-01T00:00:00.000Z","stopped_at":null,"expired_at":null,"link":null}`

func b649Child() {
	code, err := run(strings.Split(os.Getenv("B649_ARGS"), "|"))
	if err != nil {
		os.Stderr.WriteString(err.Error())
	}
	os.Exit(code)
}

type b649Result struct {
	code           int
	stdout, stderr string
	nodeArgv       []string // nil: Node never ran
}

// b649Run runs args through run() in a child whose HOME is home and whose
// Node is a recorder.
func b649Run(t *testing.T, test, home, dir, args string) b649Result {
	t.Helper()
	bin := t.TempDir()
	argvFile := filepath.Join(bin, "argv")
	fakeNode := filepath.Join(bin, "fake-node")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + argvFile + "'\necho fake-node-ran\nexit 0\n"
	if err := os.WriteFile(fakeNode, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^"+test+"$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+home, "B649_CHILD=1", "B649_ARGS="+args, "CODE_FORGE_NODE="+fakeNode, "CODE_FORGE_NO_ERROR_LOG=1",
		"BLOCK_HV_CHILD=", "CODE_FORGE_HANDOVER_LOG=")
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err := cmd.Run()
	code := 0
	if e, ok := err.(*exec.ExitError); ok {
		code = e.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	r := b649Result{code: code, stdout: out.String(), stderr: stderr.String()}
	if data, err := os.ReadFile(argvFile); err == nil {
		r.nodeArgv = strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	}
	return r
}

func b649Home(t *testing.T, grant string) string {
	t.Helper()
	extra := map[string]any{}
	f := newHVFixture(t, nil, extra)
	if grant != "" {
		b649SetGrant(t, f, grant)
	}
	return f.home
}

// b649SetGrant writes grant (raw JSON) into the fixture run record.
func b649SetGrant(t *testing.T, f *hvFixture, grant string) {
	t.Helper()
	file := filepath.Join(f.dir, "r-test.json")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	body := strings.TrimSuffix(strings.TrimSpace(string(data)), "}") + `,"autopilot":` + grant + "}"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func b649HandoverRows(t *testing.T, home string) string {
	t.Helper()
	data, _ := os.ReadFile(filepath.Join(home, ".code-forge", "native", "handovers.jsonl"))
	return string(data)
}

const b649Refusal = "code-forge block: run r-test has an until-done autopilot grant (ap-done); this case still runs in Node, which cannot read an until-done grant, so nothing was run (until-done-grant). Run it again after the grant ends (code-forge autopilot status --run r-test), or stop the grant first (code-forge autopilot stop --run r-test).\n"

func TestB649HandoverRefusedUnderUntilDoneGrant(t *testing.T) {
	if os.Getenv("B649_CHILD") == "1" {
		b649Child()
	}
	const test = "TestB649HandoverRefusedUnderUntilDoneGrant"
	t.Run("until-done grant: Node never starts", func(t *testing.T) {
		home := b649Home(t, b649UntilDone)
		for _, args := range []string{"block|frobnicate|B1|--run|r-test", "block|frobnicate|B1|--run=r-test"} {
			r := b649Run(t, test, home, t.TempDir(), args)
			if r.nodeArgv != nil || r.code != 1 || r.stdout != "" || r.stderr != b649Refusal {
				t.Fatalf("%s: exit %d stdout %q stderr %q node %q", args, r.code, r.stdout, r.stderr, r.nodeArgv)
			}
		}
		if rows := b649HandoverRows(t, home); !strings.Contains(rows, `"reason":"until-done-grant"`) {
			t.Fatalf("handover log %q", rows)
		}
	})
	t.Run("until-done grant with a deadline: Node never starts", func(t *testing.T) {
		home := b649Home(t, strings.Replace(b649UntilDone, `"until":null`, `"until":"2999-01-01T00:00:00.000Z"`, 1))
		r := b649Run(t, test, home, t.TempDir(), "block|frobnicate|B1|--run|r-test")
		if r.nodeArgv != nil || r.code != 1 || r.stderr != b649Refusal {
			t.Fatalf("exit %d stderr %q node %q", r.code, r.stderr, r.nodeArgv)
		}
	})
	for name, grant := range map[string]string{
		"timed grant":          b649Timed,
		"stopped until-done":   strings.Replace(b649UntilDone, `"status":"active"`, `"status":"stopped"`, 1),
		"expired (done)":       strings.Replace(b649UntilDone, `"status":"active"`, `"status":"expired"`, 1),
		"no grant":             "",
		"other run's grant":    "", // the until-done grant belongs to a run the command does not name
		"until-done, no --run": b649UntilDone,
	} {
		t.Run(name+": hands over as before", func(t *testing.T) {
			home := b649Home(t, grant)
			args := "block|frobnicate|B1|--run|r-test"
			if name == "other run's grant" {
				args = "block|frobnicate|B1|--run|r-other"
			}
			if name == "until-done, no --run" {
				args = "block|frobnicate|B1"
			}
			r := b649Run(t, test, home, t.TempDir(), args)
			want := append([]string{}, strings.Split(args, "|")...)
			if r.code != 0 || r.stdout != "fake-node-ran\n" || len(r.nodeArgv) != len(want)+1 || strings.Join(r.nodeArgv[1:], "|") != args {
				t.Fatalf("exit %d stdout %q stderr %q node %q", r.code, r.stdout, r.stderr, r.nodeArgv)
			}
		})
	}
}

// The critical --yes waive under an until-done grant used to fall back to
// Node (time.Parse of a null until failed); Go now decides it, reading the
// grant as active, the same as under a live timed grant.
func TestB649WaiveCriticalUntilDoneStaysInGo(t *testing.T) {
	if os.Getenv("B649_CHILD") == "1" {
		b649Child()
	}
	const test = "TestB649WaiveCriticalUntilDoneStaysInGo"
	args := "block|waive|B1|F1|--run|r-test|--file|src/a.go|--reason|ok|--yes"
	results := map[string]b649Result{}
	for name, grant := range map[string]string{"until-done": b649UntilDone, "timed": b649Timed} {
		f := newHVFixture(t, nil, nil)
		b649SetGrant(t, f, grant)
		f.rows(map[string]any{"event": "review.result", "block": "B1", "file": "src/a.go", "open": []any{map[string]any{"id": "F1", "severity": "critical"}}})
		r := b649Run(t, test, f.home, f.workspace, args)
		if r.nodeArgv != nil {
			t.Fatalf("%s: handed over to Node: %q (stderr %q)", name, r.nodeArgv, r.stderr)
		}
		if strings.Contains(r.stderr, "waive-yes-critical:") {
			t.Fatalf("%s: the grant was read as inactive: %q", name, r.stderr)
		}
		if len(f.ledger()) != 1 {
			t.Fatalf("%s: rows written: %#v", name, f.ledger())
		}
		results[name] = r
	}
	done, timed := results["until-done"], results["timed"]
	if done.code != timed.code || done.stdout != timed.stdout || done.stderr != timed.stderr || done.code == 0 {
		t.Fatalf("until-done exit %d %q %q; timed exit %d %q %q", done.code, done.stdout, done.stderr, timed.code, timed.stdout, timed.stderr)
	}
	t.Logf("both: exit %d stderr %q", done.code, done.stderr)

	// A grant whose planned blocks are all closed is no longer active.
	f := newHVFixture(t, nil, nil)
	b649SetGrant(t, f, strings.Replace(b649UntilDone, `"until_done":true`, `"until_done":true,"planned_blocks":["B9"]`, 1))
	record := f.record()
	record["blocks"].(map[string]any)["B9"] = map[string]any{"status": "closed", "owned_files": []any{}, "owned_kinds": map[string]any{}}
	b649WriteRecord(t, f, record)
	f.rows(map[string]any{"event": "review.result", "block": "B1", "file": "src/a.go", "open": []any{map[string]any{"id": "F1", "severity": "critical"}}})
	r := b649Run(t, test, f.home, f.workspace, args)
	if r.nodeArgv != nil || r.code != 1 || !strings.Contains(r.stderr, "waive-yes-critical:") {
		t.Fatalf("done grant: exit %d stderr %q node %q", r.code, r.stderr, r.nodeArgv)
	}
}

func b649WriteRecord(t *testing.T, f *hvFixture, record map[string]any) {
	t.Helper()
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "r-test.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestB649RunIDs(t *testing.T) {
	saved := hudWorkerRuns
	t.Cleanup(func() { hudWorkerRuns = saved })
	hudWorkerRuns = func() []string { return []string{"r-worker"} }
	for _, tc := range []struct {
		args string
		want string
	}{
		{"block|close|B1|--run|r1", "r1"},
		{"gates|run|--run=r2|--block|B1", "r2"},
		{"worker|--run|r3|--run|r3", "r3"},
		{"block|land|B1|--run|r1|--|--run|r9", "r1"},
		{"status", ""},
		{"block|close|B1|--run", ""},
		{"review-file|src/a.go", "r-worker"},
		{"review-file|--wait-block|B1|--run|r4", "r4"},
	} {
		if got := strings.Join(hudRunIDs(strings.Split(tc.args, "|")), ","); got != tc.want {
			t.Errorf("%s: runs %q, want %q", tc.args, got, tc.want)
		}
	}
}
