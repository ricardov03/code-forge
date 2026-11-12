// Package config loads project configuration using the same boundaries and YAML
// core schema as src/config/load.mjs. Loading and validation are separate steps.
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const Filename = ".code-forge.yml"

// Error carries the Node loader's error kind and safe, excerpt-free message.
type Error struct {
	Kind    string
	Message string
	Path    string
}

func (e *Error) Error() string { return e.Message }

func failure(kind, message, path string) error {
	return &Error{Kind: kind, Message: message, Path: path}
}

// Load reads the project root's configuration, including from nested worktrees.
// Like loadProjectConfig, it migrates version but does not insert provider or
// schema defaults, validate, or refresh the user's model cache.
func Load(cwd string) (map[string]any, error) {
	return LoadFile(filepath.Join(ProjectRootFor(cwd), Filename))
}

// LoadFile parses a particular file without searching for a project root.
// LoadUnchecked is Load without the merge-gate settings check: the parse,
// root and version steps of loadProjectConfig only, which makes no such check.
func LoadUnchecked(cwd string) (map[string]any, error) {
	mapping, _, err := LoadOrdered(cwd)
	return mapping, err
}

func LoadFile(path string) (map[string]any, error) {
	mapping, _, err := LoadFileOrdered(path)
	if err != nil {
		return nil, err
	}
	settings, err := MergeGateSettings(mapping)
	if err == nil {
		err = ValidateMergeGateLinks(filepath.Dir(path), settings)
	}
	if err != nil {
		return nil, failure("schema", err.Error(), path)
	}
	return mapping, nil
}

// LoadOrdered is loadProjectConfig: the project root's config, parsed and
// migrated only, plus the YAML key order of every mapping.
func LoadOrdered(cwd string) (map[string]any, Ordered, error) {
	return LoadFileOrdered(filepath.Join(ProjectRootFor(cwd), Filename))
}

// LoadFileOrdered is loadConfigFile in src/config/load.mjs: parse and migrate
// only (no merge-gate checks), plus the YAML key order of every mapping, so a
// caller can print config values in Node's JSON.stringify property order.
func LoadFileOrdered(path string) (map[string]any, Ordered, error) {
	none := Ordered{}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, none, failure("not-found", "no config file at "+path, path)
		}
		return nil, none, failure("read-error", "could not read "+path+": "+errno(err), path)
	}
	value, order, err := parseYAMLOrdered(string(raw))
	if err != nil {
		return nil, none, failure("parse-error", err.Error(), path)
	}
	mapping, ok := value.(map[string]any)
	if !ok {
		kind := jsType(value)
		if value == nil {
			kind = "an empty document"
		} else if _, ok := value.([]any); ok {
			kind = "a list"
		}
		return nil, none, failure("parse-error", "config root must be a mapping, got "+kind, path)
	}
	version := mapping["version"]
	if version == nil {
		version = float64(1)
	}
	n, numeric := version.(float64)
	if !numeric || math.IsNaN(n) || math.IsInf(n, 0) || math.Trunc(n) != n || n < 1 {
		kind := jsType(mapping["version"])
		if _, ok := version.([]any); ok {
			kind = "array"
		}
		return nil, none, failure("migrate-error", "migrateConfig: version must be a positive integer, got a "+kind, path)
	}
	if n > 1 {
		return nil, none, failure("migrate-error", fmt.Sprintf("migrateConfig: config version %s is newer than this code-forge understands (1) — upgrade the package", jsKey(n)), path)
	}
	mapping["version"] = float64(1)
	jsonNumbers(mapping)
	return mapping, Ordered{order: order}, nil
}

func jsType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case string:
		return "string"
	case bool:
		return "boolean"
	case float64:
		return "number"
	default:
		return "object"
	}
}

func errno(err error) string {
	for _, code := range []struct {
		err  error
		name string
	}{{syscall.EISDIR, "EISDIR"}, {syscall.EACCES, "EACCES"}, {syscall.EPERM, "EPERM"}, {syscall.ENOTDIR, "ENOTDIR"}, {syscall.ELOOP, "ELOOP"}, {syscall.EMFILE, "EMFILE"}, {syscall.ENAMETOOLONG, "ENAMETOOLONG"}} {
		if errors.Is(err, code.err) {
			return code.name
		}
	}
	return err.Error()
}

func realOrResolved(path string) string {
	if path == "" {
		path, _ = os.Getwd()
	}
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	abs, _ := filepath.Abs(path)
	return abs
}

// ProjectRootFor retains the caller's symlink spelling when possible. Configs
// above git, above HOME, or above a non-HOME start never capture a project.
func ProjectRootFor(start string) string {
	from := realOrResolved(start)
	home, _ := os.UserHomeDir()
	home = realOrResolved(home)
	git := ""
	for dir := from; ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			git = dir
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	root, stop := from, ""
	if git != "" && git != home {
		root, stop = git, git
	} else if from == home || strings.HasPrefix(from, home+string(filepath.Separator)) {
		stop = home
	}
	if stop != "" {
		for dir := from; ; dir = filepath.Dir(dir) {
			if stat, err := os.Stat(filepath.Join(dir, Filename)); err == nil && stat.Mode().IsRegular() {
				root = dir
				break
			}
			if dir == stop || filepath.Dir(dir) == dir {
				break
			}
		}
	}
	if start == "" {
		start, _ = os.Getwd()
	}
	spelling, _ := filepath.Abs(start)
	for dir := spelling; ; dir = filepath.Dir(dir) {
		if realOrResolved(dir) == root {
			return dir
		}
		if filepath.Dir(dir) == dir {
			return root
		}
	}
}
