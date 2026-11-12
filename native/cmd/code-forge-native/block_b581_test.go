package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B581: the block open cases that went to Node run in Go with Node's
// output; the forms Go does not reproduce hand over under a named reason
// before anything is written. The expected texts are Node's own output
// (CODE_FORGE_NATIVE=0 ./bin/code-forge block open …) for the same case.

// b581Fixture is the close fixture with its workspace recorded resolved,
// as run start records it, an acceptance file and the cwd at the workspace.
func b581Fixture(t *testing.T) (*closeReviewFixture, string) {
	t.Helper()
	f := newCloseReviewFixture(t, 1)
	real, err := filepath.EvalSymlinks(f.top)
	if err != nil {
		t.Fatal(err)
	}
	f.top = real
	b470SetRun(t, f, func(record, _ *ledger.Object) { record.Set("workspace", real) })
	acceptance := filepath.Join(f.home, "acceptance.yml")
	closeFixtureWrite(t, acceptance, []byte("- clause: c\n  tests: [t]\n"))
	wd, _ := os.Getwd()
	if err := os.Chdir(f.top); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	return f, acceptance
}

func b581RunFile(f *closeReviewFixture) string {
	return filepath.Join(f.home, ".code-forge/runs/r-close-fixture.json")
}

// b581Refused runs block open and wants exit code, no stdout, the exact
// stderr and the run record unchanged.
func b581Refused(t *testing.T, f *closeReviewFixture, code int, want string, args ...string) {
	t.Helper()
	before, _ := os.ReadFile(b581RunFile(f))
	res := b451Run(t, f.home, append([]string{"open"}, args...)...)
	if res.code != code || res.stdout != "" || res.stderr != want {
		t.Fatalf("exit=%d stdout=%q stderr=%q; want exit %d and %q", res.code, res.stdout, res.stderr, code, want)
	}
	if after, _ := os.ReadFile(b581RunFile(f)); !bytes.Equal(before, after) {
		t.Fatal("the run record changed")
	}
}

// b581HandedOver runs block open and wants one hand-over row with reason,
// written before the run record changed.
func b581HandedOver(t *testing.T, f *closeReviewFixture, reason string, args ...string) {
	t.Helper()
	before, _ := os.ReadFile(b581RunFile(f))
	res := b451RunWith(t, f.home, true, append([]string{"open"}, args...)...)
	if res.code != 127 {
		t.Fatalf("exit=%d stderr=%q; want the hand-over to a Node that cannot start (127)", res.code, res.stderr)
	}
	file, err := os.Open(filepath.Join(f.home, ".code-forge/native/handovers.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var reasons []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var row struct{ Reason string }
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		reasons = append(reasons, row.Reason)
	}
	if len(reasons) != 1 || reasons[0] != reason {
		t.Fatalf("hand-over rows %v; want [%s]", reasons, reason)
	}
	if after, _ := os.ReadFile(b581RunFile(f)); !bytes.Equal(before, after) {
		t.Fatal("the run record changed before the hand-over")
	}
}

// b581SetConfig records cfg as the config snapshot in force.
func b581SetConfig(t *testing.T, f *closeReviewFixture, cfg ledger.Object) {
	t.Helper()
	encoded, err := canon.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	hash := hex.EncodeToString(digest[:])
	b470SetRun(t, f, func(record, _ *ledger.Object) {
		record.Set("config", ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: cfg}}}})
	})
}

func b581Open(acceptance string, extra ...string) []string {
	return append([]string{"B2", "--run", "r-close-fixture", "--level", "L1", "--owned", "card.go", "--acceptance", acceptance}, extra...)
}

// A dependency status that is not a string prints as JavaScript's String().
func TestB581OpenAfterStatusIsNative(t *testing.T) {
	for _, tc := range []struct {
		status any
		label  string
	}{
		{5.0, "5"}, {1.5e300, "1.5e+300"}, {true, "true"}, {false, "false"},
		{[]any{1.0, nil, "a", []any{2.0, 3.0}}, "1,,a,2,3"},
		{ledger.Object{{Key: "x", Value: 1.0}}, "[object Object]"}, {[]any{}, ""},
	} {
		for _, outcome := range []bool{false, true} {
			t.Run(tc.label+map[bool]string{true: "-outcome"}[outcome], func(t *testing.T) {
				f, acceptance := b581Fixture(t)
				b470SetRun(t, f, func(_, entry *ledger.Object) { entry.Set("status", tc.status) })
				args := b581Open(acceptance, "--after", "B1")
				if outcome {
					args = append(args, "--after-outcome", "s1")
				}
				b581Refused(t, f, 1, "block open: block B2: --after dependencies not closed: B1 ("+tc.label+")\n", args...)
			})
		}
	}
}

// A signed claim row whose path is not a string is named as JSON.stringify
// prints it, in the row's own key order.
func TestB581OpenClaimInvalidPathIsNative(t *testing.T) {
	for _, tc := range []struct {
		name  string
		path  any
		shown string
	}{
		{"number", 5.0, "5"}, {"null", nil, "null"}, {"bool", true, "true"},
		{"object", ledger.Object{{Key: "1", Value: 3.0}, {Key: "z", Value: 1.0}, {Key: "a", Value: 2.0}}, `{"1":3,"z":1,"a":2}`},
		{"array", []any{1.0, "x"}, `[1,"x"]`}, {"absent", "-", "undefined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, acceptance := b581Fixture(t)
			row := ledger.Object{{Key: "event", Value: "block.claim"}, {Key: "run", Value: "r-close-fixture"}, {Key: "block", Value: "B2"}}
			if tc.path != "-" {
				row.Set("path", tc.path)
			}
			if _, err := state.WriteSigned(f.home, "r-close-fixture", row); err != nil {
				t.Fatal(err)
			}
			b581Refused(t, f, 1, "block open: owned files: invalid path "+tc.shown+": must be a non-empty string\n", b581Open(acceptance, "--attempt", "2")...)
		})
	}
}

// b581Level appends a signed autopilot level row for B2.
func b581Level(t *testing.T, f *closeReviewFixture) {
	t.Helper()
	row := ledger.Object{{Key: "event", Value: "autopilot.level"}, {Key: "run", Value: "r-close-fixture"}, {Key: "block", Value: "B2"}, {Key: "level", Value: "L1"}}
	if _, err := state.WriteSigned(f.home, "r-close-fixture", row); err != nil {
		t.Fatal(err)
	}
}

// With no --level, the signer key and the ledger are read in Go with
// Node's refusals.
func TestB581OpenNoLevelKeyAndLedgerAreNative(t *testing.T) {
	keyFile := func(f *closeReviewFixture) string {
		return filepath.Join(f.home, ".code-forge/runs/r-close-fixture.key")
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *closeReviewFixture)
		code  int
		want  func(f *closeReviewFixture) string
	}{
		{"none", func(*testing.T, *closeReviewFixture) {}, 2, func(*closeReviewFixture) string {
			return "block open: no --level given and no autopilot level recorded for B2\n"
		}},
		{"no-key", func(t *testing.T, f *closeReviewFixture) {
			if err := os.Remove(keyFile(f)); err != nil {
				t.Fatal(err)
			}
		}, 1, func(*closeReviewFixture) string { return "block open: run r-close-fixture has no signer key\n" }},
		{"key-mode", func(t *testing.T, f *closeReviewFixture) {
			if err := os.Chmod(keyFile(f), 0644); err != nil {
				t.Fatal(err)
			}
		}, 1, func(*closeReviewFixture) string {
			return "block open: run r-close-fixture signer key is accessible to other users (mode 644); expected 600\n"
		}},
		{"key-length", func(t *testing.T, f *closeReviewFixture) {
			if err := os.WriteFile(keyFile(f), []byte("short"), 0600); err != nil {
				t.Fatal(err)
			}
		}, 1, func(*closeReviewFixture) string {
			return "block open: run r-close-fixture signer key is not 32 bytes\n"
		}},
		{"key-directory", func(t *testing.T, f *closeReviewFixture) {
			if err := os.Remove(keyFile(f)); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(keyFile(f), 0700); err != nil {
				t.Fatal(err)
			}
		}, 1, func(*closeReviewFixture) string {
			return "block open: EISDIR: illegal operation on a directory, read\n"
		}},
		{"key-unreadable", func(t *testing.T, f *closeReviewFixture) {
			if err := os.Chmod(keyFile(f), 0200); err != nil {
				t.Fatal(err)
			}
		}, 1, func(f *closeReviewFixture) string {
			return "block open: EACCES: permission denied, open '" + keyFile(f) + "'\n"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, acceptance := b581Fixture(t)
			// the key is read when an autopilot level row names the block
			if tc.name != "none" {
				b581Level(t, f)
			}
			tc.setup(t, f)
			b581Refused(t, f, tc.code, tc.want(f), "B2", "--run", "r-close-fixture", "--owned", "card.go", "--acceptance", acceptance)
		})
	}
}

// A ledger line Go's reader rejects (Node prints JSON.parse's text) still
// hands over, under its own reason, before anything is written.
func TestB581OpenNoLevelTornLedgerHandsOver(t *testing.T) {
	f, acceptance := b581Fixture(t)
	matches, _ := filepath.Glob(filepath.Join(f.home, ".code-forge/ledger/*.jsonl"))
	if len(matches) == 0 {
		t.Fatal("no ledger file")
	}
	file, err := os.OpenFile(matches[0], os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.WriteString("{x\n")
	_ = file.Close()
	b581Level(t, f)
	b581HandedOver(t, f, "open-level-ledger", "B2", "--run", "r-close-fixture", "--owned", "card.go", "--acceptance", acceptance)
}

// --brief errors are Node's realpath and readFile texts.
func TestB581OpenBriefErrorsAreNative(t *testing.T) {
	for _, tc := range []struct {
		name  string
		brief func(t *testing.T, f *closeReviewFixture) string
		code  int
		want  func(f *closeReviewFixture) string
	}{
		{"loop", func(t *testing.T, f *closeReviewFixture) string {
			_ = os.Symlink("l2", filepath.Join(f.top, "l1"))
			_ = os.Symlink("l1", filepath.Join(f.top, "l2"))
			return "l1"
		}, 1, func(*closeReviewFixture) string {
			return "block open: ELOOP: too many symbolic links encountered, realpath 'l1'\n"
		}},
		{"too-long", func(*testing.T, *closeReviewFixture) string { return strings.Repeat("n", 300) }, 1, func(*closeReviewFixture) string {
			return "block open: ENAMETOOLONG: name too long, realpath '" + strings.Repeat("n", 300) + "'\n"
		}},
		{"workspace", func(_ *testing.T, f *closeReviewFixture) string { return f.top }, 1, func(*closeReviewFixture) string {
			return "block open: EISDIR: illegal operation on a directory, read\n"
		}},
		{"unreadable", func(t *testing.T, f *closeReviewFixture) string {
			closeFixtureWrite(t, filepath.Join(f.top, "b.md"), []byte("brief\n"))
			if err := os.Chmod(filepath.Join(f.top, "b.md"), 0200); err != nil {
				t.Fatal(err)
			}
			return "b.md"
		}, 1, func(f *closeReviewFixture) string {
			return "block open: EACCES: permission denied, open '" + filepath.Join(f.top, "b.md") + "'\n"
		}},
		{"outside", func(t *testing.T, f *closeReviewFixture) string {
			closeFixtureWrite(t, filepath.Join(f.home, "b.md"), []byte("brief\n"))
			return filepath.Join(f.home, "b.md")
		}, 2, func(*closeReviewFixture) string { return "block open: --brief must be a file inside the workspace\n" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, acceptance := b581Fixture(t)
			b581Refused(t, f, tc.code, tc.want(f), b581Open(acceptance, "--brief", tc.brief(t, f))...)
		})
	}
}

// An empty proof.worktree.base is a configured value, not an absent one.
func TestB581OpenWorktreeEmptyBaseIsNative(t *testing.T) {
	for _, dir := range []any{nil, ""} {
		t.Run(map[bool]string{true: "base", false: "dir-and-base"}[dir == nil], func(t *testing.T) {
			f, acceptance := b581Fixture(t)
			wt := ledger.Object{{Key: "base", Value: ""}}
			if dir != nil {
				wt.Set("dir", dir)
			}
			b581SetConfig(t, f, ledger.Object{{Key: "proof", Value: ledger.Object{{Key: "isolation", Value: "worktree"}, {Key: "worktree", Value: wt}}}})
			b581Refused(t, f, 1, "block open: block B2: proof.worktree.base \"\" is not a commit of the repository; nothing was recorded\n", b581Open(acceptance)...)
		})
	}
}

// Acceptance YAML: the yaml package's message alone, as Node prints it;
// forms with no byte-equal port hand over before anything is written.
func TestB581OpenAcceptanceYAML(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
	}{
		{"duplicate-key", "- clause: c\n  clause: d\n  tests: [t]\n", "block open: Map keys must be unique\n"},
		{"duplicate-number-key", "- clause: c\n  tests: [t]\n  m: {1: a, 1.0: b}\n", "block open: Map keys must be unique\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, acceptance := b581Fixture(t)
			closeFixtureWrite(t, acceptance, []byte(tc.text))
			b581Refused(t, f, 1, tc.want, b581Open(acceptance)...)
		})
	}
	for _, tc := range []struct{ name, text string }{
		{"tag", "- clause: !custom c\n  tests: [t]\n"},
		{"core-tag", "- clause: c\n  tests: [t]\n  f: !!float 1\n"},
		{"directive", "%YAML 1.2\n---\n- clause: c\n  tests: [t]\n"},
		{"tab", "- clause: c\n\ttests: [t]\n"},
		{"bom", "\ufeff- clause: c\n  tests: [t]\n"},
		{"not-utf8", "- clause: c\xff\n  tests: [t]\n"},
		{"collection-key", "- clause: c\n  tests: [t]\n  ? [a]\n  : b\n"},
		{"multi-line-quote", "- \"clause\": \"multi\n  line\"\n  tests: [t]\n"},
		{"null-keys", "- clause: c\n  tests: [t]\n  m: {: d, null: g}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, acceptance := b581Fixture(t)
			closeFixtureWrite(t, acceptance, []byte(tc.text))
			b581HandedOver(t, f, "open-acceptance-yaml", b581Open(acceptance)...)
		})
	}
}

const b581Pipeline = "root: .\nsteps:\n  - id: s1\n    skill: k\n    outcome: out/s1.json\n    approval: none\n"

// --after-outcome: the pipeline and the outcome contract are read and
// checked in Go with Node's problems; AJV's wording hands over.
func TestB581OpenAfterOutcomeIsNative(t *testing.T) {
	pipeline := func(t *testing.T, f *closeReviewFixture, text string) {
		closeFixtureWrite(t, filepath.Join(f.top, "code-forge.pipeline.yml"), []byte(text))
	}
	outcome := func(t *testing.T, f *closeReviewFixture, text string) {
		pipeline(t, f, b581Pipeline)
		closeFixtureWrite(t, filepath.Join(f.top, "out/s1.json"), []byte(text))
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, f *closeReviewFixture)
		want  func(f *closeReviewFixture) string
	}{
		{"pipeline-directory", func(t *testing.T, f *closeReviewFixture) {
			_ = os.Mkdir(filepath.Join(f.top, "code-forge.pipeline.yml"), 0700)
		}, func(*closeReviewFixture) string { return "--after-outcome: Cannot read pipeline (EISDIR)" }},
		{"pipeline-unreadable", func(t *testing.T, f *closeReviewFixture) {
			pipeline(t, f, b581Pipeline)
			_ = os.Chmod(filepath.Join(f.top, "code-forge.pipeline.yml"), 0200)
		}, func(*closeReviewFixture) string { return "--after-outcome: Cannot read pipeline (EACCES)" }},
		{"pipeline-yaml", func(t *testing.T, f *closeReviewFixture) { pipeline(t, f, "root: .\nroot: .\n") },
			func(*closeReviewFixture) string {
				return "--after-outcome: Invalid pipeline YAML: Map keys must be unique"
			}},
		{"outcome-unreadable", func(t *testing.T, f *closeReviewFixture) {
			outcome(t, f, `{"skill":"k","status":"approved","files":[]}`)
			_ = os.Chmod(filepath.Join(f.top, "out/s1.json"), 0200)
		}, func(f *closeReviewFixture) string {
			return "--after-outcome step s1: Cannot read outcome \"" + filepath.Join(f.top, "out/s1.json") + "\" (EACCES)"
		}},
		{"outcome-directory", func(t *testing.T, f *closeReviewFixture) {
			pipeline(t, f, b581Pipeline)
			_ = os.MkdirAll(filepath.Join(f.top, "out/s1.json"), 0700)
		}, func(f *closeReviewFixture) string {
			return "--after-outcome step s1: Cannot read outcome \"" + filepath.Join(f.top, "out/s1.json") + "\" (EISDIR)"
		}},
		{"outcome-file-missing", func(t *testing.T, f *closeReviewFixture) {
			outcome(t, f, `{"skill":"k","status":"approved","files":[{"path":"q","sha256":"x"}]}`)
		}, func(*closeReviewFixture) string { return "--after-outcome step s1: file \"q\": missing file" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, acceptance := b581Fixture(t)
			tc.setup(t, f)
			b581Refused(t, f, 1, "block open: block B2: "+tc.want(f)+"\n", b581Open(acceptance, "--after-outcome", "s1")...)
		})
	}
	for _, tc := range []struct {
		name, reason string
		setup        func(t *testing.T, f *closeReviewFixture)
	}{
		{"pipeline-schema", "open-pipeline-form", func(t *testing.T, f *closeReviewFixture) { pipeline(t, f, "steps: []\n") }},
		{"pipeline-yaml-form", "open-pipeline-form", func(t *testing.T, f *closeReviewFixture) { pipeline(t, f, "root: !x .\n") }},
		{"outcome-schema", "open-outcome-form", func(t *testing.T, f *closeReviewFixture) {
			outcome(t, f, `{"skill":1,"status":"approved","files":[]}`)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, acceptance := b581Fixture(t)
			tc.setup(t, f)
			b581HandedOver(t, f, tc.reason, b581Open(acceptance, "--after-outcome", "s1")...)
		})
	}
}

// b581RunBlockNoHome runs one block verb with HOME unset and Node set to a
// path that cannot start.
func b581RunBlockNoHome(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "0")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	t.Setenv("HOME", "")
	if err := os.Unsetenv("HOME"); err != nil {
		t.Fatal(err)
	}
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	stdout, stderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	outC, errC := make(chan string), make(chan string)
	go func() { data, _ := io.ReadAll(outR); outC <- string(data) }()
	go func() { data, _ := io.ReadAll(errR); errC <- string(data) }()
	code := runBlock(args)
	os.Stdout, os.Stderr = stdout, stderr
	_ = outW.Close()
	_ = errW.Close()
	return code, <-outC, <-errC
}

// HOME unset: Node's os.homedir() falls back to the user database; Go
// reads the same home there. A failed lookup hands over.
func TestB581OpenHomeUnset(t *testing.T) {
	f, acceptance := b581Fixture(t)
	saved := boUserHome
	t.Cleanup(func() { boUserHome = saved })
	boUserHome = func() (string, error) { return f.home, nil }
	before, _ := os.ReadFile(b581RunFile(f))
	code, stdout, stderr := b581RunBlockNoHome(t, append([]string{"open"}, b581Open(acceptance)...)...)
	if code != 0 || stderr != "" || !strings.Contains(stdout, "B2") {
		t.Fatalf("exit=%d stdout=%q stderr=%q; want block B2 opened", code, stdout, stderr)
	}
	record, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(b581RunFile(f)); bytes.Equal(before, after) || record.Get("blocks").(ledger.Object).Get("B2") == nil {
		t.Fatal("block B2 is not in the run record")
	}
	boUserHome = func() (string, error) { return "", errors.New("no user") }
	if code, _, _ := b581RunBlockNoHome(t, append([]string{"open"}, b581Open(acceptance, "--attempt", "2")...)...); code != 127 {
		t.Fatalf("exit=%d; want the hand-over to a Node that cannot start (127)", code)
	}
}

// A ready outcome and an autopilot level open the block in Go: the entry
// records the outcome's sha256 and the level the signed row names.
func TestB581OpenReadyOutcomeAndAutopilotLevel(t *testing.T) {
	f, acceptance := b581Fixture(t)
	b581Level(t, f)
	contract := []byte(`{"skill":"k","status":"approved","files":[]}`)
	closeFixtureWrite(t, filepath.Join(f.top, "code-forge.pipeline.yml"), []byte(b581Pipeline))
	closeFixtureWrite(t, filepath.Join(f.top, "out/s1.json"), contract)
	res := b451Run(t, f.home, "open", "B2", "--run", "r-close-fixture", "--owned", "card.go", "--acceptance", acceptance, "--after-outcome", "s1")
	if res.code != 0 || res.stderr != "" {
		t.Fatalf("exit=%d stderr=%q; want block B2 opened", res.code, res.stderr)
	}
	record, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil {
		t.Fatal(err)
	}
	entry, _ := record.Get("blocks").(ledger.Object).Get("B2").(ledger.Object)
	sum := sha256.Sum256(contract)
	got, _ := ledger.Stringify(entry.Get("after_outcome"), false)
	if want := `[{"step":"s1","sha256":"` + hex.EncodeToString(sum[:]) + `"}]`; string(got) != want || entry.Get("level") != "L1" {
		t.Fatalf("after_outcome=%s level=%v; want %s and L1", got, entry.Get("level"), want)
	}
}
