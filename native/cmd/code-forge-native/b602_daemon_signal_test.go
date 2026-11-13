package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/procguard"
)

// B602: the worker's daemon (internal serve) shares the worker's process
// group, so a stop of that group (the B553 reap, a terminal ^C) gives it one
// SIGTERM and the worker's daemon stop gives it a second. Whenever the
// signals land, the daemon still releases the lazy root its first git child
// made (proc-<pid>-<ms> under the temp root) and exits on its own.

// b602Serve starts one daemon on a fresh run of home and waits until it
// answers a ping on its socket.
func b602Serve(t *testing.T, bin, home, tmp, repo string, round int) (*exec.Cmd, chan error) {
	t.Helper()
	run := "r-b602-" + strconv.Itoa(round)
	record, _ := json.Marshal(map[string]any{"run_id": run, "project": "p", "workspace": repo})
	for _, file := range []string{filepath.Join(home, ".code-forge", "runs", run+".json"), filepath.Join(repo, ".code-forge", "runs", run+".json")} {
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, record, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(bin, "internal", "serve", "--home", home, "--run", run)
	cmd.Dir = repo
	cmd.Env = []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + home, "TMPDIR=" + tmp, "CODE_FORGE_TMP_ROOT=" + tmp,
		procguard.Env + "=" + os.Getenv(procguard.Env)}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	socket := filepath.Join(home, ".code-forge", "runs", run, "native.sock")
	deadline := time.Now().Add(20 * time.Second)
	for {
		if b602Ping(socket) {
			return cmd, exited
		}
		select {
		case err := <-exited:
			t.Fatalf("daemon ended before it answered: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			<-exited
			t.Fatal("daemon never answered a ping")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func b602Ping(socket string) bool {
	conn, err := net.DialTimeout("unix", socket, time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write([]byte(`{"method":"ping"}` + "\n")); err != nil {
		return false
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	return err == nil && strings.Contains(line, "ok")
}

func TestB602DaemonReleasesLazyRootUnderRepeatedSigterm(t *testing.T) {
	bin := b553Binary(t)
	// A short home: the socket path must fit sun_path (104 bytes on macOS).
	home, err := os.MkdirTemp("/tmp", "b602-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	tmp, repo := filepath.Join(home, "tmp"), filepath.Join(home, "repo")
	for _, d := range []string{tmp, repo} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	git := exec.Command("git", "-c", "init.defaultBranch=main", "init", "-q")
	git.Dir = repo
	git.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for round := 0; round < 12; round++ {
		cmd, exited := b602Serve(t, bin, home, tmp, repo, round)
		pid := cmd.Process.Pid
		prefix := "proc-" + strconv.Itoa(pid) + "-"
		made := false
		for _, name := range b573Left(t, tmp) {
			made = made || strings.HasPrefix(name, prefix)
		}
		if !made {
			t.Fatalf("round %d: the daemon made no lazy root (%v)", round, b573Left(t, tmp))
		}
		// the group stop, then the worker's stop, and more of the same until
		// the daemon is gone (bounded by its exit, at most 10s)
		var waitErr error
		deadline := time.Now().Add(10 * time.Second)
	signal:
		for {
			_ = syscall.Kill(pid, syscall.SIGTERM)
			select {
			case waitErr = <-exited:
				break signal
			case <-time.After(200 * time.Microsecond):
			}
			if time.Now().After(deadline) {
				_ = cmd.Process.Kill()
				<-exited
				t.Fatalf("round %d: the daemon did not stop on SIGTERM", round)
			}
		}
		for _, name := range b573Left(t, tmp) {
			if strings.HasPrefix(name, prefix) {
				t.Errorf("round %d: lazy root %s left behind (exit %v)", round, name, waitErr)
			}
		}
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			t.Errorf("round %d: the daemon died by %v instead of exiting", round, status.Signal())
		}
	}
}
