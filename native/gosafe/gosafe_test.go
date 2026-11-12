package gosafe

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func panicky() { panic("value that must not reach the handler: sk-test-SECRET") }

// A panic in fn reaches the handler with the goroutine's name and the
// panic site (function, file, line), never the panic value.
func TestGoPassesPanicSiteToHandler(t *testing.T) {
	type call struct{ name, site string }
	got := make(chan call, 1)
	SetHandler(func(name, site string) { got <- call{name, site} })
	defer SetHandler(nil)
	Go("unit", panicky)
	c := <-got
	if c.name != "unit" || !strings.HasPrefix(c.site, "gosafe.panicky gosafe_test.go:") || strings.Contains(c.site, "SECRET") {
		t.Errorf("handler got %+v", c)
	}
}

// The seam panics before fn runs, only in the goroutine it names.
func TestGoSeamPanicsNamedGoroutine(t *testing.T) {
	got := make(chan string, 2)
	SetHandler(func(name, _ string) { got <- name })
	defer SetHandler(nil)
	Seam = func(name string) {
		if name == "target" {
			panic("seam")
		}
	}
	defer func() { Seam = nil }()
	ran := make(chan string, 2)
	Go("target", func() { ran <- "target" })
	Go("other", func() { ran <- "other" })
	if name := <-got; name != "target" {
		t.Errorf("handler got %q, want target", name)
	}
	if name := <-ran; name != "other" {
		t.Errorf("ran %q, want only other", name)
	}
}

// Without a handler the panic is raised again: the process crashes as Go
// always did (exit 2), so tools without a guard lose nothing.
func TestGoWithoutHandlerRepanics(t *testing.T) {
	if os.Getenv("GOSAFE_CHILD") == "1" {
		done := make(chan struct{})
		Go("bare", func() { defer close(done); panicky() })
		<-done
		select {}
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestGoWithoutHandlerRepanics$")
	cmd.Env = append(os.Environ(), "GOSAFE_CHILD=1")
	out, err := cmd.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 2 || !strings.Contains(string(out), "panic: value that must not reach the handler") {
		t.Errorf("err=%v out=%s; want the plain Go crash", err, out)
	}
}
