package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/sign"
)

// b471RecordProject is a project whose ledger holds a run.start for r1 and
// whose run key is a fixed 32 bytes, as `run start` leaves them.
func b471RecordProject(t *testing.T, keyMode os.FileMode) (home, repo, sum string, key []byte) {
	repo, sum = outcomeFixture(t)
	home = os.Getenv("HOME")
	key = []byte("0123456789abcdef0123456789abcdef")
	writeHomeFile(t, home, ".code-forge/runs/r1.key", string(key))
	if err := os.Chmod(filepath.Join(home, ".code-forge/runs/r1.key"), keyMode); err != nil {
		t.Fatal(err)
	}
	writeHomeFile(t, home, ".code-forge/ledger/repo.jsonl", `{"run":"r0","event":"run.start"}`+"\n"+`{"run":"r1","event":"run.start"}`+"\n")
	return home, repo, sum, key
}

func b471LedgerLines(t *testing.T, home string) []string {
	data, err := os.ReadFile(filepath.Join(home, ".code-forge/ledger/repo.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// A passing record signs one outcome.recorded row under the run lock, with
// Node's field order, output and the already-recorded short cut.
func TestB471OutcomeRecordSignsPassingOutcome(t *testing.T) {
	home, repo, sum, key := b471RecordProject(t, 0o600)
	writeHomeFile(t, repo, "good.json", `{"skill":"s","status":"approved","files":[{"path":"site/index.html","sha256":"`+sum+`"}],"cost_usd":1.50}`)
	code, out, errText := runCaptured(t, "outcome", "record", "good.json", "--json")
	if code != 0 || errText != "" {
		t.Fatalf("record: code=%d stderr=%q", code, errText)
	}
	shape := regexp.MustCompile(`^\{"ok":true,"problems":\[\],"already_recorded":false,"row":(\{"run":"r1","event":"outcome.recorded","skill":"s","step":null,"status":"approved","sha256":"[0-9a-f]{64}","cost_usd":1.5,"minutes":null,"ts":"[^"]+","mac":"[0-9a-f]{64}","tokens_source":null,"cost_source":"estimated"\})\}\n$`)
	m := shape.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("record --json = %q", out)
	}
	lines := b471LedgerLines(t, home)
	if len(lines) != 3 || lines[2] != m[1] {
		t.Fatalf("ledger = %q, want last line %q", lines, m[1])
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(m[1]), &row); err != nil {
		t.Fatal(err)
	}
	if result, err := sign.VerifyRow(row, key); err != nil || !result.OK {
		t.Fatalf("row does not verify with the run key: %+v %v", result, err)
	}
	if _, err := os.Stat(filepath.Join(home, ".code-forge/runs/r1.lock")); !os.IsNotExist(err) {
		t.Fatalf("run lock left behind: %v", err)
	}
	checkSmallCases(t, []smallCase{
		{[]string{"outcome", "record", "good.json"}, 0, "already recorded\n", ""},
		{[]string{"outcome", "record", "good.json", "--json"}, 0, `{"ok":true,"problems":[],"already_recorded":true}` + "\n", ""},
		{[]string{"outcome", "record", "good.json", "--step", "x"}, 0, "recorded\n", ""},
		{[]string{"outcome", "record", "good.json", "--step", "x"}, 0, "already recorded\n", ""},
	})
	if lines := b471LedgerLines(t, home); len(lines) != 4 || !strings.Contains(lines[3], `"step":"x"`) {
		t.Fatalf("ledger after --step = %q", lines)
	}
}

// The step comes from the pipeline when its outcome path names the file.
func TestB471OutcomeRecordTakesStepFromPipeline(t *testing.T) {
	home, repo, sum, _ := b471RecordProject(t, 0o600)
	writeHomeFile(t, repo, "out/a.json", `{"skill":"s","status":"approved","files":[{"path":"site/index.html","sha256":"`+sum+`"}]}`)
	writeHomeFile(t, repo, "code-forge.pipeline.yml", "root: .\nsteps:\n  - id: first\n    skill: s\n    outcome: out/a.json\n    approval: none\n")
	checkSmallCases(t, []smallCase{{[]string{"outcome", "record", "out/a.json"}, 0, "recorded\n", ""}})
	if lines := b471LedgerLines(t, home); !strings.Contains(lines[len(lines)-1], `"step":"first"`) || !strings.Contains(lines[len(lines)-1], `"cost_usd":null,"minutes":null`) {
		t.Fatalf("ledger = %q", lines)
	}
}

// Node's refusals: no run.start, a key other users can read, a bad slug.
func TestB471OutcomeRecordRefusalsMatchNode(t *testing.T) {
	_, repo, sum, _ := b471RecordProject(t, 0o644)
	writeHomeFile(t, repo, "good.json", `{"skill":"s","status":"approved","files":[{"path":"site/index.html","sha256":"`+sum+`"}]}`)
	checkSmallCases(t, []smallCase{
		{[]string{"outcome", "record", "good.json"}, 1, "", "run r1 signer key is accessible to other users (mode 644); expected 600\n"},
	})
	if err := os.Remove(filepath.Join(os.Getenv("HOME"), ".code-forge/ledger/repo.jsonl")); err != nil {
		t.Fatal(err)
	}
	checkSmallCases(t, []smallCase{
		{[]string{"outcome", "record", "good.json", "--json"}, 1, `{"ok":false,"problems":["no project run to sign the outcome; start a run first"]}` + "\n", ""},
	})
	writeHomeFile(t, repo, ".code-forge.yml", "project:\n  slug: Bad_Slug\n")
	checkSmallCases(t, []smallCase{
		{[]string{"outcome", "record", "good.json"}, 1, "", "ledgerPath: slug must match /^[a-z0-9][a-z0-9-]*$/ (got \"Bad_Slug\")\n"},
	})
}
