package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ricardov03/code-forge/native/gitx"
)

// MergeGateConfig keeps optional preparation separate from the suite commands.
// Empty slices are the defaults; loading never inserts defaults into the map.
type MergeGateConfig struct {
	Links []string
	Setup []string
}

func MergeGateSettings(cfg map[string]any) (MergeGateConfig, error) {
	out := MergeGateConfig{Links: []string{}, Setup: []string{}}
	gates, _ := cfg["gates"].(map[string]any)
	for _, field := range []struct {
		key string
		dst *[]string
	}{{"merge_gate_links", &out.Links}, {"merge_gate_setup", &out.Setup}} {
		value, exists := gates[field.key]
		if !exists || value == nil && field.key == "merge_gate_setup" {
			continue
		}
		items, ok := value.([]any)
		if !ok {
			return out, fmt.Errorf("gates.%s must be an argv/path array", field.key)
		}
		for _, item := range items {
			text, ok := item.(string)
			if !ok || text == "" || strings.ContainsRune(text, 0) {
				return out, fmt.Errorf("gates.%s must contain nonempty strings", field.key)
			}
			*field.dst = append(*field.dst, text)
		}
		if field.key == "merge_gate_setup" && len(items) == 0 {
			return out, fmt.Errorf("gates.merge_gate_setup must contain an executable")
		}
	}
	return out, nil
}

// MergeGatePath resolves existing ancestors as well as the leaf: a missing file
// beneath an escaping symlink is still outside the repository.
func MergeGatePath(root, name string) (string, error) {
	refuse := func() (string, error) {
		return "", fmt.Errorf("gates.merge_gate_links paths must stay inside the repository (no absolute paths or ..)")
	}
	if name == "" || filepath.IsAbs(name) || strings.ContainsAny(name, "\\\x00") {
		return refuse()
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." || part == ".git" || strings.Contains(part, ":") {
			return refuse()
		}
	}
	if filepath.Clean(name) == "." {
		return refuse()
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(root, name)
	ancestor := path
	var tail []string
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		tail = append(tail, filepath.Base(ancestor))
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return refuse()
		}
		ancestor = parent
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", fmt.Errorf("gates.merge_gate_links cannot resolve path")
	}
	for i := len(tail) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, tail[i])
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return refuse()
	}
	return resolved, nil
}

// MergeGateSource is the primary checkout, even when loading a linked or
// detached worktree's config. Only the primary checkout supplies local files.
func MergeGateSource(cwd string) (string, error) {
	out, err := gitx.Run(cwd, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return "", fmt.Errorf("gates.merge_gate_links requires a git repository")
	}
	first, _, _ := strings.Cut(out, "\x00")
	if !strings.HasPrefix(first, "worktree ") {
		return "", fmt.Errorf("gates.merge_gate_links cannot resolve main checkout")
	}
	return filepath.EvalSymlinks(strings.TrimPrefix(first, "worktree "))
}

func ValidateMergeGateLinks(cwd string, settings MergeGateConfig) error {
	if len(settings.Links) == 0 {
		return nil
	}
	source, err := MergeGateSource(cwd)
	if err != nil {
		return err
	}
	top, err := gitx.RepoTop(cwd)
	if err != nil {
		return err
	}
	for _, name := range settings.Links {
		if _, err := MergeGatePath(source, name); err != nil {
			return err
		}
		for _, root := range []string{source, top} {
			tracked, err := gitx.Run(root, "ls-files", "-z", "--", ":(literal)"+name)
			if err != nil {
				return fmt.Errorf("gates.merge_gate_links cannot check tracked paths")
			}
			if tracked != "" {
				return fmt.Errorf("gates.merge_gate_links must not include tracked paths; they need no link")
			}
		}
	}
	return nil
}
