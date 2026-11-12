package session

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// B556: the facts role's read-only snapshot cwd (spawn.mjs assertFactsCwd,
// factsTmpRoot, gitWorkTreeAncestor; tmp.mjs tmpBase and untrustedReason).

// TmpBase is tmp.mjs tmpBase() with no root: CODE_FORGE_TMP_ROOT when it is
// absolute, else <os.tmpdir()>/code-forge.
func TmpBase() string {
	if env := os.Getenv("CODE_FORGE_TMP_ROOT"); filepath.IsAbs(env) {
		return env
	}
	return filepath.Join(tempDir(), "code-forge")
}

// RealOrResolved is facts.mjs / spawn.mjs realOrResolved: the realpath when
// the path exists, else the path resolved as given.
func RealOrResolved(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		if abs, err := filepath.Abs(real); err == nil {
			return abs
		}
		return real
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return abs
}

// GitWorkTreeAncestor is spawn.mjs gitWorkTreeAncestor: the nearest directory
// at or above dir holding any `.git` entry, or "" when none has one.
func GitWorkTreeAncestor(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// FactsTmpRoot is spawn.mjs factsTmpRoot(runRootDir) without create: the path
// of the private root a facts snapshot uses when the run root sits inside a
// git work tree.
func FactsTmpRoot(runRootDir string) string {
	sum := sha256.Sum256([]byte(RealOrResolved(runRootDir)))
	return filepath.Join(TmpBase(), "facts-"+hex.EncodeToString(sum[:])[:16])
}

// untrustedReason is tmp.mjs untrustedReason(dir) with the 0o022 mask.
func untrustedReason(dir string) string {
	st, err := os.Lstat(dir)
	if err != nil {
		return "missing"
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return "symlink"
	}
	if !st.IsDir() {
		return "not a directory"
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return "foreign owner"
	}
	if st.Mode().Perm()&0o022 != 0 {
		return "group/other writable"
	}
	return ""
}

// assertFactsCwd is spawn.mjs assertFactsCwd: a facts cwd must be an existing
// directory inside a facts snapshot area of the run root, never the live
// tree and never inside a git work tree.
func assertFactsCwd(cwd, runRootDir string) error {
	refuse := func(why string) error { return &Error{"usage", "facts cwd refused: " + why} }
	if cwd == "" {
		return refuse("it must be a non-empty path")
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return refuse("it does not exist")
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return refuse("it does not exist")
	}
	st, err := os.Stat(real)
	if err != nil {
		return refuse("it does not exist")
	}
	if !st.IsDir() {
		return refuse("it is not a directory")
	}
	candidates := []string{filepath.Join(runRootDir, "facts")}
	if tmpRoot := FactsTmpRoot(runRootDir); untrustedReason(tmpRoot) == "" {
		candidates = append(candidates, filepath.Join(tmpRoot, "facts"))
	}
	areas := []string{}
	for _, area := range candidates {
		if r, err := filepath.EvalSymlinks(area); err == nil {
			areas = append(areas, r)
		}
	}
	if len(areas) == 0 {
		return refuse("the run root has no facts snapshot area")
	}
	inside := false
	for _, area := range areas {
		if strings.HasPrefix(real, area+string(filepath.Separator)) {
			inside = true
		}
	}
	if !inside {
		return refuse("it is outside the run root facts snapshot area")
	}
	if wd, err := os.Getwd(); err == nil {
		if live, err := filepath.EvalSymlinks(wd); err == nil && live == real {
			return refuse("it is a live work tree")
		}
	}
	if tree := GitWorkTreeAncestor(real); tree != "" {
		return refuse("inside a git work tree (" + tree + ")")
	}
	return nil
}

// factsSnapshot is buildStep's snapshot: realpathSync(path.resolve(cwd)).
func factsSnapshot(cwd string) (string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(abs)
}
