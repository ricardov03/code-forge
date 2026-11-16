// Command cf-gotest is code-forge's own test gate (B760; maintainer tool, not shipped). The
// repository's .code-forge.yml runs it as `go -C native run ./cmd/cf-gotest {test_files}`: the gate
// passes the selected Go files as paths from the repository root, and cf-gotest runs `go test
// -count=1` on the packages that hold them, from native/. With no file it runs every package (the
// full suite). Files outside native/ are not Go packages here and are left out.
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
