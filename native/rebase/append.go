package rebase

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
	"github.com/ricardov03/code-forge/native/landmerge"
	"github.com/ricardov03/code-forge/native/releasecheck"
)

// B431: a replay that stops is a real conflict unless every unmerged file is a
// CHANGELOG.md to which both sides only added lines. Such a file is merged by
// keeping both sets of added lines (the base side's first, then the replayed
// commit's) and the stopped cherry-pick or rebase goes on. Nothing in the
// repository's files or config is changed for this.

// UnionOff is passed as `-c` to every replay command. A repository may set
// `CHANGELOG.md merge=union` in .gitattributes; git's union driver then
// merges a conflicted region by keeping both sides, which brings back a line
// the replayed commit removed. A user driver named "union" is found before
// git's built-in one, so this one, which always fails, makes git stop with
// the file unmerged; resolveAppend then merges it: first a plain three-way
// merge, then the append-only rule for CHANGELOG.md, and for any other file
// marked merge=union git's own union merge, as before.
const UnionOff = "merge.union.driver=false"

// ConflictError names the files a replay could not merge.
type ConflictError struct {
	Files []string
	Kinds []landmerge.File // B795: each file with its kind (code, changelog, table, ...)
}

func (e *ConflictError) Error() string {
	if len(e.Files) == 0 {
		return "the replay stopped without a merge conflict"
	}
	return "replay conflicts in: " + FileList(e.Files)
}

// FileList joins file names on one line; a name with a control character or
// a quote is shown Go-quoted.
func FileList(files []string) string {
	shown := make([]string, len(files))
	for i, file := range files {
		shown[i] = file
		if strings.ContainsFunc(file, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '"' }) {
			shown[i] = strconv.Quote(file)
		}
	}
	return strings.Join(shown, ", ")
}

// ConflictMessage is the one-line rebase-conflict reason for a block.
func ConflictMessage(block string, err error) string {
	return "block " + block + ": " + err.Error() + "; nothing changed: rebase the block worktree onto its base branch by hand, resolve these files, then run block rebase again"
}

func unmerged(tree string, env []string) ([]string, error) {
	out, err := gitx.WithEnv("", env, "-C", tree, "diff", "--name-only", "--diff-filter=U", "-z", "--")
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, file := range strings.Split(out, "\x00") {
		if file != "" {
			files = append(files, file)
		}
	}
	sort.Strings(files)
	return files, nil
}

// AppendMerge merges an add-only file three ways: lines both sides added at
// the same place keep ours (the new base side) first, then theirs. ok is false
// when either side removed or changed a base line. B795: a `### Kind` heading
// the merge would repeat in one release section is folded into one.
func AppendMerge(base, ours, theirs string) (merged string, ok bool, err error) {
	return landmerge.Changelog(base, ours, theirs)
}

// resolveAppend merges and stages every unmerged file, or returns a
// ConflictError naming all of them when any one is not append-only under
// settings (B431: CHANGELOG.md and merge=union files; B795: tables and
// generated files of land.auto_resolve).
func resolveAppend(tree string, env []string, settings landmerge.Settings) error {
	_, err := landmerge.Resolve(tree, env, settings)
	var conflict *landmerge.Conflict
	if errors.As(err, &conflict) {
		if len(conflict.Files) == 0 {
			return &ConflictError{}
		}
		return &ConflictError{Files: landmerge.Paths(conflict.Files), Kinds: conflict.Files}
	}
	return err
}

func inProgress(tree string, env []string, ref string) bool {
	_, err := gitx.WithEnv("", env, "-C", tree, "rev-parse", "-q", "--verify", ref)
	return err == nil
}

func rebasing(tree string, env []string) bool {
	for _, dir := range []string{"rebase-merge", "rebase-apply"} {
		out, err := gitx.WithEnv("", env, "-C", tree, "rev-parse", "--git-path", dir)
		if err != nil {
			continue
		}
		p := strings.TrimSpace(out)
		if !filepath.IsAbs(p) {
			p = filepath.Join(tree, p)
		}
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// Continue finishes a stopped cherry-pick or rebase in tree after resolving
// append-only CHANGELOG.md conflicts, step after step, until the replay ends.
// A cherry-pick is continued under its picked commit's original committer.
// Any other stop returns a ConflictError naming the unmerged files; the caller
// aborts and restores its HEAD.
func Continue(tree string, env []string) error {
	return ContinueWith(tree, env, landmerge.Legacy())
}

// ContinueWith is Continue with land.auto_resolve settings (B795).
func ContinueWith(tree string, env []string, settings landmerge.Settings) error {
	editor := append(append([]string{}, env...), "GIT_EDITOR=true")
	for step := 0; ; step++ {
		if step > 10000 {
			return fmt.Errorf("replay did not finish")
		}
		if err := resolveAppend(tree, env, settings); err != nil {
			return err
		}
		if inProgress(tree, env, "CHERRY_PICK_HEAD") {
			identity, err := gitx.WithEnv("", env, "-C", tree, "show", "-s", "--format=%cn%x00%ce", "CHERRY_PICK_HEAD")
			if err != nil {
				return err
			}
			id := strings.Split(strings.TrimSuffix(identity, "\n"), "\x00")
			if len(id) != 2 {
				return fmt.Errorf("invalid committer")
			}
			pick := append(append([]string{}, editor...), "GIT_COMMITTER_NAME="+id[0], "GIT_COMMITTER_EMAIL="+id[1])
			if _, err := gitx.WithEnv("", pick, "-C", tree, "-c", UnionOff, "-c", "commit.gpgsign=false", "cherry-pick", "--continue"); err != nil {
				continue
			}
		}
		if !rebasing(tree, env) {
			return nil
		}
		if _, err := gitx.WithEnv("", editor, "-C", tree, "-c", UnionOff, "-c", "commit.gpgsign=false", "rebase", "--continue"); err == nil {
			return nil
		}
	}
}

// Conflicts lists the unmerged files of a stopped replay (for the refusal).
func Conflicts(tree string) []string {
	files, _ := unmerged(tree, gitx.ChildEnv())
	return files
}

// ChangelogShape is the CHANGELOG.md shape check a replay runs before its
// result can be pushed (B890): HEAD's ## [Unreleased] must not repeat a ###
// kind that base's does not repeat. A tree with no CHANGELOG.md passes.
func ChangelogShape(tree, base string) error {
	head, err := gitWithEnv(tree, gitEnv(), "show", "HEAD:CHANGELOG.md")
	if err != nil {
		return nil
	}
	old := map[string]bool{}
	if body, err := gitWithEnv(tree, gitEnv(), "show", base+":CHANGELOG.md"); err == nil {
		for _, kind := range releasecheck.ChangelogRepeats(body) {
			old[kind] = true
		}
	}
	repeated := []string{}
	for _, kind := range releasecheck.ChangelogRepeats(head) {
		if !old[kind] {
			repeated = append(repeated, kind)
		}
	}
	if len(repeated) == 0 {
		return nil
	}
	return fmt.Errorf("CHANGELOG.md repeats ### %s in ## [Unreleased] after the replay; merge them into one heading per kind", strings.Join(repeated, ", ### "))
}
