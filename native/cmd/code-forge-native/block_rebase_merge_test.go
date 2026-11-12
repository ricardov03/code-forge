package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/humanask"
)

// B589 (issue #36, bug 1): a feature branch cut before main changed an owned
// file is merged into main with --no-ff after the block opened. The merge
// brings only the branch's own files, so the owned file main already had at
// the block's base is not reported as changed by main.
//
// featTouchesOwned makes the feature branch change a.txt too: then the merge
// does bring an owned change and it must still be reported.
func rebaseMergeFixture(t *testing.T, featTouchesOwned bool) *rebaseCmdFixture {
	t.Helper()
	f := newRebaseCmdFixture(t, map[string]string{"a.txt": "one\n", "b.txt": "b\n", "c.txt": "c\n"})
	f.git(f.repo, "branch", "feat", f.base)
	f.write(f.repo, "a.txt", "one\nX\n")
	f.git(f.repo, "commit", "-qam", "X changes a.txt")
	x := f.git(f.repo, "rev-parse", "HEAD")
	f.git(f.tree, "merge", "-q", "--ff-only", x)
	f.base = x
	f.write(f.tree, "c.txt", "c\nblock\n")
	f.git(f.tree, "commit", "-qam", "A changes c.txt")
	f.git(f.repo, "checkout", "-q", "feat")
	f.write(f.repo, "b.txt", "b\nfeat\n")
	if featTouchesOwned {
		f.write(f.repo, "a.txt", "feat\none\n")
	}
	f.git(f.repo, "commit", "-qam", "feat")
	f.git(f.repo, "checkout", "-q", "main")
	f.git(f.repo, "merge", "-q", "--no-ff", "-m", "M merges feat", "feat")
	f.save("a.txt", "c.txt")
	return f
}

func TestBlockRebaseFeatureMergeNoFalseUpstream(t *testing.T) {
	f := rebaseMergeFixture(t, false)
	code, stdout, stderr := f.rebase("--json")
	if code != 0 || stderr != "" {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want exit 0", code, stdout, stderr)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not one JSON line: %q", stdout)
	}
	newBase := f.git(f.repo, "rev-parse", "main")
	want := map[string]any{"block": "A", "event": "rebased", "old_base": f.base, "new_base": newBase}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("json = %v, want %v (main did not change a.txt since the block's base)", got, want)
	}
	if base := f.block().Get("base_sha"); base != newBase {
		t.Fatalf("base_sha = %v, want %s", base, newBase)
	}
	for _, row := range f.rows() {
		if files, _ := row["upstream_owned"].([]any); row["event"] == "block.rebase" && len(files) != 0 {
			t.Fatalf("block.rebase row names upstream_owned: %v", row)
		}
	}
}

func TestBlockRebaseFeatureMergeOwnedChangeStillReported(t *testing.T) {
	f := rebaseMergeFixture(t, true)
	code, stdout, stderr := f.rebase()
	if code != 0 || !strings.HasPrefix(stdout, "block A rebased ") || !strings.Contains(stdout, "upstream_owned: a.txt (main changed") {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want a.txt reported as changed by main", code, stdout, stderr)
	}
}

// The --accept-changed form of an open block refuses owned files main changed
// (Node's owned-conflict). A feature merge that changed none must not trip it.
func TestBlockRebaseAcceptChangedFeatureMergeNoOwnedConflict(t *testing.T) {
	rebaseTestTTY(t, true, humanask.Confirmed)
	f := rebaseMergeFixture(t, false)
	head := f.git(f.tree, "rev-parse", "HEAD")
	code, stdout, stderr := f.call("A", "--run", "r-rebase-unit", "--accept-changed", "a.txt", "--reason", "main merged feat")
	if code != 2 || stdout != "" || stderr != "block rebase: --accept-changed names files not changed by this rebase: a.txt\n" {
		t.Fatalf("exit=%d stdout=%q stderr=%q, want the usage refusal, not owned-conflict", code, stdout, stderr)
	}
	if got := f.git(f.tree, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD = %s, want unchanged %s", got, head)
	}

	g := rebaseMergeFixture(t, true)
	code, stdout, stderr = g.call("A", "--run", "r-rebase-unit", "--accept-changed", "a.txt", "--reason", "main merged feat")
	if code != 1 || stdout != "" || stderr != "block rebase: block A: commits since its base changed owned files: a.txt\n" {
		t.Fatalf("feat changes a.txt: exit=%d stdout=%q stderr=%q, want owned-conflict naming a.txt", code, stdout, stderr)
	}
}
