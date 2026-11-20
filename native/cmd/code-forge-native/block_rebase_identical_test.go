package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/humanask"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
)

// A signed checkpoint represents a completed replay awaiting clearance. The
// reviewed binary and the replayed binary differ; the base has the approved one.
func b1014Fixture(t *testing.T, status string) *rebaseCmdFixture {
	f := newRebaseCmdFixture(t, map[string]string{"asset.bin": "\x00base", "approved.bin": "\x00approved"})
	f.write(f.tree, "asset.bin", "\x00reviewed")
	f.git(f.tree, "commit", "-qam", "block binary")
	oldHead := f.git(f.tree, "rev-parse", "HEAD")
	f.git(f.repo, "merge", "--ff-only", oldHead)
	f.write(f.repo, "asset.bin", "\x00approved")
	f.git(f.repo, "commit", "-qam", "approved replay")
	head := f.git(f.repo, "rev-parse", "HEAD")
	f.git(f.tree, "merge", "--ff-only", head)
	f.save("asset.bin")
	checkpoint, err := sign.SignRow(ledger.Object{{Key: "run", Value: "r-rebase-unit"}, {Key: "block", Value: "A"}, {Key: "old_base", Value: f.base}, {Key: "old_head", Value: oldHead}, {Key: "head", Value: head}, {Key: "started_at", Value: "2026-01-01T00:00:00.000Z"}}.Map(), bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f.setBlock(func(b *ledger.Object) { b.Set("status", status); b.Set("rebase_check", checkpoint) })
	b1014Configure(t, f, []any{"approved.bin"}, []any{"rebase:accept-identical"}, nil)
	rebaseTestTTY(t, false, humanask.Cancelled)
	return f
}

func b1014Configure(t *testing.T, f *rebaseCmdFixture, sources, scopes, deny []any) {
	t.Helper()
	cfg := ledger.Object{{Key: "rebase", Value: ledger.Object{{Key: "approved_binary_sources", Value: sources}}}}
	hash := config.Hash(cfg)
	err := state.UpdateRun(f.home, "r-rebase-unit", func(r *ledger.Object) error {
		r.Set("config", ledger.Object{{Key: "hash", Value: hash}, {Key: "snapshots", Value: ledger.Object{{Key: hash, Value: cfg}}}})
		r.Set("autopilot", ledger.Object{{Key: "grant_id", Value: "g1014"}, {Key: "status", Value: "active"}, {Key: "scopes", Value: scopes}, {Key: "deny", Value: append([]any{}, deny...)}, {Key: "delegate", Value: "L2"}, {Key: "until", Value: "2999-01-01T00:00:00.000Z"}, {Key: "started_at", Value: "2026-01-01T00:00:00.000Z"}, {Key: "caps", Value: ledger.Object{}}, {Key: "stop_at", Value: 0.5}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = state.WriteSigned(f.home, "r-rebase-unit", ledger.Object{{Key: "event", Value: "autopilot.grant"}, {Key: "grant_id", Value: "g1014"}, {Key: "scopes", Value: scopes}, {Key: "deny", Value: append([]any{}, deny...)}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestB1014RebaseIdentical(t *testing.T) {
	for _, status := range []string{"closed", "awaiting_human_check", "open"} {
		for _, source := range []string{"approved.bin", "main:asset.bin"} {
			t.Run(status+"/"+source, func(t *testing.T) {
				f := b1014Fixture(t, status)
				code, out, errText := f.rebase("--accept-identical", "asset.bin", "--source", source, "--json")
				if code != 0 || errText != "" || !strings.Contains(out, `"event":"rebased"`) {
					t.Fatalf("exit=%d out=%q err=%q", code, out, errText)
				}
				if f.block().Get("rebase_check") != nil || f.block().Get("base_sha") != f.git(f.repo, "rev-parse", "main") {
					t.Fatal("checkpoint/base not updated")
				}
				var accepted map[string]any
				for _, row := range f.rows() {
					if row["event"] == "rebase.accepted" {
						accepted = row
					}
				}
				hash, err := gitx.ContentHash(f.tree, "asset.bin")
				if err != nil {
					t.Fatal(err)
				}
				if accepted["by"] != "autopilot" || accepted["grant_id"] != "g1014" || accepted["source"] != source || accepted["source_hash"] != hash || accepted["content_hash"] != hash {
					t.Fatalf("accepted=%v hash=%s", accepted, hash)
				}
				verified, err := sign.VerifyRow(accepted, bytes.Repeat([]byte{7}, 32))
				if err != nil || !verified.OK {
					t.Fatalf("invalid signature: %v %v", verified, err)
				}
			})
		}
	}
}

func TestB1014RebaseIdenticalRefusals(t *testing.T) {
	cases := []struct {
		name, source, want string
		edit               func(*testing.T, *rebaseCmdFixture)
	}{
		{"byte-different", "approved.bin", "rebase-identical-mismatch", func(t *testing.T, f *rebaseCmdFixture) { f.write(f.repo, "approved.bin", "\x00approveD") }},
		{"no-scope", "approved.bin", "rebase-identical-scope", func(t *testing.T, f *rebaseCmdFixture) {
			b1014Configure(t, f, []any{"approved.bin"}, []any{"close:routine"}, nil)
		}},
		{"denied", "approved.bin", "rebase-identical-scope", func(t *testing.T, f *rebaseCmdFixture) {
			b1014Configure(t, f, []any{"approved.bin"}, []any{"rebase:accept-identical"}, []any{"rebase:accept-identical"})
		}},
		{"off", "main:asset.bin", "rebase-identical-off", func(t *testing.T, f *rebaseCmdFixture) {
			b1014Configure(t, f, []any{}, []any{"rebase:accept-identical"}, nil)
		}},
		{"unapproved", "asset.bin", "rebase-identical-source", nil},
		{"foreign-ref", "HEAD:asset.bin", "rebase-identical-source", nil},
		{"traversal", "../approved.bin", "rebase-identical-source", nil},
		{"symlink", "approved.bin", "symlink", func(t *testing.T, f *rebaseCmdFixture) {
			if err := os.Remove(filepath.Join(f.repo, "approved.bin")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("asset.bin", filepath.Join(f.repo, "approved.bin")); err != nil {
				t.Fatal(err)
			}
		}},
		{"no-grant", "approved.bin", "rebase-identical-scope", func(t *testing.T, f *rebaseCmdFixture) {
			if err := state.UpdateRun(f.home, "r-rebase-unit", func(r *ledger.Object) error { r.Delete("autopilot"); return nil }); err != nil {
				t.Fatal(err)
			}
		}},
		{"unsigned-grant", "approved.bin", "rebase-identical-grant", func(t *testing.T, f *rebaseCmdFixture) {
			if err := state.UpdateRun(f.home, "r-rebase-unit", func(r *ledger.Object) error {
				g := blockObject(r.Get("autopilot"))
				g.Set("grant_id", "unsigned")
				r.Set("autopilot", g)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}},
		{"no-snapshot", "approved.bin", "rebase-identical-off", func(t *testing.T, f *rebaseCmdFixture) {
			if err := state.UpdateRun(f.home, "r-rebase-unit", func(r *ledger.Object) error { r.Delete("config"); return nil }); err != nil {
				t.Fatal(err)
			}
			f.write(f.repo, ".code-forge.yml", "rebase:\n  approved_binary_sources: [approved.bin]\n")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := b1014Fixture(t, "closed")
			if c.edit != nil {
				c.edit(t, f)
			}
			before, err := state.ReadRun(f.home, "r-rebase-unit")
			if err != nil {
				t.Fatal(err)
			}
			rows := f.rows()
			head := f.git(f.tree, "rev-parse", "HEAD")
			code, out, errText := f.rebase("--accept-identical", "asset.bin", "--source", c.source)
			if code != 1 || out != "" || !strings.Contains(errText, c.want) {
				t.Fatalf("exit=%d out=%q err=%q want %s", code, out, errText, c.want)
			}
			after, _ := state.ReadRun(f.home, "r-rebase-unit")
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(rows, f.rows()) || head != f.git(f.tree, "rev-parse", "HEAD") {
				t.Fatal("refusal changed record, ledger or HEAD")
			}
		})
	}
}

func TestB1014TextStillNeedsReview(t *testing.T) {
	f, _ := closedFixture(t, "closed")
	b1014Configure(t, f, []any{"a.go"}, []any{"rebase:accept-identical"}, nil)
	rebaseTestTTY(t, false, humanask.Cancelled)
	code, _, errText := f.rebase("--accept-identical", "a.go", "--source", "main:a.go")
	if code != 1 || !strings.Contains(errText, "rebase-identical-text") {
		t.Fatalf("exit=%d err=%q", code, errText)
	}
}

func TestB1014GrantRequiresSnapshotOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			f := b1014Fixture(t, "open")
			if err := state.UpdateRun(f.home, "r-rebase-unit", func(r *ledger.Object) error {
				r.Delete("autopilot")
				if !enabled {
					r.Delete("config")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC().Truncate(time.Millisecond)
			oldNow := autopilotNow
			autopilotNow = func() time.Time { return now }
			t.Cleanup(func() { autopilotNow = oldNow })
			before := f.rows()
			code, out, errText := autopilotCall(t, "start", "--run", "r-rebase-unit", "--until", now.Add(time.Hour).Format(time.RFC3339), "--delegate", "L2", "--allow", "rebase:accept-identical", "--yes")
			if enabled {
				if code != 0 || !strings.Contains(out, "rebase:accept-identical") {
					t.Fatalf("exit=%d out=%q err=%q", code, out, errText)
				}
			} else if code != 1 || !strings.Contains(errText, "rebase-identical-off") || !reflect.DeepEqual(before, f.rows()) {
				t.Fatalf("exit=%d out=%q err=%q", code, out, errText)
			}
		})
	}
}

func TestB1014RebaseIdenticalUsage(t *testing.T) {
	f := b1014Fixture(t, "closed")
	for _, args := range [][]string{{"--source", "approved.bin"}, {"--accept-identical", "asset.bin"}, {"--accept-identical", "asset.bin", "--source", "approved.bin", "--head", "main"}, {"--accept-identical", "asset.bin", "--source", "approved.bin", "--accept-landed"}} {
		code, out, errText := f.rebase(args...)
		if code != 2 || out != "" || !strings.Contains(errText, "--accept-identical") {
			t.Fatalf("args=%v exit=%d out=%q err=%q", args, code, out, errText)
		}
	}
}
