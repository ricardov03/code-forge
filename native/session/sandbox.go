package session

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// B618: the Codex coder sandbox of src/engines/sandbox-dirs.mjs and
// builders/codex.mjs coderSandbox (block B93): coder.sandbox.{profile,
// add_dirs}. workspace (the default) is today's argv; toolchain adds one
// --add-dir per checked add_dirs entry; full swaps -s workspace-write for
// -s danger-full-access. A refused entry is the sandbox-dir-refused session
// error, naming the key path and never the value.

// sandboxProfiles is SANDBOX_PROFILES, in the order the docs list them.
var sandboxProfiles = []string{"workspace", "toolchain", "full"}

// sandboxDefaultProfile is DEFAULT_SANDBOX_PROFILE.
const sandboxDefaultProfile = "workspace"

// sandboxProtectedHomeDirs is PROTECTED_HOME_DIRS.
var sandboxProtectedHomeDirs = []string{".code-forge", ".codex", ".ssh", ".gnupg", ".aws", ".config", filepath.Join("Library", "Keychains")}

const sandboxProtectedWhy = "it is, holds or sits inside a protected directory (~/.code-forge, ~/.codex, ~/.ssh, ~/.gnupg, ~/.aws, ~/.config, ~/Library/Keychains)"

// coderSandboxFor is spawn.mjs coderSandboxFor: the coder.sandbox value a
// Codex coder session builds with, or nil (no key, a null value, another
// role or another CLI).
func coderSandboxFor(o Opts, provider string) any {
	if provider != "openai" || o.Role != "coder" {
		return nil
	}
	return section(o.Cfg, "coder")["sandbox"]
}

// sandboxPlanOK is the part of coderSandbox Go answers like Node: a mapping
// whose profile is absent, null or one of the profiles. Anything else is a
// TypeError Node prints with its stack, which stays with Node.
func sandboxPlanOK(sandbox any) bool {
	m, ok := sandbox.(map[string]any)
	if !ok {
		return false
	}
	profile, present := m["profile"]
	if !present || profile == nil {
		return true
	}
	text, ok := profile.(string)
	return ok && contains(sandboxProfiles, text)
}

// sandboxProfile is the profile of a checked sandbox (the session row's
// sandbox field and the builder's profile).
func sandboxProfile(sandbox any) string {
	m, _ := sandbox.(map[string]any)
	if text, ok := m["profile"].(string); ok {
		return text
	}
	return sandboxDefaultProfile
}

// coderSandbox is builders/codex.mjs coderSandbox for a sandbox plan let
// through: the profile and the add dirs (toolchain only), or the
// sandbox-dir-refused session error of the first refused entry.
func coderSandbox(sandbox any, extra []string) (string, []string, error) {
	if sandbox == nil {
		return sandboxDefaultProfile, nil, nil
	}
	profile := sandboxProfile(sandbox)
	m, _ := sandbox.(map[string]any)
	dirs, err := sandboxCheckDirs(m, extra)
	if err != nil {
		return "", nil, err
	}
	if profile != "toolchain" {
		dirs = nil
	}
	return profile, dirs, nil
}

// sandboxCheckDirs is checkSandboxDirs: every add_dirs entry checked and
// resolved; the first refusal wins.
func sandboxCheckDirs(m map[string]any, extra []string) ([]string, error) {
	raw, present := m["add_dirs"]
	if !present {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, sandboxRefusal("coder.sandbox.add_dirs", "it must be a list of directories")
	}
	trees := sandboxRefusedTrees(extra)
	var out []string
	for i, entry := range list {
		dir, why := sandboxCheckOne(entry, trees)
		if why != "" {
			return nil, sandboxRefusal("coder.sandbox.add_dirs["+strconv.Itoa(i)+"]", why)
		}
		out = append(out, dir)
	}
	return out, nil
}

// sandboxRefusal is sandboxRefusalMessage as the session error spawn.mjs
// buildStep throws for a SandboxDirError.
func sandboxRefusal(keyPath, why string) error {
	return &Error{"sandbox-dir-refused", "sandbox-dir-refused: " + keyPath + " is refused — " + why}
}

type sandboxTrees struct {
	home, realHome string
	protected, run []string
}

// sandboxHome is homeOf(process.env).
func sandboxHome() string {
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	if u, err := user.Current(); err == nil {
		return u.HomeDir
	}
	return ""
}

// sandboxRefusedTrees is refusedTrees({extra}) with the process env.
func sandboxRefusedTrees(extra []string) sandboxTrees {
	home := sandboxHome()
	trees := sandboxTrees{home: home, realHome: sandboxResolveTree(home)}
	for _, dir := range sandboxProtectedHomeDirs {
		trees.protected = append(trees.protected, sandboxResolveTree(filepath.Join(home, dir)))
	}
	if codex := os.Getenv("CODEX_HOME"); filepath.IsAbs(codex) {
		trees.protected = append(trees.protected, sandboxResolveTree(codex))
	}
	for _, p := range extra {
		if filepath.IsAbs(p) {
			trees.run = append(trees.run, sandboxResolveTree(p))
		}
	}
	return trees
}

// sandboxCheckOne is checkOne: the resolved dir, or the refusal's why.
func sandboxCheckOne(entry any, trees sandboxTrees) (string, string) {
	text, _ := entry.(string)
	var expanded string
	switch {
	case text == "" || strings.Contains(text, "\x00"):
		return "", "it must be an absolute path or start with ~/"
	case strings.HasPrefix(text, "~/"):
		abs, _ := filepath.Abs(trees.home)
		expanded = abs + string(filepath.Separator) + text[2:]
	case filepath.IsAbs(text):
		expanded = text
	default:
		return "", "it must be an absolute path or start with ~/"
	}
	hops := 0
	real, err := sandboxResolvePath(expanded, &hops)
	switch {
	case err != nil:
		return "", "its symlinks cannot be resolved"
	case real == "/":
		return "", "it is the filesystem root"
	case sandboxSame(real, trees.realHome):
		return "", "it is the home directory"
	case sandboxWithin(trees.realHome, real):
		return "", "it holds the home directory"
	}
	for _, tree := range trees.protected {
		if sandboxWithin(real, tree) || sandboxWithin(tree, real) {
			return "", sandboxProtectedWhy
		}
	}
	for _, tree := range trees.run {
		if sandboxWithin(real, tree) || sandboxWithin(tree, real) {
			return "", "it is, holds or sits inside the run temp root or the session CODEX_HOME"
		}
	}
	return real, ""
}

var errSandboxUnresolvable = errors.New("unresolvable")

// sandboxNative is realpathSync.native, or p when it fails.
func sandboxNative(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return p
}

// sandboxResolvePath is resolvePath: a component walk with lstat, following
// each symlink (at most 40), ".." from the resolved dir; a missing component
// keeps the rest as written.
func sandboxResolvePath(p string, hops *int) (string, error) {
	var parts []string
	for _, part := range strings.Split(strings.TrimPrefix(p, "/"), "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	cur := "/"
	for i, part := range parts {
		if part == ".." {
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, part)
		st, err := os.Lstat(next)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
				return filepath.Join(append([]string{sandboxNative(cur)}, parts[i:]...)...), nil
			}
			return "", errSandboxUnresolvable
		}
		if st.Mode()&fs.ModeSymlink == 0 {
			cur = next
			continue
		}
		*hops++
		if *hops > 40 {
			return "", errSandboxUnresolvable
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", errSandboxUnresolvable
		}
		if !filepath.IsAbs(target) {
			target = cur + "/" + target
		}
		if cur, err = sandboxResolvePath(target, hops); err != nil {
			return "", err
		}
	}
	return sandboxNative(cur), nil
}

// sandboxResolveTree is resolveTree: resolved, or kept as written.
func sandboxResolveTree(p string) string {
	abs, _ := filepath.Abs(p)
	hops := 0
	if real, err := sandboxResolvePath(abs, &hops); err == nil {
		return real
	}
	return abs
}

func sandboxFold(p string) string {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.ToLower(p)
	}
	return p
}

func sandboxSame(a, b string) bool { return sandboxFold(a) == sandboxFold(b) }

// sandboxWithin: a is b or inside it.
func sandboxWithin(a, b string) bool {
	rel, err := filepath.Rel(sandboxFold(b), sandboxFold(a))
	if err != nil {
		return false
	}
	return rel == "." || !(rel == ".." || strings.HasPrefix(rel, "../") || filepath.IsAbs(rel))
}
