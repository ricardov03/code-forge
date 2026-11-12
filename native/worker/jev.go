package worker

import (
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// B489: the Go worker serves a run only while the System 1 (Jev) key is
// provably absent. Jev triage is not ported, and a resolved key makes every
// engine ticket hand over anyway (planEngineDry), so a worker whose key could
// resolve is left to Node as a whole, before it boots.

var keyName = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// JevProbe holds the seams of JevKeyAbsent.
type JevProbe struct {
	Getenv func(string) string // nil = os.Getenv
	// Keychain reports whether the login keychain holds a generic password
	// for service/account; decided false means the answer is unknown.
	// nil = `/usr/bin/security find-generic-password -s <service> -a <name>`
	// (no -w: the secret is never read) on darwin, undecided elsewhere.
	Keychain func(service, account string) (found, decided bool)
}

func (p JevProbe) getenv(name string) string {
	if p.Getenv != nil {
		return p.Getenv(name)
	}
	return os.Getenv(name)
}

// JevKeyAbsent is true only when loop.mjs resolveJevKey would return null
// without asking 1Password: the env variable is empty, the key store holds no
// entry (the file backend; with the default backends also the keychain), and
// the config names no 1Password reference.
func JevKeyAbsent(cfg map[string]any, p JevProbe) bool {
	var ref any
	if s1, ok := cfg["system1"].(map[string]any); ok {
		ref = s1["key"]
	}
	if ref == nil {
		if keys, ok := cfg["keys"].(map[string]any); ok {
			ref = keys["jev"]
		}
	}
	envName, storeName := "CODE_FORGE_KEY_JEV", "jev"
	if s, ok := ref.(string); ok {
		switch {
		case strings.HasPrefix(s, "env:"):
			envName = s[4:]
		case strings.HasPrefix(s, "keychain:"):
			storeName = s[9:]
			if !keyName.MatchString(storeName) {
				return false // Node throws at boot
			}
		case strings.HasPrefix(s, "op://"):
			return false // 1Password may resolve it
		}
	}
	if p.getenv(envName) != "" {
		return false
	}
	home := p.getenv("HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		home = h
	}
	if _, err := os.Lstat(filepath.Join(home, ".code-forge", "store", storeName+".key")); !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	if p.getenv("CODE_FORGE_KEY_BACKEND") == "file" {
		return true
	}
	keychain := p.Keychain
	if keychain == nil {
		keychain = securityKeychain
	}
	found, decided := keychain("code-forge", storeName)
	return decided && !found
}

// securityKeychain asks the macOS `security` tool for the item's attributes
// only; exit 44 is "not found".
func securityKeychain(service, account string) (found, decided bool) {
	if runtime.GOOS != "darwin" {
		return false, false
	}
	cmd := exec.Command("/usr/bin/security", "find-generic-password", "-s", service, "-a", account)
	err := cmd.Run()
	if err == nil {
		return true, true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 44 {
		return false, true
	}
	return false, false
}
