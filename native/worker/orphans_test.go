package worker

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

const psFixture = `  501   100     1 S        10 Wed Nov 11 09:00:00 2026 /usr/bin/claude -p review
  501   101     1 S        10 Wed Nov 11 09:00:00 2026 /usr/bin/claude -p other-run
  501   102     1 S        10 Wed Nov 11 09:00:00 2026 /usr/bin/claude -p live-owner
  501   103    77 S        10 Wed Nov 11 09:00:00 2026 /usr/bin/claude -p has-parent
    0   104     1 S        10 Wed Nov 11 09:00:00 2026 /usr/bin/root-owned
  501   105     1 Z        10 Wed Nov 11 09:00:00 2026 zombie
  501   500     1 S        10 Wed Nov 11 08:00:00 2026 node code-forge worker
garbage row
`

func TestB489ParseProcesses(t *testing.T) {
	rows := ParseProcesses(psFixture)
	if len(rows) != 7 || rows[0].PID != 100 || rows[0].Start != "Wed Nov 11 09:00:00 2026" || rows[0].Command != "/usr/bin/claude -p review" {
		t.Fatalf("rows %+v", rows)
	}
}

// Only this user's init-parented process of this run whose owner is proven
// dead is stopped: SIGTERM, then SIGKILL after the grace when it lives on.
func TestB489OrphanSweepStopsOnlyProvenOrphans(t *testing.T) {
	envs := map[int]map[string]string{
		100: {"CODE_FORGE_OWNER_PID": "900", "CODE_FORGE_RUN_ID": "r1", "CODE_FORGE_OWNER_START_TIME": "Wed Nov 11 08:00:00 2026"},
		101: {"CODE_FORGE_OWNER_PID": "900", "CODE_FORGE_RUN_ID": "r2"},
		102: {"CODE_FORGE_OWNER_PID": "500", "CODE_FORGE_RUN_ID": "r1", "CODE_FORGE_OWNER_START_TIME": "Wed Nov 11 08:00:00 2026"},
		103: {"CODE_FORGE_OWNER_PID": "900", "CODE_FORGE_RUN_ID": "r1"},
		104: {"CODE_FORGE_OWNER_PID": "900", "CODE_FORGE_RUN_ID": "r1"},
	}
	var kills []string
	clock := fixedNow
	s := &OrphanSweep{RunID: "r1", UID: 501, Self: 600, Parent: 601, GraceMs: 100,
		Processes: func() []ProcInfo { return ParseProcesses(psFixture) },
		ReadEnv:   func(p ProcInfo) map[string]string { return envs[p.PID] },
		Alive:     func(pid int) bool { return pid != 900 },
		Kill: func(pid int, sig syscall.Signal) error {
			kills = append(kills, map[syscall.Signal]string{syscall.SIGTERM: "TERM", syscall.SIGKILL: "KILL"}[sig]+":"+itoa(pid))
			return nil
		},
		Sleep: func(d time.Duration) { clock = clock.Add(d) },
		Now:   func() time.Time { return clock },
	}
	orphans := s.Orphans()
	if len(orphans) != 1 || orphans[0].PID != 100 {
		t.Fatalf("orphans %+v, want only pid 100", orphans)
	}
	s.Sweep()
	if len(kills) != 2 || kills[0] != "TERM:100" || kills[1] != "KILL:100" {
		t.Fatalf("kills %v", kills)
	}
}

func itoa(n int) string {
	return string(rune('0'+n/100)) + string(rune('0'+n/10%10)) + string(rune('0'+n%10))
}

func TestB489EnvMarker(t *testing.T) {
	text := "HOME=/Users/x CODE_FORGE_OWNER_PID=42 CODE_FORGE_OWNER_START_TIME=Wed Nov 11 08:00:00 2026 PATH=/bin"
	if v, ok := envMarker(text, "CODE_FORGE_OWNER_START_TIME"); !ok || v != "Wed Nov 11 08:00:00 2026" {
		t.Fatalf("start %q %v", v, ok)
	}
	if v, ok := envMarker(text, "CODE_FORGE_OWNER_PID"); !ok || v != "42" {
		t.Fatalf("pid %q", v)
	}
	if _, ok := envMarker(text, "CODE_FORGE_RUN_ID"); ok {
		t.Fatal("found a missing marker")
	}
}

// The Go worker serves only when the Jev key is provably absent.
func TestB489JevKeyAbsent(t *testing.T) {
	home := t.TempDir()
	env := map[string]string{"HOME": home, "CODE_FORGE_KEY_BACKEND": "file"}
	probe := JevProbe{Getenv: func(k string) string { return env[k] },
		Keychain: func(string, string) (bool, bool) {
			t.Fatal("keychain asked with the file backend")
			return false, false
		}}
	if !JevKeyAbsent(nil, probe) {
		t.Fatal("no env, no file: absent")
	}
	if JevKeyAbsent(map[string]any{"system1": map[string]any{"key": "op://vault/jev"}}, probe) {
		t.Fatal("a 1Password reference may resolve")
	}
	env["CODE_FORGE_KEY_JEV"] = "k"
	if JevKeyAbsent(nil, probe) {
		t.Fatal("env key set")
	}
	delete(env, "CODE_FORGE_KEY_JEV")
	env["MY_JEV"] = "k"
	if JevKeyAbsent(map[string]any{"keys": map[string]any{"jev": "env:MY_JEV"}}, probe) {
		t.Fatal("named env key set")
	}
	store := filepath.Join(home, ".code-forge", "store")
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "jev.key"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if JevKeyAbsent(nil, probe) {
		t.Fatal("store file present")
	}
	if !JevKeyAbsent(map[string]any{"system1": map[string]any{"key": "keychain:other"}}, probe) {
		t.Fatal("other store name absent")
	}
	delete(env, "CODE_FORGE_KEY_BACKEND")
	for _, c := range []struct {
		found, decided, want bool
	}{{false, true, true}, {true, true, false}, {false, false, false}} {
		probe.Keychain = func(string, string) (bool, bool) { return c.found, c.decided }
		if got := JevKeyAbsent(map[string]any{"system1": map[string]any{"key": "keychain:other"}}, probe); got != c.want {
			t.Errorf("keychain found=%v decided=%v: absent %v, want %v", c.found, c.decided, got, c.want)
		}
	}
}
