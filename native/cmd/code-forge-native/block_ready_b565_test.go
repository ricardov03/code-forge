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

// B713: the one text Node masks in ready output is the run's signer key in
// hex (writeSafe after loadKey registered it). Go prints it masked too, with
// no hand-over (Node is unreachable here). Node builds each line with the
// key's hex (a plain word: no shell quotes in the fix) and writeSafe then
// swaps it for [REDACTED], so the golden is the same fixture's output for a
// plain orphan name with that name swapped, in --json and in text form.
func TestB713BlockReadySignerKeyTextRunsInGo(t *testing.T) {
	f := newReadyFixture(t)
	keyHex := strings.Repeat("29", 32)
	setOrphan := func(name string) {
		if err := state.UpdateRun(f.home, f.run, func(record *ledger.Object) error {
			record.Set("orphans", []any{name})
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, form := range [][]string{{"--json"}, {}} {
		args := append([]string{"block", "ready", "B1", "--run", f.run}, form...)
		setOrphan("plainorphan.txt")
		wantCode, plain, wantErr := f.call(args...)
		want := strings.ReplaceAll(plain, "plainorphan", "[REDACTED]")
		if wantCode != 1 || !strings.Contains(plain, "orphan in run registry: plainorphan.txt") || wantErr != "" {
			t.Fatalf("%v baseline: code=%d stdout=%q stderr=%q", form, wantCode, plain, wantErr)
		}
		setOrphan(keyHex + ".txt")
		code, stdout, stderr := f.call(args...)
		if code != 1 || stdout != want || stderr != "" {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q; want code=1 stdout=%q, no hand-over", form, code, stdout, stderr, want)
		}
	}
}
