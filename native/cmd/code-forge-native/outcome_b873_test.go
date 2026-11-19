package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestB873OutcomeNamesTheSchemaProblem is the B801 audit's P02 sample: an
// outcome that is valid JSON with the wrong shape ({"step","files"}), or not
// UTF-8, is refused with the problem named and exit 1, not handed over.
func TestB873OutcomeNamesTheSchemaProblem(t *testing.T) {
	repo, _ := outcomeFixture(t)
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "")
	writeHomeFile(t, repo, "wrong.json", `{"step":"s1","files":[{"path":"site/index.html"}]}`)
	writeHomeFile(t, repo, "types.json", `{"skill":1,"status":"done","files":[{"path":2,"sha256":"","x":1}],"decisions":[3],"checks":[{"name":"n","result":"ok","detail":"d"}],"cost_usd":"1"}`)
	writeHomeFile(t, repo, "array.json", `[1]`)
	writeHomeFile(t, repo, "latin1.json", "{\"skill\":\"caf\xe9\",\"status\":\"draft\",\"files\":[]}")
	wrong := `Outcome "wrong.json" does not match the outcome schema: unknown field "step"; missing field "skill"; missing field "status"; "files[0]" is missing field "sha256"`
	types := `Outcome "types.json" does not match the outcome schema: "skill" must be a string; "status" must be one of draft, approved, blocked, failed; "files[0]" has unknown field "x"; "files[0].path" must be a string; "decisions[0]" must be a string; "checks[0].result" must be one of pass, warn, fail; "checks[0].detail" must be an array of strings; "cost_usd" must be a number or null`
	array := `Outcome "array.json" does not match the outcome schema: must be a JSON object`
	latin1 := `Outcome "latin1.json" is not UTF-8 text`
	checkSmallCases(t, []smallCase{
		{[]string{"outcome", "seal", "wrong.json"}, 1, "", wrong + "\n"},
		{[]string{"outcome", "check", "wrong.json"}, 1, "", wrong + "\n"},
		{[]string{"outcome", "check", "wrong.json", "--json"}, 1, `{"ok":false,"problems":["` + jsonInner(wrong) + `"]}` + "\n", ""},
		{[]string{"outcome", "record", "wrong.json", "--step", "s1", "--json"}, 1, `{"ok":false,"problems":["` + jsonInner(wrong) + `"]}` + "\n", ""},
		{[]string{"outcome", "check", "types.json"}, 1, "", types + "\n"},
		{[]string{"outcome", "seal", "array.json"}, 1, "", array + "\n"},
		{[]string{"outcome", "seal", "latin1.json"}, 1, "", latin1 + "\n"},
		{[]string{"outcome", "check", "latin1.json"}, 1, "", latin1 + "\n"},
		{[]string{"outcome", "record", "latin1.json", "--step", "s1"}, 1, "", latin1 + "\n"},
	})
	if data, _ := os.ReadFile(handoverLogPath()); strings.Contains(string(data), "outcome") {
		t.Fatalf("a hand-over row was written: %s", data)
	}
	if data, _ := os.ReadFile(filepath.Join(repo, "wrong.json")); string(data) != `{"step":"s1","files":[{"path":"site/index.html"}]}` {
		t.Fatalf("seal changed the file: %q", data)
	}
}

// jsonInner is s as a JSON string body (quotes escaped).
func jsonInner(s string) string {
	out := ""
	for _, r := range s {
		if r == '"' || r == '\\' {
			out += `\`
		}
		out += string(r)
	}
	return out
}
