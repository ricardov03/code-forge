package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
)

// contextFixture is a HOME with one run whose workspace is a git repository,
// and a fake install (bin/code-forge.mjs, package.json, a node file).
type contextFixture struct {
	t                          *testing.T
	home, workspace, tmp, node string
}

func newContextFixture(t *testing.T, blocks map[string]any, config string) *contextFixture {
	t.Helper()
	base := t.TempDir()
	base, _ = filepath.EvalSymlinks(base)
	f := &contextFixture{t: t, home: filepath.Join(base, "home"), workspace: filepath.Join(base, "repo"), tmp: filepath.Join(base, "tmp")}
	for _, dir := range []string{filepath.Join(f.home, ".code-forge", "runs"), f.workspace, filepath.Join(base, "pkg", "bin"), filepath.Join(base, "pkg", "native", "bin", "x"), filepath.Join(base, "nodebin"), f.tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	git := exec.Command("git", "init", "-q", f.workspace)
	git.Env = gitx.ChildEnv()
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git init: %s: %v", out, err)
	}
	write := func(file, text string, mode os.FileMode) {
		if err := os.WriteFile(file, []byte(text), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(f.workspace, ".code-forge.yml"), config, 0o600)
	record, _ := json.Marshal(map[string]any{"run_id": "r-ctx", "workspace": f.workspace, "project": "ctx", "status": "running", "blocks": blocks})
	write(filepath.Join(f.home, ".code-forge", "runs", "r-ctx.json"), string(record), 0o600)
	write(filepath.Join(f.home, ".code-forge", "runs", "r-ctx.key"), strings.Repeat("k", 32), 0o600)
	write(filepath.Join(base, "pkg", "bin", "code-forge.mjs"), "", 0o600)
	write(filepath.Join(base, "pkg", "package.json"), `{"version":"9.8.7"}`, 0o600)
	f.node = filepath.Join(base, "nodebin", "node")
	write(f.node, "\x7fELF", 0o700)
	executable := filepath.Join(base, "pkg", "native", "bin", "x", "code-forge-native")
	write(executable, "", 0o700)
	previous := contextExecutable
	contextExecutable = func() (string, error) { return executable, nil }
	t.Cleanup(func() { contextExecutable = previous })
	t.Setenv("HOME", f.home)
	t.Setenv("CODE_FORGE_NODE", f.node)
	t.Setenv("CODE_FORGE_TMP_ROOT", f.tmp)
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "1")
	return f
}

// runCaptured runs the public route in-process. A handover would exec Node;
// with CODE_FORGE_NODE naming no executable it returns an error instead.
func runCaptured(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	dir := t.TempDir()
	files := [2]*os.File{}
	for i := range files {
		file, err := os.CreateTemp(dir, "out-")
		if err != nil {
			t.Fatal(err)
		}
		files[i] = file
	}
	out, errOut := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = files[0], files[1]
	code, err := run(args)
	os.Stdout, os.Stderr = out, errOut
	if err != nil {
		t.Fatalf("%v: handed over (code %d): %v", args, code, err)
	}
	texts := [2]string{}
	for i, file := range files {
		_ = file.Close()
		data, _ := os.ReadFile(file.Name())
		texts[i] = string(data)
	}
	return code, texts[0], texts[1]
}

func TestB452ContextHelpAndUsageInGo(t *testing.T) {
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b452-node")
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{{"context", "--help"}, {"context", "-h"}, {"context", "pack", "--help"}, {"context", "pack", "-h", "--run"}} {
		code, out, errText := runCaptured(t, args...)
		if code != 0 || out != helpExpect(t, args, 0, contextUsage) || errText != "" {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q", args, code, out, errText)
		}
	}
	cases := []struct {
		args []string
		err  string
	}{
		{[]string{"context"}, contextUsage},
		{[]string{"context", "nope", "--help"}, contextUsage},
		{[]string{"context", "pack", "--run", "r", "--block", "B1", "--bogus"}, "context: unknown flag \"--bogus\"\n" + contextUsage},
		{[]string{"context", "pack", "--bogus", "--run", "r", "--run", "r"}, "context: unknown flag \"--bogus\"\n" + contextUsage},
		{[]string{"context", "pack", "--run", "r", "--run", "r", "--bogus"}, "context: --run given more than once\n" + contextUsage},
		{[]string{"context", "pack", "--"}, "context: unknown flag \"--\"\n" + contextUsage},
		{[]string{"context", "pack", "--run", "r", "--block", "B1\n"}, "context: --block: \"B1\\n\" is not a block id\n" + contextUsage},
	}
	for _, c := range cases {
		code, out, errText := runCaptured(t, c.args...)
		if code != 2 || out != "" || errText != c.err {
			t.Fatalf("%v: code=%d stdout=%q stderr=%q, want %q", c.args, code, out, errText, c.err)
		}
	}
}

func TestB452ContextLogsBreadcrumbAndFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODE_FORGE_NODE", "/nonexistent-b452-node")
	t.Setenv("CODE_FORGE_NO_ERROR_LOG", "")
	t.Setenv("CODE_FORGE_TMP_ROOT", "")
	here, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(here) })
	code, _, errText := runCaptured(t, "context", "pack", "--run", "r-missing", "--block", "B1", "--json")
	if code != 1 || errText != "context: no run record for r-missing; nothing was written\n" {
		t.Fatalf("code=%d stderr=%q", code, errText)
	}
	crumbs, err := os.ReadFile(filepath.Join(home, ".code-forge", "logs", "breadcrumbs.json"))
	if err != nil || string(crumbs) != "[\"context pack\"]\n" {
		t.Fatalf("breadcrumbs=%q err=%v", crumbs, err)
	}
	file, err := os.Open(filepath.Join(home, ".code-forge", "logs", "errors.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		t.Fatal("no error entry")
	}
	var entry map[string]any
	if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
		t.Fatal(err)
	}
	flags, _ := json.Marshal(entry["flags"])
	if entry["verb"] != "context" || entry["sub"] != "pack" || entry["exit"] != float64(1) || entry["kind"] != "error" || string(flags) != `["--run","--block","--json"]` || entry["message"] != "context: no run record for r-missing; nothing was written" {
		t.Fatalf("entry=%s", scanner.Bytes())
	}
}

func TestB452ContextPackMakesMissingRunRoot(t *testing.T) {
	config := "version: 1\ncontext:\n  rules: [RULES.md]\n  commands:\n    test: [go, test]\n"
	f := newContextFixture(t, map[string]any{"B1": map[string]any{"status": "open", "level": "L1", "owned_files": []any{"a.go", 7.0}, "acceptance": []any{map[string]any{"clause": "works", "tests": []any{"T1", nil}}}}}, config)
	// Invalid UTF-8 reads as Node's WHATWG decoder: one U+FFFD per maximal subpart.
	if err := os.WriteFile(filepath.Join(f.workspace, "RULES.md"), []byte("rule \xe2\x82 one \xff\xfe two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errText := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "B1")
	if code != 0 || errText != "" {
		t.Fatalf("code=%d stderr=%q", code, errText)
	}
	for _, want := range []string{"level: L1\n", "- a.go\n- 7\n", "run code-forge verbs with exactly: " + f.node, "version: 9.8.7\n", "test: go test\n", "- works\n  tests: T1, \n", "rule \ufffd one \ufffd\ufffd two\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pack lacks %q:\n%s", want, out)
		}
	}
	root := filepath.Join(f.tmp, "r-ctx")
	st, err := os.Stat(root)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("run root: %v %v", st, err)
	}
	var owner map[string]any
	data, err := os.ReadFile(filepath.Join(root, "owner.json"))
	if err != nil || json.Unmarshal(data, &owner) != nil || owner["pid"] != float64(os.Getpid()) || owner["start_time"] == "" || owner["created_at"] == "" {
		t.Fatalf("owner.json=%s err=%v", data, err)
	}
	pack, err := os.ReadFile(filepath.Join(root, "context", "B1.md"))
	if err != nil || string(pack) != out {
		t.Fatalf("pack file differs: %v", err)
	}
	// The same pack again appends no second row and prints the same path.
	code, again, _ := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "B1", "--path-only")
	if code != 0 || !strings.HasPrefix(again, filepath.Join(root, "context", "B1.md")+"\ntokens ~") {
		t.Fatalf("path-only: code=%d out=%q", code, again)
	}
	ledgerDir := filepath.Join(f.home, ".code-forge", "ledger")
	rows := 0
	_ = filepath.Walk(ledgerDir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			data, _ := os.ReadFile(p)
			rows += strings.Count(string(data), `"event":"context.pack"`)
		}
		return nil
	})
	if rows != 1 {
		t.Fatalf("context.pack rows=%d, want 1", rows)
	}
}

func TestB452ContextFormerHandoverRefusals(t *testing.T) {
	t.Run("truthy non-mapping entry packs as an empty block", func(t *testing.T) {
		newContextFixture(t, map[string]any{"B1": "yes"}, "version: 1\n")
		code, out, errText := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "B1")
		if code != 0 || errText != "" || !strings.Contains(out, "level: (unknown)\nowned files:\n## code-forge\n") {
			t.Fatalf("code=%d stderr=%q out=%q", code, errText, out)
		}
	})
	t.Run("inherited name is no block", func(t *testing.T) {
		newContextFixture(t, map[string]any{"B1": map[string]any{}}, "version: 1\n")
		code, _, errText := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "constructor")
		if code != 1 || errText != "context: run r-ctx has no block constructor; nothing was written\n" {
			t.Fatalf("code=%d stderr=%q", code, errText)
		}
	})
	t.Run("first unknown command key in file order", func(t *testing.T) {
		newContextFixture(t, map[string]any{"B1": map[string]any{}}, "version: 1\ncontext:\n  commands:\n    zeta: [a]\n    alpha: [b]\n")
		code, _, errText := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "B1")
		if code != 1 || errText != "context: context.commands.zeta is not one of build, test, lint, format; nothing was written\n" {
			t.Fatalf("code=%d stderr=%q", code, errText)
		}
	})
	t.Run("config parse error is refused in Go", func(t *testing.T) {
		newContextFixture(t, map[string]any{"B1": map[string]any{}}, "version: [1\n")
		code, _, errText := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "B1")
		if code != 1 || !strings.HasPrefix(errText, "context: ") || !strings.HasSuffix(errText, "; nothing was written\n") {
			t.Fatalf("code=%d stderr=%q", code, errText)
		}
	})
	t.Run("acceptance not a list", func(t *testing.T) {
		newContextFixture(t, map[string]any{"B1": map[string]any{"acceptance": "x"}}, "version: 1\n")
		code, _, errText := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "B1")
		if code != 1 || errText != "context: block B1: acceptance must be a list of clauses; nothing was written\n" {
			t.Fatalf("code=%d stderr=%q", code, errText)
		}
	})
	t.Run("untrusted run root", func(t *testing.T) {
		f := newContextFixture(t, map[string]any{"B1": map[string]any{}}, "version: 1\n")
		root := filepath.Join(f.tmp, "r-ctx")
		if err := os.Mkdir(root, 0o777); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(root, 0o777)
		code, _, errText := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "B1")
		want := "context: the run root cannot be used (tmp: refusing untrusted run root (group/other writable): " + root + "); nothing was written\n"
		if code != 1 || errText != want {
			t.Fatalf("code=%d stderr=%q want %q", code, errText, want)
		}
	})
	t.Run("pack folder parent is a file", func(t *testing.T) {
		f := newContextFixture(t, map[string]any{"B1": map[string]any{}}, "version: 1\n")
		root := filepath.Join(f.tmp, "r-ctx")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "context"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		code, _, errText := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "B1")
		want := "context: cannot write " + filepath.Join(root, "context", "B1.md") + " (ENOTDIR); nothing was written\n"
		if code != 1 || errText != want {
			t.Fatalf("code=%d stderr=%q want %q", code, errText, want)
		}
	})
	// pathWith returns a PATH folder holding git and, when node is set, a
	// node file, so the lookup sees exactly these tools.
	pathWith := func(t *testing.T, node string) string {
		dir := t.TempDir()
		git, err := exec.LookPath("git")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(git, filepath.Join(dir, "git")); err != nil {
			t.Fatal(err)
		}
		if node != "" {
			if err := os.Symlink(node, filepath.Join(dir, "node")); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	// B572: no node anywhere is no longer a refusal; context_b572_test.go
	// checks the pack that names the install's bin/code-forge instead.
	// B469: a CODE_FORGE_NODE that names no executable (the /nonexistent
	// speed-proof setting) falls back to node on PATH, as before B452.
	t.Run("an unresolvable CODE_FORGE_NODE uses node on PATH", func(t *testing.T) {
		f := newContextFixture(t, map[string]any{"B1": map[string]any{}}, "version: 1\n")
		t.Setenv("CODE_FORGE_NODE", "/nonexistent-b452-node")
		t.Setenv("PATH", pathWith(t, f.node))
		code, out, errText := runCaptured(t, "context", "pack", "--run", "r-ctx", "--block", "B1")
		if code != 0 || errText != "" || !strings.Contains(out, f.node) {
			t.Fatalf("code=%d stdout=%q stderr=%q, want a pack naming %s", code, out, errText, f.node)
		}
	})
}
