package gitx

import (
	"errors"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// B828: a project's .code-forge/ folder (runs mirror, queue, workers,
// reviews and the proof export, which holds copies of .env) is local state
// and never belongs in git. These helpers own the .gitignore line and tell
// whether git would see the folder.

const (
	// StateFolder is the per-project state folder name.
	StateFolder = ".code-forge"
	// StateIgnoreLine is the .gitignore line init and upgrade add.
	StateIgnoreLine = ".code-forge/"
	// StateUntrackFix is the command that stops git tracking the folder.
	StateUntrackFix = "git rm -r --cached .code-forge && git commit"
)

// IsStatePath reports a repo-relative POSIX path under a .code-forge/
// folder at any depth (".code-forge/x", "app/.code-forge/x",
// ".code-forge/"). ".code-forge.yml" and ".code-forge-x/y" are not.
func IsStatePath(p string) bool {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if part == StateFolder && (i < len(parts)-1) {
			return true
		}
	}
	return false
}

// stateIgnoreLines are the .gitignore lines that already ignore the folder.
var stateIgnoreLines = map[string]bool{
	".code-forge/": true, ".code-forge": true, "/.code-forge/": true, "/.code-forge": true,
	".code-forge/*": true, "/.code-forge/*": true, ".code-forge/**": true, "/.code-forge/**": true,
}

// StateIgnoreFile is the .gitignore init and upgrade own: the one at the
// repository top of dir ("" when dir is not in a git work tree).
func StateIgnoreFile(dir string) string {
	if top, err := RepoTop(dir); err == nil {
		return filepath.Join(top, ".gitignore")
	}
	return ""
}

// EnsureStateIgnored adds ".code-forge/" to the .gitignore of dir's
// repository (StateIgnoreFile), creating the file when it is missing. It
// keeps every user line in order and is idempotent: a file that already
// has an ignoring line, or a repository where git already ignores the
// folder, is left alone. added is true when the file was written. Outside
// a git work tree nothing is written (file is "").
func EnsureStateIgnored(dir string) (file string, added bool, err error) {
	file = StateIgnoreFile(dir)
	if file == "" {
		return "", false, nil
	}
	data, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return file, false, err
	}
	text := string(data)
	for _, line := range strings.Split(text, "\n") {
		if stateIgnoreLines[strings.TrimSpace(strings.TrimSuffix(line, "\r"))] {
			return file, false, nil
		}
	}
	if visible, known := StateVisible(dir); known && !visible {
		return file, false, nil
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += StateIgnoreLine + "\n"
	mode := os.FileMode(0o644)
	if info, statErr := os.Stat(file); statErr == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(file, []byte(text), mode); err != nil {
		return file, false, err
	}
	return file, true, nil
}

// StateVisible asks git whether a file in dir's .code-forge/export would be
// seen (not ignored). The probe name has no extension on purpose: a
// Laravel .gitignore ignores ".env" anywhere, yet the export also copies
// .env.testing and other files it does not ignore. known is false outside
// a git work tree or when git cannot answer; then nothing is visible to git
// through dir.
func StateVisible(dir string) (visible, known bool) {
	probe := path.Join(StateFolder, "export", "B0", "probe")
	_, err := Run(dir, "check-ignore", "-q", "--no-index", "--", probe)
	if err == nil {
		return false, true
	}
	var ce *CommandError
	if errors.As(err, &ce) && ce.Code == 1 {
		return true, true
	}
	return false, false
}

// StateTracked lists the paths git tracks under any .code-forge/ folder of
// dir's repository (repo-relative). nil outside a git work tree.
func StateTracked(dir string) []string {
	top, err := RepoTop(dir)
	if err != nil {
		return nil
	}
	out, err := Run(top, "ls-files", "-z", "--", ":(glob)**/"+StateFolder+"/**")
	if err != nil {
		return nil
	}
	return nonempty(out)
}

// StateTrackedLine is the one line init, upgrade and doctor say when git
// tracks state files: what and the exact fix (never run for the user).
func StateTrackedLine(tracked []string) string {
	noun := "files"
	if len(tracked) == 1 {
		noun = "file"
	}
	return StateIgnoreLine + ": git tracks " + strconv.Itoa(len(tracked)) + " " + noun + " under it (" + tracked[0] + "); remove them from git with: " + StateUntrackFix
}
