package gates

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ricardov03/code-forge/native/gitx"
)

// B450: the formatter batching and the gate refusals of Node's gates run.

func TestB450FormatFilesSelectsSupportedGitFiles(t *testing.T) {
	cwd := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(cwd, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = cwd
		cmd.Env = gitx.ChildEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write("src/a.ts", "x\n")
	write("-dash.js", "x\n")
	write("notes.txt", "x\n")
	write("app/X.php", "x\n")
	write("ignored.js", "x\n")
	write(".gitignore", "ignored.js\n")
	write("sub/.prettierrc", "{}\n")
	git("init", "-q")
	git("add", "-f", ".")
	if err := os.Symlink("src/a.ts", filepath.Join(cwd, "link.ts")); err != nil {
		t.Fatal(err)
	}
	git("add", "link.ts")
	write("untracked.css", "x\n")
	files, err := formatFiles(cwd, "prettier", Scope{Kind: "full"})
	if want := []string{"./-dash.js", "./src/a.ts", "./sub/.prettierrc"}; err != nil || !reflect.DeepEqual(files, want) {
		t.Fatalf("prettier full = %q err=%v, want %q", files, err, want)
	}
	files, err = formatFiles(cwd, "prettier", Scope{Kind: "changed", Changed: []string{"untracked.css", "notes.txt"}})
	if want := []string{"./untracked.css"}; err != nil || !reflect.DeepEqual(files, want) {
		t.Fatalf("prettier changed = %q err=%v, want %q", files, err, want)
	}
	files, err = formatFiles(cwd, "pint", Scope{Kind: "full"})
	if want := []string{"./app/X.php"}; err != nil || !reflect.DeepEqual(files, want) {
		t.Fatalf("pint full = %q err=%v, want %q", files, err, want)
	}
	if _, err := formatFiles(t.TempDir(), "prettier", Scope{Kind: "full"}); err == nil || !strings.HasPrefix(err.Error(), "format: cannot list Git files: ") {
		t.Fatalf("outside Git err = %v, want format: cannot list Git files", err)
	}
}

func TestB450FormatBatchesBudget(t *testing.T) {
	prefix := []string{"npx", "prettier"} // 4+9 = 13 bytes
	batches, err := formatBatches(prefix, []string{"./a.js", "./b.js", "./c.js"}, 13+14)
	want := [][]string{{"npx", "prettier", "./a.js", "./b.js"}, {"npx", "prettier", "./c.js"}}
	if err != nil || !reflect.DeepEqual(batches, want) {
		t.Fatalf("batches = %q err=%v, want %q", batches, err, want)
	}
	if _, err := formatBatches(prefix, []string{"./a-very-long-name.js"}, 20); err == nil || err.Error() != "format: command and file exceed argv byte budget: ./a-very-long-name.js" {
		t.Fatalf("over budget err = %v", err)
	}
}

func TestB450RunBatchesKeepsFirstFailure(t *testing.T) {
	cwd := t.TempDir()
	c := &Command{}
	batches := [][]string{{"/bin/sh", "-c", "echo one", "sh"}, {"/bin/sh", "-c", "echo two; exit 3", "sh"}, {"/bin/sh", "-c", "echo three; exit 4", "sh"}}
	o := runBatches(context.Background(), batches, c, cwd, "", 10000)
	if o.Get("ok") != false || o.Get("code") != 3 || o.Get("stdout") != "one\ntwo\nthree\n" {
		t.Fatalf("runBatches = %v, want the first failure (code 3) with every output", o)
	}
}

func TestB450OutputTail(t *testing.T) {
	lines := []string{}
	for i := 0; i < 12; i++ {
		lines = append(lines, string(rune('a'+i)))
	}
	if got := outputTail(strings.Join(lines, "\r\n") + "\n"); got != strings.Join(lines[2:], "\n")+"\n" {
		t.Fatalf("outputTail = %q", got)
	}
	if got := outputTail("  \n"); got != "" {
		t.Fatalf("outputTail(blank) = %q, want empty", got)
	}
}

func TestB450CustomAndKindMessages(t *testing.T) {
	configured := map[string]any{
		"custom": []any{
			map[string]any{"name": "docs", "argv": []any{"/bin/true"}, "evidence": []any{"../x"}},
			map[string]any{"name": "docs", "argv": []any{"/bin/true"}, "extra": true},
			map[string]any{"name": "test", "argv": []any{"/bin/true"}},
			"x",
		},
		"kinds": map[string]any{"code": []any{"docs", "nope", 3}, "docs": []any{"lint"}},
		"lint":  nil,
	}
	got := CustomMessages(configured)
	want := []string{
		"gates.custom[0] (docs).evidence[0] must be a non-empty relative path or glob without absolute paths or a .. segment",
		"gates.custom[1] (docs).name must be unique",
		`gates.custom[1] (docs): unknown key "extra" (an object gate has only argv and env)`,
		"gates.custom[2] (test).name must not be a fixed gate name",
		"gates.custom[3] must be a named object gate",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CustomMessages =\n%q\nwant\n%q", got, want)
	}
	kinds := KindMessages(configured)
	if want := []string{"gates.kinds.code: nope is an unknown gate name", "gates.kinds.docs: lint is a null fixed gate"}; !reflect.DeepEqual(kinds, want) {
		t.Fatalf("KindMessages = %q, want %q", kinds, want)
	}
}

func TestB450ResolveErrKeepsFormatterAndRefusesBadShapes(t *testing.T) {
	cwd := t.TempDir()
	if err := os.WriteFile(filepath.Join(cwd, "package.json"), []byte(`{"scripts":{"test":"x"},"devDependencies":{"prettier":"3"}}`), 0644); err != nil {
		t.Fatal(err)
	}
	p, err := ResolveErr(cwd, map[string]any{})
	if err != nil || p.Commands["format"] == nil || p.Commands["format"].Formatter != "prettier" {
		t.Fatalf("ResolveErr = %+v err=%v, want the detected prettier formatter kept", p.Commands["format"], err)
	}
	_, err = ResolveErr(cwd, map[string]any{"lint": map[string]any{"argv": []any{"/bin/true", "a\x00b"}}, "types": map[string]any{"argv": []any{"/bin/true"}, "env": map[string]any{"A=B": "x"}}})
	want := `gates.lint.argv must not contain a NUL byte; gates.types.env: "A=B" is not a variable name (letters, digits and _, not starting with a digit)`
	if err == nil || err.Error() != want {
		t.Fatalf("ResolveErr err = %v, want %q", err, want)
	}
}
