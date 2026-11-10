package blockopen

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// The reporter's shape (issue #35 P5/G3): a shared Kotlin composable that a
// brief expects to take a slot, while the base commit's version does not.
const cardKt = `package ui

@Composable
fun Card(
    title: String,
    modifier: Modifier = Modifier,
) {
    Text(title)
}
`

type fitFixture struct{ home, repo, run, base string }

func newFitFixture(t *testing.T) fitFixture {
	t.Helper()
	repo, home := t.TempDir(), t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-qb", "main")
	if err := os.MkdirAll(filepath.Join(repo, "ui"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "ui/Card.kt"), []byte(cardKt), 0644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=f@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "base")
	run := "r-apifit-unit"
	record := ledger.Object{{Key: "run_id", Value: run}, {Key: "project", Value: "fixture"}, {Key: "workspace", Value: repo}, {Key: "status", Value: "active"}, {Key: "blocks", Value: ledger.Object{}}, {Key: "orphans", Value: []any{}}}
	if err := state.SaveRun(home, record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".code-forge/runs", run+".key"), make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	return fitFixture{home: home, repo: repo, run: run, base: git("rev-parse", "HEAD")}
}

func (f fitFixture) open(t *testing.T, brief string) (Result, error) {
	t.Helper()
	acceptance, err := ParseAcceptance([]byte("- clause: screen shows the card\n  tests: [app/ScreenTest.kt]\n"))
	if err != nil {
		t.Fatal(err)
	}
	return Open(Options{Home: f.home, Run: f.run, ID: "B1", Level: "L2", Owned: []string{"app/Screen.kt"}, Acceptance: acceptance, Attempt: 1, Floor: "L1", Safe: func(ledger.Object) bool { return true }, BriefPath: "brief.md", BriefData: []byte(brief)})
}

func (f fitFixture) status(t *testing.T) any {
	t.Helper()
	record, err := state.ReadRun(f.home, f.run)
	if err != nil {
		t.Fatal(err)
	}
	return object(object(record.Get("blocks")).Get("B1")).Get("status")
}

func TestAPIFitRefusesMissingParameterAtBase(t *testing.T) {
	f := newFitFixture(t)
	_, err := f.open(t, "---\ntitle: Screen\nuses:\n  ui/Card.kt: Card(title, slot)\n---\n# Screen\n")
	var refused *state.Error
	if !errors.As(err, &refused) || refused.Code != "api-fit" {
		t.Fatalf("open with a mismatched uses line: err=%v; want an api-fit refusal", err)
	}
	lines := strings.Split(refused.Message, "\n")
	if lines[0] != "api-fit: ui/Card.kt Card expects (title,slot) found (title,modifier)" {
		t.Errorf("first refusal line = %q", lines[0])
	}
	if got := f.status(t); got != nil {
		t.Errorf("a refused open recorded block B1 with status %v", got)
	}
}

func TestAPIFitOpensWhenParametersMatch(t *testing.T) {
	f := newFitFixture(t)
	result, err := f.open(t, "# Screen\n\n```uses\nui/Card.kt: Card(title, modifier)\n```\n")
	if err != nil {
		t.Fatalf("open with a matching uses line: %v", err)
	}
	if result.Block.Get("status") != "open" || result.Block.Get("base_sha") != f.base {
		t.Errorf("block = status %v base %v; want open at %s", result.Block.Get("status"), result.Block.Get("base_sha"), f.base)
	}
	if got := f.status(t); got != "open" {
		t.Errorf("run record status = %v; want open", got)
	}
}

func TestAPIFitExtendsOwnsFileAsAdditive(t *testing.T) {
	f := newFitFixture(t)
	result, err := f.open(t, "---\nuses:\n  ui/Card.kt: Card(title)\nextends:\n  ui/Card.kt: Card\n---\n")
	if err != nil {
		t.Fatalf("open with an extends line: %v", err)
	}
	owned, _ := result.Block.Get("owned_files").([]any)
	if len(owned) != 2 || owned[0] != "app/Screen.kt" || owned[1] != "ui/Card.kt" {
		t.Errorf("owned_files = %v; want [app/Screen.kt ui/Card.kt]", owned)
	}
	additive, _ := result.Block.Get("additive").([]any)
	if len(additive) != 1 || object(additive[0]).Get("path") != "ui/Card.kt" || object(additive[0]).Get("symbol") != "Card" {
		t.Errorf("additive = %v; want one entry {path: ui/Card.kt, symbol: Card}", additive)
	}
}

func TestAPIFitSkipsUnknownExtensionWithNote(t *testing.T) {
	f := newFitFixture(t)
	result, err := f.open(t, "```uses\nlib/card.rb: card(title, slot)\n```\n")
	if err != nil {
		t.Fatalf("open with an unknown extension: %v", err)
	}
	notes, _ := object(result.Block.Get("api_fit")).Get("notes").([]any)
	if len(notes) != 1 || notes[0] != `api-fit: lib/card.rb card skipped (no rule for ".rb" files)` {
		t.Errorf("api_fit notes = %v; want one skip note for lib/card.rb", notes)
	}
}
