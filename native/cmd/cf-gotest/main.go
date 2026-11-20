// Command cf-gotest is code-forge's own test gate (B760; maintainer tool, not shipped). The
// repository's .code-forge.yml runs it as `go -C native run ./cmd/cf-gotest {test_files}`: the gate
// passes the selected Go files as paths from the repository root, and cf-gotest runs `go test
// -count=1` on the packages that hold them, from native/. With no file it runs every package (the
// full suite). Files outside native/ are not Go packages here and are left out.
//
// B952: the go child runs with -trimpath added to GOFLAGS. Without it Go keys each compile by the
// package's directory, so every worktree compiles every native and vendored package again; with it
// the key is the module path, and a new worktree reuses the build cache of the others.
package main

import (
	"fmt"
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

func run(args []string, stdout, stderr io.Writer) int {
	pkgs, all := packages(args)
	if !all && len(pkgs) == 0 {
		fmt.Fprintf(stdout, "cf-gotest: no Go package in the selection (%d files)\n", len(args))
		return 0
	}
	if all {
		pkgs = []string{"./..."}
	}
	argv := append([]string{"test", "-count=1", "-timeout", "75m"}, pkgs...)
	fmt.Fprintf(stdout, "cf-gotest: go %s\n", strings.Join(argv, " "))
	cmd := exec.Command("go", argv...)
	cmd.Env = goEnv(os.Environ())
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if e, ok := err.(*exec.ExitError); ok {
			return e.ExitCode()
		}
		fmt.Fprintf(stderr, "cf-gotest: %v\n", err)
		return 1
	}
	return 0
}

// goEnv is env with -trimpath in GOFLAGS (added once, the caller's other GOFLAGS kept; a caller's
// own -trimpath=false is kept too).
func goEnv(env []string) []string {
	out := make([]string, 0, len(env)+1)
	flags := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "GOFLAGS="); ok {
			flags = v
			continue
		}
		out = append(out, kv)
	}
	has := false
	for _, f := range strings.Fields(flags) {
		if name, _, _ := strings.Cut(strings.TrimLeft(f, "-"), "="); name == "trimpath" {
			has = true
		}
	}
	if !has {
		flags = strings.TrimSpace(flags + " -trimpath")
	}
	return append(out, "GOFLAGS="+flags)
}

// packages maps repository paths to the native/ module's package patterns, sorted and unique.
// No argument means every package (all is true). A file under a testdata folder belongs to the
// package that holds that folder; vendor/ is skipped.
func packages(args []string) (pkgs []string, all bool) {
	if len(args) == 0 {
		return nil, true
	}
	seen := map[string]bool{}
	for _, a := range args {
		p := filepath.ToSlash(filepath.Clean(a))
		if !strings.HasPrefix(p, "native/") || !strings.HasSuffix(p, ".go") {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(strings.TrimPrefix(p, "native/")))
		if i := strings.Index("/"+dir+"/", "/testdata/"); i >= 0 {
			dir = strings.TrimSuffix(("/" + dir + "/")[:i], "/")
			dir = strings.TrimPrefix(dir, "/")
			if dir == "" {
				dir = "."
			}
		}
		if dir == "vendor" || strings.HasPrefix(dir, "vendor/") {
			continue
		}
		pkg := "./" + dir
		if dir == "." {
			pkg = "."
		}
		if !seen[pkg] {
			seen[pkg] = true
			pkgs = append(pkgs, pkg)
		}
	}
	sort.Strings(pkgs)
	return pkgs, false
}
