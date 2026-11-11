package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ricardov03/code-forge/native/canon"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/state"
)

// B385: Go land and close auto-claims guard .code-forge.yml with Node's
// guardedConfigPaths reason, in Node's order and with Node's bytes.
func TestAutoClaimConfigGuardMatchesNode(t *testing.T) {
	t.Setenv("CODE_FORGE_NODE", "/nonexistent")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	refusal := func(why string) string {
		return `owned entry ".code-forge.yml" covers the project's .code-forge.yml; no block may own it while ` + why
	}
	hook := map[string]any{"argv": []any{"true"}}
	external := map[string]any{"required": true, "name": "ext"}
	runners := []any{map[string]any{"name": "r", "argv": []any{"true"}}}
	cases := []struct {
		name string
		cfg  map[string]any
		want string // "" means the claim is not refused as config-owned
	}{
		{"hook only", map[string]any{"hooks": map[string]any{"on_block_end": hook}}, refusal("hooks.on_block_end is set")},
		{"hook with timeout", map[string]any{"hooks": map[string]any{"on_block_end": map[string]any{"argv": []any{"true"}, "timeout_s": 5}}}, refusal("hooks.on_block_end is set")},
		{"external before hook", map[string]any{"review": map[string]any{"external": external}, "hooks": map[string]any{"on_block_end": hook}}, refusal("review.external.required is true")},
		{"hook before runners", map[string]any{"hooks": map[string]any{"on_block_end": hook}, "proof": map[string]any{"runners": runners}}, refusal("hooks.on_block_end is set")},
		{"BOM-only external name does not count", map[string]any{"review": map[string]any{"external": map[string]any{"required": true, "name": "\ufeff"}}, "hooks": map[string]any{"on_block_end": hook}}, refusal("hooks.on_block_end is set")},
		{"empty-argv hook does not count", map[string]any{"hooks": map[string]any{"on_block_end": map[string]any{"argv": []any{}}}, "proof": map[string]any{"runners": runners}}, refusal("proof.runners is set")},
		{"runners only", map[string]any{"proof": map[string]any{"runners": runners}}, refusal("proof.runners is set")},
		{"no key", map[string]any{}, ""},
	}
	paths := map[string]func(home, run, top string, record ledger.Object, print io.Writer) error{
		"land": func(home, run, top string, record ledger.Object, print io.Writer) error {
			return landAutoClaim(home, run, "B1", top, record, []string{".code-forge.yml"}, print, false)
		},
		"close": func(home, run, top string, record ledger.Object, print io.Writer) error {
			return closeAutoClaim(home, run, "B1", top, record, []string{".code-forge.yml"}, print, false)
		},
	}
	for _, verb := range []string{"land", "close"} {
		for _, tc := range cases {
			t.Run(verb+"/"+tc.name, func(t *testing.T) {
				home, repo, record := autoClaimGuardFixture(t, tc.cfg)
				t.Setenv("HOME", home)
				file := filepath.Join(home, ".code-forge", "runs", "r-guard-unit.json")
				before, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				var print bytes.Buffer
				err = paths[verb](home, "r-guard-unit", repo, record, &print)
				var refused *state.Error
				isGuard := errors.As(err, &refused) && refused.Code == "config-owned"
				if tc.want == "" {
					if isGuard {
						t.Fatalf("no guard key: want no config-owned refusal, got %q", refused.Message)
					}
					saved, err := state.ReadRun(home, "r-guard-unit")
					if err != nil {
						t.Fatal(err)
					}
					owned, _ := saved.Get("blocks").(ledger.Object).Get("B1").(ledger.Object).Get("owned_files").([]any)
					if len(owned) != 1 || owned[0] != ".code-forge.yml" {
						t.Fatalf("no guard key: want exactly one claimed .code-forge.yml, got %v", owned)
					}
					return
				}
				if !isGuard {
					t.Fatalf("want config-owned refusal %q, got %v", tc.want, err)
				}
				if refused.Message != tc.want || err.Error() != tc.want {
					t.Fatalf("refusal bytes:\n got %q\nwant %q", refused.Message, tc.want)
				}
				if print.Len() != 0 {
					t.Fatalf("refusal printed %q, want nothing", print.String())
				}
				after, err := os.ReadFile(file)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("refusal changed the run record (err %v)", err)
				}
			})
		}
	}
}

// A real git repository and a saved run whose config snapshot is cfg.
func autoClaimGuardFixture(t *testing.T, extra map[string]any) (string, string, ledger.Object) {
	t.Helper()
	home := t.TempDir()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := landTestConfig(nil)
	for k, v := range extra {
		cfg[k] = v
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".code-forge.yml"), data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", "-qb", "main"}, {"-c", "user.name=Fixture", "-c", "user.email=f@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "base"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = repo, gitx.ChildEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	snapshot, err := ledger.DecodeObject(data)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canon.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	hash := hex.EncodeToString(digest[:])
	record := ledger.Object{
		{Key: "run_id", Value: "r-guard-unit"}, {Key: "project", Value: "fixture"},
		{Key: "workspace", Value: repo}, {Key: "status", Value: "active"}, {Key: "orphans", Value: []any{}},
		{Key: "config", Value: ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: snapshot}}}}},
		{Key: "blocks", Value: ledger.Object{
			{Key: "B1", Value: ledger.Object{{Key: "status", Value: "open"}, {Key: "owned_files", Value: []any{}}, {Key: "owned_kinds", Value: ledger.Object{}}}},
		}},
	}
	if err := state.SaveRun(home, record); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".code-forge", "runs", "r-guard-unit.key"), bytes.Repeat([]byte{7}, 32), 0600); err != nil {
		t.Fatal(err)
	}
	return home, repo, record
}
