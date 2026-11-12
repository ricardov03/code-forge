package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
)

// b496Stamp is the worker's writeRow (loop.mjs): signRow({run, ...row,
// block, file, content_hash}) at the frozen clock.
func b496Stamp(t *testing.T, row ledger.Object, block, file, hash string) ledger.Object {
	t.Helper()
	out := ledger.Object{{Key: "run", Value: "r1"}}
	for _, f := range row {
		out.Set(f.Key, f.Value)
	}
	out.Set("block", block)
	out.Set("file", file)
	out.Set("content_hash", hash)
	signed, err := signAt(out, b496Key, b496Clock())
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func b496Hash(t *testing.T, repo, file string) string {
	data, err := os.ReadFile(filepath.Join(repo, file))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func b496Base(step ledger.Object, base string) *string {
	switch b := step.Get("base").(type) {
	case string:
		if b == "base" {
			return &base
		}
		return &b
	}
	return nil
}

// Every golden ticket that found no review.budget row of its block: Go's
// block budget rows (the changed owned files through computeFileSet, the
// ticket's file always in, each tiered by its path floor) are Node's — none
// when git fails on the base, the ticket's file alone with no base.
func TestBlockBudgetMatchesNode(t *testing.T) {
	checked := 0
	for _, sc := range b496Scenarios(t) {
		name := sc.Get("name").(string)
		cfg, _ := plainMap(sc.Get("cfg").(ledger.Object))
		t.Run(name, func(t *testing.T) {
			repo := t.TempDir()
			base := b496Repo(t, repo)
			seen := false
			for _, s := range sc.Get("steps").([]any) {
				st := s.(ledger.Object)
				step := st.Get("step").(ledger.Object)
				rows := objects(st.Get("rows"))
				if step.Get("op") == "ticket" && !seen {
					file := step.Get("file").(string)
					want := []ledger.Object{}
					for _, r := range rows {
						if ev, _ := r.Get("event").(string); strings.HasPrefix(ev, "review.budget") || ev == "review.over_budget" {
							want = append(want, r)
						}
					}
					got, err := BlockBudgetRows(BudgetRequest{ContentRoot: repo, Base: b496Base(step, base), Owned: step.Get("owned").([]any), Cfg: cfg, Block: "B1", File: file})
					if err != nil {
						t.Fatal(err)
					}
					hash := b496Hash(t, repo, file)
					if step.Get("hash") == "moved" {
						hash = strings.Repeat("f", 64)
					}
					stamped := []ledger.Object{}
					for _, r := range got {
						stamped = append(stamped, b496Stamp(t, r, "B1", file, hash))
					}
					sameJSON(t, "budget rows", stamped, want)
					checked++
				}
				for _, r := range rows {
					if r.Get("event") == "review.budget" {
						seen = true
					}
				}
			}
		})
	}
	if checked < 20 {
		t.Fatalf("only %d tickets checked", checked)
	}
}

// computeFileSet's argv guard (a base starting with `-` throws) and a base
// git cannot read are Node's "no row": the next keyed ticket retries.
func TestBlockBudgetNoRowCases(t *testing.T) {
	repo := t.TempDir()
	b496Repo(t, repo)
	for _, base := range []string{"-p", "", "notacommit"} {
		b := base
		rows, err := BlockBudgetRows(BudgetRequest{ContentRoot: repo, Base: &b, Owned: []any{"a.go"}, Block: "B1", File: "a.go"})
		if err != nil || rows != nil {
			t.Fatalf("base %q: %v %v", base, rows, err)
		}
	}
	if key := BlockLockKey("r1", "B1"); key != "block:r1\x00B1" {
		t.Fatalf("lock key %q", key)
	}
}
