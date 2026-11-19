package gatecheck

import (
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/ricardov03/code-forge/native/config"
	"github.com/ricardov03/code-forge/native/gitx"
)

// ErrHighPathsHandOver is HighPaths' answer when the base commit itself
// cannot be read (no base, or git cannot resolve or show it). No config
// shape reaches it.
var ErrHighPathsHandOver = errors.New("high paths: base commit unreadable")

// ConfigRefusal is a config HighPaths will not read: it does not load, it
// fails the shared schema, or it holds what looks like a raw secret (B781).
// Message names the file side, the key path and what to change; it never
// holds the secret itself.
type ConfigRefusal struct{ Message string }

func (e *ConfigRefusal) Error() string { return e.Message }

// HighPaths reads proof.tiers.high.paths from both sides: the workspace's
// .code-forge.yml and the one committed at base. Each side must load and pass
// the shared schema check, and must hold no raw secret; nothing else about
// the config matters here (B781: keys, review.routes, review.second_* and the
// other former Node-only settings no longer stop a close).
func HighPaths(workspace, base, version string) ([]any, error) {
	if base == "" || strings.HasPrefix(base, "-") {
		return nil, ErrHighPathsHandOver
	}
	paths := []any{}
	current, err := config.Load(workspace)
	if err == nil {
		if refusal := hpCheck(current, version, ".code-forge.yml", "fix the file"); refusal != nil {
			return nil, refusal
		}
		paths = append(paths, highPaths(current)...)
	} else if e, ok := err.(*config.Error); !ok || e.Kind != "not-found" {
		return nil, &ConfigRefusal{Message: ".code-forge.yml cannot be read: " + config.MaskSecrets(err.Error()) + "; fix the file and close again; nothing was recorded"}
	}
	if _, err := gitx.Run(workspace, "rev-parse", "--verify", "--quiet", base+"^{commit}"); err != nil {
		return nil, ErrHighPathsHandOver
	}
	listed, err := gitx.Run(workspace, "ls-tree", base, "--", ".code-forge.yml")
	if err != nil {
		return nil, ErrHighPathsHandOver
	}
	if strings.TrimSpace(listed) != "" {
		shown, err := gitx.Run(workspace, "show", base+":.code-forge.yml")
		if err != nil {
			return nil, ErrHighPathsHandOver
		}
		side := ".code-forge.yml at the block's base " + hpShort(base)
		fix := "commit the fix on main and rebase the block (code-forge block rebase)"
		baseConfig, _, err := config.ParseTextOrdered(shown, side)
		if err != nil {
			return nil, &ConfigRefusal{Message: side + " cannot be read: " + config.MaskSecrets(err.Error()) + "; " + fix + ", then close again; nothing was recorded"}
		}
		if refusal := hpCheck(baseConfig, version, side, fix); refusal != nil {
			return nil, refusal
		}
		for _, p := range highPaths(baseConfig) {
			if !slices.Contains(paths, p) {
				paths = append(paths, p)
			}
		}
	}
	return paths, nil
}
func highPaths(cfg map[string]any) []any {
	return array(mapping(mapping(mapping(cfg["proof"])["tiers"])["high"])["paths"])
}
func mapping(v any) map[string]any { m, _ := v.(map[string]any); return m }

func hpShort(base string) string {
	if len(base) > 12 {
		return base[:12]
	}
	return base
}

// hpCheck is the only domain check HighPaths keeps: the shared schema, then
// the secret scan.
func hpCheck(cfg map[string]any, version, side, fix string) error {
	if err := config.ValidateSchema(cfg, version); err != nil {
		return &ConfigRefusal{Message: side + " fails the config schema: " + config.MaskSecrets(strings.ReplaceAll(err.Error(), "\n", "; ")) + "; " + fix + " (code-forge validate shows each issue), then close again; nothing was recorded"}
	}
	if path, found := hpSecretPath(cfg, ""); found {
		return &ConfigRefusal{Message: side + " holds what looks like a raw secret at " + path + "; remove it and store the key with code-forge keys set <name> (the config may hold only an env: or op:// reference); " + fix + ", then close again; nothing was recorded"}
	}
	return nil
}

// hpSecretPath walks keys and values in a fixed (sorted) order and returns the
// dotted path of the first one config.LooksLikeSecret flags. An env: or
// op:// reference is never a secret.
func hpSecretPath(node any, path string) (string, bool) {
	switch v := node.(type) {
	case string:
		return path, config.LooksLikeSecret(v)
	case []any:
		for i, item := range v {
			if found, ok := hpSecretPath(item, path+"["+strconv.Itoa(i)+"]"); ok {
				return found, true
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := key
			if path != "" {
				child = path + "." + key
			}
			if config.LooksLikeSecret(key) {
				// The key itself is the secret: name its parent only.
				if path == "" {
					return "a top-level key", true
				}
				return "a key under " + path, true
			}
			if found, ok := hpSecretPath(v[key], child); ok {
				return found, true
			}
		}
	}
	return "", false
}
