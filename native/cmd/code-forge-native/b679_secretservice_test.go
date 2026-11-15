package main

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/worker"
)

// B679: the Linux keychain backend (the Secret Service store @napi-rs/keyring
// writes) in Go. No test reaches a real D-Bus: the package's tests start with
// a Secret Service that does not connect, and the cases below swap in a fake
// ssAPI that keeps items in memory like a keyring daemon.

func init() {
	ssConnect = func() (ssAPI, error) { return nil, errors.New("b679: no Secret Service in tests") }
}

type b679Item struct {
	attrs  map[string]string
	label  string
	secret []byte
	locked bool
}

// b679SS is a fake Secret Service: items by path, an optional locked default
// collection, prompts answered (or dismissed), and every call recorded.
type b679SS struct {
	items      map[string]*b679Item
	next       int
	collLocked bool
	dismiss    bool
	fail       bool
	failOn     string // a call prefix that fails ("" none)
	calls      []string
}

func newB679SS() *b679SS { return &b679SS{items: map[string]*b679Item{}} }

func (f *b679SS) call(s string) error {
	f.calls = append(f.calls, s)
	if f.fail || (f.failOn != "" && strings.HasPrefix(s, f.failOn)) {
		return errors.New("org.freedesktop.DBus.Error.Failed")
	}
	return nil
}

func b679Match(item, want map[string]string) bool {
	for k, v := range want {
		if item[k] != v {
			return false
		}
	}
	return true
}

func (f *b679SS) paths() []string {
	out := []string{}
	for p := range f.items {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func (f *b679SS) Search(attrs map[string]string) ([]string, []string, error) {
	if err := f.call("search " + attrs["service"] + " " + attrs["username"] + " " + strconv.Itoa(len(attrs))); err != nil {
		return nil, nil, err
	}
	var unlocked, locked []string
	for _, p := range f.paths() {
		if item := f.items[p]; b679Match(item.attrs, attrs) {
			if item.locked {
				locked = append(locked, p)
			} else {
				unlocked = append(unlocked, p)
			}
		}
	}
	return unlocked, locked, nil
}

func (f *b679SS) Unlock(items []string) error {
	if err := f.call("unlock " + strings.Join(items, ",")); err != nil {
		return err
	}
	if f.dismiss {
		return ssLocked
	}
	for _, p := range items {
		if item, ok := f.items[p]; ok {
			item.locked = false
		}
		if p == "collection" {
			f.collLocked = false
		}
	}
	return nil
}

func (f *b679SS) Secret(path string) ([]byte, error) {
	if err := f.call("secret " + path); err != nil {
		return nil, err
	}
	item := f.items[path]
	if item.locked {
		return nil, errors.Join(ssLocked, errors.New("IsLocked"))
	}
	return item.secret, nil
}

func (f *b679SS) SetSecret(path string, secret []byte) error {
	if err := f.call("set-secret " + path); err != nil {
		return err
	}
	f.items[path].secret = secret
	return nil
}

func (f *b679SS) Create(label string, attrs map[string]string, secret []byte) error {
	if err := f.call("create " + label); err != nil {
		return err
	}
	if f.collLocked {
		if err := f.Unlock([]string{"collection"}); err != nil {
			return err
		}
	}
	f.next++
	copied := map[string]string{}
	for k, v := range attrs {
		copied[k] = v
	}
	f.items["/org/freedesktop/secrets/collection/login/"+strconv.Itoa(f.next)] = &b679Item{attrs: copied, label: label, secret: secret}
	return nil
}

func (f *b679SS) Delete(path string) error {
	if err := f.call("delete " + path); err != nil {
		return err
	}
	delete(f.items, path)
	return nil
}

// put stores an item as @napi-rs/keyring writes it.
func (f *b679SS) put(name, value string) {
	f.next++
	f.items["/org/freedesktop/secrets/collection/login/"+strconv.Itoa(f.next)] = &b679Item{
		attrs: map[string]string{"service": "code-forge", "username": name}, label: "keyring:" + name + "@code-forge", secret: []byte(value)}
}

// item is the one stored value for a key name, or "<none>".
func (f *b679SS) item(name string) string {
	for _, p := range f.paths() {
		if f.items[p].attrs["username"] == name {
			return string(f.items[p].secret)
		}
	}
	return "<none>"
}

// b679Linux makes Linux with the package's Linux binding (binding) and a
// Secret Service that connects to ss (nil: it does not connect).
func b679Linux(t *testing.T, binding bool, ss *b679SS) {
	t.Helper()
	root := t.TempDir()
	if binding {
		pkg := filepath.Join(root, "node_modules", "@napi-rs", "keyring")
		b460Write(t, filepath.Join(pkg, "package.json"), `{"name":"@napi-rs/keyring","main":"index.js"}`)
		b460Write(t, filepath.Join(pkg, "index.js"), "")
		b460Write(t, filepath.Join(pkg, "keyring.linux-arm64-gnu.node"), "")
		b460Write(t, filepath.Join(pkg, "keyring.linux-x64-gnu.node"), "")
	}
	savedOS, savedRoot, savedConnect, savedMusl, savedArch := keysGOOS, kcPackageRoot, ssConnect, kcLinuxMusl, kcArch
	t.Cleanup(func() {
		keysGOOS, kcPackageRoot, ssConnect, kcLinuxMusl, kcArch = savedOS, savedRoot, savedConnect, savedMusl, savedArch
	})
	keysGOOS, kcArch = "linux", "x64"
	kcPackageRoot = func() string { return root }
	kcLinuxMusl = func() bool { return false }
	if ss != nil {
		ssConnect = func() (ssAPI, error) { return ss, nil }
	}
}

// The store finds and writes the items @napi-rs/keyring does: attributes
// {service, username}, label keyring:<name>@code-forge, one item per name.
func TestB679SecretServiceItems(t *testing.T) {
	ss := newB679SS()
	store := &ssStore{api: ss}
	if !store.probe() || !strings.Contains(strings.Join(ss.calls, "\n"), "search code-forge __code-forge-probe__ 2") {
		t.Fatalf("probe calls %q", ss.calls)
	}
	if _, found, err := store.get("jev"); found || err != nil {
		t.Fatalf("empty get = %v %v", found, err)
	}
	if err := store.set("jev", `{"v":"k1","exp":null}`); err != nil {
		t.Fatal(err)
	}
	if len(ss.items) != 1 {
		t.Fatalf("items %d", len(ss.items))
	}
	for _, item := range ss.items {
		if item.label != "keyring:jev@code-forge" || len(item.attrs) != 2 || item.attrs["service"] != "code-forge" || item.attrs["username"] != "jev" {
			t.Fatalf("item %+v", item)
		}
	}
	// a second set updates the same item
	if err := store.set("jev", "k2"); err != nil || len(ss.items) != 1 || ss.item("jev") != "k2" {
		t.Fatalf("update: %v items %d value %q", err, len(ss.items), ss.item("jev"))
	}
	if value, found, err := store.get("jev"); value != "k2" || !found || err != nil {
		t.Fatalf("get = %q %v %v", value, found, err)
	}
	// a locked item is unlocked (the service prompts) before it is read
	for _, item := range ss.items {
		item.locked = true
	}
	ss.calls = nil
	if value, _, err := store.get("jev"); value != "k2" || err != nil || !strings.HasPrefix(ss.calls[1], "unlock ") {
		t.Fatalf("locked get = %q %v calls %q", value, err, ss.calls)
	}
	// a dismissed prompt is Couldn't access platform storage
	for _, item := range ss.items {
		item.locked = true
	}
	ss.dismiss = true
	if _, _, err := store.get("jev"); err == nil || !strings.HasPrefix(err.Error(), "Couldn't access platform storage: ") {
		t.Fatalf("dismissed = %v", err)
	}
	if store.probe() != true {
		t.Fatal("the probe has no item to unlock")
	}
	ss.dismiss = false
	// an empty secret is no value; a non-UTF-8 one throws
	ss.items = map[string]*b679Item{}
	ss.put("jev", "")
	if _, found, err := store.get("jev"); found || err != nil {
		t.Fatalf("empty secret = %v %v", found, err)
	}
	ss.items = map[string]*b679Item{}
	ss.put("jev", "\xff")
	if _, _, err := store.get("jev"); err == nil || err.Error() != "Password data is not valid UTF-8" {
		t.Fatalf("bad utf-8 = %v", err)
	}
	// two matching items are ambiguous for every operation
	ss.put("jev", "a")
	if _, _, err := store.get("jev"); err == nil || err.Error() != "Entry is matched by 2 credentials" {
		t.Fatalf("ambiguous = %v", err)
	}
	if _, err := store.delete("jev"); err == nil {
		t.Fatal("ambiguous delete must fail")
	}
	// delete: true when an item went, false when none matched
	ss.items = map[string]*b679Item{}
	ss.put("jev", "a")
	if removed, err := store.delete("jev"); !removed || err != nil || len(ss.items) != 0 {
		t.Fatalf("delete = %v %v", removed, err)
	}
	if removed, err := store.delete("jev"); removed || err != nil {
		t.Fatalf("delete none = %v %v", removed, err)
	}
	// a locked default collection is unlocked before a new item
	ss.collLocked = true
	if err := store.set("jev", "v"); err != nil || ss.collLocked || ss.item("jev") != "v" {
		t.Fatalf("create in locked collection: %v", err)
	}
	// a failing service is a platform failure and the probe says unusable
	ss.fail = true
	if err := store.set("jev", "v"); err == nil || !strings.HasPrefix(err.Error(), "Platform failure: ") {
		t.Fatalf("fail = %v", err)
	}
	if store.probe() {
		t.Fatal("a failing service must not be usable")
	}
}

// doctor --quick on Linux reads the Secret Service, named "keychain" as in
// Node, then the file store; with no binding the file store alone.
func TestB679DoctorLinuxSecretService(t *testing.T) {
	cfg := "provider: anthropic\n" + b474Levels

	b474Project(t, cfg, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	ss := newB679SS()
	b679Linux(t, true, ss)
	ss.put("jev", "sk-b679-plain")
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, b483Rows("OK   keys: jev key from keychain, no expiry"), b474Summary(2, 1, 0)}})

	// the Secret Service misses: the file store answers
	home, _ := b474Project(t, cfg, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	b679Linux(t, true, newB679SS())
	writeHomeFile(t, home, ".code-forge/store/jev.key", "sk-b679-file\n")
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, b483Rows("OK   keys: jev key from file, no expiry"), b474Summary(2, 1, 0)}})

	// nothing anywhere: not resolved
	b474Project(t, cfg, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	b679Linux(t, true, newB679SS())
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, "OK   config: valid\n" + b474NoLinks + b474NoKey, b474Summary(1, 2, 0)}})

	// no @napi-rs/keyring binding: Node's chain is the file store alone
	home, _ = b474Project(t, cfg, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	ss = newB679SS()
	ss.put("jev", "sk-b679-unused")
	b679Linux(t, false, ss)
	writeHomeFile(t, home, ".code-forge/store/jev.key", "sk-b679-file\n")
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, b483Rows("OK   keys: jev key from file, no expiry"), b474Summary(2, 1, 0)}})
	if len(ss.calls) != 0 {
		t.Fatalf("the Secret Service was asked: %q", ss.calls)
	}
}

// An expired item is deleted with its index row; a 1Password read is cached
// in the Secret Service (the writer) with a "keychain" index row.
func TestB679DoctorLinuxRefreshAndCache(t *testing.T) {
	home, _ := b474Project(t, "provider: anthropic\n"+b474Levels, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	ss := newB679SS()
	b679Linux(t, true, ss)
	ss.put("jev", `{"v":"k1","exp":1000}`)
	writeHomeFile(t, home, ".code-forge/store/index.json", `{"jev":{"source":"op","backend":"keychain","exp":1000}}`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, "OK   config: valid\n" + b474NoLinks + b474NoKey, b474Summary(1, 2, 0)}})
	if got := ss.item("jev"); got != "<none>" {
		t.Fatalf("expired item kept: %q", got)
	}
	if got := b483File(t, home, "store/index.json"); got != "{}" {
		t.Fatalf("index = %q", got)
	}

	home, _ = b474Project(t, b483OpCfg, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	ss = newB679SS()
	b679Linux(t, true, ss)
	b483FakeOp(t, `[ "$1 $2" = "read op://vault/item/field" ] || exit 9; printf 'sk-fake-value\n'`)
	checkSmallCases(t, []smallCase{{[]string{"doctor", "--quick"}, 0, b483Rows("OK   keys: jev key from op, expires " + b483Expires), b474Summary(2, 1, 0)}})
	if got := ss.item("jev"); got != `{"v":"sk-fake-value","exp":29800000}` {
		t.Fatalf("item = %q", got)
	}
	wantIndex := "{\n  \"jev\": {\n    \"source\": \"op\",\n    \"backend\": \"keychain\",\n    \"exp\": 29800000,\n    \"ref\": \"op://vault/item/field\"\n  }\n}"
	if got := b483File(t, home, "store/index.json"); got != wantIndex {
		t.Fatalf("index = %q", got)
	}
	if got := b483File(t, home, "store/jev.key"); !strings.Contains(got, "no such file") {
		t.Fatalf("key file written: %q", got)
	}
}

// What Go still does not serve goes to Node: Node's keyutils store (the
// binding loads, no Secret Service connects) and a Secret Service that
// fails on read.
func TestB679DoctorLinuxHandOvers(t *testing.T) {
	b474Project(t, "provider: anthropic\n"+b474Levels, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	b679Linux(t, true, nil)
	expectHandover(t, "doctor-key-store", "doctor", "--quick")

	b474Project(t, "provider: anthropic\n"+b474Levels, b483Now)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	ss := newB679SS()
	b679Linux(t, true, ss)
	ss.put("jev", "a")
	ss.put("jev", "b") // ambiguous: Node's read throws
	expectHandover(t, "doctor-key-store", "doctor", "--quick")
}

// keys test and keys remove read and delete the Secret Service item.
func TestB679KeysLinuxTestAndRemove(t *testing.T) {
	dir, project := b456KeysHome(t, `{"jev":{"source":"user","backend":"keychain","exp":null}}`, nil)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	ss := newB679SS()
	b679Linux(t, true, ss)
	ss.put("jev", "sk-b679-plain")
	code, stdout, stderr := b456Keys(t, project, "test", "jev")
	if code != 0 || stdout != "jev: ok source=keychain expires=never\n" || stderr != "" {
		t.Fatalf("test hit: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if err := os.WriteFile(filepath.Join(dir, "jev.key"), []byte("sk-b679-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = b456Keys(t, project, "remove", "jev")
	if code != 0 || stdout != "removed jev (2 backends)\n" || stderr != "" || ss.item("jev") != "<none>" {
		t.Fatalf("remove: exit %d stdout %q stderr %q item %q", code, stdout, stderr, ss.item("jev"))
	}
}

// Non-interactive init finds the Jev key in the Secret Service.
func TestB679InitJevFromSecretService(t *testing.T) {
	side := b548Go(t, b548Case{name: "jev from secret service", args: []string{"--no-interaction", "--skip-doctor"}, home: func(string) {
		ss := newB679SS()
		b679Linux(t, true, ss)
		ss.put("jev", "sk-b679-plain")
	}})
	all := side.stdout + side.stderr + side.log + side.files
	if side.code != 0 || !strings.Contains(all, "keys: jev from keychain") || strings.Contains(all, "init-jev-keychain") || strings.Contains(all, "sk-b679-plain") {
		t.Fatalf("code %d\nstdout %s\nstderr %s\nlog %s\nfiles %s", side.code, side.stdout, side.stderr, side.log, side.files)
	}
}

// The init wizard's pasted key goes to the Secret Service with a keychain
// index row; with no binding it goes to the 0600 file.
func TestB679InitWizardStorePut(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	ss := newB679SS()
	b679Linux(t, true, ss)
	if failure := iwjStorePut(home, "sk-b679-typed"); failure != "" {
		t.Fatal(failure)
	}
	if got := ss.item("jev"); got != `{"v":"sk-b679-typed","exp":null}` {
		t.Fatalf("item = %q", got)
	}
	if got := b483File(t, home, "store/index.json"); got != "{\n  \"jev\": {\n    \"source\": \"user\",\n    \"backend\": \"keychain\",\n    \"exp\": null\n  }\n}" {
		t.Fatalf("index = %q", got)
	}
	if _, err := os.Stat(filepath.Join(home, ".code-forge", "store", "jev.key")); !os.IsNotExist(err) {
		t.Fatalf("key file written: %v", err)
	}
	ss.items = map[string]*b679Item{}
	ss.failOn = "create"
	if failure := iwjStorePut(home, "sk-b679-typed"); failure != "Platform failure: org.freedesktop.DBus.Error.Failed" {
		t.Fatalf("failure = %q", failure)
	}

	home = t.TempDir()
	b679Linux(t, false, newB679SS())
	if failure := iwjStorePut(home, "sk-b679-typed"); failure != "" {
		t.Fatal(failure)
	}
	if got := b483File(t, home, "store/jev.key"); got != `{"v":"sk-b679-typed","exp":null}` {
		t.Fatalf("key file = %q", got)
	}
}

// jev ask takes the key from the Secret Service.
func TestB679JevAskSecretService(t *testing.T) {
	f, fake := newB550Jev(t, "version: 1\n", 200, b550JevOK, false)
	t.Setenv("CODE_FORGE_KEY_JEV", "")
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	ss := newB679SS()
	b679Linux(t, true, ss)
	ss.put("jev", "sk-b679-kc")
	code, _, errText := b460Call(t, "jev", "ask", "lane", "--state", "state.json")
	if code != 0 || errText != "" || len(fake.calls) != 1 || !strings.HasPrefix(fake.calls[0], "Bearer sk-b679-kc ") {
		t.Fatalf("= %d %q %d calls", code, errText, len(fake.calls))
	}
	b550NoHandover(t, f)
}

// worker boot resolves a Secret Service key in Go (no worker-jev-keychain row).
func TestB679WorkerSecretServiceKey(t *testing.T) {
	f := newB489Fix(t)
	f.withConfig(t, strings.Repeat("0", 40), `{"version":1}`)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	ss := newB679SS()
	b679Linux(t, true, ss)
	ss.put("jev", "sk-b679-kc")
	oldProbe := workerJevProbe
	t.Cleanup(func() { workerJevProbe = oldProbe })
	workerJevProbe = worker.JevProbe{Keychain: func(string, string) (bool, bool) { return true, true }}
	before := len(b460Handovers(t, f.home))
	code, _, errText := b460Call(t, "worker", "--run", "r1", "--once")
	if rows := b460Handovers(t, f.home); code != 0 || len(rows) != before {
		t.Fatalf("worker = %d %q handovers %v", code, errText, rows)
	}
	if !strings.Contains(strings.Join(ss.calls, "\n"), "search code-forge jev 2") {
		t.Fatalf("Secret Service not read: %q", ss.calls)
	}
}
