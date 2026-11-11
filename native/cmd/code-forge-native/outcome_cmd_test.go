package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func outcomeFixture(t *testing.T) (repo, sum string) {
	_, repo = smallProject(t, "")
	writeHomeFile(t, repo, "site/index.html", "hello\n")
	if err := os.MkdirAll(filepath.Join(repo, "site", "dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(repo, "site", "link")); err != nil {
		t.Fatal(err)
	}
	return repo, fmt.Sprintf("%x", sha256.Sum256([]byte("hello\n")))
}

func TestB455OutcomeCheckMatchesNode(t *testing.T) {
	repo, sum := outcomeFixture(t)
	writeHomeFile(t, repo, "good.json", `{"skill":"s","status":"approved","files":[{"path":"site/index.html","sha256":"`+sum+`"}],"checks":[{"name":"lint","result":"pass"}]}`)
	writeHomeFile(t, repo, "many.json", `{"skill":"s","status":"draft","files":[{"path":"site/index.html","sha256":"x"},{"path":"/abs","sha256":""},{"path":"a/../b","sha256":""},`+
		`{"path":"C:/x","sha256":""},{"path":"nope","sha256":""},{"path":"site/link","sha256":""},{"path":"site/dir","sha256":""},{"path":"site/index.html/x","sha256":""},{"path":"","sha256":""}],`+
		`"checks":[{"name":"a","result":"fail"},{"name":"b","result":"warn"},{"name":"c","result":"fail"}]}`)
	writeHomeFile(t, repo, "bad.json", "{nope\n")
	if err := os.MkdirAll(filepath.Join(repo, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	many := []string{`status "draft": must be approved`, `check "a": result is fail`, `check "c": result is fail`, `file "site/index.html": sha256 does not match`,
		`file "/abs": path must be relative to root without ..`, `file "a/../b": path must be relative to root without ..`, `file "C:/x": path must be relative to root without ..`,
		`file "nope": missing file`, `file "site/link": path leaves root through a symlink`, `file "site/dir": cannot read (EISDIR)`, `file "site/index.html/x": cannot read (ENOTDIR)`, `file "": cannot read (EISDIR)`}
	manyText, manyJSON := "", ""
	for i, p := range many {
		manyText += p + "\n"
		if i > 0 {
			manyJSON += ","
		}
		manyJSON += jsQuote(p)
	}
	checkSmallCases(t, []smallCase{
		{[]string{"outcome", "check", "good.json"}, 0, "ok\n", ""},
		{[]string{"outcome", "check", "good.json", "--json"}, 0, `{"ok":true,"problems":[]}` + "\n", ""},
		{[]string{"outcome", "check", "many.json"}, 1, "", manyText},
		{[]string{"outcome", "check", "many.json", "--json"}, 1, `{"ok":false,"problems":[` + manyJSON + "]}\n", ""},
		{[]string{"outcome", "check", "bad.json"}, 1, "", "Outcome \"bad.json\" is not valid JSON\n"},
		{[]string{"outcome", "check", "missing.json", "--json"}, 1, `{"ok":false,"problems":["Cannot read outcome \"missing.json\" (ENOENT)"]}` + "\n", ""},
		{[]string{"outcome", "check", "site"}, 1, "", "Cannot read outcome \"site\" (EISDIR)\n"},
		{[]string{"outcome", "check", "good.json", "--root", "sub"}, 1, "", "file \"site/index.html\": missing file\n"},
		{[]string{"outcome", "record", "many.json", "--step", "s1"}, 1, "", manyText},
		{[]string{"outcome", "--help"}, 0, outcomeUsage, ""},
		{[]string{"outcome", "check", "-h"}, 0, "usage: code-forge outcome check <outcome.json> [--root <dir>] [--json]\n", ""},
		{[]string{"outcome", "record", "--help"}, 0, "usage: code-forge outcome record <outcome.json> [--root <dir>] [--step <id>] [--json]\n", ""},
	})
	for _, args := range [][]string{{}, {"x"}, {"check"}, {"check", "a", "b"}, {"check", "good.json", "--json", "--json"}, {"seal", "good.json", "--json"},
		{"record", "--step"}, {"check", "--root"}, {"check", "good.json", "-x"}, {"check", "good.json", "--help"}, {"record", "g", "--step", "a", "--step", "b"}, {"check", "--step", "a", "g"}} {
		checkSmallCases(t, []smallCase{{append([]string{"outcome"}, args...), 2, "", outcomeUsage}})
	}
}

func TestB455OutcomeSealMatchesNode(t *testing.T) {
	repo, sum := outcomeFixture(t)
	writeHomeFile(t, repo, "seal.json", `{"status":"approved","skill":"s","files":[{"sha256":"old","path":"site/index.html"},{"path":"site/index.html","sha256":""}],"cost_usd":1.50,"minutes":1e21,"decisions":["é","\u2028"],"approved_by":null}`)
	if err := os.Chmod(filepath.Join(repo, "seal.json"), 0o640); err != nil {
		t.Fatal(err)
	}
	checkSmallCases(t, []smallCase{{[]string{"outcome", "seal", "seal.json"}, 0, "", ""}})
	data, _ := os.ReadFile(filepath.Join(repo, "seal.json"))
	want := "{\n  \"status\": \"approved\",\n  \"skill\": \"s\",\n  \"files\": [\n    {\n      \"sha256\": \"" + sum + "\",\n      \"path\": \"site/index.html\"\n    },\n" +
		"    {\n      \"path\": \"site/index.html\",\n      \"sha256\": \"" + sum + "\"\n    }\n  ],\n  \"cost_usd\": 1.5,\n  \"minutes\": 1e+21,\n" +
		"  \"decisions\": [\n    \"é\",\n    \"\u2028\"\n  ],\n  \"approved_by\": null\n}\n"
	if string(data) != want {
		t.Fatalf("sealed = %q\nwant %q", data, want)
	}
	if info, _ := os.Stat(filepath.Join(repo, "seal.json")); info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v", info.Mode())
	}
	original := `{"skill":"s","status":"draft","files":[{"path":"site/index.html","sha256":""},{"path":"nope","sha256":""}]}`
	writeHomeFile(t, repo, "partial.json", original)
	checkSmallCases(t, []smallCase{
		{[]string{"outcome", "seal", "partial.json"}, 1, "", "file \"nope\": missing file\n"},
		{[]string{"outcome", "seal", "missing.json"}, 1, "", "Cannot read outcome \"missing.json\" (ENOENT)\n"},
		{[]string{"outcome", "seal", "seal.json", "--root", "nowhere"}, 1, "", "file \"site/index.html\": missing file\n"},
	})
	if data, _ := os.ReadFile(filepath.Join(repo, "partial.json")); string(data) != original {
		t.Fatalf("a failed seal changed the file: %q", data)
	}
}

// AJV wording, an outcome Go cannot rewrite, and signing a passing record
// stay with Node.
func TestB455OutcomeHandsOverSchemaUnwritableAndRecord(t *testing.T) {
	repo, sum := outcomeFixture(t)
	writeHomeFile(t, repo, "schema.json", `{"skill":1,"status":"approved","files":[]}`)
	expectHandover(t, "outcome-unsupported", "outcome", "check", "schema.json")
	expectHandover(t, "outcome-unsupported", "outcome", "seal", "schema.json")
	writeHomeFile(t, repo, "locked.json", `{"skill":"s","status":"approved","files":[]}`)
	if err := os.Chmod(filepath.Join(repo, "locked.json"), 0o444); err != nil {
		t.Fatal(err)
	}
	expectHandover(t, "outcome-unsupported", "outcome", "seal", "locked.json")
	writeHomeFile(t, repo, "good.json", `{"skill":"s","status":"approved","files":[{"path":"site/index.html","sha256":"`+sum+`"}]}`)
	expectHandover(t, "outcome-record-unported", "outcome", "record", "good.json", "--json")
}
