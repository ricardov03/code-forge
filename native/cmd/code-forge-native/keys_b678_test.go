package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// B678: `keys set <name>` (hidden prompt) and the 1Password flows of
// `keys set --op` / `keys test --ref` run in Go. Goldens are Node's own
// output (bin/code-forge on an 80x24 xterm-256color pty, and non-TTY runs
// with a fake op on PATH), recorded on 2026-11-14 with a temp HOME and the
// file backend. No test reads the real keychain or the real op.

// Node's transcripts (CRLF of the pty as LF; @MJS@ is bin/code-forge.mjs).
const (
	b678Typed = "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l\u001b[90m│\u001b[39m\n\u001b[36m◆\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[36m│\u001b[39m  \u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m└\u001b[39m\n\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪▪▪▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[1B\u001b[J\u001b[32m◇\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[90m│\u001b[39m  \u001b[2m▪▪▪▪▪▪\u001b[22m\n\u001b[?25hstored foo in file (source=user, expires=never)\n"
	b678CtrlC = "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l\u001b[90m│\u001b[39m\n\u001b[36m◆\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[36m│\u001b[39m  \u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m└\u001b[39m\n\u001b[999D\u001b[4A\u001b[1B\u001b[J\u001b[31m■\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[90m│\u001b[39m  \n\u001b[?25hfoo: nothing stored\n"
	b678Empty = "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l\u001b[90m│\u001b[39m\n\u001b[36m◆\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[36m│\u001b[39m  \u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m└\u001b[39m\n\u001b[999D\u001b[4A\u001b[1B\u001b[J\u001b[32m◇\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[90m│\u001b[39m  \n\u001b[?25hfoo: nothing stored\n"
	b678CtrlD = "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l\u001b[90m│\u001b[39m\n\u001b[36m◆\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[36m│\u001b[39m  \u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m└\u001b[39m\nWarning: Detected unsettled top-level await at file://@MJS@:212\n  const code = await run(process.argv.slice(2), { configureTmpRoot: true });\n               ^\n\n\n\n"
	b678Bs    = "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n\u001b[?25l\u001b[90m│\u001b[39m\n\u001b[36m◆\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[36m│\u001b[39m  \u001b[7m\u001b[8m_\u001b[28m\u001b[27m\n\u001b[36m└\u001b[39m\n\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[2B\u001b[2K\u001b[G\u001b[36m│\u001b[39m  ▪▪\u001b[7m\u001b[8m_\u001b[28m\u001b[27m\u001b[2B\u001b[999D\u001b[4A\u001b[1B\u001b[J\u001b[32m◇\u001b[39m  Value for key \"foo\" (input hidden)\n\u001b[90m│\u001b[39m  \u001b[2m▪▪\u001b[22m\n\u001b[?25hstored foo in file (source=user, expires=never)\n"
)

// b678PromptHome is the file store in a temp HOME on the recorded terminal.
func b678PromptHome(t *testing.T) (string, string, string) {
	t.Helper()
	dir, project := b456KeysHome(t, "", nil)
	t.Setenv("TERM", "xterm-256color")
	b528Unset(t, "NO_COLOR", "FORCE_COLOR", "NODE_DISABLE_COLORS", "CI", "TEAMCITY_VERSION", "ACCESSIBLE", "NO_UNICODE")
	repo := b458Repo(t)
	saved := upgradePackageRoot
	upgradePackageRoot = func() (string, bool) { return repo, true }
	t.Cleanup(func() { upgradePackageRoot = saved })
	mjs, err := filepath.EvalSymlinks(filepath.Join(repo, "bin", "code-forge.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	return dir, project, mjs
}

// b678Prompt runs `keys args...` on a fake 80x24 terminal fed with keys;
// stdout, stderr and the drawing share one file as on the terminal.
func b678Prompt(t *testing.T, project string, keys []string, args ...string) (int, string) {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "tty-")
	if err != nil {
		t.Fatal(err)
	}
	saved := keysPromptOpen
	keysPromptOpen = func() (keysTTY, bool) {
		return keysTTY{initWizEnv{in: &b528Keys{keys: keys}, out: file, cols: 80, rows: 24}, true}, true
	}
	defer func() { keysPromptOpen = saved }()
	keysEnded = false
	plan := keysDecide(args, project)
	if plan == nil {
		t.Fatalf("keys %v handed over to Node; want Go", args)
	}
	code := plan(file, file)
	_ = file.Close()
	data, _ := os.ReadFile(file.Name())
	return code, string(data)
}

func b678Read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return "<none>"
	}
	return string(data)
}

func TestB678KeysSetPromptMatchesNode(t *testing.T) {
	for _, tc := range []struct {
		name   string
		keys   []string
		code   int
		golden string
		stored string
	}{
		{"typed", []string{"s", "e", "k", "r", "i", "t", "\r"}, 0, b678Typed, "sekrit"},
		{"backspace", []string{"a", "b", "\x7f", "c", "\r"}, 0, b678Bs, "ac"},
		{"ctrl-c", []string{"\x03"}, 1, b678CtrlC, ""},
		{"empty enter", []string{"\r"}, 1, b678Empty, ""},
		{"ctrl-d", []string{"\x04"}, 13, b678CtrlD, ""},
		{"input ends", nil, 13, b678CtrlD, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, project, mjs := b678PromptHome(t)
			code, got := b678Prompt(t, project, tc.keys, "set", "foo")
			want := strings.ReplaceAll(tc.golden, "@MJS@", mjs)
			if code != tc.code || got != want {
				t.Fatalf("exit %d (want %d)\n%s", code, tc.code, b528Diff(want, got))
			}
			if keysEnded != (tc.code == 13) {
				t.Fatalf("keysEnded = %v", keysEnded)
			}
			key, index := b678Read(t, filepath.Join(dir, "foo.key")), b678Read(t, filepath.Join(dir, "index.json"))
			if tc.stored == "" {
				if key != "<none>" || index != "<none>" {
					t.Fatalf("something was stored: %q %q", key, index)
				}
				return
			}
			if key != `{"v":"`+tc.stored+`","exp":null}` {
				t.Fatalf("foo.key = %q", key)
			}
			wantIndex := "{\n  \"foo\": {\n    \"source\": \"user\",\n    \"backend\": \"file\",\n    \"exp\": null\n  }\n}"
			if index != wantIndex {
				t.Fatalf("index.json = %q", index)
			}
			if info, err := os.Stat(filepath.Join(dir, "foo.key")); err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("foo.key mode: %v %v", info, err)
			}
		})
	}
}

// The typed value goes to the keychain writer (fake `security`), never
// to argv or the output.
func TestB678KeysSetPromptKeychain(t *testing.T) {
	_, project, _ := b678PromptHome(t)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	kc := b610Fake(t, true)
	code, got := b678Prompt(t, project, []string{"p", "w", "\r"}, "set", "foo")
	if code != 0 || !strings.HasSuffix(got, "\x1b[?25hstored foo in keychain (source=user, expires=never)\n") || strings.Contains(got, "WARN") {
		t.Fatalf("exit %d: %q", code, got)
	}
	if item := kc.item("foo"); item != `{"v":"pw","exp":null}` {
		t.Fatalf("keychain item = %q", item)
	}
	if strings.Contains(kc.calls(), "pw") {
		t.Fatalf("the value reached argv: %q", kc.calls())
	}
}

// A store write that fails is Node's thrown put: the verb fails, exit 1.
func TestB678KeysSetPromptWriteFails(t *testing.T) {
	dir, project, _ := b678PromptHome(t)
	if err := os.MkdirAll(filepath.Join(dir, "foo.key"), 0o700); err != nil {
		t.Fatal(err)
	}
	code, got := b678Prompt(t, project, []string{"x", "y", "\r"}, "set", "foo")
	if code != 1 || !strings.Contains(got, "\x1b[?25hcode-forge: verb \"keys\" failed: Error: EISDIR\n") || strings.Contains(got, "stored foo") {
		t.Fatalf("exit %d: %q", code, got)
	}
	if b678Read(t, filepath.Join(dir, "index.json")) != "<none>" {
		t.Fatal("index row written after a failed write")
	}
}

// Cases Go does not draw like Node still go to Node, before anything runs.
func TestB678KeysSetPromptHandsOver(t *testing.T) {
	_, project, _ := b678PromptHome(t)
	saved := keysPromptOpen
	t.Cleanup(func() { keysPromptOpen = saved })
	keysPromptOpen = func() (keysTTY, bool) { return keysTTY{}, false }
	if keysDecide([]string{"set", "foo"}, project) != nil {
		t.Fatal("a terminal of size 0: Go served the prompt")
	}
	keysPromptOpen = func() (keysTTY, bool) {
		return keysTTY{initWizEnv{in: &b528Keys{}, out: os.Stdout, cols: 80, rows: 24}, true}, true
	}
	// B694: Node warns with its pid that NO_COLOR is ignored
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("NO_COLOR", "1")
	if keysDecide([]string{"set", "foo"}, project) != nil {
		t.Fatal("FORCE_COLOR with NO_COLOR: Go served the prompt")
	}
	b528Unset(t, "NO_COLOR", "FORCE_COLOR")
	upgradePackageRoot = func() (string, bool) { return "", false }
	if keysDecide([]string{"set", "foo"}, project) != nil {
		t.Fatal("no launcher to name at EOF: Go served the prompt")
	}
}

// b678OpScript is a fake op: argv and whether CODE_FORGE_OWNER_PID is set
// go to $B678_OP_LOG; B678_OP_MODE picks the answer of `op read`;
// `op item get` prints .
const b678OpScript = `#!/bin/sh
echo "$* owner=${CODE_FORGE_OWNER_PID:+set}" >> "$B678_OP_LOG"
case "$1" in
read) case "$B678_OP_MODE" in
  ok) printf 'secretval\r\n';;
  locked) echo "[ERROR] not signed in" >&2; exit 1;;
  fail) echo boom >&2; exit 3;;
  notfound) echo "isn't an item" >&2; exit 1;;
  empty) exit 0;;
  kill) kill -TERM $$;;
  slow) exec /bin/sleep 5;;
  esac;;
item) printf '%s' "$B678_OP_ITEM";;
esac
`

const (
	b678ItemID  = "abcdefghijklmnopqrstuvwxyz"
	b678Item    = `{"id":"abcdefghijklmnopqrstuvwxyz","title":"My Key","vault":{"id":"vlt123","name":"Dev"},"fields":[{"id":"credential","label":"credential","value":"x","type":"CONCEALED"}]}`
	b678NoField = `{"id":"abcdefghijklmnopqrstuvwxyz","vault":{"id":"v1"},"fields":[{"id":"a","label":"A","type":"CONCEALED"},{"id":"b","type":"CONCEALED"}]}`
	b678Approve = "1Password may ask you to approve access: check the 1Password window (Touch ID or password).\n"
	b678Warn    = "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n"
	b678Resolve = "foo: 1Password item \"My Key\" (vault Dev, field credential) -> op://vlt123/abcdefghijklmnopqrstuvwxyz/credential\n"
	b678ItemRef = "op://vlt123/abcdefghijklmnopqrstuvwxyz/credential"
)

// b678Op puts the fake op first on PATH (none when missing) in the file
// store's temp HOME; it returns the store dir, project and op log path.
func b678Op(t *testing.T, mode, item string, missing bool) (string, string, string) {
	t.Helper()
	dir, project := b456KeysHome(t, "", nil)
	bin := t.TempDir()
	if !missing {
		if err := os.WriteFile(filepath.Join(bin, "op"), []byte(b678OpScript), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(t.TempDir(), "op.log")
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Setenv("B678_OP_LOG", log)
	t.Setenv("B678_OP_MODE", mode)
	t.Setenv("B678_OP_ITEM", item)
	t.Setenv("CODE_FORGE_TMP_ROOT", t.TempDir())
	lrRoot = ""
	t.Cleanup(func() { toolsReleaseRoot(); lrRoot = "" })
	return dir, project, log
}

func b678OpRow(exp, ref string) string {
	return "{\n  \"foo\": {\n    \"source\": \"op\",\n    \"backend\": \"file\",\n    \"exp\": " + exp + ",\n    \"ref\": \"" + ref + "\"\n  }\n}"
}

func TestB678KeysOpMatchesNode(t *testing.T) {
	const at8h, at2h = "1800028800000", "1800007200000"
	for _, tc := range []struct {
		name, mode, item string
		args             []string
		code             int
		stdout, stderr   string
		oplog            string
		index            string
	}{
		{"set ref", "ok", "", []string{"set", "foo", "--op", "op://v/i/f"}, 0,
			"stored foo in file (source=op, expires=2027-01-15T16:00:00.000Z)\n", b678Approve + b678Warn,
			"read op://v/i/f owner=set\n", b678OpRow(at8h, "op://v/i/f")},
		{"set ref cache hours", "ok", "", []string{"set", "foo", "--op", "op://v/i/f", "--cache-hours", "2"}, 0,
			"stored foo in file (source=op, expires=2027-01-15T10:00:00.000Z) (cache 2 h from --cache-hours)\n", b678Approve + b678Warn,
			"read op://v/i/f owner=set\n", b678OpRow(at2h, "op://v/i/f")},
		{"set item id", "ok", b678Item, []string{"set", "foo", "--op", b678ItemID}, 0,
			b678Resolve + "stored foo in file (source=op, expires=2027-01-15T16:00:00.000Z)\n", b678Approve + b678Warn,
			"item get " + b678ItemID + " --format json owner=set\nread " + b678ItemRef + " owner=set\n", b678OpRow(at8h, b678ItemRef)},
		{"set item link", "ok", b678Item, []string{"set", "foo", "--op", "https://start.1password.com/open/i?a=X&v=Y&i=" + b678ItemID + "&h=my.1password.com"}, 0,
			b678Resolve + "stored foo in file (source=op, expires=2027-01-15T16:00:00.000Z)\n", b678Approve + b678Warn,
			"item get " + b678ItemID + " --format json owner=set\nread " + b678ItemRef + " owner=set\n", b678OpRow(at8h, b678ItemRef)},
		{"set item bad answer", "ok", "nope", []string{"set", "foo", "--op", b678ItemID}, 1,
			"", b678Approve + "foo: unexpected answer from op\n",
			"item get " + b678ItemID + " --format json owner=set\n", "<none>"},
		{"set item no single field", "ok", b678NoField, []string{"set", "foo", "--op", b678ItemID}, 1,
			"", b678Approve + "foo: the 1Password item has no single key field; pass a full op://vault/item/field reference (concealed fields: \"A\", \"b\")\n",
			"item get " + b678ItemID + " --format json owner=set\n", "<none>"},
		{"set empty answer", "empty", "", []string{"set", "foo", "--op", "op://v/i/f"}, 1,
			"", b678Approve + b678Warn + "foo: unexpected answer from op\n", "read op://v/i/f owner=set\n", "<none>"},
		{"set op fails", "fail", "", []string{"set", "foo", "--op", "op://v/i/f"}, 1,
			"", b678Approve + b678Warn + "foo: 1Password CLI failed (exit 3); run `op read op://v/i/f` yourself to see why\n", "read op://v/i/f owner=set\n", "<none>"},
		{"set op killed", "kill", "", []string{"set", "foo", "--op", "op://v/i/f"}, 1,
			"", b678Approve + b678Warn + "foo: 1Password CLI failed (signal SIGTERM); run `op read op://v/i/f` yourself to see why\n", "read op://v/i/f owner=set\n", "<none>"},
		{"set op locked", "locked", "", []string{"set", "foo", "--op", "op://v/i/f"}, 1,
			"", b678Approve + b678Warn + "foo: 1Password is locked or not signed in; unlock the app (or run `op signin`) and try again\n", "read op://v/i/f owner=set\n", "<none>"},
		{"set op not found", "notfound", "", []string{"set", "foo", "--op", "op://v/i/f"}, 1,
			"", b678Approve + b678Warn + "foo: 1Password item or field not found, or no access; check the reference\n", "read op://v/i/f owner=set\n", "<none>"},
		{"test ref store miss", "ok", "", []string{"test", "foo", "--ref", "op://v/i/f"}, 0,
			"foo: ok source=op expires=2027-01-15T16:00:00.000Z\n", b678Approve + b678Warn,
			"read op://v/i/f owner=set\n", b678OpRow(at8h, "op://v/i/f")},
		{"test ref op fails", "fail", "", []string{"test", "foo", "--ref", "op://v/i/f"}, 1,
			"", b678Approve + b678Warn + "foo: 1Password CLI failed (exit 3); run `op read op://v/i/f` yourself to see why\nfoo: not found\n",
			"read op://v/i/f owner=set\n", "<none>"},
		{"test item id", "ok", b678Item, []string{"test", "foo", "--ref", b678ItemID}, 0,
			b678Resolve + "foo: ok source=op expires=2027-01-15T16:00:00.000Z\n", b678Approve + b678Warn,
			"item get " + b678ItemID + " --format json owner=set\nread " + b678ItemRef + " owner=set\n", b678OpRow(at8h, b678ItemRef)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, project, log := b678Op(t, tc.mode, tc.item, false)
			code, stdout, stderr := b456Keys(t, project, tc.args...)
			if code != tc.code || stdout != tc.stdout || stderr != tc.stderr {
				t.Fatalf("exit %d (want %d)\nstdout %q\nwant   %q\nstderr %q\nwant   %q", code, tc.code, stdout, tc.stdout, stderr, tc.stderr)
			}
			if got := b678Read(t, log); got != tc.oplog {
				t.Fatalf("op calls %q; want %q", got, tc.oplog)
			}
			if got := b678Read(t, filepath.Join(dir, "index.json")); got != tc.index {
				t.Fatalf("index.json %q; want %q", got, tc.index)
			}
			key := b678Read(t, filepath.Join(dir, "foo.key"))
			if tc.index == "<none>" && key != "<none>" || tc.index != "<none>" && key != `{"v":"secretval","exp":`+strings.Split(strings.Split(tc.index, "\"exp\": ")[1], ",")[0]+`}` {
				t.Fatalf("foo.key %q", key)
			}
			if strings.Contains(stdout+stderr, "secretval") {
				t.Fatal("the secret was printed")
			}
		})
	}
}

// A store hit answers `keys test --ref op://...` without calling op.
func TestB678KeysTestRefStoreHit(t *testing.T) {
	_, project, log := b678Op(t, "ok", "", false)
	if code, _, _ := b456Keys(t, project, "set", "foo", "--op", "op://v/i/f"); code != 0 {
		t.Fatalf("set exit %d", code)
	}
	code, stdout, stderr := b456Keys(t, project, "test", "foo", "--ref", "op://v/i/f")
	if code != 0 || stdout != "foo: ok source=file expires=2027-01-15T16:00:00.000Z\n" || stderr != b678Approve+b678Warn {
		t.Fatalf("exit %d %q %q", code, stdout, stderr)
	}
	if got := b678Read(t, log); got != "read op://v/i/f owner=set\n" {
		t.Fatalf("op calls %q", got)
	}
}

func TestB678KeysOpMissing(t *testing.T) {
	_, project, _ := b678Op(t, "ok", "", true)
	code, stdout, stderr := b456Keys(t, project, "set", "foo", "--op", "op://v/i/f")
	if code != 1 || stdout != "" || stderr != b678Approve+b678Warn+"foo: 1Password CLI not found; install it with: brew install 1password-cli\n" {
		t.Fatalf("exit %d %q %q", code, stdout, stderr)
	}
}

// A first call that times out is retried once; a second timeout fails.
func TestB678KeysOpTimeoutRetriedOnce(t *testing.T) {
	dir, project, log := b678Op(t, "slow", "", false)
	saved := opAttemptTimeout
	opAttemptTimeout = func(int) time.Duration { return 2 * time.Second }
	t.Cleanup(func() { opAttemptTimeout = saved })
	code, stdout, stderr := b456Keys(t, project, "set", "foo", "--op", "op://v/i/f")
	if code != 1 || stdout != "" || stderr != b678Approve+b678Warn+"foo: 1Password did not answer in time; unlock the app and try again\n" {
		t.Fatalf("exit %d %q %q", code, stdout, stderr)
	}
	if got := b678Read(t, log); got != "read op://v/i/f owner=set\nread op://v/i/f owner=set\n" {
		t.Fatalf("op calls %q", got)
	}
	if got := b678Read(t, filepath.Join(dir, "index.json")); got != "<none>" {
		t.Fatalf("stored after a timeout: %q", got)
	}
}

// The value read from op goes to the keychain writer when there is one.
func TestB678KeysSetOpKeychain(t *testing.T) {
	dir, project, _ := b678Op(t, "ok", "", false)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	kc := b610Fake(t, true)
	code, stdout, stderr := b456Keys(t, project, "set", "foo", "--op", "op://v/i/f")
	if code != 0 || stdout != "stored foo in keychain (source=op, expires=2027-01-15T16:00:00.000Z)\n" || stderr != b678Approve {
		t.Fatalf("exit %d %q %q", code, stdout, stderr)
	}
	if item := kc.item("foo"); item != `{"v":"secretval","exp":1800028800000}` {
		t.Fatalf("keychain item %q", item)
	}
	if got := b678Read(t, filepath.Join(dir, "index.json")); !strings.Contains(got, "\"backend\": \"keychain\"") || !strings.Contains(got, "\"ref\": \"op://v/i/f\"") {
		t.Fatalf("index.json %q", got)
	}
	if strings.Contains(kc.calls(), "secretval") {
		t.Fatal("the value reached argv")
	}
}

// A cache write that fails keeps the value: Node prints the reason, then
// the result (recorded: foo.key is a directory).
func TestB678KeysSetOpCacheWriteFails(t *testing.T) {
	dir, project, _ := b678Op(t, "ok", "", false)
	if err := os.MkdirAll(filepath.Join(dir, "foo.key"), 0o700); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := b456Keys(t, project, "set", "foo", "--op", "op://v/i/f")
	if code != 0 || stdout != "stored foo in file (source=op, expires=2027-01-15T16:00:00.000Z)\n" ||
		stderr != b678Approve+b678Warn+"foo: cache write failed (EISDIR)\n" {
		t.Fatalf("exit %d %q %q", code, stdout, stderr)
	}
	if b678Read(t, filepath.Join(dir, "index.json")) != "<none>" {
		t.Fatal("index row written after a failed write")
	}
}
