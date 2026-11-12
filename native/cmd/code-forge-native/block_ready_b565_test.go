package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B565: a random temp directory can hold a secret-like prefix ("eyJ", "AKIA",
// "AIza", "sk-"). Node prints such a workspace path as it is, so block ready
// stays in Go there; CI run 43275901477 handed one such call to Node.
func TestBlockReadySecretLikeWorkspacePathRunsInGo(t *testing.T) {
	cfg := `{"gates":{"test":["node","--test"],"lint":null,"types":null,"format":null,"rerun_failed":0}}`
	plain := newReadyFixture(t)
	plain.setSnapshot(cfg, nil)
	_, want, _ := plain.call("block", "ready", "B1", "--run", plain.run, "--json")
	if !strings.HasPrefix(want, `{"block":"B1","ready":false,`) || !strings.Contains(want, `"code":"no_green_gates"`) {
		t.Fatalf("baseline stdout=%q", want)
	}
	for _, dir := range []string{"cf-parity-eyJx9a", "cf-AKIAqz", "cf-AIzaQ1", "risk-sk-1"} {
		root := filepath.Join(t.TempDir(), dir)
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatal(err)
		}
		f := newReadyFixtureAt(t, root)
		f.setSnapshot(cfg, nil)
		code, stdout, stderr := f.call("block", "ready", "B1", "--run", f.run, "--json")
		if code != 1 || stdout != want || stderr != "" {
			t.Fatalf("%s: code=%d stdout=%q stderr=%q; want code=1 stdout=%q, no hand-over", dir, code, stdout, stderr, want)
		}
	}
}

// The one text Node masks in ready output is the run's signer key in hex; a
// blocker that would print it still goes to Node (here unavailable: exit 127).
func TestBlockReadySignerKeyTextHandsOver(t *testing.T) {
	f := newReadyFixture(t)
	keyHex := strings.Repeat("29", 32)
	if err := state.UpdateRun(f.home, f.run, func(record *ledger.Object) error {
		record.Set("orphans", []any{keyHex + ".txt"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := f.call("block", "ready", "B1", "--run", f.run, "--json")
	if code != 127 || stdout != "" || !strings.Contains(stderr, "node") {
		t.Fatalf("code=%d stdout=%q stderr=%q; want the Node hand-over", code, stdout, stderr)
	}
}
