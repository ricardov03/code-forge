package gates

import (
	"context"
	"reflect"
	"testing"
)

// B924: Plan.BaseEnv (TMPDIR on the RAM disk) reaches every gate command;
// a command's own env wins, and BaseEnv keys are not reported as env_keys.
func TestB924BaseEnvReachesGateCommands(t *testing.T) {
	dir := t.TempDir()
	p := Plan{
		Commands: map[string]*Command{
			"test": {Argv: []string{"/bin/sh", "-c", `printf %s "$TMPDIR"`}},
			"lint": {Argv: []string{"/bin/sh", "-c", `printf %s "$TMPDIR"`}, Env: map[string]string{"TMPDIR": "/own"}},
		},
		Custom:  []Custom{{Name: "extra", Command: &Command{Argv: []string{"/bin/sh", "-c", `printf %s "$TMPDIR"`}}}},
		BaseEnv: map[string]string{"TMPDIR": "/Volumes/cf-tmp/r1-B1-x"},
	}
	results, all, err := Run(context.Background(), p, dir, "r1", 30000, Scope{})
	if err != nil || !all {
		t.Fatalf("Run: all=%v err=%v %v", all, err, results)
	}
	got := map[string]any{}
	keys := map[string]any{}
	for _, r := range results {
		o := r.(interface{ Get(string) any })
		if o.Get("skipped") == true {
			continue
		}
		got[o.Get("gate").(string)] = o.Get("stdout")
		keys[o.Get("gate").(string)] = o.Get("env_keys")
	}
	want := map[string]any{"test": "/Volumes/cf-tmp/r1-B1-x", "lint": "/own", "extra": "/Volumes/cf-tmp/r1-B1-x"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TMPDIR seen by gates = %v, want %v", got, want)
	}
	if keys["test"] != nil || keys["extra"] != nil || !reflect.DeepEqual(keys["lint"], []string{"TMPDIR"}) {
		t.Fatalf("env_keys = %v", keys)
	}
	if m := Masks(p); len(m) != 0 {
		t.Fatalf("BaseEnv must not be masked as a secret: %q", m)
	}
}
