package rebase

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
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
type ConflictError struct{ Files []string }

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

// lines splits text into lines that keep their newline.
func lines(text string) []string {
	if text == "" {
		return nil
	}
	return strings.SplitAfter(strings.TrimSuffix(text, "\n"), "\n")
}

// addOnly reports whether side is base with lines only inserted: every base
// line is kept, in order. A trailing newline is normalised so that adding a
// line after an unterminated last line still counts as an addition.
func addOnly(base, side string) bool {
	b, s := lines(base), lines(side)
	j := 0
	for _, line := range s {
		if j < len(b) && strings.TrimSuffix(line, "\n") == strings.TrimSuffix(b[j], "\n") {
			j++
		}
	}
	return j == len(b)
}

// AppendMerge merges an add-only file three ways: lines both sides added at
// the same place keep ours (the new base side) first, then theirs. ok is false
// when either side removed or changed a base line.
func AppendMerge(base, ours, theirs string) (merged string, ok bool, err error) {
	if !addOnly(base, ours) || !addOnly(base, theirs) {
		return "", false, nil
	}
	return threeWay(base, ours, theirs, true)
}

// threeWay runs git merge-file on the three texts; ok is false when a plain
// merge (union false) has conflicts.
func threeWay(base, ours, theirs string, union bool) (merged string, ok bool, err error) {
	dir, err := os.MkdirTemp("", "forge-append-")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(dir)
	names := []string{filepath.Join(dir, "ours"), filepath.Join(dir, "base"), filepath.Join(dir, "theirs")}
	for i, body := range []string{ours, base, theirs} {
		if err := os.WriteFile(names[i], []byte(body), 0600); err != nil {
			return "", false, err
		}
	}
	args := []string{"merge-file", "-p"}
	if union {
		args = append(args, "--union")
	}
	cmd := exec.Command("git", append(args, names...)...)
	cmd.Env = gitx.ChildEnv()
	out, err := cmd.Output()
	if exit, isExit := err.(*exec.ExitError); isExit && exit.ExitCode() > 0 && exit.ExitCode() < 128 {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return string(out), true, nil
}

// unionFile reports whether the file's merge attribute is union.
func unionFile(tree string, env []string, file string) bool {
	out, err := gitx.WithEnv("", env, "-C", tree, "check-attr", "-z", "merge", "--", file)
	if err != nil {
		return false
	}
	fields := strings.Split(out, "\x00")
	return len(fields) >= 3 && fields[2] == "union"
}

// stages reads the unmerged index entries of one file: stage → blob id.
func stages(tree string, env []string, file string) (map[string]string, error) {
	out, err := gitx.WithEnv("", env, "-C", tree, "ls-files", "-u", "-z", "--", file)
	if err != nil {
		return nil, err
	}
	found := map[string]string{}
	for _, entry := range strings.Split(out, "\x00") {
		meta, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(meta)
		if ok && name == file && len(fields) == 3 {
			found[fields[2]] = fields[1]
		}
	}
	return found, nil
}

func blob(tree string, env []string, id string) (string, error) {
	if id == "" {
		return "", nil
	}
	return gitx.WithEnv("", env, "-C", tree, "cat-file", "blob", id)
}

// resolveAppend merges and stages every unmerged file, or returns a
// ConflictError naming all of them when any one is not an add-only CHANGELOG.md.
func resolveAppend(tree string, env []string) error {
	files, err := unmerged(tree, env)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return &ConflictError{}
	}
	merged := map[string]string{}
	for _, file := range files {
		union := unionFile(tree, env, file)
		if path.Base(file) != "CHANGELOG.md" && !union {
			return &ConflictError{files}
		}
		ids, err := stages(tree, env, file)
		if err != nil {
			return err
		}
		if ids["2"] == "" || ids["3"] == "" {
			return &ConflictError{files}
		}
		var text [3]string
		for i, stage := range []string{"1", "2", "3"} {
			if text[i], err = blob(tree, env, ids[stage]); err != nil {
				return err
			}
		}
		body, ok, err := threeWay(text[0], text[1], text[2], false)
		if err == nil && !ok && path.Base(file) == "CHANGELOG.md" {
			body, ok, err = AppendMerge(text[0], text[1], text[2])
		} else if err == nil && !ok && union {
			body, ok, err = threeWay(text[0], text[1], text[2], true)
		}
		if err != nil {
			return err
		}
		if !ok {
			return &ConflictError{files}
		}
		merged[file] = body
	}
	for _, file := range files {
		info, err := os.Lstat(filepath.Join(tree, file))
		if err != nil || !info.Mode().IsRegular() {
			return &ConflictError{files}
		}
		if err := os.WriteFile(filepath.Join(tree, file), []byte(merged[file]), info.Mode().Perm()); err != nil {
			return err
		}
		if _, err := gitx.WithEnv("", env, "-C", tree, "add", "--", file); err != nil {
			return err
		}
	}
	return nil
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
	editor := append(append([]string{}, env...), "GIT_EDITOR=true")
	for step := 0; ; step++ {
		if step > 10000 {
			return fmt.Errorf("replay did not finish")
		}
		if err := resolveAppend(tree, env); err != nil {
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
