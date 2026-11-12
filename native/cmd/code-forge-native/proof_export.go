package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/owned"
	"github.com/ricardov03/code-forge/native/proof"
)

// B462: `proof export` builds the measurement export in Go, as
// src/proof/export.mjs buildExport and src/cli/proof.mjs exportAndRecord:
// git archive + tar, export-ignore restoration from a temporary index, the
// copy_untracked files, the block's owned files at current content and the
// link_dirs (a Composer vendor dir is copied and its main-tree links
// retargeted), then the signed `proof` row (step export).

var proofDefaultLinkDirs = []string{"vendor", "node_modules"}
var proofDefaultCopyUntracked = []string{".env", ".env.testing"}

const proofGitTimeout = 120 * time.Second

// errProofHandover marks a case Go does not word as Node does.
var errProofHandover = errors.New("proof: hand over")

// proofGit runs git with every GIT_* variable stripped plus extra, as
// export.mjs git(). A non-zero exit is Node's `git <verb> failed (exit N)`.
func proofGit(cwd string, extra []string, input string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir, cmd.Env = cwd, append(gitx.ChildEnv(), extra...)
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var stdout bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, io.Discard
	timer := time.AfterFunc(proofGitTimeout, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	err := cmd.Run()
	timer.Stop()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			return "", &proofError{"git-failed", fmt.Sprintf("git %s failed (exit %d)", args[0], exit.ExitCode())}
		}
		return "", errProofHandover
	}
	return stdout.String(), nil
}

// proofExportRel is export.mjs assertRelPath.
func proofExportRel(p, key string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return &proofError{"bad-path", key + ": every entry must be a repo-relative POSIX path"}
	}
	for _, s := range strings.Split(p, "/") {
		if s == "" || s == "." || s == ".." {
			return &proofError{"bad-path", key + `: no ".", ".." or empty segments`}
		}
	}
	return nil
}

// proofStrings is a config list of strings; anything else is not ported.
func proofStrings(value any, fallback []string) ([]string, bool) {
	if value == nil {
		return fallback, true
	}
	list, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, len(list))
	for i, item := range list {
		if out[i], ok = item.(string); !ok {
			return nil, false
		}
	}
	return out, true
}

type proofExported struct {
	dir, base                                        string
	restored, untracked, owned, removed, linked, cps []string
}

func proofList(items []string) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out
}

// proofExport serves `proof export <block>` (no --remove) for an open block.
func proofExport(home, runID, blockID string, record ledger.Object) (*proofOutcome, handoverReason) {
	const sub = "export"
	workspace, _ := record.Get("workspace").(string)
	block := proofBlock(record, blockID)
	cfg, refusal, handover := proofConfigAt(workspace)
	if handover != 0 {
		return nil, handover
	}
	if refusal != "" {
		return proofRefuse(sub, refusal, 1), 0
	}
	dir, tree := workspace, ""
	base := block.Get("base_sha")
	wt, err := proof.BlockTree(workspace, block)
	if err != nil {
		var gitErr *gitx.CommandError
		if errors.As(err, &gitErr) {
			return nil, hProofRun
		}
		return proofRefuse(sub, fmt.Sprintf("block %s of run %s: %s; no proof was run", blockID, runID, err.Error()), 1), 0
	}
	if wt != nil {
		dir, tree = wt.Workspace, wt.Path
		base = jsObject(block.Get("worktree"))["base_sha"]
		if object, ok := block.Get("worktree").(ledger.Object); ok {
			base = object.Get("base_sha")
		}
	}
	exportCfg := jsObject(jsObject(cfg["proof"])["export"])
	if tree == "" && jsObject(cfg["proof"])["isolation"] == "lock" {
		return proofRefuse(sub, "proof.isolation is lock — use proof lock instead of an export", 1), 0
	}
	linkDirs, okLink := proofStrings(exportCfg["link_dirs"], proofDefaultLinkDirs)
	copyUntracked, okCopy := proofStrings(exportCfg["copy_untracked"], proofDefaultCopyUntracked)
	if !okLink || !okCopy {
		return nil, hProofRun
	}
	exportRoot := ""
	if tree != "" {
		exportDir, err := proofExportDir(workspace, blockID)
		if err != nil {
			return proofFail(sub, err)
		}
		exportRoot = filepath.Dir(exportDir)
	}
	exp, err := proofBuildExport(dir, blockID, base, block.Get("owned_files"), block.Get("owned_kinds"), linkDirs, copyUntracked, exportRoot)
	if err != nil {
		return proofFail(sub, err)
	}
	fields := ledger.Object{{Key: "step", Value: "export"}, {Key: "base_sha", Value: exp.base}, {Key: "restored", Value: proofList(exp.restored)}, {Key: "untracked", Value: proofList(exp.untracked)}}
	if tree != "" {
		fields = append(fields, ledger.Field{Key: "worktree", Value: tree})
	}
	if err := proofRecord(home, runID, blockID, "export", fields); err != nil {
		return proofFail(sub, err)
	}
	return &proofOutcome{stdout: proofJSON(ledger.Object{
		{Key: "dir", Value: exp.dir}, {Key: "base", Value: exp.base}, {Key: "restored", Value: proofList(exp.restored)},
		{Key: "untracked", Value: proofList(exp.untracked)}, {Key: "owned", Value: proofList(exp.owned)}, {Key: "removed", Value: proofList(exp.removed)},
		{Key: "linked", Value: proofList(exp.linked)}, {Key: "copied", Value: proofList(exp.cps)},
	})}, 0
}

func proofLstat(p string) fs.FileInfo {
	info, err := os.Lstat(p)
	if err != nil {
		return nil
	}
	return info
}

// proofBuildExport is buildExport. Errors are *proofError (Node's refusal
// text) or anything else, which hands the call over.
func proofBuildExport(cwdGiven, blockID string, baseValue, ownedValue, kindsValue any, linkDirs, copyUntracked []string, exportRoot string) (*proofExported, error) {
	if !filepath.IsAbs(cwdGiven) {
		return nil, &proofError{"bad-path", "buildExport: cwd must be an absolute path"}
	}
	cwd, err := filepath.EvalSymlinks(cwdGiven)
	if err != nil {
		return nil, err
	}
	dir, err := proofExportDir(cwd, blockID)
	if err != nil {
		return nil, err
	}
	if exportRoot != "" {
		dir = filepath.Join(exportRoot, blockID)
	}
	ownedList, ok := ownedValue.([]any)
	if !ok {
		return nil, &proofError{"bad-owned", "buildExport: owned must be an array"}
	}
	for _, entry := range ownedList {
		if _, ok := entry.(string); !ok {
			return nil, errProofHandover
		}
	}
	kinds, ok := kindsValue.(ledger.Object)
	if kindsValue == nil {
		kinds, ok = ledger.Object{}, true
	}
	if !ok {
		return nil, errProofHandover
	}
	for _, p := range linkDirs {
		if err := proofExportRel(p, "proof.export.link_dirs"); err != nil {
			return nil, err
		}
	}
	for _, p := range copyUntracked {
		if err := proofExportRel(p, "proof.export.copy_untracked"); err != nil {
			return nil, err
		}
		if p == ".code-forge" || strings.HasPrefix(p, ".code-forge/") {
			return nil, &proofError{"bad-path", "proof.export.copy_untracked: nothing under .code-forge/ is ever exported"}
		}
	}
	baseRef := "undefined"
	if baseValue != nil {
		if baseRef, ok = baseValue.(string); !ok {
			return nil, errProofHandover
		}
	}
	if strings.HasPrefix(baseRef, "-") {
		return nil, errProofHandover
	}
	resolved, err := proofGit(cwd, nil, "", "rev-parse", baseRef+"^{commit}")
	if err != nil {
		var pe *proofError
		if errors.As(err, &pe) {
			return nil, &proofError{"bad-ref", "buildExport: the base is not a commit in this repository"}
		}
		return nil, err
	}
	base := strings.TrimSpace(resolved)

	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o777); err != nil {
		return nil, err
	}
	scratchBase := os.TempDir()
	if root := os.Getenv("CODE_FORGE_TMP_ROOT"); filepath.IsAbs(root) {
		scratchBase = root
	}
	if err := os.MkdirAll(scratchBase, 0o700); err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp(scratchBase, "export-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)

	listing, err := proofGit(cwd, nil, "", "ls-tree", "-r", "-t", "-z", "--full-tree", base)
	if err != nil {
		return nil, err
	}
	type treeEntry struct{ kind, path string }
	entries := []treeEntry{}
	baseBlobs := map[string]bool{}
	for _, line := range strings.Split(listing, "\x00") {
		if line == "" {
			continue
		}
		tab := strings.IndexByte(line, '\t')
		head := strings.Split(line[:tab], " ")
		entry := treeEntry{kind: head[1], path: line[tab+1:]}
		entries = append(entries, entry)
		if entry.kind == "blob" {
			baseBlobs[entry.path] = true
		}
	}

	// (a) + (c)
	untracked := []string{}
	addArgs := []string{}
	for _, rel := range copyUntracked {
		if baseBlobs[rel] {
			continue
		}
		if info := proofLstat(filepath.Join(cwd, rel)); info == nil || !info.Mode().IsRegular() {
			continue
		}
		parent := path.Dir(rel)
		prefix := ""
		if parent != "." {
			prefix = parent + "/"
		}
		addArgs = append(addArgs, "--prefix="+prefix, "--add-file="+filepath.Join(cwd, rel))
		untracked = append(untracked, rel)
	}
	tar := filepath.Join(scratch, "base.tar")
	archive := append(append([]string{"archive", "--format=tar", "-o", tar}, addArgs...), "--prefix=", base)
	if _, err := proofGit(cwd, nil, "", archive...); err != nil {
		return nil, err
	}
	extract := exec.Command("tar", "-xf", tar, "-C", dir)
	extract.Stdout, extract.Stderr = io.Discard, io.Discard
	if err := extract.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() >= 0 {
			return nil, &proofError{"tar-failed", fmt.Sprintf("tar -x failed (exit %d)", exit.ExitCode())}
		}
		return nil, err
	}

	// (b) export-ignore restoration.
	restored := []string{}
	relevant := []treeEntry{}
	for _, e := range entries {
		if e.kind == "tree" || e.kind == "blob" {
			relevant = append(relevant, e)
		}
	}
	if len(relevant) > 0 {
		names := make([]string, len(relevant))
		for i, e := range relevant {
			names[i] = e.path
			if e.kind == "tree" {
				names[i] += "/"
			}
		}
		out, err := proofGit(cwd, nil, strings.Join(names, "\x00")+"\x00", "check-attr", "--source="+base, "-z", "--stdin", "export-ignore")
		if err != nil {
			return nil, err
		}
		fields := strings.Split(out, "\x00")
		ignored := map[string]bool{}
		for i := 0; i+2 < len(fields); i += 3 {
			if fields[i+2] == "set" {
				ignored[strings.TrimSuffix(fields[i], "/")] = true
			}
		}
		for _, e := range relevant {
			if e.kind != "blob" {
				continue
			}
			segments := strings.Split(e.path, "/")
			for n := 1; n <= len(segments); n++ {
				if ignored[strings.Join(segments[:n], "/")] {
					restored = append(restored, e.path)
					break
				}
			}
		}
	}
	if len(restored) > 0 {
		env := []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "index")}
		if _, err := proofGit(cwd, env, "", "read-tree", base); err != nil {
			return nil, err
		}
		if _, err := proofGit(cwd, env, strings.Join(restored, "\x00")+"\x00", "checkout-index", "-f", "-z", "--stdin", "--prefix="+dir+string(filepath.Separator)); err != nil {
			return nil, err
		}
	}

	// (d) owned files at current content.
	listedText, err := proofGit(cwd, nil, "", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	candidates := []string{}
	add := func(p string) {
		if p != "" && !seen[p] {
			seen[p] = true
			if owned.OwnsFile(ownedList, kinds, p) {
				candidates = append(candidates, p)
			}
		}
	}
	for _, p := range strings.Split(listedText, "\x00") {
		add(p)
	}
	for _, entry := range ownedList {
		if p := entry.(string); owned.IsExactEntry(p) {
			add(p)
		}
	}
	for _, e := range entries {
		if e.kind == "blob" {
			add(e.path)
		}
	}
	sort.Strings(candidates)
	candidateSet := map[string]bool{}
	for _, c := range candidates {
		candidateSet[c] = true
	}
	ownedCopied := []string{}
	removedSet := map[string]bool{}
	markRemoved := func(rel string) {
		if baseBlobs[rel] {
			removedSet[rel] = true
		}
	}
	clearParents := func(rel string) error {
		segments := strings.Split(rel, "/")
		for n := 1; n < len(segments); n++ {
			parent := strings.Join(segments[:n], "/")
			at := proofLstat(filepath.Join(dir, parent))
			if at == nil {
				return nil
			}
			if !at.IsDir() {
				if err := os.Remove(filepath.Join(dir, parent)); err != nil && !os.IsNotExist(err) {
					return err
				}
				markRemoved(parent)
				return nil
			}
		}
		return nil
	}
	for _, rel := range candidates {
		if err := proofExportRel(rel, "owned files"); err != nil {
			return nil, err
		}
		src, dest := filepath.Join(cwd, rel), filepath.Join(dir, rel)
		st, at := proofLstat(src), proofLstat(dest)
		if st == nil || st.IsDir() {
			if at != nil && !at.IsDir() {
				if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
					return nil, err
				}
			}
			markRemoved(rel)
			continue
		}
		if at != nil && at.IsDir() {
			for blob := range baseBlobs {
				if strings.HasPrefix(blob, rel+"/") && !candidateSet[blob] {
					markRemoved(blob)
				}
			}
			if err := os.RemoveAll(dest); err != nil {
				return nil, err
			}
		} else if at != nil {
			if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
		}
		isLink := st.Mode()&fs.ModeSymlink != 0
		if !st.Mode().IsRegular() && !isLink {
			continue
		}
		if err := clearParents(rel); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o777); err != nil {
			return nil, err
		}
		if isLink {
			target, err := os.Readlink(src)
			if err != nil {
				return nil, err
			}
			if err := os.Symlink(target, dest); err != nil {
				return nil, err
			}
		} else if err := proofCopyFile(src, dest, st.Mode().Perm()); err != nil {
			return nil, err
		}
		ownedCopied = append(ownedCopied, rel)
	}

	// (e) dependency dirs.
	linked, copied := []string{}, []string{}
	for _, rel := range linkDirs {
		src := filepath.Join(cwd, rel)
		if info, err := os.Stat(src); err != nil || !info.IsDir() {
			continue
		}
		dest := filepath.Join(dir, rel)
		if err := os.RemoveAll(dest); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o777); err != nil {
			return nil, err
		}
		real, err := filepath.EvalSymlinks(src)
		if err != nil {
			return nil, err
		}
		if info, err := os.Stat(filepath.Join(real, "composer", "autoload_real.php")); err == nil && info.Mode().IsRegular() {
			if err := proofCopyTree(real, dest); err != nil {
				return nil, err
			}
			roots := []string{filepath.Clean(cwdGiven)}
			if cwd != roots[0] {
				roots = append(roots, cwd)
			}
			if err := proofRetargetLinks(dest, roots, dir); err != nil {
				return nil, err
			}
			copied = append(copied, rel)
			continue
		}
		if err := os.Symlink(src, dest); err != nil {
			return nil, err
		}
		linked = append(linked, rel)
	}
	removed := []string{}
	for rel := range removedSet {
		removed = append(removed, rel)
	}
	sort.Strings(restored)
	sort.Strings(untracked)
	sort.Strings(removed)
	sort.Strings(linked)
	sort.Strings(copied)
	return &proofExported{dir: dir, base: base, restored: restored, untracked: untracked, owned: ownedCopied, removed: removed, linked: linked, cps: copied}, nil
}

// proofCopyFile is fs.copyFile: the bytes, with the source's permissions.
func proofCopyFile(src, dest string, mode fs.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.WriteFile(dest, data, mode); err != nil {
		return err
	}
	return os.Chmod(dest, mode)
}

// proofCopyTree is fs.cp(recursive, verbatimSymlinks): directories and files
// keep their permissions, links are copied as they are. Anything else (a
// socket, a fifo) is not ported.
func proofCopyTree(src, dest string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.IsDir():
		if err := os.Mkdir(dest, 0o700); err != nil {
			return err
		}
		names, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, entry := range names {
			if err := proofCopyTree(filepath.Join(src, entry.Name()), filepath.Join(dest, entry.Name())); err != nil {
				return err
			}
		}
		return os.Chmod(dest, info.Mode().Perm())
	case info.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dest)
	case info.Mode().IsRegular():
		return proofCopyFile(src, dest, info.Mode().Perm())
	}
	return errProofHandover
}

// proofRetargetLinks is retargetMainTreeLinks: an absolute link into the
// main tree (not into its .code-forge/) points at the same place in the export.
func proofRetargetLinks(copy string, roots []string, dir string) error {
	pending := []string{copy}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		entries, err := os.ReadDir(current)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			entryPath := filepath.Join(current, entry.Name())
			if entry.IsDir() {
				pending = append(pending, entryPath)
				continue
			}
			if entry.Type()&fs.ModeSymlink == 0 {
				continue
			}
			target, err := os.Readlink(entryPath)
			if err != nil {
				return err
			}
			if !filepath.IsAbs(target) {
				continue
			}
			for _, root := range roots {
				rel, err := filepath.Rel(root, filepath.Clean(target))
				if err != nil || rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
					continue
				}
				if rel == "." {
					rel = ""
				}
				if rel == ".code-forge" || strings.HasPrefix(rel, ".code-forge/") {
					break
				}
				if err := os.Remove(entryPath); err != nil && !os.IsNotExist(err) {
					return err
				}
				next := dir
				if rel != "" {
					next = filepath.Join(dir, rel)
				}
				if err := os.Symlink(next, entryPath); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}
