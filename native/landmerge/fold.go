package landmerge

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
)

// B813: a merge git finishes without a conflict can still leave two
// `### Added` headings in one release section of CHANGELOG.md (each side
// added its own group in a different place). After every CHANGELOG merge the
// headings of one kind are folded into the first one, every line kept in
// order.

// FoldChangelog folds, inside each `## ` section of merged, the `### Kind`
// headings repeated beyond ref's count into the first one of that kind; a
// bullet repeated beyond ref's count is kept once. ref is the side whose
// shape is kept (main for a land, the new base for a rebase).
func FoldChangelog(merged, ref string) string { return foldHeadings(merged, ref) }

func isChangelog(file string) bool { return path.Base(path.Clean(file)) == "CHANGELOG.md" }

// FoldNeeded reports whether merging rev into tree's HEAD gives a
// CHANGELOG.md that FoldChangelog would change. It writes nothing (git
// merge-tree); a merge with conflicts is judged on git's result too.
func FoldNeeded(tree string, env []string, rev string) bool {
	out, err := gitx.WithEnv("", env, "-C", tree, "merge-tree", "--write-tree", "--no-messages", "HEAD", rev)
	if err != nil {
		if _, isCommand := err.(*gitx.CommandError); !isCommand || out == "" {
			return false
		}
	}
	result := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
	if result == "" {
		return false
	}
	changed, err := gitx.WithEnv("", env, "-C", tree, "diff", "--name-only", "-z", "HEAD", result, "--")
	if err != nil {
		return false
	}
	for _, file := range strings.Split(changed, "\x00") {
		if file == "" || !isChangelog(file) {
			continue
		}
		merged, err := gitx.WithEnv("", env, "-C", tree, "cat-file", "blob", result+":"+file)
		if err != nil {
			continue
		}
		ref, _ := gitx.WithEnv("", env, "-C", tree, "cat-file", "blob", "HEAD:"+file)
		if FoldChangelog(merged, ref) != merged {
			return true
		}
	}
	return false
}

// FoldIndex folds every CHANGELOG.md of tree's index that differs from HEAD
// against HEAD's copy, writes and stages the folded text, and lists the files
// it changed. The index must have no unmerged entry.
func FoldIndex(tree string, env []string) ([]string, error) {
	changed, err := gitx.WithEnv("", env, "-C", tree, "diff", "--cached", "--name-only", "-z", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	folded := []string{}
	for _, file := range strings.Split(changed, "\x00") {
		if file == "" || !isChangelog(file) {
			continue
		}
		merged, err := gitx.WithEnv("", env, "-C", tree, "show", ":"+file)
		if err != nil {
			continue // removed in the index
		}
		ref, _ := gitx.WithEnv("", env, "-C", tree, "show", "HEAD:"+file)
		body := FoldChangelog(merged, ref)
		if body == merged {
			continue
		}
		full := filepath.Join(tree, file)
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if err := os.WriteFile(full, []byte(body), info.Mode().Perm()); err != nil {
			return nil, err
		}
		if _, err := gitx.WithEnv("", env, "-C", tree, "add", "--", file); err != nil {
			return nil, err
		}
		folded = append(folded, file)
	}
	return folded, nil
}
