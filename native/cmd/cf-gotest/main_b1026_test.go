package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func b1026Module(t *testing.T, files map[string]string) {
	t.Helper()
	old := nativeDir
	nativeDir = t.TempDir()
	t.Cleanup(func() { nativeDir = old })
	for name, source := range files {
		file := filepath.Join(nativeDir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestB1026FileOnlyNamesPerPackage(t *testing.T) {
	log := fakeGo(t, "0")
	t.Setenv("CODE_FORGE_TEST_RUN", "")
	t.Setenv("CODE_FORGE_RUN_ID", "")
	b1026Module(t, map[string]string{
		"one/a_test.go":     "package one\nimport \"testing\"\nfunc TestChosen(t *testing.T) {}\nfunc TestMain(m *testing.M) {}\nfunc helper() {}\n",
		"one/other_test.go": "package one\nimport \"testing\"\nfunc TestUnselected(t *testing.T) { t.Fatal(\"must not run\") }\n",
		"two/b_test.go":     "package two\nimport \"testing\"\nfunc TestOther(t *testing.T) {}\n",
	})
	var out, errb bytes.Buffer
	if code := run([]string{"native/two/b_test.go", "native/one/a_test.go", "native/one/a_test.go"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	want := "test -count=1 -timeout 75m -p 2 -run ^(TestChosen)$ -trimpath ./one\ntest -count=1 -timeout 75m -p 2 -run ^(TestOther)$ -trimpath ./two\n"
	if got := readLog(t, log); got != want {
		t.Fatalf("go ran %q, want only selected tests per package %q", got, want)
	}
}

func TestB1026FileSelectionFailsClosed(t *testing.T) {
	for _, c := range []struct{ name, source string }{
		{"missing_test.go", ""},
		{"helpers_test.go", "package one\nfunc helper() {}\n"},
		{"bad_test.go", "package one\nfunc TestBroken("},
		{"source.go", "package one\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			log := fakeGo(t, "0")
			t.Setenv("CODE_FORGE_TEST_RUN", "")
			t.Setenv("CODE_FORGE_RUN_ID", "")
			files := map[string]string{}
			if c.source != "" {
				files["one/"+c.name] = c.source
			}
			b1026Module(t, files)
			var out, errb bytes.Buffer
			if code := run([]string{"native/one/" + c.name}, &out, &errb); code != 2 || !strings.Contains(errb.String(), "test names") {
				t.Fatalf("exit %d, stderr %q; want named-selection refusal", code, errb.String())
			}
			if got := readLog(t, log); got != "" {
				t.Fatalf("unfiltered tests started: %s", got)
			}
		})
	}
}
