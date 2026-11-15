package main

import (
	"testing"
)

// B728: an npm lock whose JSON holds lone surrogate escapes is judged in Go
// as Node's lockfileProblem does: JSON.parse keeps a lone surrogate (WTF-8 in
// Go), a surrogate in a package key, a URL path or an integrity is only read
// and compared, and the close prints Node's bytes. Before B728 every such lock
// handed over (close-lockfile-format). Expected bytes are Node's
// (B583_NODE_GOLDEN=1 runs CODE_FORGE_NATIVE=0 ./bin/code-forge block close
// on the same fixture).
func TestB728LockLoneSurrogateInGo(t *testing.T) {
	refused := func(detail string) (string, string) {
		return "lockfile package-lock.json " + detail + "\n  fix: no automatic fix: lockfile; repair or regenerate package-lock.json as valid lock JSON with HTTPS resolved URLs on review.lockfile_registries and nonempty integrity for resolved packages\n",
			b493OpenedWarn + "block B1 open: lockfile package-lock.json: " + detail + "\n"
	}
	cases := []struct {
		name, lock string
		code       int
		out        string
		detail     string
	}{
		{name: "surrogates in a key, a URL path and an integrity",
			lock: `{"name":"x\ud800","lockfileVersion":3,"packages":{"":{"name":"x"},"node_modules/a\udc00":{"version":"1.0.0","resolved":"https://registry.npmjs.org/a/-/a\ud83d-1.0.0.tgz","integrity":"sha512-\udfff"},"node_modules/b":{"link":true,"resolved":"../b\ud800"}}}`,
			out:  b587Reviewed + "not reviewed (review.exclude **/package-lock.json): package-lock.json\nblock B1 closed\n"},
		{name: "a problem on another entry",
			lock: `{"packages":{"node_modules/a\ud800":{"resolved":"https://registry.npmjs.org/a.tgz","integrity":"x"},"node_modules/b":{"resolved":"http://registry.npmjs.org/b.tgz","integrity":"x"}}}`,
			code: 1, detail: "packages.node_modules/b.resolved must be HTTPS on review.lockfile_registries"},
		{name: "a surrogate in the scheme",
			lock: `{"packages":{"node_modules/a":{"resolved":"ht\ud800tps://registry.npmjs.org/a.tgz","integrity":"x"}}}`,
			code: 1, detail: "packages.node_modules/a.resolved must be an HTTPS URL on an allowed host"},
		{name: "an integrity that is only a surrogate is not blank",
			lock: `{"dependencies":{"a":{"resolved":"https://registry.npmjs.org/a.tgz","integrity":"\udc00","dependencies":{"b":{"resolved":"https://registry.npmjs.org/b.tgz"}}}}}`,
			code: 1, detail: "dependencies.a.dependencies.b has no integrity"},
		{name: "a surrogate pair escape is one character",
			lock: `{"packages":{"node_modules/😀":{"resolved":"https://registry.npmjs.org/a.tgz"}}}`,
			code: 1, detail: "packages.node_modules/😀 has no integrity"},
	}
	for _, c := range cases {
		b583Node(t, func() *closeReviewFixture { return b587Fixture(t, nil, b587Lock(c.lock)) })
		f := b587Fixture(t, nil, b587Lock(c.lock))
		wantOut, wantErr := c.out, b493OpenedWarn
		if c.detail != "" {
			wantOut, wantErr = refused(c.detail)
		}
		code, out, errs, reason := b493Close(f)
		if reason != 0 || code != c.code || out != wantOut || errs != wantErr {
			t.Errorf("%s: exit=%d reason=%v\nout=%q\nerr=%q\nNode: exit=%d\nout=%q\nerr=%q", c.name, code, reason, out, errs, c.code, wantOut, wantErr)
		}
	}
}

// B728: a problem whose text names a package key with a lone surrogate (Node
// prints it with U+FFFD) and a surrogate in a resolved host (outside the URL
// forms closeLockURL proves) still hand over before any write.
func TestB728LockLoneSurrogateHandsOver(t *testing.T) {
	for _, c := range []struct{ name, lock string }{
		{"problem names the key", `{"packages":{"\ud800":1}}`},
		{"surrogate in the host", `{"packages":{"a":{"resolved":"https://registry\ud800.npmjs.org/a.tgz","integrity":"x"}}}`},
	} {
		b583Node(t, func() *closeReviewFixture { return b587Fixture(t, nil, b587Lock(c.lock)) })
		f := b587Fixture(t, nil, b587Lock(c.lock))
		before := b493LedgerLines(t, f)
		code, out, errs, reason := b493Close(f)
		if reason != hCloseLockfileFormat || code != landHandover || out != "" || errs != "" {
			t.Errorf("%s: exit=%d reason=%v out=%q err=%q, want the close-lockfile-format hand-over", c.name, code, reason, out, errs)
		}
		if b493LedgerLines(t, f) != before {
			t.Errorf("%s: the hand-over wrote ledger rows", c.name)
		}
	}
}
