package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// B771: Deps.HardTimer starts the foreground session's hard timeout. A child
// that never answers times out when the seam fires (not when a real clock
// runs out); a child that answers is never cut and its timer is stopped.
func TestB771HardTimerSeam(t *testing.T) {
	o, dir := testSetup(t)
	o.Cfg = map[string]any{"levels": map[string]any{"L1": map[string]any{"provider": "anthropic", "model": "claude-x"}}}
	o.TimeoutMs = 3000
	marker := filepath.Join(dir, "sleeping")
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\nif [ -f " + filepath.Join(dir, "hang") + " ]; then : > " + marker + "; exec sleep 60; fi\n" +
		"sleep 4\nprintf '%s' '{\"type\":\"result\",\"is_error\":false,\"result\":\"done\"}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var asked []time.Duration
	stopped := 0
	deps := Deps{Stderr: &strings.Builder{}, Bins: map[string]string{"claude": bin}, Environ: []string{"PATH=/usr/bin:/bin"},
		HardTimer: func(d time.Duration) (<-chan time.Time, func()) {
			asked = append(asked, d)
			c, done := make(chan time.Time, 1), make(chan struct{})
			go func() {
				for {
					select {
					case <-done:
						return
					case <-time.After(10 * time.Millisecond):
						if _, err := os.Stat(marker); err == nil {
							c <- time.Now()
							return
						}
					}
				}
			}()
			return c, func() { stopped++; close(done) }
		}}
	// answers after 4 s, past the 3 s timeout: the seam, not a clock, decides
	res, err := Spawn(o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Get("status") != "ok" {
		t.Fatalf("answering child = %v, want ok", res)
	}
	if err := os.WriteFile(filepath.Join(dir, "hang"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	res, err = Spawn(o, deps)
	if err != nil {
		t.Fatal(err)
	}
	if res.Get("status") != "timeout" || res.Get("reason") != "killed after 3000 ms" || time.Since(start) > 30*time.Second {
		t.Fatalf("hanging child = %v after %s, want the timeout the seam fired", res, time.Since(start))
	}
	if len(asked) != 2 || asked[0] != 3*time.Second || stopped != 2 {
		t.Fatalf("seam asked %v, stopped %d", asked, stopped)
	}
}
