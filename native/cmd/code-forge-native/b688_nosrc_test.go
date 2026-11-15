package main

// B688: Go reads no data file from the Node src/ tree. The lenses, the finding
// schema and the known-fixes table are built into the binary, and upgrade
// finds its package root by package.json. Each test copies this test binary
// into a temp package root with no src/ folder and runs it there, so
// os.Executable() and the package root are the temp ones.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/ledger"
	"github.com/ricardov03/code-forge/native/review"
	"github.com/ricardov03/code-forge/native/session"
)

// TestB688Child is the copied binary's body: B688_CHILD picks what it does.
func TestB688Child(t *testing.T) {
	mode := os.Getenv("B688_CHILD")
	if mode == "" {
		return
	}
	args := []string{}
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	switch mode {
	case "uproot":
		root, ok := upgradeRealPackageRoot()
		fmt.Printf("%s %v\n", root, ok)
		os.Exit(0)
	case "data":
		for _, name := range review.Lenses {
			text, err := review.LoadLens(name)
			if err != nil {
				fmt.Printf("lens %s: %v\n", name, err)
				os.Exit(1)
			}
			fmt.Printf("lens %s %d\n%s\n", name, len(text), text)
		}
		schema, err := session.LoadFindingSchema()
		if err != nil {
			fmt.Printf("schema: %v\n", err)
			os.Exit(1)
		}
		data, _ := schema.MarshalJSON()
		fmt.Printf("schema %s\n", data)
		os.Exit(0)
	case "run":
		code, err := runGuarded(args)
		if err != nil {
			fmt.Fprintln(os.Stderr, "code-forge-native:", err)
		}
		os.Exit(code)
	}
	os.Exit(3)
}

// b688Package makes <tmp>/package.json (name given) and copies this test
// binary to <tmp>/native/bin/<platform>-<arch>/code-forge-native.
func b688Package(t *testing.T, name string) (root, binary string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf("{\n  \"name\": %q,\n  \"version\": \"0.24.0\"\n}\n", name)
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	arch := map[string]string{"arm64": "arm64", "amd64": "x64"}[runtime.GOARCH]
	binary = filepath.Join(root, "native", "bin", runtime.GOOS+"-"+arch, "code-forge-native")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(binary, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	return root, binary
}

func b688Run(t *testing.T, binary, mode string, env []string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(binary, append([]string{"-test.run=^TestB688Child$", "--"}, args...)...)
	cmd.Env = append([]string{"B688_CHILD=" + mode, "PATH=/usr/bin:/bin"}, env...)
	cmd.Dir = filepath.Dir(binary)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return code, stdout.String(), stderr.String()
}

func b688NoSrc(t *testing.T, root string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, "src")); !os.IsNotExist(err) {
		t.Fatalf("package root has a src/ folder: %v", err)
	}
}

// Upgrade (and init/doctor through the same seam) finds the package root
// by package.json alone; no src/cli/upgrade.mjs is needed.
func TestB688UpgradeRootWithoutSrc(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("upgrade runs on darwin and linux only")
	}
	root, binary := b688Package(t, "@codedology/code-forge")
	b688NoSrc(t, root)
	if code, out, errText := b688Run(t, binary, "uproot", nil); code != 0 || out != root+" true\n" {
		t.Fatalf("package root = %d %q %q, want %q", code, out, errText, root+" true\n")
	}
	// Another package's manifest is not the root, src/cli/upgrade.mjs or not.
	other, otherBinary := b688Package(t, "someone-else")
	if err := os.MkdirAll(filepath.Join(other, "src", "cli"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "src", "cli", "upgrade.mjs"), []byte("\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, out, _ := b688Run(t, otherBinary, "uproot", nil); code != 0 || out != " false\n" {
		t.Fatalf("foreign package root = %d %q, want not found", code, out)
	}
}

// The upgrade verb itself runs from a package without src/: the bad
// --source refusal comes after the package root check, so it proves the
// root was found (with no root, Go hands over with upgrade-package-root).
func TestB688UpgradeVerbWithoutSrc(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("upgrade runs on darwin and linux only")
	}
	root, binary := b688Package(t, "@codedology/code-forge")
	b688NoSrc(t, root)
	home := t.TempDir()
	env := []string{"HOME=" + home, "CODE_FORGE_NODE=/nonexistent/b688-node", "CODE_FORGE_NO_ERROR_LOG=1"}
	code, out, errText := b688Run(t, binary, "run", env, "upgrade", "--plain")
	want := "upgrade: source \"" + filepath.Join(root, "skill") + "\" does not exist\n"
	if code != 1 || out != "" || errText != want {
		t.Fatalf("upgrade = %d %q %q, want 1 \"\" %q", code, out, errText, want)
	}
}

// The lenses and the finding schema load with no src/ folder and equal the
// Node copies in src/review.
func TestB688LensesAndSchemaWithoutSrc(t *testing.T) {
	root, binary := b688Package(t, "@codedology/code-forge")
	b688NoSrc(t, root)
	code, out, errText := b688Run(t, binary, "data", nil)
	if code != 0 {
		t.Fatalf("data = %d %q %q", code, out, errText)
	}
	var want strings.Builder
	for _, name := range review.Lenses {
		data, err := os.ReadFile(filepath.Join("..", "..", "..", "src", "review", "lenses", name+".md"))
		if err != nil {
			t.Fatal(err)
		}
		text := strings.TrimRight(string(data), " \t\r\n")
		fmt.Fprintf(&want, "lens %s %d\n%s\n", name, len(text), text)
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "src", "review", "finding.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := ledger.DecodeObject(data)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := schema.MarshalJSON()
	fmt.Fprintf(&want, "schema %s\n", encoded)
	if out != want.String() {
		t.Fatalf("data output differs from src/review:\n got %q\nwant %q", out, want.String())
	}
}

const b688Entry = `{"ts":"2099-01-02T03:04:05.000Z","verb":"run","sub":null,"kind":"exit","exit":2,"message":"\n  boom  \nsecond","fp":"000000000001"}` + "\n"

func b688LogsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".code-forge", "logs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "errors.jsonl"), []byte(b688Entry), 0o600); err != nil {
		t.Fatal(err)
	}
	return home
}

// `logs` lists in Go from a package with no src/ folder, and a known-fixes
// table left in an old src/util no longer sends the listing to Node: Go uses
// the table built into it.
func TestB688LogsWithoutSrcKnownFixes(t *testing.T) {
	const want = "2099-01-02T03:04:05.000Z  run  2  exit  000000000001  boom\n"
	t.Run("no src folder", func(t *testing.T) {
		root, binary := b688Package(t, "@codedology/code-forge")
		b688NoSrc(t, root)
		env := []string{"HOME=" + b688LogsHome(t), "CODE_FORGE_NODE=/nonexistent/b688-node", "CODE_FORGE_NO_ERROR_LOG=1"}
		if code, out, errText := b688Run(t, binary, "run", env, "logs"); code != 0 || out != want || errText != "" {
			t.Fatalf("logs = %d %q %q, want 0 %q", code, out, errText, want)
		}
	})
	t.Run("stale src table ignored", func(t *testing.T) {
		root, binary := b688Package(t, "@codedology/code-forge")
		dir := filepath.Join(root, "src", "util")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		table := `[{"fp":"000000000001","fixed_in":"0.16.3","summary":"A known fix","issue":null}]`
		if err := os.WriteFile(filepath.Join(dir, "known-fixes.json"), []byte(table), 0o644); err != nil {
			t.Fatal(err)
		}
		env := []string{"HOME=" + b688LogsHome(t), "CODE_FORGE_NODE=/nonexistent/b688-node", "CODE_FORGE_NO_ERROR_LOG=1"}
		if code, out, errText := b688Run(t, binary, "run", env, "logs"); code != 0 || out != want || errText != "" {
			t.Fatalf("logs = %d %q %q, want 0 %q", code, out, errText, want)
		}
	})
}

// The built-in known-fixes table equals the shipped Node copy, so Go and
// Node agree on it until Node is removed (scripts/known-fix.mjs edits the
// src/util copy; copy it to native/cmd/code-forge-native/data after).
func TestB688KnownFixesMatchesSrc(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "src", "util", "known-fixes.json"))
	if err != nil {
		t.Fatal(err)
	}
	var built, shipped any
	if err := json.Unmarshal(logsKnownFixes, &built); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &shipped); err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(built)
	b, _ := json.Marshal(shipped)
	if !bytes.Equal(a, b) {
		t.Fatalf("built-in known-fixes table %s differs from src/util/known-fixes.json %s", a, b)
	}
}
