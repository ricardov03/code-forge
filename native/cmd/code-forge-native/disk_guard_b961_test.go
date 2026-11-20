package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/diskguard"
)

// B961 tests: land, gates run and run start refuse on a disk under
// land.min_free_gb. The free space comes from diskguard.TestFreeEnv, the Go
// cache from a temp GOCACHE.

func b961LowDisk(t *testing.T, gb string) {
	t.Helper()
	t.Setenv(diskguard.TestFreeEnv, gb)
	t.Setenv("GOCACHE", t.TempDir())
}

func TestB961LandRefuses(t *testing.T) {
	f := newLandBatchFixture(t, 0)
	b961LowDisk(t, "10")
	code, output := f.land("B1", "B2")
	if code != 1 {
		t.Fatalf("exit=%d, want 1: %s", code, output)
	}
	for _, want := range []string{"block land: disk guard: only 10.0 GB free", "land.min_free_gb (15 GB)"} {
		if !strings.Contains(output, want) {
			t.Errorf("output must contain %q: %s", want, output)
		}
	}
	if got := f.git(f.repo, "rev-parse", "main"); got != f.base {
		t.Errorf("main=%s, want it unchanged at %s", got, f.base)
	}
	// With room the same land goes through.
	b961LowDisk(t, "100")
	if code, output := f.land("B1", "B2"); code != 0 {
		t.Fatalf("land with room: exit=%d: %s", code, output)
	}
}

func TestB961GatesRefuses(t *testing.T) {
	b961LowDisk(t, "10")
	res, marker := b878InGatesRepo(t, "gates", "run", "--cwd", ".")
	if res.code != 1 || !strings.Contains(res.stderr, "gates run: disk guard: only 10.0 GB free") || !strings.Contains(res.stderr, "no gate was run") {
		t.Fatalf("code=%d stderr=%q, want the disk refusal", res.code, res.stderr)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("a gate ran on a full disk")
	}
	b961LowDisk(t, "100")
	if res, marker := b878InGatesRepo(t, "gates", "run", "--cwd", "."); res.code != 0 {
		t.Fatalf("gates run with room: code=%d stderr=%q", res.code, res.stderr)
	} else if _, err := os.Stat(marker); err != nil {
		t.Errorf("the gate did not run with room on the disk")
	}
}

func TestB961RunStartRefuses(t *testing.T) {
	b961LowDisk(t, "10")
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), []byte("version: 1\nprovider: anthropic\nproject:\n  slug: b961\n"+b474Levels), 0o600); err != nil {
		t.Fatal(err)
	}
	res := b508Run(t, nil, "run", "start", "--cwd", repo, "--run", "r-b961", "--worker-pid", strconv.Itoa(os.Getpid()))
	if res.code == 0 || !strings.Contains(res.stderr+res.stdout, "disk guard: only 10.0 GB free") {
		t.Fatalf("code=%d stdout=%q stderr=%q, want the disk refusal", res.code, res.stdout, res.stderr)
	}
	if _, err := os.Stat(filepath.Join(res.home, ".code-forge", "runs", "r-b961")); err == nil {
		t.Errorf("run start made the run on a full disk")
	}
}

func TestB961DoctorRow(t *testing.T) {
	cases := []struct{ free, status string }{{"10", "FAIL"}, {"20", "WARN"}, {"40", "OK"}}
	for _, c := range cases {
		t.Setenv(diskguard.TestFreeEnv, c.free)
		row, ok := doctorDiskRow(nil, t.TempDir())
		if !ok || row.status != c.status || row.label != "free disk" || !strings.Contains(row.detail, c.free+".0 GB") {
			t.Errorf("free %s GB: row=%+v ok=%v, want %s", c.free, row, ok, c.status)
		}
	}
	if _, ok := doctorDiskRow(map[string]any{"land": map[string]any{"min_free_gb": float64(0)}}, t.TempDir()); ok {
		t.Errorf("land.min_free_gb 0: a doctor row, want none")
	}
}

func TestB961Config(t *testing.T) {
	if got := diskGuardMinGB(nil); got != 15 {
		t.Errorf("default land.min_free_gb=%g, want 15", got)
	}
	if got := diskGuardMinGB(map[string]any{"land": map[string]any{"min_free_gb": float64(40)}}); got != 40 {
		t.Errorf("land.min_free_gb 40 read as %g", got)
	}
	landIssues := func(v any) []config.SchemaIssue {
		out := []config.SchemaIssue{}
		for _, issue := range config.SchemaIssues(map[string]any{"land": map[string]any{"min_free_gb": v}}, "1") {
			if strings.HasPrefix(issue.Path, "/land") {
				out = append(out, issue)
			}
		}
		return out
	}
	if issues := landIssues(float64(20)); len(issues) != 0 {
		t.Errorf("land.min_free_gb 20: schema issues %v, want none", issues)
	}
	if issues := landIssues(float64(-1)); len(issues) == 0 {
		t.Errorf("land.min_free_gb -1: no schema issue, want one")
	}
}
