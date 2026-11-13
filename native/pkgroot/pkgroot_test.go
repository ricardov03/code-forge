package pkgroot

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o755); err != nil {
		t.Fatal(err)
	}
}

const mainManifest = "{\n  \"name\": \"@codedology/code-forge\",\n  \"version\": \"1.2.3\"\n}\n"

func tmp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFindAndFromExecutableNested(t *testing.T) {
	lib := tmp(t)
	root := filepath.Join(lib, "node_modules", "@codedology", "code-forge")
	write(t, filepath.Join(root, "package.json"), mainManifest)
	bin := filepath.Join(root, "node_modules", "@codedology", "code-forge-linux-x64", "bin", BinaryName)
	write(t, bin, "x")
	got, ok := Find(root, "linux-x64")
	if !ok || got != bin {
		t.Fatalf("Find nested = %q %v, want %q", got, ok, bin)
	}
	if r := FromExecutable(bin); r != root {
		t.Fatalf("FromExecutable nested = %q, want %q", r, root)
	}
}

func TestFindAndFromExecutableSibling(t *testing.T) {
	lib := tmp(t)
	root := filepath.Join(lib, "node_modules", "@codedology", "code-forge")
	write(t, filepath.Join(root, "package.json"), mainManifest)
	write(t, filepath.Join(lib, "package.json"), "{\"name\":\"someone-else\"}\n")
	bin := filepath.Join(lib, "node_modules", "@codedology", "code-forge-darwin-arm64", "bin", BinaryName)
	write(t, bin, "x")
	got, ok := Find(root, "darwin-arm64")
	if !ok || got != bin {
		t.Fatalf("Find sibling = %q %v, want %q", got, ok, bin)
	}
	if r := FromExecutable(bin); r != root {
		t.Fatalf("FromExecutable sibling = %q, want %q", r, root)
	}
}

func TestFindNestedBeatsSiblingAndDev(t *testing.T) {
	lib := tmp(t)
	root := filepath.Join(lib, "node_modules", "@codedology", "code-forge")
	write(t, filepath.Join(root, "package.json"), mainManifest)
	nested := filepath.Join(root, "node_modules", "@codedology", "code-forge-linux-arm64", "bin", BinaryName)
	for _, p := range []string{nested,
		filepath.Join(lib, "node_modules", "@codedology", "code-forge-linux-arm64", "bin", BinaryName),
		filepath.Join(root, "native", "bin", "linux-arm64", BinaryName)} {
		write(t, p, "x")
	}
	if got, ok := Find(root, "linux-arm64"); !ok || got != nested {
		t.Fatalf("Find = %q %v, want nested %q", got, ok, nested)
	}
}

func TestFindDevBuildAndSourceCheckout(t *testing.T) {
	root := tmp(t)
	write(t, filepath.Join(root, "package.json"), mainManifest)
	dev := filepath.Join(root, "native", "bin", "darwin-x64", BinaryName)
	write(t, dev, "x")
	if got, ok := Find(root, "darwin-x64"); !ok || got != dev {
		t.Fatalf("Find dev = %q %v, want %q", got, ok, dev)
	}
	if r := FromExecutable(dev); r != root {
		t.Fatalf("FromExecutable dev = %q, want %q", r, root)
	}
	// A source checkout uses its own build only, even when npm put a published one in node_modules.
	write(t, filepath.Join(root, "native", "go.mod"), "module x\n")
	write(t, filepath.Join(root, "node_modules", "@codedology", "code-forge-darwin-x64", "bin", BinaryName), "x")
	if got, ok := Find(root, "darwin-x64"); !ok || got != dev {
		t.Fatalf("Find source checkout = %q %v, want %q", got, ok, dev)
	}
	if err := os.Remove(dev); err != nil {
		t.Fatal(err)
	}
	if got, ok := Find(root, "darwin-x64"); ok || got != dev {
		t.Fatalf("Find source checkout, no build = %q %v, want %q false", got, ok, dev)
	}
}

func TestFindNoneNamesNestedPath(t *testing.T) {
	root := tmp(t)
	want := filepath.Join(root, "node_modules", "@codedology", "code-forge-linux-x64", "bin", BinaryName)
	if got, ok := Find(root, "linux-x64"); ok || got != want {
		t.Fatalf("Find none = %q %v, want %q false", got, ok, want)
	}
}

func TestFromExecutableForeignPlatformFolderFallsBack(t *testing.T) {
	lib := tmp(t)
	// A code-forge-<target> folder with no main package beside or above: the old rule applies.
	bin := filepath.Join(lib, "node_modules", "@codedology", "code-forge-linux-x64", "bin", BinaryName)
	write(t, bin, "x")
	want := filepath.Join(lib, "node_modules")
	if r := FromExecutable(bin); r != want {
		t.Fatalf("FromExecutable = %q, want %q", r, want)
	}
}
