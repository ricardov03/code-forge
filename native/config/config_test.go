package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/canon"
)

type configCase struct {
	Name      string
	Group     string
	Files     map[string]string
	Dirs      []string
	Links     map[string]string
	Cwd       string
	Home      string
	Root      string
	Operation string
	Provider  string
	Expected  struct {
		OK        bool
		Path      string
		Canonical string
		Error     string
		Message   string
	}
}

func cases(t *testing.T, group string) []configCase {
	t.Helper()
	files, err := filepath.Glob("../../test/compat/golden/config/*.json")
	if err != nil || len(files) < 4 {
		t.Fatalf("expected at least four config golden groups, got %d: %v", len(files), err)
	}
	var out []configCase
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var entries []configCase
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, c := range entries {
			if c.Group == group {
				out = append(out, c)
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("no golden cases in group %s", group)
	}
	return out
}

func fixture(t *testing.T, c configCase) (string, string) {
	t.Helper()
	root := t.TempDir()
	for _, dir := range append(c.Dirs, c.Home) {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range c.Files {
		file := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range c.Links {
		file := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, target), file); err != nil {
			t.Fatal(err)
		}
	}
	cwd := filepath.Join(root, c.Cwd)
	if err := os.MkdirAll(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, c.Home))
	return root, cwd
}

func checkLoad(t *testing.T, c configCase, root, cwd string) {
	t.Helper()
	var value any
	var err error
	switch c.Operation {
	case "defaults":
		value = DefaultsForProvider(c.Provider)
	case "schema":
		data, readErr := os.ReadFile("../../package.json")
		if readErr != nil {
			t.Fatal(readErr)
		}
		var pkg struct{ Version string }
		if err := json.Unmarshal(data, &pkg); err != nil {
			t.Fatal(err)
		}
		value, err = LoadValidated(cwd, pkg.Version)
	case "refresh":
		home := filepath.Join(root, c.Home)
		result, refreshErr := RefreshFromCLICaches(home)
		err = refreshErr
		if err == nil {
			data, readErr := os.ReadFile(UserModelsCachePath(home))
			if readErr != nil {
				t.Fatal(readErr)
			}
			var cache map[string]any
			if err := json.Unmarshal(data, &cache); err != nil {
				t.Fatal(err)
			}
			if stamp, ok := cache["updated_at"].(string); !ok || stamp == "" {
				t.Fatal("refresh timestamp must be a nonempty string")
			}
			delete(cache, "updated_at")
			value = map[string]any{"result": result, "cache": cache, "seen": LoadSeenInCache(home)}
		}
	default:
		value, err = Load(cwd)
	}
	normalize := func(text string) string { return strings.ReplaceAll(text, root, "$ROOT") }
	if c.Expected.OK {
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		data, err := canon.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if normalize(string(data)) != c.Expected.Canonical {
			t.Fatalf("canonical config got %s\nwant %s", data, c.Expected.Canonical)
		}
		if got := normalize(filepath.Join(ProjectRootFor(cwd), Filename)); c.Expected.Path != "" && got != c.Expected.Path {
			t.Fatalf("config path got %s want %s", got, c.Expected.Path)
		}
		return
	}
	var loadErr *Error
	if !errors.As(err, &loadErr) {
		t.Fatalf("expected loader Error %s, got %v (config %v)", c.Expected.Error, err, value)
	}
	if loadErr.Kind != c.Expected.Error || normalize(loadErr.Message) != c.Expected.Message || normalize(loadErr.Path) != c.Expected.Path {
		t.Fatalf("error got kind=%q message=%q path=%q\nwant kind=%q message=%q path=%q", loadErr.Kind, normalize(loadErr.Message), normalize(loadErr.Path), c.Expected.Error, c.Expected.Message, c.Expected.Path)
	}
}

func TestConfigValid(t *testing.T) {
	for _, c := range cases(t, "valid") {
		t.Run(c.Name, func(t *testing.T) {
			root, cwd := fixture(t, c)
			checkLoad(t, c, root, cwd)
		})
	}
}

func TestConfigErrors(t *testing.T) {
	for _, c := range cases(t, "error") {
		t.Run(c.Name, func(t *testing.T) {
			root, cwd := fixture(t, c)
			checkLoad(t, c, root, cwd)
		})
	}
}

func TestConfigRoots(t *testing.T) {
	for _, c := range cases(t, "root") {
		t.Run(c.Name, func(t *testing.T) {
			root, cwd := fixture(t, c)
			got := strings.ReplaceAll(ProjectRootFor(cwd), root, "$ROOT")
			if got != c.Root {
				t.Fatalf("project root got %s want %s", got, c.Root)
			}
			checkLoad(t, c, root, cwd)
		})
	}
}
