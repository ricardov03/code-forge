package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/worker"
)

// B610: the default key chain's keychain backend in Go through the macOS
// `security` CLI (src/keys/backends/security-cli.mjs). No test reads the
// real keychain: the package's tests start with a `security` that does not
// exist (no keychain, the old hand-overs), and the cases below swap in a fake
// `security` whose items are files in a temp dir.

func init() {
	keysSecurityBin = "/nonexistent-b610/security"
	kcPackageRoot = func() string { return "" }
}

// b610Script is `security` over a dir of items (<service>.<account>): help,
// find-generic-password -w, add-generic-password -U (the value read twice
// from stdin), delete-generic-password; 44 when no item. argv goes to
// calls (values never appear in argv); a file "fail" makes every item verb
// exit 51.
const b610Script = `#!/bin/sh
D=$(/usr/bin/dirname "$0"); KC=$D/items
echo "$*" >> "$D/calls"
[ "$1" = help ] && exit 0
[ -f "$D/fail" ] && exit 51
case "$1" in
find-generic-password)
  [ -f "$KC/$3.$5" ] || exit 44
  /bin/cat "$KC/$3.$5"; echo ;;
add-generic-password)
  IFS= read -r a; IFS= read -r b
  [ "$a" = "$b" ] || exit 2
  # B643: a read-only store exits 1 on every shell (dash exits 2 on a failed redirect)
  [ -w "$KC" ] || exit 1
  printf '%s' "$a" > "$KC/$4.$6" ;;
delete-generic-password)
  [ -f "$KC/$3.$5" ] || exit 44
  /bin/rm "$KC/$3.$5" ;;
*) exit 2 ;;
esac
`

type b610Keychain struct{ dir string }

// b610Fake makes macOS with the fake `security`; with napi the package root
// holds @napi-rs/keyring and its macOS binding, so the backend is named
// "keychain" as in Node, else "security-cli".
func b610Fake(t *testing.T, napi bool) b610Keychain {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "items"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "security"), []byte(b610Script), 0o700); err != nil {
		t.Fatal(err)
	}
	root := ""
	if napi {
		root = t.TempDir()
		pkg := filepath.Join(root, "node_modules", "@napi-rs", "keyring")
		b460Write(t, filepath.Join(pkg, "package.json"), `{"name":"@napi-rs/keyring","main":"index.js"}`)
		b460Write(t, filepath.Join(pkg, "index.js"), "")
		b460Write(t, filepath.Join(pkg, "keyring.darwin-universal.node"), "")
	}
	savedOS, savedBin, savedRoot := keysGOOS, keysSecurityBin, kcPackageRoot
	keysGOOS, keysSecurityBin = "darwin", filepath.Join(dir, "security")
	kcPackageRoot = func() string { return root }
	t.Cleanup(func() { keysGOOS, keysSecurityBin, kcPackageRoot = savedOS, savedBin, savedRoot })
	return b610Keychain{dir}
}

func (k b610Keychain) put(t *testing.T, account, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(k.dir, "items", "code-forge."+account), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

// item is the stored value, or "<none>".
func (k b610Keychain) item(account string) string {
	data, err := os.ReadFile(filepath.Join(k.dir, "items", "code-forge."+account))
	if err != nil {
		return "<none>"
	}
	return string(data)
}

func (k b610Keychain) calls() string {
	data, _ := os.ReadFile(filepath.Join(k.dir, "calls"))
	return string(data)
}

func (k b610Keychain) fail(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(k.dir, "fail"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

const b610Find = "find-generic-password -s code-forge -a jev -w\n"

// doctor --quick on the default chain: the keychain is read first through
// `security`, then the file store; no Node hand-over.
func TestB610DoctorReadsKeychain(t *testing.T) {
	cfg := "provider: anthropic\n" + b474Levels

	// nothing stored (the common case): the key is not resolved
	b474Project(t, cfg, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	kc := b610Fake(t, true)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, "OK   config: valid\n" + b474NoLinks + b474NoKey, b474Summary(1, 2, 0)}})
	if got := kc.calls(); got != "help\n"+b610Find {
		t.Fatalf("security calls = %q", got)
	}

	// a keychain item, named as Node names the backend
	for _, napi := range []bool{true, false} {
		name := map[bool]string{true: "keychain", false: "security-cli"}[napi]
		b474Project(t, cfg, b483Now)
		t.Setenv("CODE_FORGE_KEY_BACKEND", "")
		kc = b610Fake(t, napi)
		kc.put(t, "jev", "sk-b610-plain")
		checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, b483Rows("OK   keys: jev key from " + name + ", no expiry"), b474Summary(2, 1, 0)}})
	}

	// the keychain misses: the file store answers
	home, _ := b474Project(t, cfg, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	b610Fake(t, true)
	writeHomeFile(t, home, ".code-forge/store/jev.key", "sk-b610-file\n")
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, b483Rows("OK   keys: jev key from file, no expiry"), b474Summary(2, 1, 0)}})
}

// An expired item is deleted with its index row and the file store is not
// read (store.read returns null); an expiry past the cache hours is cut in
// the item and the existing index row.
func TestB610DoctorKeychainRefresh(t *testing.T) {
	home, _ := b474Project(t, "provider: anthropic\n"+b474Levels, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	kc := b610Fake(t, true)
	kc.put(t, "jev", `{"v":"k1","exp":1000}`)
	writeHomeFile(t, home, ".code-forge/store/jev.key", "sk-b610-file\n")
	writeHomeFile(t, home, ".code-forge/store/index.json", `{"jev":{"source":"op","backend":"keychain","exp":1000},"other":{"source":"user","exp":null}}`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, "OK   config: valid\n" + b474NoLinks + b474NoKey, b474Summary(1, 2, 0)}})
	if got := kc.item("jev"); got != "<none>" {
		t.Fatalf("expired item kept: %q", got)
	}
	if got := b483File(t, home, "store/index.json"); got != "{\n  \"other\": {\n    \"source\": \"user\",\n    \"exp\": null\n  }\n}" {
		t.Fatalf("index = %q", got)
	}
	if got := b483File(t, home, "store/jev.key"); got != "sk-b610-file\n" {
		t.Fatalf("file store touched: %q", got)
	}

	home, _ = b474Project(t, "provider: anthropic\nkeys: {cache_hours: 3}\n"+b474Levels, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	kc = b610Fake(t, false)
	kc.put(t, "jev", `{"v":"long-value","exp":99999999999999}`)
	writeHomeFile(t, home, ".code-forge/store/index.json", `{"jev":{"source":"op","exp":99999999999999,"backend":"keychain","ref":"op://a/b/c"}}`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0,
		b483Rows("OK   keys: jev key from security-cli, expires 1970-01-01T03:16:40.000Z (cache 3 h)"), b474Summary(2, 1, 0)}})
	if got := kc.item("jev"); got != `{"v":"long-value","exp":11800000}` {
		t.Fatalf("item = %q", got)
	}
	want := "{\n  \"jev\": {\n    \"source\": \"op\",\n    \"exp\": 11800000,\n    \"backend\": \"security-cli\",\n    \"ref\": \"op://a/b/c\"\n  }\n}"
	if got := b483File(t, home, "store/index.json"); got != want {
		t.Fatalf("index = %q", got)
	}
}

// An op:// reference with nothing stored is read with `op read` and cached
// in the keychain (the writer); no key file is written.
func TestB610DoctorCachesOpInKeychain(t *testing.T) {
	home, _ := b474Project(t, b483OpCfg, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	kc := b610Fake(t, true)
	b483FakeOp(t, `[ "$1 $2" = "read op://vault/item/field" ] || exit 9; printf 'sk-fake-value\n'`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, b483Rows("OK   keys: jev key from op, expires " + b483Expires), b474Summary(2, 1, 0)}})
	if got := kc.item("jev"); got != `{"v":"sk-fake-value","exp":29800000}` {
		t.Fatalf("item = %q", got)
	}
	if strings.Contains(kc.calls(), "sk-fake-value") {
		t.Fatal("the key value reached security's argv")
	}
	wantIndex := "{\n  \"jev\": {\n    \"source\": \"op\",\n    \"backend\": \"keychain\",\n    \"exp\": 29800000,\n    \"ref\": \"op://vault/item/field\"\n  }\n}"
	if got := b483File(t, home, "store/index.json"); got != wantIndex {
		t.Fatalf("index = %q", got)
	}
	if got := b483File(t, home, "store/jev.key"); !strings.Contains(got, "no such file") {
		t.Fatalf("key file written: %q", got)
	}
}

// What Go still cannot do goes to Node: a `security` that fails on read,
// and no macOS keychain at all (Node's Secret Service / Credential Manager).
func TestB610DoctorKeychainHandOvers(t *testing.T) {
	b474Project(t, "provider: anthropic\n"+b474Levels, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	kc := b610Fake(t, true)
	kc.fail(t)
	expectHandover(t, "doctor-key-store", "doctor", "--quick")

	b474Project(t, "provider: anthropic\n"+b474Levels, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	b610Fake(t, true)
	keysGOOS = "linux"
	expectHandover(t, "doctor-key-store", "doctor", "--quick")
}

// keys test and keys remove on the default chain read and delete the
// keychain item in Go.
func TestB610KeysTestAndRemove(t *testing.T) {
	dir, project := b456KeysHome(t, `{"jev":{"source":"user","backend":"keychain","exp":null}}`, nil)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	kc := b610Fake(t, true)
	kc.put(t, "jev", "sk-b610-plain")
	code, stdout, stderr := b456Keys(t, project, "test", "jev")
	if code != 0 || stdout != "jev: ok source=keychain expires=never\n" || stderr != "" {
		t.Fatalf("test hit: exit %d stdout %q stderr %q", code, stdout, stderr)
	}

	// an expired item is dropped with its index row: not found
	kc.put(t, "jev", `{"v":"old","exp":1000}`)
	code, stdout, stderr = b456Keys(t, project, "test", "jev")
	if code != 1 || stdout != "" || stderr != "jev: not found\n" || kc.item("jev") != "<none>" {
		t.Fatalf("test expired: exit %d stdout %q stderr %q item %q", code, stdout, stderr, kc.item("jev"))
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "index.json")); string(data) != "{}" {
		t.Fatalf("index = %q", data)
	}

	// a miss goes on to the file store
	if err := os.WriteFile(filepath.Join(dir, "jev.key"), []byte("sk-b610-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = b456Keys(t, project, "test", "jev")
	if code != 0 || stdout != "jev: ok source=file expires=never\n" {
		t.Fatalf("test miss: exit %d stdout %q", code, stdout)
	}

	// remove deletes the item and the file, counting both
	kc.put(t, "jev", "sk-b610-plain")
	code, stdout, stderr = b456Keys(t, project, "remove", "jev")
	if code != 0 || stdout != "removed jev (2 backends)\n" || stderr != "" {
		t.Fatalf("remove: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if kc.item("jev") != "<none>" {
		t.Fatal("keychain item kept")
	}
	if _, err := os.Stat(filepath.Join(dir, "jev.key")); !os.IsNotExist(err) {
		t.Fatalf("key file kept: %v", err)
	}
	if !strings.Contains(kc.calls(), "delete-generic-password -s code-forge -a jev\n") {
		t.Fatalf("calls = %q", kc.calls())
	}

	// a failing `security` keeps the Node hand-over for keys test
	kc.fail(t)
	if keysDecide([]string{"test", "jev"}, project) != nil {
		t.Fatal("a failing security read must hand over")
	}
}

// Non-interactive init finds the Jev key in the keychain and records its
// source; nothing goes to Node.
func TestB610InitJevFromKeychain(t *testing.T) {
	for _, napi := range []bool{true, false} {
		name := map[bool]string{true: "keychain", false: "security-cli"}[napi]
		var kc b610Keychain
		side := b548Go(t, b548Case{name: "jev from " + name, args: []string{"--no-interaction", "--skip-doctor"}, home: func(string) {
			kc = b610Fake(t, napi)
			kc.put(t, "jev", "sk-b610-plain")
		}})
		all := side.stdout + side.stderr + side.log + side.files
		if side.code != 0 || !strings.Contains(all, "keys: jev from "+name) || strings.Contains(all, "init-jev-keychain") || strings.Contains(all, "sk-b610-plain") {
			t.Fatalf("%s: code %d\nstdout %s\nstderr %s\nlog %s\nfiles %s", name, side.code, side.stdout, side.stderr, side.log, side.files)
		}
	}
}

// jev ask on the default chain takes the key from the keychain.
func TestB610JevAskKeychainKey(t *testing.T) {
	f, fake := newB550Jev(t, "version: 1\n", 200, b550JevOK, false)
	t.Setenv("CODE_FORGE_KEY_JEV", "")
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	kc := b610Fake(t, true)
	kc.put(t, "jev", "sk-b610-kc")
	code, _, errText := b460Call(t, "jev", "ask", "lane", "--state", "state.json")
	if code != 0 || errText != "" || len(fake.calls) != 1 || !strings.HasPrefix(fake.calls[0], "Bearer sk-b610-kc ") {
		t.Fatalf("= %d %q %d calls", code, errText, len(fake.calls))
	}
	b550NoHandover(t, f)
}

// worker boot resolves a keychain key in Go (no worker-jev-keychain row).
func TestB610WorkerKeychainKey(t *testing.T) {
	f := newB489Fix(t)
	f.withConfig(t, strings.Repeat("0", 40), `{"version":1}`)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	kc := b610Fake(t, true)
	kc.put(t, "jev", "sk-b610-kc")
	// the worker's own existence probe must not reach the real keychain
	oldProbe := workerJevProbe
	t.Cleanup(func() { workerJevProbe = oldProbe })
	workerJevProbe = worker.JevProbe{Keychain: func(string, string) (bool, bool) { return true, true }}
	before := len(b460Handovers(t, f.home))
	code, _, errText := b460Call(t, "worker", "--run", "r1", "--once")
	if rows := b460Handovers(t, f.home); code != 0 || len(rows) != before {
		t.Fatalf("worker = %d %q handovers %v", code, errText, rows)
	}
	if !strings.Contains(kc.calls(), b610Find) {
		t.Fatalf("keychain not read: %q", kc.calls())
	}
}
