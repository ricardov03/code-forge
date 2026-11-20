// Command cf-redgreen is code-forge's own red→green proof for Go tests (B957; maintainer tool,
// not shipped). `code-forge proof red-green` has no Go runner, so a lane's commit text was the
// only claim that a new test was red on the base. cf-redgreen checks it:
//
//	go -C native run ./cmd/cf-redgreen --base <sha> [--out <file>] <test files...>
//
// The test files are repository paths (as cf-gotest takes them). The new tests are the Test
// functions a file has now and did not have at <sha>. cf-redgreen extracts <sha>'s copy of the Go
// module into a temp folder (git archive; the repository and its worktrees are not touched), puts
// the current test files in, and runs only the new tests there: each must fail by assertion (a
// build error or a panic proves nothing). Then it runs them in the working tree, where each must
// pass. The verdict is PROVEN (exit 0), NO_NEW_TESTS (exit 0, nothing to prove), or NOT_RED,
// RED_INVALID, NOT_GREEN (exit 1). --out writes the result as JSON to the named file. Its tip is HEAD; dirty: true
// says the working tree the tip run used had changes HEAD lacks.
package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// redgreenTest is one new test and its two outcomes ("pass", "fail", "panic", "build", "none").
type redgreenTest struct {
	Name    string `json:"name"`
	File    string `json:"file"`
	Package string `json:"package"`
	Base    string `json:"base"`
	Tip     string `json:"tip"`
}

type redgreenResult struct {
	Verdict string         `json:"verdict"`
	Base    string         `json:"base"`
	Tip     string         `json:"tip"`
	Dirty   bool           `json:"dirty,omitempty"`
	Files   []string       `json:"files"`
	Tests   []redgreenTest `json:"tests"`
	Detail  string         `json:"detail,omitempty"`
}

func run(args []string, stdout, stderr io.Writer) int {
	base, out, files, err := redgreenFlags(args)
	if err != nil {
		fmt.Fprintf(stderr, "cf-redgreen: %v\nusage: cf-redgreen --base <sha> [--out <file>] <test files...>\n", err)
		return 2
	}
	result, err := redgreenProve(base, files)
	if err != nil {
		fmt.Fprintf(stderr, "cf-redgreen: %v\n", err)
		return 2
	}
	for _, t := range result.Tests {
		fmt.Fprintf(stdout, "%s (%s): base %s, tip %s\n", t.Name, t.Package, t.Base, t.Tip)
	}
	line := "cf-redgreen: " + result.Verdict
	if result.Detail != "" {
		line += ": " + result.Detail
	}
	fmt.Fprintln(stdout, line)
	if out != "" {
		data, _ := json.MarshalIndent(result, "", "  ")
		if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
			fmt.Fprintf(stderr, "cf-redgreen: %v\n", err)
			return 2
		}
	}
	if result.Verdict == "PROVEN" || result.Verdict == "NO_NEW_TESTS" {
		return 0
	}
	return 1
}

func redgreenFlags(args []string) (base, out string, files []string, err error) {
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--base", "--out":
			if i+1 >= len(args) {
				return "", "", nil, fmt.Errorf("%s needs a value", a)
			}
			i++
			if a == "--base" {
				base = args[i]
			} else {
				out = args[i]
			}
		default:
			if strings.HasPrefix(a, "-") {
				return "", "", nil, fmt.Errorf("unknown flag %s", a)
			}
			files = append(files, a)
		}
	}
	if base == "" || len(files) == 0 {
		return "", "", nil, errors.New("--base and at least one test file are needed")
	}
	return base, out, files, nil
}

func redgreenGit(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var errb bytes.Buffer
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return string(out), nil
}

// redgreenProve finds the new tests of files and runs them on base and on the working tree.
func redgreenProve(base string, files []string) (*redgreenResult, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	topOut, err := redgreenGit(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	top, err := filepath.EvalSymlinks(strings.TrimSpace(topOut))
	if err != nil {
		return nil, err
	}
	sha, err := redgreenGit(top, "rev-parse", "--verify", base+"^{commit}")
	if err != nil {
		return nil, err
	}
	tip, err := redgreenGit(top, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, err
	}
	result := &redgreenResult{Base: strings.TrimSpace(sha), Tip: strings.TrimSpace(tip)}
	fileModule := map[string]string{} // every given test file -> its module (all go into the base copy)
	// module root (repository path) -> package dir (module path) -> tests
	type pkgTests struct{ module, pkg string }
	groups := map[pkgTests][]int{}
	for _, f := range files {
		rel, err := redgreenRel(top, cwd, f)
		if err != nil {
			return nil, err
		}
		if !strings.HasSuffix(rel, "_test.go") {
			continue
		}
		result.Files = append(result.Files, rel)
		now, err := os.ReadFile(filepath.Join(top, rel))
		if err != nil {
			return nil, err
		}
		before, _ := redgreenGit(top, "show", result.Base+":"+rel) // a new file has no base copy
		old := map[string]bool{}
		oldNames, _ := redgreenTestNames(before) // a base copy that does not parse has no tests
		for _, name := range oldNames {
			old[name] = true
		}
		module, err := redgreenModule(top, filepath.Dir(rel))
		if err != nil {
			return nil, err
		}
		fileModule[rel] = module
		pkg, _ := filepath.Rel(filepath.Join(top, module), filepath.Join(top, filepath.Dir(rel)))
		key := pkgTests{module, "./" + filepath.ToSlash(pkg)}
		nowNames, err := redgreenTestNames(string(now))
		if err != nil {
			return nil, fmt.Errorf("%s does not parse: %v", rel, err)
		}
		for _, name := range nowNames {
			if !old[name] {
				groups[key] = append(groups[key], len(result.Tests))
				result.Tests = append(result.Tests, redgreenTest{Name: name, File: rel, Package: key.pkg})
			}
		}
	}
	if len(result.Tests) == 0 {
		result.Verdict = "NO_NEW_TESTS"
		return result, nil
	}
	tmp, err := os.MkdirTemp("", "cf-redgreen-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	keys := make([]pkgTests, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].module+keys[i].pkg < keys[j].module+keys[j].pkg })
	extracted := map[string]bool{}
	for _, k := range keys {
		baseModule := filepath.Join(tmp, k.module)
		if !extracted[k.module] {
			if err := redgreenExtract(top, result.Base, k.module, tmp); err != nil {
				return nil, err
			}
			extracted[k.module] = true
			// Every given test file of the module goes in, not only those with new tests: a
			// changed helper file the new tests call must be there too.
			for _, rel := range result.Files {
				if fileModule[rel] != k.module {
					continue
				}
				data, err := os.ReadFile(filepath.Join(top, rel))
				if err != nil {
					return nil, err
				}
				if err := os.MkdirAll(filepath.Dir(filepath.Join(tmp, rel)), 0o755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(filepath.Join(tmp, rel), data, 0o644); err != nil {
					return nil, err
				}
			}
			// The tip run uses the working tree: a change there that HEAD lacks marks the result
			// dirty, so the stored tip sha is never read as the tested code.
			if status, err := redgreenGit(top, "status", "--porcelain", "--", k.module); err != nil {
				return nil, err
			} else if strings.TrimSpace(status) != "" {
				result.Dirty = true
			}
		}
		names := []string{}
		for _, i := range groups[k] {
			names = append(names, result.Tests[i].Name)
		}
		onBase := redgreenGoTest(baseModule, k.pkg, names)
		onTip := redgreenGoTest(filepath.Join(top, k.module), k.pkg, names)
		for _, i := range groups[k] {
			result.Tests[i].Base = onBase[result.Tests[i].Name]
			result.Tests[i].Tip = onTip[result.Tests[i].Name]
		}
	}
	result.Verdict = "PROVEN"
judge:
	for _, t := range result.Tests {
		switch {
		case t.Base == "pass":
			result.Verdict, result.Detail = "NOT_RED", t.Name+" passes on the base: it proves nothing"
		case t.Base != "fail":
			result.Verdict, result.Detail = "RED_INVALID", t.Name+" is red on the base by "+t.Base+", not by an assertion"
		case t.Tip != "pass":
			result.Verdict, result.Detail = "NOT_GREEN", t.Name+" does not pass on the tip ("+t.Tip+")"
		default:
			continue
		}
		break judge
	}
	return result, nil
}

// redgreenRel is f as a repository path; f is a repository path or a path from cwd.
func redgreenRel(top, cwd, f string) (string, error) {
	abs := f
	if !filepath.IsAbs(abs) {
		if _, err := os.Stat(filepath.Join(top, f)); err == nil {
			abs = filepath.Join(top, f)
		} else {
			abs = filepath.Join(cwd, f)
		}
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
		abs = filepath.Join(dir, filepath.Base(abs))
	}
	rel, err := filepath.Rel(top, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is outside the repository", f)
	}
	return filepath.ToSlash(rel), nil
}

// redgreenModule is the repository path of the Go module that holds dir.
func redgreenModule(top, dir string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(top, d, "go.mod")); err == nil {
			return d, nil
		}
		if d == "." || d == "/" {
			return "", fmt.Errorf("%s is not in a Go module", dir)
		}
	}
}

// redgreenTestNames lists the top-level `func TestXxx(t *testing.T)` of a Go source.
func redgreenTestNames(src string) ([]string, error) {
	if src == "" {
		return nil, nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), "x_test.go", src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
			continue
		}
		if star, ok := fn.Type.Params.List[0].Type.(*ast.StarExpr); ok {
			if sel, ok := star.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "T" {
				names = append(names, fn.Name.Name)
			}
		}
	}
	return names, nil
}

// redgreenExtract writes base's copy of the module folder into dest (git archive, read only).
func redgreenExtract(top, base, module, dest string) error {
	args := []string{"archive", "--format=tar", base}
	if module != "." {
		args = append(args, "--", module)
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = top
	var errb bytes.Buffer
	cmd.Stderr = &errb
	data, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("git archive: %v: %s", err, strings.TrimSpace(errb.String()))
	}
	r := tar.NewReader(bytes.NewReader(data))
	for {
		h, err := r.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Join(dest, filepath.FromSlash(h.Name))
		if !strings.HasPrefix(name, filepath.Clean(dest)+string(filepath.Separator)) {
			return fmt.Errorf("git archive: unsafe path %s", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(name, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return err
			}
			body, err := io.ReadAll(r)
			if err != nil {
				return err
			}
			if err := os.WriteFile(name, body, os.FileMode(h.Mode)&0o777|0o200); err != nil {
				return err
			}
		}
	}
}

// redgreenGoTest runs only names in pkg of the module at dir and gives each name's outcome:
// "pass", "fail" (an assertion), "panic", "build" (the package did not build) or "none".
func redgreenGoTest(dir, pkg string, names []string) map[string]string {
	cmd := exec.Command("go", "test", "-count=1", "-json", "-run", "^("+strings.Join(names, "|")+")$", pkg)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	_ = cmd.Run()
	outcome := map[string]string{}
	panicked := map[string]bool{}
	building := false
	scanner := bufio.NewScanner(&out)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		var ev struct{ Action, Test, Output string }
		if json.Unmarshal(scanner.Bytes(), &ev) != nil {
			building = true // plain text from the build (compile errors)
			continue
		}
		if ev.Action == "build-fail" || strings.Contains(ev.Output, "[build failed]") || strings.Contains(ev.Output, "[setup failed]") {
			building = true
		}
		root := strings.SplitN(ev.Test, "/", 2)[0]
		if root == "" {
			continue
		}
		if ev.Action == "output" && strings.HasPrefix(ev.Output, "panic: ") {
			panicked[root] = true
		}
		if (ev.Action == "pass" || ev.Action == "fail") && !strings.Contains(ev.Test, "/") {
			outcome[root] = ev.Action
		}
	}
	for _, name := range names {
		switch {
		case outcome[name] == "fail" && panicked[name]:
			outcome[name] = "panic"
		case outcome[name] == "" && building:
			outcome[name] = "build"
		case outcome[name] == "":
			outcome[name] = "none"
		}
	}
	return outcome
}
