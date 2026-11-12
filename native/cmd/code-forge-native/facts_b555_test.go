package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// B555: the facts verb's pre-run refusals run in Go. The expected stderr,
// exit code and error-log rows below are Node's, from
// `CODE_FORGE_NATIVE=0 ./bin/code-forge facts …` (Node 24) on the same
// setups; <DIR> masks the per-run temp folder, the only mask.

type fcFixture struct {
	home, tmp, proj, empty, bad, v9, vstr string
}

func fcSetup(t *testing.T) fcFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := fcFixture{home: filepath.Join(base, "home"), tmp: filepath.Join(base, "tmp"), proj: filepath.Join(base, "proj"),
		empty: filepath.Join(base, "empty"), bad: filepath.Join(base, "bad"), v9: filepath.Join(base, "v9"), vstr: filepath.Join(base, "vstr")}
	files := map[string]string{
		filepath.Join(f.proj, ".code-forge.yml"): "version: 1\nproject:\n  name: p\n  slug: p\nprovider: anthropic\n",
		filepath.Join(f.proj, "brief.md"):        "# brief\n",
		filepath.Join(f.bad, ".code-forge.yml"):  "version: [\n",
		filepath.Join(f.bad, "brief.md"):         "x\n",
		filepath.Join(f.v9, ".code-forge.yml"):   "version: 9\n",
		filepath.Join(f.v9, "b.md"):              "x\n",
		filepath.Join(f.vstr, ".code-forge.yml"): "version: \"x\"\n",
		filepath.Join(f.vstr, "b.md"):            "x\n",
	}
	for _, dir := range []string{f.home, f.tmp, f.empty, filepath.Join(f.proj, "sub"), filepath.Join(f.proj, "dirbrief")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for file, text := range files {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_TMP_ROOT", f.tmp)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b555-node")
	t.Setenv("CODE_FORGE_NATIVE", "1")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	t.Setenv("CODE_FORGE_HANDOVER_LOG", "1")
	return f
}

func fcChdir(t *testing.T, dir string) {
	t.Helper()
	here, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(here) })
}

// fcTree lists every path below dir (relative), so a test can prove a call wrote nothing.
func fcTree(t *testing.T, dir string) []string {
	t.Helper()
	paths := []string{}
	_ = filepath.Walk(dir, func(p string, _ os.FileInfo, err error) error {
		if err == nil && p != dir {
			rel, _ := filepath.Rel(dir, p)
			paths = append(paths, rel)
		}
		return nil
	})
	return paths
}

// fcNodeRows: Node's errors.jsonl row (message, cleaned, fp) for each stderr line.
var fcNodeRows = map[string]struct{ message, cleaned, fp string }{
	"facts: no config file at <DIR>/empty/.code-forge.yml\n":                                                       {"facts: no config file at <project>/.code-forge.yml", `[{"count":1,"rule":"project_path"}]`, "22269f3fe898"},
	"facts: no config file at <DIR>/proj/sub/.code-forge.yml\n":                                                    {"facts: no config file at <project>/.code-forge.yml", `[{"count":1,"rule":"project_path"}]`, "22269f3fe898"},
	"facts: migrateConfig: config version 9 is newer than this code-forge understands (1) — upgrade the package\n": {"facts: migrateConfig: config version 9 is newer than this code-forge understands (1) — upgrade the package", "[]", "aea05045ac7f"},
	"facts: migrateConfig: version must be a positive integer, got a string\n":                                     {"facts: migrateConfig: version must be a positive integer, got a string", "[]", "5ec13aff2cb3"},
	"facts: --brief cannot be read\n":                                                                              {"facts: --brief cannot be read", "[]", "29937dc76437"},
	"facts: --sources path cannot be read\n":                                                                       {"facts: --sources path cannot be read", "[]", "e5abd5d4b73a"},
	"facts: --timeout must be a positive integer\n":                                                                {"facts: --timeout must be a positive integer", "[]", "4cf80ed9ff71"},
	"facts: a run id is one path segment of letters, digits, \".\", \"_\" or \"-\"\n":                              {"facts: a run id is one path segment of letters, digits, \".\", \"_\" or \"-\"", "[]", "d86c97c8eb4f"},
}

func TestB555FactsPreRunRefusalsMatchNode(t *testing.T) {
	cases := []struct {
		name, dir string // dir: proj, sub, empty, v9, vstr
		args      []string
		stderr    string
		flags     []string
	}{
		{"no config", "empty", []string{"--brief", "x.md"}, "facts: no config file at <DIR>/empty/.code-forge.yml\n", []string{"--brief"}},
		{"config in a non-git subfolder", "sub", []string{"--brief", "../missing.md"}, "facts: no config file at <DIR>/proj/sub/.code-forge.yml\n", []string{"--brief"}},
		{"config version too new", "v9", []string{"--brief", "b.md"}, "facts: migrateConfig: config version 9 is newer than this code-forge understands (1) — upgrade the package\n", []string{"--brief"}},
		{"config version a string", "vstr", []string{"--brief", "b.md"}, "facts: migrateConfig: version must be a positive integer, got a string\n", []string{"--brief"}},
		{"missing brief", "proj", []string{"--brief", "missing.md"}, "facts: --brief cannot be read\n", []string{"--brief"}},
		{"brief is a folder", "proj", []string{"--brief", "dirbrief"}, "facts: --brief cannot be read\n", []string{"--brief"}},
		{"empty brief path", "proj", []string{"--brief", ""}, "facts: --brief cannot be read\n", []string{"--brief"}},
		{"missing source", "proj", []string{"--brief", "brief.md", "--sources", "missing"}, "facts: --sources path cannot be read\n", []string{"--brief", "--sources"}},
		{"second source missing", "proj", []string{"--brief", "brief.md", "--sources", "brief.md", "--sources", "nope"}, "facts: --sources path cannot be read\n", []string{"--brief", "--sources", "--sources"}},
		{"timeout zero", "proj", []string{"--brief", "brief.md", "--timeout", "0"}, "facts: --timeout must be a positive integer\n", []string{"--brief", "--timeout"}},
		{"timeout fraction", "proj", []string{"--brief", "brief.md", "--timeout", "1.5"}, "facts: --timeout must be a positive integer\n", []string{"--brief", "--timeout"}},
		{"run id with a slash", "proj", []string{"--brief", "brief.md", "--run", "a/b"}, "facts: a run id is one path segment of letters, digits, \".\", \"_\" or \"-\"\n", []string{"--brief", "--run"}},
		{"run id dot dot", "proj", []string{"--brief", "brief.md", "--run", ".."}, "facts: a run id is one path segment of letters, digits, \".\", \"_\" or \"-\"\n", []string{"--brief", "--run"}},
		{"brief before timeout", "proj", []string{"--brief", "missing.md", "--timeout", "0"}, "facts: --brief cannot be read\n", []string{"--brief", "--timeout"}},
		{"sources before timeout", "proj", []string{"--brief", "brief.md", "--sources", "nope", "--timeout", "0"}, "facts: --sources path cannot be read\n", []string{"--brief", "--sources", "--timeout"}},
		{"timeout before run", "proj", []string{"--brief", "brief.md", "--timeout", "0", "--run", "a/b"}, "facts: --timeout must be a positive integer\n", []string{"--brief", "--timeout", "--run"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := fcSetup(t)
			dirs := map[string]string{"proj": f.proj, "sub": filepath.Join(f.proj, "sub"), "empty": f.empty, "v9": f.v9, "vstr": f.vstr}
			fcChdir(t, dirs[c.dir])
			code, stdout, stderr := runCaptured(t, append([]string{"facts"}, c.args...)...)
			want := strings.ReplaceAll(c.stderr, "<DIR>", filepath.Dir(f.home))
			if code != 2 || stdout != "" || stderr != want {
				t.Fatalf("code=%d stdout=%q stderr=%q; want 2, \"\", %q", code, stdout, stderr, want)
			}
			crumbs, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "logs", "breadcrumbs.json"))
			if string(crumbs) != "[\"facts\"]\n" {
				t.Fatalf("breadcrumbs %q", crumbs)
			}
			data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "logs", "errors.jsonl"))
			row := map[string]any{}
			if err := json.Unmarshal(bytes.TrimSpace(data), &row); err != nil {
				t.Fatalf("errors.jsonl %q: %v", data, err)
			}
			flags, _ := json.Marshal(row["flags"])
			wantFlags, _ := json.Marshal(c.flags)
			node, ok := fcNodeRows[c.stderr]
			if !ok {
				t.Fatalf("no Node row for %q", c.stderr)
			}
			cleaned, _ := json.Marshal(row["cleaned"])
			if row["verb"] != "facts" || row["sub"] != nil || row["exit"] != float64(2) || row["kind"] != "usage" || row["message"] != node.message ||
				row["fp"] != node.fp || string(cleaned) != node.cleaned || row["stack"] != nil || string(flags) != string(wantFlags) {
				t.Fatalf("error row %v; want Node's message %q fp %s cleaned %s", row, node.message, node.fp, node.cleaned)
			}
			if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl")); err == nil {
				t.Fatal("a Go refusal wrote a hand-over row")
			}
			if left := fcTree(t, f.tmp); len(left) != 0 {
				t.Fatalf("the refusal wrote under the tmp root: %v", left)
			}
			if left := fcTree(t, filepath.Join(f.proj, "plans")); len(left) != 0 {
				t.Fatalf("the refusal wrote a sheet: %v", left)
			}
		})
	}
}

// A call that passes every pre-run check still runs buildFacts in Node, for
// every provider and level (anthropic, xai and the openai/Codex level alike):
// Go hands over before it writes anything. A YAML parse error hands over too:
// Go's parser words it differently from Node's yaml package.
func TestB555FactsSessionAndParseErrorHandOver(t *testing.T) {
	cases := []struct {
		name, dir, provider string
		args                []string
		reason              string
	}{
		{"anthropic", "proj", "anthropic", []string{"--brief", "brief.md"}, "facts-session"},
		{"xai with sources, run and timeout", "proj", "xai", []string{"--brief", "brief.md", "--sources", "brief.md", "dirbrief", "--run", "r1", "--timeout", "30", "--out", "x.md"}, "facts-session"},
		{"openai", "proj", "openai", []string{"--brief", "brief.md"}, "facts-session"},
		{"yaml parse error", "bad", "", []string{"--brief", "brief.md"}, "facts-config"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := fcSetup(t)
			if c.provider != "" {
				text := "version: 1\nproject:\n  name: p\n  slug: p\nprovider: " + c.provider + "\n"
				if err := os.WriteFile(filepath.Join(f.proj, ".code-forge.yml"), []byte(text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			fcChdir(t, map[string]string{"proj": f.proj, "bad": f.bad}[c.dir])
			if code, _ := run(append([]string{"facts"}, c.args...)); code != 127 {
				t.Fatalf("code=%d; want the Node hand-over", code)
			}
			data, _ := os.ReadFile(filepath.Join(f.home, ".code-forge", "native", "handovers.jsonl"))
			var row handoverRow
			if json.Unmarshal(bytes.TrimSpace(data), &row) != nil || row.Verb != "facts" || row.Reason != c.reason {
				t.Fatalf("handover row %q; want reason %q", data, c.reason)
			}
			if _, err := os.Stat(filepath.Join(f.home, ".code-forge", "logs", "breadcrumbs.json")); err == nil {
				t.Fatal("Go wrote a breadcrumb before the hand-over; Node writes its own")
			}
			if left := fcTree(t, f.tmp); len(left) != 0 {
				t.Fatalf("Go wrote under the tmp root before the hand-over: %v", left)
			}
			for _, sheet := range []string{filepath.Join(f.proj, "plans"), filepath.Join(f.proj, "x.md")} {
				if _, err := os.Stat(sheet); err == nil {
					t.Fatalf("Go wrote %s before the hand-over", sheet)
				}
			}
		})
	}
}
