package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/refguard"
)

// B808: doctor without .code-forge.yml shows the ref guard row too, in the
// same place as with a config (right before the native daemon row).
func TestB808DoctorNoConfigShowsTheRefGuard(t *testing.T) {
	_, repo := b504NoConfig(t)
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = repo, gitx.ChildEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	git("-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "--allow-empty", "-m", "base")
	tree := filepath.Join(t.TempDir(), "wt", "B1")
	git("worktree", "add", "-q", "-b", "forge/r1/B1", tree)
	hook, err := refguard.Install(tree, "main", "forge/r1/B1", "forge/{run}/{block}")
	if err != nil {
		t.Fatal(err)
	}
	_, out, _ := runCaptured(t, "doctor")
	if want := "FAIL probes: skipped: no valid config\n"; !strings.Contains(out, want) {
		t.Fatalf("not the no-config path:\n%s", out)
	}
	if want := "OK   ref guard: on: hook " + hook + "; 1 coder worktree(s) guarded\nINFO native daemon: "; !strings.Contains(out, want) {
		t.Fatalf("doctor lacks %q:\n%s", want, out)
	}
	_, out, _ = runCaptured(t, "doctor", "--json")
	var doc struct {
		Rows []struct{ ID, Status string } `json:"rows"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	ids := []string{}
	for _, r := range doc.Rows {
		ids = append(ids, r.ID)
	}
	if got := strings.Join(ids, " "); !strings.HasSuffix(got, " native ref-guard native_daemon") {
		t.Fatalf("ids = %s", got)
	}
}
