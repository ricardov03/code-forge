package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/worker"
)

// B707: the keychain hand-over cases of TestB474DoctorHandsOverUnportedCases,
// TestB489WorkerHandOverCases, TestB458/B502/B527/B548 init hand-overs run
// as macOS with no usable `security` (the package default, every host). On
// Linux without @napi-rs/keyring's binding, Node's default chain is the file
// store alone (B679), so the same cases run in Go with no hand-over. These
// tests state that Linux expectation on every host.

// doctor --quick with no key anywhere: the keys WARN row, no hand-over.
func TestB707DoctorLinuxNoBindingRunsInGo(t *testing.T) {
	b474Project(t, "provider: anthropic\n"+b474Levels, 0)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	b679Linux(t, false, nil)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, "OK   config: valid\n" + b474NoLinks + b474NoKey, b474Summary(1, 2, 0)}})
}

// The same doctor run on the macOS default hands over (the keychain Go
// cannot open), so the two OS expectations differ only by the seam.
func TestB707DoctorMacNoSecurityHandsOver(t *testing.T) {
	b474Project(t, "provider: anthropic\n"+b474Levels, 0)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	expectHandover(t, "doctor-key-store", "doctor", "--quick")
}

// init's Jev auto step and its doctor step: no init-jev-keychain or
// doctor-key-store hand-over. No key anywhere is Node's own refusal
// (wizard/run.mjs: exit 2, keys.jev missing, nothing written); a key in the
// file store is found in Go.
func TestB707InitLinuxNoBindingRunsInGo(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		key   string
		code  int
		wants []string
	}{
		{"jev key", []string{"--no-interaction", "--skip-doctor"}, "", 2, []string{"missing required answers, nothing written:\n  keys.jev (pass --jev-ref, --jev-env or --no-jev)"}},
		{"jev key in the file store", []string{"--no-interaction", "--skip-doctor"}, "sk-b707-file\n", 0, []string{"keys: jev from file"}},
		{"doctor reads the key chain", []string{"--no-interaction", "--no-jev"}, "", 0, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ss := newB679SS()
			side := b548Go(t, b548Case{name: c.name, args: c.args, home: func(home string) {
				b679Linux(t, false, ss)
				if c.key != "" {
					writeHomeFile(t, home, ".code-forge/store/jev.key", c.key)
				}
			}})
			all := side.stdout + side.stderr + side.log + side.files
			if side.code != c.code || strings.Contains(all, "init-jev-keychain") || strings.Contains(all, "doctor-key-store") || strings.Contains(side.stdout+side.stderr+side.log, "sk-b707-file") {
				t.Fatalf("code %d\nstdout %s\nstderr %s\nlog %s\nfiles %s", side.code, side.stdout, side.stderr, side.log, side.files)
			}
			for _, want := range c.wants {
				if !strings.Contains(all, want) {
					t.Fatalf("missing %q\nstdout %s\nstderr %s\nlog %s", want, side.stdout, side.stderr, side.log)
				}
			}
			if len(ss.calls) != 0 {
				t.Fatalf("the Secret Service was asked: %q", ss.calls)
			}
		})
	}
}

// worker boot with the keychain case of TestB489WorkerHandOverCases: on
// Linux without the binding no keychain is asked and the worker runs in Go.
func TestB707WorkerLinuxNoBindingRunsInGo(t *testing.T) {
	f := newB489Fix(t)
	f.withConfig(t, strings.Repeat("0", 40), `{"version":1}`)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	ss := newB679SS()
	b679Linux(t, false, ss)
	asked := false
	oldProbe := workerJevProbe
	t.Cleanup(func() { workerJevProbe = oldProbe })
	workerJevProbe = worker.JevProbe{Keychain: func(string, string) (bool, bool) { asked = true; return true, true }}
	_ = os.Remove(filepath.Join(f.home, ".code-forge", "store", "jev.key"))
	before := len(b460Handovers(t, f.home))
	code, _, errText := b460Call(t, "worker", "--run", "r1", "--once")
	if rows := b460Handovers(t, f.home); code != 0 || len(rows) != before {
		t.Fatalf("worker = %d %q handovers %v", code, errText, rows)
	}
	if asked || len(ss.calls) != 0 {
		t.Fatalf("a keychain was asked: probe %v, Secret Service %q", asked, ss.calls)
	}
}
