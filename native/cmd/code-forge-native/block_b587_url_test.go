package main

import "testing"

// B587: closeLockURL against `new URL(s)` in Node 24 (protocol and host taken
// with node -e on this list). Go either agrees or hands the close over
// (ok=false); it never gives a different answer. Host is compared for the
// special schemes, the only ones lockfileProblem's host test can pass.
func TestB587LockURLMatchesNode(t *testing.T) {
	cases := []struct {
		in             string
		valid          bool
		protocol, host string
	}{
		{"https://registry.npmjs.org/a/-/a-1.0.0.tgz", true, "https:", "registry.npmjs.org"},
		{"HTTPS://Registry.NPMJS.org/a", true, "https:", "registry.npmjs.org"},
		{"https://registry.npmjs.org:443/a", true, "https:", "registry.npmjs.org"},
		{"https://registry.npmjs.org:0443/x", true, "https:", "registry.npmjs.org"},
		{"https://registry.npmjs.org:8443/a", true, "https:", "registry.npmjs.org:8443"},
		{"https://registry.npmjs.org:/a", true, "https:", "registry.npmjs.org"},
		{"https://registry.npmjs.org:99999/a", false, "", ""},
		{"https://registry.npmjs.org:4x/a", false, "", ""},
		{"https://registry.npmjs.org./a", true, "https:", "registry.npmjs.org."},
		{"https://a..b/x", true, "https:", "a..b"},
		{"http://registry.npmjs.org/a", true, "http:", "registry.npmjs.org"},
		{"  https://registry.npmjs.org/a  ", true, "https:", "registry.npmjs.org"},
		{"\thttps://registry.\nnpmjs.org/a\r", true, "https:", "registry.npmjs.org"},
		{"https://", false, "", ""},
		{"https:///a", true, "https:", "a"},
		{"https:registry.npmjs.org/a", true, "https:", "registry.npmjs.org"},
		{"https://user@registry.npmjs.org/a", true, "https:", "registry.npmjs.org"},
		{"https://[::1]/a", true, "https:", "[::1]"},
		{"https://1.2.3.4/a", true, "https:", "1.2.3.4"},
		{"https://example.123/a", false, "", ""},
		{"https://xn--nxasmq6b.com/a", true, "https:", "xn--nxasmq6b.com"},
		{"https://ex%41mple.com/", true, "https:", "example.com"},
		{"https://ex\u00e4mple.com/", true, "https:", "xn--exmple-cua.com"},
		{"https://registry.npmjs.org\\a", true, "https:", "registry.npmjs.org"},
		{"ws://registry.npmjs.org/", true, "ws:", "registry.npmjs.org"},
		{"ftp://registry.npmjs.org:21/", true, "ftp:", "registry.npmjs.org"},
		{"wss://h:443/", true, "wss:", "h"},
		{"file:///tmp/a.tgz", true, "file:", ""},
		{"file:a.tgz", true, "file:", ""},
		{"file:../a", true, "file:", ""},
		{"file://host/x", true, "file:", "host"},
		{"git+ssh://git@github.com/x/y.git", true, "git+ssh:", "github.com"},
		{"git+https://github.com/x/y.git", true, "git+https:", "github.com"},
		{"git://github.com/x/y.git#abc", true, "git:", "github.com"},
		{"github:x/y", true, "github:", ""},
		{"npm:foo@1.0.0", true, "npm:", ""},
		{"foo://:80/", false, "", ""},
		{"foo://user@/x", false, "", ""},
		{"foo:///x", true, "foo:", ""},
		{"foo://h:8x/", false, "", ""},
		{"foo://h:65536/", false, "", ""},
		{"foo://h:65535/", true, "foo:", "h:65535"},
		{"foo://a b/", false, "", ""},
		{"foo://a<b/", false, "", ""},
		{"foo://a^b/", false, "", ""},
		{"foo://a|b/", false, "", ""},
		{"foo://[::1]/", true, "foo:", "[::1]"},
		{"foo://u:p@h:1/x", true, "foo:", "h:1"},
		{"foo:bar", true, "foo:", ""},
		{"a:", true, "a:", ""},
		{"1abc:x", false, "", ""},
		{"", false, "", ""},
		{"registry.npmjs.org/a", false, "", ""},
		{"/a/b", false, "", ""},
		{"../x", false, "", ""},
		{"null", false, "", ""},
		{"true", false, "", ""},
		{"12", false, "", ""},
		{"[object Object]", false, "", ""},
		{"https://a.b/\u00e9", true, "https:", "a.b"},
		{"foo://a%zz/", true, "foo:", "a%zz"},
		{"foo://h/ x", true, "foo:", "h"},
		{"https://0x/", true, "https:", "0.0.0.0"},
		{"https://a.0x1f/", false, "", ""},
		{"https://a.09/", false, "", ""},
		{"https://-a.com/", true, "https:", "-a.com"},
		{"https://a_b.com/", true, "https:", "a_b.com"},
		{"https://.", true, "https:", "."},
		{"https://a:b:c/", false, "", ""},
		{"git+ssh://git@github.com:x/y.git", false, "", ""},
		{"\u0000https://registry.npmjs.org/a\u0001", true, "https:", "registry.npmjs.org"},
	}
	served := 0
	for _, c := range cases {
		valid, protocol, host, ok := closeLockURL(c.in)
		if !ok {
			continue
		}
		served++
		special := protocol == "https:" || protocol == "http:" || protocol == "ws:" || protocol == "wss:" || protocol == "ftp:"
		if valid != c.valid || valid && protocol != c.protocol || special && host != c.host {
			t.Errorf("%q: Go valid=%v %q %q, Node valid=%v %q %q", c.in, valid, protocol, host, c.valid, c.protocol, c.host)
		}
	}
	t.Logf("Go served %d of %d URL forms", served, len(cases))
	if served < 50 {
		t.Errorf("Go served %d of %d URL forms, want at least 50", served, len(cases))
	}
	for _, v := range []any{nil, true, 12.0} {
		if valid, _, _, ok := closeLockURL(v); !ok || valid {
			t.Errorf("%v: want an invalid URL (Node: String(v) has no scheme)", v)
		}
	}
	if _, _, _, ok := closeLockURL([]any{"https://registry.npmjs.org/a"}); ok {
		t.Error("an array resolved (String joins it) must hand over")
	}
}
