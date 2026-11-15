package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// B689: Node's output for the top-level `code-forge land`, recorded by
// running the Node router (CODE_FORGE_NATIVE=0) in a temp HOME on 2026-11-15.
// src/cli/land.mjs has no default export, so every form but a leading
// --help/-h refuses with exit 1; the errors.jsonl entry fingerprint is Node's.
const (
	b689NodeStderr = "code-forge: verb \"land\" has no default export function\n"
	b689NodeUsage  = "usage: code-forge land\n  Used internally by code-forge block land; this module has no standalone handler.\n"
	b689NodeFP     = "014b7ffb7d5f"
)

func TestB689LandRefusesInGoLikeNode(t *testing.T) {
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
		if code != 1 || stdout != "" || stderr != b689NodeStderr {
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
		want := map[string]any{"verb": "land", "sub": nil, "flags": tc.flags, "exit": float64(1), "kind": "error",
			"message": strings.TrimSuffix(b689NodeStderr, "\n"), "stack": nil, "cleaned": []any{}, "fp": b689NodeFP, "before": []any{}}
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
