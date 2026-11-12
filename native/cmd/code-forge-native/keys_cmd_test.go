package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const b456Now = 1_800_000_000_000.0

func b456KeysHome(t *testing.T, index string, keys map[string]string) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "file")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	dir := filepath.Join(home, ".code-forge", "store")
	if index != "" || len(keys) > 0 {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if index != "" {
		if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(index), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range keys {
		if err := os.WriteFile(filepath.Join(dir, name+".key"), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := keysNow
	keysNow = func() float64 { return b456Now }
	t.Cleanup(func() { keysNow = old })
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, ".git"), 0700); err != nil {
		t.Fatal(err)
	}
	return dir, project
}

func b456Keys(t *testing.T, cwd string, args ...string) (int, string, string) {
	t.Helper()
	plan := keysDecide(args, cwd)
	if plan == nil {
		t.Fatalf("keys %v handed over to Node; want Go", args)
	}
	var stdout, stderr bytes.Buffer
	code := plan(&stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestB456KeysRouteRegistered(t *testing.T) {
	if routes["keys"] == nil {
		t.Fatal("keys has no Go route")
	}
}

func TestB456KeysListFileBackend(t *testing.T) {
	index := `{
  "zeta": {"source": "user", "backend": "file", "exp": null},
  "alpha": {"source": "op", "backend": "file", "exp": 1800086400000, "ref": "op://v/i/f"},
  "beta": {"source": "op", "backend": "file", "exp": 1800001500000, "ref": "op://v/it em/f"},
  "gamma": {"source": "op", "backend": "file", "exp": 1700000000000},
  "delta": {"source": "op", "backend": "file", "exp": "x"},
  "eps": {"backend": null, "exp": 1800000030000}
}`
	_, project := b456KeysHome(t, index, nil)
	code, stdout, stderr := b456Keys(t, project, "list")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	wantOut := "NAME\tSOURCE\tBACKEND\tEXPIRES\n" +
		"alpha\top\tfile\t2027-01-16T08:00:00.000Z\n" +
		"beta\top\tfile\t2027-01-15T08:25:00.000Z\n" +
		"delta\top\tfile\tunknown\n" +
		"eps\tundefined\tnull\t2027-01-15T08:00:30.000Z\n" +
		"gamma\top\tfile\texpired\n" +
		"zeta\tuser\tfile\tnever\n"
	if stdout != wantOut {
		t.Fatalf("stdout:\n%q\nwant:\n%q", stdout, wantOut)
	}
	wantErr := "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n" +
		"WARN beta expires in 25 min; re-link: code-forge keys set beta --op 'op://v/it em/f'\n" +
		"WARN eps expires in 1 min; re-link: code-forge keys set eps --op <your 1Password reference>\n"
	if stderr != wantErr {
		t.Fatalf("stderr:\n%q\nwant:\n%q", stderr, wantErr)
	}
}

func TestB456KeysListCorruptIndexIsEmpty(t *testing.T) {
	_, project := b456KeysHome(t, "{bad", nil)
	code, stdout, _ := b456Keys(t, project, "list")
	if code != 0 || stdout != "NAME\tSOURCE\tBACKEND\tEXPIRES\n" {
		t.Fatalf("exit %d stdout %q", code, stdout)
	}
}

func TestB456KeysUsageErrors(t *testing.T) {
	_, project := b456KeysHome(t, "", nil)
	cases := []struct {
		args []string
		err  string
	}{
		{nil, keysUsage},
		{[]string{"frob"}, keysUsage},
		{[]string{"list", "x"}, keysUsage},
		{[]string{"remove", "-x"}, keysUsage},
		{[]string{"set", "zz", "--cache-hours", "3"}, keysUsage},
		{[]string{"set", "zz", "--op", "a", "--op", "b"}, keysUsage},
		{[]string{"remove", "Bad"}, "keys: key name must match /^[a-z][a-z0-9_-]{0,63}$/\n"},
		{[]string{"set", "zz", "--op", "op://a/b/c", "--cache-hours", "30"}, "keys: --cache-hours must be a whole number of hours from 1 to 24\n"},
		{[]string{"set", "zz", "--op", "junk"}, "zz: --op must be a 1Password item ID, item link or op://vault/item/field reference\n"},
		{[]string{"test", "zz", "--ref", "op://x"}, "keys: key \"zz\": malformed 1Password reference\n"},
		{[]string{"test", "zz", "--ref", "keychain:BAD"}, "keys: key name must match /^[a-z][a-z0-9_-]{0,63}$/\n"},
	}
	for _, c := range cases {
		code, stdout, stderr := b456Keys(t, project, c.args...)
		if code != 2 || stdout != "" || stderr != c.err {
			t.Errorf("%v: exit %d stdout %q stderr %q; want 2 and %q", c.args, code, stdout, stderr, c.err)
		}
	}
}

func TestB456KeysConfigCacheHours(t *testing.T) {
	_, project := b456KeysHome(t, "", nil)
	if err := os.WriteFile(filepath.Join(project, ".code-forge.yml"), []byte("keys:\n  cache_hours: 0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := b456Keys(t, project, "set", "zz", "--op", "op://a/b/c")
	if code != 2 || stderr != "keys: keys.cache_hours must be a whole number of hours from 1 to 24 (.code-forge.yml)\n" {
		t.Fatalf("set: exit %d stderr %q", code, stderr)
	}
	code, _, stderr = b456Keys(t, project, "test", "zz")
	if code != 1 || !strings.HasPrefix(stderr, "WARN keys: keys.cache_hours must be a whole number of hours from 1 to 24 (.code-forge.yml); caching for the default 8 h\n") || !strings.HasSuffix(stderr, "zz: not found\n") {
		t.Fatalf("test: exit %d stderr %q", code, stderr)
	}
	if err := os.WriteFile(filepath.Join(project, ".code-forge.yml"), []byte("a: [\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, stderr = b456Keys(t, project, "test", "zz")
	if !strings.HasPrefix(stderr, "WARN keys: .code-forge.yml cannot be loaded (parse-error); caching for the default 8 h\n") {
		t.Fatalf("bad config: stderr %q", stderr)
	}
}

func TestB456KeysRemoveFileBackend(t *testing.T) {
	dir, project := b456KeysHome(t, `{"a": {"source": "user", "backend": "file", "exp": null}, "b": {"source": "user", "backend": "file", "exp": null}}`, map[string]string{"a": "secret"})
	code, stdout, _ := b456Keys(t, project, "remove", "a")
	if code != 0 || stdout != "removed a (1 backend)\n" {
		t.Fatalf("exit %d stdout %q", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.key")); !os.IsNotExist(err) {
		t.Fatalf("a.key still there: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil || string(data) != "{\n  \"b\": {\n    \"source\": \"user\",\n    \"backend\": \"file\",\n    \"exp\": null\n  }\n}" {
		t.Fatalf("index %q %v", data, err)
	}
	info, _ := os.Stat(filepath.Join(dir, "index.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatalf("index mode %v", info.Mode().Perm())
	}
	code, stdout, _ = b456Keys(t, project, "remove", "nobody")
	if code != 0 || stdout != "removed nobody (0 backends)\n" {
		t.Fatalf("miss: exit %d stdout %q", code, stdout)
	}
}

func TestB456KeysTestFileBackend(t *testing.T) {
	dir, project := b456KeysHome(t, `{"long": {"source": "op", "backend": "keychain", "exp": 1900000000000, "ref": "op://a/b/c"}, "old": {"source": "op", "backend": "file", "exp": 1}}`,
		map[string]string{"plain": "value\n", "long": `{"v":"lsecret","exp":1900000000000}`, "old": `{"v":"osecret","exp":1}`})
	code, stdout, stderr := b456Keys(t, project, "test", "plain")
	if code != 0 || stdout != "plain: ok source=file expires=never\n" || stderr != "WARN "+keysFileWarning+"\n" {
		t.Fatalf("plain: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	// D2: an expiry past now + 8 h is cut to now + 8 h in the entry and its index row.
	code, stdout, _ = b456Keys(t, project, "test", "long")
	if code != 0 || stdout != "long: ok source=file expires=2027-01-15T16:00:00.000Z\n" {
		t.Fatalf("long: exit %d stdout %q", code, stdout)
	}
	entry, _ := os.ReadFile(filepath.Join(dir, "long.key"))
	if string(entry) != `{"v":"lsecret","exp":1800028800000}` {
		t.Fatalf("long.key %q", entry)
	}
	index, _ := os.ReadFile(filepath.Join(dir, "index.json"))
	if !strings.Contains(string(index), "\"long\": {\n    \"source\": \"op\",\n    \"backend\": \"file\",\n    \"exp\": 1800028800000,\n    \"ref\": \"op://a/b/c\"\n  }") {
		t.Fatalf("index %s", index)
	}
	// an expired entry is deleted with its index row, and reads as not found
	code, _, stderr = b456Keys(t, project, "test", "old")
	if code != 1 || !strings.HasSuffix(stderr, "old: not found\n") {
		t.Fatalf("old: exit %d stderr %q", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "old.key")); !os.IsNotExist(err) {
		t.Fatal("expired key file kept")
	}
	index, _ = os.ReadFile(filepath.Join(dir, "index.json"))
	if strings.Contains(string(index), `"old"`) {
		t.Fatalf("expired row kept: %s", index)
	}
}

func TestB456KeysTestEnvIsRedacted(t *testing.T) {
	_, project := b456KeysHome(t, "", nil)
	t.Setenv("CODE_FORGE_KEY_ZZ", "ok")
	code, stdout, _ := b456Keys(t, project, "test", "zz")
	if code != 0 || stdout != "zz: [REDACTED] source=env expires=never\n" {
		t.Fatalf("exit %d stdout %q", code, stdout)
	}
	t.Setenv("OTHER", "v1")
	code, stdout, stderr := b456Keys(t, project, "test", "zz", "--ref", "op://v/i/f")
	if code != 0 || stdout != "zz: [REDACTED] source=env expires=never\n" || !strings.HasPrefix(stderr, keysOpApproveNote+"\n") {
		t.Fatalf("op ref with env: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
}

func TestB456KeysDarwinListProbesSecurity(t *testing.T) {
	_, project := b456KeysHome(t, `{"a": {"source": "user", "backend": "keychain", "exp": null}}`, nil)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	oldOS, oldBin := keysGOOS, keysSecurityBin
	t.Cleanup(func() { keysGOOS, keysSecurityBin = oldOS, oldBin })
	keysGOOS = "darwin"
	fake := filepath.Join(t.TempDir(), "security")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	keysSecurityBin = fake
	code, stdout, stderr := b456Keys(t, project, "list")
	if code != 0 || stdout != "NAME\tSOURCE\tBACKEND\tEXPIRES\na\tuser\tkeychain\tnever\n" || stderr != "" {
		t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 3\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if keysDecide([]string{"list"}, project) != nil {
		t.Fatal("list with no usable keychain must hand over")
	}
	keysGOOS = "linux"
	if keysDecide([]string{"list"}, project) != nil {
		t.Fatal("linux default chain must hand over")
	}
}

func TestB456KeysNodeOnlyCasesHandOver(t *testing.T) {
	_, project := b456KeysHome(t, "", nil)
	for _, args := range [][]string{
		{"set", "zz"},
		{"set", "zz", "--op", "op://a/b/c"},
		{"set", "zz", "--op", "abcdefghijklmnopqrstuvwxyz"},
		{"test", "zz", "--ref", "op://a/b/c"},
		{"test", "zz", "--ref", "https://start.1password.com/open/i?i=abcdefghijklmnopqrstuvwxyz"},
	} {
		if keysDecide(args, project) != nil {
			t.Errorf("%v: Go served a case that needs Node", args)
		}
	}
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	for _, args := range [][]string{{"remove", "a"}, {"test", "a"}} {
		if keysDecide(args, project) != nil {
			t.Errorf("%v with the keychain chain: Go served it", args)
		}
	}
}
