package blockopen

import (
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/blockops"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/sign"
	"github.com/ricardov03/code-forge/native/state"
	"github.com/ricardov03/code-forge/native/worktree"
)

type Options struct {
	Home, Run, ID, Level, Kind, Base, BriefPath, Pointer, Floor, GuardWhy string
	Owned, Tests, Guarded                                                 []string
	Acceptance                                                            any
	BriefData                                                             []byte
	Attempt                                                               int64
	Lines                                                                 any
	BaseExplicit                                                          bool
	Settings                                                              *worktree.Settings
	Safe                                                                  func(ledger.Object) bool
	After                                                                 []string
	AfterOutcome                                                          []any
	Decision                                                              ledger.Object
}
type Result struct {
	Block   ledger.Object
	Pointer string
	Reused  bool
	Kept    int
}

func Resolve(cwd, ref string) (string, error) {
	if ref == "" || strings.HasPrefix(ref, "-") {
		return "", refusal("bad-ref", `a git ref must be non-empty and not start with "-" (got `+quote(ref)+")")
	}
	out, err := gitx.Run(cwd, "rev-parse", ref+"^{commit}")
	if err != nil {
		code := -1
		if e, ok := err.(*gitx.CommandError); ok {
			code = e.Code
		}
		return "", refusal("git-failed", fmt.Sprintf("git rev-parse failed (exit %d)", code))
	}
	return strings.TrimSpace(out), nil
}
func previous(record ledger.Object, o Options) ledger.Object {
	if o.Settings == nil {
		return nil
	}
	return object(object(object(record.Get("blocks")).Get(o.ID)).Get("worktree"))
}
func claims(home, run, project, id string) ([]string, error) {
	keyPath := filepath.Join(home, ".code-forge/runs", run+".key")
	st, err := os.Stat(keyPath)
	if os.IsNotExist(err) {
		return nil, refusal("no-key", "run "+run+" has no signer key")
	}
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0077 != 0 {
		return nil, refusal("bad-key-mode", fmt.Sprintf("run %s signer key is accessible to other users (mode %o); expected 600", run, st.Mode().Perm()))
	}
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, refusal("bad-key", "run "+run+" signer key is not 32 bytes")
	}
	rows, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge/ledger"), project)
	if err != nil {
		return nil, err
	}
	out := []string{}
	seen := map[string]bool{}
	for _, v := range rows {
		r, ok := v.(map[string]any)
		if !ok || r["event"] != "block.claim" || r["run"] != run || r["block"] != id {
			continue
		}
		verified, err := sign.VerifyRow(r, key)
		if err != nil || !verified.OK {
			continue
		}
		file, ok := r["path"].(string)
		if !ok {
			return nil, blockops.ErrHandover
		}
		if err := AssertOwned([]string{file}, "owned files", "bad-owned"); err != nil {
			return nil, err
		}
		if !seen[file] {
			seen[file] = true
			out = append(out, file)
		}
	}
	return out, nil
}

func Open(o Options) (result Result, err error) {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$`).MatchString(o.ID) {
		return result, refusal("bad-block-id", "block id must match /^[A-Za-z0-9][A-Za-z0-9._-]{0,31}$/")
	}
	if !regexp.MustCompile(`^L[0-3]$`).MatchString(o.Level) {
		return result, refusal("bad-level", "level must be L0..L3")
	}
	if o.Attempt < 1 {
		return result, refusal("bad-attempt", "attempt must be an integer >= 1")
	}
	if o.Kind != "" && o.Kind != "light" && o.Kind != "code" && o.Kind != "docs" && o.Kind != "contract" {
		return result, refusal("bad-kind", "kind must be one of code, docs, contract, light")
	}
	if err := AssertOwned(o.Owned, "owned files", "bad-owned"); err != nil {
		return result, err
	}
	acceptance, err := Acceptance(o.Acceptance)
	if err != nil {
		return result, err
	}
	if o.Tests != nil {
		if err := AssertOwned(o.Tests, "test files", "bad-tests"); err != nil {
			return result, err
		}
	}
	kind := Kind(o.Owned, o.Kind)
	level := o.Level
	if (kind == "docs" || kind == "contract") && o.Floor > level {
		level = o.Floor
	}
	if o.BriefPath != "" {
		o.Pointer, err = BriefPointer(o.BriefPath, o.BriefData)
		if err != nil {
			return result, err
		}
	}
	before, err := state.ReadRun(o.Home, o.Run)
	if err != nil {
		return result, err
	}
	workspace := text(before.Get("workspace"))
	prior := previous(before, o)
	ref := o.Base
	if !o.BaseExplicit {
		ref = "HEAD"
	}
	if prior != nil {
		ref = text(prior.Get("base_sha"))
	}
	stamped, err := Resolve(workspace, ref)
	if err != nil {
		return result, err
	}
	if prior != nil && o.BaseExplicit {
		explicit, err := Resolve(workspace, o.Base)
		if err != nil {
			return result, err
		}
		if explicit != prior.Get("base_sha") {
			return result, refusal("worktree-base", fmt.Sprintf("block %s: --base %s is not %s, the base of the block's worktree %s; a re-open keeps the worktree and its base — drop --base; nothing was recorded", o.ID, o.Base, stamped[:12], text(prior.Get("path"))))
		}
	}
	treeOpen, err := gitx.TreeAtOpen(workspace, stamped)
	if err != nil {
		return result, err
	}
	o.Guarded, err = blockops.Guarded(before, o.GuardWhy)
	if err != nil {
		return result, err
	}
	var made *worktree.Made
	prepared := false
	err = state.UpdateBlock(o.Home, o.Run, "", func(record, _ *ledger.Object) ([]ledger.Object, error) {
		if !o.Safe(*record) {
			return nil, blockops.ErrHandover
		}
		blocks := object(record.Get("blocks"))
		status := object(blocks.Get(o.ID)).Get("status")
		if status == "open" {
			return nil, refusal("already-open", "block "+o.ID+" is already open")
		}
		if status == "awaiting_human_check" {
			return nil, refusal("already-open", "block "+o.ID+" is awaiting a human check")
		}
		if err := CheckAfter(*record, o.ID, o.After); err != nil {
			return nil, err
		}
		kept := []string{}
		if o.Attempt > 1 {
			var err error
			kept, err = claims(o.Home, o.Run, text(record.Get("project")), o.ID)
			if err != nil {
				return nil, err
			}
		}
		files := []any{}
		seen := map[string]bool{}
		for _, file := range append(append([]string{}, o.Owned...), kept...) {
			if !seen[file] {
				seen[file] = true
				files = append(files, file)
			}
		}
		kinds := owned.Kinds(text(record.Get("workspace")), files)
		for _, file := range files {
			for _, path := range o.Guarded {
				if owned.OwnsFile([]any{file}, kinds, path) {
					return nil, refusal("config-owned", "owned entry "+quote(text(file))+" covers the project's .code-forge.yml; no block may own it while "+o.GuardWhy)
				}
			}
		}
		prior := previous(*record, o)
		ref := o.Base
		if !o.BaseExplicit {
			ref = "HEAD"
		}
		if prior != nil {
			ref = text(prior.Get("base_sha"))
		}
		base, err := Resolve(text(record.Get("workspace")), ref)
		if err != nil {
			return nil, err
		}
		if base != stamped || record.Get("workspace") != workspace {
			return nil, refusal("base-moved", "the base moved while block "+o.ID+" was being opened; run block open again")
		}
		branch, err := gitx.Run(workspace, "symbolic-ref", "--quiet", "--short", "HEAD")
		var baseBranch any
		if err == nil {
			baseBranch = strings.TrimSpace(branch)
		} else if e, ok := err.(*gitx.CommandError); !ok || e.Code != 1 {
			if !ok {
				return nil, blockops.ErrHandover
			}
			return nil, refusal("git-failed", fmt.Sprintf("git symbolic-ref failed (exit %d)", e.Code))
		}
		var planned *worktree.Plan
		tree := ""
		if o.Settings != nil {
			planned, err = worktree.Prepare(workspace, *o.Settings, o.Run, o.ID, base, o.BaseExplicit, prior)
			if err != nil {
				return nil, err
			}
			tree = planned.Path
		}
		// Re-enumerate JS integer keys before checking the first overlapping block.
		encoded, _ := ledger.Stringify(blocks, false)
		ordered, _ := ledger.DecodeObject(encoded)
		for _, f := range ordered {
			other := object(f.Value)
			if other.Get("status") != "open" {
				continue
			}
			if a, b := overlap(files, kinds, tree, other); a != "" {
				return nil, refusal("overlap", fmt.Sprintf("block %s owns %s, which overlaps %s owned by open block %s", o.ID, a, b, f.Key))
			}
		}
		if planned != nil {
			made, err = planned.Make()
			if err != nil {
				return nil, err
			}
		}
		entry := ledger.Object{{Key: "block", Value: o.ID}, {Key: "base_sha", Value: base}, {Key: "base_branch", Value: baseBranch}, {Key: "owned_files", Value: files}, {Key: "owned_kinds", Value: kinds}, {Key: "level", Value: level}, {Key: "kind", Value: kind}, {Key: "attempt", Value: o.Attempt}, {Key: "opened_at", Value: now}, {Key: "acceptance", Value: acceptance}, {Key: "lines_forecast", Value: o.Lines}, {Key: "status", Value: "open"}, {Key: "tree_at_open", Value: treeOpen}}
		if o.Tests != nil {
			tests := []any{}
			for _, file := range o.Tests {
				tests = append(tests, file)
			}
			entry.Set("test_files", tests)
		}
		if o.After != nil {
			after := []any{}
			for _, dependency := range unique(o.After) {
				after = append(after, dependency)
			}
			entry.Set("after", after)
		}
		if o.AfterOutcome != nil {
			entry.Set("after_outcome", o.AfterOutcome)
		}
		if made != nil {
			entry.Set("worktree", made.Record)
		}
		human := []any{}
		for _, c := range acceptance {
			if object(c).Get("kind") == "human_check" {
				human = append(human, object(c).Get("clause"))
			}
		}
		if len(human) > 0 {
			entry.Set("human_checks", human)
		}
		if o.BriefPath != "" {
			entry.Set("brief", ledger.Object{{Key: "path", Value: o.BriefPath}})
		}
		blocks.Set(o.ID, entry)
		record.Set("blocks", blocks)
		rows := []ledger.Object{}
		if o.Decision != nil {
			uuid := make([]byte, 16)
			if _, err := rand.Read(uuid); err != nil {
				return nil, err
			}
			uuid[6], uuid[8] = uuid[6]&0x0f|0x40, uuid[8]&0x3f|0x80
			decision := append(ledger.Object{}, o.Decision...)
			decision.Set("event", "decision")
			decision.Set("decision_id", fmt.Sprintf("%x-%x-%x-%x-%x", uuid[:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:]))
			decision.Set("block", o.ID)
			rows = append(rows, decision)
		}
		if made != nil && made.Created {
			rows = append(rows, ledger.Object{{Key: "event", Value: "worktree.created"}, {Key: "block", Value: o.ID}, {Key: "path", Value: made.Record.Get("path")}, {Key: "branch", Value: made.Record.Get("branch")}, {Key: "base", Value: made.Record.Get("base_sha")}})
		}
		dispatch := ledger.Object{{Key: "event", Value: "dispatch"}, {Key: "block", Value: o.ID}, {Key: "level", Value: level}, {Key: "kind", Value: kind}}
		if level != o.Level {
			dispatch.Set("requested_level", o.Level)
			dispatch.Set("trigger", "docs_floor")
		}
		for _, f := range (ledger.Object{{Key: "attempt", Value: o.Attempt}, {Key: "base_sha", Value: base}, {Key: "base_branch", Value: baseBranch}, {Key: "owned_files", Value: files}, {Key: "lines_forecast", Value: o.Lines}}) {
			dispatch.Set(f.Key, f.Value)
		}
		if o.Tests != nil {
			dispatch.Set("test_files", entry.Get("test_files"))
		}
		if o.After != nil {
			dispatch.Set("after", entry.Get("after"))
		}
		if o.AfterOutcome != nil {
			dispatch.Set("after_outcome", o.AfterOutcome)
		}
		rows = append(rows, dispatch)
		result = Result{Block: entry, Pointer: o.Pointer, Reused: made != nil && !made.Created, Kept: len(kept)}
		prepared = true
		return rows, nil
	})
	if err != nil {
		if made != nil && made.Created && made.Rollback != nil {
			if left := made.Rollback(); left != "" {
				err = fmt.Errorf("%s. %s", strings.TrimSuffix(err.Error(), "."), left)
			}
		}
		if prepared {
			err = &blockops.PublicationError{Err: err}
		}
	}
	return result, err
}
