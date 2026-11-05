// Package blockops implements the exact-path block mutations shared with Node.
package blockops

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/state"
)

// ErrHandover is returned before mutation when a fresh record requires Node.
var ErrHandover = errors.New("block requires Node")

// PublicationError means the callback completed and the helper entered its
// sign/save phase. Re-executing this command could publish a second signed row.
type PublicationError struct{ Err error }

func (e *PublicationError) Error() string { return e.Err.Error() }
func (e *PublicationError) Unwrap() error { return e.Err }

func object(v any) ledger.Object { o, _ := v.(ledger.Object); return o }
func text(v any) string          { s, _ := v.(string); return s }
func quote(v string) string      { b, _ := ledger.Stringify(v, false); return string(b) }

// QuietExpiry proves the B48 no-op: no grant and no active approval means
// checkExpiry writes nothing and approvalCheckText receives three empty lists.
// Malformed approvals are delegated because Node may throw and print a warning.
func QuietExpiry(record ledger.Object) bool {
	if record.Get("autopilot") != nil {
		return false
	}
	if values, ok := record.Get("autopilot_approvals").([]any); ok {
		for _, value := range values {
			a, ok := value.(ledger.Object)
			if !ok || a.Get("status") == "active" {
				return false
			}
		}
	}
	return true
}

// ValidateClaim preserves assertOwned's diagnostic precedence before the exact
// entry refusal. Braces expand only for validation; a valid glob is still refused.
func ValidateClaim(file string) error {
	refuse := func(why string) error {
		return &state.Error{Code: "bad-owned", Message: "owned files: invalid path " + quote(file) + ": " + why}
	}
	if file == "" {
		return refuse("must be a non-empty string")
	}
	if strings.Contains(file, `\`) {
		return refuse("backslashes are not allowed (POSIX paths only)")
	}
	glob := !owned.IsExactEntry(file)
	if glob && strings.ContainsAny(file, "[]()!+@") {
		return refuse("a glob may not contain [ ] ( ) ! + @ — character classes and extglobs are not supported; name the files exactly or use only *, ?, ** and {a,b}")
	}
	for _, alt := range alternatives(file) {
		if strings.HasPrefix(alt, "/") {
			return refuse("must be repo-relative, not absolute")
		}
		if glob && strings.ContainsAny(alt, "{}") {
			return refuse("unbalanced braces")
		}
		for _, part := range strings.Split(alt, "/") {
			if part == "" || part == "." || part == ".." {
				return refuse(`no ".", ".." or empty segments`)
			}
		}
	}
	if glob {
		return &state.Error{Code: "bad-owned", Message: "block claim takes one exact path, not a glob (" + quote(file) + ")"}
	}
	return nil
}

func alternatives(pattern string) []string {
	open := strings.IndexByte(pattern, '{')
	if open < 0 {
		return []string{pattern}
	}
	depth, close := 0, -1
	bounds := []int{open}
	for i := open; i < len(pattern); i++ {
		switch pattern[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				close = i
			}
		case ',':
			if depth == 1 {
				bounds = append(bounds, i)
			}
		}
		if close >= 0 {
			break
		}
	}
	if close < 0 {
		return []string{pattern}
	}
	bounds = append(bounds, close)
	out := []string{}
	for i := 0; i < len(bounds)-1; i++ {
		out = append(out, alternatives(pattern[:open]+pattern[bounds[i]+1:bounds[i+1]]+pattern[close+1:])...)
	}
	return out
}

// below is registry.globBelow for one brace-free pattern. Segment matching uses
// the kit's UTF-16 glob matcher, also used by OwnsFile.
func below(dir, pattern string) bool {
	f, p := strings.Split(dir, "/"), strings.Split(pattern, "/")
	for i := 0; ; i++ {
		if i < len(p) && p[i] == "**" {
			return true
		}
		if i == len(f) {
			return i < len(p)
		}
		if i == len(p) || !owned.OwnsFile([]any{p[i]}, nil, f[i]) {
			return false
		}
	}
}

func overlaps(file string, kinds ledger.Object, block, other ledger.Object) (string, bool) {
	a, b := object(block.Get("worktree")), object(other.Get("worktree"))
	if a != nil && b != nil && a.Get("path") != b.Get("path") {
		return "", false
	}
	entries, _ := other.Get("owned_files").([]any)
	otherKinds := object(other.Get("owned_kinds"))
	for _, value := range entries {
		entry := text(value)
		if owned.IsExactEntry(entry) {
			if owned.OwnsFile([]any{file}, kinds, strings.TrimRight(entry, "/")) || owned.OwnsFile([]any{entry}, otherKinds, file) {
				return entry, true
			}
		} else {
			for _, pattern := range alternatives(entry) {
				literal := ledger.Object{{Key: pattern, Value: "file"}}
				if owned.OwnsFile([]any{pattern}, literal, file) || kinds.Get(file) != "file" && below(file, pattern) {
					return entry, true
				}
			}
		}
	}
	return "", false
}

// Guarded computes the spellings before the mutation lock, just as claimPath.
func Guarded(record ledger.Object, why string) ([]string, error) {
	if why == "" {
		return nil, nil
	}
	workspace := text(record.Get("workspace"))
	top, err := gitx.RepoTop(workspace)
	if err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}
	prefix, err := filepath.Rel(top, real)
	if err != nil {
		return nil, err
	}
	paths := []string{".code-forge.yml"}
	if prefix != "." {
		paths = append(paths, filepath.ToSlash(prefix)+"/.code-forge.yml")
	}
	return paths, nil
}

// Claim signs even an already-owned claim and removes all covered orphans.
func Claim(home, run, id, file string, guarded []string, why string, safe func(ledger.Object) bool) error {
	prepared := false
	err := state.UpdateBlock(home, run, id, func(record, block *ledger.Object) ([]ledger.Object, error) {
		if !safe(*record) {
			return nil, ErrHandover
		}
		kinds := ledger.Object{{Key: file, Value: owned.PathKind(text(record.Get("workspace")), file)}}
		for _, path := range guarded {
			if owned.OwnsFile([]any{file}, kinds, path) {
				return nil, &state.Error{Code: "config-owned", Message: "owned entry " + quote(file) + " covers the project's .code-forge.yml; no block may own it while " + why}
			}
		}
		blocks := object(record.Get("blocks"))
		// Stringify enumerates integer-index keys in JS order as well.
		encoded, _ := ledger.Stringify(blocks, false)
		ordered, _ := ledger.DecodeObject(encoded)
		for _, field := range ordered {
			other := object(field.Value)
			if field.Key != id && other.Get("status") == "open" {
				if entry, ok := overlaps(file, kinds, *block, other); ok {
					return nil, &state.Error{Code: "overlap", Message: fmt.Sprintf("%s overlaps %s owned by open block %s", file, entry, field.Key)}
				}
			}
		}
		files, ok := block.Get("owned_files").([]any)
		orphans, orphanOK := record.Get("orphans").([]any)
		if !ok || !orphanOK {
			return nil, ErrHandover
		}
		has := false
		for _, value := range files {
			if value == file {
				has = true
			}
		}
		if !has {
			block.Set("owned_files", append(files, file))
			stored := object(block.Get("owned_kinds")).Clone()
			stored.Set(file, kinds.Get(file))
			block.Set("owned_kinds", stored)
		}
		remaining := []any{}
		for _, orphan := range orphans {
			if !owned.OwnsFile([]any{file}, kinds, text(orphan)) {
				remaining = append(remaining, orphan)
			}
		}
		record.Set("orphans", remaining)
		prepared = true
		return []ledger.Object{{{Key: "event", Value: "block.claim"}, {Key: "block", Value: id}, {Key: "path", Value: file}}}, nil
	})
	if err != nil && prepared {
		return &PublicationError{err}
	}
	return err
}

func Stop(home, run, id, reason string, safe func(ledger.Object) bool) error {
	prepared := false
	err := state.UpdateBlock(home, run, "", func(record, _ *ledger.Object) ([]ledger.Object, error) {
		if !safe(*record) {
			return nil, ErrHandover
		}
		blocks := object(record.Get("blocks"))
		block := object(blocks.Get(id))
		if block.Get("status") != "open" && block.Get("status") != "awaiting_human_check" {
			return nil, &state.Error{Code: "no-block", Message: fmt.Sprintf("block %s is not open in run %s", id, run)}
		}
		block.Set("status", "stopped")
		block.Set("stop_reason", reason)
		blocks.Set(id, block)
		record.Set("blocks", blocks)
		prepared = true
		return []ledger.Object{{{Key: "event", Value: "block.stop"}, {Key: "block", Value: id}, {Key: "reason", Value: reason}}}, nil
	})
	if err != nil && prepared {
		return &PublicationError{err}
	}
	return err
}
