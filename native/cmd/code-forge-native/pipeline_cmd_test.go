package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const pipelineFixture = `root: .
steps:
  - id: brief
    skill: client-intake
    outcome: out/brief.json
    approval: human
  - id: logo
    skill: logo-designer
    needs: [brief]
    outcome: out/logo.json
    approval: none
  - id: copy
    skill: seo-writer
    needs: [brief]
    outcome: out/copy.json
    approval: none
  - id: site
    skill: website-kit
    needs: [logo, copy]
    outcome: out/site.json
    approval: human
  - id: bad
    skill: x
    needs: [brief]
    outcome: out/bad.json
    approval: none
  - id: dir
    skill: x
    needs: [brief]
    outcome: out/dirout
    approval: none
`

func TestB455PipelineMatchesNode(t *testing.T) {
	_, repo := smallProject(t, "")
	checkSmallCases(t, []smallCase{
		{[]string{"pipeline", "status"}, 0, "no code-forge.pipeline.yml in " + repo + "\n", ""},
		{[]string{"pipeline", "next", "--json"}, 0, "{\"exists\":false}\n", ""},
		{[]string{"pipeline"}, 2, "", pipelineUsage},
		{[]string{"pipeline", "foo"}, 2, "", pipelineUsage},
		{[]string{"pipeline", "status", "--root"}, 2, "", pipelineUsage},
		{[]string{"pipeline", "status", "--root", "-x"}, 2, "", pipelineUsage},
		{[]string{"pipeline", "status", "--json", "--json"}, 2, "", pipelineUsage},
		{[]string{"pipeline", "next", "--root", "a", "--root", "b"}, 2, "", pipelineUsage},
		{[]string{"pipeline", "--help"}, 0, pipelineUsage, ""},
		{[]string{"pipeline", "next", "-h"}, 0, "usage: code-forge pipeline next [--json] [--root <dir>]\n", ""},
	})
	writeHomeFile(t, repo, "code-forge.pipeline.yml", pipelineFixture)
	writeHomeFile(t, repo, "site/index.html", "hello\n")
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte("hello\n")))
	writeHomeFile(t, repo, "out/brief.json", `{"skill":"client-intake","status":"approved","files":[{"path":"site/index.html","sha256":"`+sum+`"}]}`)
	writeHomeFile(t, repo, "out/logo.json", `{"skill":"logo","status":"draft","files":[]}`)
	writeHomeFile(t, repo, "out/bad.json", `{nope`)
	if err := os.MkdirAll(filepath.Join(repo, "out", "dirout"), 0o700); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(repo, "out")
	checkSmallCases(t, []smallCase{
		{[]string{"pipeline", "status"}, 0, "brief  approved  client-intake\nlogo  draft  logo-designer\ncopy  ready  seo-writer\nsite  waiting  website-kit\n" +
			"bad  invalid  x  — Outcome \"" + out + "/bad.json\" is not valid JSON\ndir  invalid  x  — Cannot read outcome \"" + out + "/dirout\" (EISDIR)\n", ""},
		{[]string{"pipeline", "status", "--json", "--root", "."}, 0, `{"exists":true,"steps":[{"id":"brief","skill":"client-intake","state":"approved"},{"id":"logo","skill":"logo-designer","state":"draft"},` +
			`{"id":"copy","skill":"seo-writer","state":"ready"},{"id":"site","skill":"website-kit","state":"waiting"},` +
			`{"id":"bad","skill":"x","state":"invalid","problem":"Outcome \"` + out + `/bad.json\" is not valid JSON"},{"id":"dir","skill":"x","state":"invalid","problem":"Cannot read outcome \"` + out + `/dirout\" (EISDIR)"}]}` + "\n", ""},
		{[]string{"pipeline", "next"}, 0, "copy\n", ""},
		{[]string{"pipeline", "next", "--json"}, 0, `{"exists":true,"ready":["copy"],"waiting":[{"id":"site","needs":["logo","copy"]}],"done":false}` + "\n", ""},
	})
	writeHomeFile(t, repo, "site/index.html", "changed\n")
	checkSmallCases(t, []smallCase{{[]string{"pipeline", "next"}, 0, "nothing ready\nlogo  waiting for brief\ncopy  waiting for brief\nsite  waiting for logo, copy\nbad  waiting for brief\ndir  waiting for brief\n", ""}})
	writeHomeFile(t, repo, "code-forge.pipeline.yml", "root: .\nsteps:\n  - {id: a, skill: s, outcome: a.json, approval: none}\n")
	writeHomeFile(t, repo, "a.json", `{"skill":"s","status":"approved","files":[],"checks":[{"name":"lint","result":"pass"}]}`)
	checkSmallCases(t, []smallCase{{[]string{"pipeline", "next"}, 0, "all steps approved\n", ""}})
}

func TestB455PipelineProblems(t *testing.T) {
	_, repo := smallProject(t, "")
	writeHomeFile(t, repo, "code-forge.pipeline.yml", "root: .\nsteps:\n  - {id: a, skill: s, outcome: a.json, approval: none, needs: [b, zz]}\n  - {id: b, skill: s, outcome: b.json, approval: none, needs: [a]}\n  - {id: a, skill: s, outcome: c.json, approval: none}\n")
	checkSmallCases(t, []smallCase{{[]string{"pipeline", "status"}, 1, "", "duplicate step id \"a\"\nstep \"a\": unknown need \"zz\"\n"}})
	writeHomeFile(t, repo, "code-forge.pipeline.yml", "root: .\nsteps:\n  - {id: a, skill: s, outcome: a.json, approval: none, needs: [b]}\n  - {id: b, skill: s, outcome: b.json, approval: none, needs: [a]}\n")
	checkSmallCases(t, []smallCase{{[]string{"pipeline", "next", "--json"}, 1, "", "cycle at step \"a\"\n"}})
	if err := os.Remove(filepath.Join(repo, "code-forge.pipeline.yml")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "code-forge.pipeline.yml"), 0o700); err != nil {
		t.Fatal(err)
	}
	checkSmallCases(t, []smallCase{{[]string{"pipeline", "status"}, 1, "", "Cannot read pipeline (EISDIR)\n"}})
}

// AJV and YAML wording stays with Node: a pipeline or an outcome of an
// unsupported shape, and a YAML syntax error, go to Node before any output.
func TestB455PipelineHandsOverSchemaAndYAMLErrors(t *testing.T) {
	_, repo := smallProject(t, "")
	for _, pipeline := range []string{
		"root: .\nsteps:\n  - {id: A, skill: s, outcome: a.json, approval: maybe}\n",
		"root: .\nsteps: [\n",
		"",
	} {
		writeHomeFile(t, repo, "code-forge.pipeline.yml", pipeline)
		expectHandover(t, "pipeline-unsupported", "pipeline", "status")
	}
	writeHomeFile(t, repo, "code-forge.pipeline.yml", "root: .\nsteps:\n  - {id: a, skill: s, outcome: a.json, approval: none}\n")
	writeHomeFile(t, repo, "a.json", `{"skill":"s","status":"done","files":[]}`)
	expectHandover(t, "pipeline-unsupported", "pipeline", "next")
}
