package procguard

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestB882Helper is the orphan's body: this test binary sleeps 30 s. A
// platform binary such as /bin/sleep hides its environment from `ps -E` on
// macOS; a code-forge worker, like this binary, does not.
func TestB882Helper(t *testing.T) {
	if os.Getenv("B882_HELPER") == "1" {
		time.Sleep(30 * time.Second)
	}
}

// b882Orphan starts this test binary as a 30 s sleeper through a shell that
// exits at once, so init adopts it: it does not descend from this test.
// extra is added to its env.
func b882Orphan(t *testing.T, extra ...string) int {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", `"$0" -test.run='^TestB882Helper$' >/dev/null 2>&1 </dev/null & echo $!`, os.Args[0])
	cmd.Env = append(append(os.Environ(), "B882_HELPER=1"), extra...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("pid %q: %v", out, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(5 * time.Second)
	for {
		ppid, _ := exec.Command("/bin/ps", "-o", "ppid=", "-p", strconv.Itoa(pid)).Output()
		if strings.TrimSpace(string(ppid)) == "1" {
			return pid
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d still has parent %q", pid, ppid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// B882: a detached process launched with LaunchEnv (it names this test root)
// may be signalled after init adopts it; one that only inherited the test
// environment, or names another root, may not.
func TestB882OwnedDetachedProcessMayBeSignalled(t *testing.T) {
	root := strconv.Itoa(os.Getpid())
	t.Setenv(Env, root)
	env := LaunchEnv([]string{"A=1", OwnedEnv + "=999"})
	if len(env) != 2 || env[0] != "A=1" || env[1] != OwnedEnv+"="+root {
		t.Fatalf("LaunchEnv = %q", env)
	}
	mine := b882Orphan(t, OwnedEnv+"="+root)
	foreign := b882Orphan(t)
	other := b882Orphan(t, OwnedEnv+"=999999")
	if !Allowed(mine, syscall.SIGTERM) {
		t.Fatal("the owned detached process is refused")
	}
	if Allowed(foreign, syscall.SIGTERM) || Allowed(other, syscall.SIGTERM) {
		t.Fatal("a detached process this test root does not own is allowed")
	}
	if err := Kill(foreign, syscall.SIGTERM); err != syscall.EPERM {
		t.Fatalf("kill foreign = %v, want EPERM", err)
	}
	if err := Kill(mine, syscall.SIGTERM); err != nil {
		t.Fatalf("kill owned = %v", err)
	}
	if syscall.Kill(foreign, 0) != nil {
		t.Fatal("the foreign process was stopped")
	}
}
