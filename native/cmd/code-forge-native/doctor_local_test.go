package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gitx"
)

// B504: the ported doctor checks return Node's rows. Each want list is the
// output of src/doctor/local.mjs / probes.mjs for the same config (Node
// function script run in the scratchpad).

func b504Config(t *testing.T, yaml string) (map[string]any, config.Ordered) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".code-forge.yml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, order, err := config.LoadOrdered(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, order
}

func b504Bin(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func b504Rows(rows []doctorRow) [][4]string {
	out := [][4]string{}
	for _, r := range rows {
		out = append(out, [4]string{r.id, r.status, r.label, r.detail})
	}
	return out
}

const (
	b504Solo  = "UNVERIFIED (no Solo probe in this version)"
	b504Slots = "6 tickets at once; provider slots anthropic 4 · openai 2 · xai 2"
)

func TestB504CommandRows(t *testing.T) {
	bin := b504Bin(t, "make", "code-forge")
	cfg, order := b504Config(t, "gates:\n  test: [make, test]\n  '10': [nope-x]\n  '2': {argv: [make]}\n  lint: []\n  fmt: x\n  z: [null]\n")
	want := [][4]string{
		{"path", "OK", "code-forge", "on PATH"},
		{"solo", "INFO", "solo", b504Solo},
		{"gate.2", "OK", "gate 2", "make on PATH"},
		{"gate.10", "FAIL", "gate 10", "nope-x not on PATH"},
		{"gate.test", "OK", "gate test", "make on PATH"},
		{"gate.z", "FAIL", "gate z", "null not on PATH"},
	}
	if got := b504Rows(dfCommandRows(cfg, order, bin)); !reflect.DeepEqual(got, want) {
		t.Errorf("gates:\n got %q\nwant %q", got, want)
	}
	want = [][4]string{
		{"path", "WARN", "code-forge", "not on PATH (the skill shim calls it)"},
		{"solo", "INFO", "solo", b504Solo},
		{"gate.t", "FAIL", "gate t", "make not on PATH"},
	}
	cfg, order = b504Config(t, "gates: {t: [make]}\n")
	if got := b504Rows(dfCommandRows(cfg, order, t.TempDir())); !reflect.DeepEqual(got, want) {
		t.Errorf("empty PATH:\n got %q\nwant %q", got, want)
	}
	want = [][4]string{
		{"path", "OK", "code-forge", "on PATH"},
		{"solo", "INFO", "solo", b504Solo},
		{"gates", "WARN", "gates", "no gate command configured"},
	}
	if got := b504Rows(dfCommandRows(nil, config.Ordered{}, bin)); !reflect.DeepEqual(got, want) {
		t.Errorf("no gates:\n got %q\nwant %q", got, want)
	}
}

func TestB504CoderSandboxRows(t *testing.T) {
	full := "profile full runs the Codex coder with -s danger-full-access: it can write anywhere you can; only the forbidden list, the transcript grep and the gates limit it"
	for _, c := range []struct {
		name, yaml string
		want       [][4]string
	}{
		{"none", "provider: openai\n", [][4]string{}},
		{"workspace", "provider: openai\nlevels: {L0: {model: a}}\ncoder: {sandbox: {}}\n", [][4]string{
			{"coder-sandbox", "INFO", "coder sandbox", "profile workspace; applies to the Codex (openai) coder only"},
		}},
		{"toolchain", "provider: anthropic\nlevels: {L0: {model: a}, L1: {model: b, provider: xai}}\ncoder: {sandbox: {profile: toolchain, add_dirs: [/x]}}\n", [][4]string{
			{"coder-sandbox", "INFO", "coder sandbox", "profile toolchain, 1 extra writable dir; applies to the Codex (openai) coder only"},
			{"coder-sandbox-no-effect", "INFO", "coder sandbox", "coder.sandbox has no effect on the Claude (anthropic) and Grok (xai) coders: the argv does not change"},
		}},
		{"full", "provider: openai\nlevels: {L2: {model: a, fallback: [{provider: anthropic}]}}\ncoder: {sandbox: {profile: full}}\n", [][4]string{
			{"coder-sandbox", "INFO", "coder sandbox", "profile full; applies to the Codex (openai) coder only"},
			{"coder-sandbox-no-effect", "INFO", "coder sandbox", "coder.sandbox has no effect on the Claude (anthropic) coder: the argv does not change"},
			{"coder-sandbox-full", "WARN", "coder sandbox", full},
		}},
		{"unknown profile", "coder: {sandbox: {profile: other}}\n", [][4]string{}},
	} {
		cfg, _ := b504Config(t, c.yaml)
		if got := b504Rows(dfCoderSandboxRows(cfg)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestB504ReviewConcurrencyRow(t *testing.T) {
	if got := dfReviewConcurrencyRow(nil, config.Ordered{}).detail; got != b504Slots {
		t.Errorf("default: %q", got)
	}
	cfg, order := b504Config(t, "review: {max_parallel: 1, provider_concurrency: {zeta: 3, '1': 2, openai: 5}}\n")
	want := "6 tickets at once; provider slots anthropic 4 · openai 5 · xai 2 · 1 2 · zeta 3"
	if got := dfReviewConcurrencyRow(cfg, order); got.id != "review-concurrency" || got.status != "INFO" || got.detail != want {
		t.Errorf("got %+v, want %q", got, want)
	}
}

func TestB504TmpAndLedgerRows(t *testing.T) {
	if got := b504Rows(dfTmpRows("rel", true, t.TempDir())); !reflect.DeepEqual(got, [][4]string{{"tmp", "FAIL", "tmp", "tmp.root is not an absolute path"}}) {
		t.Errorf("relative tmp.root: %q", got)
	}
	home := t.TempDir()
	if got := dfLedgerRow(home); got.status != "OK" || got.detail != "writable" {
		t.Errorf("ledger: %+v", got)
	}
	home = t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".code-forge"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".code-forge", "ledger"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := dfLedgerRow(home); got.status != "FAIL" || got.detail != "not writable (EEXIST)" {
		t.Errorf("ledger is a file: %+v", got)
	}
}

func TestB504WorktreeIndexingRow(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	top := filepath.Join(base, "top")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", top, "-c", "user.email=a@b", "-c", "user.name=a"}, args...)...)
		cmd.Env = gitx.ChildEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
	}
	if err := os.MkdirAll(top, 0o700); err != nil {
		t.Fatal(err)
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "i")
	warn := func(dir string) [][4]string {
		return [][4]string{{"worktree-indexing", "WARN", "worktree indexing", dir + " may be indexed by Spotlight.\nproof.worktree.dir: ../top-worktrees.noindex\nTo move the old folder, finish or end the runs, then mv the folder to the .noindex path; or remove it."}}
	}
	str := func(s string) *string { return &s }
	branch := "cf/{run}/{block}"
	for _, c := range []struct {
		name string
		dir  *string
		run  string
		want [][4]string
	}{
		{"default", nil, "", [][4]string{}},
		{"relative dir", str("../wt"), "", warn(filepath.Join(base, "wt"))},
		{"noindex dir", str("x.noindex/a"), "", [][4]string{}},
		{"absolute dir", str("/abs/wt"), "", warn("/abs/wt")},
		{"run, no old worktree", nil, "r1", [][4]string{}},
	} {
		got, ok := dfWorktreeIndexingRow(top, c.dir, branch, c.run)
		if !ok || !reflect.DeepEqual(b504Rows(got), c.want) {
			t.Errorf("%s: ok=%v\n got %q\nwant %q", c.name, ok, b504Rows(got), c.want)
		}
	}
	git("worktree", "add", "-q", "-b", "cf/r1/B1", filepath.Join(base, "top-worktrees", "B1"))
	got, ok := dfWorktreeIndexingRow(top, nil, branch, "r1")
	if want := warn(filepath.Join(base, "top-worktrees")); !ok || !reflect.DeepEqual(b504Rows(got), want) {
		t.Errorf("old worktree of the run: ok=%v\n got %q\nwant %q", ok, b504Rows(got), want)
	}
}

// A config that loads but fails validation still goes to Node, named.
func TestB504InvalidConfigKeepsHandover(t *testing.T) {
	b474Project(t, "provider: openai\nengine: subprocess\nreview: {multimodel: true, second_provider: openai}\nlevels:\n  L0: {model: gpt-6-luna, effort: max}\n  L1: {model: gpt-6-luna, effort: max}\n  L2: {model: gpt-6-sol}\n  L3: {model: gpt-6-astra}\n", 0)
	home := os.Getenv("HOME")
	expectHandover(t, "doctor-full-invalid-config", "doctor")
	if _, err := os.Stat(filepath.Join(home, ".code-forge", "ledger")); err == nil {
		t.Error("Go made the ledger folder before it handed over")
	}
}

func TestB504NativeAndDaemonRows(t *testing.T) {
	_, repo := b474Project(t, "", 0)
	t.Setenv("CODE_FORGE_NATIVE", "0")
	if got, reason := dfNativeRow(); reason != 0 || got.status != "INFO" || got.detail != "not found, using Node" {
		t.Errorf("native: %+v %v", got, reason)
	}
	if got, reason := dfDaemonRow(repo); reason != 0 || got.detail != "off" || got.label != "native daemon" {
		t.Errorf("daemon: %+v %v", got, reason)
	}
}
