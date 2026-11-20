package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// B957 tests: a throwaway git repository with its own Go module (no dependencies). The base
// commit holds the code; the working tree holds the tip's code and the new test file.

const redgreenBuggy = "package m\n\nfunc Add(a, b int) int { return a - b }\n"
const redgreenFixed = "package m\n\nfunc Add(a, b int) int { return a + b }\n"
const redgreenOldTest = "package m\n\nimport \"testing\"\n\nfunc TestOld(t *testing.T) {}\n"

func redgreenNewTest(body string) string {
	return redgreenOldTest + "\nfunc TestB1New(t *testing.T) {\n" + body + "\n}\n"
}

const redgreenAssert = "\tif Add(2, 3) != 5 {\n\t\tt.Fatal(\"Add(2, 3) != 5\")\n\t}"

// redgreenRepo makes the base commit (sub/go.mod, sub/m.go, sub/m_test.go) and returns the
// repository, the base sha, and a chdir into it.
func redgreenRepo(t *testing.T, baseCode string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GOFLAGS", "-mod=mod")
	write := func(name, body string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	write("sub/go.mod", "module example.com/m\n\ngo 1.22\n")
	write("sub/m.go", baseCode)
	write("sub/m_test.go", redgreenOldTest)
	git("add", "-A")
	git("commit", "-qm", "base")
	base := git("rev-parse", "HEAD")
	write("sub/m.go", redgreenFixed)
	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return dir, base
}

func redgreenRun(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String() + errb.String()
}

func redgreenWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestB957Proven(t *testing.T) {
	dir, base := redgreenRepo(t, redgreenBuggy)
	redgreenWrite(t, filepath.Join(dir, "sub/m_test.go"), redgreenNewTest(redgreenAssert))
	out := filepath.Join(t.TempDir(), "redgreen.json")
	code, output := redgreenRun(t, "--base", base, "--out", out, "sub/m_test.go")
	if code != 0 || !strings.Contains(output, "cf-redgreen: PROVEN") {
		t.Fatalf("exit=%d, want 0 and PROVEN: %s", code, output)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var result redgreenResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if result.Verdict != "PROVEN" || result.Base != base || len(result.Tests) != 1 {
		t.Fatalf("result=%+v, want PROVEN at %s with one new test", result, base)
	}
	if got := result.Tests[0]; got.Name != "TestB1New" || got.Base != "fail" || got.Tip != "pass" || got.Package != "./." {
		t.Errorf("test=%+v, want TestB1New fail on base, pass on tip (TestOld is not new)", got)
	}
}

func TestB957NotRed(t *testing.T) {
	// The base already had the right code: the new test passes there and proves nothing.
	dir, base := redgreenRepo(t, redgreenFixed)
	redgreenWrite(t, filepath.Join(dir, "sub/m_test.go"), redgreenNewTest(redgreenAssert))
	code, output := redgreenRun(t, "--base", base, "sub/m_test.go")
	if code != 1 || !strings.Contains(output, "cf-redgreen: NOT_RED: TestB1New passes on the base") {
		t.Fatalf("exit=%d, want 1 and NOT_RED: %s", code, output)
	}
}

func TestB957CompileRedInvalid(t *testing.T) {
	// The new test calls a function the base lacks: red by a build error, not an assertion.
	dir, base := redgreenRepo(t, redgreenBuggy)
	redgreenWrite(t, filepath.Join(dir, "sub/m.go"), redgreenFixed+"\nfunc Sub(a, b int) int { return a - b }\n")
	redgreenWrite(t, filepath.Join(dir, "sub/m_test.go"), redgreenNewTest("\tif Sub(3, 2) != 1 {\n\t\tt.Fatal(\"sub\")\n\t}"))
	code, output := redgreenRun(t, "--base", base, "sub/m_test.go")
	if code != 1 || !strings.Contains(output, "cf-redgreen: RED_INVALID: TestB1New is red on the base by build") {
		t.Fatalf("exit=%d, want 1 and RED_INVALID by build: %s", code, output)
	}
}

func TestB957NotGreen(t *testing.T) {
	dir, base := redgreenRepo(t, redgreenBuggy)
	redgreenWrite(t, filepath.Join(dir, "sub/m.go"), redgreenBuggy)
	redgreenWrite(t, filepath.Join(dir, "sub/m_test.go"), redgreenNewTest(redgreenAssert))
	code, output := redgreenRun(t, "--base", base, "sub/m_test.go")
	if code != 1 || !strings.Contains(output, "cf-redgreen: NOT_GREEN: TestB1New does not pass on the tip (fail)") {
		t.Fatalf("exit=%d, want 1 and NOT_GREEN: %s", code, output)
	}
}

func TestB957NoNewTests(t *testing.T) {
	_, base := redgreenRepo(t, redgreenBuggy)
	code, output := redgreenRun(t, "--base", base, "sub/m_test.go")
	if code != 0 || !strings.Contains(output, "cf-redgreen: NO_NEW_TESTS") {
		t.Fatalf("exit=%d, want 0 and NO_NEW_TESTS: %s", code, output)
	}
}

func TestB957TipParseErrorRefused(t *testing.T) {
	// A test file that does not parse is an error (exit 2), never NO_NEW_TESTS.
	dir, base := redgreenRepo(t, redgreenBuggy)
	redgreenWrite(t, filepath.Join(dir, "sub/m_test.go"), redgreenOldTest+"\nfunc TestB1New(t *testing.T) {\n")
	code, output := redgreenRun(t, "--base", base, "sub/m_test.go")
	if code != 2 || !strings.Contains(output, "cf-redgreen: sub/m_test.go does not parse") {
		t.Fatalf("exit=%d, want 2 and a parse error: %s", code, output)
	}
}

func TestB957ChangedHelperFileGoesToBase(t *testing.T) {
	// A changed test helper file with no new tests is copied into the base too, so the new
	// test that calls its new helper builds there and fails by assertion (PROVEN, not RED_INVALID).
	dir, base := redgreenRepo(t, redgreenBuggy)
	redgreenWrite(t, filepath.Join(dir, "sub/helper_test.go"), "package m\n\nfunc sum5() int { return Add(2, 3) }\n")
	redgreenWrite(t, filepath.Join(dir, "sub/m_test.go"), redgreenNewTest("\tif sum5() != 5 {\n\t\tt.Fatal(\"sum5\")\n\t}"))
	code, output := redgreenRun(t, "--base", base, "sub/m_test.go", "sub/helper_test.go")
	if code != 0 || !strings.Contains(output, "cf-redgreen: PROVEN") {
		t.Fatalf("exit=%d, want 0 and PROVEN: %s", code, output)
	}
}

func TestB957DirtyTipMarked(t *testing.T) {
	// The tip run uses the working tree: uncommitted changes mark the result dirty; once they
	// are committed the result names HEAD and is clean.
	dir, base := redgreenRepo(t, redgreenBuggy)
	redgreenWrite(t, filepath.Join(dir, "sub/m_test.go"), redgreenNewTest(redgreenAssert))
	read := func() redgreenResult {
		t.Helper()
		out := filepath.Join(t.TempDir(), "redgreen.json")
		if code, output := redgreenRun(t, "--base", base, "--out", out, "sub/m_test.go"); code != 0 {
			t.Fatalf("exit=%d: %s", code, output)
		}
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		var result redgreenResult
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if got := read(); !got.Dirty || got.Tip != base {
		t.Fatalf("uncommitted tip: dirty=%v tip=%s, want dirty at HEAD %s", got.Dirty, got.Tip, base)
	}
	cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "commit", "-qam", "tip")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
	head, _ := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if got := read(); got.Dirty || got.Tip != strings.TrimSpace(string(head)) {
		t.Fatalf("committed tip: dirty=%v tip=%s, want clean at %s", got.Dirty, got.Tip, head)
	}
}
