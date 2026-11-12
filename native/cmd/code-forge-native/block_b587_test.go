package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

type b587File struct{ name, text string }

// b587Fixture is the approved one-file close plus extra owned, untracked
// files (a lock file, a doc) and, when cfg is not nil, that config.
func b587Fixture(t *testing.T, cfg ledger.Object, files ...b587File) *closeReviewFixture {
	t.Helper()
	f := newCloseReviewFixture(t, 1)
	b493Approve(t, f)
	record, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil {
		t.Fatal(err)
	}
	blocks := record.Get("blocks").(ledger.Object)
	entry := blocks.Get("B1").(ledger.Object)
	owned := append([]any{}, entry.Get("owned_files").([]any)...)
	for _, file := range files {
		owned = append(owned, file.name)
		closeFixtureWrite(t, filepath.Join(f.top, file.name), []byte(file.text))
	}
	entry.Set("owned_files", owned)
	blocks.Set("B1", entry)
	record.Set("blocks", blocks)
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		b493SetConfig(t, f, cfg)
	}
	return f
}

func b587Config(review ...ledger.Field) ledger.Object {
	return ledger.Object{{Key: "review", Value: append(ledger.Object{{Key: "close_wait_s", Value: 1}}, review...)}, {Key: "context", Value: ledger.Object{{Key: "require_coder_tests", Value: true}}}}
}

const (
	b587Reviewed = "reviewed file0.txt ticket b82733d3089a96c3991c870e\n"
	b587GoodLock = `{"name":"x","lockfileVersion":3,"packages":{"":{"name":"x"},"node_modules/a":{"version":"1.0.0","resolved":"https://registry.npmjs.org/a/-/a-1.0.0.tgz","integrity":"sha512-AAAA"},"node_modules/b":{"link":true,"resolved":"../b"}}}`
)

func b587Lock(text string) b587File { return b587File{"package-lock.json", text} }

// B587: a block whose changed files match review.exclude (the default lock
// list or a configured list) closes in Go with Node's bytes: the excluded
// files leave the review set, keep the proof check, get a signed
// gate.review_excluded close row, and an npm lock gets lockfileProblem's
// safety check. Before B587 every such close handed over
// (close-excluded-file). The bytes are Node's (CODE_FORGE_NATIVE=0 on the
// same fixtures).
func TestB587CloseExcludedFiles(t *testing.T) {
	refused := func(file, detail string) (string, string) {
		return "lockfile " + file + " " + detail + "\n  fix: no automatic fix: lockfile; repair or regenerate " + file + " as valid lock JSON with HTTPS resolved URLs on review.lockfile_registries and nonempty integrity for resolved packages\n",
			b493OpenedWarn + "block B1 open: lockfile " + file + ": " + detail + "\n"
	}
	type golden struct {
		name   string
		cfg    ledger.Object
		files  []b587File
		json   bool
		code   int
		out    string
		detail string // a refusal: the lockfile detail of the last file
	}
	cases := []golden{
		{name: "default rule, safe npm lock", files: []b587File{b587Lock(b587GoodLock)},
			out: b587Reviewed + "not reviewed (review.exclude **/package-lock.json): package-lock.json\nblock B1 closed\n"},
		{name: "default rule, safe npm lock, --json", files: []b587File{b587Lock(b587GoodLock)}, json: true,
			out: `{"block":"B1","event":"closed","status":"closed","reason":null,"close_event":null,"reviewed":[{"file":"file0.txt","ticket":"b82733d3089a96c3991c870e","via":"approved"}]}` + "\n"},
		{name: "configured glob and brace rules replace the defaults", cfg: b587Config(ledger.Field{Key: "exclude", Value: []any{"docs/**", "*.{md,txt}"}}), files: []b587File{{"docs/a.md", "doc\n"}, {"notes.md", "n\n"}},
			out: "not reviewed (review.exclude docs/**): docs/a.md\nnot reviewed (review.exclude *.{md,txt}): file0.txt\nnot reviewed (review.exclude *.{md,txt}): notes.md\nblock B1 closed\n"},
		{name: "yarn lock not parsed, nested shrinkwrap checked", files: []b587File{{"yarn.lock", "not parsed\n"}, {"sub/npm-shrinkwrap.json", `{"dependencies":{"a":{"version":"1","resolved":"https://registry.npmjs.org/a.tgz","integrity":"sha1-x","dependencies":{"b":{"resolved":"https://registry.npmjs.org/b.tgz","integrity":" "}}}}}`}},
			code: 1, detail: "dependencies.a.dependencies.b has no integrity"},
		{name: "not JSON", files: []b587File{b587Lock("{")}, code: 1, detail: "cannot read valid lock JSON"},
		{name: "not JSON, --json", files: []b587File{b587Lock("{")}, json: true, code: 1, detail: "cannot read valid lock JSON",
			out: `{"block":"B1","ok":false,"reason":"lockfile package-lock.json: cannot read valid lock JSON","blockers":[{"code":"lockfile","file":"package-lock.json","detail":"cannot read valid lock JSON","fix":"no automatic fix: lockfile; repair or regenerate package-lock.json as valid lock JSON with HTTPS resolved URLs on review.lockfile_registries and nonempty integrity for resolved packages"}]}` + "\n"},
		{name: "an array", files: []b587File{b587Lock("[]")}, code: 1, detail: "expected a lock object"},
		{name: "http resolved", files: []b587File{b587Lock(`{"packages":{"node_modules/a":{"resolved":"http://registry.npmjs.org/a.tgz","integrity":"sha1-x"}}}`)}, code: 1, detail: "packages.node_modules/a.resolved must be HTTPS on review.lockfile_registries"},
		{name: "null resolved", files: []b587File{b587Lock(`{"packages":{"node_modules/a":{"resolved":null}}}`)}, code: 1, detail: "packages.node_modules/a.resolved must be an HTTPS URL on an allowed host"},
		{name: "blank integrity (NBSP)", files: []b587File{b587Lock(`{"packages":{"node_modules/a":{"resolved":"https://registry.npmjs.org/a.tgz","integrity":"\u00a0"}}}`)}, code: 1, detail: "packages.node_modules/a has no integrity"},
		{name: "index keys first", files: []b587File{b587Lock(`{"packages":{"z":{"resolved":1},"7":[],"1":{}}}`)}, code: 1, detail: "packages.7 must be an object"},
		{name: "packages not an object", files: []b587File{b587Lock(`{"packages":"x"}`)}, code: 1, detail: "packages must be an object"},
		{name: "dependencies entry not an object", files: []b587File{b587Lock(`{"packages":null,"dependencies":{"a":{"dependencies":{"b":0}}}}`)}, code: 1, detail: "dependencies.a.dependencies.b must be an object"},
		{name: "own registry with a port", cfg: b587Config(ledger.Field{Key: "lockfile_registries", Value: []any{"npm.example.com:8443"}}), files: []b587File{b587Lock(`{"packages":{"node_modules/a":{"resolved":"https://NPM.example.com:8443/a.tgz","integrity":"sha1-x"},"node_modules/b":{"resolved":"https://registry.npmjs.org/b.tgz","integrity":"sha1-x"}}}`)}, code: 1, detail: "packages.node_modules/b.resolved must be HTTPS on review.lockfile_registries"},
		{name: "git dependency", files: []b587File{b587Lock(`{"packages":{"node_modules/a":{"resolved":"git+ssh://git@github.com/x/a.git#abc","integrity":"sha1-x"}}}`)}, code: 1, detail: "packages.node_modules/a.resolved must be HTTPS on review.lockfile_registries"},
	}
	for _, c := range cases {
		extra := []string{}
		if c.json {
			extra = []string{"--json"}
		}
		b583Node(t, func() *closeReviewFixture { return b587Fixture(t, c.cfg, c.files...) }, extra...)
		f := b587Fixture(t, c.cfg, c.files...)
		wantOut, wantErr := c.out, b493OpenedWarn
		if c.detail != "" {
			lock := c.files[len(c.files)-1].name
			text, errs := refused(lock, c.detail)
			if wantOut == "" {
				wantOut = text
			}
			wantErr = errs
		}
		code, out, errs, reason := b493Close(f, extra...)
		if reason != 0 || code != c.code || out != wantOut || errs != wantErr {
			t.Errorf("%s: exit=%d reason=%v\nout=%q\nerr=%q\nNode: exit=%d\nout=%q\nerr=%q", c.name, code, reason, out, errs, c.code, wantOut, wantErr)
		}
	}
}

// B587: the close rows Node signs for excluded files, in Node's order (after
// coder.tests, before block.close), as CODE_FORGE_NATIVE=0 wrote them on the
// same fixture (mac, ts and prev left out).
func TestB587CloseExcludedRows(t *testing.T) {
	f := b587Fixture(t, nil, b587Lock(b587GoodLock), b587File{"yarn.lock", "x\n"})
	before := len(b493LedgerLines(t, f))
	if code, _, _, reason := b493Close(f); code != 0 || reason != 0 {
		t.Fatalf("exit=%d reason=%v", code, reason)
	}
	got := b587NewRows(t, f, before)
	want := []string{
		`{"run":"r-close-fixture","event":"coder.tests","block":"B1","command":"go test ./...","exit":0,"tokens_source":null,"cost_source":null}`,
		`{"run":"r-close-fixture","event":"gate.review_excluded","block":"B1","file":"package-lock.json","content_hash":"9833f3eb647f0e333b6767f5d0418cba912fa5396023055ca57a95173e3bf2f4","rule":"**/package-lock.json","tokens_source":null,"cost_source":null}`,
		`{"run":"r-close-fixture","event":"gate.review_excluded","block":"B1","file":"yarn.lock","content_hash":"73cb3858a687a8494ca3323053016282f3dad39d42cf62ca4e79dda2aac7d9ac","rule":"**/yarn.lock","tokens_source":null,"cost_source":null}`,
		`{"run":"r-close-fixture","event":"block.close","block":"B1","status":"complete","tokens_source":null,"cost_source":null}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s\nNode:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// B587: what Go cannot judge byte-equal still hands over, before any write: a
// review.exclude that is not a list of strings (Node throws a TypeError) and
// lock shapes outside the proved forms (an escaped surrogate, an array
// "resolved", lockfile_registries that is not a list of strings, a URL with
// userinfo).
func TestB587CloseExcludedHandsOver(t *testing.T) {
	cases := []struct {
		name   string
		cfg    ledger.Object
		lock   string
		reason handoverReason
	}{
		{"exclude is a string", b587Config(ledger.Field{Key: "exclude", Value: "**/x"}), b587GoodLock, hCloseExcludedFile},
		{"exclude holds a number", b587Config(ledger.Field{Key: "exclude", Value: []any{1.0}}), b587GoodLock, hCloseExcludedFile},
		{"escaped surrogate", nil, "{\"packages\":{\"\\ud800\":{}}}", hCloseLockfileFormat},
		{"array resolved", nil, `{"packages":{"a":{"resolved":["https://registry.npmjs.org/a"]}}}`, hCloseLockfileFormat},
		{"registries not a list", b587Config(ledger.Field{Key: "lockfile_registries", Value: "registry.npmjs.org"}), b587GoodLock, hCloseLockfileFormat},
		{"userinfo URL", nil, `{"packages":{"a":{"resolved":"https://u@registry.npmjs.org/a","integrity":"x"}}}`, hCloseLockfileFormat},
	}
	for _, c := range cases {
		f := b587Fixture(t, c.cfg, b587Lock(c.lock))
		before := b493LedgerLines(t, f)
		code, out, errs, reason := b493Close(f)
		if reason != c.reason || code != landHandover || out != "" || errs != "" {
			t.Errorf("%s: exit=%d reason=%v out=%q err=%q, want the %v hand-over", c.name, code, reason, out, errs, c.reason)
		}
		if b493LedgerLines(t, f) != before {
			t.Errorf("%s: the hand-over wrote ledger rows", c.name)
		}
	}
}

func b587NewRows(t *testing.T, f *closeReviewFixture, before int) []string {
	t.Helper()
	got := []string{}
	for _, line := range strings.Split(b493LedgerLines(t, f)[before:], "\n") {
		if line == "" {
			continue
		}
		o, err := ledger.DecodeObject([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		o.Delete("mac")
		o.Delete("ts")
		o.Delete("prev")
		data, _ := ledger.Stringify(o, false)
		got = append(got, string(data))
	}
	return got
}

// B587: a routine close under an active close:routine grant signs the
// excluded rows after the grant and coder.tests rows, as Node does
// (CODE_FORGE_NATIVE=0 on the same fixture; mac, ts and prev left out).
func TestB587RoutineCloseExcludedRows(t *testing.T) {
	f := b587Fixture(t, nil, b587Lock(b587GoodLock))
	cfg := b587Config()
	cfg.Set("close", ledger.Object{{Key: "require_human", Value: true}})
	b493SetConfig(t, f, cfg)
	record, err := state.ReadRun(f.home, "r-close-fixture")
	if err != nil {
		t.Fatal(err)
	}
	record.Set("autopilot", ledger.Object{{Key: "grant_id", Value: "g-1"}, {Key: "status", Value: "active"}, {Key: "scopes", Value: []any{"close:routine"}}, {Key: "deny", Value: []any{}}, {Key: "delegate", Value: "L2"}, {Key: "until", Value: "2999-01-01T00:00:00.000Z"}, {Key: "caps", Value: ledger.Object{}}, {Key: "stop_at", Value: 0.8}, {Key: "started_at", Value: "2019-01-01T00:00:00.000Z"}})
	if err := state.SaveRun(f.home, record); err != nil {
		t.Fatal(err)
	}
	b493Sign(t, f, ledger.Object{{Key: "event", Value: "gate.done"}, {Key: "block", Value: "B1"}, {Key: "all_ok", Value: true}})
	before := len(b493LedgerLines(t, f))
	code, out, _, reason := b493Close(f)
	if code != 0 || reason != 0 || out != b587Reviewed+"not reviewed (review.exclude **/package-lock.json): package-lock.json\nblock B1 closed\n" {
		t.Fatalf("exit=%d reason=%v out=%q", code, reason, out)
	}
	want := []string{
		`{"run":"r-close-fixture","event":"autopilot.close","block":"B1","grant":"g-1","tokens_source":null,"cost_source":null}`,
		`{"run":"r-close-fixture","event":"coder.tests","block":"B1","command":"go test ./...","exit":0,"tokens_source":null,"cost_source":null}`,
		`{"run":"r-close-fixture","event":"gate.review_excluded","block":"B1","file":"package-lock.json","content_hash":"9833f3eb647f0e333b6767f5d0418cba912fa5396023055ca57a95173e3bf2f4","rule":"**/package-lock.json","tokens_source":null,"cost_source":null}`,
		`{"run":"r-close-fixture","event":"block.close","block":"B1","status":"complete","tokens_source":null,"cost_source":null}`,
	}
	if got := b587NewRows(t, f, before); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s\nNode:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
