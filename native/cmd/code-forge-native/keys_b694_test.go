package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B694: the keys hand-overs B678/B679 left run in Go. Goldens are Node's own
// output (bin/code-forge with CODE_FORGE_NATIVE=0, temp HOME, file store),
// recorded on 2026-11-15. No test reads the real keychain, Secret Service or
// 1Password.

const b694Warn = "WARN keys: no OS keychain available; secrets are stored in 0600 files under ~/.code-forge/store\n"

// Linux without the @napi-rs/keyring binding: Node's chain is the file
// store alone, so list, test, remove and set use it with
// FILE_FALLBACK_WARNING (the same output as CODE_FORGE_KEY_BACKEND=file).
func TestB694KeysLinuxNoBindingFileChain(t *testing.T) {
	dir, project := b456KeysHome(t, `{"jev":{"source":"user","backend":"file","exp":null}}`, map[string]string{"jev": "sk-b694-fake\n"})
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	b679Linux(t, false, nil)
	for _, tc := range []struct {
		args           []string
		code           int
		stdout, stderr string
	}{
		{[]string{"list"}, 0, "NAME\tSOURCE\tBACKEND\tEXPIRES\njev\tuser\tfile\tnever\n", b694Warn},
		{[]string{"test", "jev"}, 0, "jev: ok source=file expires=never\n", b694Warn},
		{[]string{"test", "nope"}, 1, "", b694Warn + "nope: not found\n"},
		{[]string{"remove", "jev"}, 0, "removed jev (1 backend)\n", b694Warn},
		{[]string{"list"}, 0, "NAME\tSOURCE\tBACKEND\tEXPIRES\n", b694Warn},
	} {
		code, stdout, stderr := b456Keys(t, project, tc.args...)
		if code != tc.code || stdout != tc.stdout || stderr != tc.stderr {
			t.Fatalf("keys %v: exit %d stdout %q stderr %q", tc.args, code, stdout, stderr)
		}
	}
	if got := b678Read(t, filepath.Join(dir, "index.json")); got != "{}" {
		t.Fatalf("index.json = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "jev.key")); !os.IsNotExist(err) {
		t.Fatalf("jev.key still there: %v", err)
	}
}

// keys set <name> on Linux without the binding prompts and writes the file
// store like the file backend run Node recorded (b678Typed).
func TestB694KeysLinuxNoBindingSetPrompt(t *testing.T) {
	dir, project, _ := b678PromptHome(t)
	t.Setenv("CODE_FORGE_KEY_BACKEND", "")
	b679Linux(t, false, nil)
	code, got := b678Prompt(t, project, []string{"s", "e", "k", "r", "i", "t", "\r"}, "set", "foo")
	if code != 0 || got != b678Typed {
		t.Fatalf("exit %d\n%s", code, b528Diff(b678Typed, got))
	}
	if key := b678Read(t, filepath.Join(dir, "foo.key")); key != `{"v":"sekrit","exp":null}` {
		t.Fatalf("foo.key = %q", key)
	}
	if !strings.Contains(b678Read(t, filepath.Join(dir, "index.json")), `"backend": "file"`) {
		t.Fatal("index row is not the file backend")
	}
}

// opItemIdFromInput as Node's WHATWG URL reads each link (recorded from
// src/keys/onepassword.mjs): Go finds the same ID, or is unsure only where
// the host needs IDNA (Node runs those).
func TestB694OpItemLinksMatchNode(t *testing.T) {
	unsure := map[string]bool{
		"https://xn--zz.1password.com/?i=abcdefghijklmnopqrstuvwxyz": true,
		"https://１password.com/?i=abcdefghijklmnopqrstuvwxyz":        true,
		"https://start.1password.com/ü?i=abcdefghijklmnopqrstuvwxyz": true,
	}
	for _, tc := range []struct{ in, node string }{
		{"https://start.1password.com/open/i?a=AAAA&v=BBBB&h=my.1password.com", ""},
		{"https://my.1password.com/vaults/abc/allitems", ""},
		{"https://start.1password.com/open/i?a=X&i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://start.1password.com/open/i?i=ABCDEFGHIJKLMNOPQRSTUVWXYZ", ""},
		{"https://start.1password.com/open/i?i=abcdefghijklmnopqrstuvwxy", ""},
		{"https://start.1password.com/open/i?i=", ""},
		{"https://start.1password.com/open/i?i", ""},
		{"https://start.1password.com/open/i#i=abcdefghijklmnopqrstuvwxyz", ""},
		{"https://start.1password.com/open/i?%69=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://start.1password.com/open/i?i=%61bcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://start.1password.com/open/i?i=abcdefghijklmnopqrstuvwxyz&i=zzzzzzzzzzzzzzzzzzzzzzzzzz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://start.1password.com/open/i?i=bad%zzid&i=abcdefghijklmnopqrstuvwxyz", ""},
		{"https://START.1Password.COM/open/i?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"HTTPS://start.1password.com/open/i?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https:\\\\start.1password.com\\open?i=abcdefghijklmnopqrstuvwxyz", ""},
		{"https:///start.1password.com/open?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://user:pw@start.1password.com:443/open?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://start.1password.com:99999/open?i=abcdefghijklmnopqrstuvwxyz", ""},
		{"https://start.1password.com:x/open?i=abcdefghijklmnopqrstuvwxyz", ""},
		{"https://evil.com/?h=1password.com&i=abcdefghijklmnopqrstuvwxyz", ""},
		{"https://1password.com.evil.com/?i=abcdefghijklmnopqrstuvwxyz", ""},
		{"https://evil1password.com/?i=abcdefghijklmnopqrstuvwxyz", ""},
		{"https://1password.eu?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://x.1password.ca./?i=abcdefghijklmnopqrstuvwxyz", ""},
		{"https://a..1password.com/?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://a_b.1password.com/?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://xn--zz.1password.com/?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://st%61rt.1password.com/?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://st art.1password.com/?i=abcdefghijklmnopqrstuvwxyz", ""},
		{"https://start.1pass\tword.com/?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://start.1password.com/?i=abcdefghijklm\nnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://start.1password.com/?a&i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://start.1password.com/?i+=abcdefghijklmnopqrstuvwxyz", ""},
		{"https://start.1password.com/?+i=abcdefghijklmnopqrstuvwxyz", ""},
		{"https://start.1password.com/?i=abcdefghijklmnopqrstuvwxyz+", ""},
		{"https://start.1password.com/?i=abcdefghijklmnopqrstuvwxyz=", ""},
		{"https://start.1password.com/?;i=abcdefghijklmnopqrstuvwxyz", ""},
		{"https://[::1]/?i=abcdefghijklmnopqrstuvwxyz", ""},
		{"https://１password.com/?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://start.1password.com/ü?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://", ""},
		{"https://start.1password.com", ""},
		{"  https://start.1password.com/?i=abcdefghijklmnopqrstuvwxyz  ", "abcdefghijklmnopqrstuvwxyz"},
		{"https://start.1password.com/?i=abcdefghijklmnopqrstuvwxyz\u0001", "abcdefghijklmnopqrstuvwxyz"},
		{"https://@1password.com/?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://a@b@1password.com/?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://1password.com:/?i=abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		{"https://1password.com?x=1#?i=abcdefghijklmnopqrstuvwxyz", ""},
		{"https://1password.com/%3Fi=abcdefghijklmnopqrstuvwxyz", ""},
	} {
		id, sure := keysOpItem(tc.in)
		if sure == unsure[tc.in] || sure && id != tc.node {
			t.Errorf("%q: id %q sure %v; Node %q", tc.in, id, sure, tc.node)
		}
	}
}

// A 1Password link with no i= item ID: set refuses it (exit 2) and test
// looks the name up as Node does, in Go.
func TestB694KeysLinkWithoutItemID(t *testing.T) {
	const link = "https://start.1password.com/open/i?a=AAAA&v=BBBB&h=my.1password.com"
	_, project := b456KeysHome(t, "", nil)
	code, stdout, stderr := b456Keys(t, project, "set", "foo", "--op", link)
	if code != 2 || stdout != "" || stderr != "foo: --op must be a 1Password item ID, item link or op://vault/item/field reference\n" {
		t.Fatalf("set: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	code, stdout, stderr = b456Keys(t, project, "test", "foo", "--ref", link)
	if code != 1 || stdout != "" || stderr != b694Warn+"foo: not found\n" {
		t.Fatalf("test: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
}
