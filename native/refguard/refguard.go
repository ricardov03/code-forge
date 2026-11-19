// Package refguard keeps coders from moving the main branch or other blocks'
// branches (B797). Block open installs a git reference-transaction hook in
// the repository's hooks folder and marks the coder worktree; the hook acts
// only in a marked worktree and refuses any update of the main branch or of
// another block's branch unless the process carries the land token. Land
// checks main's reflog before it merges, and doctor reports the guard state.
package refguard

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ricardov03/code-forge/native/gitx"
)

const (
	// HookName is the git hook the guard installs.
	HookName = "reference-transaction"
	// TokenEnv carries the land token; land and release set it.
	TokenEnv = "CODE_FORGE_REF_GUARD_TOKEN"
	// MarkerName is the per-worktree file (in the worktree's git dir) that
	// turns the guard on for that worktree.
	MarkerName = "code-forge-guard.conf"
	// StateDir is the folder in the git common dir holding the token and
	// the reviewed mark.
	StateDir = "code-forge-guard"
	// LandAction is the reflog action land writes on main (GIT_REFLOG_ACTION).
	LandAction = "code-forge land"
	hookTag    = "# code-forge ref guard (B797)"
)

// hookScript is POSIX sh so it runs wherever git runs hooks.
const hookScript = `#!/bin/sh
` + hookTag + `
# Refuses, inside a code-forge coder worktree only, any move of the main
# branch or of another block's branch. code-forge block land and the release
# carry ` + TokenEnv + `. code-forge rewrites this file; do not edit it.
[ "$1" = prepared ] || exit 0
gd=$(git rev-parse --absolute-git-dir 2>/dev/null) || exit 0
conf="$gd/` + MarkerName + `"
[ -f "$conf" ] || exit 0
if [ -n "$` + TokenEnv + `" ]; then
	common=$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)
	token=$(cat "$common/` + StateDir + `/token" 2>/dev/null)
	if [ -n "$token" ] && [ "$token" = "$` + TokenEnv + `" ]; then exit 0; fi
fi
protect= own= pattern=
while IFS=' ' read -r key value; do
	case $key in
	protect) protect=$value ;;
	own) own=$value ;;
	pattern) pattern=$value ;;
	esac
done < "$conf"
status=0
while read -r old new ref; do
	# an unchanged ref is no move; a delete can carry zero ids on both sides
	case $new in *[!0]*) [ "$old" = "$new" ] && continue ;; esac
	[ -n "$own" ] && [ "$ref" = "$own" ] && continue
	what=
	if [ -n "$protect" ] && [ "$ref" = "$protect" ]; then
		what="the main branch"
	elif [ -n "$pattern" ]; then
		case $ref in $pattern) what="another block's branch" ;; esac
	fi
	if [ -n "$what" ]; then
		echo "code-forge ref guard: refused to move $ref ($what) from this coder worktree" >&2
		status=1
	fi
done
if [ $status -ne 0 ]; then
	echo "safe: git fetch origin, then read FETCH_HEAD (git log FETCH_HEAD, git diff FETCH_HEAD...HEAD); never git branch -f, git update-ref or git fetch into a local branch; only code-forge block land moves ${protect:-the main branch}" >&2
fi
exit $status
`

// Installed reports whether the file at path is the guard's hook.
func Installed(path string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), hookTag)
}

func gitPath(tree string, args ...string) (string, error) {
	out, err := gitx.Run(tree, append([]string{"rev-parse", "--path-format=absolute"}, args...)...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// HooksDir is the folder git runs this repository's hooks from.
func HooksDir(tree string) (string, error) { return gitPath(tree, "--git-path", "hooks") }

// CommonDir is the repository's shared git dir.
func CommonDir(tree string) (string, error) { return gitPath(tree, "--git-common-dir") }

// Pattern is the shell glob of all block branches made from a
// proof.worktree.branch template.
func Pattern(template string) string {
	return "refs/heads/" + strings.NewReplacer("{run}", "*", "{block}", "*").Replace(template)
}

func inside(dir, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	_, err = tmp.Write(data)
	if e := tmp.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = os.Chmod(name, mode)
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

// ensureState makes the token and the reviewed mark once per repository.
func ensureState(common string) error {
	dir := filepath.Join(common, StateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	token := filepath.Join(dir, "token")
	if data, err := os.ReadFile(token); err != nil || strings.TrimSpace(string(data)) == "" {
		raw := make([]byte, 16)
		if _, err := rand.Read(raw); err != nil {
			return err
		}
		if err := writeAtomic(token, []byte(hex.EncodeToString(raw)+"\n"), 0o600); err != nil {
			return err
		}
	}
	reviewed := filepath.Join(dir, "reviewed")
	if _, err := os.Stat(reviewed); os.IsNotExist(err) {
		return writeAtomic(reviewed, []byte("main reflog entries up to this file's time are reviewed; touch it to accept newer ones\n"), 0o600)
	}
	return nil
}

// Install guards the coder worktree tree: protect is the main branch (empty
// when the block was opened on a detached HEAD), own the block's branch and
// template the proof.worktree.branch template. It returns the hook path. An
// error names why the guard is not on; the worktree still works.
func Install(tree, protect, own, template string) (string, error) {
	hooks, err := HooksDir(tree)
	if err != nil {
		return "", fmt.Errorf("cannot find the hooks folder: %w", err)
	}
	common, err := CommonDir(tree)
	if err != nil {
		return "", fmt.Errorf("cannot find the git common dir: %w", err)
	}
	gitDir, err := gitPath(tree, "--absolute-git-dir")
	if err != nil {
		return "", fmt.Errorf("cannot find the worktree git dir: %w", err)
	}
	top, _ := gitx.Run(tree, "rev-parse", "--show-toplevel")
	mainTop := filepath.Dir(common)
	hook := filepath.Join(hooks, HookName)
	var off error
	if !inside(hooks, common) && (inside(hooks, strings.TrimSpace(top)) || filepath.Base(common) == ".git" && inside(hooks, mainTop)) {
		off = fmt.Errorf("core.hooksPath %s is inside a work tree; add the code-forge hook there by hand", hooks)
	} else if _, err := os.Lstat(hook); err == nil && !Installed(hook) {
		off = fmt.Errorf("%s already holds another hook; the guard is off", hook)
	}
	if err := ensureState(common); err != nil {
		return hook, fmt.Errorf("cannot write the guard state: %w", err)
	}
	offFile := filepath.Join(common, StateDir, "off")
	if off != nil {
		// doctor reads why the guard could not go on.
		_ = writeAtomic(offFile, []byte(off.Error()+"\n"), 0o600)
		return hook, off
	}
	_ = os.Remove(offFile)
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		return hook, err
	}
	if data, err := os.ReadFile(hook); err != nil || string(data) != hookScript {
		if err := writeAtomic(hook, []byte(hookScript), 0o755); err != nil {
			return hook, err
		}
	}
	conf := ""
	if protect != "" {
		conf += "protect refs/heads/" + protect + "\n"
	}
	conf += "own refs/heads/" + own + "\npattern " + Pattern(template) + "\n"
	if err := writeAtomic(filepath.Join(gitDir, MarkerName), []byte(conf), 0o644); err != nil {
		return hook, err
	}
	return hook, nil
}

// Authorize sets the land token in this process's environment so git
// children (merges on main) pass the hook. No guard state: nothing to do.
func Authorize(tree string) {
	common, err := CommonDir(tree)
	if err != nil {
		return
	}
	if data, err := os.ReadFile(filepath.Join(common, StateDir, "token")); err == nil {
		if token := strings.TrimSpace(string(data)); token != "" {
			_ = os.Setenv(TokenEnv, token)
		}
	}
}

// LandEnv is the git environment for land's merge on main: the land token
// (Authorize) and the reflog action the next land's check stops at.
func LandEnv(ids ...string) []string {
	return append(gitx.ChildEnv(), "GIT_REFLOG_ACTION="+LandAction+" "+strings.Join(ids, " "))
}

// Entry is one reflog line of the main branch the check refused.
type Entry struct {
	SHA, Subject string
	At           time.Time
}

func (e Entry) String() string {
	return fmt.Sprintf("%.12s %s %s", e.SHA, e.At.UTC().Format(time.RFC3339), e.Subject)
}

var (
	reflogTime = regexp.MustCompile(`@\{(\d+)\}$`)
	safeAction = regexp.MustCompile(`^(commit|commit \(merge\)|commit \(initial\)|merge [^:]*|pull[^:]*|code-forge release[^:]*): `)
)

// ReviewedPath is the file whose time marks main's reviewed reflog entries.
func ReviewedPath(common string) string { return filepath.Join(common, StateDir, "reviewed") }

// Unsafe lists main's reflog entries since the last land (and after the
// reviewed mark) that are not a land, a release or a plain fast-forward
// commit, merge or pull. No guard state, no reflog: nothing to check.
func Unsafe(tree, branch string) ([]Entry, string, error) {
	common, err := CommonDir(tree)
	if err != nil {
		return nil, "", err
	}
	reviewed := ReviewedPath(common)
	info, err := os.Stat(reviewed)
	if err != nil {
		return nil, reviewed, nil
	}
	out, err := gitx.Run(tree, "reflog", "show", "--date=unix", "--format=%H%x09%gd%x09%gs", "refs/heads/"+branch, "--")
	if err != nil {
		return nil, reviewed, nil
	}
	type line struct {
		sha, subject string
		at           int64
	}
	lines := []line{}
	for _, raw := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		parts := strings.SplitN(raw, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		m := reflogTime.FindStringSubmatch(parts[1])
		if m == nil {
			continue
		}
		at, _ := strconv.ParseInt(m[1], 10, 64)
		lines = append(lines, line{parts[0], parts[2], at})
	}
	since := info.ModTime().Unix()
	bad := []Entry{}
	for i, l := range lines {
		if l.at <= since || strings.HasPrefix(l.subject, LandAction) {
			break
		}
		ok := safeAction.MatchString(l.subject)
		if ok && i+1 < len(lines) && lines[i+1].sha != l.sha {
			_, err := gitx.Run(tree, "merge-base", "--is-ancestor", lines[i+1].sha, l.sha)
			ok = err == nil
		}
		if !ok {
			bad = append(bad, Entry{l.sha, l.subject, time.Unix(l.at, 0)})
		}
	}
	return bad, reviewed, nil
}

// Refusal is land's refusal text for unsafe reflog entries.
func Refusal(branch, main, reviewed string, bad []Entry) string {
	lines := []string{fmt.Sprintf("%s moved outside land since the last land (%d reflog entr%s that are not a land, release or fast-forward commit):", branch, len(bad), map[bool]string{true: "y", false: "ies"}[len(bad) == 1])}
	for _, e := range bad {
		lines = append(lines, "  "+e.String())
	}
	lines = append(lines, fmt.Sprintf("check %s (git -C %s reflog show %s); when it is right, accept these entries with: touch %s", branch, main, branch, reviewed))
	return strings.Join(lines, "\n")
}

// State is doctor's view of the guard for the repository at tree: "" when
// no coder worktree was ever opened there (no row).
func State(tree string) (status, detail string) {
	if neverGuarded(tree) {
		return "", ""
	}
	common, err := CommonDir(tree)
	if err != nil {
		return "", ""
	}
	hooks, err := HooksDir(tree)
	if err != nil {
		return "", ""
	}
	hook := filepath.Join(hooks, HookName)
	if _, err := os.Stat(filepath.Join(common, StateDir, "token")); err != nil {
		// No coder worktree was ever opened here: nothing to guard, no row.
		return "", ""
	}
	if data, err := os.ReadFile(filepath.Join(common, StateDir, "off")); err == nil && !Installed(hook) {
		return "WARN", "off: " + strings.TrimSpace(string(data))
	}
	if !Installed(hook) {
		if _, err := os.Lstat(hook); err == nil {
			return "WARN", "off: " + hook + " holds another hook"
		}
		return "WARN", "off: hook " + hook + " is missing (the next block open puts it back)"
	}
	guarded := 0
	if entries, err := os.ReadDir(filepath.Join(common, "worktrees")); err == nil {
		for _, e := range entries {
			if _, err := os.Stat(filepath.Join(common, "worktrees", e.Name(), MarkerName)); err == nil {
				guarded++
			}
		}
	}
	return "OK", fmt.Sprintf("on: hook %s; %d coder worktree(s) guarded", hook, guarded)
}

// neverGuarded answers without running git when the nearest .git above tree
// is a plain git dir with no guard state, the common case of a project that
// never opened a coder worktree, so doctor starts no extra child.
func neverGuarded(tree string) bool {
	dir, err := filepath.Abs(tree)
	if err != nil {
		return false
	}
	for {
		info, err := os.Stat(filepath.Join(dir, ".git"))
		if err == nil {
			if !info.IsDir() {
				return false
			}
			_, err := os.Stat(filepath.Join(dir, ".git", StateDir))
			return os.IsNotExist(err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}
