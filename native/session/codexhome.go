package session

// B523: the parts of src/engines/codex-home.mjs and the Codex coder build
// (src/engines/builders/codex.mjs buildCoderArgv) the doctor's Codex rules
// probe runs. No Codex CLI is started here.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// CodexRulesFileName is CODEX_RULES_FILE_NAME.
const CodexRulesFileName = "code-forge.rules"

// codexSandboxTmpExclusions is codex.mjs SANDBOX_TMP_EXCLUSIONS.
var codexSandboxTmpExclusions = []string{"-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true", "-c", "sandbox_workspace_write.exclude_slash_tmp=true"}

func envLookup(environ []string, key string) (string, bool) {
	for i := len(environ) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(environ[i], "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// codexRealHome is realCodexHome(env).
func codexRealHome(environ []string) string {
	if v, _ := envLookup(environ, "CODEX_HOME"); v != "" {
		p, _ := filepath.Abs(v)
		return p
	}
	home, _ := envLookup(environ, "HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	p, _ := filepath.Abs(filepath.Join(home, ".codex"))
	return p
}

// codexResolveExisting is resolveExisting: the realpath of the deepest
// existing ancestor, with the missing tail joined back on.
func codexResolveExisting(p string) (string, error) {
	head, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	var tail []string
	for {
		if _, err := os.Stat(head); err == nil {
			break
		}
		parent := filepath.Dir(head)
		if parent == head {
			break
		}
		tail = append([]string{filepath.Base(head)}, tail...)
		head = parent
	}
	real, err := filepath.EvalSymlinks(head)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{real}, tail...)...), nil
}

// codexRelInside reports path.relative(dir, candidate) as Node's checks read it.
func codexRelInside(candidate, dir string, allowSelf bool) (bool, error) {
	d, err := codexResolveExisting(dir)
	if err != nil {
		return false, err
	}
	c, err := codexResolveExisting(candidate)
	if err != nil {
		return false, err
	}
	rel, err := filepath.Rel(d, c)
	if err != nil {
		return false, nil
	}
	if rel == "." {
		return allowSelf, nil
	}
	return !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel), nil
}

// codexIsInside is isInside(candidate, dir).
func codexIsInside(candidate, dir string) (bool, error) { return codexRelInside(candidate, dir, true) }

// codexIsSessionHome is isSessionCodexHome(candidate, runRoot).
func codexIsSessionHome(candidate, runRoot string) (bool, error) {
	if !filepath.IsAbs(candidate) {
		return false, nil
	}
	return codexRelInside(candidate, filepath.Join(runRoot, "codex-homes"), false)
}

func codexRefuseExisting(dest string) error {
	if _, err := os.Lstat(dest); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return errors.New("prepareCodexHome: refusing to write " + filepath.Base(dest) + ": something already exists at the destination")
}

func codexWriteFresh(dest string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Chmod(mode)
}

// codexPrepareHome is prepareCodexHome(home, renderForCodex(mergeForbidden()), {runRoot}).
func codexPrepareHome(home, runRoot string, environ []string) error {
	if inside, err := codexIsInside(home, codexRealHome(environ)); err != nil || inside {
		return errors.New("prepareCodexHome: codexHome must not be the real Codex home or inside it")
	}
	if ok, err := codexIsSessionHome(home, runRoot); err != nil || !ok {
		return errors.New("prepareCodexHome: codexHome must be strictly inside <run root>/codex-homes/")
	}
	content, count := codexRulesFile(forbiddenMerged())
	if count == 0 {
		return errors.New("prepareCodexHome: the forbidden-list render has 0 rules")
	}
	rulesDir := filepath.Join(home, "rules")
	rulesPath := filepath.Join(rulesDir, CodexRulesFileName)
	auth := filepath.Join(codexRealHome(environ), "auth.json")
	authCopy := filepath.Join(home, "auth.json")
	if err := codexRefuseExisting(rulesPath); err != nil {
		return err
	}
	if err := codexRefuseExisting(authCopy); err != nil {
		return err
	}
	if err := os.MkdirAll(rulesDir, 0o700); err != nil {
		return err
	}
	if st, err := os.Lstat(rulesDir); err != nil || st.Mode()&os.ModeSymlink != 0 {
		return errors.New("prepareCodexHome: refusing a symlinked rules directory")
	}
	if err := codexWriteFresh(rulesPath, []byte(content), 0o444); err != nil {
		return err
	}
	if _, err := os.Stat(auth); err == nil {
		data, err := os.ReadFile(auth)
		if err != nil {
			return err
		}
		return codexWriteFresh(authCopy, data, 0o600)
	}
	return nil
}

// codexRemoveHome is removeCodexHome(home, runRoot).
func codexRemoveHome(home, runRoot string) error {
	if ok, err := codexIsSessionHome(home, runRoot); err != nil || !ok {
		return errors.New("removeCodexHome: refusing to delete a path outside <run root>/codex-homes/")
	}
	real, err := codexResolveExisting(home)
	if err != nil {
		return err
	}
	return os.RemoveAll(real)
}

// codexWorktreeGitDirs is worktreeGitDirs(cwd): a linked worktree's git dirs.
func codexWorktreeGitDirs(cwd string, environ []string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir")
	cmd.Dir = cwd
	cmd.Env = environ
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(trimJS(string(out)), "\n")
	if len(lines) < 2 {
		return nil // Node: path.join(undefined, …) throws, the catch returns []
	}
	if lines[0] == lines[1] {
		return nil
	}
	return []string{lines[0], filepath.Join(lines[1], "objects"), filepath.Join(lines[1], "refs"), filepath.Join(lines[1], "logs")}
}

func randomHexN(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// CodexRulesCheck is the measurable part of doctor probes.mjs
// probeCodexRules: the Codex coder build for the probe's work dir (no
// effort, the default sandbox profile, no codexHome: a fresh home under
// <runRoot>/codex-homes) and the checks on what it wrote. ok=false is the
// WARN row; count is the rule count of an OK row. The home is removed again
// through the guarded removal. err is a case Node would throw out of the
// probe (an unreadable rules file, a refused removal).
func CodexRulesCheck(workDir, runRoot string, environ []string) (count int, ok bool, err error) {
	if environ == nil {
		environ = os.Environ()
	}
	home := filepath.Join(runRoot, "codex-homes", "coder-"+randomHexN(6))
	if !filepath.IsAbs(home) {
		return 0, false, nil // the builder's TypeError
	}
	if inside, ierr := codexIsInside(home, workDir); ierr != nil || inside {
		return 0, false, nil
	}
	addDirs := codexWorktreeGitDirs(workDir, environ)
	argv := []string{"codex", "exec", "-m", "probe", "-s", "workspace-write"}
	seen := map[string]bool{}
	for _, d := range addDirs {
		if !seen[d] {
			seen[d] = true
			argv = append(argv, "--add-dir", d)
		}
	}
	argv = append(argv, codexSandboxTmpExclusions...)
	argv = append(argv, "-c", `approval_policy="never"`, "-C", workDir, "--json", "-o", filepath.Join(workDir, "out.txt"), filepath.Join(workDir, "brief.md"))
	if perr := codexPrepareHome(home, runRoot, environ); perr != nil {
		return 0, false, nil // the build throws: WARN
	}
	if sess, serr := codexIsSessionHome(home, runRoot); serr != nil || !sess {
		return 0, false, nil
	}
	if inside, ierr := codexIsInside(home, codexRealHome(environ)); ierr != nil || inside {
		return 0, false, nil
	}
	defer func() {
		if rerr := codexRemoveHome(home, runRoot); rerr != nil && err == nil {
			err = rerr
		}
	}()
	if !strings.Contains(strings.Join(argv, "\x00"), strings.Join(codexSandboxTmpExclusions, "\x00")) {
		return 0, false, nil
	}
	rulesPath := filepath.Join(home, "rules", CodexRulesFileName)
	if _, serr := os.Stat(rulesPath); serr != nil {
		return 0, false, nil
	}
	_, expected := codexRulesFile(forbiddenMerged())
	data, rerr := os.ReadFile(rulesPath)
	if rerr != nil {
		return 0, false, rerr
	}
	for _, line := range strings.Split(utf8Text(data), "\n") {
		if strings.HasPrefix(line, "prefix_rule(") && strings.Contains(line, `decision="forbidden"`) {
			count++
		}
	}
	if count == 0 || count != expected {
		return 0, false, nil
	}
	st, serr := os.Stat(rulesPath)
	if serr != nil {
		return 0, false, serr
	}
	if st.Mode().Perm()&0o222 != 0 {
		return 0, false, nil
	}
	return count, true, nil
}
