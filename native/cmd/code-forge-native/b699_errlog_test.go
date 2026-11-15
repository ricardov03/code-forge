package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// B699 goldens: recorded from Node's src/util/error-log.mjs (buildEntry for a
// failure, logWarning for a warning) with the slug "acme", exit 1 and each
// message as the last stderr text. Columns: message, the logged (scrubbed,
// JS-trimmed) message, cleaned counts, then the fingerprint for block (sub
// null), block waive, review-file, gates run and a spawn_retry warning.
var b699Goldens = []struct{ in, message, cleaned, block, waive, reviewFile, gates, warning string }{
	{"see op://vault/item/field\u00a0tail", "see op://<ref>\u00a0tail", "[{\"rule\":\"op_ref\",\"count\":1}]", "42f9e1ed9e7c", "03c3af14b7a5", "2e813d388c0b", "54dbc46aba1a", "11b5a2d2925a"},
	{"ref op://v/i/f\u2028next and op://x\u3000y and op://x\u000by and op://x\ufeffy and op://p\u2003q", "ref op://<ref>\u2028next and op://<ref>\u3000y and op://<ref>\u000by and op://<ref>\ufeffy and op://<ref>\u2003q", "[{\"rule\":\"op_ref\",\"count\":5}]", "38ac4278a5fa", "4092a6559556", "f66581231230", "c30b26255780", "1dec6946875c"},
	{"x_abcdefghijklmnopqrstuvwxyz0 end", "x_abcdefghijklmnopqrstuvwxyz0 end", "[]", "637bd83cc80a", "11467bdd67ff", "c6764b7c87b5", "30f2c199dc4e", "586b632811f6"},
	{"id x_abcdefghijklmnopqrstuvwxyz and abcdefghijklmnopqrstuvwxyz-x and --abcdefghijklmnopqrstuvwxyz", "id x_<item-id> and <item-id>-x and --<item-id>", "[{\"rule\":\"item_id\",\"count\":3}]", "7e21d1f3335a", "3f7a47113393", "9fecd8875821", "3ac5853aab3e", "5f19c108f936"},
	{"Aabcdefghijklmnopqrstuvwxyz and 0abcdefghijklmnopqrstuvwxyz1 and abcdefghijklmnopqrstuvwxyz", "Aabcdefghijklmnopqrstuvwxyz and 0abcdefghijklmnopqrstuvwxyz1 and <item-id>", "[{\"rule\":\"item_id\",\"count\":1}]", "9bd9c968811b", "5a72daecb7d9", "4ba8e6e828d6", "addaaf30d878", "6ae6642da7b3"},
	{"acme-x acme_y <acme> acme://z acme ok", "acme-x acme_y <acme> acme://z <slug> ok", "[{\"rule\":\"project_name\",\"count\":1}]", "5854743985e0", "bd662d596c2c", "b421a9230f43", "aa4cd9b88a7b", "17900bf96e70"},
	{"bad\u000bvalue\u00a0here 12345.6x 2026-11-15 \u2003 ", "bad\u000bvalue\u00a0here 12345.6x 2026-11-15", "[]", "9cfbad95a17b", "73cc3cec9e79", "705903b96baf", "6584d487b3d8", "3a5e34baef6c"},
	{"\u0085lead and trail\u0085", "\u0085lead and trail\u0085", "[]", "89d30feb3ae3", "fe18ab8135af", "06f89149f0f4", "0238ca686546", "828cebed441a"},
	{"\ufeff  ready: block B1\u00a0is gone\u000b", "ready: block B1\u00a0is gone", "[]", "6fc33801a970", "ea4052c0329a", "74981ba84d3c", "67f609b88e4a", "60d79b11c3df"},
	{"it's 'quoted' x'y' a/b\u00a0c/d tail\u2009\u2009end 1234567 deadbeef9", "it's 'quoted' x'y' a/b\u00a0c/d tail\u2009\u2009end 1234567 deadbeef9", "[]", "6073b11b8ac9", "482ae3e6c484", "add00ec078a2", "fd91294c7715", "3c50628a7912"},
}

// b699LogFixture: a temp HOME and a project dir with the slug "acme" as the
// working directory, logging on.
func b699LogFixture(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, proj := filepath.Join(root, "home"), filepath.Join(root, "proj")
	for _, dir := range []string{home, proj} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(proj, ".code-forge.yml"), []byte("project:\n  slug: acme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	b699Chdir(t, proj)
	return home
}

// b699LastEntry reads the last errors.jsonl line and removes the log.
func b699LastEntry(t *testing.T, home string) (message, cleaned, fp string) {
	t.Helper()
	file := filepath.Join(home, ".code-forge", "logs", "errors.jsonl")
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("no error log line: %v", err)
	}
	_ = os.Remove(file)
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	var entry struct {
		Message string          `json:"message"`
		Cleaned json.RawMessage `json:"cleaned"`
		Fp      string          `json:"fp"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
		t.Fatal(err)
	}
	return entry.Message, string(entry.Cleaned), entry.Fp
}

func b699Check(t *testing.T, home, route, in, wantMessage, wantCleaned, wantFp string) {
	t.Helper()
	message, cleaned, fp := b699LastEntry(t, home)
	if message != wantMessage || cleaned != wantCleaned || fp != wantFp {
		t.Errorf("%s %q:\n got message=%q cleaned=%s fp=%s\nwant message=%q cleaned=%s fp=%s (Node)", route, in, message, cleaned, fp, wantMessage, wantCleaned, wantFp)
	}
}

// Failures: JS \s in the op:// rule, the item-id boundary (letters and digits
// only), JS trim (U+FEFF, NBSP, \v trimmed; U+0085 kept) and Node's
// normalizeMessage for every route that logs a failure in Go.
func TestB699FailureScrubAndFingerprintMatchNode(t *testing.T) {
	home := b699LogFixture(t)
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	for _, g := range b699Goldens {
		nativeVerbFailure("block", "", nil, 1, g.in, []any{})
		b699Check(t, home, "block", g.in, g.message, g.cleaned, g.block)
		nativeVerbFailure("block", "waive", nil, 1, g.in, []any{})
		b699Check(t, home, "block waive", g.in, g.message, g.cleaned, g.waive)
		reviewWaitFailure(nil, 1, g.in, []any{})
		b699Check(t, home, "review-file", g.in, g.message, g.cleaned, g.reviewFile)
		stderr := os.Stderr
		os.Stderr = devnull
		gatesLastMessage = nil
		gatesErrf("%s", g.in)
		os.Stderr = stderr
		gatesFailure(nil, 1, gatesLastMessage, []any{})
		b699Check(t, home, "gates run", g.in, g.message, g.cleaned, g.gates)
	}
}

// Warnings: logWarning's clean(message.trim()) and fingerprint, for the
// spawn/worker warning writer and the doctor/keys one.
func TestB699WarningScrubAndFingerprintMatchNode(t *testing.T) {
	home := b699LogFixture(t)
	verb := spawnWarnVerb
	spawnWarnVerb = "spawn"
	defer func() { spawnWarnVerb = verb }()
	for _, g := range b699Goldens {
		spawnWarnMu.Lock()
		spawnWarned = map[string]bool{}
		spawnWarnMu.Unlock()
		spawnLogWarning("spawn_retry", g.in)
		b699Check(t, home, "spawn warning", g.in, g.message, g.cleaned, g.warning)
		logNativeWarning("spawn", "spawn_retry", g.in)
		b699Check(t, home, "native warning", g.in, g.message, g.cleaned, g.warning)
	}
}

// Path forms like Node's pathVariants: the given HOME (a symlink) and its real
// path, the real working directory unless it is a home form, longest first,
// each replaced only where no letter, digit, _ or - follows. Recorded from
// Node buildEntry with HOME=<root>/homelink -> <root>/home, cwd <root>/home/proj.
func TestB699PathScrubMatchesNode(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h, l, c := filepath.Join(root, "home"), filepath.Join(root, "homelink"), filepath.Join(root, "home", "proj")
	if err := os.MkdirAll(c, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(h, l); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c, ".code-forge.yml"), []byte("project:\n  slug: acme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", l)
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	b699Chdir(t, c)
	expand := strings.NewReplacer("{C}", c, "{H}", h, "{L}", l)
	for _, g := range []struct{ in, message, cleaned, fp string }{
		{"{C}/src/a.go failed", "<project>/src/a.go failed", `[{"rule":"project_path","count":1}]`, "07ad5a8f1840"},
		{"{H}/.code-forge and {L}/x and {H}-other and {H}_x and {H}", "~/.code-forge and ~/x and {H}-other and {H}_x and ~", `[{"rule":"home","count":3}]`, "f00cc5c93dd9"},
		{"{C}x and {C}. and {C}\u00a0next {L}/proj/y", "~/projx and <project>. and <project>\u00a0next ~/proj/y", `[{"rule":"home","count":2},{"rule":"project_path","count":2}]`, "c0b0967fd4dd"},
		{"cd {C}\\ and {H}/", "cd <project>\\ and ~/", `[{"rule":"home","count":1},{"rule":"project_path","count":1}]`, "0a82bef449f0"},
	} {
		gatesFailure([]string{"run"}, 1, expand.Replace(g.in), []any{})
		b699Check(t, l, "gates run", g.in, expand.Replace(g.message), g.cleaned, g.fp)
	}
}

func b699Chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
}

// b699Call runs the CLI in a child process like preflightFixture.call, with
// the error log on and no Node (a hand-over exits 127).
func b699Call(f *preflightFixture, args ...string) (int, string, string) {
	f.t.Helper()
	exe, err := os.Executable()
	if err != nil {
		f.t.Fatal(err)
	}
	encoded, _ := json.Marshal(args)
	cmd := exec.Command(exe, "-test.run=^TestGatesProcess$")
	cmd.Dir = f.repo
	cmd.Env = append(os.Environ(), "HOME="+f.home, "CODE_FORGE_NO_ERROR_LOG=", "CODE_FORGE_HANDOVER_LOG=0", "CODE_FORGE_NODE=/nonexistent-code-forge-node", "CF_GATES_PROCESS=1", "CF_GATES_ARGS="+string(encoded))
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	code := 0
	if err := cmd.Run(); err != nil {
		e, ok := err.(*exec.ExitError)
		if !ok {
			f.t.Fatal(err)
		}
		code = e.ExitCode()
	}
	return code, out.String(), errOut.String()
}

// block ready/reopen/waive refusals with non-ASCII text or (waive) an
// apostrophe were handed to Node only so the error log matched; they now log
// in Go with Node's message, flags and fingerprint (from Node buildEntry).
func TestB699BlockPreviewRefusalsLogInGo(t *testing.T) {
	f := newReadyFixture(t)
	type row struct {
		Sub     any      `json:"sub"`
		Message string   `json:"message"`
		Flags   []string `json:"flags"`
		Exit    int      `json:"exit"`
		Fp      string   `json:"fp"`
	}
	for _, c := range []struct {
		args   []string
		code   int
		stderr string
		want   row
	}{
		{[]string{"ready", "Bé", "--run", f.run}, 1, "block ready: block Bé is not in run r-ready\n",
			row{nil, "block ready: block Bé is not in run r-ready", []string{"--run"}, 1, "324efab0c2fd"}},
		{[]string{"reopen", "Bé\u00a0x", "--run", f.run, "--file", "sample.txt", "--reason", "it's", "--yes"}, 1, "block reopen: block Bé\u00a0x is not in run r-ready\n",
			row{nil, "block reopen: block Bé\u00a0x is not in run r-ready", []string{"--run", "--file", "--reason", "--yes"}, 1, "337c56697dc0"}},
		{[]string{"waive", "Bé", "F1", "--run", f.run, "--file", "sample.txt", "--reason", "r", "--yes"}, 1, "block waive: block Bé is not in run r-ready\n",
			row{"waive", "block waive: block Bé is not in run r-ready", []string{"--run", "--file", "--reason", "--yes"}, 1, "ce6269b90e9b"}},
		{[]string{"ready", "B1\ufeff", "--run", f.run}, 1, "block ready: block B1\ufeff is not in run r-ready\n",
			row{nil, "block ready: block B1\ufeff is not in run r-ready", []string{"--run"}, 1, "f63d59ce12b5"}},
		{[]string{"waive", "B1", "F1", "--run", f.run, "--file", "it's.txt", "--reason", "r", "--yes"}, 1, "block waive: waive-not-owned: block B1 does not own it's.txt; nothing was recorded\n",
			row{"waive", "block waive: waive-not-owned: block B1 does not own it's.txt; nothing was recorded", []string{"--run", "--file", "--reason", "--yes"}, 1, "9f9a6065525c"}},
	} {
		code, stdout, stderr := b699Call(f, append([]string{"block"}, c.args...)...)
		if code != c.code || stdout != "" || stderr != c.stderr {
			t.Errorf("%q: code=%d stdout=%q stderr=%q; want code=%d stderr=%q in Go (no hand-over)", c.args, code, stdout, stderr, c.code, c.stderr)
			continue
		}
		file := filepath.Join(f.home, ".code-forge", "logs", "errors.jsonl")
		data, err := os.ReadFile(file)
		_ = os.Remove(file)
		var got row
		if err != nil || json.Unmarshal(data, &got) != nil {
			t.Errorf("%q: error log %q (%v)", c.args, data, err)
			continue
		}
		if got.Sub != c.want.Sub || got.Message != c.want.Message || strings.Join(got.Flags, " ") != strings.Join(c.want.Flags, " ") || got.Exit != c.want.Exit || got.Fp != c.want.Fp {
			t.Errorf("%q: logged %+v; want %+v (Node)", c.args, got, c.want)
		}
	}
}
