package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// B689: the top-level `code-forge land` (Node's usage). B879 (B801 audit
// P07): every form but a leading --help/-h is a usage error: exit 2, the
// usage, a line that names block land and its next line; the error log keeps
// the refusal line (not the next line) and Go never hands it to Node.
const (
	b689Refusal   = "land: not a command on its own; code-forge block land lands a closed block"
	b689NodeUsage = "usage: code-forge land\n  Used internally by code-forge block land; this module has no standalone handler.\n"
	b689Stderr    = b689NodeUsage + b689Refusal + "\nnext: code-forge block land <id> --run <run>\n"
)

func TestB689LandRefusesInGo(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		flags []any
	}{
		{[]string{"land"}, []any{}},
		{[]string{"land", "x"}, []any{}},
		{[]string{"land", "x", "--help"}, []any{"--help"}},
		{[]string{"land", "x", "-h"}, []any{"-h"}},
		{[]string{"land", "x", "y", "--help"}, []any{"--help"}},
		{[]string{"land", "--json"}, []any{"--json"}},
	} {
		home := b613Env(t)
		// A process starts with no reported kind; an earlier test in this
		// binary (doctor keys' op_timeout) can leave one set.
		nativeReportedKind, nativeCrashStack = "", nil
		code, stdout, stderr := runCaptured(t, tc.args...)
		if code != 2 || stdout != "" || stderr != b689Stderr {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", tc.args, code, stdout, stderr)
		}
		if _, err := os.Stat(filepath.Join(home, ".code-forge", "native", "handovers.jsonl")); err == nil {
			t.Fatalf("%v handed over to Node", tc.args)
		}
		data, err := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
		if err != nil {
			t.Fatalf("%v: no errors.jsonl entry: %v", tc.args, err)
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &entry); err != nil {
			t.Fatalf("%v: entry %q: %v", tc.args, data, err)
		}
		want := map[string]any{"verb": "land", "sub": nil, "flags": tc.flags, "exit": float64(2), "kind": "usage",
			"message": b689Refusal, "stack": nil, "cleaned": []any{}, "before": []any{}}
		for key, value := range want {
			if !reflect.DeepEqual(entry[key], value) {
				t.Errorf("%v: entry %s = %#v, want %#v", tc.args, key, entry[key], value)
			}
		}
		crumbs, err := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "breadcrumbs.json"))
		if err != nil || string(crumbs) != "[\"land\"]\n" {
			t.Fatalf("%v: breadcrumbs %q (%v)", tc.args, crumbs, err)
		}
	}
}

// land --help and -h print Node's usage, with no hand-over and no log entry.
func TestB689LandHelpPrintsNodeUsage(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		home := b613Env(t)
		code, stdout, stderr := runCapturedRoute(t, routes["land"], flag)
		if code != 0 || stdout != b689NodeUsage || stderr != "" {
			t.Fatalf("land %s: code=%d stdout=%q stderr=%q", flag, code, stdout, stderr)
		}
		// through the router the B613 page wraps the same usage
		code, stdout, _ = runCaptured(t, "land", flag)
		if code != 0 || stdout != helpWrapped(t, "land", b689NodeUsage) {
			t.Fatalf("code-forge land %s: code=%d stdout=%q", flag, code, stdout)
		}
		for _, name := range []string{"native/handovers.jsonl", "logs/errors.jsonl", "logs/breadcrumbs.json"} {
			if _, err := os.Stat(filepath.Join(home, ".code-forge", name)); err == nil {
				t.Fatalf("land %s wrote %s", flag, name)
			}
		}
	}
}
