package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B715: Node registers every gate env value of 8 or more UTF-16 units
// (src/gates/run.mjs maskableEnvValues, inside runGates) with the writeSafe
// set, so every later stdout or stderr line of the verb masks it. Here the
// gate env value is the word "repeat_new_tests", and the repeat skip line
// names that setting. Node's stderr (recorded with CODE_FORGE_NATIVE=0 on
// the same fixture) is the golden.
func TestB715GateEnvValueMaskedInRepeatSkipLine(t *testing.T) {
	config := map[string]any{"gates": map[string]any{
		"test":   map[string]any{"argv": []string{"/bin/sh", "-c", "printf green"}, "env": map[string]any{"SKIP_WORD": "repeat_new_tests"}},
		"lint":   nil,
		"types":  nil,
		"format": nil, "rerun_failed": 0, "repeat_new_tests": 2}}
	f := b450Setup(t, config, nil)
	f.write("a.mjs", "export {};\n", 0644)
	f.git("init", "-q")
	f.git("add", ".")
	f.git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-qm", "base")
	base := f.git("rev-parse", "HEAD")
	record, _ := json.Marshal(map[string]any{"run_id": "fixture", "workspace": f.repo, "status": "active", "project": "fixture", "blocks": map[string]any{
		"B1": map[string]any{"status": "open", "kind": "code", "base_sha": base, "owned_files": []string{"a.mjs"}}}})
	f.must(os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "fixture.json"), record, 0600))
	f.must(os.WriteFile(filepath.Join(f.home, ".code-forge", "runs", "fixture.key"), bytes.Repeat([]byte{7}, 32), 0600))
	code, out, errOut := f.call(nil, "gates", "run", "--run", "fixture", "--block", "B1")
	want := "[REDACTED]: skipped (gates.test has no {test_files})\n"
	if code != 0 || errOut != want {
		t.Fatalf("exit=%d stderr=%q\nwant exit 0 stderr %q (stdout %s)", code, errOut, want, out)
	}
	r := f.result(out)
	if !r.AllOK || len(r.Results) == 0 || r.Results[0].Stdout != "green" {
		t.Fatalf("want a green test gate; got %s", out)
	}
}

// B715: Node's API key store registers every key it hands out (store.mjs
// get/put/env/op read) with the writeSafe set, so a jev ask failure line
// that holds the key's text prints [REDACTED]. Here the Jev key is "lane",
// the same as the question id, and Jev answers no question. Node's stderr
// (CODE_FORGE_NATIVE=0, fake fetch with {"answers":{}}) is the golden.
func TestB715JevKeyMaskedInNoAnswerLine(t *testing.T) {
	saved := autopilotSecrets
	t.Cleanup(func() { autopilotSecrets = saved })
	autopilotSecrets = nil
	f, fake := newB550Jev(t, "version: 1\n", 200, `{"answers":{}}`, false)
	t.Setenv("CODE_FORGE_KEY_JEV", "lane")
	code, out, errText := b460Call(t, "jev", "ask", "lane", "--state", "state.json")
	want := "jev ask: Jev did not answer question \"[REDACTED]\"\n"
	if code != 1 || out != "" || errText != want {
		t.Fatalf("= %d %q %q\nwant 1 \"\" %q", code, out, errText, want)
	}
	if len(fake.calls) != 1 || !strings.HasPrefix(fake.calls[0], "Bearer lane ") {
		t.Fatalf("Jev calls %q", fake.calls)
	}
	b550NoHandover(t, f)
}

// B715: keys test reads each key through the store, which registers it with
// writeSafe in Node; every later write of the same process masks it.
func TestB715KeysTestRegistersKeyForLaterWrites(t *testing.T) {
	saved := autopilotSecrets
	t.Cleanup(func() { autopilotSecrets = saved })
	autopilotSecrets = nil
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "file")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	t.Setenv("CODE_FORGE_KEY_LANEKEY", "sk-b715-env-value")
	b460Chdir(t, home)
	code, out, errText := b460Call(t, "keys", "test", "lanekey")
	if code != 0 || out != "lanekey: ok source=env expires=never\n" || strings.Contains(errText, "sk-b715") {
		t.Fatalf("keys test = %d %q %q", code, out, errText)
	}
	_, later := b712Capture(t, func() { fmt.Fprint(safeStderr, "later: sk-b715-env-value\n") })
	if later != "later: [REDACTED]\n" {
		t.Fatalf("later stderr %q", later)
	}
}

// B715: doctor and the worker resolve the Jev key through jevKeyChain; the
// value it reads joins the safe-output set when the resolver runs.
func TestB715JevKeyChainRegistersKey(t *testing.T) {
	saved := autopilotSecrets
	t.Cleanup(func() { autopilotSecrets = saved })
	autopilotSecrets = nil
	home := t.TempDir()
	t.Setenv("CODE_FORGE_KEY_BACKEND", "file")
	t.Setenv("CODE_FORGE_KEY_JEV", "")
	b460Write(t, filepath.Join(home, ".code-forge", "store", "jev.key"), "sk-b715-file-value\n")
	chain, _, reason := jevKeyChain(nil, home, 1_800_000_000_000, false)
	if chain == nil || reason != 0 {
		t.Fatalf("chain nil=%v reason=%v", chain == nil, reason)
	}
	_, before := b712Capture(t, func() { fmt.Fprint(safeStderr, "x sk-b715-file-value\n") })
	if before != "x sk-b715-file-value\n" {
		t.Fatalf("masked before the resolver ran: %q", before)
	}
	if r := chain(); r.key != "sk-b715-file-value" {
		t.Fatalf("key %q", r.key)
	}
	_, after := b712Capture(t, func() { fmt.Fprint(safeStderr, "x sk-b715-file-value\n") })
	if after != "x [REDACTED]\n" {
		t.Fatalf("after the resolver ran: %q", after)
	}
}

// b715KeepSecrets restores the process-wide safe-output set when the test
// ends. Tests that call a verb's plan directly (not through run, which
// starts each command with an empty set) register the keys they read; a
// short fake key such as "value" would otherwise mask later tests' output.
func b715KeepSecrets(t *testing.T) {
	t.Helper()
	saved := safeSecretList()
	t.Cleanup(func() {
		autopilotSecretsMu.Lock()
		autopilotSecrets = saved
		autopilotSecretsMu.Unlock()
	})
}
