package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// landB806FakeGh is a fake gh whose first `fails` run list calls fail with a
// network error (exit 1, gh's text on stderr); later ones answer green. pr
// list answers [] and pr close exits 0. Every call is logged to $dir/calls.
func landB806FakeGh(t *testing.T, fails int) string {
	t.Helper()
	dir := t.TempDir()
	d := closeCommand([]string{dir})
	script := `#!/bin/sh
d=` + d + `
printf '%s\n' "$*" >> "$d/calls"
case "$1 $2" in
  'run list')
    n=$(cat "$d/count" 2>/dev/null || echo 0); n=$((n+1)); echo $n > "$d/count"
    if [ "$n" -le ` + strconv.Itoa(fails) + ` ]; then
      echo 'Get "https://api.github.com/repos/o/r/actions/runs": read tcp 10.0.0.2:5555->140.82.112.6:443: read: connection reset by peer' >&2
      exit 1
    fi
    cat "$d/runs.json" ;;
  'pr list') echo '[]' ;;
  'pr close') exit 0 ;;
  *) echo "fake gh: unexpected $*" >&2; exit 9 ;;
esac
`
	landWrite(t, filepath.Join(dir, "gh"), script)
	if err := os.Chmod(filepath.Join(dir, "gh"), 0o700); err != nil {
		t.Fatal(err)
	}
	landWrite(t, filepath.Join(dir, "runs.json"), landCIGreen)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
	return dir
}

// B806: two gh network errors while reading CI are retried (with a backoff
// on the fake clock) and the land goes on.
func TestB806GhNetworkErrorRetried(t *testing.T) {
	f := landB910Fixture(t, map[string]any{"require_ci": "github"})
	f.bareRemote()
	gh := landB806FakeGh(t, 2)
	slept := landCIFakeClock(t)
	code, output := f.land("B1", "B2")
	if code != 0 {
		t.Fatalf("exit=%d, want 0 after the retries: %s", code, output)
	}
	f.checkMain([]string{"B1", "B2"})
	if *slept == 0 {
		t.Errorf("the retries must back off (fake clock slept 0)")
	}
	runList := 0
	for _, call := range landCICalls(t, gh) {
		if strings.HasPrefix(call, "run list ") {
			runList++
		}
	}
	// B1 tip: 2 failures + green; B2 tip; B2 rebased tip.
	if runList != 5 {
		t.Errorf("run list calls=%d, want 5 (2 retried failures, then one per tip)", runList)
	}
}

// B806: a gh network error that lasts past 3 tries refuses at ci with gh's
// message, the try count and a next line; nothing lands.
func TestB806GhNetworkErrorRefusesAfterThreeTries(t *testing.T) {
	f := landB910Fixture(t, map[string]any{"require_ci": "github"})
	gh := landB806FakeGh(t, 4)
	landCIFakeClock(t)
	code, output := f.land("B1", "B2")
	if code == 0 {
		t.Fatalf("exit=0, want a refusal: %s", output)
	}
	for _, want := range []string{
		"block B1 land stopped at ci: cannot read CI runs for tip ",
		"connection reset by peer (after 3 tries)",
		"\nnext: code-forge ci watch --sha ",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("output must contain %q:\n%s", want, output)
		}
	}
	if calls := landCICalls(t, gh); len(calls) != 3 {
		t.Errorf("gh calls=%q, want 3 tries", calls)
	}
	if got := f.git(f.repo, "rev-parse", "main"); got != f.base {
		t.Errorf("main=%s, want unchanged %s", got, f.base)
	}
}
