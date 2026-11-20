package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ricardov03/code-forge/native/autopilot"
	"github.com/ricardov03/code-forge/native/gatecheck"
	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/state"
)

type rebaseIdenticalRequest struct{ file, source string }

func rebaseIdenticalError(code, message string) error {
	code = "rebase-identical-" + code
	return &state.Error{Code: code, Message: code + ": " + message}
}

// No workspace fallback: old runs and missing/corrupt snapshots stay opted out.
func rebaseIdenticalSources(record ledger.Object) ([]string, error) {
	cfg, snapshot, valid := sweepConfigInForce(record)
	if !snapshot || !valid {
		return nil, rebaseIdenticalError("off", "set rebase.approved_binary_sources before starting a new run")
	}
	values, _ := blockObject(cfg.Get("rebase")).Get("approved_binary_sources").([]any)
	sources := []string{}
	for _, value := range values {
		source, ok := value.(string)
		if !ok || blockstateSourcePath(source) != nil {
			return nil, rebaseIdenticalError("off", "invalid rebase.approved_binary_sources in snapshot")
		}
		sources = append(sources, source)
	}
	if len(sources) == 0 {
		return nil, rebaseIdenticalError("off", "set rebase.approved_binary_sources before starting a new run")
	}
	return sources, nil
}

// Source patterns and source paths use only repository-relative POSIX paths.
func blockstateSourcePath(file string) error {
	if file == "" || strings.ContainsAny(file, "\x00\\:\r\n") || strings.HasPrefix(file, "/") {
		return rebaseIdenticalError("source", "source must be a repository-relative path")
	}
	for i, segment := range strings.Split(file, "/") {
		if segment == "" || segment == "." || segment == ".." || i == 0 && (strings.EqualFold(segment, ".git") || strings.EqualFold(segment, ".code-forge")) {
			return rebaseIdenticalError("source", "source must stay inside the repository outside git and run state")
		}
	}
	return nil
}

// Check the live record and signed grant both before replay and under the run
// lock. A hand-edited record cannot add a scope the owner never granted.
func (r *rebaseIdenticalRequest) authorize(home, run string, record ledger.Object) ([]string, error) {
	if record.Get("status") != "active" {
		return nil, rebaseIdenticalError("run", "run is not active")
	}
	sources, err := rebaseIdenticalSources(record)
	if err != nil {
		return nil, err
	}
	grant, err := autopilot.ReadGrantNode(record, rebaseNow())
	if err != nil {
		return nil, rebaseIdenticalError("scope", "grant is invalid")
	}
	if refusal := autopilot.ScopeRefusal(grant, autopilot.AcceptIdenticalScope); refusal != "" {
		return nil, rebaseIdenticalError("scope", refusal)
	}
	key, err := rebaseKey(home, run)
	if err != nil {
		return nil, err
	}
	all, err := ledger.ReadAllRows(filepath.Join(home, ".code-forge", "ledger"), blockString(record.Get("project")))
	if err != nil {
		return nil, err
	}
	rows := []gatecheck.Row{}
	for _, v := range all {
		if row, ok := v.(map[string]any); ok {
			rows = append(rows, row)
		}
	}
	row := autopilotGrantRow(rows, run, blockString(grant.Grant.Get("grant_id")), key)
	if row == nil {
		return nil, rebaseIdenticalError("grant", "active grant has no signed autopilot.grant row")
	}
	allow, _ := row["scopes"].([]any)
	deny, _ := row["deny"].([]any)
	if !slices.Contains(allow, any(autopilot.AcceptIdenticalScope)) || slices.Contains(deny, any(autopilot.AcceptIdenticalScope)) {
		return nil, rebaseIdenticalError("grant", "signed grant does not allow the scope")
	}
	return sources, nil
}

// Text (including SVG) keeps the existing review path. PDF and font signatures
// are binary even when their particular file happens to contain ASCII only.
func rebaseIdenticalBinary(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return true
	}
	mime := http.DetectContentType(data)
	return mime == "application/pdf" || strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "font/") || strings.HasPrefix(mime, "application/font")
}

func (r *rebaseIdenticalRequest) check(home, run, id string, record, block ledger.Object, tree, base string) (ledger.Object, error) {
	sources, err := r.authorize(home, run, record)
	if err != nil {
		return nil, err
	}
	target, err := gitx.ReadRegularFileNoFollow(tree, r.file)
	if err != nil {
		return nil, err
	}
	if !rebaseIdenticalBinary(target) {
		return nil, rebaseIdenticalError("text", "text and missing files need re-review")
	}
	var source []byte
	if ref, file, qualified := strings.Cut(r.source, ":"); qualified {
		branch := blockString(block.Get("base_branch"))
		if ref != branch && ref != "refs/heads/"+branch && ref != base {
			return nil, rebaseIdenticalError("source", "source ref must be the recorded base branch or its resolved commit")
		}
		if err := blockstateSourcePath(file); err != nil {
			return nil, err
		}
		blobs, err := rebaseBlobs(tree, base, []string{file})
		if err != nil {
			return nil, err
		}
		if blobs[file] == "" {
			return nil, rebaseIdenticalError("source", "source is missing from the base branch")
		}
		entry, err := rebaseNodeGit(tree, nil, "ls-tree", base, "--", file)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(entry, "100644 blob ") && !strings.HasPrefix(entry, "100755 blob ") {
			return nil, rebaseIdenticalError("source", "source is not a regular blob")
		}
		data, err := rebaseNodeGit(tree, nil, "cat-file", "blob", blobs[file])
		if err != nil {
			return nil, err
		}
		source = []byte(data)
	} else {
		if err := blockstateSourcePath(r.source); err != nil {
			return nil, err
		}
		allowed := false
		for _, pattern := range sources {
			allowed = allowed || owned.GlobMatch(r.source, pattern)
		}
		if !allowed {
			return nil, rebaseIdenticalError("source", "source is not in rebase.approved_binary_sources")
		}
		source, err = gitx.ReadRegularFileNoFollow(blockString(record.Get("workspace")), r.source)
		if err != nil {
			return nil, err
		}
	}
	// Hash the exact checked bytes, with ContentHash's SHA-256 dialect. Recheck
	// the target pathname before recording so a replacement cannot inherit clearance.
	contentHash := fmt.Sprintf("%x", sha256.Sum256(target))
	sourceHash := fmt.Sprintf("%x", sha256.Sum256(source))
	if source == nil || sourceHash != contentHash {
		return nil, rebaseIdenticalError("mismatch", "binary differs from the approved source")
	}
	current, err := gitx.ContentHash(tree, r.file)
	if err != nil {
		return nil, err
	}
	if current != contentHash {
		return nil, rebaseIdenticalError("mismatch", "binary changed during verification")
	}
	grant := blockObject(record.Get("autopilot"))
	return ledger.Object{{Key: "event", Value: "rebase.accepted"}, {Key: "block", Value: id}, {Key: "file", Value: r.file}, {Key: "content_hash", Value: contentHash}, {Key: "source", Value: r.source}, {Key: "source_hash", Value: sourceHash}, {Key: "by", Value: "autopilot"}, {Key: "grant_id", Value: grant.Get("grant_id")}}, nil
}
